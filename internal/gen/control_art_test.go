package gen_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"reflect"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func controlPages(t *testing.T, ta *testutil.TestApp, count int) (*genFixture, pagestore.Service, []pagedoc.Page) {
	t.Helper()
	ctx := context.Background()
	f, ps := prepareArt(t, ta, 1)
	source := imagePNG(t, 128, 192)
	a, err := core.MustUse[*assets.Service](ta.Deps).Save(ctx, assets.SaveParams{OwnerID: f.actor.UserID, ProjectID: f.project.ID, Kind: "reference", Source: assets.SourceUpload, Data: source})
	if err != nil {
		t.Fatal(err)
	}
	err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
		for _, kind := range []string{"front", "side", "outfit"} {
			if _, err := f.studio.AddCharacterRefTx(ctx, tx, f.characters[0].ID, a.ID, kind, kind); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	input := make([]pagedoc.Page, 0, count)
	for n := 0; n < count; n++ {
		p := pagedoc.New(db.NewID(), f.project.ID, f.chapter.ID, n+1, 600, 800, "rtl")
		pn := pagedoc.Panel{ID: db.NewID(), Order: 0, Points: pagedoc.RectPoints(pagedoc.Rect{X: 20, Y: 20, W: 560, H: 740}), Prompt: "Rin delivers a letter at the tide gate.", PromptSource: "user"}
		for _, kind := range []string{"background", "character", "fx", "lettering"} {
			l := pagedoc.Layer{ID: db.NewID(), Kind: kind, Visible: true, Opacity: 1, Order: len(pn.Layers), Provenance: "human"}
			if kind != "lettering" {
				l.Asset = &pagedoc.AssetRef{ID: a.ID}
			}
			if kind == "character" {
				l.CharacterID = f.characters[0].ID
			}
			pn.Layers = append(pn.Layers, l)
		}
		p.Panels = []pagedoc.Panel{pn}
		p.Bubbles = []pagedoc.Bubble{{ID: db.NewID(), PanelID: pn.ID, Kind: "speech", Shape: "ellipse", Order: 0, Box: pagedoc.Rect{X: 70, Y: 80, W: 180, H: 100}, Texts: map[string]pagedoc.BubbleText{"en": {Text: "The tide waits for no courier.", Source: "human", Status: "approved"}}}}
		input = append(input, p)
	}
	var made []pagedoc.Page
	err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
		var err error
		made, err = ps.CreatePagesTx(ctx, tx, f.chapter.ID, input, pagestore.Change{ActorKind: "user", ActorID: f.actor.UserID, Action: "create_page"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return f, ps, made
}
func imagePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			im.SetNRGBA(x, y, color.NRGBA{R: uint8(x % 200), G: uint8(y % 160), B: 70, A: 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func pageBytes(t *testing.T, p pagedoc.Page) string {
	t.Helper()
	b, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}

func TestPageSevenRegenerationCommitsOneVersionAndTouchesNoOtherPage(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, ps, pages := controlPages(t, ta, 30)
		fake := &fakeImages{}
		gen.SetImageServiceForTest(f.gen, fake)
		target := pages[6]
		controls := core.MustUse[gen.ControlService](ta.Deps)
		quote, err := controls.QuotePage(ctx, f.actor, gen.PageRequest{ProjectID: f.project.ID, PageID: target.ID, ExpectedVersion: target.Version}, false)
		if err != nil || quote.Layers != 3 || fake.calls() != 0 {
			t.Fatalf("quote mutated or incorrect: %+v %v", quote, err)
		}
		j, err := controls.EnqueuePage(ctx, f.actor, gen.PageRequest{ProjectID: f.project.ID, PageID: target.ID, ExpectedVersion: target.Version})
		if err != nil {
			t.Fatal(err)
		}
		done := runArtJob(t, ta, j.ID)
		if done.State != jobs.StateDone {
			t.Fatalf("page generation: %+v", done.Error)
		}
		after, err := ps.ChapterPages(ctx, f.chapter.ID)
		if err != nil {
			t.Fatal(err)
		}
		for i, p := range after {
			if i != 6 && pageBytes(t, p) != pageBytes(t, pages[i]) {
				t.Fatalf("page %d changed outside scope", i+1)
			}
		}
		if after[6].Version != target.Version+1 || !reflect.DeepEqual(after[6].Bubbles, target.Bubbles) {
			t.Fatal("operation was not one version or changed lettering")
		}
		if len(fake.requests) != 3 || len(fake.requests[0].References) != 2 || len(fake.requests[1].References) != 1 {
			t.Fatalf("neighbour/identity references mixed: %+v", fake.requests)
		}
		versions, err := ps.Versions(ctx, target.ID, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(versions) != 2 || versions[0].Action != "generate_page" {
			t.Fatalf("history contains intermediate states: %+v", versions)
		}
		reverted, err := ps.Revert(ctx, target.ID, versions[1].ID, pagestore.Change{ActorKind: "user", ActorID: f.actor.UserID, Action: "revert", ExpectedVersion: after[6].Version})
		if err != nil {
			t.Fatal(err)
		}
		reverted.Version = target.Version
		if pageBytes(t, reverted) != pageBytes(t, target) {
			t.Fatal("single restore did not recover the complete prior page")
		}
	})
}
func TestPageGenerationStaleNeighbourOrCancellationLeavesTargetUnchanged(t *testing.T) {
	for _, reason := range []string{"neighbour", "cancel", "lock", "failure"} {
		t.Run(reason, func(t *testing.T) {
			forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
				ctx := context.Background()
				f, ps, pages := controlPages(t, ta, 3)
				target := pages[1]
				fake := &fakeImages{}
				gen.SetImageServiceForTest(f.gen, fake)
				controls := core.MustUse[gen.ControlService](ta.Deps)
				j, err := controls.EnqueuePage(ctx, f.actor, gen.PageRequest{ProjectID: f.project.ID, PageID: target.ID, ExpectedVersion: target.Version})
				if err != nil {
					t.Fatal(err)
				}
				fake.hook = func() {
					switch reason {
					case "neighbour":
						p := pages[0]
						p.Note = "a collaborator changed this neighbour"
						_, err = ps.SavePage(ctx, p, p.Version, pagestore.Change{ActorKind: "user", ActorID: f.actor.UserID, Action: "save"})
					case "cancel":
						_, err = ta.Deps.Jobs.Cancel(ctx, j.ID)
					case "lock":
						err = ps.SetLock(ctx, target.ID, target.Panels[0].ID, target.Panels[0].Layers[1].ID, true, pagestore.Change{ActorKind: "user", ActorID: f.actor.UserID, Action: "lock", ExpectedVersion: target.Version})
					case "failure":
						fake.mu.Lock()
						fake.fail = 1
						fake.mu.Unlock()
					}
					if err != nil {
						t.Error(err)
					}
				}
				done := runArtJob(t, ta, j.ID)
				if done.State == jobs.StateDone {
					t.Fatalf("%s generation committed", reason)
				}
				after, err := ps.GetPage(ctx, target.ID)
				if err != nil {
					t.Fatal(err)
				}
				if reason == "lock" {
					target.Version = after.Version
					target.Panels[0].Layers[1].Locked = true
				}
				if pageBytes(t, after) != pageBytes(t, target) {
					t.Fatal("failed operation partially replaced the target")
				}
			})
		})
	}
}
func TestCharacterRepairUsesOnlyTheSelectedLayerAndRejectsLocks(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, ps, pages := controlPages(t, ta, 2)
		target := pages[0]
		l := target.Panels[0].Layers[1]
		fake := &fakeImages{}
		gen.SetImageServiceForTest(f.gen, fake)
		controls := core.MustUse[gen.ControlService](ta.Deps)
		req := gen.RepairRequest{ProjectID: f.project.ID, ChapterID: f.chapter.ID, CharacterID: l.CharacterID, Targets: []gen.RepairTarget{{PageID: target.ID, PanelID: target.Panels[0].ID, LayerID: l.ID, Version: target.Version}}}
		j, err := controls.EnqueueRepair(ctx, f.actor, req)
		if err != nil {
			t.Fatal(err)
		}
		done := runArtJob(t, ta, j.ID)
		if done.State != jobs.StateDone {
			t.Fatalf("repair: %+v", done.Error)
		}
		after, _ := ps.GetPage(ctx, target.ID)
		other, _ := ps.GetPage(ctx, pages[1].ID)
		if pageBytes(t, other) != pageBytes(t, pages[1]) || !reflect.DeepEqual(after.Bubbles, target.Bubbles) {
			t.Fatal("repair touched unrelated work")
		}
		if len(fake.requests) != 1 || fake.requests[0].Init == nil || fake.requests[0].Mask != nil || len(fake.requests[0].References) != 1 {
			t.Fatal("repair did not use the current character image and own references")
		}
		if after.Version != target.Version+1 || after.Panels[0].Layers[0].Asset.ID != target.Panels[0].Layers[0].Asset.ID || after.Panels[0].Layers[2].Asset.ID != target.Panels[0].Layers[2].Asset.ID || after.Panels[0].Layers[1].Provenance != "mixed" {
			t.Fatal("repair changed other layers or lost provenance")
		}
		if err := ps.SetLock(ctx, target.ID, after.Panels[0].ID, l.ID, true, pagestore.Change{ActorKind: "user", ActorID: f.actor.UserID, Action: "lock", ExpectedVersion: after.Version}); err != nil {
			t.Fatal(err)
		}
		locked, _ := ps.GetPage(ctx, target.ID)
		req.Targets[0].Version = locked.Version
		if _, err := controls.EnqueueRepair(ctx, f.actor, req); !errors.Is(err, studio.ErrLocked) && !errors.Is(err, pagestore.ErrLocked) {
			t.Fatalf("locked repair accepted: %v", err)
		}
	})
}

