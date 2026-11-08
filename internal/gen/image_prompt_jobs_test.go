package gen_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// ADR 0009: each layer call carries its own prompt, alpha behaviour, avoid
// list and only its own character's references.
func TestPanelLayersCarryTheirOwnPromptAlphaAndReferences(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, ps := prepareArt(t, ta, 1)
		fake := &fakeImages{}
		gen.SetImageServiceForTest(f.gen, fake)
		im := image.NewNRGBA(image.Rect(0, 0, 64, 64))
		im.SetNRGBA(1, 1, color.NRGBA{A: 255})
		var b bytes.Buffer
		_ = png.Encode(&b, im)
		ref, err := assets.From(ta.Deps).Save(ctx, assets.SaveParams{ProjectID: f.project.ID, OwnerID: f.owner.ID, ContentType: "image/png", Data: b.Bytes()})
		if err != nil {
			t.Fatal(err)
		}
		if err := ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			_, err := f.studio.AddCharacterRefTx(ctx, tx, f.characters[0].ID, ref.ID, "sheet", "Kept sheet")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		p := createArtPages(t, f, ps, false)[0]
		j, err := f.gen.EnqueuePanel(ctx, f.actor, gen.PanelRequest{ProjectID: f.project.ID, PageID: p.ID, PanelID: p.Panels[0].ID, Variations: 1, ExpectedVersion: p.Version})
		if err != nil {
			t.Fatal(err)
		}
		if done := runArtJob(t, ta, j.ID); done.State != jobs.StateDone {
			t.Fatalf("panel job: %+v", done.Error)
		}
		if len(fake.requests) != 3 {
			t.Fatalf("calls = %d", len(fake.requests))
		}
		want := []struct {
			background, layerText, avoid string
			refs                         int
		}{
			{"opaque", "Layer: background plate only", "people, characters", 0},
			{"transparent", "Layer: draw only this one character", "other characters", 1},
			{"transparent", "Layer: effects only", "characters, people", 0},
		}
		for i, r := range fake.requests {
			w := want[i]
			if r.Background != w.background || len(r.References) != w.refs || !strings.Contains(r.Prompt, w.layerText) || !strings.Contains(r.Negative, w.avoid) {
				t.Fatalf("call %d: background=%q refs=%d prompt=%q negative=%q", i, r.Background, len(r.References), r.Prompt, r.Negative)
			}
			if len(r.Prompt) > 4000 || !strings.Contains(r.Prompt, "no text, letters, numbers, speech bubbles") || !strings.Contains(r.Prompt, "The courier crosses the sea gate") {
				t.Fatalf("call %d prompt = %q", i, r.Prompt)
			}
			if r.Plan.Needs.References != 1 {
				t.Fatalf("plan references = %d", r.Plan.Needs.References)
			}
		}
		if !strings.Contains(fake.requests[1].Prompt, "Character: "+f.characters[0].Name) || !strings.Contains(fake.requests[1].Prompt, "brass compass") {
			t.Fatalf("character prompt lacks the bible look: %q", fake.requests[1].Prompt)
		}
		if strings.Contains(fake.requests[0].Prompt, "Appearance:") {
			t.Fatal("background prompt describes a character")
		}
		// The stored layer keeps the deterministic layer prompt.
		after, err := ps.GetPage(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range after.Panels[0].Layers {
			if l.Kind == pagedoc.LayerBackground && !strings.Contains(l.Prompt, "background plate") {
				t.Fatalf("background layer prompt = %q", l.Prompt)
			}
		}
	})
}

// A posed character call carries the pose guide after its identity
// references, but its prompt names no reference image: each route adapter
// describes exactly the images it sends (a two-slot GPT Image route packs the
// identity references into a contact sheet; fal sends none).
func TestPosedCharacterPromptLeavesReferenceWordingToTheRoute(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, ps, pages := controlPages(t, ta, 1)
		for _, kind := range []string{"sheet", "front"} {
			ref, err := assets.From(ta.Deps).Save(ctx, assets.SaveParams{OwnerID: f.actor.UserID, ProjectID: f.project.ID, Kind: "reference", Source: assets.SourceUpload, Data: imagePNG(t, 96, 96)})
			if err != nil {
				t.Fatal(err)
			}
			if err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
				_, err := f.studio.AddCharacterRefTx(ctx, tx, f.characters[0].ID, ref.ID, kind, "Kept "+kind)
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
		target := pages[0]
		poseBytes, _ := json.Marshal(pagedoc.PosePresets()["crouch"])
		if err := json.Unmarshal(poseBytes, &target.Panels[0].Pose); err != nil {
			t.Fatal(err)
		}
		if _, err := ps.SavePage(ctx, target, target.Version, pagestore.Change{ActorKind: "user", ActorID: f.actor.UserID, Action: "save"}); err != nil {
			t.Fatal(err)
		}
		target, err := ps.GetPage(ctx, target.ID)
		if err != nil {
			t.Fatal(err)
		}
		fake := &fakeImages{}
		gen.SetImageServiceForTest(f.gen, fake)
		j, err := core.MustUse[gen.ControlService](ta.Deps).EnqueuePage(ctx, f.actor, gen.PageRequest{ProjectID: f.project.ID, PageID: target.ID, ExpectedVersion: target.Version})
		if err != nil {
			t.Fatal(err)
		}
		if done := runArtJob(t, ta, j.ID); done.State != jobs.StateDone {
			t.Fatalf("pose generation: %+v", done.Error)
		}
		posed := 0
		for _, call := range fake.requests {
			refs := call.References
			if len(refs) == 0 || refs[len(refs)-1].Role != "pose" {
				continue
			}
			posed++
			if len(refs) < 3 {
				t.Fatalf("posed call carries %d references, want the kept identity references and the pose", len(refs))
			}
			for i, r := range refs[:len(refs)-1] {
				if r.Role != "reference" {
					t.Fatalf("reference %d has role %q before the pose", i, r.Role)
				}
			}
			if strings.Contains(call.Prompt, "skeleton") || strings.Contains(call.Prompt, "references define") {
				t.Fatalf("component prompt describes reference images: %q", call.Prompt)
			}
		}
		if posed == 0 {
			t.Fatal("no posed character call")
		}
	})
}
