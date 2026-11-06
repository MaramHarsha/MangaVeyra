package ai

// This file is the frozen Phase 1 contract other modules code against
// (docs/plans/0002-phase-1-first-playable.md). Implementations live in this
// package; consumers obtain them with core.MustUse[ai.TextService](d) and
// core.MustUse[ai.ImageService](d).

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// Routing modes.
const (
	ModeBalanced  = "balanced"
	ModeBest      = "best"
	ModeFastest   = "fastest"
	ModeLocalOnly = "local_only"
	ModeFreeOnly  = "free_only"
)

// Text tasks and document schemas.
const (
	TaskScript      = "script.generate" // premise or source text → mangaveyra.script.v1
	TaskCharacter   = "character.describe"
	SchemaScriptV1  = "mangaveyra.script.v1"
	SchemaFreeText  = "text"
	PlaceholderID   = "placeholder" // built-in provider id (always present, never remote)
	PlaceholderText = "placeholder-text"
	PlaceholderArt  = "placeholder-art"
)

// Image tasks.
const (
	TaskPanel          = "panel.generate"  // new panel or layer art
	TaskCharacterSheet = "character.sheet" // reference sheet from a description
	TaskRegionEdit     = "region.edit"     // inpaint the masked region of an image
	TaskCover          = "cover.generate"
)

// TextService plans and runs structured text generation.
type TextService interface {
	// PlanText picks a provider and model and estimates cost. It never holds a
	// decrypted key and makes no network calls.
	PlanText(ctx context.Context, req TextPlanRequest) (TextPlan, error)
	// GenerateText treats the plan as untrusted: it re-resolves the provider and
	// key, rechecks local-only and spend limits before every outbound call,
	// validates the document against req.Plan.Schema (at most one repair
	// attempt) and returns it.
	GenerateText(ctx context.Context, req TextGenerateRequest) (TextResult, error)
}

// TextPlanRequest asks for a text plan.
type TextPlanRequest struct {
	Task, Schema, ActorID, ProjectID, RoutingMode string
	// PinProviderID/PinModelID force a choice (the plan still validates it).
	PinProviderID, PinModelID string
	Input                     json.RawMessage
}

// TextPlan is a routing decision for one text job.
type TextPlan struct {
	Task       string          `json:"task"`
	Schema     string          `json:"schema"`
	ProviderID string          `json:"provider_id"`
	ModelID    string          `json:"model_id"`
	Remote     bool            `json:"remote"`
	Decision   json.RawMessage `json:"decision"`
	Estimate   jobs.Cost       `json:"estimate"`
}

// TextGenerateRequest runs a plan. Input is the task input (for TaskScript see ScriptInput).
type TextGenerateRequest struct {
	Plan           TextPlan
	ActorID, JobID string
	ProjectID      string
	Attempt        int
	Input          json.RawMessage
}

// ScriptInput is the input document for TaskScript.
type ScriptInput struct {
	// Mode is "premise" (write from a premise) or "adapt" (turn SourceText into a script).
	Mode          string   `json:"mode"`
	Premise       string   `json:"premise"`
	SourceText    string   `json:"source_text,omitempty"`
	Genres        []string `json:"genres,omitempty"`
	Style         string   `json:"style,omitempty"`
	Format        string   `json:"format,omitempty"`
	Language      string   `json:"language,omitempty"`
	ChapterNumber int      `json:"chapter_number"`
	ChapterTitle  string   `json:"chapter_title,omitempty"`
	PagesTarget   int      `json:"pages_target,omitempty"`
	// Characters is the allow-list the script may reference by ID.
	Characters []ScriptCharacter `json:"characters,omitempty"`
}

// ScriptCharacter is one allowed character for script generation.
type ScriptCharacter struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Role        string `json:"role,omitempty"`
	Look        string `json:"look,omitempty"`
	Personality string `json:"personality,omitempty"`
}

// TextResult is a validated generated document.
type TextResult struct {
	Document   json.RawMessage `json:"document"`
	ProviderID string          `json:"provider_id"`
	ModelID    string          `json:"model_id"`
	Actual     jobs.Cost       `json:"actual"`
}

