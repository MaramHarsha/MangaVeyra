package gen

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

type scriptJobOutput struct {
	ChapterID string `json:"chapter_id"`
	Scenes    int    `json:"scenes"`
	Panels    int    `json:"panels"`
	Pages     int    `json:"pages"`
	Provider  string `json:"provider_id"`
	Model     string `json:"model_id"`
}

func (s *service) handleScript(ctx context.Context, run *jobs.Run) error {
	if s == nil || s.auth == nil || s.text == nil || s.studio == nil || run == nil || run.Job == nil {
		return internalJobError()
	}
	job := run.Job
	if job.Type != JobScript || job.Executor != jobs.ExecutorGo || !db.ValidID(job.ID) || !db.ValidID(job.UserID) ||
		job.MaxAttempts != scriptMaxAttempts || job.AttemptBase < 0 || job.RoundAttempt() < 1 || job.RoundAttempt() > scriptMaxAttempts ||
		job.Scope.Kind != jobs.ScopeChapter || job.Scope.Validate() != nil {
		return badInputJobError("The stored script job is invalid.")
	}
	payload, err := decodeScriptPayload(job.Input)
	if err != nil {
		return badInputJobError("The stored script job input is invalid.")
	}
	if job.ProviderID == ai.PlaceholderID && (payload.Remote || job.ModelID != ai.PlaceholderText) {
		return badInputJobError("The stored script route is inconsistent.")
	}
	perAttemptEstimate, err := unscaleCost(job.Estimate, scriptMaxAttempts)
	if err != nil {
		return badInputJobError("The stored script estimate is invalid.")
	}
	plan := ai.TextPlan{Task: ai.TaskScript, Schema: ai.SchemaScriptV1, RoutingMode: payload.RoutingMode,
		ProviderID: job.ProviderID, ModelID: job.ModelID, Remote: payload.Remote,
		Decision: append(json.RawMessage(nil), job.Decision...), Estimate: perAttemptEstimate}
	if err := validateTextPlan(plan, payload.RoutingMode, "", ""); err != nil || !validCost(job.Estimate) {
		return badInputJobError("The stored script route is invalid.")
	}
	actor, _, err := s.resolveScriptActor(ctx, studio.Actor{UserID: job.UserID, TokenID: payload.TokenID},
		job.Scope.ProjectID, job.Estimate, payload.Remote)
	if err != nil {
		return mapAuthorizationJobError(ctx, err)
	}
	chapter, err := s.studio.GetChapter(ctx, job.Scope.ChapterID)
	if err != nil {
		return mapStudioJobError(ctx, err)
	}
	if chapter.ProjectID != job.Scope.ProjectID {
		return badInputJobError("The script chapter no longer belongs to this project.")
	}
	// The save below is fenced on this revision. Checking it before the
	// provider call means a job that can no longer land (the manuscript was
	// edited or another job already saved) fails without paying for a call.
	if !payload.ExpectedUpdatedAt.IsZero() && !chapter.UpdatedAt.UTC().Equal(payload.ExpectedUpdatedAt.UTC()) {
		return badInputJobError("The script can no longer be saved.")
	}
	if err := s.studio.CheckScopeWritable(ctx, job.Scope); err != nil {
		return mapStudioJobError(ctx, err)
	}
	currentCharacters, err := s.studio.ListCharacters(ctx, job.Scope.ProjectID)
	if err != nil {
		return mapStudioJobError(ctx, err)
	}
	owned := make(map[string]struct{}, len(currentCharacters))
	for _, character := range currentCharacters {
		if character.ProjectID != job.Scope.ProjectID {
			return badInputJobError("The script character list is inconsistent.")
		}
		owned[character.ID] = struct{}{}
	}
	for _, character := range payload.Input.Characters {
		if _, ok := owned[character.ID]; !ok {
			return badInputJobError("A script character no longer belongs to this project.")
		}
	}
	if err := run.Progress(ctx, 0.1, 1, 3, "Preparing script"); err != nil {
		return mapRuntimeJobError(ctx, err)
	}
	inputJSON, err := json.Marshal(payload.Input)
	if err != nil || len(inputJSON) > maxScriptInputBytes {
		return badInputJobError("The stored script job input is invalid.")
	}
	result, err := s.text.GenerateText(ctx, ai.TextGenerateRequest{
		Plan: plan, ActorID: actor.UserID, JobID: job.ID, ProjectID: job.Scope.ProjectID,
		Attempt: job.Attempts, Input: inputJSON,
	})
	if err != nil {
		return mapAIJobError(ctx, run, err)
	}
	if !validCost(result.Actual) {
		return internalJobError()
	}
	run.AddActual(result.Actual)
	if result.ProviderID != plan.ProviderID || result.ModelID != plan.ModelID {
		return badInputJobError("The text provider returned an inconsistent route.")
	}
	doc, err := decodeGeneratedScript(result.Document, payload.Input, job.Scope.ChapterID)
	if err != nil {
		return &jobs.JobError{Code: ai.CodeBadOutput, Message: "The text provider returned an invalid script."}
	}
	if err := s.studio.ValidateScript(ctx, job.Scope.ProjectID, doc); err != nil {
		return mapStudioJobError(ctx, err)
	}
	if err := run.Progress(ctx, 0.8, 2, 3, "Saving script"); err != nil {
		return mapRuntimeJobError(ctx, err)
	}
	err = run.CommitSideEffect(ctx, func(commitCtx context.Context, tx *db.Tx) (any, error) {
		result, replaceErr := s.studio.ReplaceGeneratedScriptTx(commitCtx, tx, studio.ReplaceScriptInput{
			Actor: actor, ProjectID: job.Scope.ProjectID, ChapterID: job.Scope.ChapterID, Doc: doc,
			ExpectedUpdatedAt: payload.ExpectedUpdatedAt, JobID: job.ID, ProviderID: plan.ProviderID,
			ModelID: plan.ModelID, Provenance: "ai",
		})
		if replaceErr != nil {
			return nil, replaceErr
		}
		return scriptJobOutput{ChapterID: job.Scope.ChapterID, Scenes: result.Scenes, Panels: result.Panels,
			Pages: result.Pages, Provider: plan.ProviderID, Model: plan.ModelID}, nil
	})
	if err != nil {
		if errors.Is(err, jobs.ErrLeaseLost) {
			return jobs.ErrLeaseLost
		}
		return mapStudioJobError(ctx, err)
	}
	return nil
}

