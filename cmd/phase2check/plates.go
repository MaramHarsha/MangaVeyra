package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// Only the scenery and ambient FX are intentionally repeated. All thirty
// character appearances are separate provider outputs, never copies of a ref.
func (h *harness) plates() error {
	list, err := h.pages.ListPages(h.ctx, h.s.ChapterID)
	if err != nil {
		return err
	}
	if len(list) != 30 {
		return fmt.Errorf("expected 30 pages")
	}
	plateIDs := make([]string, 3)
	plateJobs := make([]*jobs.Job, 3)
	settings := []string{
		"Empty original Japanese-inspired harbour at dawn: weathered stone quay, wet wooden footbridge, moored small boats, distant tide gate and soft sea mist. Fine black manga ink, screentone and clean perspective. An open walking path occupies the central foreground; scenery belongs at the sides and behind the independently drawn character. No people, no text.",
		"Empty original harbour tide gate interior: narrow wet stone walkway, heavy chain pulleys, arched floodgate machinery, ripples visible below a railing. Fine black manga ink, grey screentone, dawn light through high windows. Leave clear space in the central foreground for a separately drawn character. No people, no text.",
		"Empty original harbour above the floodgate: a broad stone crossing, ropes and old lanterns at the edges, calm water and pale dawn sky beyond. Fine black manga ink and delicate grey screentone. Leave an open central foreground for a separate character. No people, no text.",
	}
	for i, prompt := range settings {
		p, e := h.pages.GetPage(h.ctx, list[i*10].ID)
		if e != nil {
			return e
		}
		pn := p.Panels[0]
		j, e := h.step(fmt.Sprintf("scenery-%d", i+1), func() (*jobs.Job, error) {
			return h.gen.EnqueuePanel(h.ctx, h.actor, gen.PanelRequest{ProjectID: h.s.ProjectID, PageID: p.ID, PanelID: pn.ID, LayerID: pn.Layers[0].ID, Variations: 1, Prompt: prompt, RoutingMode: "best", PinModelID: modelID, ExpectedVersion: p.Version})
		})
		if e != nil {
			return e
		}
		p, e = h.pages.GetPage(h.ctx, p.ID)
		if e != nil {
			return e
		}
		plateIDs[i] = p.Panels[0].Layers[0].Asset.ID
		plateJobs[i] = j
	}
	p, err := h.pages.GetPage(h.ctx, list[0].ID)
	if err != nil {
		return err
	}
	pn := p.Panels[0]
	fx, err := h.step("ambient-fx", func() (*jobs.Job, error) {
		return h.gen.EnqueuePanel(h.ctx, h.actor, gen.PanelRequest{ProjectID: h.s.ProjectID, PageID: p.ID, PanelID: pn.ID, LayerID: pn.Layers[2].ID, Variations: 1, Prompt: "A very sparse original manga atmosphere overlay: a few delicate black ink sea-mist strokes confined to the bottom corners. The rest is fully transparent. No characters, scenery, text, panel frames or dense central shading.", RoutingMode: "best", PinModelID: modelID, ExpectedVersion: p.Version})
	})
	if err != nil {
		return err
	}
	p, err = h.pages.GetPage(h.ctx, p.ID)
	if err != nil {
		return err
	}
	fxID := p.Panels[0].Layers[2].Asset.ID
	for i, summary := range list {
		p, e := h.pages.GetPage(h.ctx, summary.ID)
		if e != nil {
			return e
		}
		pn := p.Panels[0]
		updates := []pagestore.SetLayerAsset{}
		for _, v := range []struct {
			index int
			asset string
			job   *jobs.Job
		}{{0, plateIDs[i/10], plateJobs[i/10]}, {2, fxID, fx}} {
			l := pn.Layers[v.index]
			if l.Asset != nil && l.Asset.ID == v.asset {
				continue
			}
			updates = append(updates, pagestore.SetLayerAsset{PageID: p.ID, PanelID: pn.ID, LayerID: l.ID, Kind: l.Kind, AssetID: v.asset, Provenance: "ai", ModelID: v.job.ModelID, ProviderID: v.job.ProviderID, JobID: v.job.ID, Prompt: "Intentional reuse of the generated harbour scenery or ambient overlay; characters remain separate unique images."})
		}
		if len(updates) == 0 {
			continue
		}
		if e = h.a.Deps.DB.WithTx(h.ctx, func(tx *db.Tx) error {
			if err := h.studio.AuthorizeTx(h.ctx, tx, h.actor, h.s.ProjectID, studio.ActEditArt); err != nil {
				return err
			}
			_, err := h.pages.SetLayerAssetsTx(h.ctx, tx, p.ID, updates, pagestore.Change{ActorKind: "user", ActorID: h.s.UserID, Action: "reuse_art", Summary: "Approved reuse of generated scenery and ambient FX; original source jobs retained on layers"})
			return err
		}); e != nil {
			return e
		}
	}
	return nil
}

