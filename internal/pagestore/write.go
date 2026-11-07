package pagestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

func changeActor(ch Change) (studio.Actor, error) {
	if !db.ValidID(ch.ActorID) || !utf8.ValidString(ch.Summary) || utf8.RuneCountInString(ch.Summary) > 500 ||
		len(ch.Action) > 80 || strings.TrimSpace(ch.Action) == "" {
		return studio.Actor{}, ErrInvalid
	}
	switch ch.ActorKind {
	case "user", "agent", "system":
	default:
		return studio.Actor{}, ErrInvalid
	}
	if ch.TokenID != "" && !db.ValidID(ch.TokenID) {
		return studio.Actor{}, ErrInvalid
	}
	if ch.JobID != "" && !db.ValidID(ch.JobID) {
		return studio.Actor{}, ErrInvalid
	}
	return studio.Actor{UserID: ch.ActorID, TokenID: ch.TokenID}, nil
}

func (s *store) authorize(ctx context.Context, tx *db.Tx, projectID string, action studio.Action, ch Change) error {
	actor, err := changeActor(ch)
	if err != nil {
		return err
	}
	if ch.JobID != "" && action != studio.ActLock && action != studio.ActLetter {
		action = studio.ActGenerate
	}
	return s.studio.AuthorizeTx(ctx, tx, actor, projectID, action)
}

