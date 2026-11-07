package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
)

func imageTestPNG(t *testing.T, w, h int, c color.NRGBA) []byte {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			im.SetNRGBA(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func imageTestModule(t *testing.T, d *db.DB, doer httpDoer) (*Module, *aiDBFixture) {
	t.Helper()
	f := newAIDBFixture(t, d)
	store, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	deps := &core.Deps{DB: d, Settings: f.settings, Secrets: f.box, Store: store, Media: storage.NewSigner(f.box.Derive("test-images"))}
	deps.Jobs = jobs.New(d, f.settings, nil, jobs.Options{})
	return newModule(deps, doer, nil), f
}
func imageTestJob(t *testing.T, m *Module, f *aiDBFixture, p ImagePlan) string {
	t.Helper()
	id := f.seedRunningJob(t, p.ProviderID, 1)
	scope := jobs.Scope{Kind: jobs.ScopePage, ProjectID: f.projectID, PageID: db.NewID()}
	if _, err := m.d.DB.Exec(context.Background(), `UPDATE jobs SET type = ?, model_id = ?, scope = ?, scope_kind = 'page', page_id = ?, estimate = ? WHERE id = ?`, p.Task, p.ModelID, db.MustJSON(scope), scope.PageID, db.MustJSON(jobs.Cost{USD: p.Estimate.USD * 3, GPUSeconds: p.Estimate.GPUSeconds * 3}), id); err != nil {
		t.Fatal(err)
	}
	return id
}
func imageTestRequest(f *aiDBFixture, p ImagePlan, id string) ImageGenerateRequest {
	return ImageGenerateRequest{Plan: p, ActorID: f.userID, ProjectID: f.projectID, JobID: id, Attempt: 1, Prompt: "Rin carries an original paper lantern through the harbour.", Width: p.Needs.Width, Height: p.Needs.Height, Count: p.Needs.Count, Seed: 42}
}

func TestPlaceholderImageOracleAndMaskPixels(t *testing.T) {
	req := ImageGenerateRequest{Prompt: "An original courier", Width: 96, Height: 64, Count: 4, Seed: 7}
	one, err := placeholderImages(req)
	if err != nil {
		t.Fatal(err)
	}
	two, err := placeholderImages(req)
	if err != nil {
		t.Fatal(err)
	}
	for i := range one {
		if !bytes.Equal(one[i].Data, two[i].Data) {
			t.Fatal("placeholder is not deterministic")
		}
		if i > 0 && bytes.Equal(one[0].Data, one[i].Data) {
			t.Fatal("variations are identical")
		}
	}
	init := imageTestPNG(t, 96, 64, color.NRGBA{10, 20, 30, 128})
	mask := image.NewNRGBA(image.Rect(0, 0, 96, 64))
	for y := 0; y < 64; y++ {
		for x := 48; x < 96; x++ {
			mask.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, mask)
	req.Init = &ImageRef{Data: init, ContentType: "image/png"}
	req.Mask = &ImageRef{Data: b.Bytes(), ContentType: "image/png"}
	edited, err := placeholderImages(req)
	if err != nil {
		t.Fatal(err)
	}
	source, _, _ := storage.DecodeImage(init, storage.ImageLimits{})
	out, _, _ := storage.DecodeImage(edited[0].Data, storage.ImageLimits{})
	for y := 0; y < 64; y++ {
		for x := 0; x < 48; x++ {
			if source.At(x, y) != out.At(x, y) {
				t.Fatalf("outside-mask pixel changed at %d,%d", x, y)
			}
		}
	}
}

func TestImageQuotesUseRoundedMegapixelsAndRejectUnknownRoutes(t *testing.T) {
	for _, tc := range []struct {
		kind, model string
		w, h, n     int
		want        float64
	}{{providerKindFal, falSchnellModel, 1024, 1024, 4, .024}, {providerKindFal, falSchnellModel, 1000, 1000, 1, .003}, {providerKindFal, falInpaintModel, 1024, 1024, 4, .280}, {providerKindOpenRouter, seedreamModel, 4096, 4096, 4, .16}} {
		got, ok := quoteImage(tc.kind, tc.model, ImageNeeds{Width: tc.w, Height: tc.h, Count: tc.n, Edit: tc.model == falInpaintModel, Mask: tc.model == falInpaintModel})
		if !ok || got.USD != tc.want {
			t.Errorf("quote %+v = %+v %v", tc, got, ok)
		}
	}
	if _, ok := quoteImage(providerKindFal, "custom/unbounded", ImageNeeds{Width: 1024, Height: 1024, Count: 1}); ok {
		t.Fatal("unbounded route accepted")
	}
}

func TestImageRemoteReservationAndSanitizedDispatch(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		pngBytes := imageTestPNG(t, 64, 64, color.NRGBA{80, 90, 100, 255})
		fake := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){func(r *http.Request) (*http.Response, error) {
			return httpJSON(200, map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(pngBytes), "media_type": "image/png"}}, "usage": map[string]any{"cost": .025}}, nil), nil
		}}}
		m, f := imageTestModule(t, d, fake)
		provider := f.createProvider(t, m.providers, providerScopeServer, "image-secret")
		p, err := m.image.PlanImage(ctx, ImagePlanRequest{Task: TaskPanel, ActorID: f.userID, ProjectID: f.projectID, Needs: ImageNeeds{Width: 64, Height: 64, Count: 1}, PinProviderID: provider.ID})
		if err != nil {
			t.Fatal(err)
		}
		if p.ModelID != seedreamModel || !p.Remote || p.Estimate.USD != .04 {
			t.Fatalf("plan = %+v", p)
		}
		id := imageTestJob(t, m, f, p)
		result, err := m.image.GenerateImage(ctx, imageTestRequest(f, p, id))
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Images) != 1 || result.Actual.USD != .025 {
			t.Fatalf("result=%+v", result)
		}
		var row struct {
			State    string `db:"state"`
			Estimate int64  `db:"estimate_microusd"`
			Actual   int64  `db:"actual_microusd"`
		}
		if err := d.Get(ctx, &row, `SELECT state,estimate_microusd,actual_microusd FROM spend_reservations WHERE job_id = ?`, id); err != nil {
			t.Fatal(err)
		}
		if row.State != "reconciled" || row.Estimate != 40000 || row.Actual != 25000 {
			t.Fatalf("reservation=%+v", row)
		}
		seen := fake.requests()
		if seen[0].URL != "https://openrouter.ai/api/v1/images" || seen[0].Authorization != "Bearer image-secret" {
			t.Fatalf("wrong dispatch %+v", seen[0])
		}
		var body map[string]any
		_ = json.Unmarshal(seen[0].Body, &body)
		if body["n"] != float64(1) || body["model"] != seedreamModel {
			t.Fatalf("bad payload %v", body)
		}
	})
}

