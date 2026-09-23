package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// PageDoc is the whole editable document of one page: the page, its panels
// with their layers, and its bubbles with their texts in every language. The
// editor island, MCP tools and generation handlers read and write pages
// through it; every save records a version (see Versions).
type PageDoc struct {
	Page    Page        `json:"page"`
	Panels  []PanelDoc  `json:"panels"`
	Bubbles []BubbleDoc `json:"bubbles"`
}

// PanelDoc is a panel with its layers (JSON flattens the panel fields).
type PanelDoc struct {
	Panel
	Layers []Layer `json:"layers"`
}

// BubbleDoc is a bubble with its texts (JSON flattens the bubble fields).
type BubbleDoc struct {
	Bubble
	Texts []BubbleText `json:"texts"`
}

// Panel returns the panel doc with id, or nil.
func (d *PageDoc) Panel(id string) *PanelDoc {
	for i := range d.Panels {
		if d.Panels[i].ID == id {
			return &d.Panels[i]
		}
	}
	return nil
}

// LoadPageDoc loads a page document.
func (s *Service) LoadPageDoc(ctx context.Context, pageID string) (*PageDoc, error) {
	return s.loadPageDoc(ctx, s.db, pageID)
}

func (s *Service) loadPageDoc(ctx context.Context, q db.Querier, pageID string) (*PageDoc, error) {
	var d PageDoc
	if err := q.Get(ctx, &d.Page, `SELECT `+pageCols+` FROM pages WHERE id = ?`, pageID); err != nil {
		return nil, notFound(err)
	}
	var panels []Panel
	if err := q.Select(ctx, &panels, `SELECT `+panelCols+` FROM panels WHERE page_id = ? ORDER BY order_idx, created_at`, pageID); err != nil {
		return nil, err
	}
	var layers []Layer
	if err := q.Select(ctx, &layers, `SELECT `+prefixed(layerCols, "l")+` FROM layers l JOIN panels p ON p.id = l.panel_id
		WHERE p.page_id = ? ORDER BY l.order_idx, l.created_at`, pageID); err != nil {
		return nil, err
	}
	byPanel := map[string][]Layer{}
	for _, l := range layers {
		byPanel[l.PanelID] = append(byPanel[l.PanelID], l)
	}
	d.Panels = make([]PanelDoc, 0, len(panels))
	for _, p := range panels {
		ls := byPanel[p.ID]
		if ls == nil {
			ls = []Layer{}
		}
		d.Panels = append(d.Panels, PanelDoc{Panel: p, Layers: ls})
	}
	var bubbles []Bubble
	if err := q.Select(ctx, &bubbles, `SELECT `+bubbleCols+` FROM bubbles WHERE page_id = ? ORDER BY order_idx, created_at`, pageID); err != nil {
		return nil, err
	}
	var texts []BubbleText
	if err := q.Select(ctx, &texts, `SELECT t.id, t.bubble_id, t.lang, t.text, t.status, t.source, t.fit, COALESCE(t.updated_by, '') AS updated_by,
		t.created_at, t.updated_at FROM bubble_texts t JOIN bubbles b ON b.id = t.bubble_id WHERE b.page_id = ? ORDER BY t.lang`, pageID); err != nil {
		return nil, err
	}
	byBubble := map[string][]BubbleText{}
	for _, t := range texts {
		byBubble[t.BubbleID] = append(byBubble[t.BubbleID], t)
	}
	d.Bubbles = make([]BubbleDoc, 0, len(bubbles))
	for _, b := range bubbles {
		ts := byBubble[b.ID]
		if ts == nil {
			ts = []BubbleText{}
		}
		d.Bubbles = append(d.Bubbles, BubbleDoc{Bubble: b, Texts: ts})
	}
	return &d, nil
}

