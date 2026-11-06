package jobs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

func forEach(t *testing.T, fn func(t *testing.T, ta *testutil.TestApp)) {
	for _, d := range testdb.Dialects() {
		t.Run(d, func(t *testing.T) { fn(t, testutil.NewTestApp(t, testutil.WithDialect(d))) })
	}
}

var none = jobs.Scope{Kind: jobs.ScopeNone}

type recorder struct {
	mu     sync.Mutex
	topics map[string][]string
}

func (r *recorder) Publish(topic, typ string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.topics == nil {
		r.topics = map[string][]string{}
	}
	r.topics[topic] = append(r.topics[topic], typ)
}

func (r *recorder) get(topic string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.topics[topic]...)
}

func TestScopeValidation(t *testing.T) {
	valid := []struct {
		name  string
		scope jobs.Scope
	}{
		{"chapter", jobs.Scope{Kind: jobs.ScopeChapter, ProjectID: "project", ChapterID: "chapter"}},
		{"page", jobs.Scope{Kind: jobs.ScopePage, ProjectID: "project", PageID: "page"}},
		{"panel", jobs.Scope{Kind: jobs.ScopePanel, ProjectID: "project", PageID: "page", PanelID: "panel"}},
		{"layer", jobs.Scope{Kind: jobs.ScopeLayer, ProjectID: "project", PanelID: "panel", LayerID: "layer"}},
		{"region", jobs.Scope{Kind: jobs.ScopeRegion, ProjectID: "project", PanelID: "panel", Region: json.RawMessage(`{"box":[1,2,3,4]}`)}},
		{"project", jobs.Scope{Kind: jobs.ScopeProject, ProjectID: "project"}},
		{"none", none},
	}
	for _, tc := range valid {
		t.Run("valid/"+tc.name, func(t *testing.T) {
			if err := tc.scope.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}

	missing := []struct {
		name  string
		scope jobs.Scope
		want  []string
	}{
		{"kind", jobs.Scope{}, []string{"scope kind is required"}},
		{"unknown kind", jobs.Scope{Kind: "galaxy"}, []string{`unknown scope kind "galaxy"`}},
		{"chapter ids", jobs.Scope{Kind: jobs.ScopeChapter}, []string{"project_id", "chapter_id"}},
		{"page ids", jobs.Scope{Kind: jobs.ScopePage}, []string{"project_id", "page_id"}},
		{"panel ids", jobs.Scope{Kind: jobs.ScopePanel}, []string{"project_id", "page_id", "panel_id"}},
		{"layer ids", jobs.Scope{Kind: jobs.ScopeLayer}, []string{"project_id", "panel_id", "layer_id"}},
		{"region fields", jobs.Scope{Kind: jobs.ScopeRegion}, []string{"project_id", "panel_id", "needs region"}},
		{"project id", jobs.Scope{Kind: jobs.ScopeProject}, []string{"project_id"}},
	}
	for _, tc := range missing {
		t.Run("missing/"+tc.name, func(t *testing.T) {
			err := tc.scope.Validate()
			if err == nil {
				t.Fatal("Validate() accepted incomplete scope")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Validate() error = %q, want substring %q", err, want)
				}
			}
		})
	}

	validByKind := make(map[string]jobs.Scope, len(valid))
	for _, tc := range valid {
		validByKind[tc.scope.Kind] = tc.scope
	}
	forbiddenIDs := map[string][]string{
		jobs.ScopeChapter: {"page_id", "panel_id", "layer_id"},
		jobs.ScopePage:    {"chapter_id", "panel_id", "layer_id"},
		jobs.ScopePanel:   {"chapter_id", "layer_id"},
		jobs.ScopeLayer:   {"chapter_id", "page_id"},
		jobs.ScopeRegion:  {"chapter_id", "page_id", "layer_id"},
		jobs.ScopeProject: {"chapter_id", "page_id", "panel_id", "layer_id"},
		jobs.ScopeNone:    {"project_id", "chapter_id", "page_id", "panel_id", "layer_id"},
	}
	setID := func(s *jobs.Scope, field string) {
		switch field {
		case "project_id":
			s.ProjectID = "extra"
		case "chapter_id":
			s.ChapterID = "extra"
		case "page_id":
			s.PageID = "extra"
		case "panel_id":
			s.PanelID = "extra"
		case "layer_id":
			s.LayerID = "extra"
		default:
			t.Fatalf("unknown test field %q", field)
		}
	}
	for kind, fields := range forbiddenIDs {
		for _, field := range fields {
			t.Run("extraneous/"+kind+"/"+field, func(t *testing.T) {
				scope := validByKind[kind]
				setID(&scope, field)
				err := scope.Validate()
				if err == nil || !strings.Contains(err.Error(), "forbids "+field) {
					t.Fatalf("Validate() error = %v, want forbidden %s", err, field)
				}
			})
		}
	}

	for _, tc := range valid {
		if tc.scope.Kind == jobs.ScopeRegion {
			continue
		}
		t.Run("forbidden region/"+tc.name, func(t *testing.T) {
			scope := tc.scope
			scope.Region = json.RawMessage(`{"box":[1,2,3,4]}`)
			err := scope.Validate()
			if err == nil || !strings.Contains(err.Error(), "forbids region") {
				t.Fatalf("Validate() error = %v, want forbidden region", err)
			}
		})
	}

	for _, region := range []json.RawMessage{nil, json.RawMessage{}, json.RawMessage(`null`), json.RawMessage(" \nnull\t")} {
		t.Run("required region/"+string(region), func(t *testing.T) {
			scope := jobs.Scope{Kind: jobs.ScopeRegion, ProjectID: "project", PanelID: "panel", Region: region}
			err := scope.Validate()
			if err == nil || !strings.Contains(err.Error(), "needs region") {
				t.Fatalf("Validate() error = %v, want required region", err)
			}
		})
	}

	t.Run("explicit null is forbidden outside region scope", func(t *testing.T) {
		scope := jobs.Scope{Kind: jobs.ScopeNone, Region: json.RawMessage(`null`)}
		err := scope.Validate()
		if err == nil || !strings.Contains(err.Error(), "forbids region") {
			t.Fatalf("Validate() error = %v, want forbidden region", err)
		}
	})
}

func TestEnqueueValidatorAndEvents(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		q := ta.Deps.Jobs
		rec := &recorder{}
		q.SetPublisher(rec)
		locked := errors.New("panel is locked")
		q.AddValidator(func(ctx context.Context, p *jobs.EnqueueParams) error {
			if p.Scope.PanelID == "locked-panel" {
				return httpx.ErrLocked(locked.Error())
			}
			return nil
		})
		proj := seedProject(t, ta)
		_, err := q.Enqueue(ctx, jobs.EnqueueParams{Type: "image.generate", UserID: ta.Admin.ID,
			Scope: jobs.Scope{Kind: jobs.ScopePanel, ProjectID: proj, PageID: "pg", PanelID: "locked-panel"}})
		var ae *httpx.APIError
		if !errors.As(err, &ae) || ae.Code != httpx.CodeLocked {
			t.Fatalf("lock validator not applied: %v", err)
		}
		if _, err := q.Enqueue(ctx, jobs.EnqueueParams{Type: "image.generate", Scope: jobs.Scope{Kind: jobs.ScopePage}}); err == nil {
			t.Fatal("invalid scope accepted")
		}
		j, err := q.Enqueue(ctx, jobs.EnqueueParams{Type: "image.generate", UserID: ta.Admin.ID, Executor: jobs.ExecutorWorker,
			Scope: jobs.Scope{Kind: jobs.ScopePanel, ProjectID: proj, PageID: "pg", PanelID: "p1"}, Input: map[string]any{"prompt": "Rin on the pier"},
			Estimate: jobs.Cost{GPUSeconds: 40}, ModelID: "flux2-klein"})
		if err != nil {
			t.Fatal(err)
		}
		if j.State != jobs.StateQueued || j.Estimate.GPUSeconds != 40 || j.Scope.PanelID != "p1" || !strings.Contains(string(j.Input), "pier") {
			t.Fatalf("job %+v", j)
		}
		for _, topic := range []string{"job:" + j.ID, "user:" + ta.Admin.ID, "project:" + proj, "admin:queue"} {
			if ev := rec.get(topic); len(ev) != 1 || ev[0] != "job.queued" {
				t.Fatalf("%s: %v", topic, ev)
			}
		}
		evs, err := q.Events(ctx, j.ID)
		if err != nil || len(evs) != 1 || evs[0].Kind != "created" {
			t.Fatalf("events %+v %v", evs, err)
		}
	})
}

