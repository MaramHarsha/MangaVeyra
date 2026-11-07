package gen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/sdk/mvpack"
)

const (
	scriptPayloadVersion   = 2
	scriptMaxAttempts      = 3
	maxScriptSourceBytes   = 1 << 20
	maxScriptInputBytes    = 8 << 20
	maxScriptPayloadBytes  = maxScriptInputBytes + (4 << 10)
	maxDecisionBytes       = 64 << 10
	maxPlanUSD             = 1_000
	maxPlanGPUSeconds      = 24 * 60 * 60
	costMicrodollars       = 1_000_000
	maxPromptGenres        = 32
	maxPromptCharacters    = 256
	maxPromptCharacterText = 2_000
)

// ErrScriptJobActive is returned by EnqueueScript when the chapter already has
// a queued or running script job. The check runs in the enqueue transaction
// under the chapter row lock, so concurrent requests enqueue at most one job.
var ErrScriptJobActive = errors.New("gen: a script job is already active for this chapter")

type scriptJobPayload struct {
	Version           int            `json:"version"`
	ExpectedUpdatedAt time.Time      `json:"expected_updated_at"`
	TokenID           string         `json:"token_id"`
	RoutingMode       string         `json:"routing_mode"`
	Remote            bool           `json:"remote"`
	Input             ai.ScriptInput `json:"input"`
}

func (s *service) EnqueueScript(ctx context.Context, claimed studio.Actor, req ScriptRequest) (*jobs.Job, error) {
	if s == nil || s.queue == nil || s.auth == nil || s.text == nil || s.studio == nil {
		return nil, internalGenerateError()
	}
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.ChapterID = strings.TrimSpace(req.ChapterID)
	req.Mode = strings.TrimSpace(req.Mode)
	req.Premise = strings.TrimSpace(req.Premise)
	req.RoutingMode = strings.TrimSpace(req.RoutingMode)
	req.PinProviderID = strings.TrimSpace(req.PinProviderID)
	req.PinModelID = strings.TrimSpace(req.PinModelID)
	claimed.UserID = strings.TrimSpace(claimed.UserID)
	claimed.TokenID = strings.TrimSpace(claimed.TokenID)
	if !db.ValidID(req.ProjectID) || !db.ValidID(req.ChapterID) || !db.ValidID(claimed.UserID) ||
		(req.Mode != "premise" && req.Mode != "adapt") || req.PagesTarget < 0 || req.PagesTarget > 256 ||
		(req.RoutingMode != "" && !validRoutingMode(req.RoutingMode)) ||
		!boundedIdentifier(req.PinProviderID) || !boundedIdentifier(req.PinModelID) {
		return nil, badInputGenerateError("The script request is invalid.")
	}

	// Resolve the actor before reading any private project material or asking AI
	// to plan. SiteRole is intentionally discarded and rebuilt from storage.
	actor, _, err := s.resolveScriptActor(ctx, claimed, req.ProjectID, jobs.Cost{}, false)
	if err != nil {
		return nil, err
	}
	scope := jobs.Scope{Kind: jobs.ScopeChapter, ProjectID: req.ProjectID, ChapterID: req.ChapterID}
	chapter, err := s.studio.GetChapter(ctx, req.ChapterID)
	if err != nil {
		return nil, err
	}
	if chapter.ProjectID != req.ProjectID {
		return nil, studio.ErrForbidden
	}
	if !req.ExpectedUpdatedAt.IsZero() && !chapter.UpdatedAt.UTC().Equal(req.ExpectedUpdatedAt.UTC()) {
		return nil, studio.ErrConflict
	}
	if err := s.studio.CheckScopeWritable(ctx, scope); err != nil {
		return nil, err
	}
	project, err := s.studio.GetProject(ctx, req.ProjectID)
	if err != nil {
		return nil, err
	}
	characters, err := s.studio.ListCharacters(ctx, req.ProjectID)
	if err != nil {
		return nil, err
	}

	input := ai.ScriptInput{
		Mode:          req.Mode,
		Premise:       req.Premise,
		Genres:        promptGenres(project.Genres),
		Style:         project.Style,
		Format:        project.Format,
		Language:      project.Language,
		ChapterNumber: chapter.Number,
		ChapterTitle:  chapter.Title,
		PagesTarget:   req.PagesTarget,
		Characters:    make([]ai.ScriptCharacter, 0, min(len(characters), maxPromptCharacters)),
	}
	if input.Premise == "" {
		input.Premise = project.Premise
	}
	if req.Mode == "adapt" {
		// An explicitly supplied source wins even when it is whitespace; AI must
		// reject an empty adaptation instead of silently substituting different
		// private material. The string is otherwise preserved byte-for-byte.
		input.SourceText = req.SourceText
		if input.SourceText == "" {
			input.SourceText, err = s.studio.SourceText(ctx, req.ProjectID)
			if err != nil {
				return nil, err
			}
		}
	}
	input.Characters, err = promptCharacters(req.ProjectID, characters)
	if err != nil {
		return nil, err
	}
	input, err = normalizeScriptInput(input)
	if err != nil {
		return nil, badInputGenerateError("The script request is invalid.")
	}
	inputJSON, err := json.Marshal(input)
	if err != nil || len(inputJSON) > maxScriptInputBytes {
		return nil, badInputGenerateError("The script request is too large.")
	}

	plan, err := s.text.PlanText(ctx, ai.TextPlanRequest{
		Task: ai.TaskScript, Schema: ai.SchemaScriptV1, ActorID: actor.UserID, ProjectID: req.ProjectID,
		RoutingMode: req.RoutingMode, PinProviderID: req.PinProviderID, PinModelID: req.PinModelID, Input: inputJSON,
	})
	if err != nil {
		return nil, sanitizePlanningError(ctx, err)
	}
	if err := validateTextPlan(plan, req.RoutingMode, req.PinProviderID, req.PinModelID); err != nil {
		return nil, internalGenerateError()
	}
	// Plan.Estimate is the bound for one claimed attempt (initial call plus its
	// one repair). The queued action may make three automatic attempts, so both
	// approval and the durable job estimate use the checked full action bound.
	actionEstimate, err := scaleCost(plan.Estimate, scriptMaxAttempts)
	if err != nil {
		return nil, internalGenerateError()
	}

	// Close the planning window: token/session state, both Studio permissions,
	// and action-level approval are re-resolved immediately before enqueue.
	actor, _, err = s.resolveScriptActor(ctx, claimed, req.ProjectID, actionEstimate, plan.Remote)
	if err != nil {
		return nil, err
	}
	payload := scriptJobPayload{Version: scriptPayloadVersion, ExpectedUpdatedAt: chapter.UpdatedAt, TokenID: actor.TokenID,
		RoutingMode: plan.RoutingMode, Remote: plan.Remote, Input: input}
	payloadJSON, err := json.Marshal(payload)
	if err != nil || len(payloadJSON) > maxScriptPayloadBytes {
		return nil, badInputGenerateError("The script request is too large.")
	}
	return s.queue.Enqueue(ctx, jobs.EnqueueParams{
		Type: JobScript, Scope: scope, UserID: actor.UserID, Executor: jobs.ExecutorGo,
		ModelID: plan.ModelID, ProviderID: plan.ProviderID, Decision: plan.Decision, Input: json.RawMessage(payloadJSON),
		Estimate: actionEstimate, MaxAttempts: scriptMaxAttempts,
		Guard: func(ctx context.Context, tx *db.Tx) error {
			return guardScriptEnqueue(ctx, tx, req.ProjectID, req.ChapterID, chapter.UpdatedAt)
		},
	})
}

