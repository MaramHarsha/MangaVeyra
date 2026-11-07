package pagestore

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
)

// refreshChapterProvenance derives each page role from all of the chapter's
// current content. The chapter row is already locked by page mutations; taking
// that lock here as well keeps aggregation and its merged JSON write atomic.
// Empty layers and empty bubbles do not invent an unrecorded human role.
func refreshChapterProvenance(ctx context.Context, tx *db.Tx, chapterID string) error {
	var raw string
	if err := tx.Get(ctx, &raw, `SELECT ai_label FROM chapters WHERE id = ?`+rowLock(tx), chapterID); err != nil {
		return err
	}
	labels := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &labels); err != nil {
		return err
	}
	if labels == nil {
		labels = map[string]string{}
	}
	var art, lettering []string
	if err := tx.Select(ctx, &art, `SELECT DISTINCT l.provenance FROM layers l
		JOIN panels pn ON pn.id = l.panel_id JOIN pages p ON p.id = pn.page_id
		WHERE p.chapter_id = ? AND l.kind <> 'lettering' AND l.asset_id IS NOT NULL`, chapterID); err != nil {
		return err
	}
	if err := tx.Select(ctx, &lettering, `SELECT DISTINCT t.source FROM bubble_texts t
		JOIN bubbles b ON b.id = t.bubble_id JOIN pages p ON p.id = b.page_id
		WHERE p.chapter_id = ? AND TRIM(t.text) <> ''`, chapterID); err != nil {
		return err
	}
	var layouts []string
	if err := tx.Select(ctx, &layouts, `SELECT DISTINCT l.provenance FROM layers l
		JOIN panels pn ON pn.id = l.panel_id JOIN pages p ON p.id = pn.page_id
		WHERE p.chapter_id = ? AND l.kind = 'lettering' AND l.provenance IN ('ai', 'mixed')
		AND EXISTS (SELECT 1 FROM bubbles b JOIN bubble_texts t ON t.bubble_id = b.id
			WHERE b.panel_id = pn.id AND TRIM(t.text) <> '')`, chapterID); err != nil {
		return err
	}
	lettering = append(lettering, layouts...)
	for role, sources := range map[string][]string{"art": art, "lettering": lettering} {
		label := provenanceLabel(sources)
		if label == "" {
			delete(labels, role)
		} else {
			labels[role] = label
		}
	}
	_, err := tx.Exec(ctx, `UPDATE chapters SET ai_label = ? WHERE id = ?`, db.MustJSON(labels), chapterID)
	return err
}

func provenanceLabel(sources []string) string {
	human, ai := false, false
	for _, source := range sources {
		switch source {
		case pagedoc.ProvenanceHuman:
			human = true
		case pagedoc.ProvenanceAI:
			ai = true
		case pagedoc.ProvenanceMixed:
			human, ai = true, true
		}
	}
	switch {
	case human && ai:
		return "ai_assisted"
	case ai:
		return "ai"
	case human:
		return "human"
	default:
		return ""
	}
}

// Bubble text has a human/AI source, while the lettering layer also records the
// contribution to its placement and styling. Retain both contributions when a
// human edits AI lettering, including a geometry-only edit or a complete text
// rewrite. A revert restores the snapshot's own provenance instead.
func markHumanLettering(old pagedoc.Page, next *pagedoc.Page, ch Change) {
	if ch.ActorKind != "user" || ch.JobID != "" || ch.Action == "revert" {
		return
	}
	for i := range next.Panels {
		pn := &next.Panels[i]
		before, after := panelBubbles(old, pn.ID), panelBubbles(*next, pn.ID)
		if reflect.DeepEqual(before, after) {
			continue
		}
		if len(after) == 0 {
			// Removing every bubble also removes its AI placement; a later
			// newly written bubble must not inherit that deleted contribution.
			if l := pn.LayerOfKind(pagedoc.LayerLettering, ""); l != nil {
				l.Provenance = pagedoc.ProvenanceHuman
			}
			continue
		}
		ai := false
		for _, bubble := range before {
			for _, text := range bubble.Texts {
				ai = ai || text.Source == pagedoc.ProvenanceAI
			}
		}
		if op := old.Panel(pn.ID); op != nil {
			if l := op.LayerOfKind(pagedoc.LayerLettering, ""); l != nil {
				ai = ai || l.Provenance == pagedoc.ProvenanceAI || l.Provenance == pagedoc.ProvenanceMixed
			}
		}
		if !ai {
			continue
		}
		l := pn.LayerOfKind(pagedoc.LayerLettering, "")
		if l == nil {
			pn.Layers = append(pn.Layers, pagedoc.Layer{ID: db.NewID(), Kind: pagedoc.LayerLettering,
				Name: "Lettering", Order: len(pn.Layers), Visible: true, Opacity: 1, Blend: "normal"})
			l = &pn.Layers[len(pn.Layers)-1]
		}
		l.Provenance = pagedoc.ProvenanceMixed
	}
}