func seedProject(t *testing.T, ta *testutil.TestApp) string {
	t.Helper()
	id := db.NewID()
	if _, err := ta.Deps.DB.Exec(context.Background(), `INSERT INTO projects (id, owner_id, title, slug) VALUES (?, ?, ?, ?)`,
		id, ta.Admin.ID, "Tidebreak Courier", "tidebreak-"+id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestClaimOrderingAndFairShare(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		q := ta.Deps.Jobs
		alice := ta.CreateUser(t, "alice", auth.RoleCreator)
		bob := ta.CreateUser(t, "bob", auth.RoleCreator)
		enq := func(user string, prio int) *jobs.Job {
			j, err := q.Enqueue(ctx, jobs.EnqueueParams{Type: "t", UserID: user, Scope: none, Priority: prio})
			if err != nil {
				t.Fatal(err)
			}
			return j
		}
		a1, a2, a3 := enq(alice.ID, 0), enq(alice.ID, 0), enq(alice.ID, 0)
		b1 := enq(bob.ID, 0)
		urgent := enq(bob.ID, 10)
		claim := func() *jobs.Job {
			j, err := q.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorGo, Types: []string{"t"}})
			if err != nil {
				t.Fatal(err)
			}
			return j
		}
		order := []string{}
		for i := 0; i < 5; i++ {
			j := claim()
			if j == nil {
				break
			}
			order = append(order, j.ID)
		}
		// priority first; then alice (0 running) before bob (1 running); at 1–1
		// the older job wins (a2); then bob (1 running) before alice (2 running).
		want := []string{urgent.ID, a1.ID, a2.ID, b1.ID, a3.ID}
		if strings.Join(order, ",") != strings.Join(want, ",") {
			t.Fatalf("claim order\n got %v\nwant %v", order, want)
		}
		// per-user running limit
		if _, err := ta.Deps.Settings.UpdateInstance(ctx, "", func(i *settings.Instance) error { i.Limits.RunningJobs = 1; return nil }); err != nil {
			t.Fatal(err)
		}
		enq(alice.ID, 0)
		enq(bob.ID, 0)
		if j := claim(); j != nil {
			t.Fatalf("claimed %s although both users are at their limit", j.ID)
		}
		carol := ta.CreateUser(t, "carol", auth.RoleCreator)
		c1 := enq(carol.ID, 0)
		if j := claim(); j == nil || j.ID != c1.ID {
			t.Fatalf("carol should run: %+v", j)
		}
		// finishing alice's jobs frees her slot
		for _, id := range []string{a1.ID, a2.ID, a3.ID} {
			if _, err := q.Complete(ctx, id, "", map[string]any{"ok": true}, jobs.Cost{}); err != nil {
				t.Fatal(err)
			}
		}
		if j := claim(); j == nil || j.UserID != alice.ID {
			t.Fatalf("alice's next job should run: %+v", j)
		}
	})
}