// guardScriptEnqueue runs inside the enqueue transaction. It locks the chapter
// row (SQLite write transactions are already serialised), rejects a chapter
// whose manuscript changed since planning and rejects a second active script
// job, so a double submit cannot pay for two generations of one chapter.
func guardScriptEnqueue(ctx context.Context, tx *db.Tx, projectID, chapterID string, revision time.Time) error {
	lock := ""
	if tx.Dialect() == db.Postgres {
		lock = " FOR UPDATE"
	}
	var current time.Time
	if err := tx.Get(ctx, &current, `SELECT updated_at FROM chapters WHERE id = ? AND project_id = ?`+lock,
		chapterID, projectID); err != nil {
		if db.IsNotFound(err) {
			return studio.ErrNotFound
		}
		return err
	}
	if !current.UTC().Equal(revision.UTC()) {
		return studio.ErrConflict
	}
	var active int
	if err := tx.Get(ctx, &active, `SELECT COUNT(*) FROM jobs WHERE chapter_id = ? AND type = ? AND state IN (?, ?)`,
		chapterID, JobScript, jobs.StateQueued, jobs.StateRunning); err != nil {
		return err
	}
	if active != 0 {
		return ErrScriptJobActive
	}
	return nil
}

func (s *service) resolveScriptActor(ctx context.Context, claimed studio.Actor, projectID string, estimate jobs.Cost, remote bool) (studio.Actor, *httpx.Token, error) {
	claimed.UserID = strings.TrimSpace(claimed.UserID)
	claimed.TokenID = strings.TrimSpace(claimed.TokenID)
	if !db.ValidID(claimed.UserID) {
		return studio.Actor{}, nil, studio.ErrForbidden
	}
	var (
		actor studio.Actor
		token *httpx.Token
	)
	if claimed.TokenID == "" {
		fresh, err := s.studio.ActorFromUser(ctx, claimed.UserID)
		if err != nil {
			return studio.Actor{}, nil, err
		}
		actor = fresh
	} else {
		if !db.ValidID(claimed.TokenID) {
			return studio.Actor{}, nil, studio.ErrForbidden
		}
		tok, user, err := s.auth.ResolveStoredToken(ctx, claimed.TokenID)
		if err != nil || tok == nil || user == nil || tok.UserID != claimed.UserID || user.ID != claimed.UserID ||
			user.Status != auth.StatusActive || !tok.HasScope("projects:write") || !tok.HasScope("generate") ||
			!tok.AllowsProject(projectID) {
			return studio.Actor{}, nil, studio.ErrForbidden
		}
		token = tok
		actor = studio.Actor{UserID: user.ID, TokenID: tok.ID}
	}
	if err := s.studio.Authorize(ctx, actor, projectID, studio.ActGenerate); err != nil {
		return studio.Actor{}, nil, err
	}
	if err := s.studio.Authorize(ctx, actor, projectID, studio.ActEditScript); err != nil {
		return studio.Actor{}, nil, err
	}
	if remote && token != nil && token.ApproveOverUSD != nil {
		threshold := *token.ApproveOverUSD
		if !finiteNonnegative(threshold) {
			return studio.Actor{}, nil, approvalGenerateError()
		}
		// Equal-to-threshold is approved; only a strict crossing requires a
		// human. Estimate is the full automatic-retry action bound.
		exceeds, err := approvalExceeded(estimate.USD, threshold)
		if err != nil || exceeds {
			return studio.Actor{}, nil, approvalGenerateError()
		}
	}
	return actor, token, nil
}

