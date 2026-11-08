// Package jobs is the job queue behind every generation, export and batch
// action: enqueue with scope validation (the studio plugs its lock check in
// here), fair-share claiming (per-user running limits, priorities, night-mode
// batch deferral), leases with expiry requeue, retries with backoff, router
// fallback, cancellation, an event log, a Go executor pool for in-process
// handlers and the pull-based worker HTTP API (/api/worker/v1, see
// sdk/api/worker.md). Every state change is published on the WebSocket
// topics job:{id}, user:{id}, project:{id} and admin:queue.
package jobs

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// Job states.
const (
	StateQueued   = "queued"
	StateRunning  = "running"
	StateDone     = "done"
	StateFailed   = "failed"
	StateCanceled = "canceled"
)

// Executors.
const (
	ExecutorGo     = "go"     // in-process Go handler (remote APIs, text LLMs, exports, compositing)
	ExecutorWorker = "worker" // Python worker pulling over HTTP
)

// Scope kinds. Every AI action targets exactly one of chapter, page, panel,
// layer or region; project and none are for non-AI jobs (exports, backups).
const (
	ScopeChapter = "chapter"
	ScopePage    = "page"
	ScopePanel   = "panel"
	ScopeLayer   = "layer"
	ScopeRegion  = "region"
	ScopeProject = "project"
	ScopeNone    = "none"
)

// Scope is what a job may change. Nothing outside it is modified.
type Scope struct {
	Kind      string `json:"kind"`
	ProjectID string `json:"project_id,omitempty"`
	ChapterID string `json:"chapter_id,omitempty"`
	PageID    string `json:"page_id,omitempty"`
	PanelID   string `json:"panel_id,omitempty"`
	LayerID   string `json:"layer_id,omitempty"`
	// Region is the edit region (mask asset, polygon or box) for region scope.
	// A region targets LayerID; its page/panel ancestry is resolved server-side.
	Region json.RawMessage `json:"region,omitempty"`
}

// Validate checks that the scope names exactly one target and the ids it needs.
func (s Scope) Validate() error {
	need := func(v, name string) error {
		if v == "" {
			return fmt.Errorf("jobs: %s scope needs %s", s.Kind, name)
		}
		return nil
	}
	forbid := func(set bool, name string) error {
		if set {
			return fmt.Errorf("jobs: %s scope forbids %s", s.Kind, name)
		}
		return nil
	}
	region := bytes.TrimSpace(s.Region)
	regionSet := len(region) != 0
	needRegion := func() error {
		if !regionSet || bytes.Equal(region, []byte("null")) {
			return errors.New("jobs: region scope needs region")
		}
		if !json.Valid(region) || region[0] != '{' {
			return errors.New("jobs: region scope needs a JSON object")
		}
		return nil
	}
	switch s.Kind {
	case ScopeChapter:
		return errors.Join(
			need(s.ProjectID, "project_id"),
			need(s.ChapterID, "chapter_id"),
			forbid(s.PageID != "", "page_id"),
			forbid(s.PanelID != "", "panel_id"),
			forbid(s.LayerID != "", "layer_id"),
			forbid(regionSet, "region"),
		)
	case ScopePage:
		return errors.Join(
			need(s.ProjectID, "project_id"),
			need(s.PageID, "page_id"),
			forbid(s.ChapterID != "", "chapter_id"),
			forbid(s.PanelID != "", "panel_id"),
			forbid(s.LayerID != "", "layer_id"),
			forbid(regionSet, "region"),
		)
	case ScopePanel:
		return errors.Join(
			need(s.ProjectID, "project_id"),
			need(s.PageID, "page_id"),
			need(s.PanelID, "panel_id"),
			forbid(s.ChapterID != "", "chapter_id"),
			forbid(s.LayerID != "", "layer_id"),
			forbid(regionSet, "region"),
		)
	case ScopeLayer:
		return errors.Join(
			need(s.ProjectID, "project_id"),
			need(s.PanelID, "panel_id"),
			need(s.LayerID, "layer_id"),
			forbid(s.ChapterID != "", "chapter_id"),
			forbid(s.PageID != "", "page_id"),
			forbid(regionSet, "region"),
		)
	case ScopeRegion:
		return errors.Join(
			need(s.ProjectID, "project_id"),
			need(s.LayerID, "layer_id"),
			needRegion(),
			forbid(s.ChapterID != "", "chapter_id"),
			forbid(s.PageID != "", "page_id"),
			forbid(s.PanelID != "", "panel_id"),
		)
	case ScopeProject:
		return errors.Join(
			need(s.ProjectID, "project_id"),
			forbid(s.ChapterID != "", "chapter_id"),
			forbid(s.PageID != "", "page_id"),
			forbid(s.PanelID != "", "panel_id"),
			forbid(s.LayerID != "", "layer_id"),
			forbid(regionSet, "region"),
		)
	case ScopeNone:
		return errors.Join(
			forbid(s.ProjectID != "", "project_id"),
			forbid(s.ChapterID != "", "chapter_id"),
			forbid(s.PageID != "", "page_id"),
			forbid(s.PanelID != "", "panel_id"),
			forbid(s.LayerID != "", "layer_id"),
			forbid(regionSet, "region"),
		)
	case "":
		return errors.New("jobs: scope kind is required")
	}
	return fmt.Errorf("jobs: unknown scope kind %q", s.Kind)
}