func TestImageLocalOnlyFreeOnlyAndForgedPlanSendNothing(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		fake := &queuedDoer{}
		m, f := imageTestModule(t, d, fake)
		provider := f.createProvider(t, m.providers, providerScopeServer, "blocked-image-secret")
		for _, mode := range []string{ModeLocalOnly, ModeFreeOnly} {
			p, err := m.image.PlanImage(ctx, ImagePlanRequest{Task: TaskPanel, ActorID: f.userID, ProjectID: f.projectID, RoutingMode: mode, Needs: ImageNeeds{Width: 64, Height: 64, Count: 1}})
			if err != nil || p.ProviderID != PlaceholderID {
				t.Fatalf("mode %s = %+v %v", mode, p, err)
			}
		}
		p, err := m.image.PlanImage(ctx, ImagePlanRequest{Task: TaskPanel, ActorID: f.userID, ProjectID: f.projectID, PinProviderID: provider.ID, Needs: ImageNeeds{Width: 64, Height: 64, Count: 1}})
		if err != nil {
			t.Fatal(err)
		}
		id := imageTestJob(t, m, f, p)
		req := imageTestRequest(f, p, id)
		req.Plan.ProviderID = db.NewID()
		if _, err := m.image.GenerateImage(ctx, req); err == nil {
			t.Fatal("forged provider accepted")
		}
		req.Plan = p
		updateInstance(t, f.settings, func(i *settings.Instance) { i.LocalOnly = true })
		if _, err := m.image.GenerateImage(ctx, req); err == nil {
			t.Fatal("local-only allowed dispatch")
		}
		if fake.count() != 0 {
			t.Fatal("blocked dispatch called HTTP")
		}
		var count int
		if err := d.Get(ctx, &count, `SELECT COUNT(*) FROM spend_reservations WHERE job_id = ?`, id); err != nil || count != 0 {
			t.Fatalf("blocked dispatch reserved: %d %v", count, err)
		}
	})
}

