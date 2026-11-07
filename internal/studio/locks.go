package studio

import (
	"context"
	"fmt"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

func (s *service) CheckScopeWritable(ctx context.Context, scope jobs.Scope) error {
	if err := scope.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return checkScopeWritable(ctx, s.db, scope)
}

func checkScopeWritable(ctx context.Context, q db.Querier, scope jobs.Scope) error {
	switch scope.Kind {
	case jobs.ScopeNone:
		return nil
	case jobs.ScopeProject:
		var id string
		if err := q.Get(ctx, &id, `SELECT id FROM projects WHERE id = ?`+scopeRowLock(q), scope.ProjectID); err != nil {
			return scopeLookupError(err)
		}
		return nil
	case jobs.ScopeChapter:
		var row struct {
			ProjectID string `db:"project_id"`
			Locked    bool   `db:"locked"`
		}
		if err := q.Get(ctx, &row, `SELECT project_id, locked FROM chapters WHERE id = ?`+scopeRowLock(q), scope.ChapterID); err != nil {
			return scopeLookupError(err)
		}
		if row.ProjectID != scope.ProjectID {
			return ErrForbidden
		}
		if row.Locked {
			return ErrLocked
		}
		return nil
	case jobs.ScopePage:
		var row struct {
			PageProjectID string `db:"page_project_id"`
			ProjectID     string `db:"project_id"`
			ChapterLocked bool   `db:"chapter_locked"`
			PageLocked    bool   `db:"page_locked"`
		}
		if err := q.Get(ctx, &row, `SELECT pg.project_id AS page_project_id, c.project_id, c.locked AS chapter_locked, pg.locked AS page_locked
			FROM pages pg JOIN chapters c ON c.id = pg.chapter_id WHERE pg.id = ?`+scopeRowLock(q), scope.PageID); err != nil {
			return scopeLookupError(err)
		}
		if row.ProjectID != scope.ProjectID || row.PageProjectID != scope.ProjectID {
			return ErrForbidden
		}
		if row.ChapterLocked || row.PageLocked {
			return ErrLocked
		}
		return nil
	case jobs.ScopePanel:
		var row struct {
			PanelProjectID string `db:"panel_project_id"`
			PageProjectID  string `db:"page_project_id"`
			ProjectID      string `db:"project_id"`
			PageID         string `db:"page_id"`
			ChapterLocked  bool   `db:"chapter_locked"`
			PageLocked     bool   `db:"page_locked"`
			PanelLocked    bool   `db:"panel_locked"`
		}
		if err := q.Get(ctx, &row, `SELECT pa.project_id AS panel_project_id, pg.project_id AS page_project_id,
			c.project_id, pa.page_id, c.locked AS chapter_locked,
			pg.locked AS page_locked, pa.locked AS panel_locked FROM panels pa
			JOIN pages pg ON pg.id = pa.page_id JOIN chapters c ON c.id = pg.chapter_id WHERE pa.id = ?`+scopeRowLock(q), scope.PanelID); err != nil {
			return scopeLookupError(err)
		}
		if row.ProjectID != scope.ProjectID || row.PageProjectID != scope.ProjectID ||
			row.PanelProjectID != scope.ProjectID || row.PageID != scope.PageID {
			return ErrForbidden
		}
		if row.ChapterLocked || row.PageLocked || row.PanelLocked {
			return ErrLocked
		}
		return nil
	case jobs.ScopeLayer, jobs.ScopeRegion:
		var row struct {
			PanelProjectID string `db:"panel_project_id"`
			PageProjectID  string `db:"page_project_id"`
			ProjectID      string `db:"project_id"`
			PanelID        string `db:"panel_id"`
			Kind           string `db:"kind"`
			ChapterLocked  bool   `db:"chapter_locked"`
			PageLocked     bool   `db:"page_locked"`
			PanelLocked    bool   `db:"panel_locked"`
			LayerLocked    bool   `db:"layer_locked"`
		}
		if err := q.Get(ctx, &row, `SELECT pa.project_id AS panel_project_id, pg.project_id AS page_project_id,
			c.project_id, l.panel_id, l.kind, c.locked AS chapter_locked,
			pg.locked AS page_locked, pa.locked AS panel_locked, l.locked AS layer_locked FROM layers l
			JOIN panels pa ON pa.id = l.panel_id JOIN pages pg ON pg.id = pa.page_id
			JOIN chapters c ON c.id = pg.chapter_id WHERE l.id = ?`+scopeRowLock(q), scope.LayerID); err != nil {
			return scopeLookupError(err)
		}
		if row.ProjectID != scope.ProjectID || row.PageProjectID != scope.ProjectID || row.PanelProjectID != scope.ProjectID ||
			(scope.Kind == jobs.ScopeLayer && row.PanelID != scope.PanelID) {
			return ErrForbidden
		}
		if scope.Kind == jobs.ScopeRegion && row.Kind == "lettering" {
			return ErrForbidden
		}
		if row.ChapterLocked || row.PageLocked || row.PanelLocked || row.LayerLocked {
			return ErrLocked
		}
		return nil
	default:
		return ErrInvalid
	}
}

func scopeRowLock(q db.Querier) string {
	if _, transactional := q.(*db.Tx); transactional && q.Dialect() == db.Postgres {
		return " FOR UPDATE"
	}
	return ""
}

func scopeLookupError(err error) error {
	if db.IsNotFound(err) {
		return ErrNotFound
	}
	return fmt.Errorf("studio: resolve job scope: %w", err)
}
