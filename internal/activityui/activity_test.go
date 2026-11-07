package activityui_test

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/notify"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

func each(t *testing.T, fn func(*testing.T, *testutil.TestApp)) {
	t.Helper()
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) { fn(t, testutil.NewTestApp(t, testutil.WithDialect(dialect))) })
	}
}

func form(c *testutil.Client, path string, values url.Values) *testutil.Response {
	return c.Do(http.MethodPost, path, strings.NewReader(values.Encode()), "application/x-www-form-urlencoded")
}

func job(t *testing.T, ta *testutil.TestApp, user string) *jobs.Job {
	t.Helper()
	j, err := ta.Deps.Jobs.Enqueue(context.Background(), jobs.EnqueueParams{Type: "activity.fixture", UserID: user,
		Scope: jobs.Scope{Kind: jobs.ScopeNone}, Input: map[string]string{"prompt": "PRIVATE_INPUT_MUST_NOT_RENDER"}})
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestQueueOwnershipRedactionPagingAndCancel(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		owner, client := ta.UserClient(t, auth.RoleCreator)
		other, outsider := ta.UserClient(t, auth.RoleCreator)
		mine, foreign := job(t, ta, owner.ID), job(t, ta, other.ID)
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE jobs SET message = ?, error = ? WHERE id = ?`, "PRIVATE_MESSAGE_MUST_NOT_RENDER",
			db.MustJSON(jobs.JobError{Code: "unknown-private-code", Message: "PRIVATE_ERROR_MUST_NOT_RENDER"}), mine.ID); err != nil {
			t.Fatal(err)
		}
		if err := ta.Deps.Jobs.Log(ctx, mine.ID, "provider-private-kind", "PRIVATE_EVENT_MUST_NOT_RENDER", map[string]string{"raw": "PRIVATE_DATA_MUST_NOT_RENDER"}); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/queue", "/queue?all=1", "/queue?job=" + mine.ID, "/queue/status"} {
			got := client.Get(path)
			if got.StatusCode != http.StatusOK {
				t.Fatalf("%s: %d", path, got.StatusCode)
			}
			body := string(got.Body)
			if !strings.Contains(body, mine.ID) || strings.Contains(body, foreign.ID) {
				t.Fatalf("queue ownership: %s", path)
			}
			for _, private := range []string{"PRIVATE_", "provider-private-kind", "unknown-private-code"} {
				if strings.Contains(body, private) {
					t.Fatalf("private job data rendered in %s", path)
				}
			}
			if got.Header.Get("Cache-Control") != "no-store" {
				t.Fatal("private queue was cacheable")
			}
		}
		admin := ta.Login(t, ta.Admin.Username)
		if got := admin.Get("/queue"); !strings.Contains(string(got.Body), foreign.ID) {
			t.Fatal("admin cannot inspect server queue")
		}
		if got := outsider.Get("/queue?job=" + mine.ID); got.StatusCode != http.StatusNotFound {
			t.Fatalf("foreign detail: %d", got.StatusCode)
		}
		if got := form(outsider, "/queue/jobs/"+mine.ID+"/cancel", nil); got.StatusCode != http.StatusNotFound {
			t.Fatalf("foreign cancel: %d", got.StatusCode)
		}
		if current, _ := ta.Deps.Jobs.Get(ctx, mine.ID); current.State != jobs.StateQueued {
			t.Fatal("foreign request changed job")
		}
		if got := client.Get("/queue/jobs/" + mine.ID + "/cancel"); got.StatusCode == http.StatusOK {
			t.Fatal("GET canceled a job")
		}
		if got := form(client, "/queue/jobs/"+mine.ID+"/cancel", nil); got.StatusCode != http.StatusSeeOther {
			t.Fatalf("cancel: %d", got.StatusCode)
		}
		if current, _ := ta.Deps.Jobs.Get(ctx, mine.ID); current.State != jobs.StateCanceled {
			t.Fatal("queued job not canceled")
		}
		if got := form(client, "/queue/jobs/"+mine.ID+"/cancel", nil); got.StatusCode != http.StatusSeeOther {
			t.Fatal("stale cancel did not recover")
		}
		for i := 0; i < 22; i++ {
			job(t, ta, owner.ID)
		}
		first := client.Get("/queue")
		if n := strings.Count(string(first.Body), "data-job-id="); n != 20 {
			t.Fatalf("unbounded first page: %d", n)
		}
		match := regexp.MustCompile(`href="/queue\?cursor=([a-z0-9]{26})"`).FindSubmatch(first.Body)
		if len(match) != 2 {
			t.Fatal("missing next cursor")
		}
		last := client.Get("/queue?cursor=" + string(match[1]))
		if n := strings.Count(string(last.Body), "data-job-id="); n != 3 {
			t.Fatalf("last page: %d", n)
		}
		if !strings.Contains(string(last.Body), ">Latest<") {
			t.Fatal("last page lost the latest-items navigation")
		}
		if client.Get("/queue?cursor=broken").StatusCode != http.StatusBadRequest {
			t.Fatal("invalid cursor accepted")
		}
	})
}

func TestNotificationCategoriesExplicitReadAndSafeLinks(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		owner, client := ta.UserClient(t, auth.RoleCreator)
		other, outsider := ta.UserClient(t, auth.RoleCreator)
		var ids []string
		for _, in := range []notify.Input{
			{Kind: notify.KindJobDone, Title: "Rin's panel is ready", Link: "/queue"},
			{Kind: notify.KindComment, Title: "Kaito reviewed the harbour scene"},
			{Kind: notify.KindChapter, Title: "Tidebreak Courier chapter is ready"},
			{Kind: notify.KindUpdate, Title: "Studio update available"},
			{Kind: "future_system_kind", Title: "New system notice"},
		} {
			in.UserID = owner.ID
			n, err := ta.Deps.Notify.Notify(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, n.ID)
		}
		foreign, err := ta.Deps.Notify.Notify(ctx, notify.Input{UserID: other.ID, Kind: notify.KindSystem, Title: "Other member's private notification"})
		if err != nil {
			t.Fatal(err)
		}
		for cat, count := range map[string]int{"all": 5, "studio": 2, "reading": 1, "system": 2} {
			got := client.Get("/notifications?category=" + cat)
			if got.StatusCode != http.StatusOK || strings.Count(string(got.Body), "data-notification-id=") != count {
				t.Fatalf("category %s: %d", cat, got.StatusCode)
			}
			if strings.Contains(string(got.Body), foreign.Title) {
				t.Fatal("notification ownership leak")
			}
		}
		if n, _ := ta.Deps.Notify.UnreadCount(ctx, owner.ID); n != 5 {
			t.Fatal("GET marked notifications as read")
		}
		if got := form(outsider, "/notifications/"+ids[0]+"/open", nil); got.StatusCode != http.StatusNotFound {
			t.Fatalf("foreign read: %d", got.StatusCode)
		}
		got := form(client, "/notifications/"+ids[0]+"/open", nil)
		if got.StatusCode != http.StatusSeeOther || got.Header.Get("Location") != "/queue" {
			t.Fatalf("local notification: %d %q", got.StatusCode, got.Header.Get("Location"))
		}
		if n, _ := ta.Deps.Notify.UnreadCount(ctx, owner.ID); n != 4 {
			t.Fatal("read-one did not update count")
		}
		for _, link := range []string{"https://example.invalid/private", "//example.invalid", "/%2fexample.invalid", "/%5cexample.invalid", "javascript:alert(1)", "/not-a-built-feature"} {
			n, err := ta.Deps.Notify.Notify(ctx, notify.Input{UserID: owner.ID, Kind: notify.KindSystem, Title: "Safe local notice", Link: link})
			if err != nil {
				t.Fatal(err)
			}
			got := form(client, "/notifications/"+n.ID+"/open", url.Values{"category": {"system"}})
			if got.StatusCode != http.StatusSeeOther || got.Header.Get("Location") != "/notifications?category=system" {
				t.Fatalf("unsafe or unbuilt notification link %q -> %d %q", link, got.StatusCode, got.Header.Get("Location"))
			}
		}
		if got := form(client, "/notifications/read", url.Values{"category": {"reading"}}); got.StatusCode != http.StatusSeeOther {
			t.Fatal("read all failed")
		}
		if n, _ := ta.Deps.Notify.UnreadCount(ctx, owner.ID); n != 0 {
			t.Fatal("mark all was incorrectly limited to the selected category")
		}
		if n, _ := ta.Deps.Notify.UnreadCount(ctx, other.ID); n != 1 {
			t.Fatal("read all modified another member")
		}
	})
}

func TestNotificationPagingKeepsCategoryAndOwnership(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		u, c := ta.UserClient(t, auth.RoleCreator)
		for i := 0; i < 23; i++ {
			if _, err := ta.Deps.Notify.Notify(ctx, notify.Input{UserID: u.ID, Kind: notify.KindSystem, Title: "Harbour studio notice"}); err != nil {
				t.Fatal(err)
			}
		}
		got := c.Get("/notifications?category=system")
		if n := strings.Count(string(got.Body), "data-notification-id="); n != 20 {
			t.Fatalf("first page: %d", n)
		}
		match := regexp.MustCompile(`href="/notifications\?category=system&amp;cursor=([a-z0-9]{26})"`).FindSubmatch(got.Body)
		if len(match) != 2 {
			t.Fatal("notification paging lost its category")
		}
		got = c.Get("/notifications?category=system&cursor=" + string(match[1]))
		if n := strings.Count(string(got.Body), "data-notification-id="); n != 3 {
			t.Fatalf("last page: %d", n)
		}
		if !strings.Contains(string(got.Body), ">Latest<") {
			t.Fatal("no latest navigation on last page")
		}
	})
}

func TestLegacyFailureNotificationDoesNotRenderRawWorkerText(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		u, c := ta.UserClient(t, auth.RoleCreator)
		_, err := ta.Deps.Notify.Notify(ctx, notify.Input{UserID: u.ID, Kind: notify.KindJobFailed,
			Title: "A generation job failed", Body: "PRIVATE_WORKER_PROMPT_MUST_NOT_RENDER"})
		if err != nil {
			t.Fatal(err)
		}
		got := c.Get("/notifications")
		if got.StatusCode != http.StatusOK || strings.Contains(string(got.Body), "PRIVATE_WORKER_PROMPT") ||
			!strings.Contains(string(got.Body), "Review it in Studio") {
			t.Fatal("legacy failure notification did not use safe recovery copy")
		}
	})
}

func TestRunningQueueShowsMeasuredProgressAndPendingCancellation(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		u, c := ta.UserClient(t, auth.RoleCreator)
		j := job(t, ta, u.ID)
		claimed, err := ta.Deps.Jobs.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorGo, Types: []string{j.Type}})
		if err != nil || claimed == nil || claimed.ID != j.ID {
			t.Fatalf("claim: %v", err)
		}
		if _, err := ta.Deps.Jobs.Progress(ctx, j.ID, "", jobs.ProgressUpdate{Progress: .63}); err != nil {
			t.Fatal(err)
		}
		got := c.Get("/queue/status")
		if got.StatusCode != http.StatusOK || !strings.Contains(string(got.Body), `aria-valuenow="63"`) ||
			!strings.Contains(string(got.Body), "Running") {
			t.Fatal("queue did not render measured progress")
		}
		if got := form(c, "/queue/jobs/"+j.ID+"/cancel", nil); got.StatusCode != http.StatusSeeOther {
			t.Fatal("cancel request failed")
		}
		current, err := ta.Deps.Jobs.Get(ctx, j.ID)
		if err != nil || current.State != jobs.StateRunning || !current.CancelRequested {
			t.Fatal("cancellation was reported complete before acknowledgement")
		}
		got = c.Get("/queue/status")
		if !strings.Contains(string(got.Body), "Stopping") || strings.Contains(string(got.Body), `action="/queue/jobs/`+j.ID+`/cancel"`) {
			t.Fatal("pending cancellation did not update the queue's state/actions")
		}
	})
}

func TestActivitySessionCSRFAndWorkerAuthority(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		u, c := ta.UserClient(t, auth.RoleCreator)
		raw, _, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: u.ID, Kind: auth.TokenAPI, Name: "API-only fixture", Scopes: []string{"projects:read", "projects:write", "generate"}})
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/queue", "/queue/status", "/notifications"} {
			if got := ta.NewClient().Get(path); got.StatusCode == http.StatusOK {
				t.Fatalf("anonymous %s", path)
			}
			if got := ta.BearerClient(raw).Get(path); got.StatusCode == http.StatusOK {
				t.Fatalf("bearer browser surface %s", path)
			}
		}
		_, key, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: ta.Admin.ID, Kind: auth.TokenWorker, Name: "Harbour worker fixture"})
		if err != nil {
			t.Fatal(err)
		}
		wk, err := ta.Deps.Jobs.RegisterWorker(ctx, key.ID, "127.0.0.1", jobs.Registration{Name: "Harbour worker", MaxConcurrent: 3})
		if err != nil {
			t.Fatal(err)
		}
		path := "/queue/workers/" + wk.ID + "/drain"
		if got := form(c, path, nil); got.StatusCode != http.StatusForbidden {
			t.Fatalf("creator paused worker: %d", got.StatusCode)
		}
		admin := ta.Login(t, ta.Admin.Username)
		if got := admin.Get("/queue"); !strings.Contains(string(got.Body), "3 running slots") {
			t.Fatal("queue used a sample or per-user count instead of actual worker slots")
		}
		if got := form(admin, path, nil); got.StatusCode != http.StatusSeeOther {
			t.Fatalf("admin drain: %d", got.StatusCode)
		}
		workers, _ := ta.Deps.Jobs.ListWorkers(ctx)
		if len(workers) != 1 || workers[0].Status != jobs.WorkerDraining {
			t.Fatal("drain not persisted")
		}
		if got := form(admin, "/queue/workers/"+wk.ID+"/resume", nil); got.StatusCode != http.StatusSeeOther {
			t.Fatal("admin resume failed")
		}
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE workers SET status = 'offline' WHERE id = ?`, wk.ID); err != nil {
			t.Fatal(err)
		}
		if got := form(admin, "/queue/workers/"+wk.ID+"/resume", nil); got.StatusCode != http.StatusConflict {
			t.Fatal("offline worker falsely resumed")
		}
		admin.Header.Set("Origin", "https://example.invalid")
		if got := form(admin, "/notifications/read", nil); got.StatusCode != http.StatusForbidden {
			t.Fatalf("cross-origin mutation: %d", got.StatusCode)
		}
	})
}