func TestImageSpendLimitAndBadOutputsConserveCharges(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		fake := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){func(*http.Request) (*http.Response, error) {
			return httpJSON(200, map[string]any{"data": []any{map[string]string{"b64_json": "not-an-image"}}}, nil), nil
		}}}
		m, f := imageTestModule(t, d, fake)
		provider := f.createProvider(t, m.providers, providerScopeServer, "bounded-key")
		p, err := m.image.PlanImage(ctx, ImagePlanRequest{Task: TaskPanel, ActorID: f.userID, ProjectID: f.projectID, PinProviderID: provider.ID, Needs: ImageNeeds{Width: 64, Height: 64, Count: 1}})
		if err != nil {
			t.Fatal(err)
		}
		id := imageTestJob(t, m, f, p)
		updateInstance(t, f.settings, func(i *settings.Instance) { i.Limits.ServerMonthlyRemoteSpendUSD = .039999 })
		_, err = m.image.GenerateImage(ctx, imageTestRequest(f, p, id))
		requireGenerateCode(t, err, CodeSpendLimit)
		if fake.count() != 0 {
			t.Fatal("over-cap request dispatched")
		}
		updateInstance(t, f.settings, func(i *settings.Instance) { i.Limits.ServerMonthlyRemoteSpendUSD = .04 })
		_, err = m.image.GenerateImage(ctx, imageTestRequest(f, p, id))
		requireGenerateCode(t, err, CodeBadOutput)
		var g *GenerateError
		if !errorsAsImage(err, &g) || g.Actual.USD != .04 {
			t.Fatalf("bad output charge lost: %v", err)
		}
	})
}

func errorsAsImage(err error, target **GenerateError) bool {
	g, ok := err.(*GenerateError)
	if ok {
		*target = g
	}
	return ok
}

func TestImageWorkerChildFencedAndReadsOnlyOwnedArtifacts(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		m, f := imageTestModule(t, d, &queuedDoer{})
		worker, err := m.d.Jobs.RegisterWorker(ctx, "image-test-token", "127.0.0.1", jobs.Registration{Name: "image-test", Backends: []string{"mock"}, Models: []string{"sdxl-base"}, JobTypes: []string{"image.generate"}, GPUs: []jobs.GPU{{Name: "test", VRAMTotalMB: 8000}}, MaxConcurrent: 1})
		if err != nil {
			t.Fatal(err)
		}
		p, err := m.image.PlanImage(ctx, ImagePlanRequest{Task: TaskPanel, ActorID: f.userID, ProjectID: f.projectID, RoutingMode: ModeFreeOnly, PinModelID: "sdxl-base", Needs: ImageNeeds{Width: 64, Height: 64, Count: 1}})
		if err != nil || p.Executor != jobs.ExecutorWorker {
			t.Fatalf("worker plan=%+v %v", p, err)
		}
		id := imageTestJob(t, m, f, p)
		done := make(chan error, 1)
		go func() {
			until := time.Now().Add(5 * time.Second)
			for time.Now().Before(until) {
				child, err := m.d.Jobs.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorWorker, Types: []string{"image.generate"}, Models: []string{"sdxl-base"}, WorkerID: worker.ID, MaxRunning: 1})
				if err != nil {
					done <- err
					return
				}
				if child == nil {
					time.Sleep(10 * time.Millisecond)
					continue
				}
				if child.ParentID != id {
					done <- fmt.Errorf("missing parent")
					return
				}
				a := assets.New(d, m.d.Store, m.d.Media)
				asset, err := a.Save(ctx, assets.SaveParams{OwnerID: f.userID, ProjectID: f.projectID, Kind: "job_artifact", Source: assets.SourceWorker, Prefix: "jobs/" + child.ID, Data: imageTestPNG(t, 64, 64, color.NRGBA{90, 100, 110, 255})})
				if err != nil {
					done <- err
					return
				}
				_, err = m.d.Jobs.Complete(ctx, child.ID, worker.ID, map[string]any{"images": []any{map[string]any{"key": asset.Key, "seed": 5}}}, jobs.Cost{GPUSeconds: 2.5})
				done <- err
				return
			}
			done <- fmt.Errorf("child not claimed")
		}()
		req := imageTestRequest(f, p, id)
		result, err := m.image.GenerateImage(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if len(result.Images) != 1 || result.Actual.GPUSeconds != 2.5 {
			t.Fatalf("worker result=%+v", result)
		}
		second, err := m.image.GenerateImage(ctx, req)
		if err != nil || !bytes.Equal(second.Images[0].Data, result.Images[0].Data) {
			t.Fatalf("child reuse=%+v %v", second, err)
		}
		var count int
		if err := d.Get(ctx, &count, `SELECT COUNT(*) FROM jobs WHERE parent_id = ?`, id); err != nil || count != 1 {
			t.Fatalf("duplicate child count=%d %v", count, err)
		}
		req.Attempt = 2
		if _, err := m.image.GenerateImage(ctx, req); err == nil {
			t.Fatal("stale parent attempt accepted")
		}
	})
}

func TestManifestOverridesCannotIntroduceRemotePrices(t *testing.T) {
	raw, _ := catalogueFiles.ReadFile("catalog/fal-schnell.yaml")
	if _, err := decodeManifest(raw); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeManifest(append(raw, []byte("\nprice: 0\n")...)); err == nil {
		t.Fatal("arbitrary price accepted")
	}
	if _, err := decodeManifest([]byte(strings.Replace(string(raw), "max_count: 4", "max_count: 100", 1))); err == nil {
		t.Fatal("unbounded count accepted")
	}
}