func mapAIJobError(ctx context.Context, run *jobs.Run, err error) error {
	var generateErr *ai.GenerateError
	if errors.As(err, &generateErr) {
		// A provider may finish billing while the request is being canceled or
		// its lease is lost. Preserve every safe actual before selecting the
		// terminal error mapping; the spend ledger remains authoritative for a
		// stale attempt whose fenced queue update can no longer land.
		if !validCost(generateErr.Actual) {
			return internalJobError()
		}
		run.AddActual(generateErr.Actual)
	}
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return &jobs.JobError{Code: jobs.ErrCodeCanceled, Message: "canceled"}
	}
	if errors.Is(err, jobs.ErrLeaseLost) {
		return jobs.ErrLeaseLost
	}
	if generateErr == nil || !knownAICode(generateErr.Code) {
		return internalJobError()
	}
	retryable := generateErr.Retryable
	if generateErr.Code == ai.CodeBadInput || generateErr.Code == ai.CodeBadOutput || generateErr.Code == ai.CodeApproval ||
		generateErr.Code == ai.CodeLocalOnly || generateErr.Code == ai.CodeNoProvider || generateErr.Code == ai.CodeSpendLimit ||
		generateErr.Code == ai.CodeProviderAuth || generateErr.Code == ai.CodeProviderRefuse {
		retryable = false
	}
	return &jobs.JobError{Code: generateErr.Code, Message: safeAIMessage(generateErr.Code, generateErr.SafeMessage), Retryable: retryable}
}

func mapRuntimeJobError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, jobs.ErrLeaseLost):
		return jobs.ErrLeaseLost
	case errors.Is(err, jobs.ErrCanceled), errors.Is(err, context.Canceled) && ctx.Err() != nil:
		return &jobs.JobError{Code: jobs.ErrCodeCanceled, Message: "canceled"}
	default:
		return internalJobError()
	}
}

func mapStudioJobError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, jobs.ErrLeaseLost):
		return jobs.ErrLeaseLost
	case errors.Is(err, jobs.ErrCanceled), errors.Is(err, context.Canceled) && ctx.Err() != nil:
		return &jobs.JobError{Code: jobs.ErrCodeCanceled, Message: "canceled"}
	case errors.Is(err, studio.ErrForbidden):
		return badInputJobError("The script job is no longer authorized.")
	case errors.Is(err, studio.ErrNotFound):
		return badInputJobError("The script target no longer exists.")
	case errors.Is(err, studio.ErrLocked):
		return badInputJobError("The script chapter is locked.")
	case errors.Is(err, studio.ErrInvalid), errors.Is(err, studio.ErrConflict):
		return badInputJobError("The script can no longer be saved.")
	case db.IsTransient(err):
		// A deadlock or serialization failure rolled the save back; the
		// attempt can safely run again instead of failing for good.
		return &jobs.JobError{Code: jobs.ErrCodeInternal, Message: "The script save was interrupted; it will be retried.", Retryable: true}
	default:
		return internalJobError()
	}
}

func mapAuthorizationJobError(ctx context.Context, err error) error {
	if errors.Is(err, jobs.ErrCanceled) || errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return &jobs.JobError{Code: jobs.ErrCodeCanceled, Message: "canceled"}
	}
	var generateErr *ai.GenerateError
	if errors.As(err, &generateErr) && knownAICode(generateErr.Code) {
		return &jobs.JobError{Code: generateErr.Code, Message: safeAIMessage(generateErr.Code, generateErr.SafeMessage)}
	}
	return mapStudioJobError(ctx, err)
}

func badInputJobError(message string) *jobs.JobError {
	return &jobs.JobError{Code: jobs.ErrCodeBadInput, Message: message}
}

func internalJobError() *jobs.JobError {
	return &jobs.JobError{Code: jobs.ErrCodeInternal, Message: "The script job could not be completed."}
}
