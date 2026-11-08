package social

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/admin/adminapi"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
)

// Moderation is the admin panel's narrow view of public comments (ADR 0013
// §1). It is not a reader-facing API: the admin module calls it only for site
// admins resolving a report.
type Moderation interface {
	// ReportedComment returns a public comment for a report review, hidden or
	// not; deleted comments come back with Deleted set and no body.
	ReportedComment(ctx context.Context, id string) (ReportedComment, error)
	// HideComment hides a public comment from everyone except its author, as
	// the series owner can, and records the admin; the owner cannot show it again.
	HideComment(ctx context.Context, id, adminID string) error
}

// ReportedComment is what an admin sees of a reported comment.
type ReportedComment struct {
	ID         string
	SeriesID   string
	TargetKind string // series | chapter | panel
	TargetID   string // series or publication ID
	Anchor     Anchor
	Body       string
	AuthorID   string
	Hidden     bool
	Deleted    bool
	CreatedAt  time.Time
}

var _ Moderation = (*service)(nil)

func (s *service) ReportedComment(ctx context.Context, id string) (ReportedComment, error) {
	id = strings.TrimSpace(id)
	if !validID(id) {
		return ReportedComment{}, ErrNotFound
	}
	var row struct {
		ID         string         `db:"id"`
		SeriesID   sql.NullString `db:"series_id"`
		TargetKind string         `db:"target_kind"`
		TargetID   string         `db:"target_id"`
		Anchor     string         `db:"anchor"`
		Body       string         `db:"body"`
		AuthorID   sql.NullString `db:"author_id"`
		ResolvedAt sql.NullTime   `db:"resolved_at"`
		DeletedAt  sql.NullTime   `db:"deleted_at"`
		CreatedAt  time.Time      `db:"created_at"`
	}
	if err := s.db.Get(ctx, &row, `SELECT id, series_id, target_kind, target_id, anchor, body, author_id, resolved_at,
		deleted_at, created_at FROM comments WHERE id = ? AND visibility = 'public'`, id); err != nil {
		if db.IsNotFound(err) {
			return ReportedComment{}, ErrNotFound
		}
		return ReportedComment{}, fmt.Errorf("social: reported comment: %w", err)
	}
	c := ReportedComment{ID: row.ID, SeriesID: row.SeriesID.String, TargetKind: row.TargetKind, TargetID: row.TargetID,
		Body: row.Body, AuthorID: row.AuthorID.String, Hidden: row.ResolvedAt.Valid, Deleted: row.DeletedAt.Valid,
		CreatedAt: row.CreatedAt.UTC()}
	_ = json.Unmarshal([]byte(row.Anchor), &c.Anchor)
	if c.Deleted {
		c.Body = ""
	}
	return c, nil
}

func (s *service) HideComment(ctx context.Context, id, adminID string) error {
	id = strings.TrimSpace(id)
	if !validID(id) || adminID == "" {
		return ErrNotFound
	}
	now := s.now()
	res, err := s.db.Exec(ctx, `UPDATE comments SET resolved_at = COALESCE(resolved_at, ?), resolved_by = ?, updated_at = ?
		WHERE id = ? AND visibility = 'public' AND deleted_at IS NULL`, now, adminID, now, id)
	if err != nil {
		return fmt.Errorf("social: admin hide comment: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// blocked reports whether text contains a word the content rules block.
func (s *service) blocked(ctx context.Context, text string) bool {
	if s.settings == nil {
		return false
	}
	_, ok := adminapi.Blocked(s.settings.MustInstance(ctx).Content.BlockedWords, text)
	return ok
}

// moderationNotice is the E5 notice for project members when readers outside
// the project cannot see the series or one of its chapters. Only project
// members can read such a series at all, so others never get here.
func (m *Module) moderationNotice(ctx context.Context, s publish.Series, pubs []publish.Publication) string {
	if s.TakenDown {
		return i18n.T(ctx, "social.series.taken_down")
	}
	for _, p := range pubs {
		if p.TakenDown {
			return i18n.T(ctx, "social.series.chapter_taken_down")
		}
	}
	if !adminapi.RatingAllowed(m.d.Settings.MustInstance(ctx).Content, s.AgeRating) {
		return i18n.T(ctx, "social.series.rating_hidden", "rating", ratingLabel(ctx, s.AgeRating))
	}
	return ""
}
