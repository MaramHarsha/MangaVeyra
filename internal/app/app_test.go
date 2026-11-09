package app_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/MaramHarsha/MangaVeyra/internal/app"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/config"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

func TestHealthReadyVersionAndModules(t *testing.T) {
	for _, d := range testdb.Dialects() {
		t.Run(d, func(t *testing.T) {
			ta := testutil.NewTestApp(t, testutil.WithDialect(d))
			if ta.JobAPI.Manifests == nil {
				t.Fatal("worker claims have no installed model manifest lookup")
			}
			c := ta.NewClient()
			if r := c.Get("/healthz"); r.StatusCode != 200 || !strings.Contains(string(r.Body), `"status":"ok"`) {
				t.Fatalf("healthz: %d %s", r.StatusCode, r.Body)
			}
			r := c.Get("/readyz")
			m := r.Map(t)
			if r.StatusCode != 200 || m["ready"] != true {
				t.Fatalf("readyz: %d %s", r.StatusCode, r.Body)
			}
			checks := m["checks"].(map[string]any)
			if checks["database"].(map[string]any)["detail"] != d || checks["storage"].(map[string]any)["ok"] != true {
				t.Fatalf("checks: %v", checks)
			}
			if r := c.Get("/api/v1/version"); !strings.Contains(string(r.Body), `"version":"1.0.0"`) {
				t.Fatalf("version: %s", r.Body)
			}
			names := map[string]bool{}
			for _, mod := range ta.Modules {
				if names[mod.Name()] {
					t.Fatalf("module %s listed twice", mod.Name())
				}
				names[mod.Name()] = true
			}
			for _, want := range []string{"health", "media", "auth", "jobs", "ws", "notify", "setup", "account", "studio", "editor",
				"lettering", "compose", "ai", "gen", "translate", "animate", "publish", "reader", "social", "discover", "analytics",
				"xp", "export", "importer", "backup", "update", "admin", "mcp", "world", "packs", "plugins", "seed"} {
				if !names[want] {
					t.Errorf("module %s missing from the app", want)
				}
			}
		})
	}
}

func TestReadinessTracksS3BucketAvailability(t *testing.T) {
	endpoint := os.Getenv("MV_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("MV_TEST_S3_ENDPOINT not set")
	}
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			ctx := context.Background()
			access, secret := os.Getenv("MV_TEST_S3_ACCESS_KEY"), os.Getenv("MV_TEST_S3_SECRET_KEY")
			bucket := "mvtest-readyz-" + db.NewID()
			admin, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(access, secret, ""), Region: "us-east-1", BucketLookup: minio.BucketLookupPath})
			if err != nil {
				t.Fatal(err)
			}
			if err := admin.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: "us-east-1"}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = admin.RemoveBucket(context.Background(), bucket) })
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect), testutil.WithEnv(
				"MV_STORAGE", "s3", "MV_S3_ENDPOINT", endpoint, "MV_S3_REGION", "us-east-1", "MV_S3_BUCKET", bucket,
				"MV_S3_ACCESS_KEY", access, "MV_S3_SECRET_KEY", secret, "MV_S3_USE_SSL", "false", "MV_S3_PATH_STYLE", "true"))
			browser := ta.NewClient()
			r := browser.Get("/readyz")
			body := r.Map(t)
			if r.StatusCode != http.StatusOK || body["ready"] != true || body["checks"].(map[string]any)["storage"].(map[string]any)["detail"] != "s3" {
				t.Fatalf("existing bucket: %d %s", r.StatusCode, r.Body)
			}
			if err := admin.RemoveBucket(ctx, bucket); err != nil {
				t.Fatal(err)
			}
			r = browser.Get("/readyz")
			body = r.Map(t)
			if r.StatusCode != http.StatusServiceUnavailable || body["ready"] != false || body["checks"].(map[string]any)["storage"].(map[string]any)["ok"] != false {
				t.Fatalf("missing bucket still ready: %d %s", r.StatusCode, r.Body)
			}
			if r := browser.Get("/healthz"); r.StatusCode != http.StatusOK {
				t.Fatalf("storage failure affected liveness: %d", r.StatusCode)
			}
			if err := admin.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: "us-east-1"}); err != nil {
				t.Fatal(err)
			}
			if r := browser.Get("/readyz"); r.StatusCode != http.StatusOK || r.Map(t)["ready"] != true {
				t.Fatalf("recreated bucket not ready: %d %s", r.StatusCode, r.Body)
			}
		})
	}
}

func TestSignedMediaThroughApp(t *testing.T) {
	ta := testutil.NewTestApp(t)
	ctx := context.Background()
	if err := storage.PutBytes(ctx, ta.Deps.Store, "covers/tidebreak.png", []byte("\x89PNG\r\n\x1a\nimage"), "image/png"); err != nil {
		t.Fatal(err)
	}
	c := ta.NewClient()
	u := ta.Deps.Media.URL("covers/tidebreak.png", time.Minute)
	r := c.Get(u)
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/png" || !strings.Contains(r.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("media: %d %v", r.StatusCode, r.Header)
	}
	if r := c.Get("/media/covers/tidebreak.png"); r.StatusCode != 403 {
		t.Fatalf("unsigned media: %d", r.StatusCode)
	}
}

