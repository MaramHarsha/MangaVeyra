package ai_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/ai/router"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func jsonDecode(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }

func needGenerate(userID string) router.Need {
	return router.Need{Task: "generate", UserID: userID, Aspect: "2:3", Label: "p.7 panel 3 · regenerate"}
}

// fakeFal serves the fal queue API; fail makes every submission fail with a safety error.
func fakeFal(t *testing.T, fail *atomic.Bool) *httptest.Server {
	pngData := tinyPNG(t)
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			if fail.Load() {
				w.WriteHeader(422)
				writeJSON(w, map[string]any{"detail": "Content flagged by the safety checker"})
				return
			}
			writeJSON(w, map[string]any{"request_id": "r1", "status_url": srvURL + "/q/r1/status", "response_url": srvURL + "/q/r1"})
		case strings.HasSuffix(r.URL.Path, "/status"):
			writeJSON(w, map[string]any{"status": "COMPLETED"})
		case r.URL.Path == "/q/r1":
			writeJSON(w, map[string]any{"images": []any{map[string]any{"url": srvURL + "/img.png"}}, "seed": 5})
		case r.URL.Path == "/img.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngData)
		}
	}))
	srvURL = srv.URL
	t.Cleanup(srv.Close)
	return srv
}

func addFal(t *testing.T, ta *testutil.TestApp, svc *ai.Service, url string) *ai.Provider {
	t.Helper()
	p, err := svc.SaveProvider(context.Background(), ta.Admin, "", ai.ProviderInput{Kind: "fal", Name: "fal", BaseURL: url, Key: "fal-key"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunFallsBackAndLogsTheSwitch(t *testing.T) {
	for _, dialect := range dialects() {
		t.Run(dialect, func(t *testing.T) {
			ta, svc := newApp(t, testutil.WithDialect(dialect))
			ctx := context.Background()
			var fail atomic.Bool
			fail.Store(true)
			srv := fakeFal(t, &fail)
			addFal(t, ta, svc, srv.URL)

			var result *ai.Result
			ta.Deps.Executor.Register("test.ai.generate", func(ctx context.Context, run *jobs.Run) error {
				need := needGenerate("")
				need.Mode = router.ModeBest
				res, err := svc.Run(ctx, run, need, ai.Request{Prompt: "Kaito Mbeki on the harbour wall", Width: 832, Height: 1248, N: 1, Label: "p.7 panel 3"})
				if err != nil {
					return err
				}
				result = res
				run.SetOutput(res)
				return nil
			})
			j, err := ta.Deps.Jobs.Enqueue(ctx, jobs.EnqueueParams{Type: "test.ai.generate", Scope: jobs.Scope{Kind: jobs.ScopeNone}, UserID: ta.Admin.ID})
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := ta.Deps.Executor.RunOnce(ctx); !ok || err != nil {
				t.Fatalf("run once: %v %v", ok, err)
			}
			j, _ = ta.Deps.Jobs.Get(ctx, j.ID)
			if j.State != jobs.StateDone || result == nil {
				t.Fatalf("job %+v", j)
			}
			if !result.Placeholder || len(result.Images) != 1 || len(result.Attempts) == 0 {
				t.Fatalf("every fal model is blocked, so the placeholder closes the chain: %+v", result)
			}
			for _, a := range result.Attempts {
				if a.Code != "safety" {
					t.Fatalf("attempt %+v", a)
				}
			}
			evs, _ := ta.Deps.Jobs.Events(ctx, j.ID)
			fallbacks := 0
			for _, e := range evs {
				if e.Kind == "fallback" {
					fallbacks++
				}
			}
			if fallbacks != len(result.Attempts) {
				t.Fatalf("fallback events %d, attempts %d", fallbacks, len(result.Attempts))
			}
			if j.ModelID != "placeholder-image" || len(j.Decision) == 0 {
				t.Fatalf("job model %q", j.ModelID)
			}
			var n int
			_ = ta.Deps.DB.Get(ctx, &n, `SELECT COUNT(*) FROM routing_decisions WHERE job_id = ? AND status = 'done'`, j.ID)
			if n != 1 {
				t.Fatalf("decisions %d", n)
			}

			// Now fal works: the remote spend lands on the ledger and the job.
			fail.Store(false)
			j2, _ := ta.Deps.Jobs.Enqueue(ctx, jobs.EnqueueParams{Type: "test.ai.generate", Scope: jobs.Scope{Kind: jobs.ScopeNone}, UserID: ta.Admin.ID})
			if ok, _ := ta.Deps.Executor.RunOnce(ctx); !ok {
				t.Fatal("second run")
			}
			j2, _ = ta.Deps.Jobs.Get(ctx, j2.ID)
			if j2.State != jobs.StateDone || result.Placeholder || result.Cost.USD <= 0 || j2.Actual.USD != result.Cost.USD {
				t.Fatalf("remote run: job %+v result %+v", j2.Actual, result)
			}
			spent, _ := svc.MonthSpend(ctx, ta.Admin.ID)
			if spent != result.Cost.USD {
				t.Fatalf("ledger %v vs %v", spent, result.Cost.USD)
			}
		})
	}
}

func TestLocalOnlyNeverCallsRemote(t *testing.T) {
	ta, svc := newApp(t)
	ctx := context.Background()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer srv.Close()
	addFal(t, ta, svc, srv.URL)
	if _, err := ta.Deps.Settings.UpdateInstance(ctx, "", func(i *settingsInstance) error { i.LocalOnly = true; return nil }); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Run(ctx, nil, router.Need{Task: "generate", UserID: ta.Admin.ID, Mode: router.ModeBest}, ai.Request{Prompt: "x", N: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Placeholder || calls.Load() != 0 {
		t.Fatalf("local only leaked: calls %d result %+v", calls.Load(), res)
	}
	tr, err := svc.TestProvider(ctx, ta.Admin, res.ProviderID, "")
	if err != nil || !tr.OK || tr.ImageKey == "" {
		t.Fatalf("placeholder test: %+v %v", tr, err)
	}
}

func TestSpendGuardAndTextTasks(t *testing.T) {
	ta, svc := newApp(t)
	ctx := context.Background()
	if _, err := ta.Deps.Settings.UpdateInstance(ctx, "", func(i *settingsInstance) error {
		i.Limits.ServerMonthlyRemoteSpendUSD = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err := ta.Deps.Jobs.Enqueue(ctx, jobs.EnqueueParams{Type: "x", Scope: jobs.Scope{Kind: jobs.ScopeNone}, UserID: ta.Admin.ID, Estimate: jobs.Cost{USD: 2}})
	if err == nil || !strings.Contains(err.Error(), "spend") {
		t.Fatalf("spend guard: %v", err)
	}
	out, err := svc.Text(ctx, router.Need{UserID: ta.Admin.ID, Task: "translate"}, ai.TextRequest{Prompt: "Hello", Lang: "ja"})
	if err != nil || !out.Placeholder || !strings.Contains(out.Text, "ja") {
		t.Fatalf("text %+v %v", out, err)
	}
	ex, err := svc.Explain(ctx, router.Need{Task: "text", UserID: ta.Admin.ID}, "")
	if err != nil || !strings.HasPrefix(ex.Label, "Auto → ") {
		t.Fatalf("explain %+v %v", ex, err)
	}
}

// workerLoop plays a worker: it claims worker jobs and completes them with a stored image.
func workerLoop(t *testing.T, ta *testutil.TestApp, workerID string, fail bool, stop <-chan struct{}) {
	ctx := context.Background()
	for {
		select {
		case <-stop:
			return
		default:
		}
		j, err := ta.Deps.Jobs.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorWorker, Types: []string{"image.generate", "image.edit"},
			WorkerID: workerID, Models: []string{"flux2-klein"}})
		if err != nil || j == nil {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		if fail {
			_, _ = ta.Deps.Jobs.Fail(ctx, j.ID, workerID, jobs.JobError{Code: "oom", Message: "CUDA out of memory", Retryable: true}, jobs.Cost{GPUSeconds: 2})
			continue
		}
		key := "jobs/" + j.ID + "/variation-1.png"
		png := tinyPNG(t)
		_ = ta.Deps.Store.Put(ctx, key, bytes.NewReader(png), int64(len(png)), "image/png")
		_, _ = ta.Deps.Jobs.Progress(ctx, j.ID, workerID, jobs.ProgressUpdate{Progress: 0.5, Message: "denoising"})
		_, _ = ta.Deps.Jobs.Complete(ctx, j.ID, workerID, map[string]any{"images": []any{map[string]any{"key": key, "seed": 9}}}, jobs.Cost{GPUSeconds: 6.5})
	}
}

func TestRunRedispatchesToWorker(t *testing.T) {
	ta, svc := newApp(t)
	ctx := context.Background()
	w, err := ta.Deps.Jobs.RegisterWorker(ctx, "env", "10.0.0.3", jobs.Registration{Name: "studio-pc", JobTypes: []string{"image.generate", "image.edit"},
		Models: []string{"flux2-klein"}, GPUs: []jobs.GPU{{Name: "NVIDIA RTX 4080", VRAMTotalMB: 16384, VRAMFreeMB: 16000}}, MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer close(stop)
	go workerLoop(t, ta, w.ID, false, stop)
	ref := tinyPNG(t)
	res, err := svc.Run(ctx, nil, router.Need{Task: "generate", UserID: ta.Admin.ID, Mode: router.ModeLocalOnly},
		ai.Request{Prompt: "Old Wren mending nets", Width: 512, Height: 768, N: 1, Refs: []ai.Input{{Data: ref}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.ModelID != "flux2-klein" || res.ProviderKind != "local_worker" || len(res.Images) != 1 || res.Images[0].Seed != 9 || res.Cost.GPUSeconds != 6.5 {
		t.Fatalf("worker result %+v", res)
	}
	// The child job carried the reference as a stored file and the worker-protocol input.
	list, _ := ta.Deps.Jobs.List(ctx, jobs.Filter{Executor: jobs.ExecutorWorker})
	if len(list) != 1 || list[0].Files["ref_0"] == "" || !strings.Contains(string(list[0].Input), `"width":512`) {
		t.Fatalf("child job %+v", list)
	}
	// GPU time reaches the ledger through the finish hook (once).
	var gpu float64
	_ = ta.Deps.DB.Get(ctx, &gpu, `SELECT COALESCE(SUM(gpu_seconds), 0) FROM spend_ledger`)
	if gpu != 6.5 {
		t.Fatalf("gpu ledger %v", gpu)
	}
	m, _ := svc.Meter(ctx, ta.Admin.ID)
	if m.GPU != "NVIDIA RTX 4080" || m.WeekGPUSeconds != 6.5 || m.WorkersOnline != 1 {
		t.Fatalf("meter %+v", m)
	}
}

func TestWorkerJobFailsOverToRemote(t *testing.T) {
	ta, svc := newApp(t)
	ctx := context.Background()
	var fail atomic.Bool
	srv := fakeFal(t, &fail)
	fal := addFal(t, ta, svc, srv.URL)
	w, _ := ta.Deps.Jobs.RegisterWorker(ctx, "env", "10.0.0.4", jobs.Registration{Name: "tiny", JobTypes: []string{"image.generate"},
		Models: []string{"flux2-klein"}, MaxConcurrent: 1})
	// Route the job the way the gen module does and enqueue it on the worker.
	dec, err := svc.Route(ctx, router.Need{Task: "generate", UserID: ta.Admin.ID, ModelID: "flux2-klein", ProviderID: ai.LocalWorkerID}, "")
	if err != nil {
		t.Fatal(err)
	}
	// Make fal flux2-klein the next candidate after the local worker.
	chain := []router.Candidate{*dec.Pick()}
	for _, c := range dec.Chain {
		if c.ProviderID == fal.ID && c.ModelID == "flux2-klein" {
			chain = append(chain, c)
		}
	}
	dec.Chain = chain
	j, err := ta.Deps.Jobs.Enqueue(ctx, jobs.EnqueueParams{Type: "image.generate", Scope: jobs.Scope{Kind: jobs.ScopeNone}, UserID: ta.Admin.ID,
		Executor: jobs.ExecutorWorker, ModelID: "flux2-klein", ProviderID: ai.LocalWorkerID, Decision: dec,
		Input: map[string]any{"prompt": "Sora Vell at the lighthouse", "width": 512, "height": 512, "n": 1}})
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	go workerLoop(t, ta, w.ID, true, stop)
	deadline := time.Now().Add(5 * time.Second)
	for {
		cur, _ := ta.Deps.Jobs.Get(ctx, j.ID)
		if cur.Executor == jobs.ExecutorGo && cur.State == jobs.StateQueued {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no failover: %+v", cur)
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(stop)
	if ok, err := ta.Deps.Executor.RunOnce(ctx); !ok || err != nil {
		t.Fatalf("go run: %v %v", ok, err)
	}
	cur, _ := ta.Deps.Jobs.Get(ctx, j.ID)
	if cur.State != jobs.StateDone || cur.ProviderID != fal.ID || !strings.Contains(string(cur.Output), `"images"`) {
		t.Fatalf("after failover %+v %s", cur, cur.Output)
	}
	evs, _ := ta.Deps.Jobs.Events(ctx, j.ID)
	found := false
	for _, e := range evs {
		found = found || (e.Kind == "fallback" && strings.Contains(e.Message, "falling back"))
	}
	if !found {
		t.Fatal("fallback not logged")
	}
	// Workers receive manifests for claimed models.
	lookup := core.MustUse[jobs.ManifestLookup](ta.Deps)
	raw, err := lookup(ctx, "flux2-klein")
	if err != nil || !strings.Contains(string(raw), `"diffusers"`) {
		t.Fatalf("manifest %s %v", raw, err)
	}
}
