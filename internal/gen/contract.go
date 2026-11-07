package gen

// Frozen Phase 1 contract (docs/plans/0002-phase-1-first-playable.md).
// Screens enqueue generation through core.MustUse[gen.Service](d); the work
// runs as Go-executor jobs registered by this module (JobHandlers).

import (
	"context"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// Job types.
const (
	JobScript          = "script.generate"
	JobCharacterSheet  = "character.sheet"
	JobPagesFromScript = "pages.from_script"
	JobPanel           = "panel.generate"
	JobRegionEdit      = "region.edit"
	JobLetterPage      = "page.letter"
)

// ScriptRequest generates or adapts a chapter script.
type ScriptRequest struct {
	ProjectID string `json:"project_id"`
	ChapterID string `json:"chapter_id"`
	// Mode is "premise" or "adapt".
	Mode        string `json:"mode"`
	Premise     string `json:"premise,omitempty"`
	SourceText  string `json:"source_text,omitempty"`
	PagesTarget int    `json:"pages_target,omitempty"`
	RoutingMode string `json:"routing_mode,omitempty"`
	// Pins force the Gate A placeholder/OpenRouter provider or model after all
	// authorization, local-only and availability checks still pass.
	PinProviderID string `json:"pin_provider_id,omitempty"`
	PinModelID    string `json:"pin_model_id,omitempty"`
	// ExpectedUpdatedAt, when non-zero, is the chapter revision the caller
	// validated (for example "this chapter has no script yet"). Enqueue fails
	// with studio.ErrConflict when the chapter changed since, atomically with
	// the job insert, so a caller's check-then-act cannot race a save.
	ExpectedUpdatedAt time.Time `json:"expected_updated_at,omitempty"`
}

// CharacterSheetRequest generates reference sheets for a character.
type CharacterSheetRequest struct {
	ProjectID   string `json:"project_id"`
	CharacterID string `json:"character_id"`
	// Count is the number of candidate sheets (1–4).
	Count       int    `json:"count"`
	Style       string `json:"style,omitempty"`
	RoutingMode string `json:"routing_mode,omitempty"`
}

// PagesFromScriptRequest lays out pages for a chapter from its script.
type PagesFromScriptRequest struct {
	ProjectID string `json:"project_id"`
	ChapterID string `json:"chapter_id"`
	// Replace deletes existing unlocked pages first (default: append only when the chapter has no pages).
	Replace bool `json:"replace"`
	// Letter places bubbles from the script dialogue.
	Letter bool `json:"letter"`
	// Generate queues panel art for every new panel.
	Generate    bool   `json:"generate"`
	RoutingMode string `json:"routing_mode,omitempty"`
}

// PanelRequest generates art for a panel (scope "panel") or one layer (scope "layer").
type PanelRequest struct {
	ProjectID string `json:"project_id"`
	PageID    string `json:"page_id"`
	PanelID   string `json:"panel_id"`
	// LayerID limits regeneration to one layer (character or background).
	LayerID string `json:"layer_id,omitempty"`
	// Variations is how many candidates to make (1–4). With more than one the
	// results are kept as candidates until one is chosen with ApplyVariation.
	Variations  int    `json:"variations"`
	Prompt      string `json:"prompt,omitempty"` // overrides the stored panel prompt
	RoutingMode string `json:"routing_mode,omitempty"`
	PinModelID  string `json:"pin_model_id,omitempty"`
}

// RegionEditRequest changes only a masked region of a panel image.
type RegionEditRequest struct {
	ProjectID string `json:"project_id"`
	LayerID   string `json:"layer_id"`
	// Page/panel/chapter ancestry is resolved from LayerID server-side so a
	// client cannot spoof a less-restricted parent scope.
	// MaskAssetID is a PNG mask (white = change) in the layer image's pixel space.
	MaskAssetID string `json:"mask_asset_id"`
	Prompt      string `json:"prompt"`
	Variations  int    `json:"variations"`
	RoutingMode string `json:"routing_mode,omitempty"`
}

// Candidate is one generated variation waiting to be chosen.
type Candidate struct {
	Index     int      `json:"index"`
	AssetID   string   `json:"asset_id"`
	URL       string   `json:"url,omitempty"`
	Seed      int64    `json:"seed"`
	LockScore *float64 `json:"lock_score,omitempty"`
}

// Service enqueues generation jobs. Every method authorizes the actor,
// validates scope and locks, plans routing (so the job records the model and
// estimate) and returns the queued job.
type Service interface {
	EnqueueScript(ctx context.Context, actor studio.Actor, req ScriptRequest) (*jobs.Job, error)
	EnqueueCharacterSheet(ctx context.Context, actor studio.Actor, req CharacterSheetRequest) (*jobs.Job, error)
	EnqueuePagesFromScript(ctx context.Context, actor studio.Actor, req PagesFromScriptRequest) (*jobs.Job, error)
	EnqueuePanel(ctx context.Context, actor studio.Actor, req PanelRequest) (*jobs.Job, error)
	EnqueueRegionEdit(ctx context.Context, actor studio.Actor, req RegionEditRequest) (*jobs.Job, error)
	// Candidates returns the variations produced by a finished job.
	Candidates(ctx context.Context, actor studio.Actor, jobID string) ([]Candidate, error)
	// ApplyVariation writes candidate index of a finished job into its layer.
	ApplyVariation(ctx context.Context, actor studio.Actor, jobID string, index int) error
}