func TestNightModeDefersBatchJobs(t *testing.T) {
	ta := testutil.NewTestApp(t)
	ctx := context.Background()
	q := ta.Deps.Jobs
	h := time.Now().Hour()
	// a one-hour night window that excludes the current hour
	start := (h + 2) % 24
	if _, err := ta.Deps.Settings.UpdateInstance(ctx, "", func(i *settings.Instance) error {
		i.NightMode = settings.NightMode{Enabled: true, StartHour: start, EndHour: (start + 1) % 24}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(ctx, jobs.EnqueueParams{Type: "t", Scope: none, Batch: true}); err != nil {
		t.Fatal(err)
	}
	inter, _ := q.Enqueue(ctx, jobs.EnqueueParams{Type: "t", Scope: none})
	j, _ := q.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorGo, Types: []string{"t"}})
	if j == nil || j.ID != inter.ID {
		t.Fatalf("interactive job should run first: %+v", j)
	}
	if j, _ := q.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorGo, Types: []string{"t"}}); j != nil {
		t.Fatal("batch job ran outside the night window")
	}
}

func TestRetryFailoverCancelAndLeaseExpiry(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		q := ta.Deps.Jobs
		clock := testutil.NewClock(time.Now())
		q.Now = clock.Now
		claim := func() *jobs.Job {
			j, err := q.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorGo, Types: []string{"t"}})
			if err != nil {
				t.Fatal(err)
			}
			return j
		}

		// Retryable failure → requeued with backoff, then permanent failure after max attempts.
		j, _ := q.Enqueue(ctx, jobs.EnqueueParams{Type: "t", Scope: none, MaxAttempts: 2})
		claim()
		j, err := q.Fail(ctx, j.ID, "", jobs.JobError{Code: "timeout", Message: "remote timed out", Retryable: true}, jobs.Cost{USD: 0.01})
		if err != nil || j.State != jobs.StateQueued || j.RunAfter == nil {
			t.Fatalf("retry: %+v %v", j, err)
		}
		if claim() != nil {
			t.Fatal("claimed before backoff elapsed")
		}
		clock.Advance(6 * time.Second)
		if c := claim(); c == nil || c.Attempts != 2 {
			t.Fatalf("retry claim: %+v", c)
		}
		j, _ = q.Fail(ctx, j.ID, "", jobs.JobError{Code: "timeout", Message: "again", Retryable: true}, jobs.Cost{USD: 0.01})
		if j.State != jobs.StateFailed || j.Error == nil || j.Error.Code != "timeout" || j.Actual.USD < 0.019 || j.FinishedAt == nil {
			t.Fatalf("final failure: %+v", j)
		}
		// manual retry resets the attempt budget
		if j, err = q.Retry(ctx, j.ID); err != nil || j.State != jobs.StateQueued || j.Attempts != 0 {
			t.Fatalf("retry: %+v %v", j, err)
		}
		claim()
		q.Complete(ctx, j.ID, "", nil, jobs.Cost{})

		// Failover: the router moves the job to the next model.
		q.SetFailover(func(ctx context.Context, j *jobs.Job, e jobs.JobError) (*jobs.Reroute, error) {
			if j.ModelID == "model-a" && e.Code == "safety" {
				return &jobs.Reroute{ModelID: "model-b", ProviderID: "local", Reason: "safety block on model-a"}, nil
			}
			return nil, nil
		})
		j, _ = q.Enqueue(ctx, jobs.EnqueueParams{Type: "t", Scope: none, ModelID: "model-a"})
		claim()
		j, _ = q.Fail(ctx, j.ID, "", jobs.JobError{Code: "safety", Message: "blocked"}, jobs.Cost{})
		if j.State != jobs.StateQueued || j.ModelID != "model-b" {
			t.Fatalf("failover: %+v", j)
		}
		evs, _ := q.Events(ctx, j.ID)
		if evs[len(evs)-1].Kind != "fallback" || !strings.Contains(string(evs[len(evs)-1].Data), "model-b") {
			t.Fatalf("fallback not logged: %+v", evs[len(evs)-1])
		}
		claim()
		q.Complete(ctx, j.ID, "", nil, jobs.Cost{})
		q.SetFailover(nil)

		// Cancel: queued → canceled immediately; running → requested, then canceled on fail.
		qj, _ := q.Enqueue(ctx, jobs.EnqueueParams{Type: "t", Scope: none})
		if c, _ := q.Cancel(ctx, qj.ID); c.State != jobs.StateCanceled {
			t.Fatalf("cancel queued: %+v", c)
		}
		if _, err := q.Cancel(ctx, qj.ID); err == nil {
			t.Fatal("canceled twice")
		}
		rj, _ := q.Enqueue(ctx, jobs.EnqueueParams{Type: "t", Scope: none})
		claim()
		hooked := ""
		q.OnCancel(func(id string) { hooked = id })
		if c, _ := q.Cancel(ctx, rj.ID); c.State != jobs.StateRunning || !c.CancelRequested || hooked != rj.ID {
			t.Fatalf("cancel running: %+v hook=%q", c, hooked)
		}
		if cancel, _ := q.Progress(ctx, rj.ID, "", jobs.ProgressUpdate{Progress: 0.5}); !cancel {
			t.Fatal("progress must report the cancel request")
		}
		if c, _ := q.Fail(ctx, rj.ID, "", jobs.JobError{Code: "internal", Message: "stopped", Retryable: true}, jobs.Cost{}); c.State != jobs.StateCanceled {
			t.Fatalf("fail after cancel request must cancel: %+v", c)
		}

		// Lease expiry → requeued; expiry on the last attempt → failed with timeout.
		lj, _ := q.Enqueue(ctx, jobs.EnqueueParams{Type: "t", Scope: none, MaxAttempts: 2})
		claim()
		clock.Advance(2 * time.Minute)
		if n, err := q.RequeueExpired(ctx); err != nil || n != 1 {
			t.Fatalf("requeue expired: %d %v", n, err)
		}
		if g, _ := q.Get(ctx, lj.ID); g.State != jobs.StateQueued {
			t.Fatalf("after expiry: %+v", g)
		}
		clock.Advance(time.Minute)
		claim()
		clock.Advance(2 * time.Minute)
		q.RequeueExpired(ctx)
		if g, _ := q.Get(ctx, lj.ID); g.State != jobs.StateFailed || g.Error.Code != jobs.ErrCodeTimeout {
			t.Fatalf("expired on last attempt: %+v", g)
		}
	})
}