func TestActivePageAndRepairRequestsReuseOnlyTheIdenticalCoordinator(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, _, pages := controlPages(t, ta, 2)
		controls := core.MustUse[gen.ControlService](ta.Deps)
		req := gen.PageRequest{ProjectID: f.project.ID, PageID: pages[0].ID, ExpectedVersion: pages[0].Version}
		first, err := controls.EnqueuePage(ctx, f.actor, req)
		if err != nil {
			t.Fatal(err)
		}
		again, err := controls.EnqueuePage(ctx, f.actor, req)
		if err != nil || again.ID != first.ID {
			t.Fatalf("duplicate page coordinator: %v", err)
		}
		changed := req
		changed.Strength = .5
		if _, err = controls.EnqueuePage(ctx, f.actor, changed); !errors.Is(err, studio.ErrConflict) {
			t.Fatalf("different active request: %v", err)
		}
		p := pages[0]
		l := p.Panels[0].Layers[1]
		repair := gen.RepairRequest{ProjectID: f.project.ID, ChapterID: f.chapter.ID, CharacterID: l.CharacterID, Targets: []gen.RepairTarget{{PageID: p.ID, PanelID: p.Panels[0].ID, LayerID: l.ID, Version: p.Version}}}
		if _, err = controls.EnqueueRepair(ctx, f.actor, repair); !errors.Is(err, studio.ErrConflict) {
			t.Fatalf("overlapping page/repair: %v", err)
		}
		if _, err = ta.Deps.Jobs.Cancel(ctx, first.ID); err != nil {
			t.Fatal(err)
		}
		fixed, err := controls.EnqueueRepair(ctx, f.actor, repair)
		if err != nil {
			t.Fatal(err)
		}
		again, err = controls.EnqueueRepair(ctx, f.actor, repair)
		if err != nil || again.ID != fixed.ID {
			t.Fatalf("duplicate repair coordinator: %v", err)
		}
		var count int
		if err = ta.Deps.DB.Get(ctx, &count, `SELECT COUNT(*) FROM jobs WHERE type IN (?,?) AND state IN ('queued','running')`, gen.JobPage, gen.JobRepair); err != nil || count != 1 {
			t.Fatalf("active jobs=%d %v", count, err)
		}
	})
}
func TestLegacyAndMissingBackendScoresNeverBecomeMeasuredIdentity(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, _, pages := controlPages(t, ta, 1)
		controls := core.MustUse[gen.ControlService](ta.Deps)
		p := pages[0]
		n := .99
		p.Panels[0].Layers[1].LockScore = &n
		if err := controls.WithLockScores(ctx, f.actor, &p); err != nil {
			t.Fatal(err)
		}
		if p.Panels[0].Layers[1].LockScore != nil {
			t.Fatal("legacy score treated as learned evidence")
		}
		status, err := controls.LockStatus(ctx, f.actor, f.project.ID, f.characters[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if status.Score != nil || status.CanScore || status.CanTrain || status.Unscored != 1 {
			t.Fatalf("invented readiness: %+v", status)
		}
	})
}

