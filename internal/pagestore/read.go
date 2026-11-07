package pagestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

type store struct {
	db     *db.DB
	studio studio.Service
	assets *assets.Service
}

var _ Service = (*store)(nil)

const maxChapterPages = 256

func rowLock(q db.Querier) string {
	if _, ok := q.(*db.Tx); ok && q.Dialect() == db.Postgres {
		return " FOR UPDATE"
	}
	return ""
}

func lookupError(err error) error {
	if db.IsNotFound(err) {
		return ErrNotFound
	}
	return fmt.Errorf("pagestore: query: %w", err)
}

type pageRow struct {
	ID             string         `db:"id"`
	ProjectID      string         `db:"project_id"`
	ChapterID      string         `db:"chapter_id"`
	ChapterProject string         `db:"chapter_project"`
	Number         int            `db:"number"`
	Width          int            `db:"width"`
	Height         int            `db:"height"`
	LayoutID       string         `db:"layout_id"`
	Locked         bool           `db:"locked"`
	LockedBy       sql.NullString `db:"locked_by"`
	Version        int            `db:"version"`
	Thumb          sql.NullString `db:"thumb_asset_id"`
	Note           string         `db:"notes"`
	Format         string         `db:"format"`
}

// GetPage reads every normalized table in one consistent snapshot.
func (s *store) GetPage(ctx context.Context, id string) (pagedoc.Page, error) {
	var out pagedoc.Page
	err := s.db.WithReadTx(ctx, func(tx *db.Tx) error {
		var err error
		out, err = s.load(ctx, tx, strings.TrimSpace(id), false)
		return err
	})
	return out, err
}

func (s *store) GetPageTx(ctx context.Context, tx *db.Tx, id string) (pagedoc.Page, error) {
	if tx == nil {
		return pagedoc.Page{}, ErrInvalid
	}
	return s.load(ctx, tx, strings.TrimSpace(id), true)
}

func (s *store) load(ctx context.Context, q db.Querier, id string, lock bool) (pagedoc.Page, error) {
	var r pageRow
	query := `SELECT pg.id, pg.project_id, pg.chapter_id, c.project_id AS chapter_project, pg.number,
		pg.width, pg.height, pg.layout_id, pg.locked, pg.locked_by, pg.version, pg.thumb_asset_id,
		pg.notes, pr.format FROM pages pg JOIN chapters c ON c.id = pg.chapter_id
		JOIN projects pr ON pr.id = c.project_id WHERE pg.id = ?`
	if lock && q.Dialect() == db.Postgres {
		query += " FOR UPDATE OF c, pg"
	}
	if err := q.Get(ctx, &r, query, id); err != nil {
		return pagedoc.Page{}, lookupError(err)
	}
	if r.ProjectID != r.ChapterProject {
		return pagedoc.Page{}, ErrInvalid
	}
	p := pagedoc.New(r.ID, r.ProjectID, r.ChapterID, r.Number, r.Width, r.Height, pagedoc.DirectionForFormat(r.Format))
	p.Version, p.LayoutID, p.Locked, p.LockedBy, p.Note = r.Version, r.LayoutID, r.Locked, r.LockedBy.String, r.Note
	if r.Thumb.Valid {
		var err error
		p.Thumb, err = assetRef(ctx, q, r.Thumb.String, p.ProjectID)
		if err != nil {
			return pagedoc.Page{}, err
		}
	}
	type panelRow struct {
		ID        string         `db:"id"`
		ProjectID string         `db:"project_id"`
		Order     int            `db:"order_idx"`
		Points    string         `db:"points"`
		Script    sql.NullString `db:"script_panel_id"`
		Prompt    string         `db:"prompt"`
		Source    string         `db:"prompt_source"`
		Camera    string         `db:"camera"`
		Shot      string         `db:"shot"`
		Style     string         `db:"style"`
		Pose      string         `db:"pose"`
		Locked    bool           `db:"locked"`
		LockedBy  sql.NullString `db:"locked_by"`
		Alt       string         `db:"alt_text"`
	}
	var panels []panelRow
	if err := q.Select(ctx, &panels, `SELECT id, project_id, order_idx, points, script_panel_id, prompt,
		prompt_source, camera, shot, style, pose, locked, locked_by, alt_text FROM panels WHERE page_id = ? ORDER BY order_idx, id`, id); err != nil {
		return pagedoc.Page{}, lookupError(err)
	}
	for _, pr := range panels {
		if pr.ProjectID != p.ProjectID {
			return pagedoc.Page{}, ErrInvalid
		}
		pn := pagedoc.Panel{ID: pr.ID, Order: pr.Order, ScriptPanelID: pr.Script.String, Prompt: pr.Prompt,
			PromptSource: pr.Source, Camera: pr.Camera, Shot: pr.Shot, Style: pr.Style, Locked: pr.Locked,
			LockedBy: pr.LockedBy.String, AltText: pr.Alt, Layers: []pagedoc.Layer{}}
		if err := json.Unmarshal([]byte(pr.Points), &pn.Points); err != nil {
			return pagedoc.Page{}, ErrInvalid
		}
		for i := range pn.Points {
			pn.Points[i].X *= float64(p.Width) / 100
			pn.Points[i].Y *= float64(p.Height) / 100
		}
		if err := json.Unmarshal([]byte(pr.Pose), &pn.Pose); err != nil {
			return pagedoc.Page{}, ErrInvalid
		}
		if err := s.loadLayers(ctx, q, p.ProjectID, &pn); err != nil {
			return pagedoc.Page{}, err
		}
		p.Panels = append(p.Panels, pn)
	}
	if err := s.loadBubbles(ctx, q, &p); err != nil {
		return pagedoc.Page{}, err
	}
	return p, nil
}