// prefixed qualifies the plain column names of a column list with alias
// (only for lists without expressions; layerCols uses COALESCE, handled here).
func prefixed(cols, alias string) string {
	switch cols {
	case layerCols:
		return `l.id, l.panel_id, l.kind, l.name, COALESCE(l.character_id, '') AS character_id, COALESCE(l.asset_id, '') AS asset_id,
	COALESCE(l.mask_asset_id, '') AS mask_asset_id, l.order_idx, l.visible, l.locked, l.opacity, l.blend, l.transform, l.provenance, l.model_id,
	l.provider_id, COALESCE(l.job_id, '') AS job_id, l.seed, l.prompt, l.lock_score, l.created_at, l.updated_at`
	}
	return cols
}

// SaveOptions describe a page document save.
type SaveOptions struct {
	// Action is a short machine word for the version list ("edit", "generate", "layout", "restore", "merge"...).
	Action string
	// Summary is the human line shown in version history ("Regenerated panel 3 background").
	Summary string
	// Scope is the AI scope the change was made in (recorded with the version).
	Scope *jobs.Scope
	// BaseVersion, when > 0, must equal the page's current version (optimistic concurrency).
	BaseVersion int
}

var (
	validProvenance   = []string{"ai", "human", "mixed"}
	validPromptSource = []string{"script", "user", "agent"}
	validReview       = []string{"none", "open", "needs_changes", "done", "approved"}
	validBubbleKinds  = []string{BubbleSpeech, BubbleThought, BubbleShout, BubbleWhisper, BubbleCaption, BubbleSFX}
	validTextStatus   = []string{"draft", "machine", "reviewed", "approved", "overflow"}
)

func validPoint(v float64) bool { return !math.IsNaN(v) && v >= -0.001 && v <= 100.001 }

// validateDoc checks shapes and fills defaults and missing ids.
func validateDoc(ctx context.Context, d *PageDoc) error {
	errs := map[string]string{}
	bad := func(field string) { errs[field] = i18n.T(ctx, "studio.form.invalid") }
	panelIDs := map[string]bool{}
	for i := range d.Panels {
		p := &d.Panels[i]
		if p.ID == "" {
			p.ID = db.NewID()
		}
		panelIDs[p.ID] = true
		p.PageID, p.ProjectID, p.OrderIdx = d.Page.ID, d.Page.ProjectID, i
		if len(p.Points.V) < 3 || len(p.Points.V) > 64 {
			bad(fmt.Sprintf("panels[%d].points", i))
		}
		for _, pt := range p.Points.V {
			if !validPoint(pt[0]) || !validPoint(pt[1]) {
				bad(fmt.Sprintf("panels[%d].points", i))
			}
		}
		if p.PromptSource == "" {
			p.PromptSource = "user"
		}
		if !slices.Contains(validPromptSource, p.PromptSource) {
			bad(fmt.Sprintf("panels[%d].prompt_source", i))
		}
		if p.ReviewStatus == "" {
			p.ReviewStatus = "none"
		}
		if !slices.Contains(validReview, p.ReviewStatus) {
			bad(fmt.Sprintf("panels[%d].review_status", i))
		}
		p.Prompt, p.AltText = clip(p.Prompt, 4000), clip(p.AltText, 2000)
		for j := range p.Layers {
			l := &p.Layers[j]
			if l.ID == "" {
				l.ID = db.NewID()
			}
			l.PanelID, l.OrderIdx = p.ID, j
			if !slices.Contains(LayerKinds, l.Kind) {
				bad(fmt.Sprintf("panels[%d].layers[%d].kind", i, j))
			}
			if l.Provenance == "" {
				l.Provenance = "human"
			}
			if !slices.Contains(validProvenance, l.Provenance) {
				bad(fmt.Sprintf("panels[%d].layers[%d].provenance", i, j))
			}
			if l.Blend == "" {
				l.Blend = "normal"
			}
			if l.Opacity < 0 || l.Opacity > 1 || math.IsNaN(l.Opacity) {
				bad(fmt.Sprintf("panels[%d].layers[%d].opacity", i, j))
			}
		}
	}
	for i := range d.Bubbles {
		b := &d.Bubbles[i]
		if b.ID == "" {
			b.ID = db.NewID()
		}
		b.PageID, b.OrderIdx = d.Page.ID, i
		if b.PanelID != "" && !panelIDs[b.PanelID] {
			b.PanelID = ""
		}
		if b.Kind == "" {
			b.Kind = BubbleSpeech
		}
		if !slices.Contains(validBubbleKinds, b.Kind) {
			bad(fmt.Sprintf("bubbles[%d].kind", i))
		}
		g := b.Geometry.V
		if !validPoint(g.X) || !validPoint(g.Y) || g.W < 0 || g.H < 0 || g.W > 100 || g.H > 100 {
			bad(fmt.Sprintf("bubbles[%d].geometry", i))
		}
		seen := map[string]bool{}
		for j := range b.Texts {
			t := &b.Texts[j]
			if t.ID == "" {
				t.ID = db.NewID()
			}
			t.BubbleID = b.ID
			t.Text = clip(t.Text, 2000)
			if t.Lang == "" || seen[t.Lang] || len(t.Lang) > 16 {
				bad(fmt.Sprintf("bubbles[%d].texts[%d].lang", i, j))
			}
			seen[t.Lang] = true
			if t.Status == "" {
				t.Status = "draft"
			}
			if !slices.Contains(validTextStatus, t.Status) {
				bad(fmt.Sprintf("bubbles[%d].texts[%d].status", i, j))
			}
			if t.Source != "ai" {
				t.Source = "human"
			}
		}
	}
	if len(errs) > 0 {
		return core.ErrValidation(errs)
	}
	return nil
}

