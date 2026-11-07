package gen_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

type fakeImages struct {
	mu       sync.Mutex
	requests []ai.ImageGenerateRequest
	hook     func()
	planHook func(*ai.ImagePlan)
}

func (f *fakeImages) PlanImage(_ context.Context, r ai.ImagePlanRequest) (ai.ImagePlan, error) {
	if r.RoutingMode == "" {
		r.RoutingMode = ai.ModeBalanced
	}
	p := ai.ImagePlan{Task: r.Task, Needs: r.Needs, RoutingMode: r.RoutingMode, ProviderID: ai.PlaceholderID, ModelID: ai.PlaceholderArt, Executor: jobs.ExecutorGo, PinModelID: r.PinModelID, PinProviderID: r.PinProviderID, PublicSeries: r.PublicSeries, Decision: json.RawMessage(`{"provider":"placeholder"}`)}
	if f.planHook != nil {
		f.planHook(&p)
	}
	return p, nil
}
func (f *fakeImages) GenerateImage(_ context.Context, r ai.ImageGenerateRequest) (ai.ImageResult, error) {
	f.mu.Lock()
	f.requests = append(f.requests, r)
	hook := f.hook
	f.hook = nil
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	out := ai.ImageResult{ProviderID: r.Plan.ProviderID, ModelID: r.Plan.ModelID}
	for n := 0; n < r.Count; n++ {
		im := image.NewNRGBA(image.Rect(0, 0, r.Width, r.Height))
		for y := 0; y < r.Height; y++ {
			for x := 0; x < r.Width; x++ {
				im.SetNRGBA(x, y, color.NRGBA{R: uint8(40 + n*30), G: uint8(x % 127), B: uint8(y % 127), A: 255})
			}
		}
		var b bytes.Buffer
		if err := png.Encode(&b, im); err != nil {
			return out, err
		}
		out.Images = append(out.Images, ai.GeneratedImage{Data: b.Bytes(), ContentType: "image/png", Width: r.Width, Height: r.Height, Seed: r.Seed + int64(n)})
	}
	return out, nil
}
func (f *fakeImages) calls() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.requests) }
func runArtJob(t *testing.T, ta *testutil.TestApp, id string) *jobs.Job {
	t.Helper()
	ran, err := ta.Deps.Executor.RunOnce(context.Background())
	if err != nil || !ran {
		t.Fatalf("run: %v %v", ran, err)
	}
	j, err := ta.Deps.Jobs.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}
