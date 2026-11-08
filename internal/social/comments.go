package social

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
)

// Comment target kinds served by the public comments API.
const (
	TargetSeries  = "series"
	TargetChapter = "chapter"
	TargetPanel   = "panel"
)

// MaxCommentRunes bounds a comment body.
const MaxCommentRunes = 2000

// Anchor places a chapter or panel comment on a frozen page (1-based page
// number) and guided panel (GuidedPanel.Order). Series comments have none.
type Anchor struct {
	Page  int `json:"page,omitempty"`
	Panel int `json:"panel,omitempty"`
}

func (a Anchor) canonical() string {
	b, _ := json.Marshal(a)
	return string(b)
}

// CommentAuthor is the public face of a comment's author.
type CommentAuthor struct {
	ID          string `json:"id,omitempty"`
	Username    string `json:"username,omitempty"`
	DisplayName string `json:"display_name"`
}

// Comment is a public comment as the API returns it. Body is plain text; the
// client must render it as text, never as HTML.
type Comment struct {
	ID         string        `json:"id"`
	SeriesID   string        `json:"series_id"`
	TargetKind string        `json:"target_kind"`
	TargetID   string        `json:"target_id"`
	Anchor     Anchor        `json:"anchor"`
	ParentID   string        `json:"parent_id,omitempty"`
	Body       string        `json:"body"`
	Author     CommentAuthor `json:"author"`
	CreatedAt  time.Time     `json:"created_at"`
	EditedAt   *time.Time    `json:"edited_at,omitempty"`
	// Hidden is true when the series owner hid the comment; only its author
	// and the owner still receive it.
	Hidden    bool `json:"hidden"`
	CanEdit   bool `json:"can_edit"`
	CanDelete bool `json:"can_delete"`
	CanHide   bool `json:"can_hide"`
	CanReport bool `json:"can_report"`
}

// target is a resolved, readable comment target.
type target struct {
	series publish.Series
	// pub is set for chapter and panel targets.
	pub *publish.Publication
}

func (t target) ownerID() string { return t.series.OwnerID }

// resolveTarget checks through publish.Service that the viewer may read the
// series and (for chapter and panel kinds) the publication targetID.
func (s *service) resolveTarget(ctx context.Context, v publish.Viewer, seriesID, kind, targetID string) (target, error) {
	seriesID, targetID = strings.TrimSpace(seriesID), strings.TrimSpace(targetID)
	if !validID(seriesID) || !validID(targetID) {
		return target{}, ErrNotFound
	}
	if kind != TargetSeries && kind != TargetChapter && kind != TargetPanel {
		return target{}, invalid("target_kind", "social.error.target_kind")
	}
	series, err := s.pub.SeriesByID(ctx, v, seriesID)
	if err != nil {
		return target{}, accessErr(err)
	}
	if kind == TargetSeries {
		if targetID != series.ID {
			return target{}, ErrNotFound
		}
		return target{series: series}, nil
	}
	pub, err := s.findPublication(ctx, v, series, targetID)
	if err != nil {
		return target{}, err
	}
	if !s.pub.CanRead(ctx, v, series, pub) {
		return target{}, ErrNotFound
	}
	return target{series: series, pub: &pub}, nil
}

// findPublication finds a readable publication of series by ID in any of its languages.
func (s *service) findPublication(ctx context.Context, v publish.Viewer, series publish.Series, id string) (publish.Publication, error) {
	langs := append([]string{series.OriginalLang}, series.Languages...)
	seen := map[string]bool{}
	for _, lang := range langs {
		if lang == "" || seen[lang] {
			continue
		}
		seen[lang] = true
		pubs, err := s.pub.Publications(ctx, v, series.ID, lang)
		if err != nil {
			return publish.Publication{}, accessErr(err)
		}
		for _, p := range pubs {
			if p.ID == id {
				return p, nil
			}
		}
	}
	return publish.Publication{}, ErrNotFound
}