func TestExecutorPool(t *testing.T) {
	ta := testutil.NewTestApp(t)
	ctx := context.Background()
	q := ta.Deps.Jobs
	pool := jobs.NewPool(q, 2, slog.New(slog.NewTextHandler(io.Discard, nil)))
	pool.Register("text.script", func(ctx context.Context, run *jobs.Run) error {
		var in struct{ Premise string }
		if err := run.DecodeInput(&in); err != nil {
			return err
		}
		if err := run.Progress(ctx, 0.5, 1, 2, "drafting"); err != nil {
			return err
		}
		run.AddActual(jobs.Cost{USD: 0.002})
		run.SetOutput(map[string]string{"script": "Rin runs the tide line: " + in.Premise})
		return nil
	})
	pool.Register("bad.input", func(ctx context.Context, run *jobs.Run) error {
		return jobs.Permanent(jobs.ErrCodeBadInput, errors.New("premise is empty"))
	})
	pool.Register("panics", func(ctx context.Context, run *jobs.Run) error { panic("boom") })
	var finished []string
	q.OnFinish(func(ctx context.Context, j *jobs.Job) { finished = append(finished, j.State) })

	j1, _ := q.Enqueue(ctx, jobs.EnqueueParams{Type: "text.script", Scope: none, Input: map[string]string{"premise": "a courier in a drowned city"}})
	j2, _ := q.Enqueue(ctx, jobs.EnqueueParams{Type: "bad.input", Scope: none})
	j3, _ := q.Enqueue(ctx, jobs.EnqueueParams{Type: "panics", Scope: none, MaxAttempts: 1})
	for i := 0; i < 3; i++ {
		if ok, err := pool.RunOnce(ctx); !ok || err != nil {
			t.Fatalf("run once %d: %v %v", i, ok, err)
		}
	}
	g1, _ := q.Get(ctx, j1.ID)
	var out struct{ Script string }
	if g1.State != jobs.StateDone || g1.DecodeOutput(&out) != nil || !strings.Contains(out.Script, "drowned city") || g1.Actual.USD != 0.002 || g1.Progress != 1 {
		t.Fatalf("done job: %+v", g1)
	}
	if g2, _ := q.Get(ctx, j2.ID); g2.State != jobs.StateFailed || g2.Error.Code != jobs.ErrCodeBadInput || g2.Attempts != 1 {
		t.Fatalf("permanent failure: %+v", g2)
	}
	if g3, _ := q.Get(ctx, j3.ID); g3.State != jobs.StateFailed || !strings.Contains(g3.Error.Message, "panicked") {
		t.Fatalf("panic: %+v", g3)
	}
	if len(finished) != 3 {
		t.Fatalf("finish hooks: %v", finished)
	}

	// Background pool: cancellation reaches the handler's context.
	started := make(chan struct{})
	pool.Register("slow", func(ctx context.Context, run *jobs.Run) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	pctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { pool.Start(pctx); close(done) }()
	slow, _ := q.Enqueue(ctx, jobs.EnqueueParams{Type: "slow", Scope: none})
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("pool did not pick up the job")
	}
	if _, err := q.Cancel(ctx, slow.ID); err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if g, err := q.Wait(wctx, slow.ID, 20*time.Millisecond); err != nil || g.State != jobs.StateCanceled {
		t.Fatalf("slow job: %+v %v", g, err)
	}
	stop()
	<-done
}