// Exercise actual D12 repair on three representative low-score appearances.
// A low result stays low; the harness never changes the drift threshold.
func (h *harness) repair() error {
	status, err := h.control.LockChapterStatus(h.ctx, h.actor, h.s.ProjectID, h.s.CharacterID, h.s.ChapterID)
	if err != nil {
		return err
	}
	var targets []gen.RepairTarget
	for _, d := range status.Drift {
		if !d.Locked && (d.PageNumber == 1 || d.PageNumber == 7 || d.PageNumber == 17) {
			targets = append(targets, d.RepairTarget)
		}
	}
	if len(targets) == 0 {
		return nil
	}
	_, err = h.step("representative-repair", func() (*jobs.Job, error) {
		return h.control.EnqueueRepair(h.ctx, h.actor, gen.RepairRequest{ProjectID: h.s.ProjectID, ChapterID: h.s.ChapterID, CharacterID: h.s.CharacterID, Targets: targets})
	})
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(map[string]any{"before": status, "targets": targets}, "", "  ")
	return os.WriteFile(filepath.Join(h.root, "repair-before.json"), b, 0600)
}

// Each selected D12 correction is a separate, deliberate operation. Resume
// reuses its recorded job; a new round must be requested explicitly. The
// original global spend guard remains active for every reservation.
func (h *harness) repairDrift(round int) error {
	status, err := h.control.LockChapterStatus(h.ctx, h.actor, h.s.ProjectID, h.s.CharacterID, h.s.ChapterID)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(status.Method, "ccip-caformer24-v1:") {
		return fmt.Errorf("repair-drift requires a completed CCIP assessment")
	}
	for _, d := range status.Drift {
		if d.Locked {
			continue
		}
		key := fmt.Sprintf("ccip-repair-r%d-p%02d", round, d.PageNumber)
		if h.s.Jobs[key] != "" {
			continue
		}
		before, err := h.pages.ChapterPages(h.ctx, h.s.ChapterID)
		if err != nil {
			return err
		}
		if _, err = h.step(key, func() (*jobs.Job, error) {
			return h.control.EnqueueRepair(h.ctx, h.actor, gen.RepairRequest{ProjectID: h.s.ProjectID, ChapterID: h.s.ChapterID, CharacterID: h.s.CharacterID, Targets: []gen.RepairTarget{d.RepairTarget}})
		}); err != nil {
			return err
		}
		after, err := h.pages.ChapterPages(h.ctx, h.s.ChapterID)
		if err != nil {
			return err
		}
		unchanged := 0
		for i, original := range before {
			if original.ID != d.PageID {
				if !wireEqual(original, after[i]) {
					return fmt.Errorf("repair changed unrelated page %d", original.Number)
				}
				unchanged++
				continue
			}
			original.Version = after[i].Version
			for j := range original.Panels {
				for k := range original.Panels[j].Layers {
					if original.Panels[j].Layers[k].ID == d.LayerID {
						original.Panels[j].Layers[k] = after[i].Panels[j].Layers[k]
					}
				}
			}
			if !wireEqual(original, after[i]) {
				return fmt.Errorf("repair changed protected page metadata/art/lettering")
			}
		}
		current, err := h.control.LockChapterStatus(h.ctx, h.actor, h.s.ProjectID, h.s.CharacterID, h.s.ChapterID)
		if err != nil {
			return err
		}
		evidence, _ := json.MarshalIndent(map[string]any{"page": d.PageNumber, "before": d, "after_status": current, "other_pages_unchanged": unchanged}, "", "  ")
		if err = os.WriteFile(filepath.Join(h.root, key+".json"), evidence, 0600); err != nil {
			return err
		}
		fmt.Printf("Repair round %d, page %d: %d drift remain; %d other pages unchanged\n", round, d.PageNumber, current.DriftTotal, unchanged)
	}
	return nil
}