func prepareArt(t *testing.T, ta *testutil.TestApp, panels int) (*genFixture, pagestore.Service) {
	t.Helper()
	f := newGenFixture(t, ta, "", 1)
	doc := studioScript(f.characters[0].ID, "The courier crosses the sea gate.")
	for i := 1; i < panels; i++ {
		p := doc.Scenes[0].Panels[0]
		p.Key = "panel-" + db.NewID()
		doc.Scenes[0].Panels = append(doc.Scenes[0].Panels, p)
	}
	doc.ChapterID = f.chapter.ID
	err := ta.Deps.DB.WithTx(context.Background(), func(tx *db.Tx) error {
		_, err := f.studio.ReplaceGeneratedScriptTx(context.Background(), tx, studio.ReplaceScriptInput{Actor: f.actor, ProjectID: f.project.ID, ChapterID: f.chapter.ID, Doc: doc, Provenance: "ai", JobID: db.NewID(), ProviderID: ai.PlaceholderID, ModelID: ai.PlaceholderText})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return f, core.MustUse[pagestore.Service](ta.Deps)
}
func createArtPages(t *testing.T, f *genFixture, ps pagestore.Service, generate bool) []pagedoc.Page {
	t.Helper()
	j, err := f.gen.EnqueuePagesFromScript(context.Background(), f.actor, gen.PagesFromScriptRequest{ProjectID: f.project.ID, ChapterID: f.chapter.ID, Letter: true, Generate: generate})
	if err != nil {
		t.Fatal(err)
	}
	done := runArtJob(t, f.ta, j.ID)
	if done.State != jobs.StateDone {
		t.Fatalf("pages job: %+v", done.Error)
	}
	pages, err := ps.ChapterPages(context.Background(), f.chapter.ID)
	if err != nil || len(pages) != 1 {
		t.Fatalf("pages: %d %v", len(pages), err)
	}
	return pages
}
func TestPagesPanelsAndCandidateApplyPreserveLettering(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, ps := prepareArt(t, ta, 1)
		fake := &fakeImages{}
		gen.SetImageServiceForTest(f.gen, fake)
		p := createArtPages(t, f, ps, false)[0]
		if len(p.Bubbles) != 1 || len(p.Panels[0].Layers) != 4 || p.Panels[0].ScriptPanelID == "" {
			t.Fatalf("missing structured layers/bubbles: %+v", p)
		}
		originalText := p.Bubbles[0].Texts["en"].Text
		j, err := f.gen.EnqueuePanel(ctx, f.actor, gen.PanelRequest{ProjectID: f.project.ID, PageID: p.ID, PanelID: p.Panels[0].ID, Variations: 4, ExpectedVersion: p.Version})
		if err != nil {
			t.Fatal(err)
		}
		if j.Executor != jobs.ExecutorGo || j.MaxAttempts != 3 {
			t.Fatal("wrong parent envelope")
		}
		done := runArtJob(t, ta, j.ID)
		if done.State != jobs.StateDone {
			t.Fatalf("panel job: %+v", done.Error)
		}
		before, err := ps.GetPage(ctx, p.ID)
		if err != nil || before.Version != p.Version {
			t.Fatal("candidate generation changed page", err)
		}
		candidates, err := f.gen.Candidates(ctx, f.actor, j.ID)
		if err != nil || len(candidates) != 4 || len(candidates[0].Layers) != 3 || candidates[0].URL == "" {
			t.Fatalf("candidates %+v %v", candidates, err)
		}
		if err = f.gen.ApplyVariation(ctx, f.actor, j.ID, 2); err != nil {
			t.Fatal(err)
		}
		after, err := ps.GetPage(ctx, p.ID)
		if err != nil || after.Version <= p.Version || after.Bubbles[0].Texts["en"].Text != originalText {
			t.Fatal("apply scope/lettering", after, err)
		}
		for _, l := range after.Panels[0].Layers {
			if l.Kind == pagedoc.LayerLettering {
				if l.Asset != nil {
					t.Fatal("raster lettering")
				}
			} else if l.Asset == nil || l.Provenance != pagedoc.ProvenanceAI {
				t.Fatal("independent layer missing", l)
			}
		}
		if err = f.gen.ApplyVariation(ctx, f.actor, j.ID, 2); !errors.Is(err, studio.ErrConflict) {
			t.Fatal("candidate reapplied", err)
		}
	})
}
func TestGenerateBatchDoesNotInvalidateSiblingArt(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		f, ps := prepareArt(t, ta, 2)
		gen.SetImageServiceForTest(f.gen, &fakeImages{})
		p := createArtPages(t, f, ps, true)[0]
		children, err := ta.Deps.Jobs.List(context.Background(), jobs.Filter{ProjectID: f.project.ID, Types: []string{gen.JobPanel}, Limit: 10})
		if err != nil || len(children) != 2 {
			t.Fatal("children not committed", len(children), err)
		}
		for range children {
			ran, err := ta.Deps.Executor.RunOnce(context.Background())
			if err != nil || !ran {
				t.Fatal("batch run", ran, err)
			}
		}
		for _, child := range children {
			done, err := ta.Deps.Jobs.Get(context.Background(), child.ID)
			if err != nil || done.State != jobs.StateDone {
				t.Fatalf("sibling invalidated: %+v %v", done, err)
			}
		}
		p, err = ps.GetPage(context.Background(), p.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, pn := range p.Panels {
			for _, l := range pn.Layers {
				if l.Kind != pagedoc.LayerLettering && l.Asset == nil {
					t.Fatal("batch art missing", l)
				}
			}
		}
	})
}
func TestCharacterSheetCandidatesAndChangedRevision(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		f := newGenFixture(t, ta, "", 1)
		gen.SetImageServiceForTest(f.gen, &fakeImages{})
		ctx := context.Background()
		j, err := f.gen.EnqueueCharacterSheet(ctx, f.actor, gen.CharacterSheetRequest{ProjectID: f.project.ID, CharacterID: f.characters[0].ID, Count: 4, ExpectedUpdatedAt: f.characters[0].UpdatedAt})
		if err != nil {
			t.Fatal(err)
		}
		done := runArtJob(t, ta, j.ID)
		if done.State != jobs.StateDone {
			t.Fatalf("sheet job: %+v", done.Error)
		}
		cs, err := f.gen.Candidates(ctx, f.actor, j.ID)
		if err != nil || len(cs) != 4 || len(cs[0].Layers) != 0 {
			t.Fatal(cs, err)
		}
		if err = f.gen.ApplyVariation(ctx, f.actor, j.ID, 1); err != nil {
			t.Fatal(err)
		}
		c, err := f.studio.GetCharacter(ctx, f.characters[0].ID)
		if err != nil || len(c.Refs) != 1 || c.PortraitAssetID != cs[1].AssetID {
			t.Fatal("sheet not attached", c, err)
		}
		if _, err = f.gen.EnqueueCharacterSheet(ctx, f.actor, gen.CharacterSheetRequest{ProjectID: f.project.ID, CharacterID: c.ID, Count: 1, ExpectedUpdatedAt: f.characters[0].UpdatedAt}); !errors.Is(err, studio.ErrConflict) {
			t.Fatal("stale sheet accepted", err)
		}
	})
}
func TestRegionWhiteMaskPreservesEveryOutsidePixel(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, ps := prepareArt(t, ta, 1)
		gen.SetImageServiceForTest(f.gen, &fakeImages{})
		p := createArtPages(t, f, ps, false)[0]
		a := assets.From(ta.Deps)
		src := image.NewNRGBA(image.Rect(0, 0, 64, 64))
		mask := image.NewNRGBA(src.Bounds())
		for y := 0; y < 64; y++ {
			for x := 0; x < 64; x++ {
				src.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 3), G: uint8(y * 3), B: 101, A: uint8(100 + x)})
				mask.SetNRGBA(x, y, color.NRGBA{127, 127, 127, 255})
				if x >= 24 && x < 40 && y >= 24 && y < 40 {
					mask.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
				}
			}
		}
		save := func(im image.Image) string {
			var b bytes.Buffer
			_ = png.Encode(&b, im)
			asset, err := a.Save(ctx, assets.SaveParams{ProjectID: f.project.ID, OwnerID: f.owner.ID, ContentType: "image/png", Data: b.Bytes()})
			if err != nil {
				t.Fatal(err)
			}
			return asset.ID
		}
		originalID, maskID := save(src), save(mask)
		err := ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			_, err := ps.SetLayerAssetTx(ctx, tx, pagestore.SetLayerAsset{PageID: p.ID, PanelID: p.Panels[0].ID, LayerID: p.Panels[0].Layers[0].ID, Kind: pagedoc.LayerBackground, AssetID: originalID, Provenance: "human"}, pagestore.Change{ActorKind: "user", ActorID: f.owner.ID, Action: "save"})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		p, _ = ps.GetPage(ctx, p.ID)
		layer := p.Panels[0].Layers[0]
		j, err := f.gen.EnqueueRegionEdit(ctx, f.actor, gen.RegionEditRequest{ProjectID: f.project.ID, LayerID: layer.ID, MaskAssetID: maskID, Prompt: "Repair the compass.", Variations: 4, ExpectedVersion: p.Version})
		if err != nil {
			t.Fatal(err)
		}
		if j.Scope.PageID != "" || j.Scope.PanelID != "" || j.Scope.LayerID != layer.ID {
			t.Fatal("spoofable region ancestry", j.Scope)
		}
		done := runArtJob(t, ta, j.ID)
		if done.State != jobs.StateDone {
			t.Fatalf("region: %+v", done.Error)
		}
		cs, err := f.gen.Candidates(ctx, f.actor, j.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cs {
			data, _, err := a.Read(ctx, c.AssetID, 0)
			if err != nil {
				t.Fatal(err)
			}
			got, err := png.Decode(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			for y := 0; y < 64; y++ {
				for x := 0; x < 64; x++ {
					if !(x >= 24 && x < 40 && y >= 24 && y < 40) && color.NRGBAModel.Convert(got.At(x, y)) != src.NRGBAAt(x, y) {
						t.Fatalf("unmasked pixel changed %d,%d", x, y)
					}
				}
			}
		}
		if err = f.gen.ApplyVariation(ctx, f.actor, j.ID, 0); err != nil {
			t.Fatal(err)
		}
	})
}
func TestImageLocksStaleApplyAndCleanup(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, ps := prepareArt(t, ta, 1)
		fake := &fakeImages{}
		gen.SetImageServiceForTest(f.gen, fake)
		p := createArtPages(t, f, ps, false)[0]
		req := gen.PanelRequest{ProjectID: f.project.ID, PageID: p.ID, PanelID: p.Panels[0].ID, Variations: 4, ExpectedVersion: p.Version}
		j, err := f.gen.EnqueuePanel(ctx, f.actor, req)
		if err != nil {
			t.Fatal(err)
		}
		fake.hook = func() {
			if err := ps.SetLock(ctx, p.ID, "", "", true, pagestore.Change{ActorKind: "user", ActorID: f.owner.ID, Action: "lock"}); err != nil {
				t.Error(err)
			}
		}
		done := runArtJob(t, ta, j.ID)
		if done.State == jobs.StateDone {
			t.Fatal("locked during generation persisted")
		}
		var assetsCount int
		if err = ta.Deps.DB.Get(ctx, &assetsCount, `SELECT COUNT(*) FROM assets WHERE project_id=?`, f.project.ID); err != nil || assetsCount != 0 {
			t.Fatal("rolled-back art rows survived", assetsCount, err)
		}
		objects, err := ta.Deps.Store.List(ctx, "projects/"+f.project.ID+"/")
		if err != nil || len(objects) != 0 {
			t.Fatal("staged object cleanup", len(objects), err)
		}
		if _, err = f.gen.EnqueuePanel(ctx, f.actor, req); err == nil {
			t.Fatal("locked enqueue allowed")
		}
	})
}
func TestLocalCompatibleTextIsAccepted(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		f := newGenFixture(t, ta, "", 0)
		provider := db.NewID()
		f.useText(&fakeTextService{planFn: func(_ context.Context, r ai.TextPlanRequest) (ai.TextPlan, error) {
			p := placeholderTextPlan(ai.ModeBalanced)
			p.ProviderID = provider
			p.ModelID = "local-manuscript"
			p.Remote = false
			return p, nil
		}})
		j, err := f.gen.EnqueueScript(context.Background(), f.actor, gen.ScriptRequest{ProjectID: f.project.ID, ChapterID: f.chapter.ID, Mode: "premise"})
		if err != nil {
			t.Fatal(err)
		}
		done := runArtJob(t, ta, j.ID)
		if done.State != jobs.StateDone {
			t.Fatalf("local text failed: %+v", done.Error)
		}
		doc, err := f.studio.GetScript(context.Background(), f.chapter.ID)
		if err != nil || !strings.Contains(doc.Scenes[0].Panels[0].Action, "courier") {
			t.Fatal("local script not persisted", err)
		}
	})
}