func TestPageSevenHandRegionPreservesEveryOutsidePixelAndOtherPage(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, ps, pages := controlPages(t, ta, 30)
		p := pages[6]
		l := p.Panels[0].Layers[1]
		mask := image.NewNRGBA(image.Rect(0, 0, 128, 192))
		for y := range 192 {
			for x := range 128 {
				v := uint8(0)
				if x >= 40 && x < 52 && y >= 70 && y < 85 {
					v = 255
				}
				mask.SetNRGBA(x, y, color.NRGBA{R: v, G: v, B: v, A: 255})
			}
		}
		var b bytes.Buffer
		if err := png.Encode(&b, mask); err != nil {
			t.Fatal(err)
		}
		a := core.MustUse[*assets.Service](ta.Deps)
		stored, err := a.Save(ctx, assets.SaveParams{OwnerID: f.actor.UserID, ProjectID: f.project.ID, Kind: "region_mask", Source: assets.SourceUpload, Data: b.Bytes()})
		if err != nil {
			t.Fatal(err)
		}
		original, _, err := a.Read(ctx, l.Asset.ID, assets.MaxBytes)
		if err != nil {
			t.Fatal(err)
		}
		gen.SetImageServiceForTest(f.gen, &fakeImages{})
		j, err := f.gen.EnqueueRegionEdit(ctx, f.actor, gen.RegionEditRequest{ProjectID: f.project.ID, LayerID: l.ID, MaskAssetID: stored.ID, Prompt: "Correct the courier's hand holding the letter.", Variations: 1, ExpectedVersion: p.Version})
		if err != nil {
			t.Fatal(err)
		}
		done := runArtJob(t, ta, j.ID)
		if done.State != jobs.StateDone {
			t.Fatalf("region: %+v", done.Error)
		}
		after, err := ps.ChapterPages(ctx, f.chapter.ID)
		if err != nil {
			t.Fatal(err)
		}
		for i, page := range after {
			if i != 6 && pageBytes(t, page) != pageBytes(t, pages[i]) {
				t.Fatalf("page %d changed", i+1)
			}
		}
		if !reflect.DeepEqual(after[6].Bubbles, p.Bubbles) {
			t.Fatal("region edit changed lettering")
		}
		updated, _, err := a.Read(ctx, after[6].Panels[0].Layers[1].Asset.ID, assets.MaxBytes)
		if err != nil {
			t.Fatal(err)
		}
		beforePixels, err := png.Decode(bytes.NewReader(original))
		if err != nil {
			t.Fatal(err)
		}
		afterPixels, err := png.Decode(bytes.NewReader(updated))
		if err != nil {
			t.Fatal(err)
		}
		changed := 0
		for y := range 192 {
			for x := range 128 {
				same := color.NRGBAModel.Convert(beforePixels.At(x, y)) == color.NRGBAModel.Convert(afterPixels.At(x, y))
				if x >= 40 && x < 52 && y >= 70 && y < 85 {
					if !same {
						changed++
					}
				} else if !same {
					t.Fatalf("pixel %d,%d changed outside hand mask", x, y)
				}
			}
		}
		if changed == 0 {
			t.Fatal("fixture did not exercise a real change")
		}
	})
}
