package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// IsZero reports whether the scope leaves the role unnarrowed.
func (s MemberScope) IsZero() bool { return s == MemberScope{} }

// HasPages reports whether writes are limited to a page range.
func (s MemberScope) HasPages() bool { return s.PageFrom > 0 || s.PageTo > 0 }

// Validate checks the scope and that it narrows something role can write.
// Owners, writers and viewers are never scoped: an owner manages the project,
// a writer's script is chapter-wide and a viewer writes nothing. A lettering
// scope needs a role that letters (letterer, editor); a page range needs a
// role that changes pages (artist, letterer, editor).
func (s MemberScope) Validate(role string) error {
	if s.IsZero() {
		return nil
	}
	switch s.Layer {
	case "":
	case ScopeLayerLettering:
		if role != RoleLetterer && role != RoleEditor {
			return fmt.Errorf("%w: only letterers and editors can be limited to the lettering layer", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unknown layer scope", ErrInvalid)
	}
	if s.HasPages() {
		if s.PageFrom < 1 || s.PageTo < s.PageFrom || s.PageTo > MaxScopePage {
			return fmt.Errorf("%w: page range must be 1–%d with the first page not after the last", ErrInvalid, MaxScopePage)
		}
		if role != RoleArtist && role != RoleLetterer && role != RoleEditor {
			return fmt.Errorf("%w: only artists, letterers and editors can be limited to pages", ErrInvalid)
		}
	}
	return nil
}

// Encode returns the stored JSON form ("{}" for the zero scope).
func (s MemberScope) Encode() string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ParseMemberScope reads a stored scope. Unknown fields or malformed JSON
// are an error; callers treat that as the narrowest scope (read only).
func ParseMemberScope(raw string) (MemberScope, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" || raw == "null" {
		return MemberScope{}, nil
	}
	var s MemberScope
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return MemberScope{}, fmt.Errorf("%w: member scope: %v", ErrInvalid, err)
	}
	if s.Layer != "" && s.Layer != ScopeLayerLettering {
		return MemberScope{}, fmt.Errorf("%w: member scope layer %q", ErrInvalid, s.Layer)
	}
	if s.HasPages() && (s.PageFrom < 1 || s.PageTo < s.PageFrom) {
		return MemberScope{}, fmt.Errorf("%w: member scope pages", ErrInvalid)
	}
	return s, nil
}

// target is the resolved page (and layer kind) an action writes.
type target struct {
	page      int // page number, 0 when the target is not one page
	layerKind string
}

// scopeAllows applies a member scope to an action the role already allows.
// t is nil when the caller named no target (project-level Authorize).
func scopeAllows(s MemberScope, bad bool, action Action, t *target) bool {
	if action == ActRead {
		return true
	}
	if bad {
		return false
	}
	if s.Layer == ScopeLayerLettering {
		if action != ActLetter {
			return false
		}
		if t != nil && t.layerKind != "" && t.layerKind != ScopeLayerLettering {
			return false
		}
	}
	if s.HasPages() {
		if t == nil || t.page == 0 || t.page < s.PageFrom || t.page > s.PageTo {
			return false
		}
	}
	return true
}

func (s *service) AuthorizeTarget(ctx context.Context, actor Actor, t jobs.Scope, action Action) error {
	if t.Validate() != nil {
		return ErrInvalid
	}
	return s.authorizeAt(ctx, s.db, actor, strings.TrimSpace(t.ProjectID), action, &t)
}

func (s *service) AuthorizeTargetTx(ctx context.Context, tx *db.Tx, actor Actor, t jobs.Scope, action Action) error {
	if tx == nil || t.Validate() != nil {
		return ErrInvalid
	}
	return s.authorizeAt(ctx, tx, actor, strings.TrimSpace(t.ProjectID), action, &t)
}

// resolveTarget finds the page a target writes and checks that every row of
// its ancestry belongs to the scope's project. It reads without row locks:
// CheckScopeWritable takes them in its established order afterwards.
func resolveTarget(ctx context.Context, q db.Querier, s jobs.Scope) (target, error) {
	var row struct {
		ProjectID     string `db:"project_id"`
		PageProjectID string `db:"page_project_id"`
		PageID        string `db:"page_id"`
		PanelID       string `db:"panel_id"`
		Number        int    `db:"number"`
		LayerKind     string `db:"kind"`
	}
	switch s.Kind {
	case jobs.ScopeNone, jobs.ScopeProject:
		return target{}, nil
	case jobs.ScopeChapter:
		if err := q.Get(ctx, &row, `SELECT project_id FROM chapters WHERE id = ?`, s.ChapterID); err != nil {
			return target{}, lookupTarget(err)
		}
		if row.ProjectID != s.ProjectID {
			return target{}, ErrForbidden
		}
		return target{}, nil
	case jobs.ScopePage:
		if err := q.Get(ctx, &row, `SELECT project_id, project_id AS page_project_id, number FROM pages WHERE id = ?`, s.PageID); err != nil {
			return target{}, lookupTarget(err)
		}
	case jobs.ScopePanel:
		if err := q.Get(ctx, &row, `SELECT pa.project_id, pg.project_id AS page_project_id, pa.page_id, pg.number
			FROM panels pa JOIN pages pg ON pg.id = pa.page_id WHERE pa.id = ?`, s.PanelID); err != nil {
			return target{}, lookupTarget(err)
		}
		if row.PageID != s.PageID {
			return target{}, ErrForbidden
		}
	case jobs.ScopeLayer, jobs.ScopeRegion:
		if err := q.Get(ctx, &row, `SELECT pa.project_id, pg.project_id AS page_project_id, l.panel_id, pg.number, l.kind
			FROM layers l JOIN panels pa ON pa.id = l.panel_id JOIN pages pg ON pg.id = pa.page_id WHERE l.id = ?`, s.LayerID); err != nil {
			return target{}, lookupTarget(err)
		}
		if s.Kind == jobs.ScopeLayer && row.PanelID != s.PanelID {
			return target{}, ErrForbidden
		}
	default:
		return target{}, ErrInvalid
	}
	if row.ProjectID != s.ProjectID || row.PageProjectID != s.ProjectID {
		return target{}, ErrForbidden
	}
	return target{page: row.Number, layerKind: row.LayerKind}, nil
}

func lookupTarget(err error) error {
	if err == nil {
		return nil
	}
	if db.IsNotFound(err) {
		return ErrNotFound
	}
	return fmt.Errorf("studio: resolve target: %w", err)
}