func TestJobFailureNotifiesOwner(t *testing.T) {
	ta := testutil.NewTestApp(t)
	ctx := context.Background()
	u := ta.CreateUser(t, "kaito", auth.RoleCreator)
	q := ta.Deps.Jobs
	j, _ := q.Enqueue(ctx, jobs.EnqueueParams{Type: "t", UserID: u.ID, Scope: jobs.Scope{Kind: jobs.ScopeNone}, MaxAttempts: 1})
	q.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorGo, Types: []string{"t"}})
	q.Fail(ctx, j.ID, "", jobs.JobError{Code: "oom", Message: "PRIVATE_WORKER_PROMPT_MUST_NOT_LEAVE_JOB"}, jobs.Cost{})
	list, err := ta.Deps.Notify.List(ctx, u.ID, notifyListAll())
	if err != nil || len(list) != 1 || list[0].Kind != "job_failed" || !strings.HasPrefix(list[0].Body, "Job") ||
		strings.Contains(list[0].Body, "PRIVATE") || list[0].Link != "/queue?job="+j.ID {
		t.Fatalf("notifications: %+v %v", list, err)
	}
}

func TestJobNotificationUsesRecipientLocale(t *testing.T) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect))
			ctx := context.Background()
			u := ta.CreateUser(t, "rin-notifications", auth.RoleCreator)
			if _, err := ta.Deps.DB.Exec(ctx, `UPDATE users SET locale = 'ja' WHERE id = ?`, u.ID); err != nil {
				t.Fatal(err)
			}
			q := ta.Deps.Jobs
			j, err := q.Enqueue(ctx, jobs.EnqueueParams{Type: "notification.fixture", UserID: u.ID, Scope: jobs.Scope{Kind: jobs.ScopeNone}, MaxAttempts: 1})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := q.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorGo, Types: []string{j.Type}}); err != nil {
				t.Fatal(err)
			}
			if _, err := q.Fail(ctx, j.ID, "", jobs.JobError{Code: "oom", Message: "private worker detail"}, jobs.Cost{}); err != nil {
				t.Fatal(err)
			}
			notices, err := ta.Deps.Notify.List(ctx, u.ID, notifyListAll())
			if err != nil || len(notices) != 1 || notices[0].Title != "生成に失敗しました" || !strings.HasPrefix(notices[0].Body, "ジョブ") ||
				strings.Contains(notices[0].Body, j.Type) || strings.Contains(notices[0].Body, "private") {
				t.Fatalf("recipient locale or safe body missing: %+v %v", notices, err)
			}
		})
	}
}

func TestJobNotificationFallsBackToInstanceLocale(t *testing.T) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect))
			ctx := context.Background()
			if _, err := ta.Deps.Settings.UpdateInstance(ctx, "", func(in *settings.Instance) error {
				in.DefaultLocale = "ja"
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			u := ta.CreateUser(t, "sora-default-locale", auth.RoleCreator) // users.locale stays ''
			q := ta.Deps.Jobs
			j, err := q.Enqueue(ctx, jobs.EnqueueParams{Type: "notification.fixture", UserID: u.ID, Scope: jobs.Scope{Kind: jobs.ScopeNone}, MaxAttempts: 1})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := q.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorGo, Types: []string{j.Type}}); err != nil {
				t.Fatal(err)
			}
			if _, err := q.Fail(ctx, j.ID, "", jobs.JobError{Code: "oom", Message: "private worker detail"}, jobs.Cost{}); err != nil {
				t.Fatal(err)
			}
			notices, err := ta.Deps.Notify.List(ctx, u.ID, notifyListAll())
			if err != nil || len(notices) != 1 || notices[0].Title != "生成に失敗しました" {
				t.Fatalf("instance default locale not used for a user without a language: %+v %v", notices, err)
			}
		})
	}
}

func TestServeAndGracefulShutdown(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	dir := t.TempDir()
	env := map[string]string{"MV_DATA_DIR": dir, "MV_ADDR": addr, "MV_LOG_LEVEL": "error", "MV_SHUTDOWN_TIMEOUT": "5s"}
	cfg, err := config.Load("", func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	params := auth.FastHashParams
	a, err := app.New(context.Background(), cfg, app.Options{HashParams: &params, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	var resp *http.Response
	for i := 0; i < 50; i++ {
		resp, err = http.Get("http://" + addr + "/healthz")
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("server not up: %v", err)
	}
	resp.Body.Close()
	// the secret key and SQLite database were created in the data dir
	if _, err := storage.NewLocal(dir); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not shut down")
	}
}

// A release candidate under the update module's loopback health check only
// answers requests; it starts no job executor, schedules or loops.
func TestUpdateCandidateStartsNoBackgroundLoops(t *testing.T) {
	t.Setenv("MV_UPDATE_CANDIDATE", "1")
	env := map[string]string{"MV_DATA_DIR": t.TempDir(), "MV_LOG_LEVEL": "info"}
	cfg, err := config.Load("", func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	params := auth.FastHashParams
	a, err := app.New(context.Background(), cfg, app.Options{HashParams: &params, LogOutput: &logs})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)
	if !strings.Contains(logs.String(), "update candidate: background loops are off") {
		t.Fatalf("candidate started its loops: %s", logs.String())
	}
}