func (s *store) loadLayers(ctx context.Context, q db.Querier, projectID string, pn *pagedoc.Panel) error {
	type layerRow struct {
		ID         string          `db:"id"`
		Kind       string          `db:"kind"`
		Name       string          `db:"name"`
		Character  sql.NullString  `db:"character_id"`
		Asset      sql.NullString  `db:"asset_id"`
		Mask       sql.NullString  `db:"mask_asset_id"`
		Order      int             `db:"order_idx"`
		Visible    bool            `db:"visible"`
		Locked     bool            `db:"locked"`
		Opacity    float64         `db:"opacity"`
		Blend      string          `db:"blend"`
		Transform  string          `db:"transform"`
		Provenance string          `db:"provenance"`
		Model      string          `db:"model_id"`
		Provider   string          `db:"provider_id"`
		Job        sql.NullString  `db:"job_id"`
		Seed       sql.NullInt64   `db:"seed"`
		Prompt     string          `db:"prompt"`
		Score      sql.NullFloat64 `db:"lock_score"`
	}
	var rows []layerRow
	if err := q.Select(ctx, &rows, `SELECT id, kind, name, character_id, asset_id, mask_asset_id, order_idx,
		visible, locked, opacity, blend, transform, provenance, model_id, provider_id, job_id, seed, prompt,
		lock_score FROM layers WHERE panel_id = ? ORDER BY order_idx, id`, pn.ID); err != nil {
		return lookupError(err)
	}
	for _, r := range rows {
		l := pagedoc.Layer{ID: r.ID, Kind: r.Kind, Name: r.Name, CharacterID: r.Character.String, Order: r.Order,
			Visible: r.Visible, Locked: r.Locked, Opacity: r.Opacity, Blend: r.Blend, Provenance: r.Provenance,
			ModelID: r.Model, ProviderID: r.Provider, JobID: r.Job.String, Prompt: r.Prompt}
		if err := json.Unmarshal([]byte(r.Transform), &l.Transform); err != nil {
			return ErrInvalid
		}
		if r.Seed.Valid {
			v := r.Seed.Int64
			l.Seed = &v
		}
		if r.Score.Valid {
			v := r.Score.Float64
			l.LockScore = &v
		}
		var err error
		if r.Asset.Valid {
			l.Asset, err = assetRef(ctx, q, r.Asset.String, projectID)
			if err != nil {
				return err
			}
		}
		if r.Mask.Valid {
			l.Mask, err = assetRef(ctx, q, r.Mask.String, projectID)
			if err != nil {
				return err
			}
		}
		pn.Layers = append(pn.Layers, l)
	}
	return nil
}

type bubbleStyle struct {
	pagedoc.BubbleStyle
	Shape string `json:"shape"`
}

func (s *store) loadBubbles(ctx context.Context, q db.Querier, p *pagedoc.Page) error {
	type bubbleRow struct {
		ID       string         `db:"id"`
		Panel    sql.NullString `db:"panel_id"`
		Kind     string         `db:"kind"`
		Speaker  sql.NullString `db:"speaker_character_id"`
		Script   sql.NullString `db:"script_panel_id"`
		Order    int            `db:"order_idx"`
		Geometry string         `db:"geometry"`
		Tail     string         `db:"tail"`
		Style    string         `db:"style"`
		Vertical bool           `db:"vertical"`
	}
	var rows []bubbleRow
	if err := q.Select(ctx, &rows, `SELECT id, panel_id, kind, speaker_character_id, script_panel_id, order_idx,
		geometry, tail, style, vertical FROM bubbles WHERE page_id = ? ORDER BY order_idx, id`, p.ID); err != nil {
		return lookupError(err)
	}
	for _, r := range rows {
		b := pagedoc.Bubble{ID: r.ID, PanelID: r.Panel.String, Kind: r.Kind, SpeakerCharacterID: r.Speaker.String,
			ScriptPanelID: r.Script.String, Order: r.Order, Vertical: r.Vertical, Texts: map[string]pagedoc.BubbleText{}}
		var style bubbleStyle
		if json.Unmarshal([]byte(r.Geometry), &b.Box) != nil || json.Unmarshal([]byte(r.Tail), &b.Tail) != nil || json.Unmarshal([]byte(r.Style), &style) != nil {
			return ErrInvalid
		}
		b.Box.X *= float64(p.Width) / 100
		b.Box.Y *= float64(p.Height) / 100
		b.Box.W *= float64(p.Width) / 100
		b.Box.H *= float64(p.Height) / 100
		b.Tail.Tip.X *= float64(p.Width) / 100
		b.Tail.Tip.Y *= float64(p.Height) / 100
		b.Shape, b.Style = style.Shape, style.BubbleStyle
		type textRow struct {
			Lang   string `db:"lang"`
			Text   string `db:"text"`
			Status string `db:"status"`
			Source string `db:"source"`
			Fit    string `db:"fit"`
		}
		var texts []textRow
		if err := q.Select(ctx, &texts, `SELECT lang, text, status, source, fit FROM bubble_texts WHERE bubble_id = ? ORDER BY lang`, b.ID); err != nil {
			return lookupError(err)
		}
		for _, tr := range texts {
			var fit struct {
				Ruby []pagedoc.Ruby `json:"ruby"`
			}
			if json.Unmarshal([]byte(tr.Fit), &fit) != nil {
				return ErrInvalid
			}
			b.Texts[tr.Lang] = pagedoc.BubbleText{Text: tr.Text, Status: tr.Status, Source: tr.Source, Ruby: fit.Ruby}
		}
		p.Bubbles = append(p.Bubbles, b)
	}
	return nil
}

