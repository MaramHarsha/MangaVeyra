// Package pagestore persists page documents (pagedoc.Page) in the pages,
// panels, layers, bubbles and bubble_texts tables, enforces page/panel/layer
// locks server-side, keeps a version snapshot for every saved change and fills
// signed media URLs when documents go to a client.
//
// Frozen Phase 1 contract (docs/plans/0002-phase-1-first-playable.md). Other
// modules use core.MustUse[pagestore.Service](d).
package pagestore

import (
	"context"
	"errors"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
)

// Errors.
var (
	ErrNotFound = errors.New("pagestore: not found")
	// ErrVersion means the page changed since baseVersion (reload and retry).
	ErrVersion = errors.New("pagestore: page changed since it was loaded")
	// ErrLocked means the change touches a locked page, panel or layer.
	ErrLocked  = errors.New("pagestore: locked")
	ErrInvalid = errors.New("pagestore: invalid page")
)

// Change describes who made a change, for version history.
type Change struct {
	// ActorKind is "user", "agent" or "system".
	ActorKind string
	ActorID   string
	TokenID   string
	JobID     string
	// Action is a short machine label ("save", "generate_layer", "letter", "revert", "lock").
	Action string
	// Summary is a short human sentence ("Region edit · Rin's hand").
	Summary string
}

// PageSummary is a page row for filmstrips and lists.
type PageSummary struct {
	ID       string            `json:"id"`
	Number   int               `json:"number"`
	Panels   int               `json:"panels"`
	Locked   bool              `json:"locked"`
	Version  int               `json:"version"`
	Thumb    *pagedoc.AssetRef `json:"thumb,omitempty"`
	Updated  time.Time         `json:"updated_at"`
	LayoutID string            `json:"layout_id,omitempty"`
}

// Version is a saved snapshot.
type Version struct {
	ID        string    `json:"id"`
	Number    int       `json:"number"`
	ActorKind string    `json:"actor_kind"`
	ActorID   string    `json:"actor_id,omitempty"`
	JobID     string    `json:"job_id,omitempty"`
	Action    string    `json:"action"`
	Summary   string    `json:"summary"`
	CreatedAt time.Time `json:"created_at"`
}

// SetLayerAsset writes generated or uploaded art into a panel layer.
type SetLayerAsset struct {
	PageID  string
	PanelID string
	// LayerID updates an existing layer; empty finds (or creates) the layer of
	// Kind (and CharacterID for character layers).
	LayerID     string
	Kind        string
	CharacterID string
	Name        string
	AssetID     string
	MaskAssetID string
	Provenance  string
	ModelID     string
	ProviderID  string
	JobID       string
	Seed        *int64
	Prompt      string
	LockScore   *float64
}

// Service is the page store API.
type Service interface {
	// GetPage loads a page document (URLs empty).
	GetPage(ctx context.Context, pageID string) (pagedoc.Page, error)
	// GetPageTx loads a page inside tx (locks the page row on PostgreSQL).
	GetPageTx(ctx context.Context, tx *db.Tx, pageID string) (pagedoc.Page, error)
	// WithURLs fills signed media URLs for every asset reference (ttl ≤ 0 → 1 h).
	WithURLs(ctx context.Context, p *pagedoc.Page, ttl time.Duration)
	// ListPages lists a chapter's pages in order.
	ListPages(ctx context.Context, chapterID string) ([]PageSummary, error)
	// ChapterPages loads every page document of a chapter in order.
	ChapterPages(ctx context.Context, chapterID string) ([]pagedoc.Page, error)

	// CreatePagesTx inserts new pages (with panels, layers, bubbles) after the
	// chapter's last page, renumbering as needed. IDs in the documents are kept
	// when they are valid ULIDs and replaced otherwise; the stored pages are returned.
	CreatePagesTx(ctx context.Context, tx *db.Tx, chapterID string, pages []pagedoc.Page, ch Change) ([]pagedoc.Page, error)
	// ReplaceChapterPagesTx replaces only this chapter's pages after checking
	// every ancestor/descendant lock, inside the caller's job commit fence.
	ReplaceChapterPagesTx(ctx context.Context, tx *db.Tx, chapterID string, pages []pagedoc.Page, ch Change) ([]pagedoc.Page, error)

	// SavePage replaces a page document. baseVersion must equal the stored
	// version (else ErrVersion). Locked panels and layers must be unchanged
	// (else ErrLocked); a locked page rejects every change. Lock flags
	// themselves are ignored here (use SetLock). Returns the stored page with
	// the new version.
	SavePage(ctx context.Context, p pagedoc.Page, baseVersion int, ch Change) (pagedoc.Page, error)

	// SetLayerAssetTx stores art in a layer, bumping the page version and
	// recording a snapshot. Fails with ErrLocked if the page, panel or target
	// layer is locked.
	SetLayerAssetTx(ctx context.Context, tx *db.Tx, in SetLayerAsset, ch Change) (pagedoc.Layer, error)
	// SetBubblesTx replaces all bubbles of a page (auto-lettering, translation).
	SetBubblesTx(ctx context.Context, tx *db.Tx, pageID string, bubbles []pagedoc.Bubble, ch Change) error
	// SetThumbTx sets the page thumbnail asset.
	SetThumbTx(ctx context.Context, tx *db.Tx, pageID, assetID string) error

	// SetLock locks or unlocks a page (panelID empty), a panel or a layer.
	// Unlocking is a human action: callers must not expose it to agents.
	SetLock(ctx context.Context, pageID, panelID, layerID string, locked bool, ch Change) error
	// IsWritable reports ErrLocked when the chapter, page, panel or layer is locked.
	IsWritable(ctx context.Context, pageID, panelID, layerID string) error

	// ReorderPages sets the page order of a chapter (all page IDs, new order).
	ReorderPages(ctx context.Context, chapterID string, pageIDs []string, ch Change) error
	// DeletePage removes a page and renumbers the rest.
	DeletePage(ctx context.Context, pageID string, ch Change) error

	// Versions lists snapshots of a page, newest first.
	Versions(ctx context.Context, pageID string, limit int) ([]Version, error)
	// VersionDoc returns the page document stored in a snapshot.
	VersionDoc(ctx context.Context, versionID string) (pagedoc.Page, error)
	// Revert restores a snapshot as a new version (locks still apply).
	Revert(ctx context.Context, pageID, versionID string, ch Change) (pagedoc.Page, error)
}
