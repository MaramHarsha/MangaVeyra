package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
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

type benchmarkState struct {
	ProjectID string          `json:"project_id"`
	ChapterID string          `json:"chapter_id"`
	Cases     []benchmarkCase `json:"cases"`
}
type benchmarkCase struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Look        string `json:"look"`
	Action      string `json:"action"`
	Identity    bool   `json:"expected_identity"`
	Outfit      bool   `json:"expected_outfit"`
	CharacterID string `json:"character_id,omitempty"`
	AssetID     string `json:"asset_id,omitempty"`
}

// This independent set includes same-outfit/different-identity and
// same-identity/different-outfit counterexamples. It never adds chapter artwork
// to the kept references or trains the scorer on the acceptance chapter.
func (h *harness) benchmark(suite string) error {
	prefix := "benchmark"
	fresh := suite != ""
	if fresh {
		prefix += "-" + suite
	}
	path := filepath.Join(h.root, prefix+".json")
	var b benchmarkState
	if raw, err := os.ReadFile(path); err == nil {
		if err = json.Unmarshal(raw, &b); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	save := func() error {
		raw, err := json.MarshalIndent(b, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(path, raw, 0600)
	}
	canonical, err := h.studio.GetCharacter(h.ctx, h.s.CharacterID)
	if err != nil {
		return err
	}
	if b.ProjectID == "" {
		p, err := h.studio.CreateProject(h.ctx, h.actor, studio.NewProject{Title: "Tideglass identity controls", Premise: "Independent original character and outfit counterexamples for local scoring validation.", Style: "seinen_bw", Format: "manga_rtl", PageSize: "b5", Language: "en", PageWidth: 600, PageHeight: 840})
		if err != nil {
			return err
		}
		b.ProjectID = p.ID
		face := "Young adult woman, short straight black bob ending at her jaw, blunt bangs, one small white diamond hair clip above her left temple, dark almond eyes, slim athletic build. "
		uniform := "Knee-length dark courier coat with broad white V-shaped lapels, narrow striped scarf, white gloves, dark trousers, ankle boots and cross-body dark satchel with one round pale clasp."
		b.Cases = []benchmarkCase{
			{Key: "positive-seated", Name: canonical.Name, Look: canonical.Look, Action: "Mina sits upright on an unseen stool, front view, hands resting on her knees, full body visible", Identity: true, Outfit: true},
			{Key: "positive-profile", Name: canonical.Name, Look: canonical.Look, Action: "Mina walks left in a full side profile, one hand on her satchel strap, full body visible", Identity: true, Outfit: true},
			{Key: "different-braid", Name: "Aya Umebayashi", Look: "Original adult woman with a long black braid reaching her waist, round wire glasses, freckles and no hair clip. " + uniform, Action: "Aya stands front facing with one hand raised, full body visible", Identity: false, Outfit: true},
			{Key: "different-white-hair", Name: "Shun Hoshikura", Look: "Original broad-shouldered adult man, short spiky white hair, square face, black eyebrows, a short beard and no hair clip. " + uniform, Action: "Shun stands front facing with his hands at his sides, full body visible", Identity: false, Outfit: true},
			{Key: "outfit-white-jacket", Name: canonical.Name, Look: face + "A completely different outfit: short white hooded utility jacket with no lapels, loose shorts and white sneakers. No scarf, gloves, satchel, dark coat or boots.", Action: "Mina stands front facing in her alternate white jacket outfit, full body visible", Identity: true, Outfit: false},
			{Key: "outfit-dress", Name: canonical.Name, Look: face + "A completely different outfit: light sleeveless ankle-length floral dress, sandals and a wide fabric belt. No coat, trousers, scarf, gloves, boots or satchel.", Action: "Mina stands front facing in her alternate light floral dress, full body visible", Identity: true, Outfit: false},
		}
		if fresh {
			b.Cases[0].Key, b.Cases[0].Action = "positive-crouch", "Mina crouches on one knee with both hands raised, full folded outfit visible"
			b.Cases[1].Key, b.Cases[1].Action = "positive-wide-step", "Mina takes a wide step to the right, one hand reaching forward and the other holding her satchel, full body visible"
			b.Cases[2].Key, b.Cases[2].Name = "different-long-hair", "Akari Shionami"
			b.Cases[2].Look = "Original adult woman with long straight white hair, angular face, no glasses or hair clip. " + uniform
			b.Cases[2].Action = "Akari walks in a three-quarter view with her hands resting on her satchel, full body visible"
			b.Cases[3].Key, b.Cases[3].Name = "different-curly-hair", "Haru Kisaragi"
			b.Cases[3].Look = "Original adult man with chin-length black curly hair, round face and a short beard. " + uniform
			b.Cases[3].Action = "Haru sits on an unseen stool, both hands on his knees, full body visible"
			b.Cases[4].Key, b.Cases[4].Action = "outfit-white-walking", "Mina walks in a side view in her alternate white jacket, shorts and sneakers, full body visible"
			b.Cases[5].Key, b.Cases[5].Action = "outfit-dress-seated", "Mina sits on an unseen stool in her alternate floral dress and sandals, full body visible"
		}
		if suite == "identity-v3" {
			b.Cases[0].Key, b.Cases[0].Action = "positive-kneeling", "Mina kneels on both knees and studies a folded map held at chest height, full body and outfit visible"
			b.Cases[1].Key, b.Cases[1].Action = "positive-side-seated", "Mina sits sideways on an unseen stool, looking over her shoulder to the left, one hand on her knee and one on her satchel, full body visible"
			b.Cases[2].Key, b.Cases[2].Name = "different-white-braid", "Nene Shirosaki"
			b.Cases[2].Look = "Original adult woman with a long white braid, round glasses, freckles and a broad face. " + uniform
			b.Cases[2].Action = "Nene stands with her hands at her sides, full body visible"
			b.Cases[3].Key, b.Cases[3].Name = "different-man-seated", "Jun Aokawa"
			b.Cases[3].Look = "Original adult man, short straight white hair, square face, broad shoulders, dark eyebrows and a short beard. " + uniform
			b.Cases[3].Action = "Jun sits with his hands on his knees, full body visible"
			b.Cases[4].Key, b.Cases[4].Action = "outfit-white-seated", "Mina sits upright on an unseen stool in her alternate white jacket, shorts and sneakers, full body visible"
			b.Cases[5].Key, b.Cases[5].Action = "outfit-dress-walking", "Mina walks in a side view in her alternate floral dress and sandals, full body visible"
		}
		if suite == "outfit-views" {
			b.Cases = b.Cases[:4]
			poses := []string{"sits upright on an unseen stool, hands on knees", "crouches on one knee, hands resting near the raised knee", "takes a wide step in a three-quarter view, one arm extended and the other relaxed", "stands with both arms raised above her head"}
			for i := range b.Cases {
				b.Cases[i] = benchmarkCase{Key: fmt.Sprintf("canonical-pose-%d", i+1), Name: canonical.Name, Look: canonical.Look, Action: "Mina " + poses[i] + ". Canonical outfit reference; show the complete unchanged coat, scarf, lapels, gloves, satchel, trousers and boots, full body visible", Identity: true, Outfit: true}
			}
		}
		if suite == "multiview-final" {
			b.Cases[0].Key, b.Cases[0].Action = "positive-parcel", "Mina leans forward slightly while holding a plain wrapped parcel at chest height, covering part of her coat; full body and remaining outfit visible"
			b.Cases[1].Key, b.Cases[1].Action = "positive-boot-tie", "Mina crouches low and leans forward to tie one boot with both hands, full body and folded outfit visible"
			b.Cases[2].Key, b.Cases[2].Name = "different-white-bob", "Koharu Tsunami"
			b.Cases[2].Look = "Original adult woman with a short curly white bob, round glasses, freckles and a broad round face. " + uniform
			b.Cases[2].Action = "Koharu stands in a three-quarter view, full body visible"
			b.Cases[3].Key, b.Cases[3].Name = "different-ponytail-man", "Riku Shirohama"
			b.Cases[3].Look = "Original older adult man, long white hair in a ponytail, a long beard, round glasses and broad shoulders. " + uniform
			b.Cases[3].Action = "Riku sits on an unseen stool with his hands resting on his knees, full body visible"
			b.Cases[4].Key, b.Cases[4].Action = "outfit-dark-jacket", "Mina stands with both arms relaxed in her alternate dark zipped jacket and white sneakers, full body visible"
			b.Cases[4].Look = face + "A different costume: a short hip-length black zip jacket with a plain narrow collar, dark cargo trousers and white lace-up sneakers. No long coat, broad lapels, scarf, gloves, satchel, buttons or boots."
			b.Cases[5].Key, b.Cases[5].Action = "outfit-white-trench", "Mina walks in a three-quarter view in her alternate white trench coat, full body visible"
			b.Cases[5].Look = face + "A different costume: a white knee-length single-breasted trench coat with a plain collar and fabric belt, slim dark trousers and black ankle boots. No broad white V-shaped lapels, striped scarf, gloves or satchel."
		}
		if suite == "agreement-final" {
			b.Cases[0].Key, b.Cases[0].Action = "positive-large-letter", "Mina stands in a side three-quarter view and holds a large folded blank letter with both hands in front of her waist, hiding part of the coat; full body visible"
			b.Cases[1].Key, b.Cases[1].Action = "positive-low-kneel", "Mina kneels low with one knee raised, looking down while reaching one gloved hand toward her boot and the other holding the satchel strap; full body and folded outfit visible"
			b.Cases[2].Key, b.Cases[2].Name = "different-white-curls", "Yuna Tokinari"
			b.Cases[2].Look = "Original adult woman with short curly white hair, round glasses, a round face and freckles. " + uniform
			b.Cases[2].Action = "Yuna sits sideways with her hands resting on her knees, full body visible"
			b.Cases[3].Key, b.Cases[3].Name = "different-white-beard", "Kaito Urahama"
			b.Cases[3].Look = "Original older adult man with a bald head, long white beard, heavy eyebrows, round glasses and broad build. " + uniform
			b.Cases[3].Action = "Kaito stands with his arms relaxed, full body visible"
			b.Cases[4].Key, b.Cases[4].Action = "outfit-dark-bomber", "Mina sits upright in her alternate black bomber jacket and white shoes, full body visible"
			b.Cases[4].Look = face + "A different costume: a short black bomber jacket with a front zipper and ribbed collar and cuffs, loose black cargo trousers and white sneakers. No long coat, broad lapels, scarf, gloves, satchel or boots."
			b.Cases[5].Key, b.Cases[5].Action = "outfit-light-cape", "Mina stands in a three-quarter view in her alternate light hooded cape and dark leggings, full body visible"
			b.Cases[5].Look = face + "A different costume: a light knee-length hooded cape with a plain clasp, dark leggings and simple flat shoes. No sleeves, coat buttons, broad lapels, striped scarf, gloves, satchel or boots."
		}
		if suite == "outfit-multiview" {
			b.Cases[0].Key, b.Cases[0].Action = "positive-left-walk", "Mina walks to the left in a side three-quarter view with one gloved hand holding a folded blank note, full body visible"
			b.Cases[1].Key, b.Cases[1].Action = "positive-carry-box", "Mina stands with one foot forward while holding a small plain box at waist height with both gloved hands, partly covering her coat, full body visible"
			b.Cases[2].Key, b.Cases[2].Name = "different-white-bun", "Noa Sakunami"
			b.Cases[2].Look = "Original adult woman with white hair in a high bun, oval glasses, freckles and a broad face. " + uniform
			b.Cases[2].Action = "Noa sits on an unseen stool with her hands in her lap, full body visible"
			b.Cases[3].Key, b.Cases[3].Name = "different-man-glasses", "Taichi Mikazaki"
			b.Cases[3].Look = "Original adult man with short wavy white hair, rectangular glasses, a moustache, square jaw and broad shoulders. " + uniform
			b.Cases[3].Action = "Taichi stands in a three-quarter view with one hand raised, full body visible"
			b.Cases[4].Key, b.Cases[4].Action = "outfit-dark-blazer", "Mina stands with her arms relaxed in her alternate short black blazer and cargo trousers, full body visible"
			b.Cases[4].Look = face + "A different costume: a short hip-length fitted black blazer with small black notch lapels over a plain white crew-neck shirt, dark cargo trousers and white sneakers. No knee-length coat, broad white lapels, striped scarf, white gloves, satchel or boots."
			b.Cases[5].Key, b.Cases[5].Action = "outfit-light-tunic", "Mina sits upright on an unseen stool in her alternate light tunic and leggings, full body visible"
			b.Cases[5].Look = face + "A different costume: a loose light long-sleeved tunic with a round neckline and no buttons, dark leggings and simple flat shoes. No coat, lapels, striped scarf, gloves, satchel or boots."
		}
		if err = save(); err != nil {
			return err
		}
	}
	if b.ChapterID == "" {
		c, err := h.studio.CreateChapter(h.ctx, h.actor, b.ProjectID, "Independent controls")
		if err != nil {
			return err
		}
		b.ChapterID = c.ID
		if err = save(); err != nil {
			return err
		}
	}
	as := core.MustUse[*assets.Service](h.a.Deps)
	for i := range b.Cases {
		c := &b.Cases[i]
		if c.CharacterID != "" {
			continue
		}
		ch, err := h.studio.CreateCharacter(h.ctx, h.actor, studio.NewCharacter{ProjectID: b.ProjectID, Name: c.Name, Role: "validation", Category: "main", Age: "22", Look: c.Look})
		if err != nil {
			return err
		}
		c.CharacterID = ch.ID
		// Costume counterexamples receive the original face only. Body views
		// would contradict the deliberately changed costume in their prompt.
		if c.Identity {
			for _, ref := range canonical.Refs {
				if !c.Outfit && ref.Kind != "front" {
					continue
				}
				data, _, err := as.Read(h.ctx, ref.AssetID, assets.MaxBytes)
				if err != nil {
					return err
				}
				kind := ref.Kind
				meta := map[string]any{"derived_from": ref.AssetID, "validation_only": true}
				if !c.Outfit {
					im, err := png.Decode(bytes.NewReader(data))
					if err != nil {
						return err
					}
					rect := image.Rect(40, 0, 200, 145)
					if !rect.In(im.Bounds()) {
						return fmt.Errorf("inspected front face crop is outside this reference")
					}
					face := image.NewNRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
					draw.Draw(face, face.Bounds(), im, rect.Min, draw.Src)
					var out bytes.Buffer
					if err = png.Encode(&out, face); err != nil {
						return err
					}
					data = out.Bytes()
					kind = "face"
					meta["crop"] = []int{40, 0, 160, 145}
				}
				a, err := as.Save(h.ctx, assets.SaveParams{OwnerID: h.s.UserID, ProjectID: b.ProjectID, Kind: "character_ref", Source: assets.SourceAI, Data: data, Meta: meta})
				if err != nil {
					return err
				}
				if err = h.a.Deps.DB.WithTx(h.ctx, func(tx *db.Tx) error {
					_, err := h.studio.AddCharacterRefTx(h.ctx, tx, ch.ID, a.ID, kind, "Original validation reference")
					return err
				}); err != nil {
					return err
				}
			}
		}
		if err = save(); err != nil {
			return err
		}
	}
	h.a.Deps.Executor.Register("qa.phase2.benchmark", func(ctx context.Context, run *jobs.Run) error {
		return run.CommitSideEffect(ctx, func(ctx context.Context, tx *db.Tx) (any, error) {
			var pages []pagedoc.Page
			for i, c := range b.Cases {
				p := pagedoc.New(db.NewID(), b.ProjectID, b.ChapterID, i+1, 600, 840, "rtl")
				p.Note = "Independent Codex-authored scoring control, not a kept reference or acceptance chapter page."
				if suite == "outfit-views" {
					p.Note = "Dedicated canonical outfit pose candidate, authored by Codex; review before keeping, excluded from independent validation."
				}
				pn := pagedoc.Panel{ID: db.NewID(), Points: pagedoc.RectPoints(pagedoc.Rect{X: 20, Y: 20, W: 560, H: 800}), Prompt: c.Action + ". Single original black-and-white manga character isolated on transparent background.", PromptSource: "script", Shot: "wide", Camera: "eye level"}
				pn.Layers = []pagedoc.Layer{{ID: db.NewID(), Kind: "character", Name: c.Name, CharacterID: c.CharacterID, Visible: true, Opacity: 1, Provenance: "ai"}, {ID: db.NewID(), Kind: "lettering", Name: "Lettering", Visible: true, Opacity: 1, Order: 1, Provenance: "ai"}}
				p.Panels = []pagedoc.Panel{pn}
				pages = append(pages, p)
			}
			_, err := h.pages.CreatePagesTx(ctx, tx, b.ChapterID, pages, pagestore.Change{ActorKind: "system", ActorID: h.s.UserID, JobID: run.Job.ID, Action: "create_page", Summary: "Independent Codex-authored scoring controls"})
			return map[string]int{"controls": len(pages)}, err
		})
	})
	if _, err = h.step(prefix+"-structure", func() (*jobs.Job, error) {
		return h.a.Deps.Jobs.Enqueue(h.ctx, jobs.EnqueueParams{Type: "qa.phase2.benchmark", Scope: jobs.Scope{Kind: jobs.ScopeChapter, ProjectID: b.ProjectID, ChapterID: b.ChapterID}, UserID: h.s.UserID, Executor: jobs.ExecutorGo, MaxAttempts: 1})
	}); err != nil {
		return err
	}
	pages, err := h.pages.ChapterPages(h.ctx, b.ChapterID)
	if err != nil {
		return err
	}
	if len(pages) != len(b.Cases) {
		return fmt.Errorf("independent benchmark page count changed")
	}
	for i := range b.Cases {
		p := pages[i]
		l := p.Panels[0].Layers[0]
		if _, err = h.step(prefix+"-"+b.Cases[i].Key, func() (*jobs.Job, error) {
			return h.gen.EnqueuePanel(h.ctx, h.actor, gen.PanelRequest{ProjectID: b.ProjectID, PageID: p.ID, PanelID: p.Panels[0].ID, LayerID: l.ID, ExpectedVersion: p.Version, Variations: 1, RoutingMode: "best", PinModelID: modelID})
		}); err != nil {
			return err
		}
		p, err = h.pages.GetPage(h.ctx, p.ID)
		if err != nil {
			return err
		}
		b.Cases[i].AssetID = p.Panels[0].Layers[0].Asset.ID
		data, _, err := as.Read(h.ctx, b.Cases[i].AssetID, assets.MaxBytes)
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(h.root, prefix+"-"+b.Cases[i].Key+".png"), data, 0600); err != nil {
			return err
		}
		if err = save(); err != nil {
			return err
		}
		fmt.Println("Generated independent control:", b.Cases[i].Key)
	}
	return nil
}