func TestImageFallbackFitsInitialEnvelopeAndNeverEscalatesFreeWork(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		m, f := imageTestModule(t, d, &queuedDoer{})
		or := f.createProvider(t, m.providers, providerScopeServer, "fallback-or")
		fal, err := m.providers.Create(ctx, providerActor{UserID: f.userID, Admin: true}, providerCreate{Kind: providerKindFal, Name: "Fallback fal", Key: "fallback-fal", Scope: providerScopeServer})
		if err != nil {
			t.Fatal(err)
		}
		p, err := m.image.PlanImage(ctx, ImagePlanRequest{Task: TaskRegionEdit, ActorID: f.userID, ProjectID: f.projectID, Needs: ImageNeeds{Width: 1024, Height: 1024, Count: 1, Edit: true, Mask: true}})
		if err != nil {
			t.Fatal(err)
		}
		if p.ProviderID != or.ID || p.Estimate.USD != .07 {
			t.Fatalf("initial envelope=%+v", p)
		}
		id := imageTestJob(t, m, f, p)
		j, err := m.d.Jobs.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		fallback, err := m.image.fallback(ctx, j, p, jobs.JobError{Code: CodeTimeout, Retryable: true})
		if err != nil || fallback == nil || fallback.ProviderID != fal.ID || fallback.Executor != jobs.ExecutorGo {
			t.Fatalf("fallback=%+v %v", fallback, err)
		}
		p.Remote = false
		fallback, err = m.image.fallback(ctx, j, p, jobs.JobError{Code: CodeTimeout, Retryable: true})
		if err != nil || fallback != nil && fallback.ProviderID != PlaceholderID {
			t.Fatalf("free fallback escalated: %+v %v", fallback, err)
		}
		p.Remote = true
		p.PinModelID = seedreamModel
		fallback, err = m.image.fallback(ctx, j, p, jobs.JobError{Code: CodeTimeout, Retryable: true})
		if err != nil || fallback != nil {
			t.Fatal("pinned route fell back")
		}
		p.PinModelID = ""
		for _, code := range []string{CodeProviderRefuse, CodeProviderAuth, CodeBadInput, CodeSpendLimit, jobs.ErrCodeCanceled} {
			fallback, err = m.image.fallback(ctx, j, p, jobs.JobError{Code: code})
			if err != nil || fallback != nil {
				t.Fatalf("terminal %s fell back", code)
			}
		}
		j.Attempts = 3
		fallback, err = m.image.fallback(ctx, j, p, jobs.JobError{Code: CodeTimeout})
		if err != nil || fallback != nil {
			t.Fatal("fallback exceeded the approved three attempts")
		}
	})
}

func TestImageLicenceGuardAndRoutingPins(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		m, f := imageTestModule(t, d, &queuedDoer{})
		p := f.createProvider(t, m.providers, providerScopeServer, "licence-key")
		manifest, err := m.catalog.lookup(ctx, seedreamModel)
		if err != nil {
			t.Fatal(err)
		}
		manifest.Commercial = false
		manifest.Licence = "test-noncommercial"
		raw, _ := json.Marshal(manifest)
		if _, err := d.Exec(ctx, `INSERT INTO models (id,source,manifest,enabled) VALUES (?,'custom',?,TRUE)`, manifest.ID, string(raw)); err != nil {
			t.Fatal(err)
		}
		req := ImagePlanRequest{Task: TaskPanel, ActorID: f.userID, ProjectID: f.projectID, PublicSeries: true, Needs: ImageNeeds{Width: 64, Height: 64, Count: 1}}
		plan, err := m.image.PlanImage(ctx, req)
		if err != nil || plan.ProviderID != PlaceholderID {
			t.Fatalf("public licence guard=%+v %v", plan, err)
		}
		req.PublicSeries = false
		plan, err = m.image.PlanImage(ctx, req)
		if err != nil || plan.ProviderID != p.ID {
			t.Fatalf("private route=%+v %v", plan, err)
		}
		if _, err := d.Exec(ctx, `INSERT INTO routing_pins (id,scope_kind,scope_id,task,model_id,provider_id) VALUES (?,'project',?,?,?,?)`, db.NewID(), f.projectID, TaskPanel, PlaceholderArt, PlaceholderID); err != nil {
			t.Fatal(err)
		}
		plan, err = m.image.PlanImage(ctx, req)
		if err != nil || plan.ProviderID != PlaceholderID || plan.PinModelID != PlaceholderArt {
			t.Fatalf("project pin=%+v %v", plan, err)
		}
	})
}