func writable(err error) error {
	if errors.Is(err, studio.ErrLocked) {
		return ErrLocked
	}
	if errors.Is(err, studio.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

type chapterInfo struct {
	ProjectID string `db:"project_id"`
	Width     int    `db:"page_width"`
	Height    int    `db:"page_height"`
	Size      string `db:"page_size"`
	Format    string `db:"format"`
}

func (s *store) chapterForWrite(ctx context.Context, tx *db.Tx, chapterID string, ch Change) (chapterInfo, error) {
	if tx == nil {
		return chapterInfo{}, ErrInvalid
	}
	var ci chapterInfo
	if err := tx.Get(ctx, &ci, `SELECT c.project_id, p.page_width, p.page_height, p.page_size, p.format
		FROM chapters c JOIN projects p ON p.id = c.project_id WHERE c.id = ?`, chapterID); err != nil {
		return ci, lookupError(err)
	}
	if err := s.authorize(ctx, tx, ci.ProjectID, studio.ActEditArt, ch); err != nil {
		return ci, err
	}
	if err := s.studio.CheckScopeWritableTx(ctx, tx, jobs.Scope{Kind: jobs.ScopeChapter, ProjectID: ci.ProjectID, ChapterID: chapterID}); err != nil {
		return ci, writable(err)
	}
	return ci, nil
}

func (s *store) CreatePagesTx(ctx context.Context, tx *db.Tx, chapterID string, pages []pagedoc.Page, ch Change) ([]pagedoc.Page, error) {
	ci, err := s.chapterForWrite(ctx, tx, chapterID, ch)
	if err != nil {
		return nil, err
	}
	var count, last int
	if err := tx.Get(ctx, &count, `SELECT COUNT(*) FROM pages WHERE chapter_id = ?`, chapterID); err != nil {
		return nil, err
	}
	if len(pages) < 1 || count+len(pages) > maxChapterPages {
		return nil, ErrInvalid
	}
	if err := tx.Get(ctx, &last, `SELECT COALESCE(MAX(number), 0) FROM pages WHERE chapter_id = ?`, chapterID); err != nil {
		return nil, err
	}
	out := make([]pagedoc.Page, 0, len(pages))
	for i, doc := range pages {
		p, err := clonePage(doc)
		if err != nil {
			return nil, err
		}
		if (p.ProjectID != "" && p.ProjectID != ci.ProjectID) || (p.ChapterID != "" && p.ChapterID != chapterID) {
			return nil, ErrInvalid
		}
		p.ProjectID, p.ChapterID, p.Number = ci.ProjectID, chapterID, last+i+1
		if p.Width == 0 {
			p.Width = ci.Width
		}
		if p.Height == 0 {
			p.Height = ci.Height
		}
		p.Direction, p.Schema, p.Version, p.Locked, p.LockedBy = pagedoc.DirectionForFormat(ci.Format), pagedoc.Schema, 1, false, ""
		if err := newIDs(&p); err != nil {
			return nil, err
		}
		for j := range p.Panels {
			p.Panels[j].Locked, p.Panels[j].LockedBy = false, ""
			for k := range p.Panels[j].Layers {
				p.Panels[j].Layers[k].Locked = false
			}
		}
		if err := s.normalize(ctx, tx, &p); err != nil {
			return nil, err
		}
		now := db.Now()
		if _, err := tx.Exec(ctx, `INSERT INTO pages (id, project_id, chapter_id, number, layout_id, size, width, height,
			version, notes, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)`,
			p.ID, p.ProjectID, p.ChapterID, p.Number, p.LayoutID, ci.Size, p.Width, p.Height, p.Note, now, now); err != nil {
			return nil, fmt.Errorf("%w: insert page: %w", ErrInvalid, err)
		}
		if err := writeChildren(ctx, tx, p, now); err != nil {
			return nil, err
		}
		if err := snapshot(ctx, tx, p, ch, now); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := touchChapter(ctx, tx, chapterID); err != nil {
		return nil, err
	}
	if err := refreshChapterProvenance(ctx, tx, chapterID); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *store) ReplaceChapterPagesTx(ctx context.Context, tx *db.Tx, chapterID string, pages []pagedoc.Page, ch Change) ([]pagedoc.Page, error) {
	if _, err := s.chapterForWrite(ctx, tx, chapterID, ch); err != nil {
		return nil, err
	}
	if len(pages) == 0 || len(pages) > maxChapterPages {
		return nil, ErrInvalid
	}
	// Replacing existing work is destructive and is never exposed to tokens.
	if ch.TokenID != "" {
		return nil, studio.ErrForbidden
	}
	var ids []string
	if err := tx.Select(ctx, &ids, `SELECT id FROM pages WHERE chapter_id = ? ORDER BY number, id`+rowLock(tx), chapterID); err != nil {
		return nil, err
	}
	for _, id := range ids {
		p, err := s.GetPageTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if anyLocked(p) {
			return nil, ErrLocked
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM pages WHERE chapter_id = ?`, chapterID); err != nil {
		return nil, err
	}
	return s.CreatePagesTx(ctx, tx, chapterID, pages, ch)
}

func (s *store) SavePage(ctx context.Context, p pagedoc.Page, baseVersion int, ch Change) (pagedoc.Page, error) {
	var out pagedoc.Page
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		var err error
		out, err = s.save(ctx, tx, p, baseVersion, ch)
		return err
	})
	return out, err
}

func (s *store) save(ctx context.Context, tx *db.Tx, input pagedoc.Page, baseVersion int, ch Change) (pagedoc.Page, error) {
	// Resolve the owning project without trusting document ancestry, then
	// authorize before taking the page/descendant locks.
	var projectID string
	if err := tx.Get(ctx, &projectID, `SELECT project_id FROM pages WHERE id = ?`, input.ID); err != nil {
		return pagedoc.Page{}, lookupError(err)
	}
	if err := s.authorize(ctx, tx, projectID, studio.ActRead, ch); err != nil {
		return pagedoc.Page{}, err
	}
	old, err := s.GetPageTx(ctx, tx, input.ID)
	if err != nil {
		return pagedoc.Page{}, err
	}
	if baseVersion <= 0 || baseVersion != old.Version {
		return pagedoc.Page{}, ErrVersion
	}
	if input.ProjectID != old.ProjectID || input.ChapterID != old.ChapterID {
		return pagedoc.Page{}, ErrInvalid
	}
	p, err := clonePage(input)
	if err != nil {
		return pagedoc.Page{}, err
	}
	p.Schema, p.Number, p.Direction, p.Version = pagedoc.Schema, old.Number, old.Direction, old.Version
	p.Thumb = old.Thumb
	p.Locked, p.LockedBy = old.Locked, old.LockedBy
	for i := range p.Panels {
		op := old.Panel(p.Panels[i].ID)
		p.Panels[i].Locked, p.Panels[i].LockedBy = false, ""
		if op != nil {
			p.Panels[i].Locked, p.Panels[i].LockedBy = op.Locked, op.LockedBy
		}
		for j := range p.Panels[i].Layers {
			l := &p.Panels[i].Layers[j]
			l.Locked = false
			if op != nil {
				if ol := op.Layer(l.ID); ol != nil {
					l.Locked = ol.Locked
				}
			}
		}
	}
	if err := s.normalize(ctx, tx, &p); err != nil {
		return pagedoc.Page{}, err
	}
	if err := checkLockedChanges(old, p); err != nil {
		return pagedoc.Page{}, err
	}
	if err := s.studio.CheckScopeWritableTx(ctx, tx, jobs.Scope{Kind: jobs.ScopePage, ProjectID: old.ProjectID, PageID: old.ID}); err != nil {
		return pagedoc.Page{}, writable(err)
	}
	oldArt, newArt := old, p
	oldArt.Bubbles, newArt.Bubbles = nil, nil
	if !reflect.DeepEqual(oldArt, newArt) {
		if err := s.authorize(ctx, tx, projectID, studio.ActEditArt, ch); err != nil {
			return pagedoc.Page{}, err
		}
	}
	if !reflect.DeepEqual(old.Bubbles, p.Bubbles) {
		if err := s.authorize(ctx, tx, projectID, studio.ActLetter, ch); err != nil {
			return pagedoc.Page{}, err
		}
	}
	// A no-op still requires write permission and creates an auditable version.
	if reflect.DeepEqual(old, p) {
		if err := s.authorize(ctx, tx, projectID, studio.ActEditArt, ch); err != nil {
			return pagedoc.Page{}, err
		}
	}
	markHumanLettering(old, &p, ch)
	if err := p.Validate(); err != nil {
		return pagedoc.Page{}, ErrInvalid
	}
	p.Version = old.Version + 1
	if err := persistPage(ctx, tx, p, ch); err != nil {
		return pagedoc.Page{}, err
	}
	return p, nil
}

func clonePage(p pagedoc.Page) (pagedoc.Page, error) {
	b, err := json.Marshal(p)
	if err != nil || len(b) > 4<<20 {
		return pagedoc.Page{}, ErrInvalid
	}
	var out pagedoc.Page
	if json.Unmarshal(b, &out) != nil {
		return pagedoc.Page{}, ErrInvalid
	}
	return out, nil
}

func newIDs(p *pagedoc.Page) error {
	seen := map[string]bool{}
	allocate := func(id string) (string, error) {
		if id != "" && seen[id] {
			return "", ErrInvalid
		}
		if id != "" {
			seen[id] = true
		}
		if !db.ValidID(id) {
			return db.NewID(), nil
		}
		return id, nil
	}
	var err error
	p.ID, err = allocate(p.ID)
	if err != nil {
		return err
	}
	panelIDs := map[string]string{}
	for i := range p.Panels {
		old := p.Panels[i].ID
		p.Panels[i].ID, err = allocate(old)
		if err != nil {
			return err
		}
		if old != "" {
			panelIDs[old] = p.Panels[i].ID
		}
		for j := range p.Panels[i].Layers {
			p.Panels[i].Layers[j].ID, err = allocate(p.Panels[i].Layers[j].ID)
			if err != nil {
				return err
			}
		}
	}
	for i := range p.Bubbles {
		p.Bubbles[i].ID, err = allocate(p.Bubbles[i].ID)
		if err != nil {
			return err
		}
		if v, ok := panelIDs[p.Bubbles[i].PanelID]; ok {
			p.Bubbles[i].PanelID = v
		}
	}
	return nil
}

func (s *store) normalize(ctx context.Context, tx *db.Tx, p *pagedoc.Page) error {
	if !db.ValidID(p.ID) || !db.ValidID(p.ProjectID) || !db.ValidID(p.ChapterID) || !utf8.ValidString(p.Note) || utf8.RuneCountInString(p.Note) > 4000 {
		return ErrInvalid
	}
	if p.Panels == nil {
		p.Panels = []pagedoc.Panel{}
	}
	if p.Bubbles == nil {
		p.Bubbles = []pagedoc.Bubble{}
	}
	if p.Thumb != nil {
		var err error
		p.Thumb, err = assetRef(ctx, tx, p.Thumb.ID, p.ProjectID)
		if err != nil {
			return err
		}
	}
	for i := range p.Panels {
		pn := &p.Panels[i]
		if !db.ValidID(pn.ID) {
			return ErrInvalid
		}
		pn.Order = i
		if pn.PromptSource == "" {
			pn.PromptSource = "user"
		}
		if pn.Pose == nil {
			pn.Pose = map[string]any{}
		}
		if pn.Layers == nil {
			pn.Layers = []pagedoc.Layer{}
		}
		if pn.ScriptPanelID != "" {
			if err := sameParent(ctx, tx, "script_panels", "chapter_id", pn.ScriptPanelID, p.ChapterID); err != nil {
				return err
			}
		}
		for j := range pn.Layers {
			l := &pn.Layers[j]
			if !db.ValidID(l.ID) {
				return ErrInvalid
			}
			l.Order = j
			if l.Blend == "" {
				l.Blend = "normal"
			}
			if l.Provenance == "" {
				l.Provenance = pagedoc.ProvenanceHuman
			}
			if l.CharacterID != "" {
				if err := sameParent(ctx, tx, "characters", "project_id", l.CharacterID, p.ProjectID); err != nil {
					return err
				}
			}
			if l.Kind == pagedoc.LayerLettering && (l.Asset != nil || l.Mask != nil) {
				return fmt.Errorf("%w: lettering must remain structured text", ErrInvalid)
			}
			if l.Asset != nil {
				var err error
				l.Asset, err = assetRef(ctx, tx, l.Asset.ID, p.ProjectID)
				if err != nil {
					return err
				}
			}
			if l.Mask != nil {
				var err error
				l.Mask, err = assetRef(ctx, tx, l.Mask.ID, p.ProjectID)
				if err != nil {
					return err
				}
			}
		}
	}
	// Order is the bubble's reading position as the client states it; the
	// array position is incidental. Sort stably by it before renumbering so a
	// reordered reading sequence persists (equal values keep array order).
	sort.SliceStable(p.Bubbles, func(i, j int) bool { return p.Bubbles[i].Order < p.Bubbles[j].Order })
	for i := range p.Bubbles {
		b := &p.Bubbles[i]
		if !db.ValidID(b.ID) {
			return ErrInvalid
		}
		b.Order = i
		if b.SpeakerCharacterID != "" {
			if err := sameParent(ctx, tx, "characters", "project_id", b.SpeakerCharacterID, p.ProjectID); err != nil {
				return err
			}
		}
		if b.ScriptPanelID != "" {
			if err := sameParent(ctx, tx, "script_panels", "chapter_id", b.ScriptPanelID, p.ChapterID); err != nil {
				return err
			}
		}
		if len(b.Texts) == 0 {
			return ErrInvalid
		}
		for lang, text := range b.Texts {
			if text.Status == "" {
				text.Status = "draft"
			}
			if text.Source == "" {
				text.Source = "human"
			}
			b.Texts[lang] = text
		}
	}
	if err := p.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}

// table and parent are fixed literals supplied by callers, never request data.
func sameParent(ctx context.Context, tx *db.Tx, table, parent, id, expected string) error {
	var got string
	if err := tx.Get(ctx, &got, `SELECT `+parent+` FROM `+table+` WHERE id = ?`, id); err != nil {
		if db.IsNotFound(err) {
			return ErrInvalid
		}
		return err
	}
	if got != expected {
		return ErrInvalid
	}
	return nil
}

func jsonValue(v any) string { b, _ := json.Marshal(v); return string(b) }
func optional(s string) any  { return db.NullString(s) }
func assetID(a *pagedoc.AssetRef) any {
	if a == nil {
		return nil
	}
	return a.ID
}

func writeChildren(ctx context.Context, tx *db.Tx, p pagedoc.Page, now time.Time) error {
	for _, pn := range p.Panels {
		points := append([]pagedoc.Point(nil), pn.Points...)
		for i := range points {
			points[i].X *= 100 / float64(p.Width)
			points[i].Y *= 100 / float64(p.Height)
		}
		if err := writeOne(ctx, tx, `INSERT INTO panels (id, project_id, page_id, order_idx, points, script_panel_id,
			prompt, prompt_source, camera, shot, style, pose, locked, locked_by, alt_text, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET order_idx = excluded.order_idx, points = excluded.points,
			script_panel_id = excluded.script_panel_id, prompt = excluded.prompt, prompt_source = excluded.prompt_source,
			camera = excluded.camera, shot = excluded.shot, style = excluded.style, pose = excluded.pose,
			locked = excluded.locked, locked_by = excluded.locked_by, alt_text = excluded.alt_text, updated_at = excluded.updated_at
			WHERE panels.page_id = excluded.page_id AND panels.project_id = excluded.project_id`, pn.ID, p.ProjectID, p.ID, pn.Order,
			jsonValue(points), optional(pn.ScriptPanelID), pn.Prompt, pn.PromptSource, pn.Camera, pn.Shot, pn.Style,
			jsonValue(pn.Pose), pn.Locked, optional(pn.LockedBy), pn.AltText, now, now); err != nil {
			return fmt.Errorf("%w: insert panel: %w", ErrInvalid, err)
		}
		for _, l := range pn.Layers {
			if err := writeOne(ctx, tx, `INSERT INTO layers (id, panel_id, kind, name, character_id, asset_id, mask_asset_id,
				order_idx, visible, locked, opacity, blend, transform, provenance, model_id, provider_id, job_id, seed,
				prompt, lock_score, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (id) DO UPDATE SET panel_id = excluded.panel_id, kind = excluded.kind, name = excluded.name,
				character_id = excluded.character_id, asset_id = excluded.asset_id, mask_asset_id = excluded.mask_asset_id,
				order_idx = excluded.order_idx, visible = excluded.visible, locked = excluded.locked, opacity = excluded.opacity,
				blend = excluded.blend, transform = excluded.transform, provenance = excluded.provenance, model_id = excluded.model_id,
				provider_id = excluded.provider_id, job_id = excluded.job_id, seed = excluded.seed, prompt = excluded.prompt,
				lock_score = excluded.lock_score, updated_at = excluded.updated_at
				WHERE layers.panel_id IN (SELECT id FROM panels WHERE page_id = ?)`,
				l.ID, pn.ID, l.Kind, l.Name, optional(l.CharacterID), assetID(l.Asset), assetID(l.Mask), l.Order, l.Visible,
				l.Locked, l.Opacity, l.Blend, jsonValue(l.Transform), l.Provenance, l.ModelID, l.ProviderID, optional(l.JobID),
				l.Seed, l.Prompt, l.LockScore, now, now, p.ID); err != nil {
				return fmt.Errorf("%w: insert layer: %w", ErrInvalid, err)
			}
		}
	}
	for _, b := range p.Bubbles {
		box, tail := b.Box, b.Tail
		box.X *= 100 / float64(p.Width)
		box.W *= 100 / float64(p.Width)
		box.Y *= 100 / float64(p.Height)
		box.H *= 100 / float64(p.Height)
		tail.Tip.X *= 100 / float64(p.Width)
		tail.Tip.Y *= 100 / float64(p.Height)
		if err := writeOne(ctx, tx, `INSERT INTO bubbles (id, page_id, panel_id, kind, speaker_character_id, script_panel_id,
			order_idx, geometry, tail, style, vertical, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET panel_id = excluded.panel_id, kind = excluded.kind,
			speaker_character_id = excluded.speaker_character_id, script_panel_id = excluded.script_panel_id,
			order_idx = excluded.order_idx, geometry = excluded.geometry, tail = excluded.tail, style = excluded.style,
			vertical = excluded.vertical, updated_at = excluded.updated_at WHERE bubbles.page_id = excluded.page_id`,
			b.ID, p.ID, optional(b.PanelID), b.Kind, optional(b.SpeakerCharacterID), optional(b.ScriptPanelID), b.Order,
			jsonValue(box), jsonValue(tail), jsonValue(bubbleStyle{BubbleStyle: b.Style, Shape: b.Shape}), b.Vertical, now, now); err != nil {
			return fmt.Errorf("%w: insert bubble: %w", ErrInvalid, err)
		}
		for lang, text := range b.Texts {
			fit := struct {
				Ruby []pagedoc.Ruby `json:"ruby,omitempty"`
			}{Ruby: text.Ruby}
			if _, err := tx.Exec(ctx, `INSERT INTO bubble_texts (id, bubble_id, lang, text, status, source, fit, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (bubble_id, lang) DO UPDATE SET text = excluded.text,
				status = excluded.status, source = excluded.source, fit = excluded.fit, updated_at = excluded.updated_at`,
				db.NewID(), b.ID, lang, text.Text, text.Status, text.Source, jsonValue(fit), now, now); err != nil {
				return fmt.Errorf("%w: insert bubble text: %w", ErrInvalid, err)
			}
		}
	}
	// Prune only removed children, after all upserts so moving a retained layer
	// between this page's panels never deletes it. Kept panel rows preserve
	// continuity anchors and other metadata outside the page document.
	panelIDs := make([]string, 0, len(p.Panels))
	for _, pn := range p.Panels {
		panelIDs = append(panelIDs, pn.ID)
		layerIDs := make([]string, 0, len(pn.Layers))
		for _, l := range pn.Layers {
			layerIDs = append(layerIDs, l.ID)
		}
		if err := prune(ctx, tx, "layers", "panel_id", pn.ID, "id", layerIDs); err != nil {
			return err
		}
	}
	for _, b := range p.Bubbles {
		langs := make([]string, 0, len(b.Texts))
		for lang := range b.Texts {
			langs = append(langs, lang)
		}
		if err := prune(ctx, tx, "bubble_texts", "bubble_id", b.ID, "lang", langs); err != nil {
			return err
		}
	}
	bubbleIDs := make([]string, 0, len(p.Bubbles))
	for _, b := range p.Bubbles {
		bubbleIDs = append(bubbleIDs, b.ID)
	}
	if err := prune(ctx, tx, "bubbles", "page_id", p.ID, "id", bubbleIDs); err != nil {
		return err
	}
	return prune(ctx, tx, "panels", "page_id", p.ID, "id", panelIDs)
}

// A conflict with another page's child must fail the whole save, rather than
// silently skipping that row or moving private content between pages.
func writeOne(ctx context.Context, tx *db.Tx, query string, args ...any) error {
	result, err := tx.Exec(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrInvalid
	}
	return nil
}

// All SQL identifiers are fixed literals supplied above, never document data.
func prune(ctx context.Context, tx *db.Tx, table, parentColumn, parentID, keyColumn string, keep []string) error {
	query := `DELETE FROM ` + table + ` WHERE ` + parentColumn + ` = ?`
	args := []any{parentID}
	if len(keep) > 0 {
		query += ` AND ` + keyColumn + ` NOT IN (` + strings.TrimSuffix(strings.Repeat("?,", len(keep)), ",") + `)`
		for _, id := range keep {
			args = append(args, id)
		}
	}
	_, err := tx.Exec(ctx, query, args...)
	return err
}

func persistPage(ctx context.Context, tx *db.Tx, p pagedoc.Page, ch Change) error {
	now := db.Now()
	if _, err := tx.Exec(ctx, `UPDATE pages SET number = ?, layout_id = ?, width = ?, height = ?, version = ?,
		locked = ?, locked_by = ?, thumb_asset_id = ?, notes = ?, updated_at = ? WHERE id = ?`, p.Number, p.LayoutID,
		p.Width, p.Height, p.Version, p.Locked, optional(p.LockedBy), assetID(p.Thumb), p.Note, now, p.ID); err != nil {
		return err
	}
	if err := writeChildren(ctx, tx, p, now); err != nil {
		return err
	}
	if err := snapshot(ctx, tx, p, ch, now); err != nil {
		return err
	}
	if err := refreshChapterProvenance(ctx, tx, p.ChapterID); err != nil {
		return err
	}
	return touchChapter(ctx, tx, p.ChapterID)
}

func snapshot(ctx context.Context, tx *db.Tx, p pagedoc.Page, ch Change, now time.Time) error {
	// Signed URLs are response-only; normalization removed them before this call.
	var parent string
	err := tx.Get(ctx, &parent, `SELECT id FROM versions WHERE page_id = ? ORDER BY number DESC, id DESC LIMIT 1`, p.ID)
	if err != nil && !db.IsNotFound(err) {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO versions (id, page_id, parent_id, number, actor_kind, actor_id, token_id,
		job_id, action, scope, summary, snapshot, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		db.NewID(), p.ID, optional(parent), p.Version, ch.ActorKind, optional(ch.ActorID), optional(ch.TokenID), optional(ch.JobID),
		ch.Action, jsonValue(map[string]string{"kind": "page", "project_id": p.ProjectID, "page_id": p.ID}), ch.Summary, jsonValue(p), now)
	return err
}

func touchChapter(ctx context.Context, tx *db.Tx, id string) error {
	var before time.Time
	if err := tx.Get(ctx, &before, `SELECT updated_at FROM chapters WHERE id = ?`+rowLock(tx), id); err != nil {
		return err
	}
	now := db.Now()
	if !now.After(before) {
		now = before.Add(time.Microsecond)
	}
	_, err := tx.Exec(ctx, `UPDATE chapters SET updated_at = ? WHERE id = ?`, now, id)
	return err
}

func anyLocked(p pagedoc.Page) bool {
	if p.Locked {
		return true
	}
	for _, pn := range p.Panels {
		if pn.Locked {
			return true
		}
		for _, l := range pn.Layers {
			if l.Locked {
				return true
			}
		}
	}
	return false
}

func panelBubbles(p pagedoc.Page, id string) []pagedoc.Bubble {
	out := []pagedoc.Bubble{}
	for _, b := range p.Bubbles {
		if b.PanelID == id {
			out = append(out, b)
		}
	}
	return out
}

func checkLockedChanges(old, next pagedoc.Page) error {
	if old.Locked {
		return ErrLocked
	}
	for _, pn := range old.Panels {
		np := next.Panel(pn.ID)
		if pn.Locked && (np == nil || !reflect.DeepEqual(pn, *np) || !reflect.DeepEqual(panelBubbles(old, pn.ID), panelBubbles(next, pn.ID))) {
			return ErrLocked
		}
		for _, l := range pn.Layers {
			if !l.Locked {
				continue
			}
			if np == nil || !reflect.DeepEqual(pn.Points, np.Points) || old.Width != next.Width || old.Height != next.Height {
				return ErrLocked
			}
			nl := np.Layer(l.ID)
			if nl == nil || !reflect.DeepEqual(l, *nl) {
				return ErrLocked
			}
			if l.Kind == pagedoc.LayerLettering && !reflect.DeepEqual(panelBubbles(old, pn.ID), panelBubbles(next, pn.ID)) {
				return ErrLocked
			}
		}
	}
	return nil
}