// fingerprint is a comparable form of a value without timestamps.
func fingerprint(v any) string {
	b, _ := json.Marshal(v)
	var m any
	_ = json.Unmarshal(b, &m)
	stripTimes(m)
	out, _ := json.Marshal(m)
	return string(out)
}

func stripTimes(v any) {
	switch t := v.(type) {
	case map[string]any:
		for _, k := range []string{"created_at", "updated_at", "locked_at", "order"} {
			delete(t, k)
		}
		for _, c := range t {
			stripTimes(c)
		}
	case []any:
		for _, c := range t {
			stripTimes(c)
		}
	}
}

// panelArt is a panel without its lettering layers (what art edits change).
func panelArt(p PanelDoc) string {
	cp := p
	cp.Layers = nil
	for _, l := range p.Layers {
		if l.Kind != LayerLettering {
			cp.Layers = append(cp.Layers, l)
		}
	}
	cp.Locked, cp.LockedBy = false, ""
	return fingerprint(cp)
}

func panelLettering(p PanelDoc) string {
	var ls []Layer
	for _, l := range p.Layers {
		if l.Kind == LayerLettering {
			ls = append(ls, l)
		}
	}
	return fingerprint(ls)
}

// checkDocChange enforces locks, roles and member scope for a save of next over cur.
func checkDocChange(ctx context.Context, a *Access, cur, next *PageDoc) error {
	human := !a.Actor.IsAgent() && a.Actor.Kind != ActorSystem
	if !a.CanEditPageNumber(cur.Page.Number) {
		return core.ErrForbidden(i18n.T(ctx, "studio.error.page_scope"))
	}
	if cur.Page.Locked && !human {
		return lockedErr(ctx, i18n.T(ctx, "studio.lock.page", "n", cur.Page.Number))
	}
	if cur.Page.Locked && !next.Page.Locked && !human {
		return core.ErrForbidden(i18n.T(ctx, "studio.error.unlock_human"))
	}
	curPanels := map[string]PanelDoc{}
	for _, p := range cur.Panels {
		curPanels[p.ID] = p
	}
	nextPanels := map[string]PanelDoc{}
	for _, p := range next.Panels {
		nextPanels[p.ID] = p
	}
	artAllowed := a.CanEditLayerKind(LayerBackground) && (a.Can(EditPages) || a.Can(EditArt))
	letterAllowed := a.CanEditLayerKind(LayerLettering)
	for id, cp := range curPanels {
		np, ok := nextPanels[id]
		if !human {
			if cp.Locked && (!ok || fingerprint(cp) != fingerprint(np)) {
				return lockedErr(ctx, i18n.T(ctx, "studio.lock.panel"))
			}
			for _, l := range cp.Layers {
				if !l.Locked {
					continue
				}
				found := false
				for _, nl := range np.Layers {
					if nl.ID == l.ID {
						found = true
						if fingerprint(nl) != fingerprint(l) {
							return lockedErr(ctx, i18n.T(ctx, "studio.lock.layer"))
						}
					}
				}
				if !found {
					return lockedErr(ctx, i18n.T(ctx, "studio.lock.layer"))
				}
			}
		}
		if !ok {
			if !artAllowed {
				return core.ErrForbidden(i18n.T(ctx, "studio.error.layer_scope"))
			}
			continue
		}
		if !artAllowed && panelArt(cp) != panelArt(np) {
			return core.ErrForbidden(i18n.T(ctx, "studio.error.layer_scope"))
		}
		if !letterAllowed && panelLettering(cp) != panelLettering(np) {
			return core.ErrForbidden(i18n.T(ctx, "studio.error.layer_scope"))
		}
		if cp.Locked != np.Locked && !a.Can(EditPages) {
			return core.ErrForbidden(i18n.T(ctx, "studio.error.role"))
		}
	}
	for id := range nextPanels {
		if _, ok := curPanels[id]; !ok && !artAllowed {
			return core.ErrForbidden(i18n.T(ctx, "studio.error.layer_scope"))
		}
	}
	if !letterAllowed && fingerprint(cur.Bubbles) != fingerprint(next.Bubbles) {
		return core.ErrForbidden(i18n.T(ctx, "studio.error.layer_scope"))
	}
	if !human {
		// Bubbles anchored to locked panels are part of the locked work.
		locked := map[string]bool{}
		for _, p := range cur.Panels {
			if p.Locked {
				locked[p.ID] = true
			}
		}
		curB := map[string]string{}
		for _, b := range cur.Bubbles {
			if locked[b.PanelID] {
				curB[b.ID] = fingerprint(b)
			}
		}
		for _, b := range next.Bubbles {
			if fp, ok := curB[b.ID]; ok {
				if fp != fingerprint(b) {
					return lockedErr(ctx, i18n.T(ctx, "studio.lock.panel"))
				}
				delete(curB, b.ID)
			} else if locked[b.PanelID] {
				return lockedErr(ctx, i18n.T(ctx, "studio.lock.panel"))
			}
		}
		if len(curB) > 0 {
			return lockedErr(ctx, i18n.T(ctx, "studio.lock.panel"))
		}
	}
	return nil
}