func validateTextPlan(plan ai.TextPlan, requestedMode, pinProvider, pinModel string) error {
	if plan.Task != ai.TaskScript || plan.Schema != ai.SchemaScriptV1 || !validRoutingMode(plan.RoutingMode) ||
		(requestedMode != "" && plan.RoutingMode != requestedMode) || !boundedIdentifier(plan.ProviderID) ||
		!boundedIdentifier(plan.ModelID) || plan.ProviderID == "" || plan.ModelID == "" ||
		(pinProvider != "" && plan.ProviderID != pinProvider) || (pinModel != "" && plan.ModelID != pinModel) ||
		!validCost(plan.Estimate) || plan.Estimate.USD > maxPlanUSD || plan.Estimate.GPUSeconds > maxPlanGPUSeconds {
		return errors.New("invalid text plan")
	}
	placeholder := plan.ProviderID == ai.PlaceholderID || plan.ModelID == ai.PlaceholderText
	if plan.Remote {
		if placeholder || plan.Estimate.USD <= 0 || plan.Estimate.GPUSeconds != 0 {
			return errors.New("invalid remote text plan")
		}
	} else if !placeholder || plan.ProviderID != ai.PlaceholderID || plan.ModelID != ai.PlaceholderText ||
		plan.Estimate.USD != 0 || plan.Estimate.GPUSeconds != 0 {
		return errors.New("invalid local text plan")
	}
	if len(plan.Decision) == 0 || len(plan.Decision) > maxDecisionBytes || !utf8.Valid(plan.Decision) ||
		mvpack.CheckMemberNames(plan.Decision, "") != nil {
		return errors.New("invalid plan decision")
	}
	canonical, err := mvpack.Canonicalize(plan.Decision)
	if err != nil || len(bytes.TrimSpace(canonical)) == 0 || bytes.TrimSpace(canonical)[0] != '{' {
		return errors.New("invalid plan decision")
	}
	var decision map[string]json.RawMessage
	if err := json.Unmarshal(canonical, &decision); err != nil || decision == nil {
		return errors.New("invalid plan decision")
	}
	return nil
}

