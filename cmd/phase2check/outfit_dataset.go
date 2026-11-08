package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

type outfitDataset struct {
	ProjectID string         `json:"project_id"`
	ChapterID string         `json:"chapter_id"`
	People    []outfitPerson `json:"people"`
	Samples   []outfitSample `json:"samples"`
}
type outfitPerson struct {
	Name          string `json:"name"`
	Face          string `json:"face"`
	Group         string `json:"group"`
	Wardrobe      string `json:"wardrobe"`
	Split         string `json:"split"`
	CharacterID   string `json:"character_id"`
	ReferenceKept bool   `json:"reference_kept"`
}
type outfitSample struct {
	Person  int    `json:"person"`
	Pose    int    `json:"pose"`
	AssetID string `json:"asset_id"`
}

// This set is disjoint from the acceptance chapter and its earlier controls.
// Every identity has a canonical view and three new poses; no appearance is copied.
func (h *harness) outfitDataset(extra, fresh bool) error {
	prefix := "outfit-dataset"
	imagePrefix := "outfit-study"
	if fresh {
		prefix, imagePrefix = "outfit-validation", "outfit-validation"
	}
	path := filepath.Join(h.root, prefix+".json")
	var d outfitDataset
	if raw, err := os.ReadFile(path); err == nil {
		if err = json.Unmarshal(raw, &d); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	save := func() error {
		raw, err := json.MarshalIndent(d, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(path, raw, 0600)
	}
	if d.ProjectID == "" {
		p, err := h.studio.CreateProject(h.ctx, h.actor, studio.NewProject{Title: "Harbour wardrobe studies", Premise: "Independent original wardrobe pairs; no acceptance-chapter images are training data.", Style: "seinen_bw", Format: "manga_rtl", PageSize: "b5", Language: "en", PageWidth: 600, PageHeight: 840})
		if err != nil {
			return err
		}
		d.ProjectID = p.ID
		groups := []struct {
			id, wear     string
			names, faces []string
		}{
			{"courier-coat", "Knee-length dark double-breasted courier coat, broad white V-shaped lapels, narrow striped scarf, white gloves, dark trousers, ankle boots, cross-body dark satchel with a round pale clasp.", []string{"Iori Natsume", "Toma Shizuki", "Yui Kitase"}, []string{"Adult woman, chin-length wavy black hair, oval face, no hair clip.", "Adult man, short dark curly hair, square jaw and a small beard.", "Adult woman, long black braid, round glasses and freckles."}},
			{"white-utility", "Short white hooded utility jacket with zip fastener, loose white shorts and white sneakers. No coat, scarf, gloves, satchel, lapels or boots.", []string{"Nao Haseko", "Ren Mizunari", "Saki Furuse"}, []string{"Adult woman, short white pixie hair, narrow face and dark eyebrows.", "Adult man, dark shaved hair, round face and a broad build.", "Adult woman, straight shoulder-length dark hair, rectangular glasses."}},
			{"floral-dress", "Light sleeveless ankle-length floral dress, wide fabric belt, simple sandals. No coat, scarf, gloves, satchel, trousers or boots.", []string{"Ena Shirase", "Maho Asanari", "Ruka Yuzuse"}, []string{"Adult woman, long wavy black hair with centre part and a strong jaw.", "Adult woman, short curly white hair, round face and freckles.", "Adult woman, dark hair in a high bun and narrow almond eyes."}},
		}
		if fresh {
			groups[0].names = []string{"Hina Amakawa"}
			groups[0].faces = []string{"Adult woman, short straight white hair, diamond-shaped face and dark eyebrows."}
			groups[1].names = []string{"Kei Utsunami"}
			groups[1].faces = []string{"Adult man, long dark hair in a low ponytail, narrow face and round glasses."}
			groups[2].names = []string{"Suzu Kazehara"}
			groups[2].faces = []string{"Adult woman, dark chin-length curly hair, round face and freckles."}
		}
		for _, g := range groups {
			for i, name := range g.names {
				split := "train"
				if i == 2 || fresh {
					split = "heldout"
				}
				d.People = append(d.People, outfitPerson{Name: name, Face: g.faces[i], Group: g.id, Wardrobe: g.wear, Split: split})
			}
		}
		for i := range d.People {
			poseCount := 3
			if fresh {
				poseCount = 4
			}
			for pose := 0; pose < poseCount; pose++ {
				d.Samples = append(d.Samples, outfitSample{Person: i, Pose: pose})
			}
		}
		if err = save(); err != nil {
			return err
		}
	}
	if d.ChapterID == "" {
		c, err := h.studio.CreateChapter(h.ctx, h.actor, d.ProjectID, "Independent wardrobe views")
		if err != nil {
			return err
		}
		d.ChapterID = c.ID
		if err = save(); err != nil {
			return err
		}
	}
	start, structureKey := 0, prefix+"-structure"
	if extra {
		if len(d.Samples) == 27 {
			for _, s := range d.Samples {
				if s.AssetID == "" {
					return fmt.Errorf("finish the base studies before appending folded-outfit poses")
				}
			}
			for person := range d.People {
				d.Samples = append(d.Samples, outfitSample{Person: person, Pose: 3})
			}
			if err := save(); err != nil {
				return err
			}
		}
		if len(d.Samples) != 36 {
			return fmt.Errorf("folded-outfit study contract changed")
		}
		start, structureKey = 27, prefix+"-extra-structure"
	}
	for i := range d.People {
		p := &d.People[i]
		if p.CharacterID != "" {
			continue
		}
		c, err := h.studio.CreateCharacter(h.ctx, h.actor, studio.NewCharacter{ProjectID: d.ProjectID, Name: p.Name, Age: "25", Role: "wardrobe study", Category: "main", Look: p.Face + " " + p.Wardrobe})
		if err != nil {
			return err
		}
		p.CharacterID = c.ID
		if err = save(); err != nil {
			return err
		}
	}
	poses := []string{"stands in a neutral front view with arms relaxed and the complete outfit visible", "sits upright on an unseen stool, hands on knees, full body visible", "takes a wide step in a three-quarter side view, one hand raised and the other arm extended, full body visible", "crouches on one knee and raises both hands to hold an unseen object above the head; keep the full body and folded outfit visible"}
	h.a.Deps.Executor.Register("qa.phase2.outfit-dataset", func(ctx context.Context, run *jobs.Run) error {
		return run.CommitSideEffect(ctx, func(ctx context.Context, tx *db.Tx) (any, error) {
			var pages []pagedoc.Page
			for i, sample := range d.Samples {
				if i < start {
					continue
				}
				person := d.People[sample.Person]
				p := pagedoc.New(db.NewID(), d.ProjectID, d.ChapterID, i+1, 600, 840, "rtl")
				p.Note = "Independent Codex-authored wardrobe study; training split excludes held-out identities and the acceptance chapter."
				pn := pagedoc.Panel{ID: db.NewID(), Points: pagedoc.RectPoints(pagedoc.Rect{X: 20, Y: 20, W: 560, H: 800}), Prompt: person.Name + " " + poses[sample.Pose] + ". Original black-and-white manga. Keep all outfit details exactly. Isolate this one character on transparent background.", PromptSource: "script", Shot: "wide", Camera: "eye level"}
				pn.Layers = []pagedoc.Layer{{ID: db.NewID(), Kind: "character", CharacterID: person.CharacterID, Name: person.Name, Visible: true, Opacity: 1, Provenance: "ai"}, {ID: db.NewID(), Kind: "lettering", Order: 1, Name: "Lettering", Visible: true, Opacity: 1, Provenance: "ai"}}
				p.Panels = []pagedoc.Panel{pn}
				pages = append(pages, p)
			}
			_, err := h.pages.CreatePagesTx(ctx, tx, d.ChapterID, pages, pagestore.Change{ActorKind: "system", ActorID: h.s.UserID, JobID: run.Job.ID, Action: "create_page", Summary: "Independent original wardrobe dataset"})
			return map[string]int{"samples": len(pages)}, err
		})
	})
	if _, err := h.step(structureKey, func() (*jobs.Job, error) {
		return h.a.Deps.Jobs.Enqueue(h.ctx, jobs.EnqueueParams{Type: "qa.phase2.outfit-dataset", UserID: h.s.UserID, Scope: jobs.Scope{Kind: jobs.ScopeChapter, ProjectID: d.ProjectID, ChapterID: d.ChapterID}, Executor: jobs.ExecutorGo, MaxAttempts: 1})
	}); err != nil {
		return err
	}
	pages, err := h.pages.ChapterPages(h.ctx, d.ChapterID)
	if err != nil {
		return err
	}
	if len(pages) != len(d.Samples) {
		return fmt.Errorf("wardrobe dataset page count changed")
	}
	as := core.MustUse[*assets.Service](h.a.Deps)
	for i := range d.Samples {
		p := pages[i]
		sample := &d.Samples[i]
		person := &d.People[sample.Person]
		if _, err = h.step(fmt.Sprintf("%s-%02d", prefix, i+1), func() (*jobs.Job, error) {
			return h.gen.EnqueuePanel(h.ctx, h.actor, gen.PanelRequest{ProjectID: d.ProjectID, PageID: p.ID, PanelID: p.Panels[0].ID, LayerID: p.Panels[0].Layers[0].ID, ExpectedVersion: p.Version, Variations: 1, RoutingMode: "best", PinModelID: modelID})
		}); err != nil {
			return err
		}
		p, err = h.pages.GetPage(h.ctx, p.ID)
		if err != nil {
			return err
		}
		sample.AssetID = p.Panels[0].Layers[0].Asset.ID
		data, _, err := as.Read(h.ctx, sample.AssetID, assets.MaxBytes)
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(h.root, fmt.Sprintf("%s-%02d.png", imagePrefix, i+1)), data, 0600); err != nil {
			return err
		}
		if sample.Pose == 0 && !person.ReferenceKept {
			if err = h.a.Deps.DB.WithTx(h.ctx, func(tx *db.Tx) error {
				_, err := h.studio.AddCharacterRefTx(h.ctx, tx, person.CharacterID, sample.AssetID, "front", "Original canonical wardrobe view")
				return err
			}); err != nil {
				return err
			}
			person.ReferenceKept = true
		}
		if err = save(); err != nil {
			return err
		}
		fmt.Printf("Wardrobe study %d/%d: %s · %s\n", i+1, len(d.Samples), person.Group, person.Split)
	}
	return nil
}
