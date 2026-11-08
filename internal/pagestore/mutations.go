package pagestore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

func (s *store) authorizePage(ctx context.Context, tx *db.Tx, id string, action studio.Action, ch Change) error {
	if tx == nil {
		return ErrInvalid
	}
	var projectID string
	if err := tx.Get(ctx, &projectID, `SELECT project_id FROM pages WHERE id = ?`, id); err != nil {
		return lookupError(err)
	}
	return s.authorize(ctx, tx, projectID, action, ch)
}

func (s *store) SetLayerAssetTx(ctx context.Context, tx *db.Tx, in SetLayerAsset, ch Change) (pagedoc.Layer, error) {
	if err := s.authorizePage(ctx, tx, in.PageID, studio.ActEditArt, ch); err != nil {
		return pagedoc.Layer{}, err
	}
	p, err := s.GetPageTx(ctx, tx, in.PageID)
	if err != nil {
		return pagedoc.Layer{}, err
	}
	layerID, err := setArtLayer(&p, in)
	if err != nil {
		return pagedoc.Layer{}, err
	}
	saved, err := s.save(ctx, tx, p, p.Version, ch)
	if err != nil {
		return pagedoc.Layer{}, err
	}
	return *saved.Panel(in.PanelID).Layer(layerID), nil
}

func setArtLayer(p *pagedoc.Page, in SetLayerAsset) (string, error) {
	pn := p.Panel(in.PanelID)
	if pn == nil {
		return "", ErrNotFound
	}
	if in.Kind == pagedoc.LayerLettering {
		return "", ErrInvalid
	}
	l := pn.Layer(in.LayerID)
	if in.LayerID != "" && l == nil {
		return "", ErrNotFound
	}
	if l != nil && (l.Kind == pagedoc.LayerLettering || (in.Kind != "" && l.Kind != in.Kind) || (in.CharacterID != "" && l.CharacterID != in.CharacterID)) {
		return "", ErrInvalid
	}
	if l == nil {
		l = pn.LayerOfKind(in.Kind, in.CharacterID)
	}
	if l == nil {
		switch in.Kind {
		case pagedoc.LayerBackground, pagedoc.LayerCharacter, pagedoc.LayerFX:
		default:
			return "", ErrInvalid
		}
		pn.Layers = append(pn.Layers, pagedoc.Layer{ID: db.NewID(), Kind: in.Kind, CharacterID: in.CharacterID,
			Name: in.Name, Visible: true, Opacity: 1, Blend: "normal", Provenance: pagedoc.ProvenanceAI})
		l = &pn.Layers[len(pn.Layers)-1]
	}
	if l.Locked || pn.Locked || p.Locked {
		return "", ErrLocked
	}
	l.Asset = &pagedoc.AssetRef{ID: in.AssetID}
	l.Mask = nil
	if in.MaskAssetID != "" {
		l.Mask = &pagedoc.AssetRef{ID: in.MaskAssetID}
	}
	if in.Name != "" {
		l.Name = in.Name
	}
	l.ModelID, l.ProviderID, l.JobID, l.Seed, l.Prompt, l.LockScore = in.ModelID, in.ProviderID, in.JobID, in.Seed, in.Prompt, in.LockScore
	l.Provenance = in.Provenance
	if l.Provenance == "" {
		l.Provenance = pagedoc.ProvenanceAI
	}
	return l.ID, nil
}