// Cost is an estimate or actual cost: local GPU time and/or remote spend on the user's own keys.
type Cost struct {
	GPUSeconds float64 `json:"gpu_seconds,omitempty"`
	USD        float64 `json:"usd,omitempty"`
}

// Add returns c + o.
func (c Cost) Add(o Cost) Cost {
	return Cost{GPUSeconds: c.GPUSeconds + o.GPUSeconds, USD: c.USD + o.USD}
}

// Error codes reported by workers and handlers.
const (
	ErrCodeOOM      = "oom"
	ErrCodeTimeout  = "timeout"
	ErrCodeSafety   = "safety"
	ErrCodeBadInput = "bad_input"
	ErrCodeCanceled = "canceled"
	ErrCodeInternal = "internal"
)

// JobError is why a job (attempt) failed.
type JobError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// Error implements error.
func (e *JobError) Error() string { return e.Code + ": " + e.Message }

// Retryable wraps err as a retryable failure (e.g. a remote timeout).
func Retryable(code string, err error) *JobError {
	return &JobError{Code: code, Message: err.Error(), Retryable: true}
}

// Permanent wraps err as a failure that must not be retried (e.g. bad input).
func Permanent(code string, err error) *JobError {
	return &JobError{Code: code, Message: err.Error(), Retryable: false}
}