// parseAnchor validates the anchor for kind; pages are checked against the
// frozen publication when the anchor names a page.
func (s *service) parseAnchor(ctx context.Context, v publish.Viewer, t target, kind string, raw json.RawMessage) (Anchor, error) {
	var a Anchor
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if len(trimmed) > 256 || json.Unmarshal(trimmed, &a) != nil {
			return Anchor{}, invalid("anchor", "social.error.anchor")
		}
	}
	switch kind {
	case TargetSeries:
		if a != (Anchor{}) {
			return Anchor{}, invalid("anchor", "social.error.anchor")
		}
		return a, nil
	case TargetChapter:
		if a.Panel != 0 || a.Page < 0 || (a.Page > 0 && a.Page > t.pub.PageCount) {
			return Anchor{}, invalid("anchor", "social.error.anchor")
		}
		return a, nil
	}
	// Panel: the page and panel must exist in the frozen publication.
	if a.Page < 1 || a.Panel < 1 {
		return Anchor{}, invalid("anchor", "social.error.anchor")
	}
	pub, pages, err := s.pub.Publication(ctx, v, t.series.ID, t.pub.Number, t.pub.Lang)
	if err != nil {
		return Anchor{}, accessErr(err)
	}
	if pub.ID != t.pub.ID {
		return Anchor{}, ErrNotFound
	}
	for _, pg := range pages {
		if pg.Number != a.Page {
			continue
		}
		for _, pn := range pg.Panels {
			if pn.Order == a.Panel {
				return a, nil
			}
		}
	}
	return Anchor{}, invalid("anchor", "social.error.anchor")
}

