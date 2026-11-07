package studio

// Frozen Phase 1 contract (docs/plans/0002-phase-1-first-playable.md).
// Other modules use core.MustUse[studio.Service](d).

import (
	"context"
	"errors"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// Project roles (project_members.role).
const (
	RoleOwner    = "owner"
	RoleWriter   = "writer"
	RoleArtist   = "artist"
	RoleLetterer = "letterer"
	RoleEditor   = "editor"
	RoleViewer   = "viewer"
)

// Action is something an actor wants to do in a project.
type Action string

// Actions checked by Authorize. The matrix (server admins may do everything):
//
//	read            owner writer artist letterer editor viewer
//	edit_script     owner writer editor
//	edit_art        owner artist editor
//	letter          owner letterer editor
//	generate        owner writer artist editor
//	manage          owner
//	lock            owner editor
const (
	ActRead       Action = "read"
	ActEditScript Action = "edit_script"
	ActEditArt    Action = "edit_art"
	ActLetter     Action = "letter"
	ActGenerate   Action = "generate"
	ActManage     Action = "manage"
	ActLock       Action = "lock"
)

// Errors.
var (
	ErrNotFound  = errors.New("studio: not found")
	ErrForbidden = errors.New("studio: forbidden")
	ErrLocked    = errors.New("studio: locked")
	ErrConflict  = errors.New("studio: conflict")
	ErrInvalid   = errors.New("studio: invalid")
)

// Actor is who performs an action: a user, optionally through an API/agent token.
type Actor struct {
	UserID string
	// TokenID is set for bearer-token requests; scopes are rechecked by Authorize.
	TokenID string
	// SiteRole is the user's site role ("admin", "creator", "reader").
	SiteRole string
}

// Project is a manga project.
type Project struct {
	ID           string    `json:"id"`
	OwnerID      string    `json:"owner_id"`
	Title        string    `json:"title"`
	Slug         string    `json:"slug"`
	Premise      string    `json:"premise"`
	Logline      string    `json:"logline"`
	Genres       []string  `json:"genres"`
	Tags         []string  `json:"tags"`
	Style        string    `json:"style"`  // e.g. "bw_screentone", "seinen", "shoujo", "color_webtoon"
	Format       string    `json:"format"` // manga_rtl | comic_ltr | webtoon
	PageSize     string    `json:"page_size"`
	PageWidth    int       `json:"page_width"`
	PageHeight   int       `json:"page_height"`
	Language     string    `json:"language"`
	Status       string    `json:"status"`
	CoverAssetID string    `json:"cover_asset_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// NewProject is the validated input for creating a project. SourceText is
// optional private adaptation material (valid UTF-8, at most 1 MiB), stored as
// projects.settings.script_source_text and never returned in project DTOs.
type NewProject struct {
	Title      string
	Premise    string
	Logline    string
	Style      string
	Format     string
	PageSize   string
	Language   string
	Genres     []string
	Tags       []string
	PageWidth  int
	PageHeight int
	SourceText string
}

// ProjectSummary is a project row for lists and the dashboard.
type ProjectSummary struct {
	Project
	Role         string `json:"role"`
	ChapterCount int    `json:"chapter_count"`
	PageCount    int    `json:"page_count"`
	// LatestChapterID/Number point at the most recently edited chapter.
	LatestChapterID     string `json:"latest_chapter_id,omitempty"`
	LatestChapterNumber int    `json:"latest_chapter_number,omitempty"`
	ThumbAssetID        string `json:"thumb_asset_id,omitempty"`
}

// Chapter is a chapter of a project.
type Chapter struct {
	ID        string            `json:"id"`
	ProjectID string            `json:"project_id"`
	Number    int               `json:"number"`
	Title     string            `json:"title"`
	Summary   string            `json:"summary"`
	Status    string            `json:"status"`
	Locked    bool              `json:"locked"`
	AILabel   map[string]string `json:"ai_label"` // role → human | ai_assisted | ai (script, art, lettering)
	PageCount int               `json:"page_count"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// Character is a character in the bible.
type Character struct {
	ID              string         `json:"id"`
	ProjectID       string         `json:"project_id"`
	Name            string         `json:"name"`
	Kanji           string         `json:"kanji"`
	Role            string         `json:"role"`
	RoleLine        string         `json:"role_line"`
	Age             string         `json:"age"`
	Look            string         `json:"look"`
	Personality     string         `json:"personality"`
	SpeechStyle     string         `json:"speech_style"`
	Category        string         `json:"category"` // main | supporting | extra
	LockStatus      string         `json:"lock_status"`
	LockScore       *float64       `json:"lock_score,omitempty"`
	PortraitAssetID string         `json:"portrait_asset_id,omitempty"`
	LoraAssetID     string         `json:"lora_asset_id,omitempty"`
	TriggerWord     string         `json:"trigger_word,omitempty"`
	Refs            []CharacterRef `json:"refs"`
	Outfits         []Outfit       `json:"outfits"`
	Order           int            `json:"order"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

// NewCharacter is the validated input for a project character.
type NewCharacter struct {
	ProjectID   string
	Name        string
	Kanji       string
	Role        string
	RoleLine    string
	Age         string
	Look        string
	Personality string
	SpeechStyle string
	Category    string
}

// UpdateCharacterInput edits a character at the caller's loaded revision.
type UpdateCharacterInput struct {
	ProjectID, CharacterID string
	Character              NewCharacter
	ExpectedUpdatedAt      time.Time
}

// NewCharacterRef attaches an existing same-project image to a character.
type NewCharacterRef struct {
	ProjectID, CharacterID, AssetID, Kind, Label string
	Portrait                                     bool
	ExpectedUpdatedAt                            time.Time
}

// NewOutfit adds a named outfit variant at a character revision.
type NewOutfit struct {
	ProjectID, CharacterID, Name, Description, AssetID string
	IsDefault                                          bool
	ExpectedUpdatedAt                                  time.Time
}

// NewSettingItem creates a location, prop, or style reference.
type NewSettingItem struct {
	ProjectID, Kind, Name, Description, AssetID string
	Tags                                        []string
}

// BibleService owns authorized character/reference/outfit/setting mutations.
// It is provided independently so the original Studio service stays narrow.
type BibleService interface {
	UpdateCharacter(context.Context, Actor, UpdateCharacterInput) (Character, error)
	AddCharacterRef(context.Context, Actor, NewCharacterRef) (CharacterRef, error)
	CreateOutfit(context.Context, Actor, NewOutfit) (Outfit, error)
	CreateSettingItem(context.Context, Actor, NewSettingItem) (SettingItem, error)
}

// CharacterRef is one reference image.
type CharacterRef struct {
	ID      string `json:"id"`
	AssetID string `json:"asset_id"`
	Kind    string `json:"kind"` // front | side | back | face | expression | outfit | sheet | other
	Label   string `json:"label"`
	Order   int    `json:"order"`
}

// Outfit is a named outfit variant.
type Outfit struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	AssetID     string `json:"asset_id,omitempty"`
	IsDefault   bool   `json:"is_default"`
}

// SettingItem is a location, prop or style reference.
type SettingItem struct {
	ID          string   `json:"id"`
	ProjectID   string   `json:"project_id"`
	Kind        string   `json:"kind"` // location | prop | style
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	AssetID     string   `json:"asset_id,omitempty"`
}

// ScriptDoc is the persisted script of a chapter in mangaveyra.script.v1 shape
// (IDs filled in by the server).
type ScriptDoc struct {
	Schema         string        `json:"schema"`
	ChapterID      string        `json:"chapter_id"`
	ChapterSummary string        `json:"chapter_summary"`
	Scenes         []ScriptScene `json:"scenes"`
}

// ScriptScene is a scene with its panels in order.
type ScriptScene struct {
	ID        string        `json:"id,omitempty"`
	Key       string        `json:"key"`
	Title     string        `json:"title"`
	Summary   string        `json:"summary"`
	TimeOfDay string        `json:"time_of_day"`
	Panels    []ScriptPanel `json:"panels"`
}

// ScriptPanel is one panel of the script.
type ScriptPanel struct {
	ID           string           `json:"id,omitempty"`
	Key          string           `json:"key"`
	PageNumber   int              `json:"page_number"`
	Label        string           `json:"label,omitempty"` // computed "7.3"
	Camera       string           `json:"camera"`
	Shot         string           `json:"shot"`
	Emotion      string           `json:"emotion"`
	Action       string           `json:"action"`
	Dialogue     []ScriptDialogue `json:"dialogue"`
	SFX          []ScriptSFX      `json:"sfx"`
	CharacterIDs []string         `json:"character_ids"`
	Notes        string           `json:"notes"`
}

// ScriptDialogue is one line of dialogue.
type ScriptDialogue struct {
	Speaker     string `json:"speaker"`
	CharacterID string `json:"character_id"`
	Kind        string `json:"kind"` // speech | thought | caption
	Text        string `json:"text"`
}

// ScriptSFX is a sound effect.
type ScriptSFX struct {
	Text string `json:"text"`
	Note string `json:"note"`
}

// ReplaceScriptInput replaces a chapter's scenes and script panels with a
// generated (already validated) document.
type ReplaceScriptInput struct {
	Actor     Actor
	ProjectID string
	ChapterID string
	Doc       ScriptDoc
	// ExpectedUpdatedAt, when non-zero, binds generation to the chapter revision
	// that was planned. The comparison happens under the same row lock as the
	// replacement, so concurrent jobs cannot overwrite a newer manuscript.
	ExpectedUpdatedAt time.Time
	// JobID records provenance. The claimed attempt is deliberately not input:
	// jobs.Run.CommitSideEffect is the authoritative attempt/lease fence.
	JobID      string
	ProviderID string
	ModelID    string
	// Provenance is "ai" for generated scripts.
	Provenance string
}

// ReplaceScriptResult summarises a replacement.
type ReplaceScriptResult struct {
	Scenes int `json:"scenes"`
	Panels int `json:"panels"`
	Pages  int `json:"pages"`
}

// EditScriptInput updates the editable fields of an existing script. Scene and
// panel IDs are stable; ExpectedUpdatedAt is an optimistic concurrency fence.
type EditScriptInput struct {
	Actor             Actor
	ProjectID         string
	ChapterID         string
	Doc               ScriptDoc
	ExpectedUpdatedAt time.Time
}

// Service is the studio's API for other modules.
type Service interface {
	// Authorize checks the actor may perform action in project (membership, site
	// role, token scopes, active account). Returns ErrNotFound or ErrForbidden.
	Authorize(ctx context.Context, actor Actor, projectID string, action Action) error
	AuthorizeTx(ctx context.Context, tx *db.Tx, actor Actor, projectID string, action Action) error
	// ActorFromUser builds an Actor for a user ID (looks up the site role).
	ActorFromUser(ctx context.Context, userID string) (Actor, error)

	// CreateProject creates the project and its owner membership atomically.
	CreateProject(ctx context.Context, actor Actor, in NewProject) (Project, error)
	GetProject(ctx context.Context, id string) (Project, error)
	ListProjects(ctx context.Context, userID string, limit int) ([]ProjectSummary, error)
	// SourceText returns optional private adaptation material retained with the
	// project. Callers authorize project/script access before calling it.
	SourceText(ctx context.Context, projectID string) (string, error)

	CreateChapter(ctx context.Context, actor Actor, projectID, title string) (Chapter, error)
	GetChapter(ctx context.Context, id string) (Chapter, error)
	ListChapters(ctx context.Context, projectID string) ([]Chapter, error)

	GetScript(ctx context.Context, chapterID string) (ScriptDoc, error)
	// ValidateScript checks a document against the project (character allow-list, limits).
	ValidateScript(ctx context.Context, projectID string, doc ScriptDoc) error
	// ReplaceGeneratedScriptTx rechecks authorization, chapter lock and the
	// optional ExpectedUpdatedAt revision inside tx, then replaces only that
	// chapter's scenes and script panels. On any error the previous script is
	// untouched.
	ReplaceGeneratedScriptTx(ctx context.Context, tx *db.Tx, in ReplaceScriptInput) (ReplaceScriptResult, error)
	// EditScript updates an existing script atomically without changing scene or
	// panel IDs. It rejects stale ExpectedUpdatedAt revisions with ErrConflict.
	EditScript(ctx context.Context, in EditScriptInput) (Chapter, error)

	CreateCharacter(ctx context.Context, actor Actor, in NewCharacter) (Character, error)
	ListCharacters(ctx context.Context, projectID string) ([]Character, error)
	GetCharacter(ctx context.Context, id string) (Character, error)
	GetCharacterTx(ctx context.Context, tx *db.Tx, id string) (Character, error)
	// AddCharacterRefTx attaches a stored image to a character's references.
	AddCharacterRefTx(ctx context.Context, tx *db.Tx, characterID, assetID, kind, label string) (CharacterRef, error)
	// SetCharacterPortraitTx sets the portrait (main reference) image.
	SetCharacterPortraitTx(ctx context.Context, tx *db.Tx, characterID, assetID string) error

	ListSettingItems(ctx context.Context, projectID, kind string) ([]SettingItem, error)

	// SetChapterAILabel records the provenance of one role (script, art, lettering).
	SetChapterAILabelTx(ctx context.Context, tx *db.Tx, chapterID, role, value string) error

	// CheckScopeWritable rejects jobs whose scope touches a locked chapter, page,
	// panel or layer. Region scopes resolve layer ancestry server-side, verify
	// the project and reject lettering layers. It is registered as a
	// jobs.Validator by the studio module.
	CheckScopeWritable(ctx context.Context, s jobs.Scope) error
	CheckScopeWritableTx(ctx context.Context, tx *db.Tx, s jobs.Scope) error
}
