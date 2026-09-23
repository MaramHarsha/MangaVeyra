package studio

import (
	"context"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

// Pages lists a chapter's pages by number.
func (s *Service) Pages(ctx context.Context, chapterID string) ([]Page, error) {
	var out []Page
	err := s.db.Select(ctx, &out, `SELECT `+pageCols+` FROM pages WHERE chapter_id = ? ORDER BY number, created_at`, chapterID)
	return out, err
}

// Page returns a page by id.
func (s *Service) Page(ctx context.Context, id string) (*Page, error) {
	var p Page
	if err := s.db.Get(ctx, &p, `SELECT `+pageCols+` FROM pages WHERE id = ?`, id); err != nil {
		return nil, notFound(err)
	}
	return &p, nil
}

// PageByNumber returns page n of a chapter.
func (s *Service) PageByNumber(ctx context.Context, chapterID string, n int) (*Page, error) {
	var p Page
	if err := s.db.Get(ctx, &p, `SELECT `+pageCols+` FROM pages WHERE chapter_id = ? AND number = ? ORDER BY created_at LIMIT 1`, chapterID, n); err != nil {
		return nil, notFound(err)
	}
	return &p, nil
}

// CreatePage appends an empty page to a chapter (number 0 = next number).
func (s *Service) CreatePage(ctx context.Context, a *Access, chapterID string, number int) (*Page, error) {
	if err := s.require(ctx, a, EditPages); err != nil {
		return nil, err
	}
	c, err := s.ChapterByID(ctx, chapterID)
	if err != nil || c.ProjectID != a.Project.ID {
		return nil, core.ErrNotFound()
	}
	if c.Locked {
		return nil, lockedErr(ctx, i18n.T(ctx, "studio.lock.chapter", "n", c.Number))
	}
	var p *Page
	err = s.db.WithTx(ctx, func(tx *db.Tx) error {
		if number <= 0 {
			if err := tx.Get(ctx, &number, `SELECT COALESCE(MAX(number), 0) + 1 FROM pages WHERE chapter_id = ?`, chapterID); err != nil {
				return err
			}
		}
		var err error
		p, err = s.insertPage(ctx, tx, a.Project, c, number, "")
		return err
	})
	if err != nil {
		return nil, err
	}
	s.touchProject(ctx, s.db, a.Project.ID)
	s.Publish(ctx, a.Project.ID, "studio.page.created", Event{Kind: "page", ID: p.ID, ChapterID: c.ID, PageID: p.ID})
	return p, nil
}

func (s *Service) insertPage(ctx context.Context, q db.Querier, pr *Project, c *Chapter, number int, layoutID string) (*Page, error) {
	now := s.now()
	p := &Page{ID: db.NewID(), ProjectID: pr.ID, ChapterID: c.ID, Number: number, LayoutID: layoutID, Size: pr.PageSize,
		Width: pr.PageWidth, Height: pr.PageHeight, Status: "draft", Version: 1, CreatedAt: now, UpdatedAt: now}
	if p.Width == 0 {
		size := PageSizeFor(pr.PageSize)
		p.Width, p.Height = size.Width, size.Height
	}
	_, err := q.Exec(ctx, `INSERT INTO pages (id, project_id, chapter_id, number, layout_id, size, width, height, status, version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'draft', 1, ?, ?)`, p.ID, p.ProjectID, p.ChapterID, p.Number, p.LayoutID, p.Size, p.Width, p.Height, now, now)
	return p, err
}

// DeletePage deletes an unlocked page and renumbers the pages after it.
func (s *Service) DeletePage(ctx context.Context, a *Access, id string) error {
	if err := s.require(ctx, a, EditPages); err != nil {
		return err
	}
	p, err := s.Page(ctx, id)
	if err != nil || p.ProjectID != a.Project.ID {
		return core.ErrNotFound()
	}
	if p.Locked {
		return lockedErr(ctx, i18n.T(ctx, "studio.lock.page", "n", p.Number))
	}
	err = s.db.WithTx(ctx, func(tx *db.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM pages WHERE id = ?`, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE pages SET number = number - 1 WHERE chapter_id = ? AND number > ?`, p.ChapterID, p.Number)
		return err
	})
	if err == nil {
		s.Publish(ctx, a.Project.ID, "studio.page.deleted", Event{Kind: "page", ID: id, ChapterID: p.ChapterID, PageID: id})
	}
	return err
}

// SetPageLock locks or unlocks a page. Agents may lock but never unlock.
func (s *Service) SetPageLock(ctx context.Context, a *Access, id string, locked bool) error {
	if err := s.require(ctx, a, EditPages); err != nil {
		return err
	}
	p, err := s.Page(ctx, id)
	if err != nil || p.ProjectID != a.Project.ID {
		return core.ErrNotFound()
	}
	if !locked && a.Actor.IsAgent() {
		return core.ErrForbidden(i18n.T(ctx, "studio.error.unlock_human"))
	}
	var by, at any
	if locked {
		by, at = db.NullString(a.Actor.UserID), s.now()
	}
	if _, err := s.db.Exec(ctx, `UPDATE pages SET locked = ?, locked_by = ?, locked_at = ?, updated_at = ? WHERE id = ?`, locked, by, at, s.now(), id); err != nil {
		return err
	}
	s.Publish(ctx, a.Project.ID, "studio.page.updated", Event{Kind: "page_lock", ID: id, PageID: id, ChapterID: p.ChapterID})
	return nil
}

