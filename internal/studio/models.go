package studio

import (
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// Project formats (projects.format).
const (
	FormatMangaRTL = "manga_rtl"
	FormatComicLTR = "comic_ltr"
	FormatWebtoon  = "webtoon"
)

// Project statuses (projects.status).
const (
	ProjectDraft    = "draft"
	ProjectActive   = "active"
	ProjectArchived = "archived"
)

// Project visibility defaults (projects.visibility). The licence guard reads
// it: a public series may not use non-commercial models.
const (
	VisibilityPrivate = "private"
	VisibilityPublic  = "public"
)

// Project sources (projects.source); the importer sets "import".
const (
	SourceStudio = "studio"
	SourceImport = "import"
	SourcePack   = "pack"
)

// Display badges shown on project cards (B1, B2).
const (
	BadgeDraft     = "draft"
	BadgePublished = "published"
	BadgeScript    = "script"
	BadgeWebtoon   = "webtoon"
	BadgeImported  = "imported"
)

// Project is a manga project (one per series).
type Project struct {
	ID              string                  `db:"id" json:"id"`
	OwnerID         string                  `db:"owner_id" json:"owner_id"`
	Title           string                  `db:"title" json:"title"`
	Slug            string                  `db:"slug" json:"slug"`
	Premise         string                  `db:"premise" json:"premise"`
	Logline         string                  `db:"logline" json:"logline"`
	Genres          db.JSON[[]string]       `db:"genres" json:"genres"`
	Tags            db.JSON[[]string]       `db:"tags" json:"tags"`
	Style           string                  `db:"style" json:"style"`
	Format          string                  `db:"format" json:"format"`
	PageSize        string                  `db:"page_size" json:"page_size"`
	PageWidth       int                     `db:"page_width" json:"page_width"`
	PageHeight      int                     `db:"page_height" json:"page_height"`
	Language        string                  `db:"language" json:"language"`
	Status          string                  `db:"status" json:"status"`
	CoverAssetID    string                  `db:"cover_asset_id" json:"cover_asset_id,omitempty"`
	TemplateID      string                  `db:"template_id" json:"template_id,omitempty"`
	Settings        db.JSON[map[string]any] `db:"settings" json:"settings"`
	Visibility      string                  `db:"visibility" json:"visibility"`
	RoutingMode     string                  `db:"routing_mode" json:"routing_mode"`
	Source          string                  `db:"source" json:"source"`
	ChaptersPlanned int                     `db:"chapters_planned" json:"chapters_planned"`
	PagesPerChapter string                  `db:"pages_per_chapter" json:"pages_per_chapter"`
	CreatedAt       time.Time               `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time               `db:"updated_at" json:"updated_at"`
	ArchivedAt      *time.Time              `db:"archived_at" json:"archived_at,omitempty"`
}

// ReadingDirection returns "rtl", "ltr" or "vertical" for the project format.
func (p *Project) ReadingDirection() string {
	switch p.Format {
	case FormatComicLTR:
		return "ltr"
	case FormatWebtoon:
		return "vertical"
	}
	return "rtl"
}

const projectCols = `p.id, p.owner_id, p.title, p.slug, p.premise, p.logline, p.genres, p.tags, p.style, p.format, p.page_size,
	p.page_width, p.page_height, p.language, p.status, COALESCE(p.cover_asset_id, '') AS cover_asset_id,
	COALESCE(p.template_id, '') AS template_id, p.settings, p.visibility, p.routing_mode, p.source, p.chapters_planned,
	p.pages_per_chapter, p.created_at, p.updated_at, p.archived_at`

// Project member roles (project_members.role).
const (
	RoleOwner    = "owner"
	RoleWriter   = "writer"
	RoleArtist   = "artist"
	RoleLetterer = "letterer"
	RoleEditor   = "editor"
	RoleViewer   = "viewer"
)

// Roles lists the member roles in display order.
var Roles = []string{RoleOwner, RoleWriter, RoleArtist, RoleLetterer, RoleEditor, RoleViewer}

// MemberScope narrows what a member may change: a page range ("1-20") and/or
// layer kinds (["lettering"] = lettering layer only). Empty means no limit.
type MemberScope struct {
	Pages  string   `json:"pages,omitempty"`
	Layers []string `json:"layers,omitempty"`
}

// IsZero reports whether the scope has no limits.
func (s MemberScope) IsZero() bool { return s.Pages == "" && len(s.Layers) == 0 }

// Member is a project member with the user's public fields.
type Member struct {
	ProjectID   string               `db:"project_id" json:"project_id"`
	UserID      string               `db:"user_id" json:"user_id"`
	Role        string               `db:"role" json:"role"`
	Scope       db.JSON[MemberScope] `db:"scope" json:"scope"`
	InvitedBy   string               `db:"invited_by" json:"invited_by,omitempty"`
	CreatedAt   time.Time            `db:"created_at" json:"created_at"`
	Username    string               `db:"username" json:"username"`
	DisplayName string               `db:"display_name" json:"display_name"`
}

// Name returns the display name or username.
func (m *Member) Name() string {
	if m.DisplayName != "" {
		return m.DisplayName
	}
	return m.Username
}

const memberCols = `m.project_id, m.user_id, m.role, m.scope, COALESCE(m.invited_by, '') AS invited_by, m.created_at,
	u.username, u.display_name`

// Arc groups chapters.
type Arc struct {
	ID        string    `db:"id" json:"id"`
	ProjectID string    `db:"project_id" json:"project_id"`
	Title     string    `db:"title" json:"title"`
	Summary   string    `db:"summary" json:"summary"`
	Color     string    `db:"color" json:"color"`
	OrderIdx  int       `db:"order_idx" json:"order"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

const arcCols = `id, project_id, title, summary, color, order_idx, created_at, updated_at`

// Provenance values of a chapter's per-role AI label (product rule 5).
const (
	LabelHuman      = "human"
	LabelAIAssisted = "ai_assisted"
	LabelAI         = "ai"
)

// AILabel is the per-role provenance label of a chapter.
type AILabel struct {
	Script    string `json:"script,omitempty"`
	Art       string `json:"art,omitempty"`
	Lettering string `json:"lettering,omitempty"`
}

// Chapter statuses.
const (
	ChapterDraft     = "draft"
	ChapterScripting = "scripting"
	ChapterDrawing   = "drawing"
	ChapterLettering = "lettering"
	ChapterReview    = "review"
	ChapterReady     = "ready"
	ChapterPublished = "published"
)

// Chapter is one chapter of a project.
type Chapter struct {
	ID        string                  `db:"id" json:"id"`
	ProjectID string                  `db:"project_id" json:"project_id"`
	ArcID     string                  `db:"arc_id" json:"arc_id,omitempty"`
	Number    int                     `db:"number" json:"number"`
	Title     string                  `db:"title" json:"title"`
	Summary   string                  `db:"summary" json:"summary"`
	Status    string                  `db:"status" json:"status"`
	Locked    bool                    `db:"locked" json:"locked"`
	AILabel   db.JSON[AILabel]        `db:"ai_label" json:"ai_label"`
	Settings  db.JSON[map[string]any] `db:"settings" json:"settings"`
	CreatedAt time.Time               `db:"created_at" json:"created_at"`
	UpdatedAt time.Time               `db:"updated_at" json:"updated_at"`
}

const chapterCols = `id, project_id, COALESCE(arc_id, '') AS arc_id, number, title, summary, status, locked, ai_label, settings,
	created_at, updated_at`

// Scene is one scene of a chapter.
type Scene struct {
	ID         string            `db:"id" json:"id"`
	ProjectID  string            `db:"project_id" json:"project_id"`
	ChapterID  string            `db:"chapter_id" json:"chapter_id"`
	OrderIdx   int               `db:"order_idx" json:"order"`
	Title      string            `db:"title" json:"title"`
	Summary    string            `db:"summary" json:"summary"`
	LocationID string            `db:"location_id" json:"location_id,omitempty"`
	TimeOfDay  string            `db:"time_of_day" json:"time_of_day"`
	Beats      db.JSON[[]string] `db:"beats" json:"beats"`
	Prose      string            `db:"prose" json:"prose"`
	CreatedAt  time.Time         `db:"created_at" json:"created_at"`
	UpdatedAt  time.Time         `db:"updated_at" json:"updated_at"`
}

const sceneCols = `id, project_id, chapter_id, order_idx, title, summary, COALESCE(location_id, '') AS location_id, time_of_day, beats,
	prose, created_at, updated_at`

// Dialogue kinds of a script line; they become bubble kinds.
const (
	LineSpeech  = "speech"
	LineThought = "thought"
	LineShout   = "shout"
	LineWhisper = "whisper"
	LineCaption = "caption"
)

// LineKinds lists the dialogue kinds.
var LineKinds = []string{LineSpeech, LineThought, LineShout, LineWhisper, LineCaption}

// DialogueLine is one line of a script panel.
type DialogueLine struct {
	Speaker     string `json:"speaker"`
	CharacterID string `json:"character_id,omitempty"`
	Text        string `json:"text"`
	Kind        string `json:"kind"`
}

// ScriptPanel is one panel of the panel-by-panel script. Label is the human
// numbering "7.3" (page 7, panel 3).
type ScriptPanel struct {
	ID           string                  `db:"id" json:"id"`
	ProjectID    string                  `db:"project_id" json:"project_id"`
	ChapterID    string                  `db:"chapter_id" json:"chapter_id"`
	SceneID      string                  `db:"scene_id" json:"scene_id,omitempty"`
	PageNumber   int                     `db:"page_number" json:"page_number"`
	OrderIdx     int                     `db:"order_idx" json:"order"`
	Label        string                  `db:"label" json:"label"`
	Camera       string                  `db:"camera" json:"camera"`
	Shot         string                  `db:"shot" json:"shot"`
	Emotion      string                  `db:"emotion" json:"emotion"`
	Action       string                  `db:"action" json:"action"`
	Dialogue     db.JSON[[]DialogueLine] `db:"dialogue" json:"dialogue"`
	SFX          db.JSON[[]string]       `db:"sfx" json:"sfx"`
	CharacterIDs db.JSON[[]string]       `db:"character_ids" json:"character_ids"`
	SettingIDs   db.JSON[[]string]       `db:"setting_ids" json:"setting_ids"`
	Notes        string                  `db:"notes" json:"notes"`
	CreatedAt    time.Time               `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time               `db:"updated_at" json:"updated_at"`
}

const scriptPanelCols = `id, project_id, chapter_id, COALESCE(scene_id, '') AS scene_id, page_number, order_idx, label, camera, shot,
	emotion, action, dialogue, sfx, character_ids, setting_ids, notes, created_at, updated_at`

// Point is an [x, y] position in page percent (0–100).
type Point [2]float64

// Polygon is a panel outline in page percent.
type Polygon []Point

// Page is one page of a chapter.
type Page struct {
	ID           string     `db:"id" json:"id"`
	ProjectID    string     `db:"project_id" json:"project_id"`
	ChapterID    string     `db:"chapter_id" json:"chapter_id"`
	Number       int        `db:"number" json:"number"`
	LayoutID     string     `db:"layout_id" json:"layout_id"`
	Size         string     `db:"size" json:"size"`
	Width        int        `db:"width" json:"width"`
	Height       int        `db:"height" json:"height"`
	Status       string     `db:"status" json:"status"`
	Locked       bool       `db:"locked" json:"locked"`
	LockedBy     string     `db:"locked_by" json:"locked_by,omitempty"`
	LockedAt     *time.Time `db:"locked_at" json:"locked_at,omitempty"`
	Version      int        `db:"version" json:"version"`
	BranchID     string     `db:"branch_id" json:"branch_id,omitempty"`
	ThumbAssetID string     `db:"thumb_asset_id" json:"thumb_asset_id,omitempty"`
	ArtAssetID   string     `db:"art_asset_id" json:"art_asset_id,omitempty"`
	Notes        string     `db:"notes" json:"notes"`
	CreatedAt    time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time  `db:"updated_at" json:"updated_at"`
}

const pageCols = `id, project_id, chapter_id, number, layout_id, size, width, height, status, locked, COALESCE(locked_by, '') AS locked_by,
	locked_at, version, COALESCE(branch_id, '') AS branch_id, COALESCE(thumb_asset_id, '') AS thumb_asset_id,
	COALESCE(art_asset_id, '') AS art_asset_id, notes, created_at, updated_at`

// Panel is one panel of a page: polygon geometry plus generation settings.
type Panel struct {
	ID            string                  `db:"id" json:"id"`
	ProjectID     string                  `db:"project_id" json:"project_id"`
	PageID        string                  `db:"page_id" json:"page_id"`
	OrderIdx      int                     `db:"order_idx" json:"order"`
	Points        db.JSON[Polygon]        `db:"points" json:"points"`
	Border        db.JSON[map[string]any] `db:"border" json:"border"`
	ScriptPanelID string                  `db:"script_panel_id" json:"script_panel_id,omitempty"`
	Prompt        string                  `db:"prompt" json:"prompt"`
	PromptSource  string                  `db:"prompt_source" json:"prompt_source"`
	Camera        string                  `db:"camera" json:"camera"`
	Shot          string                  `db:"shot" json:"shot"`
	Pose          db.JSON[map[string]any] `db:"pose" json:"pose"`
	Style         string                  `db:"style" json:"style"`
	Locked        bool                    `db:"locked" json:"locked"`
	LockedBy      string                  `db:"locked_by" json:"locked_by,omitempty"`
	LockedAt      *time.Time              `db:"locked_at" json:"locked_at,omitempty"`
	AltText       string                  `db:"alt_text" json:"alt_text"`
	ReviewStatus  string                  `db:"review_status" json:"review_status"`
	Living        db.JSON[map[string]any] `db:"living" json:"living"`
	CreatedAt     time.Time               `db:"created_at" json:"created_at"`
	UpdatedAt     time.Time               `db:"updated_at" json:"updated_at"`
}

const panelCols = `id, project_id, page_id, order_idx, points, border, COALESCE(script_panel_id, '') AS script_panel_id, prompt,
	prompt_source, camera, shot, pose, style, locked, COALESCE(locked_by, '') AS locked_by, locked_at, alt_text, review_status,
	living, created_at, updated_at`

// Layer kinds.
const (
	LayerBackground = "background"
	LayerCharacter  = "character"
	LayerFX         = "fx"
	LayerLettering  = "lettering"
)

// LayerKinds lists the layer kinds.
var LayerKinds = []string{LayerBackground, LayerCharacter, LayerFX, LayerLettering}

// Layer is one layer of a panel.
type Layer struct {
	ID          string                  `db:"id" json:"id"`
	PanelID     string                  `db:"panel_id" json:"panel_id"`
	Kind        string                  `db:"kind" json:"kind"`
	Name        string                  `db:"name" json:"name"`
	CharacterID string                  `db:"character_id" json:"character_id,omitempty"`
	AssetID     string                  `db:"asset_id" json:"asset_id,omitempty"`
	MaskAssetID string                  `db:"mask_asset_id" json:"mask_asset_id,omitempty"`
	OrderIdx    int                     `db:"order_idx" json:"order"`
	Visible     bool                    `db:"visible" json:"visible"`
	Locked      bool                    `db:"locked" json:"locked"`
	Opacity     float64                 `db:"opacity" json:"opacity"`
	Blend       string                  `db:"blend" json:"blend"`
	Transform   db.JSON[map[string]any] `db:"transform" json:"transform"`
	Provenance  string                  `db:"provenance" json:"provenance"`
	ModelID     string                  `db:"model_id" json:"model_id"`
	ProviderID  string                  `db:"provider_id" json:"provider_id"`
	JobID       string                  `db:"job_id" json:"job_id,omitempty"`
	Seed        *int64                  `db:"seed" json:"seed,omitempty"`
	Prompt      string                  `db:"prompt" json:"prompt"`
	LockScore   *float64                `db:"lock_score" json:"lock_score,omitempty"`
	CreatedAt   time.Time               `db:"created_at" json:"created_at"`
	UpdatedAt   time.Time               `db:"updated_at" json:"updated_at"`
}

const layerCols = `id, panel_id, kind, name, COALESCE(character_id, '') AS character_id, COALESCE(asset_id, '') AS asset_id,
	COALESCE(mask_asset_id, '') AS mask_asset_id, order_idx, visible, locked, opacity, blend, transform, provenance, model_id,
	provider_id, COALESCE(job_id, '') AS job_id, seed, prompt, lock_score, created_at, updated_at`

// Bubble kinds.
const (
	BubbleSpeech  = "speech"
	BubbleThought = "thought"
	BubbleShout   = "shout"
	BubbleWhisper = "whisper"
	BubbleCaption = "caption"
	BubbleSFX     = "sfx"
)

// BubbleGeometry is a bubble box in page percent.
type BubbleGeometry struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Bubble is lettering data on a page (text lives in BubbleText per language).
type Bubble struct {
	ID                 string                  `db:"id" json:"id"`
	PageID             string                  `db:"page_id" json:"page_id"`
	PanelID            string                  `db:"panel_id" json:"panel_id,omitempty"`
	Kind               string                  `db:"kind" json:"kind"`
	SpeakerCharacterID string                  `db:"speaker_character_id" json:"speaker_character_id,omitempty"`
	ScriptPanelID      string                  `db:"script_panel_id" json:"script_panel_id,omitempty"`
	OrderIdx           int                     `db:"order_idx" json:"order"`
	Geometry           db.JSON[BubbleGeometry] `db:"geometry" json:"geometry"`
	Tail               db.JSON[map[string]any] `db:"tail" json:"tail"`
	Style              db.JSON[map[string]any] `db:"style" json:"style"`
	Vertical           bool                    `db:"vertical" json:"vertical"`
	CreatedAt          time.Time               `db:"created_at" json:"created_at"`
	UpdatedAt          time.Time               `db:"updated_at" json:"updated_at"`
}

const bubbleCols = `id, page_id, COALESCE(panel_id, '') AS panel_id, kind, COALESCE(speaker_character_id, '') AS speaker_character_id,
	COALESCE(script_panel_id, '') AS script_panel_id, order_idx, geometry, tail, style, vertical, created_at, updated_at`

// BubbleText is the text of a bubble in one language.
type BubbleText struct {
	ID        string                  `db:"id" json:"id"`
	BubbleID  string                  `db:"bubble_id" json:"bubble_id"`
	Lang      string                  `db:"lang" json:"lang"`
	Text      string                  `db:"text" json:"text"`
	Status    string                  `db:"status" json:"status"`
	Source    string                  `db:"source" json:"source"`
	Fit       db.JSON[map[string]any] `db:"fit" json:"fit"`
	UpdatedBy string                  `db:"updated_by" json:"updated_by,omitempty"`
	CreatedAt time.Time               `db:"created_at" json:"created_at"`
	UpdatedAt time.Time               `db:"updated_at" json:"updated_at"`
}

const bubbleTextCols = `id, bubble_id, lang, text, status, source, fit, COALESCE(updated_by, '') AS updated_by, created_at, updated_at`

// Character categories (characters.category).
const (
	CategoryMain       = "main"
	CategorySupporting = "supporting"
	CategoryExtra      = "extra"
)

// Character Lock statuses.
const (
	LockNone     = "none"
	LockTraining = "training"
	LockReady    = "ready"
	LockFailed   = "failed"
)

// Voice is the character's voice for animate mode.
type Voice struct {
	Description string `json:"description,omitempty"`
	Preset      string `json:"preset,omitempty"`
}

// LockInfo describes the trained Character Lock.
type LockInfo struct {
	Refs      int        `json:"refs,omitempty"`
	Model     string     `json:"model,omitempty"`
	Method    string     `json:"method,omitempty"`
	TrainedAt *time.Time `json:"trained_at,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// Character is a member of the character bible.
type Character struct {
	ID              string                  `db:"id" json:"id"`
	ProjectID       string                  `db:"project_id" json:"project_id"`
	Name            string                  `db:"name" json:"name"`
	Kanji           string                  `db:"kanji" json:"kanji"`
	Role            string                  `db:"role" json:"role"`
	RoleLine        string                  `db:"role_line" json:"role_line"`
	Age             string                  `db:"age" json:"age"`
	Look            string                  `db:"look" json:"look"`
	Personality     string                  `db:"personality" json:"personality"`
	SpeechStyle     string                  `db:"speech_style" json:"speech_style"`
	Backstory       string                  `db:"backstory" json:"backstory"`
	Voice           db.JSON[Voice]          `db:"voice" json:"voice"`
	Color           string                  `db:"color" json:"color"`
	Category        string                  `db:"category" json:"category"`
	LockStatus      string                  `db:"lock_status" json:"lock_status"`
	LockScore       *float64                `db:"lock_score" json:"lock_score,omitempty"`
	TriggerWord     string                  `db:"trigger_word" json:"trigger_word"`
	LoraAssetID     string                  `db:"lora_asset_id" json:"lora_asset_id,omitempty"`
	PortraitAssetID string                  `db:"portrait_asset_id" json:"portrait_asset_id,omitempty"`
	PackSource      db.JSON[map[string]any] `db:"pack_source" json:"pack_source"`
	OrderIdx        int                     `db:"order_idx" json:"order"`
	Style           string                  `db:"style" json:"style"`
	LockJobID       string                  `db:"lock_job_id" json:"lock_job_id,omitempty"`
	LockInfo        db.JSON[LockInfo]       `db:"lock_info" json:"lock_info"`
	SheetJobID      string                  `db:"sheet_job_id" json:"sheet_job_id,omitempty"`
	CreatedAt       time.Time               `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time               `db:"updated_at" json:"updated_at"`
}

const characterCols = `id, project_id, name, kanji, role, role_line, age, look, personality, speech_style, backstory, voice, color,
	category, lock_status, lock_score, trigger_word, COALESCE(lora_asset_id, '') AS lora_asset_id,
	COALESCE(portrait_asset_id, '') AS portrait_asset_id, pack_source, order_idx, style, COALESCE(lock_job_id, '') AS lock_job_id,
	lock_info, COALESCE(sheet_job_id, '') AS sheet_job_id, created_at, updated_at`

// Character reference kinds.
const (
	RefFront      = "front"
	RefSide       = "side"
	RefBack       = "back"
	RefFace       = "face"
	RefExpression = "expression"
	RefOutfit     = "outfit"
	RefSheet      = "sheet"
	RefOther      = "other"
)

// RefKinds lists the reference kinds.
var RefKinds = []string{RefSheet, RefFront, RefSide, RefBack, RefFace, RefExpression, RefOutfit, RefOther}

// CharacterRef is one reference image of a character.
type CharacterRef struct {
	ID          string    `db:"id" json:"id"`
	CharacterID string    `db:"character_id" json:"character_id"`
	AssetID     string    `db:"asset_id" json:"asset_id"`
	Kind        string    `db:"kind" json:"kind"`
	Label       string    `db:"label" json:"label"`
	OrderIdx    int       `db:"order_idx" json:"order"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
	Key         string    `db:"key" json:"-"`
}

// Outfit is a named outfit of a character.
type Outfit struct {
	ID          string    `db:"id" json:"id"`
	CharacterID string    `db:"character_id" json:"character_id"`
	Name        string    `db:"name" json:"name"`
	Description string    `db:"description" json:"description"`
	AssetID     string    `db:"asset_id" json:"asset_id,omitempty"`
	IsDefault   bool      `db:"is_default" json:"is_default"`
	OrderIdx    int       `db:"order_idx" json:"order"`
	Chapters    string    `db:"chapters" json:"chapters"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
}

// Relation links two characters ("Rival → reluctant partner").
type Relation struct {
	ID          string    `db:"id" json:"id"`
	CharacterID string    `db:"character_id" json:"character_id"`
	OtherID     string    `db:"other_id" json:"other_id"`
	Kind        string    `db:"kind" json:"kind"`
	Note        string    `db:"note" json:"note"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
	OtherName   string    `db:"other_name" json:"other_name"`
}

// Setting item kinds.
const (
	SettingLocation = "location"
	SettingProp     = "prop"
	SettingStyle    = "style"
)

// SettingKinds lists the setting item kinds in tab order.
var SettingKinds = []string{SettingLocation, SettingProp, SettingStyle}

// SettingData holds per-item options.
type SettingData struct {
	// TrackTimeOfDay makes the continuity checker follow the lighting of this location.
	TrackTimeOfDay bool `json:"track_time_of_day,omitempty"`
	// Notes is free text (interior + exterior, era...).
	Notes string `json:"notes,omitempty"`
}

// SettingItem is a location, prop or style reference reused across panels.
type SettingItem struct {
	ID          string               `db:"id" json:"id"`
	ProjectID   string               `db:"project_id" json:"project_id"`
	Kind        string               `db:"kind" json:"kind"`
	Name        string               `db:"name" json:"name"`
	Description string               `db:"description" json:"description"`
	Tags        db.JSON[[]string]    `db:"tags" json:"tags"`
	AssetID     string               `db:"asset_id" json:"asset_id,omitempty"`
	Refs        db.JSON[[]string]    `db:"refs" json:"refs"`
	Data        db.JSON[SettingData] `db:"data" json:"data"`
	OrderIdx    int                  `db:"order_idx" json:"order"`
	CreatedAt   time.Time            `db:"created_at" json:"created_at"`
	UpdatedAt   time.Time            `db:"updated_at" json:"updated_at"`
}

const settingCols = `id, project_id, kind, name, description, tags, COALESCE(asset_id, '') AS asset_id, refs, data, order_idx,
	created_at, updated_at`

// Version actor kinds.
const (
	ActorUser   = "user"
	ActorAgent  = "agent"
	ActorSystem = "system"
)

// Version is one snapshot of a page document.
type Version struct {
	ID        string    `db:"id" json:"id"`
	PageID    string    `db:"page_id" json:"page_id"`
	BranchID  string    `db:"branch_id" json:"branch_id,omitempty"`
	ParentID  string    `db:"parent_id" json:"parent_id,omitempty"`
	Number    int       `db:"number" json:"number"`
	ActorKind string    `db:"actor_kind" json:"actor_kind"`
	ActorID   string    `db:"actor_id" json:"actor_id,omitempty"`
	TokenID   string    `db:"token_id" json:"token_id,omitempty"`
	JobID     string    `db:"job_id" json:"job_id,omitempty"`
	Action    string    `db:"action" json:"action"`
	Scope     string    `db:"scope" json:"scope"`
	Summary   string    `db:"summary" json:"summary"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	ActorName string    `db:"actor_name" json:"actor_name"`
}

const versionCols = `v.id, v.page_id, COALESCE(v.branch_id, '') AS branch_id, COALESCE(v.parent_id, '') AS parent_id, v.number,
	v.actor_kind, COALESCE(v.actor_id, '') AS actor_id, COALESCE(v.token_id, '') AS token_id, COALESCE(v.job_id, '') AS job_id,
	v.action, v.scope, v.summary, v.created_at, COALESCE(NULLIF(u.display_name, ''), u.username, '') AS actor_name`

// Branch is a named line of versions of a page.
type Branch struct {
	ID             string     `db:"id" json:"id"`
	PageID         string     `db:"page_id" json:"page_id"`
	Name           string     `db:"name" json:"name"`
	ParentBranchID string     `db:"parent_branch_id" json:"parent_branch_id,omitempty"`
	HeadVersionID  string     `db:"head_version_id" json:"head_version_id,omitempty"`
	CreatedBy      string     `db:"created_by" json:"created_by,omitempty"`
	MergedIntoID   string     `db:"merged_into_id" json:"merged_into_id,omitempty"`
	MergedAt       *time.Time `db:"merged_at" json:"merged_at,omitempty"`
	CreatedAt      time.Time  `db:"created_at" json:"created_at"`
}

const branchCols = `id, page_id, name, COALESCE(parent_branch_id, '') AS parent_branch_id, COALESCE(head_version_id, '') AS head_version_id,
	COALESCE(created_by, '') AS created_by, COALESCE(merged_into_id, '') AS merged_into_id, merged_at, created_at`