func TestBatchRejectsInterveningHumanEdit(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, ps := prepareArt(t, ta, 2)
		gen.SetImageServiceForTest(f.gen, &fakeImages{})
		p := createArtPages(t, f, ps, true)[0]
		children, _ := ta.Deps.Jobs.List(ctx, jobs.Filter{ProjectID: f.project.ID, Types: []string{gen.JobPanel}, Limit: 10})
		if ran, err := ta.Deps.Executor.RunOnce(ctx); err != nil || !ran {
			t.Fatal(err)
		}
		p, _ = ps.GetPage(ctx, p.ID)
		p.Note = "A human note invalidates the remaining batch."
		if _, err := ps.SavePage(ctx, p, p.Version, pagestore.Change{ActorKind: "user", ActorID: f.owner.ID, Action: "save"}); err != nil {
			t.Fatal(err)
		}
		if ran, err := ta.Deps.Executor.RunOnce(ctx); err != nil || !ran {
			t.Fatal(err)
		}
		states := map[string]int{}
		for _, child := range children {
			j, _ := ta.Deps.Jobs.Get(ctx, child.ID)
			states[j.State]++
		}
		if states[jobs.StateDone] != 1 || states[jobs.StateFailed] != 1 {
			t.Fatal("manual edit was accepted as sibling history", states)
		}
	})
}
func TestCandidateApplyUsesLiveMembershipAndRevision(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, ps := prepareArt(t, ta, 1)
		gen.SetImageServiceForTest(f.gen, &fakeImages{})
		p := createArtPages(t, f, ps, false)[0]
		req := gen.PanelRequest{ProjectID: f.project.ID, PageID: p.ID, PanelID: p.Panels[0].ID, Variations: 4, ExpectedVersion: p.Version}
		j, err := f.gen.EnqueuePanel(ctx, f.actor, req)
		if err != nil {
			t.Fatal(err)
		}
		if done := runArtJob(t, ta, j.ID); done.State != jobs.StateDone {
			t.Fatal(done.Error)
		}
		if _, err = ta.Deps.DB.Exec(ctx, `UPDATE project_members SET role='viewer' WHERE project_id=? AND user_id=?`, f.project.ID, f.owner.ID); err != nil {
			t.Fatal(err)
		}
		if err = f.gen.ApplyVariation(ctx, f.actor, j.ID, 0); !errors.Is(err, studio.ErrForbidden) {
			t.Fatal("stale owner privileges used", err)
		}
		if _, err = ta.Deps.DB.Exec(ctx, `UPDATE project_members SET role='owner' WHERE project_id=? AND user_id=?`, f.project.ID, f.owner.ID); err != nil {
			t.Fatal(err)
		}
		p.Note = "Updated by the author"
		if _, err = ps.SavePage(ctx, p, p.Version, pagestore.Change{ActorKind: "user", ActorID: f.owner.ID, Action: "save"}); err != nil {
			t.Fatal(err)
		}
		if err = f.gen.ApplyVariation(ctx, f.actor, j.ID, 0); !errors.Is(err, pagestore.ErrVersion) {
			t.Fatal("stale candidate applied", err)
		}
		if _, err = f.gen.EnqueuePanel(ctx, f.actor, req); !errors.Is(err, studio.ErrConflict) {
			t.Fatal("caller revision ignored", err)
		}
	})
}
func TestImageCancellationLeaseLossAndActorRevocation(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		for _, scenario := range []string{"cancel", "lease", "suspend"} {
			t.Run(scenario, func(t *testing.T) {
				ctx := context.Background()
				f, ps := prepareArt(t, ta, 1)
				fake := &fakeImages{}
				gen.SetImageServiceForTest(f.gen, fake)
				p := createArtPages(t, f, ps, false)[0]
				j, err := f.gen.EnqueuePanel(ctx, f.actor, gen.PanelRequest{ProjectID: f.project.ID, PageID: p.ID, PanelID: p.Panels[0].ID, Variations: 1, ExpectedVersion: p.Version})
				if err != nil {
					t.Fatal(err)
				}
				fake.hook = func() {
					var err error
					switch scenario {
					case "cancel":
						_, err = ta.Deps.Jobs.Cancel(ctx, j.ID)
					case "lease":
						_, err = ta.Deps.DB.Exec(ctx, `UPDATE jobs SET attempts=attempts+1 WHERE id=?`, j.ID)
					case "suspend":
						_, err = ta.Deps.DB.Exec(ctx, `UPDATE users SET status='suspended' WHERE id=?`, f.owner.ID)
					}
					if err != nil {
						t.Error(err)
					}
				}
				done := runArtJob(t, ta, j.ID)
				if done.State == jobs.StateDone {
					t.Fatal("invalid attempt landed", scenario)
				}
				after, err := ps.GetPage(ctx, p.ID)
				if err != nil || after.Version != p.Version {
					t.Fatal("fence changed page", err)
				}
				var count int
				if err = ta.Deps.DB.Get(ctx, &count, `SELECT COUNT(*) FROM assets WHERE project_id=?`, f.project.ID); err != nil || count != 0 {
					t.Fatal("assets survived rejected commit", count, err)
				}
				objects, err := ta.Deps.Store.List(ctx, "projects/"+f.project.ID+"/")
				if err != nil || len(objects) != 0 {
					t.Fatal("staged objects survived", objects, err)
				}
			})
		}
	})
}
func TestImageEnvelopeAndTokenRechecks(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, ps := prepareArt(t, ta, 1)
		p := createArtPages(t, f, ps, false)[0]
		fake := &fakeImages{planHook: func(p *ai.ImagePlan) {
			p.ProviderID = "fixture-remote"
			p.ModelID = "fixture-art"
			p.Remote = true
			p.Estimate = jobs.Cost{USD: .2}
		}}
		gen.SetImageServiceForTest(f.gen, fake)
		threshold := 1.5
		_, token, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: f.owner.ID, Kind: auth.TokenAgent, Name: "Art agent", Scopes: []string{"projects:read", "projects:write", "generate"}, ProjectIDs: []string{f.project.ID}, ApproveOverUSD: &threshold})
		if err != nil {
			t.Fatal(err)
		}
		actor := studio.Actor{UserID: f.owner.ID, TokenID: token.ID}
		req := gen.PanelRequest{ProjectID: f.project.ID, PageID: p.ID, PanelID: p.Panels[0].ID, Variations: 4, ExpectedVersion: p.Version}
		if _, err = f.gen.EnqueuePanel(ctx, actor, req); err == nil {
			t.Fatal("aggregate approval ignored")
		}
		if fake.calls() != 0 {
			t.Fatal("planning called image provider")
		}
		if _, err = ta.Deps.DB.Exec(ctx, `UPDATE api_tokens SET approve_over_usd=2 WHERE id=?`, token.ID); err != nil {
			t.Fatal(err)
		}
		j, err := f.gen.EnqueuePanel(ctx, actor, req)
		if err != nil {
			t.Fatal(err)
		}
		if j.Estimate.USD != 1.8 {
			t.Fatalf("retry/layer envelope %+v", j.Estimate)
		}
		if ta.Deps.Jobs.PublicView(j).Input != nil {
			t.Fatal("private image prompt exposed")
		}
		fake.hook = func() {
			if err := ta.Deps.Auth.RevokeToken(ctx, f.owner.ID, token.ID, false); err != nil {
				t.Error(err)
			}
		}
		done := runArtJob(t, ta, j.ID)
		if done.State == jobs.StateDone || fake.calls() != 1 {
			t.Fatalf("revoked token continued calls: %d %+v", fake.calls(), done.Error)
		}
		// Replacing existing pages is always a human action, even with broad scopes.
		if _, err = f.gen.EnqueuePagesFromScript(ctx, actor, gen.PagesFromScriptRequest{ProjectID: f.project.ID, ChapterID: f.chapter.ID, Replace: true}); err == nil {
			t.Fatal("agent replacement accepted")
		}
	})
}
