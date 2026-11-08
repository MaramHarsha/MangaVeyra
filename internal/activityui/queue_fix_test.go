package activityui_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// rows returns the job ids of the queue table, in order.
func rows(body []byte) []string {
	var ids []string
	for _, m := range regexp.MustCompile(`data-job-id="([a-z0-9]+)"`).FindAllSubmatch(body, -1) {
		ids = append(ids, string(m[1]))
	}
	return ids
}

// running claims j on its executor and reports progress.
func running(t *testing.T, ta *testutil.TestApp, j *jobs.Job, progress float64) {
	t.Helper()
	ctx := context.Background()
	claimed, err := ta.Deps.Jobs.Claim(ctx, jobs.ClaimParams{Executor: j.Executor, Types: []string{j.Type}})
	if err != nil || claimed == nil || claimed.ID != j.ID {
		t.Fatalf("claim %s: %+v %v", j.ID, claimed, err)
	}
	if progress > 0 {
		if _, err := ta.Deps.Jobs.Progress(ctx, j.ID, "", jobs.ProgressUpdate{Progress: progress}); err != nil {
			t.Fatal(err)
		}
	}
}

// A character assessment runs as a Go parent plus a worker child. The queue
// shows one row: the parent, with the child's measured progress; stopping
// the parent stops the child; and Stopping ends once the job is canceled.
func TestQueueFoldsWorkerChildAndStopsWithParent(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		owner, c := ta.UserClient(t, auth.RoleCreator)
		parent, err := ta.Deps.Jobs.Enqueue(ctx, jobs.EnqueueParams{Type: "activity.parent", UserID: owner.ID, Scope: jobs.Scope{Kind: jobs.ScopeNone}, MaxAttempts: 1})
		if err != nil {
			t.Fatal(err)
		}
		running(t, ta, parent, 0)
		child, err := ta.Deps.Jobs.Enqueue(ctx, jobs.EnqueueParams{Type: "character.score", Executor: jobs.ExecutorWorker, UserID: owner.ID,
			Scope: jobs.Scope{Kind: jobs.ScopeNone}, ParentID: parent.ID, MaxAttempts: 1})
		if err != nil {
			t.Fatal(err)
		}
		running(t, ta, child, .62)
		got := c.Get("/queue/status")
		if ids := rows(got.Body); len(ids) != 1 || ids[0] != parent.ID {
			t.Fatalf("queue rows = %v, want only the parent %s", ids, parent.ID)
		}
		if !strings.Contains(string(got.Body), `aria-valuenow="62"`) || strings.Contains(string(got.Body), child.ID) {
			t.Fatal("running parent did not show its worker child's measured progress")
		}
		detail := c.Get("/queue?job=" + child.ID)
		if detail.StatusCode != http.StatusOK || !strings.Contains(string(detail.Body), `<span class="mv-meta">`+parent.ID+`</span>`) ||
			strings.Contains(string(detail.Body), child.ID) {
			t.Fatalf("worker child detail did not open its parent: %d", detail.StatusCode)
		}
		if got := form(c, "/queue/jobs/"+parent.ID+"/cancel", nil); got.StatusCode != http.StatusSeeOther {
			t.Fatalf("cancel parent: %d", got.StatusCode)
		}
		if cur, _ := ta.Deps.Jobs.Get(ctx, child.ID); !cur.CancelRequested {
			t.Fatal("stopping the parent did not stop its worker child")
		}
		got = c.Get("/queue/status")
		if !strings.Contains(string(got.Body), ">Stopping<") && !strings.Contains(string(got.Body), "Stopping") {
			t.Fatal("pending cancellation not shown")
		}
		if !regexp.MustCompile(`mv-badge--warning[^>]*>\s*Stopping`).Match(got.Body) {
			t.Fatal("Stopping does not use the warning tone")
		}
		if _, err := ta.Deps.Jobs.Fail(ctx, child.ID, "", jobs.JobError{Code: jobs.ErrCodeCanceled}, jobs.Cost{}); err != nil {
			t.Fatal(err)
		}
		if _, err := ta.Deps.Jobs.Fail(ctx, parent.ID, "", jobs.JobError{Code: jobs.ErrCodeCanceled}, jobs.Cost{}); err != nil {
			t.Fatal(err)
		}
		got = c.Get("/queue/status")
		if strings.Contains(string(got.Body), "Stopping") || !strings.Contains(string(got.Body), "Canceled") {
			t.Fatal("a canceled job still reads Stopping")
		}
		ja := ta.Login(t, owner.Username)
		ja.Header.Set("Accept-Language", "ja")
		if body := string(ja.Get("/queue/status").Body); strings.Contains(body, "停止中") || !strings.Contains(body, "キャンセル済み") {
			t.Fatal("a canceled job still reads 停止中")
		}
	})
}