// SetPanelLock locks or unlocks a panel. Agents may lock but never unlock.
func (s *Service) SetPanelLock(ctx context.Context, a *Access, id string, locked bool) error {
	if err := s.require(ctx, a, EditPages); err != nil {
		return err
	}
	pn, err := s.Panel(ctx, id)
	if err != nil || pn.ProjectID != a.Project.ID {
		return core.ErrNotFound()
	}
	if !locked && a.Actor.IsAgent() {
		return core.ErrForbidden(i18n.T(ctx, "studio.error.unlock_human"))
	}
	var by, at any
	if locked {
		by, at = db.NullString(a.Actor.UserID), s.now()
	}
	if _, err := s.db.Exec(ctx, `UPDATE panels SET locked = ?, locked_by = ?, locked_at = ?, updated_at = ? WHERE id = ?`, locked, by, at, s.now(), id); err != nil {
		return err
	}
	s.Publish(ctx, a.Project.ID, "studio.page.updated", Event{Kind: "panel_lock", ID: id, PageID: pn.PageID})
	return nil
}

// SetLayerLock locks or unlocks a layer. Agents may lock but never unlock.
func (s *Service) SetLayerLock(ctx context.Context, a *Access, id string, locked bool) error {
	l, err := s.Layer(ctx, id)
	if err != nil {
		return err
	}
	pn, err := s.Panel(ctx, l.PanelID)
	if err != nil || pn.ProjectID != a.Project.ID {
		return core.ErrNotFound()
	}
	if !a.CanEditLayerKind(l.Kind) {
		return core.ErrForbidden(i18n.T(ctx, "studio.error.layer_scope"))
	}
	if !locked && a.Actor.IsAgent() {
		return core.ErrForbidden(i18n.T(ctx, "studio.error.unlock_human"))
	}
	if _, err := s.db.Exec(ctx, `UPDATE layers SET locked = ?, updated_at = ? WHERE id = ?`, locked, s.now(), id); err != nil {
		return err
	}
	s.Publish(ctx, a.Project.ID, "studio.page.updated", Event{Kind: "layer_lock", ID: id, PageID: pn.PageID})
	return nil
}

// Panels lists a page's panels in reading order.
func (s *Service) Panels(ctx context.Context, pageID string) ([]Panel, error) {
	var out []Panel
	err := s.db.Select(ctx, &out, `SELECT `+panelCols+` FROM panels WHERE page_id = ? ORDER BY order_idx, created_at`, pageID)
	return out, err
}

// Panel returns a panel by id.
func (s *Service) Panel(ctx context.Context, id string) (*Panel, error) {
	var p Panel
	if err := s.db.Get(ctx, &p, `SELECT `+panelCols+` FROM panels WHERE id = ?`, id); err != nil {
		return nil, notFound(err)
	}
	return &p, nil
}

// Layers lists a panel's layers bottom to top.
func (s *Service) Layers(ctx context.Context, panelID string) ([]Layer, error) {
	var out []Layer
	err := s.db.Select(ctx, &out, `SELECT `+layerCols+` FROM layers WHERE panel_id = ? ORDER BY order_idx, created_at`, panelID)
	return out, err
}

// Layer returns a layer by id.
func (s *Service) Layer(ctx context.Context, id string) (*Layer, error) {
	var l Layer
	if err := s.db.Get(ctx, &l, `SELECT `+layerCols+` FROM layers WHERE id = ?`, id); err != nil {
		return nil, notFound(err)
	}
	return &l, nil
}

// Bubbles lists a page's bubbles in reading order.
func (s *Service) Bubbles(ctx context.Context, pageID string) ([]Bubble, error) {
	var out []Bubble
	err := s.db.Select(ctx, &out, `SELECT `+bubbleCols+` FROM bubbles WHERE page_id = ? ORDER BY order_idx, created_at`, pageID)
	return out, err
}

// BubbleTexts lists the texts of a page's bubbles (all languages).
func (s *Service) BubbleTexts(ctx context.Context, pageID string) ([]BubbleText, error) {
	var out []BubbleText
	err := s.db.Select(ctx, &out, `SELECT t.id, t.bubble_id, t.lang, t.text, t.status, t.source, t.fit, COALESCE(t.updated_by, '') AS updated_by, t.created_at, t.updated_at FROM bubble_texts t JOIN bubbles b ON b.id = t.bubble_id
		WHERE b.page_id = ? ORDER BY b.order_idx, t.lang`, pageID)
	return out, err
}