// cleanBody normalizes a plain-text comment body: valid UTF-8, \n line
// breaks, no control characters, trimmed, at most MaxCommentRunes.
func cleanBody(in string) (string, error) {
	in = strings.ToValidUTF8(in, "�")
	in = strings.ReplaceAll(in, "\r\n", "\n")
	in = strings.ReplaceAll(in, "\r", "\n")
	var b strings.Builder
	for _, r := range in {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	switch {
	case out == "":
		return "", invalid("body", "social.error.body_empty")
	case utf8.RuneCountInString(out) > MaxCommentRunes:
		return "", invalid("body", "social.error.body_long")
	}
	return out, nil
}

// NewComment is a comment to post.
type NewComment struct {
	SeriesID   string          `json:"series_id"`
	TargetKind string          `json:"target_kind"`
	TargetID   string          `json:"target_id"`
	Anchor     json.RawMessage `json:"anchor"`
	Body       string          `json:"body"`
	ParentID   string          `json:"parent_id"`
}

const commentCols = `c.id, c.series_id, c.target_kind, c.target_id, c.anchor, c.parent_id, c.body, c.author_id,
	c.created_at, c.edited_at, c.resolved_at, u.username AS author_username, u.display_name AS author_display_name`

const commentFrom = ` FROM comments c LEFT JOIN users u ON u.id = c.author_id`

type commentRow struct {
	ID                string         `db:"id"`
	SeriesID          sql.NullString `db:"series_id"`
	TargetKind        string         `db:"target_kind"`
	TargetID          string         `db:"target_id"`
	Anchor            string         `db:"anchor"`
	ParentID          sql.NullString `db:"parent_id"`
	Body              string         `db:"body"`
	AuthorID          sql.NullString `db:"author_id"`
	CreatedAt         time.Time      `db:"created_at"`
	EditedAt          sql.NullTime   `db:"edited_at"`
	ResolvedAt        sql.NullTime   `db:"resolved_at"`
	AuthorUsername    sql.NullString `db:"author_username"`
	AuthorDisplayName sql.NullString `db:"author_display_name"`
}

// comment builds the API view for viewerID; ownerID is the series owner.
func (r commentRow) comment(viewerID, ownerID string) Comment {
	c := Comment{ID: r.ID, SeriesID: r.SeriesID.String, TargetKind: r.TargetKind, TargetID: r.TargetID,
		ParentID: r.ParentID.String, Body: r.Body, CreatedAt: r.CreatedAt.UTC(), Hidden: r.ResolvedAt.Valid}
	_ = json.Unmarshal([]byte(r.Anchor), &c.Anchor)
	if r.EditedAt.Valid {
		t := r.EditedAt.Time.UTC()
		c.EditedAt = &t
	}
	if r.AuthorID.Valid {
		c.Author = CommentAuthor{ID: r.AuthorID.String, Username: r.AuthorUsername.String, DisplayName: r.AuthorDisplayName.String}
		if c.Author.DisplayName == "" {
			c.Author.DisplayName = c.Author.Username
		}
	}
	signedIn := viewerID != ""
	mine := signedIn && r.AuthorID.Valid && r.AuthorID.String == viewerID
	c.CanEdit, c.CanDelete = mine, mine
	c.CanHide = signedIn && viewerID == ownerID
	c.CanReport = signedIn && !mine && !c.Hidden
	return c
}

// visibleClause keeps deleted comments out and hidden ones visible only to
// their author and the series owner.
func visibleClause(viewerID, ownerID string) (string, []any) {
	base := "c.visibility = 'public' AND c.deleted_at IS NULL"
	switch {
	case viewerID != "" && viewerID == ownerID:
		return base, nil
	case viewerID != "":
		return base + " AND (c.resolved_at IS NULL OR c.author_id = ?)", []any{viewerID}
	}
	return base + " AND c.resolved_at IS NULL", nil
}

// createComment posts a comment as v (a signed-in viewer) on a readable target.
func (s *service) createComment(ctx context.Context, v publish.Viewer, in NewComment) (Comment, error) {
	if v.UserID == "" {
		return Comment{}, ErrForbidden
	}
	kind := strings.TrimSpace(in.TargetKind)
	t, err := s.resolveTarget(ctx, v, in.SeriesID, kind, in.TargetID)
	if err != nil {
		return Comment{}, err
	}
	anchor, err := s.parseAnchor(ctx, v, t, kind, in.Anchor)
	if err != nil {
		return Comment{}, err
	}
	body, err := cleanBody(in.Body)
	if err != nil {
		return Comment{}, err
	}
	targetID := t.series.ID
	if t.pub != nil {
		targetID = t.pub.ID
	}
	parentID := strings.TrimSpace(in.ParentID)
	id, now := db.NewID(), s.now()
	err = s.db.WithTx(ctx, func(tx *db.Tx) error {
		if parentID != "" {
			// The parent must be a visible public comment on the same target.
			vis, args := visibleClause(v.UserID, t.ownerID())
			var n int
			if err := tx.Get(ctx, &n, `SELECT COUNT(*) FROM comments c WHERE c.id = ? AND c.series_id = ? AND c.target_kind = ?
				AND c.target_id = ? AND `+vis, append([]any{parentID, t.series.ID, kind, targetID}, args...)...); err != nil {
				return fmt.Errorf("social: comment parent: %w", err)
			}
			if n == 0 {
				return invalid("parent_id", "social.error.parent")
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO comments (id, project_id, series_id, target_kind, target_id, anchor, visibility,
			author_id, parent_id, body, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 'public', ?, ?, ?, 'open', ?, ?)`,
			id, db.NullString(t.series.ProjectID), t.series.ID, kind, targetID, anchor.canonical(), v.UserID,
			db.NullString(parentID), body, now, now); err != nil {
			return fmt.Errorf("social: insert comment: %w", err)
		}
		return nil
	})
	if err != nil {
		return Comment{}, err
	}
	return s.commentByID(ctx, v, id)
}

// commentByID returns a visible public comment whose target the viewer may read.
func (s *service) commentByID(ctx context.Context, v publish.Viewer, id string) (Comment, error) {
	row, t, err := s.loadComment(ctx, v, id)
	if err != nil {
		return Comment{}, err
	}
	return row.comment(v.UserID, t.ownerID()), nil
}

// loadComment reads a comment, re-checks its target through publish and
// applies the hidden/deleted rules for the viewer.
func (s *service) loadComment(ctx context.Context, v publish.Viewer, id string) (commentRow, target, error) {
	id = strings.TrimSpace(id)
	if !validID(id) {
		return commentRow{}, target{}, ErrNotFound
	}
	var row commentRow
	if err := s.db.Get(ctx, &row, `SELECT `+commentCols+commentFrom+
		` WHERE c.id = ? AND c.visibility = 'public' AND c.deleted_at IS NULL`, id); err != nil {
		if db.IsNotFound(err) {
			return commentRow{}, target{}, ErrNotFound
		}
		return commentRow{}, target{}, fmt.Errorf("social: get comment: %w", err)
	}
	if !slices.Contains([]string{TargetSeries, TargetChapter, TargetPanel}, row.TargetKind) || !row.SeriesID.Valid {
		return commentRow{}, target{}, ErrNotFound
	}
	t, err := s.resolveTarget(ctx, v, row.SeriesID.String, row.TargetKind, row.TargetID)
	if err != nil {
		return commentRow{}, target{}, accessErr(err)
	}
	if row.ResolvedAt.Valid && v.UserID != t.ownerID() && (v.UserID == "" || row.AuthorID.String != v.UserID) {
		return commentRow{}, target{}, ErrNotFound
	}
	return row, t, nil
}

// CommentQuery selects one page of a target's comments, newest first.
type CommentQuery struct {
	SeriesID, TargetKind, TargetID string
	// Page and Panel optionally narrow chapter/panel comments to one anchor.
	Page, Panel int
	Cursor      string
	Limit       int
}

// listComments returns a page of visible comments on a readable target.
func (s *service) listComments(ctx context.Context, v publish.Viewer, q CommentQuery) ([]Comment, string, error) {
	kind := strings.TrimSpace(q.TargetKind)
	t, err := s.resolveTarget(ctx, v, q.SeriesID, kind, q.TargetID)
	if err != nil {
		return nil, "", err
	}
	limit := clampLimit(q.Limit, DefaultCommentLimit, MaxCommentLimit)
	where, args := s.targetWhere(v, t, kind)
	if q.Page != 0 || q.Panel != 0 {
		if kind == TargetSeries || q.Page < 1 || q.Panel < 0 || (kind == TargetChapter && q.Panel != 0) {
			return nil, "", invalid("anchor", "social.error.anchor")
		}
		if kind == TargetPanel && q.Panel == 0 {
			// A page alone lists every panel comment on that page. Anchors are
			// stored canonically ({"page":N,"panel":M}), so a prefix match is exact.
			where += " AND c.anchor LIKE ?"
			args = append(args, fmt.Sprintf(`{"page":%d,"panel":%%`, q.Page))
		} else {
			where += " AND c.anchor = ?"
			args = append(args, Anchor{Page: q.Page, Panel: q.Panel}.canonical())
		}
	}
	if q.Cursor != "" {
		k, err := decodeCursor(q.Cursor, true)
		if err != nil {
			return nil, "", err
		}
		where += " AND (c.created_at < ? OR (c.created_at = ? AND c.id < ?))"
		args = append(args, k.T, k.T, k.ID)
	}
	var rows []commentRow
	if err := s.db.Select(ctx, &rows, `SELECT `+commentCols+commentFrom+` WHERE `+where+
		` ORDER BY c.created_at DESC, c.id DESC LIMIT ?`, append(args, limit+1)...); err != nil {
		return nil, "", fmt.Errorf("social: list comments: %w", err)
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		next = encodeCursor(last.CreatedAt, last.ID)
	}
	out := make([]Comment, len(rows))
	for i, r := range rows {
		out[i] = r.comment(v.UserID, t.ownerID())
	}
	return out, next, nil
}

// targetWhere is the WHERE clause for the visible comments of a resolved target.
func (s *service) targetWhere(v publish.Viewer, t target, kind string) (string, []any) {
	targetID := t.series.ID
	if t.pub != nil {
		targetID = t.pub.ID
	}
	vis, args := visibleClause(v.UserID, t.ownerID())
	return "c.series_id = ? AND c.target_kind = ? AND c.target_id = ? AND " + vis,
		append([]any{t.series.ID, kind, targetID}, args...)
}

// countComments counts the viewer-visible comments of a readable target.
func (s *service) countComments(ctx context.Context, v publish.Viewer, t target, kind string) (int, error) {
	where, args := s.targetWhere(v, t, kind)
	var n int
	if err := s.db.Get(ctx, &n, `SELECT COUNT(*) FROM comments c WHERE `+where, args...); err != nil {
		return 0, fmt.Errorf("social: count comments: %w", err)
	}
	return n, nil
}

// editComment replaces the body of the viewer's own comment.
func (s *service) editComment(ctx context.Context, v publish.Viewer, id, body string) (Comment, error) {
	row, t, err := s.loadComment(ctx, v, id)
	if err != nil {
		return Comment{}, err
	}
	if v.UserID == "" || row.AuthorID.String != v.UserID {
		return Comment{}, ErrForbidden
	}
	clean, err := cleanBody(body)
	if err != nil {
		return Comment{}, err
	}
	now := s.now()
	if _, err := s.db.Exec(ctx, `UPDATE comments SET body = ?, edited_at = ?, updated_at = ? WHERE id = ? AND author_id = ?
		AND deleted_at IS NULL`, clean, now, now, row.ID, v.UserID); err != nil {
		return Comment{}, fmt.Errorf("social: edit comment: %w", err)
	}
	row.Body = clean
	row.EditedAt = sql.NullTime{Time: now, Valid: true}
	return row.comment(v.UserID, t.ownerID()), nil
}

// deleteComment deletes the viewer's own comment: the body is cleared and
// deleted_at set, so replies keep their thread position.
func (s *service) deleteComment(ctx context.Context, v publish.Viewer, id string) (Comment, error) {
	row, t, err := s.loadComment(ctx, v, id)
	if err != nil {
		return Comment{}, err
	}
	if v.UserID == "" || row.AuthorID.String != v.UserID {
		return Comment{}, ErrForbidden
	}
	now := s.now()
	if _, err := s.db.Exec(ctx, `UPDATE comments SET body = '', deleted_at = ?, updated_at = ? WHERE id = ? AND author_id = ?`,
		now, now, row.ID, v.UserID); err != nil {
		return Comment{}, fmt.Errorf("social: delete comment: %w", err)
	}
	c := row.comment(v.UserID, t.ownerID())
	c.Body = ""
	return c, nil
}

// setHidden hides or shows a comment on a series the viewer owns.
func (s *service) setHidden(ctx context.Context, v publish.Viewer, id string, hidden bool) (Comment, error) {
	row, t, err := s.loadComment(ctx, v, id)
	if err != nil {
		return Comment{}, err
	}
	if v.UserID == "" || v.UserID != t.ownerID() {
		return Comment{}, ErrForbidden
	}
	now := s.now()
	if hidden {
		_, err = s.db.Exec(ctx, `UPDATE comments SET resolved_at = COALESCE(resolved_at, ?), resolved_by = COALESCE(resolved_by, ?),
			updated_at = ? WHERE id = ?`, now, v.UserID, now, row.ID)
		if !row.ResolvedAt.Valid {
			row.ResolvedAt = sql.NullTime{Time: now, Valid: true}
		}
	} else {
		_, err = s.db.Exec(ctx, `UPDATE comments SET resolved_at = NULL, resolved_by = NULL, updated_at = ? WHERE id = ?`, now, row.ID)
		row.ResolvedAt = sql.NullTime{}
	}
	if err != nil {
		return Comment{}, fmt.Errorf("social: hide comment: %w", err)
	}
	return row.comment(v.UserID, t.ownerID()), nil
}
