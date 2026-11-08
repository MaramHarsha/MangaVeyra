package jobs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

type payloadRecorder struct {
	mu   sync.Mutex
	data []any
}

func (r *payloadRecorder) Publish(_, _ string, data any) {
	r.mu.Lock()
	r.data = append(r.data, data)
	r.mu.Unlock()
}

// Raw worker/provider text (failure messages, progress text, event messages
// and data) must never leave the server through the job API or the realtime
// summaries, whoever asks: the owner, an admin or a bearer token.
func TestJobAPIAndRealtimeNeverExposeRawWorkerText(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		q := ta.Deps.Jobs
		rec := &payloadRecorder{}
		q.SetPublisher(rec)
		owner := ta.CreateUser(t, "raw-text-owner", auth.RoleCreator)
		const secret = "sk-live-RAWSECRET"
		failed, err := q.Enqueue(ctx, jobs.EnqueueParams{Type: "public.fixture", UserID: owner.ID, Scope: none, MaxAttempts: 1})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := q.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorGo, Types: []string{failed.Type}}); err != nil {
			t.Fatal(err)
		}
		if _, err := q.Progress(ctx, failed.ID, "", jobs.ProgressUpdate{Progress: .4, Message: "progress " + secret}); err != nil {
			t.Fatal(err)
		}
		if err := q.Log(ctx, failed.ID, "provider-private-kind", "log "+secret, map[string]string{"raw": secret}); err != nil {
			t.Fatal(err)
		}
		if _, err := q.Fail(ctx, failed.ID, "", jobs.JobError{Code: "provider_error " + secret, Message: "upstream said: invalid key " + secret}, jobs.Cost{}); err != nil {
			t.Fatal(err)
		}
		timedOut, err := q.Enqueue(ctx, jobs.EnqueueParams{Type: "public.fixture", UserID: owner.ID, Scope: none, MaxAttempts: 1})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := q.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorGo, Types: []string{timedOut.Type}}); err != nil {
			t.Fatal(err)
		}
		if _, err := q.Fail(ctx, timedOut.ID, "", jobs.JobError{Code: jobs.ErrCodeTimeout, Message: "worker log " + secret}, jobs.Cost{}); err != nil {
			t.Fatal(err)
		}
		running, err := q.Enqueue(ctx, jobs.EnqueueParams{Type: "public.running", UserID: owner.ID, Scope: none})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := q.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorGo, Types: []string{running.Type}}); err != nil {
			t.Fatal(err)
		}
		if _, err := q.Progress(ctx, running.ID, "", jobs.ProgressUpdate{Progress: .62, Message: "denoising " + secret}); err != nil {
			t.Fatal(err)
		}

		raw, _, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: owner.ID, Kind: auth.TokenAPI, Name: "raw text reader", Scopes: []string{"projects:read"}})
		if err != nil {
			t.Fatal(err)
		}
		clients := map[string]*testutil.Client{"owner": ta.Login(t, owner.Username), "admin": ta.Login(t, ta.Admin.Username), "bearer": ta.BearerClient(raw)}
		for name, c := range clients {
			paths := []string{"/api/v1/jobs/" + failed.ID, "/api/v1/jobs/" + failed.ID + "/events", "/api/v1/jobs/" + timedOut.ID, "/api/v1/jobs/" + running.ID}
			if name == "admin" {
				paths = append(paths, "/api/v1/jobs?all=1")
			} else {
				paths = append(paths, "/api/v1/jobs")
			}
			for _, path := range paths {
				got := c.Get(path)
				if got.StatusCode != http.StatusOK {
					t.Fatalf("%s %s: %d %s", name, path, got.StatusCode, got.Body)
				}
				for _, private := range [][]byte{[]byte(secret), []byte("upstream said"), []byte("denoising"), []byte("provider-private-kind"), []byte("provider_error")} {
					if bytes.Contains(got.Body, private) {
						t.Fatalf("%s %s exposed %q: %s", name, path, private, got.Body)
					}
				}
			}
			var one struct {
				Job jobs.Job `json:"job"`
			}
			c.Get("/api/v1/jobs/"+failed.ID).JSON(t, &one)
			if one.Job.Error == nil || one.Job.Error.Code != jobs.ErrCodeInternal || one.Job.Error.Message != "This job could not finish. Review it in Studio before trying again." {
				t.Fatalf("%s: unknown failure was not mapped to safe copy: %+v", name, one.Job.Error)
			}
			c.Get("/api/v1/jobs/"+timedOut.ID).JSON(t, &one)
			if one.Job.Error == nil || one.Job.Error.Code != jobs.ErrCodeTimeout || one.Job.Error.Message != "The job timed out. Review it in Studio before trying again." {
				t.Fatalf("%s: public failure code lost its safe copy: %+v", name, one.Job.Error)
			}
			c.Get("/api/v1/jobs/"+running.ID).JSON(t, &one)
			if one.Job.Progress != .62 || one.Job.Message != "" {
				t.Fatalf("%s: running job lost progress or kept progress text: %+v", name, one.Job)
			}
			var events struct {
				Items []jobs.Event `json:"items"`
			}
			c.Get("/api/v1/jobs/"+failed.ID+"/events").JSON(t, &events)
			kinds := map[string]bool{}
			for _, ev := range events.Items {
				kinds[ev.Kind] = true
				if ev.Message != "" || ev.Data != nil {
					t.Fatalf("%s: event kept its message or data: %+v", name, ev)
				}
			}
			if !kinds["created"] || !kinds["claimed"] || !kinds["progress"] || !kinds["failed"] || !kinds["update"] {
				t.Fatalf("%s: event kinds = %v", name, kinds)
			}
		}
		ja := ta.Login(t, owner.Username)
		ja.Header.Set("Accept-Language", "ja")
		var one struct {
			Job jobs.Job `json:"job"`
		}
		ja.Get("/api/v1/jobs/"+timedOut.ID).JSON(t, &one)
		if one.Job.Error == nil || one.Job.Error.Message != "ジョブがタイムアウトしました。再試行の前にスタジオで確認してください。" {
			t.Fatalf("safe copy not localized: %+v", one.Job.Error)
		}

		rec.mu.Lock()
		defer rec.mu.Unlock()
		if len(rec.data) == 0 {
			t.Fatal("no realtime summaries recorded")
		}
		for _, d := range rec.data {
			b, err := json.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(b, []byte(secret)) || bytes.Contains(b, []byte("denoising")) || bytes.Contains(b, []byte("upstream said")) {
				t.Fatalf("realtime summary exposed raw worker text: %s", b)
			}
		}
	})
}