func normalizeScriptInput(in ai.ScriptInput) (ai.ScriptInput, error) {
	in.Mode = strings.TrimSpace(in.Mode)
	in.Premise = strings.TrimSpace(in.Premise)
	in.Style = strings.TrimSpace(in.Style)
	in.Format = strings.TrimSpace(in.Format)
	in.Language = strings.TrimSpace(in.Language)
	in.ChapterTitle = strings.TrimSpace(in.ChapterTitle)
	if in.Mode != "premise" && in.Mode != "adapt" {
		return ai.ScriptInput{}, errors.New("invalid script mode")
	}
	if in.Mode == "premise" && (in.Premise == "" || in.SourceText != "") {
		return ai.ScriptInput{}, errors.New("invalid premise input")
	}
	if in.Mode == "adapt" && strings.TrimSpace(in.SourceText) == "" {
		return ai.ScriptInput{}, errors.New("invalid adaptation source")
	}
	if len(in.SourceText) > maxScriptSourceBytes || !utf8.ValidString(in.SourceText) ||
		utf8.RuneCountInString(in.Premise) > 16_000 || utf8.RuneCountInString(in.Style) > 240 ||
		utf8.RuneCountInString(in.Format) > 240 || utf8.RuneCountInString(in.Language) > 80 ||
		utf8.RuneCountInString(in.ChapterTitle) > 160 || in.ChapterNumber < 1 || in.ChapterNumber > 9999 ||
		in.PagesTarget < 0 || in.PagesTarget > 256 || len(in.Genres) > 32 || len(in.Characters) > 256 {
		return ai.ScriptInput{}, errors.New("script input exceeds bounds")
	}
	seenGenres := make(map[string]struct{}, len(in.Genres))
	for i := range in.Genres {
		in.Genres[i] = strings.TrimSpace(in.Genres[i])
		if in.Genres[i] == "" || utf8.RuneCountInString(in.Genres[i]) > 160 {
			return ai.ScriptInput{}, errors.New("invalid genre")
		}
		if _, exists := seenGenres[in.Genres[i]]; exists {
			return ai.ScriptInput{}, errors.New("duplicate genre")
		}
		seenGenres[in.Genres[i]] = struct{}{}
	}
	seenCharacters := make(map[string]struct{}, len(in.Characters))
	for i := range in.Characters {
		character := &in.Characters[i]
		character.ID = strings.TrimSpace(character.ID)
		character.Name = strings.TrimSpace(character.Name)
		character.Role = strings.TrimSpace(character.Role)
		character.Look = strings.TrimSpace(character.Look)
		character.Personality = strings.TrimSpace(character.Personality)
		if !db.ValidID(character.ID) || character.Name == "" || utf8.RuneCountInString(character.Name) > 160 ||
			utf8.RuneCountInString(character.Role) > 240 || utf8.RuneCountInString(character.Look) > 2000 ||
			utf8.RuneCountInString(character.Personality) > 2000 {
			return ai.ScriptInput{}, errors.New("invalid character")
		}
		if _, exists := seenCharacters[character.ID]; exists {
			return ai.ScriptInput{}, errors.New("duplicate character")
		}
		seenCharacters[character.ID] = struct{}{}
	}
	return in, nil
}

func validRoutingMode(mode string) bool {
	switch mode {
	case ai.ModeBalanced, ai.ModeBest, ai.ModeFastest, ai.ModeLocalOnly, ai.ModeFreeOnly:
		return true
	default:
		return false
	}
}

func boundedIdentifier(value string) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) == value && utf8.RuneCountInString(value) <= 240
}

func finiteNonnegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func validCost(cost jobs.Cost) bool {
	return finiteNonnegative(cost.USD) && finiteNonnegative(cost.GPUSeconds)
}

func scaleCost(cost jobs.Cost, multiplier int) (jobs.Cost, error) {
	if multiplier <= 0 || !validCost(cost) {
		return jobs.Cost{}, errors.New("invalid cost")
	}
	micro, err := usdMicrodollars(cost.USD)
	if err != nil || micro > math.MaxInt64/int64(multiplier) {
		return jobs.Cost{}, errors.New("cost overflow")
	}
	out := jobs.Cost{USD: float64(micro*int64(multiplier)) / costMicrodollars,
		GPUSeconds: cost.GPUSeconds * float64(multiplier)}
	if !validCost(out) {
		return jobs.Cost{}, errors.New("cost overflow")
	}
	return out, nil
}

