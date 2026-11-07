package gen

import (
	"context"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

const (
	JobPage       = "page.generate"
	JobRepair     = "character.repair"
	JobTrain      = "character.lock_train"
	JobScore      = "character.lock_score"
	LockThreshold = 0.90
)

// PageRequest regenerates only the named page's art. SketchAssetID optionally
// supplies a creator-uploaded composition; existing polygons/text are retained.
type PageRequest struct {
	ProjectID       string  `json:"project_id"`
	PageID          string  `json:"page_id"`
	ExpectedVersion int     `json:"expected_version"`
	RoutingMode     string  `json:"routing_mode,omitempty"`
	SketchAssetID   string  `json:"sketch_asset_id,omitempty"`
	Strength        float64 `json:"strength,omitempty"`
	Finish          string  `json:"finish,omitempty"`
}

type RepairTarget struct {
	PageID  string `json:"page_id"`
	PanelID string `json:"panel_id"`
	LayerID string `json:"layer_id"`
	Version int    `json:"version"`
}
type RepairRequest struct {
	ProjectID   string         `json:"project_id"`
	ChapterID   string         `json:"chapter_id"`
	CharacterID string         `json:"character_id"`
	Targets     []RepairTarget `json:"targets"`
}

// LockStatus contains current, attributable evidence, never a simulated score.
type LockStatus struct {
	ReferenceFingerprint string       `json:"reference_fingerprint,omitempty"`
	JobID                string       `json:"job_id,omitempty"`
	Progress             float64      `json:"progress"`
	Unscored             int          `json:"unscored"`
	OutfitPending        int          `json:"outfit_pending"`
	CharacterID          string       `json:"character_id"`
	Status               string       `json:"status"`
	Method               string       `json:"method,omitempty"`
	Score                *float64     `json:"score,omitempty"`
	RecentScores         []float64    `json:"recent_scores,omitempty"`
	DriftTotal           int          `json:"drift_total"`
	Panels               int          `json:"panels"`
	References           int          `json:"references"`
	Threshold            float64      `json:"threshold"`
	CanTrain             bool         `json:"can_train"`
	CanScore             bool         `json:"can_score"`
	TrainingModel        string       `json:"training_model,omitempty"`
	TrainingJobID        string       `json:"training_job_id,omitempty"`
	TrainingProgress     float64      `json:"training_progress"`
	Drift                []DriftPanel `json:"drift"`
}
type DriftPanel struct {
	Locked bool `json:"locked"`
	RepairTarget
	ChapterID   string   `json:"chapter_id"`
	PageNumber  int      `json:"page_number"`
	PanelNumber int      `json:"panel_number"`
	AssetID     string   `json:"asset_id"`
	URL         string   `json:"url,omitempty"`
	Score       float64  `json:"score"`
	Identity    float64  `json:"identity"`
	Outfit      *float64 `json:"outfit,omitempty"`
}

// ControlService extends the first-playable service without widening its mocks.
// All mutations repeat authority, target revision and scope-lock checks.
type ControlService interface {
	QuotePage(context.Context, studio.Actor, PageRequest, bool) (ControlQuote, error)
	QuoteRepair(context.Context, studio.Actor, RepairRequest) (ControlQuote, error)
	WithLockScores(context.Context, studio.Actor, *pagedoc.Page) error
	EnqueuePage(context.Context, studio.Actor, PageRequest) (*jobs.Job, error)
	EnqueueRepair(context.Context, studio.Actor, RepairRequest) (*jobs.Job, error)
	LockStatus(context.Context, studio.Actor, string, string) (LockStatus, error)
	LockChapterStatus(context.Context, studio.Actor, string, string, string) (LockStatus, error)
	EnqueueTraining(context.Context, studio.Actor, string, string, time.Time) (*jobs.Job, error)
	EnqueueScoring(context.Context, studio.Actor, string, string, time.Time) (*jobs.Job, error)
}