// ImageService plans and runs image generation and edits.
type ImageService interface {
	PlanImage(ctx context.Context, req ImagePlanRequest) (ImagePlan, error)
	// GenerateImage runs the plan. Remote and placeholder providers run inline;
	// local-worker plans enqueue a child worker job (executor "worker") and wait
	// for it, so callers always get bytes back.
	GenerateImage(ctx context.Context, req ImageGenerateRequest) (ImageResult, error)
}

// ImageNeeds describes what a job requires so the router can filter models.
type ImageNeeds struct {
	References int  `json:"references"`
	TextInArt  bool `json:"text_in_art"`
	BW         bool `json:"bw"`
	Width      int  `json:"width"`
	Height     int  `json:"height"`
	Edit       bool `json:"edit"`
	Mask       bool `json:"mask"`
	Count      int  `json:"count"`
}

// ImagePlanRequest asks for an image plan.
type ImagePlanRequest struct {
	Task, ActorID, ProjectID, RoutingMode string
	PinProviderID, PinModelID             string
	Needs                                 ImageNeeds
	// PublicSeries enables the licence guard (non-commercial models are skipped).
	PublicSeries bool
}

// ImagePlan is a routing decision for one image job.
type ImagePlan struct {
	Task       string `json:"task"`
	ProviderID string `json:"provider_id"`
	ModelID    string `json:"model_id"`
	Remote     bool   `json:"remote"`
	// Executor is "go" (remote or placeholder, run inline) or "worker" (local GPU worker).
	Executor string          `json:"executor"`
	Decision json.RawMessage `json:"decision"`
	Estimate jobs.Cost       `json:"estimate"`
	// Explain is a one-line human explanation for the routing chip, e.g.
	// "Auto → Nano Banana 2 · OpenRouter · ~$0.04".
	Explain string `json:"explain"`
}

// ImageRef is an input image (reference, init image or mask).
type ImageRef struct {
	Data        []byte `json:"-"`
	ContentType string `json:"content_type"`
	// Role is "reference", "init", "mask", "pose" or "sketch".
	Role string `json:"role"`
	// Label describes a reference ("Rin Asagiri · front").
	Label string `json:"label,omitempty"`
}

// ImageGenerateRequest runs an image plan.
type ImageGenerateRequest struct {
	Plan           ImagePlan
	ActorID, JobID string
	ProjectID      string
	Attempt        int
	Prompt         string
	Negative       string
	Width, Height  int
	Seed           int64
	// Count is the number of images (variations), 1–4.
	Count      int
	Style      string
	References []ImageRef
	Init       *ImageRef
	Mask       *ImageRef
}

// GeneratedImage is one output image.
type GeneratedImage struct {
	Data        []byte `json:"-"`
	ContentType string `json:"content_type"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Seed        int64  `json:"seed"`
}

// ImageResult holds the generated images.
type ImageResult struct {
	Images     []GeneratedImage `json:"images"`
	ProviderID string           `json:"provider_id"`
	ModelID    string           `json:"model_id"`
	Actual     jobs.Cost        `json:"actual"`
}

// GenerateError is a safe, user-presentable failure. Error() exposes only
// SafeMessage; raw upstream bodies, headers, URLs and keys are never included.
type GenerateError struct {
	Code        string
	SafeMessage string
	Retryable   bool
	Actual      jobs.Cost
	Err         error
}

func (e *GenerateError) Error() string { return e.SafeMessage }
func (e *GenerateError) Unwrap() error { return e.Err }

// Error codes.
const (
	CodeNoProvider     = "no_provider"      // nothing can serve the task
	CodeLocalOnly      = "local_only"       // remote provider blocked by local-only mode
	CodeSpendLimit     = "spend_limit"      // monthly remote spend cap reached
	CodeProviderAuth   = "provider_auth"    // key rejected
	CodeProviderRefuse = "provider_refused" // safety or policy refusal
	CodeRateLimited    = "rate_limited"
	CodeTimeout        = "timeout"
	CodeBadOutput      = "bad_output" // invalid document after the repair attempt
	CodeUpstream       = "upstream"   // other provider failure
	CodeApproval       = "approval_required"
)

// ErrNoProvider is wrapped by GenerateError{Code: CodeNoProvider}.
var ErrNoProvider = errors.New("ai: no provider can serve this task")