func unscaleCost(aggregate jobs.Cost, multiplier int) (jobs.Cost, error) {
	if multiplier <= 0 || !validCost(aggregate) {
		return jobs.Cost{}, errors.New("invalid aggregate cost")
	}
	micro, err := usdMicrodollars(aggregate.USD)
	if err != nil || micro%int64(multiplier) != 0 {
		return jobs.Cost{}, errors.New("aggregate cost does not divide evenly")
	}
	perAttempt := jobs.Cost{USD: float64(micro/int64(multiplier)) / costMicrodollars,
		GPUSeconds: aggregate.GPUSeconds / float64(multiplier)}
	if !validCost(perAttempt) {
		return jobs.Cost{}, errors.New("invalid per-attempt cost")
	}
	roundTrip, err := scaleCost(perAttempt, multiplier)
	if err != nil || roundTrip != aggregate {
		return jobs.Cost{}, errors.New("aggregate cost does not round-trip")
	}
	return perAttempt, nil
}

func usdMicrodollars(value float64) (int64, error) {
	if !finiteNonnegative(value) || value > float64(math.MaxInt64)/costMicrodollars {
		return 0, errors.New("invalid USD cost")
	}
	scaled := value * costMicrodollars
	// Move down by one representable value before ceiling so a decimal amount
	// that lands one ULP above an integer (for example 0.89049) keeps its exact
	// microdollar value. A genuine fractional microdollar still rounds up.
	return int64(math.Ceil(math.Nextafter(scaled, math.Inf(-1)))), nil
}

func approvalExceeded(estimate, threshold float64) (bool, error) {
	estimateMicro, err := usdMicrodollars(estimate)
	if err != nil {
		return false, err
	}
	thresholdMicro, err := usdMicrodollars(threshold)
	if err != nil {
		return false, err
	}
	return estimateMicro > thresholdMicro, nil
}

func promptGenres(genres []string) []string {
	limit := min(len(genres), maxPromptGenres)
	return append([]string(nil), genres[:limit]...)
}

func promptCharacters(projectID string, characters []studio.Character) ([]ai.ScriptCharacter, error) {
	for _, character := range characters {
		if character.ProjectID != projectID {
			return nil, studio.ErrForbidden
		}
	}
	limit := min(len(characters), maxPromptCharacters)
	out := make([]ai.ScriptCharacter, 0, limit)
	for _, character := range characters[:limit] {
		out = append(out, ai.ScriptCharacter{
			ID: character.ID, Name: character.Name, Role: character.Role,
			Look:        truncateRunes(character.Look, maxPromptCharacterText),
			Personality: truncateRunes(character.Personality, maxPromptCharacterText),
		})
	}
	return out, nil
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

func badInputGenerateError(message string) error {
	return &ai.GenerateError{Code: ai.CodeBadInput, SafeMessage: message}
}

func approvalGenerateError() error {
	return &ai.GenerateError{Code: ai.CodeApproval, SafeMessage: "This action needs human approval before it can use the remote provider."}
}

func internalGenerateError() error {
	return &ai.GenerateError{Code: ai.CodeUpstream, SafeMessage: "The script job could not be prepared."}
}

func sanitizePlanningError(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return context.Canceled
	}
	var generateErr *ai.GenerateError
	if errors.As(err, &generateErr) && knownAICode(generateErr.Code) {
		return &ai.GenerateError{Code: generateErr.Code, SafeMessage: safeAIMessage(generateErr.Code, generateErr.SafeMessage),
			Retryable: generateErr.Retryable}
	}
	return internalGenerateError()
}

func knownAICode(code string) bool {
	switch code {
	case ai.CodeBadInput, ai.CodeNoProvider, ai.CodeLocalOnly, ai.CodeSpendLimit, ai.CodeProviderAuth,
		ai.CodeProviderRefuse, ai.CodeRateLimited, ai.CodeTimeout, ai.CodeBadOutput, ai.CodeUpstream, ai.CodeApproval:
		return true
	default:
		return false
	}
}

func safeAIMessage(code, supplied string) string {
	if utf8.ValidString(supplied) && strings.TrimSpace(supplied) != "" && utf8.RuneCountInString(supplied) <= 500 {
		return supplied
	}
	switch code {
	case ai.CodeBadInput:
		return "The script request is invalid."
	case ai.CodeNoProvider:
		return "No text provider is available for this script."
	case ai.CodeLocalOnly:
		return "Local-only mode blocks this remote text provider."
	case ai.CodeSpendLimit:
		return "The remote-spend limit has been reached."
	case ai.CodeProviderAuth:
		return "The text provider connection is not authorized."
	case ai.CodeProviderRefuse:
		return "The text provider declined this request."
	case ai.CodeRateLimited:
		return "The text provider is temporarily rate limited."
	case ai.CodeTimeout:
		return "The text provider timed out."
	case ai.CodeBadOutput:
		return "The text provider returned an invalid script."
	case ai.CodeApproval:
		return "This action needs human approval."
	default:
		return "The text provider could not complete this script."
	}
}