// Job is one queued unit of work.
type Job struct {
	ID          string            `json:"id"`
	Type        string            `json:"type"`
	Scope       Scope             `json:"scope"`
	UserID      string            `json:"user_id,omitempty"`
	State       string            `json:"state"`
	Progress    float64           `json:"progress"`
	Step        int               `json:"step"`
	Steps       int               `json:"steps"`
	Message     string            `json:"message,omitempty"`
	Executor    string            `json:"executor"`
	ModelID     string            `json:"model_id,omitempty"`
	ProviderID  string            `json:"provider_id,omitempty"`
	Decision    json.RawMessage   `json:"decision,omitempty"`
	Input       json.RawMessage   `json:"input,omitempty"`
	Files       map[string]string `json:"files,omitempty"`
	Output      json.RawMessage   `json:"output,omitempty"`
	Error       *JobError         `json:"error,omitempty"`
	Estimate    Cost              `json:"estimate"`
	Actual      Cost              `json:"actual"`
	Attempts    int               `json:"attempts"`
	MaxAttempts int               `json:"max_attempts"`
	// AttemptBase is the value of Attempts when the current round began (a
	// manual retry starts a new round without reusing attempt numbers).
	AttemptBase     int        `json:"attempt_base"`
	ParentID        string     `json:"parent_id,omitempty"`
	Priority        int        `json:"priority"`
	Batch           bool       `json:"batch"`
	WorkerID        string     `json:"worker_id,omitempty"`
	LeaseExpiresAt  *time.Time `json:"lease_expires_at,omitempty"`
	RunAfter        *time.Time `json:"run_after,omitempty"`
	CancelRequested bool       `json:"cancel_requested"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	// Deduplicated is set on the job Enqueue returns when EnqueueParams.DedupeKey
	// matched an active job, so no new job was stored. It is not persisted.
	Deduplicated bool `json:"-"`
}

// RoundAttempt is the attempt number within the current round (1 for the
// first claim after enqueue or a manual retry). Attempts itself never resets,
// so it stays a unique lease identity across retries.
func (j *Job) RoundAttempt() int { return j.Attempts - j.AttemptBase }

// Finished reports whether the job reached a terminal state.
func (j *Job) Finished() bool {
	return j.State == StateDone || j.State == StateFailed || j.State == StateCanceled
}

// DecodeInput unmarshals the job input into v.
func (j *Job) DecodeInput(v any) error {
	if len(j.Input) == 0 {
		return nil
	}
	return json.Unmarshal(j.Input, v)
}

// DecodeOutput unmarshals the job output into v.
func (j *Job) DecodeOutput(v any) error {
	if len(j.Output) == 0 {
		return nil
	}
	return json.Unmarshal(j.Output, v)
}

const jobCols = `id, type, scope, user_id, state, progress, step, steps, message, executor, model_id, provider_id,
	decision, input, files, output, error, estimate, actual, attempts, max_attempts, attempt_base, parent_id, priority, batch,
	worker_id, lease_expires_at, run_after, cancel_requested, created_at, updated_at, started_at, finished_at`

type jobRow struct {
	ID              string                     `db:"id"`
	Type            string                     `db:"type"`
	Scope           db.JSON[Scope]             `db:"scope"`
	UserID          sql.NullString             `db:"user_id"`
	State           string                     `db:"state"`
	Progress        float64                    `db:"progress"`
	Step            int                        `db:"step"`
	Steps           int                        `db:"steps"`
	Message         string                     `db:"message"`
	Executor        string                     `db:"executor"`
	ModelID         string                     `db:"model_id"`
	ProviderID      string                     `db:"provider_id"`
	Decision        string                     `db:"decision"`
	Input           string                     `db:"input"`
	Files           db.JSON[map[string]string] `db:"files"`
	Output          string                     `db:"output"`
	Error           string                     `db:"error"`
	Estimate        db.JSON[Cost]              `db:"estimate"`
	Actual          db.JSON[Cost]              `db:"actual"`
	Attempts        int                        `db:"attempts"`
	MaxAttempts     int                        `db:"max_attempts"`
	AttemptBase     int                        `db:"attempt_base"`
	ParentID        sql.NullString             `db:"parent_id"`
	Priority        int                        `db:"priority"`
	Batch           bool                       `db:"batch"`
	WorkerID        sql.NullString             `db:"worker_id"`
	LeaseExpiresAt  sql.NullTime               `db:"lease_expires_at"`
	RunAfter        sql.NullTime               `db:"run_after"`
	CancelRequested bool                       `db:"cancel_requested"`
	CreatedAt       time.Time                  `db:"created_at"`
	UpdatedAt       time.Time                  `db:"updated_at"`
	StartedAt       sql.NullTime               `db:"started_at"`
	FinishedAt      sql.NullTime               `db:"finished_at"`
}

func rawOrNil(s string) json.RawMessage {
	if s == "" || s == "{}" || s == "null" {
		return nil
	}
	return json.RawMessage(s)
}

func timePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

func (r *jobRow) job() *Job {
	j := &Job{
		ID: r.ID, Type: r.Type, Scope: r.Scope.V, UserID: r.UserID.String, State: r.State, Progress: r.Progress,
		Step: r.Step, Steps: r.Steps, Message: r.Message, Executor: r.Executor, ModelID: r.ModelID, ProviderID: r.ProviderID,
		Decision: rawOrNil(r.Decision), Input: rawOrNil(r.Input), Files: r.Files.V, Output: rawOrNil(r.Output),
		Estimate: r.Estimate.V, Actual: r.Actual.V, Attempts: r.Attempts, MaxAttempts: r.MaxAttempts, AttemptBase: r.AttemptBase, ParentID: r.ParentID.String,
		Priority: r.Priority, Batch: r.Batch, WorkerID: r.WorkerID.String, LeaseExpiresAt: timePtr(r.LeaseExpiresAt),
		RunAfter: timePtr(r.RunAfter), CancelRequested: r.CancelRequested, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
		StartedAt: timePtr(r.StartedAt), FinishedAt: timePtr(r.FinishedAt),
	}
	if r.Error != "" && r.Error != "{}" {
		var e JobError
		if json.Unmarshal([]byte(r.Error), &e) == nil && e.Code != "" {
			j.Error = &e
		}
	}
	if j.Input == nil && r.Input == "{}" {
		j.Input = json.RawMessage("{}")
	}
	return j
}

// Summary is the compact form published over WebSocket.
type Summary struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"`
	State      string    `json:"state"`
	Progress   float64   `json:"progress"`
	Step       int       `json:"step"`
	Steps      int       `json:"steps"`
	Message    string    `json:"message,omitempty"`
	ModelID    string    `json:"model_id,omitempty"`
	ProviderID string    `json:"provider_id,omitempty"`
	Scope      Scope     `json:"scope"`
	UserID     string    `json:"user_id,omitempty"`
	Estimate   Cost      `json:"estimate"`
	Actual     Cost      `json:"actual"`
	Error      *JobError `json:"error,omitempty"`
	Attempts   int       `json:"attempts"`
	UpdatedAt  time.Time `json:"updated_at"`
	// Preview is an optional data: URL of an in-progress preview image.
	Preview string `json:"preview,omitempty"`
}

// Summary returns the compact form of j. It is broadcast to the owner's,
// project's and admins' topics, so like PublicView it carries no progress
// text and only the public failure code: raw worker/provider messages can
// contain prompts or credentials.
func (j *Job) Summary() Summary {
	s := Summary{ID: j.ID, Type: j.Type, State: j.State, Progress: j.Progress, Step: j.Step, Steps: j.Steps,
		ModelID: j.ModelID, ProviderID: j.ProviderID, Scope: j.Scope, UserID: j.UserID, Estimate: j.Estimate, Actual: j.Actual,
		Attempts: j.Attempts, UpdatedAt: j.UpdatedAt}
	if j.Error != nil {
		s.Error = &JobError{Code: PublicErrorCode(j.Error.Code), Retryable: j.Error.Retryable}
	}
	return s
}

// Event is one entry of a job's log.
type Event struct {
	ID        string          `db:"id" json:"id"`
	JobID     string          `db:"job_id" json:"job_id"`
	Kind      string          `db:"kind" json:"kind"`
	Message   string          `db:"message" json:"message"`
	Data      json.RawMessage `db:"-" json:"data,omitempty"`
	RawData   string          `db:"data" json:"-"`
	CreatedAt time.Time       `db:"created_at" json:"created_at"`
}