// ---- worker protocol end to end ------------------------------------------------

type workerClient struct {
	t     *testing.T
	c     *testutil.Client
	id    string
	token string
}

func (w *workerClient) post(path string, body any) *testutil.Response {
	w.t.Helper()
	return w.c.PostJSON("/api/worker/v1"+path, body)
}

func TestWorkerProtocolEndToEnd(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		q := ta.Deps.Jobs
		raw, _, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: ta.Admin.ID, Kind: auth.TokenWorker, Name: "gpu box"})
		if err != nil {
			t.Fatal(err)
		}
		w := &workerClient{t: t, c: ta.BearerClient(raw), token: raw}

		// Anonymous and user sessions cannot use the worker API.
		if r := ta.NewClient().PostJSON("/api/worker/v1/register", map[string]any{"name": "x"}); r.StatusCode != 401 {
			t.Fatalf("anonymous register: %d", r.StatusCode)
		}
		reg := map[string]any{"name": "studio-pc", "version": "1.0.0", "backends": []string{"mock"},
			"gpus":      []map[string]any{{"name": "NVIDIA GeForce RTX 4090", "vram_total_mb": 24564, "vram_free_mb": 20110}},
			"job_types": []string{"image.generate", "image.inpaint"}, "models": []string{"flux2-klein"}, "max_concurrent": 1}
		var regResp struct {
			WorkerID  string `json:"worker_id"`
			Heartbeat int    `json:"heartbeat_interval_s"`
			LeaseS    int    `json:"lease_s"`
		}
		w.post("/register", reg).JSON(t, &regResp)
		if regResp.WorkerID == "" || regResp.Heartbeat != 15 || regResp.LeaseS != 120 {
			t.Fatalf("register: %+v", regResp)
		}
		var again struct {
			WorkerID string `json:"worker_id"`
		}
		w.post("/register", reg).JSON(t, &again)
		if again.WorkerID != regResp.WorkerID {
			t.Fatal("re-registering must keep the worker id")
		}
		w.id = regResp.WorkerID

		// An input file the worker downloads through a signed URL.
		if err := storage.PutBytes(ctx, ta.Deps.Store, "projects/p/panel.png", []byte("\x89PNG\r\n\x1a\nfake"), "image/png"); err != nil {
			t.Fatal(err)
		}
		// Unknown job types are never handed out.
		q.Enqueue(ctx, jobs.EnqueueParams{Type: "video.animate", Executor: jobs.ExecutorWorker, Scope: none})
		// Empty long poll → 204.
		start := time.Now()
		if r := w.post("/claim", map[string]any{"worker_id": w.id, "job_types": []string{"image.generate", "video.animate"}, "wait_s": 1}); r.StatusCode != 204 {
			t.Fatalf("empty claim: %d %s", r.StatusCode, r.Body)
		}
		if time.Since(start) < 900*time.Millisecond {
			t.Fatal("claim did not long-poll")
		}
		// A job enqueued during the long poll is delivered immediately.
		go func() {
			time.Sleep(200 * time.Millisecond)
			q.Enqueue(ctx, jobs.EnqueueParams{Type: "image.generate", Executor: jobs.ExecutorWorker, Scope: none, UserID: ta.Admin.ID,
				ModelID: "flux2-klein", Input: map[string]any{"prompt": "Pip the Gull steals a letter", "n": 1},
				Files: map[string]string{"ref_0": "projects/p/panel.png"}})
		}()
		var claim struct {
			Job struct {
				ID      string              `json:"id"`
				Type    string              `json:"type"`
				Attempt int                 `json:"attempt"`
				Model   struct{ ID string } `json:"model"`
				Input   map[string]any      `json:"input"`
				Files   map[string]string   `json:"files"`
			} `json:"job"`
		}
		start = time.Now()
		r := w.post("/claim", map[string]any{"worker_id": w.id, "wait_s": 10})
		if r.StatusCode != 200 {
			t.Fatalf("claim: %d %s", r.StatusCode, r.Body)
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal("long poll was not woken by the enqueue")
		}
		r.JSON(t, &claim)
		jid := claim.Job.ID
		if claim.Job.Type != "image.generate" || claim.Job.Attempt != 1 || claim.Job.Model.ID != "flux2-klein" || claim.Job.Input["prompt"] == nil {
			t.Fatalf("claimed job: %+v", claim.Job)
		}
		// the signed input URL works without auth headers
		fileURL := claim.Job.Files["ref_0"]
		if !strings.HasPrefix(fileURL, ta.URL+"/media/projects/p/panel.png?exp=") {
			t.Fatalf("file url %q", fileURL)
		}
		resp, err := http.Get(fileURL)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("download input: %v %v", resp, err)
		}
		resp.Body.Close()

		// max_concurrent=1: no second job while this one runs.
		q.Enqueue(ctx, jobs.EnqueueParams{Type: "image.generate", Executor: jobs.ExecutorWorker, Scope: none})
		if r := w.post("/claim", map[string]any{"worker_id": w.id, "wait_s": 0}); r.StatusCode != 204 {
			t.Fatalf("claimed beyond max_concurrent: %d", r.StatusCode)
		}

		// progress with preview
		var prog struct{ Cancel bool }
		w.post("/jobs/"+jid+"/progress", map[string]any{"progress": 0.62, "step": 18, "steps": 28, "message": "denoising"}).JSON(t, &prog)
		if prog.Cancel {
			t.Fatal("unexpected cancel")
		}
		if g, _ := q.Get(ctx, jid); g.Progress != 0.62 || g.Step != 18 || g.Message != "denoising" {
			t.Fatalf("progress not stored: %+v", g)
		}

		// artifact upload (multipart)
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("name", "variation-1")
		mw.WriteField("meta", `{"seed": 42}`)
		fw, _ := mw.CreateFormFile("file", "variation-1.png")
		fw.Write([]byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 100)))
		mw.Close()
		ar := w.c.Do(http.MethodPost, "/api/worker/v1/jobs/"+jid+"/artifacts", &buf, mw.FormDataContentType())
		var art struct{ Key string }
		ar.JSON(t, &art)
		if ar.StatusCode != 200 || art.Key != "jobs/"+jid+"/variation-1.png" {
			t.Fatalf("artifact: %d %s", ar.StatusCode, ar.Body)
		}
		if info, err := ta.Deps.Store.Stat(ctx, art.Key); err != nil || info.Size != 108 {
			t.Fatalf("stored artifact: %+v %v", info, err)
		}
		var assets int
		ta.Deps.DB.Get(ctx, &assets, `SELECT COUNT(*) FROM assets WHERE key = ? AND kind = 'job_artifact'`, art.Key)
		if assets != 1 {
			t.Fatal("artifact asset row missing")
		}

		// complete
		if r := w.post("/jobs/"+jid+"/complete", map[string]any{"output": map[string]any{"images": []map[string]any{{"key": art.Key, "seed": 42}}}, "actual": map[string]any{"gpu_seconds": 38.4}}); r.StatusCode != 200 {
			t.Fatalf("complete: %d %s", r.StatusCode, r.Body)
		}
		g, _ := q.Get(ctx, jid)
		if g.State != jobs.StateDone || g.Actual.GPUSeconds != 38.4 || !strings.Contains(string(g.Output), art.Key) {
			t.Fatalf("completed job: %+v", g)
		}
		// completing twice is a lease error
		if r := w.post("/jobs/"+jid+"/complete", map[string]any{"output": map[string]any{}}); r.StatusCode != 409 || r.ErrorCode() != "lease_lost" {
			t.Fatalf("double complete: %d %s", r.StatusCode, r.Body)
		}

		// second job: cancel through heartbeat, then fail with canceled
		r = w.post("/claim", map[string]any{"worker_id": w.id, "wait_s": 0})
		r.JSON(t, &claim)
		j2 := claim.Job.ID
		admin := ta.Login(t, "admin")
		if r := admin.PostJSON("/api/v1/jobs/"+j2+"/cancel", nil); r.StatusCode != 200 {
			t.Fatalf("cancel via API: %d %s", r.StatusCode, r.Body)
		}
		var hb struct{ Cancel []string }
		w.post("/heartbeat", map[string]any{"worker_id": w.id, "running": []string{j2, "01jghost0000000000000000000"}}).JSON(t, &hb)
		if len(hb.Cancel) != 2 {
			t.Fatalf("heartbeat cancel list: %v", hb.Cancel)
		}
		if r := w.post("/jobs/"+j2+"/fail", map[string]any{"error": map[string]any{"code": "canceled", "message": "stopped by user", "retryable": false}}); r.StatusCode != 200 {
			t.Fatalf("fail: %s", r.Body)
		}
		if g, _ := q.Get(ctx, j2); g.State != jobs.StateCanceled {
			t.Fatalf("canceled job: %+v", g)
		}

		// A worker that stops heartbeating goes offline and its job returns to the queue.
		j3, _ := q.Enqueue(ctx, jobs.EnqueueParams{Type: "image.inpaint", Executor: jobs.ExecutorWorker, Scope: none, MaxAttempts: 3})
		w.post("/claim", map[string]any{"worker_id": w.id, "job_types": []string{"image.inpaint"}, "wait_s": 0}).JSON(t, &claim)
		if claim.Job.ID != j3.ID {
			t.Fatalf("claimed %s want %s", claim.Job.ID, j3.ID)
		}
		clock := testutil.NewClock(time.Now().Add(2 * time.Minute))
		q.Now = clock.Now
		if n, err := q.MarkOfflineWorkers(ctx); err != nil || n != 1 {
			t.Fatalf("offline: %d %v", n, err)
		}
		if g, _ := q.Get(ctx, j3.ID); g.State != jobs.StateQueued || g.WorkerID != "" {
			t.Fatalf("job of offline worker: %+v", g)
		}
		ws, _ := q.ListWorkers(ctx)
		if len(ws) != 1 || ws[0].Status != jobs.WorkerOffline || ws[0].GPUs.V[0].VRAMTotalMB != 24564 {
			t.Fatalf("workers: %+v", ws)
		}
		// the old worker's late progress is told to stop
		w.post("/jobs/"+j3.ID+"/progress", map[string]any{"progress": 0.1}).JSON(t, &prog)
		if !prog.Cancel {
			t.Fatal("stale worker must be told to cancel")
		}

		// Another token cannot drive this worker.
		other, _, _ := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: ta.Admin.ID, Kind: auth.TokenWorker, Name: "other"})
		if r := ta.BearerClient(other).PostJSON("/api/worker/v1/heartbeat", map[string]any{"worker_id": w.id}); r.StatusCode != 404 {
			t.Fatalf("foreign heartbeat: %d", r.StatusCode)
		}

		// Admin sees workers; a creator does not.
		if r := admin.Get("/api/v1/workers"); r.StatusCode != 200 || !strings.Contains(string(r.Body), "studio-pc") {
			t.Fatalf("workers list: %d %s", r.StatusCode, r.Body)
		}
		_, creator := ta.UserClient(t, auth.RoleCreator)
		if r := creator.Get("/api/v1/workers"); r.StatusCode != 403 {
			t.Fatalf("creator listed workers: %d", r.StatusCode)
		}
		// Creators only see their own jobs.
		if r := creator.Get("/api/v1/jobs/" + jid); r.StatusCode != 404 {
			t.Fatalf("creator saw another user's job: %d", r.StatusCode)
		}
		var list struct {
			Items []jobs.Job `json:"items"`
		}
		admin.Get("/api/v1/jobs?state=done").JSON(t, &list)
		if len(list.Items) != 1 || list.Items[0].ID != jid {
			t.Fatalf("job list: %+v", list.Items)
		}
		body := admin.Get("/api/v1/jobs/" + jid + "/events").Body
		for _, kind := range []string{"created", "claimed", "progress", "artifact", "done"} {
			if !bytes.Contains(body, []byte(`"kind":"`+kind+`"`)) {
				t.Errorf("event %s missing from %s", kind, body)
			}
		}
	})
}

func TestSharedWorkerTokenRegisters(t *testing.T) {
	ta := testutil.NewTestApp(t, testutil.WithEnv("MV_WORKER_TOKEN", "pairing-secret-for-compose"))
	c := ta.BearerClient("pairing-secret-for-compose")
	r := c.PostJSON("/api/worker/v1/register", map[string]any{"name": "worker-cpu", "job_types": []string{"image.generate"}})
	if r.StatusCode != 200 {
		t.Fatalf("register with MV_WORKER_TOKEN: %d %s", r.StatusCode, r.Body)
	}
}