// SavePageDoc replaces a page's panels, layers, bubbles and texts with doc,
// after checking the actor's role, member scope and locks (AI, batch and
// agent actions never change locked pages, panels or layers; only humans
// unlock). It bumps the page version and records a Version with the actor
// kind and summary. It returns the saved document and the version.
func (s *Service) SavePageDoc(ctx context.Context, actor Actor, doc *PageDoc, o SaveOptions) (*PageDoc, *Version, error) {
	cur, err := s.LoadPageDoc(ctx, doc.Page.ID)
	if err != nil {
		return nil, nil, err
	}
	a, err := s.Access(ctx, actor, cur.Page.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	if !a.Can(EditPages) && !a.Can(EditArt) && !a.Can(EditLettering) {
		return nil, nil, core.ErrForbidden(i18n.T(ctx, "studio.error.role"))
	}
	if o.BaseVersion > 0 && o.BaseVersion != cur.Page.Version {
		return nil, nil, core.ErrConflict(i18n.T(ctx, "studio.error.stale"))
	}
	ch, err := s.ChapterByID(ctx, cur.Page.ChapterID)
	if err != nil {
		return nil, nil, err
	}
	if ch.Locked && actor.Kind != ActorUser {
		return nil, nil, lockedErr(ctx, i18n.T(ctx, "studio.lock.chapter", "n", ch.Number))
	}
	// Identity fields always come from the stored page.
	next := *doc
	next.Page.ID, next.Page.ProjectID, next.Page.ChapterID, next.Page.Number = cur.Page.ID, cur.Page.ProjectID, cur.Page.ChapterID, cur.Page.Number
	next.Page.Version, next.Page.BranchID = cur.Page.Version, cur.Page.BranchID
	if err := validateDoc(ctx, &next); err != nil {
		return nil, nil, err
	}
	if err := checkDocChange(ctx, a, cur, &next); err != nil {
		return nil, nil, err
	}
	var v *Version
	err = s.db.WithTx(ctx, func(tx *db.Tx) error {
		if err := s.writeDoc(ctx, tx, cur, &next, actor); err != nil {
			return err
		}
		var err error
		v, err = s.recordVersion(ctx, tx, next.Page.ID, actor, o)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	s.touchProject(ctx, s.db, cur.Page.ProjectID)
	s.Publish(ctx, cur.Page.ProjectID, "studio.page.updated", Event{Kind: "page", ID: cur.Page.ID, PageID: cur.Page.ID,
		ChapterID: cur.Page.ChapterID, Summary: o.Summary, ActorID: actor.UserID})
	saved, err := s.LoadPageDoc(ctx, cur.Page.ID)
	return saved, v, err
}

// writeDoc stores next (already validated) over cur inside tx.
func (s *Service) writeDoc(ctx context.Context, tx *db.Tx, cur, next *PageDoc, actor Actor) error {
	now := s.now()
	p := next.Page
	var lockedBy, lockedAt any
	if p.Locked {
		lockedBy, lockedAt = db.NullString(p.LockedBy), p.LockedAt
		if !cur.Page.Locked {
			lockedBy, lockedAt = db.NullString(actor.UserID), now
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE pages SET layout_id = ?, status = ?, locked = ?, locked_by = ?, locked_at = ?, thumb_asset_id = ?,
		art_asset_id = ?, notes = ?, version = version + 1, updated_at = ? WHERE id = ?`, clip(p.LayoutID, 80), clip(p.Status, 20), p.Locked,
		lockedBy, lockedAt, db.NullString(p.ThumbAssetID), db.NullString(p.ArtAssetID), clip(p.Notes, 4000), now, p.ID); err != nil {
		return err
	}
	keepPanels := map[string]bool{}
	for _, pn := range next.Panels {
		keepPanels[pn.ID] = true
	}
	for _, pn := range cur.Panels {
		if !keepPanels[pn.ID] {
			if _, err := tx.Exec(ctx, `DELETE FROM panels WHERE id = ?`, pn.ID); err != nil {
				return err
			}
		}
	}
	curLocked := map[string]bool{}
	for _, pn := range cur.Panels {
		curLocked[pn.ID] = pn.Locked
	}
	for _, pn := range next.Panels {
		var by, at any
		if pn.Locked {
			by, at = db.NullString(pn.LockedBy), pn.LockedAt
			if !curLocked[pn.ID] {
				by, at = db.NullString(actor.UserID), now
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO panels (id, project_id, page_id, order_idx, points, border, script_panel_id, prompt, prompt_source,
			camera, shot, pose, style, locked, locked_by, locked_at, alt_text, review_status, living, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET order_idx = excluded.order_idx, points = excluded.points, border = excluded.border,
			script_panel_id = excluded.script_panel_id, prompt = excluded.prompt, prompt_source = excluded.prompt_source,
			camera = excluded.camera, shot = excluded.shot, pose = excluded.pose, style = excluded.style, locked = excluded.locked,
			locked_by = excluded.locked_by, locked_at = excluded.locked_at, alt_text = excluded.alt_text,
			review_status = excluded.review_status, living = excluded.living, updated_at = excluded.updated_at`,
			pn.ID, p.ProjectID, p.ID, pn.OrderIdx, db.MustJSON(pn.Points.V), db.MustJSON(nonNilMap(pn.Border.V)), db.NullString(pn.ScriptPanelID),
			pn.Prompt, pn.PromptSource, clip(pn.Camera, 40), clip(pn.Shot, 60), db.MustJSON(nonNilMap(pn.Pose.V)), clip(pn.Style, 40),
			pn.Locked, by, at, pn.AltText, pn.ReviewStatus, db.MustJSON(nonNilMap(pn.Living.V)), now, now); err != nil {
			return err
		}
		keep := []string{}
		for _, l := range pn.Layers {
			keep = append(keep, l.ID)
			if _, err := tx.Exec(ctx, `INSERT INTO layers (id, panel_id, kind, name, character_id, asset_id, mask_asset_id, order_idx, visible,
				locked, opacity, blend, transform, provenance, model_id, provider_id, job_id, seed, prompt, lock_score, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (id) DO UPDATE SET panel_id = excluded.panel_id, kind = excluded.kind, name = excluded.name,
				character_id = excluded.character_id, asset_id = excluded.asset_id, mask_asset_id = excluded.mask_asset_id,
				order_idx = excluded.order_idx, visible = excluded.visible, locked = excluded.locked, opacity = excluded.opacity,
				blend = excluded.blend, transform = excluded.transform, provenance = excluded.provenance, model_id = excluded.model_id,
				provider_id = excluded.provider_id, job_id = excluded.job_id, seed = excluded.seed, prompt = excluded.prompt,
				lock_score = excluded.lock_score, updated_at = excluded.updated_at`,
				l.ID, pn.ID, l.Kind, clip(l.Name, 80), db.NullString(l.CharacterID), db.NullString(l.AssetID), db.NullString(l.MaskAssetID),
				l.OrderIdx, l.Visible, l.Locked, l.Opacity, l.Blend, db.MustJSON(nonNilMap(l.Transform.V)), l.Provenance, l.ModelID,
				l.ProviderID, db.NullString(l.JobID), l.Seed, clip(l.Prompt, 4000), l.LockScore, now, now); err != nil {
				return err
			}
		}
		if err := deleteMissing(ctx, tx, "layers", "panel_id", pn.ID, keep); err != nil {
			return err
		}
	}
	keepBubbles := []string{}
	for _, b := range next.Bubbles {
		keepBubbles = append(keepBubbles, b.ID)
		if _, err := tx.Exec(ctx, `INSERT INTO bubbles (id, page_id, panel_id, kind, speaker_character_id, script_panel_id, order_idx, geometry,
			tail, style, vertical, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET panel_id = excluded.panel_id, kind = excluded.kind, speaker_character_id = excluded.speaker_character_id,
			script_panel_id = excluded.script_panel_id, order_idx = excluded.order_idx, geometry = excluded.geometry, tail = excluded.tail,
			style = excluded.style, vertical = excluded.vertical, updated_at = excluded.updated_at`,
			b.ID, p.ID, db.NullString(b.PanelID), b.Kind, db.NullString(b.SpeakerCharacterID), db.NullString(b.ScriptPanelID), b.OrderIdx,
			db.MustJSON(b.Geometry.V), db.MustJSON(nonNilMap(b.Tail.V)), db.MustJSON(nonNilMap(b.Style.V)), b.Vertical, now, now); err != nil {
			return err
		}
		keepT := []string{}
		for _, t := range b.Texts {
			keepT = append(keepT, t.Lang)
			if _, err := tx.Exec(ctx, `INSERT INTO bubble_texts (id, bubble_id, lang, text, status, source, fit, updated_by, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (bubble_id, lang) DO UPDATE SET text = excluded.text, status = excluded.status, source = excluded.source,
				fit = excluded.fit, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
				t.ID, b.ID, t.Lang, t.Text, t.Status, t.Source, db.MustJSON(nonNilMap(t.Fit.V)), db.NullString(actor.UserID), now, now); err != nil {
				return err
			}
		}
		if err := deleteMissing(ctx, tx, "bubble_texts", "bubble_id", b.ID, keepT, "lang"); err != nil {
			return err
		}
	}
	return deleteMissing(ctx, tx, "bubbles", "page_id", p.ID, keepBubbles)
}

// deleteMissing deletes rows of table whose parent column equals parent and whose key column is not in keep.
func deleteMissing(ctx context.Context, tx *db.Tx, table, parentCol, parent string, keep []string, keyCol ...string) error {
	col := "id"
	if len(keyCol) > 0 {
		col = keyCol[0]
	}
	if len(keep) == 0 {
		_, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE `+parentCol+` = ?`, parent)
		return err
	}
	q, args, err := db.In(`DELETE FROM `+table+` WHERE `+parentCol+` = ? AND `+col+` NOT IN (?)`, parent, keep)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, q, args...)
	return err
}

// recordVersion snapshots the page inside tx as a new version on its current branch.
func (s *Service) recordVersion(ctx context.Context, tx *db.Tx, pageID string, actor Actor, o SaveOptions) (*Version, error) {
	doc, err := s.loadPageDoc(ctx, tx, pageID)
	if err != nil {
		return nil, err
	}
	var parent string
	var number int
	branch := doc.Page.BranchID
	cond, args := "branch_id IS NULL", []any{pageID}
	if branch != "" {
		cond, args = "branch_id = ?", []any{pageID, branch}
	}
	_ = tx.Get(ctx, &parent, `SELECT id FROM versions WHERE page_id = ? AND `+cond+` ORDER BY created_at DESC, id DESC LIMIT 1`, args...)
	_ = tx.Get(ctx, &number, `SELECT COALESCE(MAX(number), 0) FROM versions WHERE page_id = ?`, pageID)
	kind := actor.Kind
	if kind == "" {
		kind = ActorSystem
	}
	if o.Action == "" {
		o.Action = "edit"
	}
	scope := "{}"
	if o.Scope != nil {
		scope = db.MustJSON(o.Scope)
	}
	v := &Version{ID: db.NewID(), PageID: pageID, BranchID: branch, ParentID: parent, Number: number + 1, ActorKind: kind,
		ActorID: actor.UserID, TokenID: actor.TokenID, JobID: actor.JobID, Action: clip(o.Action, 40), Scope: scope,
		Summary: clip(o.Summary, 300), CreatedAt: s.now()}
	if _, err := tx.Exec(ctx, `INSERT INTO versions (id, page_id, branch_id, parent_id, number, actor_kind, actor_id, token_id, job_id, action,
		scope, summary, snapshot, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, v.ID, pageID, db.NullString(branch),
		db.NullString(parent), v.Number, v.ActorKind, db.NullString(v.ActorID), db.NullString(v.TokenID), db.NullString(v.JobID),
		v.Action, v.Scope, v.Summary, db.MustJSON(doc), v.CreatedAt); err != nil {
		return nil, err
	}
	if branch != "" {
		if _, err := tx.Exec(ctx, `UPDATE branches SET head_version_id = ? WHERE id = ?`, v.ID, branch); err != nil {
			return nil, err
		}
	}
	return v, nil
}

// timeOrNil helps templates.
func timeOrNil(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