func assetRef(ctx context.Context, q db.Querier, id, projectID string) (*pagedoc.AssetRef, error) {
	var r struct {
		ID      string         `db:"id"`
		Project sql.NullString `db:"project_id"`
		Width   int            `db:"width"`
		Height  int            `db:"height"`
		MIME    string         `db:"content_type"`
	}
	if err := q.Get(ctx, &r, `SELECT id, project_id, width, height, content_type FROM assets WHERE id = ?`, id); err != nil {
		return nil, lookupError(err)
	}
	if !r.Project.Valid || r.Project.String != projectID || r.Width <= 0 || r.Height <= 0 {
		return nil, ErrInvalid
	}
	switch r.MIME {
	case "image/png", "image/jpeg", "image/webp":
	default:
		return nil, ErrInvalid
	}
	return &pagedoc.AssetRef{ID: r.ID, Width: r.Width, Height: r.Height}, nil
}

func (s *store) WithURLs(ctx context.Context, p *pagedoc.Page, ttl time.Duration) {
	if p == nil || s.assets == nil {
		return
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	fill := func(a *pagedoc.AssetRef) {
		if a != nil {
			a.URL = s.assets.URLFor(ctx, a.ID, ttl)
		}
	}
	fill(p.Thumb)
	for i := range p.Panels {
		for j := range p.Panels[i].Layers {
			fill(p.Panels[i].Layers[j].Asset)
			fill(p.Panels[i].Layers[j].Mask)
		}
	}
}

func (s *store) ListPages(ctx context.Context, chapterID string) ([]PageSummary, error) {
	var out []PageSummary
	err := s.db.WithReadTx(ctx, func(tx *db.Tx) error {
		type row struct {
			ID      string         `db:"id"`
			Number  int            `db:"number"`
			Panels  int            `db:"panels"`
			Locked  bool           `db:"locked"`
			Version int            `db:"version"`
			Thumb   sql.NullString `db:"thumb_asset_id"`
			Project string         `db:"project_id"`
			Updated time.Time      `db:"updated_at"`
			Layout  string         `db:"layout_id"`
		}
		var rows []row
		if err := tx.Select(ctx, &rows, `SELECT pg.id, pg.number, pg.locked, pg.version, pg.thumb_asset_id, pg.project_id,
			pg.updated_at, pg.layout_id, (SELECT COUNT(*) FROM panels pa WHERE pa.page_id = pg.id) AS panels
			FROM pages pg WHERE chapter_id = ? ORDER BY number, id LIMIT 257`, chapterID); err != nil {
			return lookupError(err)
		}
		if len(rows) > maxChapterPages {
			return ErrInvalid
		}
		out = make([]PageSummary, 0, len(rows))
		for _, r := range rows {
			p := PageSummary{ID: r.ID, Number: r.Number, Panels: r.Panels, Locked: r.Locked, Version: r.Version, Updated: r.Updated.UTC(), LayoutID: r.Layout}
			if r.Thumb.Valid {
				var err error
				p.Thumb, err = assetRef(ctx, tx, r.Thumb.String, r.Project)
				if err != nil {
					return err
				}
			}
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

func (s *store) ChapterPages(ctx context.Context, chapterID string) ([]pagedoc.Page, error) {
	var out []pagedoc.Page
	err := s.db.WithReadTx(ctx, func(tx *db.Tx) error {
		var ids []string
		if err := tx.Select(ctx, &ids, `SELECT id FROM pages WHERE chapter_id = ? ORDER BY number, id LIMIT 257`, chapterID); err != nil {
			return lookupError(err)
		}
		if len(ids) > maxChapterPages {
			return ErrInvalid
		}
		out = make([]pagedoc.Page, 0, len(ids))
		for _, id := range ids {
			p, err := s.load(ctx, tx, id, false)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return nil
	})
	return out, err
}