// SetLayerAssetsTx records one user-visible art operation as one version.
func (s *store) SetLayerAssetsTx(ctx context.Context, tx *db.Tx, pageID string, inputs []SetLayerAsset, ch Change) ([]pagedoc.Layer, error) {
	if len(inputs) == 0 || len(inputs) > pagedoc.MaxPanels*(pagedoc.MaxLayersPerPane-1) {
		return nil, ErrInvalid
	}
	if err := s.authorizePage(ctx, tx, pageID, studio.ActEditArt, ch); err != nil {
		return nil, err
	}
	p, err := s.GetPageTx(ctx, tx, pageID)
	if err != nil {
		return nil, err
	}
	if ch.ExpectedVersion > 0 && ch.ExpectedVersion != p.Version {
		return nil, ErrVersion
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, in := range inputs {
		if in.PageID != pageID {
			return nil, ErrInvalid
		}
		id, err := setArtLayer(&p, in)
		if err != nil {
			return nil, err
		}
		if seen[id] {
			return nil, ErrInvalid
		}
		seen[id] = true
		ids = append(ids, id)
	}
	saved, err := s.save(ctx, tx, p, p.Version, ch)
	if err != nil {
		return nil, err
	}
	out := make([]pagedoc.Layer, 0, len(ids))
	for i, id := range ids {
		out = append(out, *saved.Panel(inputs[i].PanelID).Layer(id))
	}
	return out, nil
}

func (s *store) SetBubblesTx(ctx context.Context, tx *db.Tx, pageID string, bubbles []pagedoc.Bubble, ch Change) error {
	if err := s.authorizePage(ctx, tx, pageID, studio.ActLetter, ch); err != nil {
		return err
	}
	p, err := s.GetPageTx(ctx, tx, pageID)
	if err != nil {
		return err
	}
	p.Bubbles = bubbles
	for i := range p.Bubbles {
		if !db.ValidID(p.Bubbles[i].ID) {
			p.Bubbles[i].ID = db.NewID()
		}
	}
	_, err = s.save(ctx, tx, p, p.Version, ch)
	return err
}

func (s *store) SetThumbTx(ctx context.Context, tx *db.Tx, pageID, assetID string) error {
	if tx == nil {
		return ErrInvalid
	}
	p, err := s.GetPageTx(ctx, tx, pageID)
	if err != nil {
		return err
	}
	if err := s.studio.CheckScopeWritableTx(ctx, tx, jobs.Scope{Kind: jobs.ScopePage, ProjectID: p.ProjectID, PageID: p.ID}); err != nil {
		return writable(err)
	}
	if _, err := assetRef(ctx, tx, assetID, p.ProjectID); err != nil {
		return err
	}
	// Thumbnails are derived cache data, so this does not invalidate the
	// editable document revision or append a second history entry.
	_, err = tx.Exec(ctx, `UPDATE pages SET thumb_asset_id = ? WHERE id = ?`, assetID, pageID)
	return err
}

func (s *store) SetLock(ctx context.Context, pageID, panelID, layerID string, locked bool, ch Change) error {
	if ch.ActorKind != "user" || ch.TokenID != "" {
		return studio.ErrForbidden
	}
	return s.db.WithTx(ctx, func(tx *db.Tx) error {
		if err := s.authorizePage(ctx, tx, pageID, studio.ActLock, ch); err != nil {
			return err
		}
		p, err := s.GetPageTx(ctx, tx, pageID)
		if err != nil {
			return err
		}
		if ch.ExpectedVersion != 0 && ch.ExpectedVersion != p.Version {
			return ErrVersion
		}
		if layerID != "" {
			pn := p.Panel(panelID)
			if pn == nil {
				return ErrNotFound
			}
			l := pn.Layer(layerID)
			if l == nil {
				return ErrNotFound
			}
			if l.Locked == locked {
				return nil
			}
			l.Locked = locked
		} else if panelID != "" {
			pn := p.Panel(panelID)
			if pn == nil {
				return ErrNotFound
			}
			if pn.Locked == locked {
				return nil
			}
			pn.Locked, pn.LockedBy = locked, ""
			if locked {
				pn.LockedBy = ch.ActorID
			}
		} else {
			if p.Locked == locked {
				return nil
			}
			p.Locked, p.LockedBy = locked, ""
			if locked {
				p.LockedBy = ch.ActorID
			}
		}
		p.Version++
		return persistPage(ctx, tx, p, ch)
	})
}

func (s *store) IsWritable(ctx context.Context, pageID, panelID, layerID string) error {
	p, err := s.GetPage(ctx, pageID)
	if err != nil {
		return err
	}
	scope := jobs.Scope{Kind: jobs.ScopePage, ProjectID: p.ProjectID, PageID: pageID}
	if panelID != "" {
		pn := p.Panel(panelID)
		if pn == nil {
			return ErrNotFound
		}
		scope.Kind, scope.PanelID = jobs.ScopePanel, panelID
		if layerID != "" {
			if pn.Layer(layerID) == nil {
				return ErrNotFound
			}
			scope.Kind, scope.PageID, scope.LayerID = jobs.ScopeLayer, "", layerID
		}
	} else if layerID != "" {
		return ErrInvalid
	}
	return writable(s.studio.CheckScopeWritable(ctx, scope))
}

func (s *store) ReorderPages(ctx context.Context, chapterID string, ids []string, ch Change) error {
	return s.db.WithTx(ctx, func(tx *db.Tx) error {
		if _, err := s.chapterForWrite(ctx, tx, chapterID, ch); err != nil {
			return err
		}
		var stored []string
		if err := tx.Select(ctx, &stored, `SELECT id FROM pages WHERE chapter_id = ? ORDER BY number, id`+rowLock(tx), chapterID); err != nil {
			return err
		}
		if len(ids) != len(stored) || len(ids) > maxChapterPages {
			return ErrInvalid
		}
		valid := map[string]bool{}
		for _, id := range stored {
			valid[id] = true
		}
		for i, id := range ids {
			if !valid[id] {
				return ErrInvalid
			}
			delete(valid, id)
			p, err := s.GetPageTx(ctx, tx, id)
			if err != nil {
				return err
			}
			if anyLocked(p) {
				return ErrLocked
			}
			p.Number, p.Version = i+1, p.Version+1
			if err := persistPage(ctx, tx, p, ch); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *store) DeletePage(ctx context.Context, id string, ch Change) error {
	if ch.ActorKind != "user" || ch.TokenID != "" {
		return studio.ErrForbidden
	}
	return s.db.WithTx(ctx, func(tx *db.Tx) error {
		if err := s.authorizePage(ctx, tx, id, studio.ActEditArt, ch); err != nil {
			return err
		}
		p, err := s.GetPageTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if ch.ExpectedVersion != 0 && ch.ExpectedVersion != p.Version {
			return ErrVersion
		}
		if err := s.studio.CheckScopeWritableTx(ctx, tx, jobs.Scope{Kind: jobs.ScopePage, ProjectID: p.ProjectID, PageID: id}); err != nil {
			return writable(err)
		}
		if anyLocked(p) {
			return ErrLocked
		}
		if _, err := tx.Exec(ctx, `DELETE FROM pages WHERE id = ?`, id); err != nil {
			return err
		}
		var ids []string
		if err := tx.Select(ctx, &ids, `SELECT id FROM pages WHERE chapter_id = ? ORDER BY number, id`+rowLock(tx), p.ChapterID); err != nil {
			return err
		}
		for i, remaining := range ids {
			doc, err := s.GetPageTx(ctx, tx, remaining)
			if err != nil {
				return err
			}
			if doc.Number == i+1 {
				continue
			}
			if anyLocked(doc) {
				return ErrLocked
			}
			doc.Number, doc.Version = i+1, doc.Version+1
			if err := persistPage(ctx, tx, doc, ch); err != nil {
				return err
			}
		}
		if err := refreshChapterProvenance(ctx, tx, p.ChapterID); err != nil {
			return err
		}
		return touchChapter(ctx, tx, p.ChapterID)
	})
}

func (s *store) Versions(ctx context.Context, pageID string, limit int) ([]Version, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}
	var out []Version
	err := s.db.Select(ctx, &out, `SELECT id, number, actor_kind AS "actorkind", COALESCE(actor_id, '') AS "actorid",
		COALESCE(job_id, '') AS "jobid", action, summary, created_at AS "createdat"
		FROM versions WHERE page_id = ? ORDER BY number DESC, id DESC LIMIT ?`, pageID, limit)
	if out == nil {
		out = []Version{}
	}
	for i := range out {
		out[i].CreatedAt = out[i].CreatedAt.UTC()
	}
	return out, err
}

func (s *store) VersionDoc(ctx context.Context, id string) (pagedoc.Page, error) {
	return versionDoc(ctx, s.db, id)
}

func versionDoc(ctx context.Context, q db.Querier, id string) (pagedoc.Page, error) {
	var raw string
	if err := q.Get(ctx, &raw, `SELECT snapshot FROM versions WHERE id = ?`, id); err != nil {
		return pagedoc.Page{}, lookupError(err)
	}
	if len(raw) > 4<<20 {
		return pagedoc.Page{}, ErrInvalid
	}
	var p pagedoc.Page
	if json.Unmarshal([]byte(raw), &p) != nil {
		return pagedoc.Page{}, ErrInvalid
	}
	clear := func(a *pagedoc.AssetRef) {
		if a != nil {
			a.URL = ""
		}
	}
	clear(p.Thumb)
	for i := range p.Panels {
		for j := range p.Panels[i].Layers {
			clear(p.Panels[i].Layers[j].Asset)
			clear(p.Panels[i].Layers[j].Mask)
		}
	}
	if err := p.Validate(); err != nil {
		return pagedoc.Page{}, fmt.Errorf("%w: stored snapshot", ErrInvalid)
	}
	return p, nil
}

func (s *store) Revert(ctx context.Context, pageID, versionID string, ch Change) (pagedoc.Page, error) {
	var out pagedoc.Page
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		if err := s.authorizePage(ctx, tx, pageID, studio.ActRead, ch); err != nil {
			return err
		}
		current, err := s.GetPageTx(ctx, tx, pageID)
		if err != nil {
			return err
		}
		if ch.ExpectedVersion != 0 && ch.ExpectedVersion != current.Version {
			return ErrVersion
		}
		old, err := versionDoc(ctx, tx, versionID)
		if err != nil {
			return err
		}
		if old.ID != pageID || old.ProjectID != current.ProjectID || old.ChapterID != current.ChapterID {
			return ErrInvalid
		}
		out, err = s.save(ctx, tx, old, current.Version, ch)
		return err
	})
	return out, err
}