func registerWorker(t *testing.T, ta *testutil.TestApp, name string, slots int) *jobs.Worker {
	t.Helper()
	ctx := context.Background()
	_, key, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: ta.Admin.ID, Kind: auth.TokenWorker, Name: name + " token"})
	if err != nil {
		t.Fatal(err)
	}
	wk, err := ta.Deps.Jobs.RegisterWorker(ctx, key.ID, "127.0.0.1", jobs.Registration{Name: name, MaxConcurrent: slots})
	if err != nil {
		t.Fatal(err)
	}
	return wk
}

// The worker button names the action it performs, with its icon, and a
// paused worker keeps the worker subtitle; counts use singular/plural forms.
func TestQueueWorkerButtonMatchesItsAction(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		if _, err := ta.Deps.Settings.UpdateInstance(ctx, "", func(in *settings.Instance) error {
			in.Limits.RunningGPUJobs, in.Limits.RunningJobs = 1, 1
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		gone := registerWorker(t, ta, "Harbour attic", 2)
		desk := registerWorker(t, ta, "Harbour desk", 1)
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE workers SET status = 'offline' WHERE id = ?`, gone.ID); err != nil {
			t.Fatal(err)
		}
		admin := ta.Login(t, ta.Admin.Username)
		body := string(admin.Get("/queue").Body)
		if !strings.Contains(body, "1 running slot · fair share on") || !strings.Contains(body, "1 GPU job · 1 server job") {
			t.Fatal("singular counts not used")
		}
		if !strings.Contains(body, "Pause worker") || !strings.Contains(body, "ti-player-pause") || strings.Contains(body, "Resume worker") {
			t.Fatal("online worker does not offer Pause worker")
		}
		if got := form(admin, "/queue/workers/"+desk.ID+"/drain", nil); got.StatusCode != http.StatusSeeOther {
			t.Fatalf("drain: %d", got.StatusCode)
		}
		body = string(admin.Get("/queue").Body)
		if !strings.Contains(body, "Resume worker") || !strings.Contains(body, "ti-player-play") || strings.Contains(body, ">Pause worker") ||
			!strings.Contains(body, `action="/queue/workers/`+desk.ID+`/resume"`) {
			t.Fatal("paused worker (with an offline one registered) does not offer Resume worker")
		}
		if strings.Contains(body, "Server &amp; remote jobs") || !strings.Contains(body, "1 running slot paused") {
			t.Fatal("paused worker dropped the worker subtitle")
		}
		ja := ta.Login(t, ta.Admin.Username)
		ja.Header.Set("Accept-Language", "ja")
		if body := string(ja.Get("/queue").Body); !strings.Contains(body, "ワーカーを再開") || strings.Contains(body, "サーバー・リモートジョブ") {
			t.Fatal("Japanese worker controls do not match the paused worker")
		}
		registerWorker(t, ta, "Harbour loft", 3)
		body = string(admin.Get("/queue").Body)
		if !strings.Contains(body, "3 running slots · fair share on") || !strings.Contains(body, "Pause worker") {
			t.Fatal("plural slots or mixed worker label wrong")
		}
	})
}

type projectFixture struct {
	project, chapter, page, panel, layer, character string
}

func seedStudio(t *testing.T, ta *testutil.TestApp, owner string) projectFixture {
	t.Helper()
	ctx := context.Background()
	f := projectFixture{project: db.NewID(), chapter: db.NewID(), page: db.NewID(), panel: db.NewID(), layer: db.NewID(), character: db.NewID()}
	now := db.Now()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO projects (id, owner_id, title, slug) VALUES (?, ?, 'Tidebreak Courier', ?)`, []any{f.project, owner, "tidebreak-" + f.project}},
		{`INSERT INTO project_members (project_id, user_id, role) VALUES (?, ?, 'owner')`, []any{f.project, owner}},
		{`INSERT INTO characters (id, project_id, name) VALUES (?, ?, 'Rin Asagiri')`, []any{f.character, f.project}},
		{`INSERT INTO chapters (id, project_id, number, title, created_at, updated_at) VALUES (?, ?, 1, 'Harbour', ?, ?)`, []any{f.chapter, f.project, now, now}},
		{`INSERT INTO pages (id, project_id, chapter_id, number, created_at, updated_at) VALUES (?, ?, ?, 3, ?, ?)`, []any{f.page, f.project, f.chapter, now, now}},
		{`INSERT INTO panels (id, project_id, page_id, order_idx, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)`, []any{f.panel, f.project, f.page, now, now}},
		{`INSERT INTO layers (id, panel_id, kind) VALUES (?, ?, 'character')`, []any{f.layer, f.panel}},
	} {
		if _, err := ta.Deps.DB.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// finishedJob stores a finished job of a feature type directly; the queue
// screen only reads it.
func finishedJob(t *testing.T, ta *testutil.TestApp, user, typ string, scope jobs.Scope, input any, model, provider string, actual jobs.Cost) *jobs.Job {
	t.Helper()
	ctx := context.Background()
	j := job(t, ta, user)
	now := db.Now()
	if _, err := ta.Deps.DB.Exec(ctx, `UPDATE jobs SET type = ?, scope = ?, scope_kind = ?, project_id = ?, layer_id = ?, input = ?, model_id = ?, provider_id = ?,
		state = 'done', actual = ?, started_at = ?, finished_at = ? WHERE id = ?`, typ, db.MustJSON(scope), scope.Kind, db.NullString(scope.ProjectID),
		db.NullString(scope.LayerID), db.MustJSON(input), model, provider, db.MustJSON(actual), now.Add(-38*time.Second), now, j.ID); err != nil {
		t.Fatal(err)
	}
	current, err := ta.Deps.Jobs.Get(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	return current
}

// Rows name routes with display names, spend has two decimals, region edits
// carry their panel number, assessment jobs link to the character page and
// the selected-job box is padded.
func TestQueueRoutesSpendPanelsAndStudioLinks(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		owner, c := ta.UserClient(t, auth.RoleCreator)
		f := seedStudio(t, ta, owner.ID)
		score := finishedJob(t, ta, owner.ID, "character.lock_score", jobs.Scope{Kind: jobs.ScopeProject, ProjectID: f.project},
			map[string]string{"character_id": f.character}, "character-lock-dinov2-v1", "local-worker", jobs.Cost{USD: 4.18})
		region := finishedJob(t, ta, owner.ID, "region.edit", jobs.Scope{Kind: jobs.ScopeRegion, ProjectID: f.project, LayerID: f.layer, Region: json.RawMessage(`{"kind":"box"}`)},
			map[string]string{}, "", "", jobs.Cost{USD: 0.000123})
		body := string(c.Get("/queue").Body)
		for _, want := range []string{"Character assessment · Rin Asagiri", "DINOv2 · local", "$4.18", "&lt;$0.01", "Panel 3.2 · Region edit",
			`href="/studio/p/` + f.project + `/characters/` + f.character + `"`, "$4.18 remote"} {
			if !strings.Contains(body, want) {
				t.Fatalf("queue lacks %q", want)
			}
		}
		for _, raw := range []string{"character-lock-dinov2-v1", "0.000", "/studio/p/" + f.project + "/story"} {
			if strings.Contains(body, raw) {
				t.Fatalf("queue shows %q", raw)
			}
		}
		detail := string(c.Get("/queue?job=" + score.ID).Body)
		if !strings.Contains(detail, `class="mv-box mv-box--pad mv-queue__detail"`) {
			t.Fatal("selected-job box is not padded")
		}
		if !strings.Contains(string(c.Get("/queue?job="+region.ID).Body), "/chapters/"+f.chapter+"/edit?job="+region.ID) {
			t.Fatal("region edit does not review in the editor")
		}
	})
}

// Admins see their own jobs unless they explicitly ask for every member's
// (URL-restorable), and every queue link keeps the page and filter.
func TestQueueAdminFilterAndCursorLinks(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		member, _ := ta.UserClient(t, auth.RoleCreator)
		var mine []*jobs.Job
		for i := 0; i < 22; i++ {
			mine = append(mine, job(t, ta, ta.Admin.ID))
		}
		foreign := finishedJob(t, ta, member.ID, "activity.fixture", jobs.Scope{Kind: jobs.ScopeNone}, map[string]string{}, "", "", jobs.Cost{USD: 2})
		admin := ta.Login(t, ta.Admin.Username)
		body := admin.Get("/queue").Body
		if strings.Contains(string(body), foreign.ID) || strings.Contains(string(body), "$2.00 remote") {
			t.Fatal("admin default view includes another member's jobs or spend")
		}
		if !strings.Contains(string(body), `aria-current="true">My jobs`) || !strings.Contains(string(body), `href="/queue?all=1"`) {
			t.Fatal("admin has no explicit All users filter")
		}
		match := regexp.MustCompile(`href="/queue\?cursor=([a-z0-9]{26})"`).FindSubmatch(body)
		if len(match) != 2 {
			t.Fatal("missing next cursor")
		}
		cursor := string(match[1])
		page := string(admin.Get("/queue?cursor=" + cursor + "&job=" + mine[0].ID).Body)
		if !strings.Contains(page, `href="/queue?cursor=`+cursor+`&amp;job=`+mine[1].ID+`"`) {
			t.Fatal("job links on an older page dropped the cursor")
		}
		if !strings.Contains(page, `data-job-id="`+mine[0].ID+`"`) || !strings.Contains(page, `<span class="mv-meta">`+mine[0].ID+`</span>`) {
			t.Fatal("selected row disappeared from its page")
		}
		if !strings.Contains(page, `href="/queue?cursor=`+cursor+`"`) {
			t.Fatal("closing the detail dropped the cursor")
		}
		got := form(admin, "/queue/jobs/"+mine[0].ID+"/cancel?cursor="+cursor, nil)
		if got.StatusCode != http.StatusSeeOther || got.Header.Get("Location") != "/queue?cursor="+cursor+"&job="+mine[0].ID {
			t.Fatalf("cancel lost the queue position: %d %q", got.StatusCode, got.Header.Get("Location"))
		}
		all := admin.Get("/queue?all=1").Body
		if !strings.Contains(string(all), foreign.ID) || !strings.Contains(string(all), "$2.00 remote") || !strings.Contains(string(all), `data-queue-admin="true"`) {
			t.Fatal("All users does not show every member's jobs and spend")
		}
		next := regexp.MustCompile(`href="/queue\?all=1&amp;cursor=([a-z0-9]{26})"`).FindSubmatch(all)
		if len(next) != 2 {
			t.Fatal("paging dropped the All users filter")
		}
		older := string(admin.Get("/queue?all=1&cursor=" + string(next[1])).Body)
		if !strings.Contains(older, `href="/queue?all=1&amp;cursor=`+string(next[1])+`&amp;job=`) || !strings.Contains(older, `href="/queue?all=1"`) {
			t.Fatal("older All users page lost the filter in its links")
		}
		if got := form(admin, "/queue/jobs/"+foreign.ID+"/cancel?"+url.Values{"all": {"1"}}.Encode(), nil); got.Header.Get("Location") != "/queue?all=1&job="+foreign.ID {
			t.Fatalf("cancel lost the All users filter: %q", got.Header.Get("Location"))
		}
	})
}
