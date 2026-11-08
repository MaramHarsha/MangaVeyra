package admin_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

// env is one test app on one dialect with publish fixtures and a signed-in admin.
type env struct {
	t     *testing.T
	ta    *testutil.TestApp
	f     *publishtest.Fixture
	admin *testutil.Client
}

// forEachDialect runs fn on SQLite and, when MV_TEST_POSTGRES_URL is set,
// PostgreSQL. The instance is in public mode.
func forEachDialect(t *testing.T, fn func(t *testing.T, e *env)) {
	t.Helper()
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			t.Parallel()
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect))
			e := &env{t: t, ta: ta, f: publishtest.New(t, ta)}
			e.f.SetMode(settings.ModePublic)
			e.admin = ta.Login(t, ta.Admin.Username)
			fn(t, e)
		})
	}
}

func (e *env) pub() publish.Service { return core.MustUse[publish.Service](e.ta.Deps) }

func (e *env) user(role string) (*auth.User, *testutil.Client) {
	e.t.Helper()
	u := e.f.User(role)
	return u, e.ta.Login(e.t, u.Username)
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.ta.Deps.DB.Get(context.Background(), &n, query, args...); err != nil {
		e.t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func (e *env) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.ta.Deps.DB.Exec(context.Background(), query, args...); err != nil {
		e.t.Fatalf("exec %q: %v", query, err)
	}
}

func (e *env) instance() settings.Instance {
	e.t.Helper()
	e.ta.Deps.Settings.Invalidate()
	inst, err := e.ta.Deps.Settings.Instance(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return inst
}

// report files a report the way social stores it.
func (e *env) report(reporter *auth.User, kind, targetID, reason, details string) string {
	e.t.Helper()
	id := db.NewID()
	e.exec(`INSERT INTO reports (id, reporter_id, target_kind, target_id, reason, details, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'open', ?)`, id, reporter.ID, kind, targetID, reason, details, db.Now())
	return id
}

type reportState struct {
	Status     string  `db:"status"`
	Resolution string  `db:"resolution"`
	ResolvedBy *string `db:"resolved_by"`
}

func (e *env) reportState(id string) reportState {
	e.t.Helper()
	var r reportState
	if err := e.ta.Deps.DB.Get(context.Background(), &r, `SELECT status, resolution, resolved_by FROM reports WHERE id = ?`, id); err != nil {
		e.t.Fatalf("report %s: %v", id, err)
	}
	return r
}

// audits counts audit rows for action (exact) and target id, by the admin.
func (e *env) audits(action, targetID string) int {
	e.t.Helper()
	return e.count(`SELECT COUNT(*) FROM audit_log WHERE action = ? AND target_id = ? AND actor_id = ?`, action, targetID, e.ta.Admin.ID)
}

func postForm(c *testutil.Client, path string, form url.Values) *testutil.Response {
	return c.Do(http.MethodPost, path, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
}

func wantStatus(t *testing.T, what string, r *testutil.Response, status int) {
	t.Helper()
	if r.StatusCode != status {
		t.Fatalf("%s: status %d, want %d: %s", what, r.StatusCode, status, truncate(string(r.Body)))
	}
}

// wantSaved checks a no-JS form post was applied: 303 back to the tab with ?saved=1.
func wantSaved(t *testing.T, what string, r *testutil.Response, tab string) {
	t.Helper()
	wantStatus(t, what, r, http.StatusSeeOther)
	if loc := r.Header.Get("Location"); !strings.HasPrefix(loc, tab) || !strings.Contains(loc, "saved=1") {
		t.Fatalf("%s: redirected to %q, want %s?saved=1", what, loc, tab)
	}
}

func wantBody(t *testing.T, what string, r *testutil.Response, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(string(r.Body), p) {
			t.Fatalf("%s: body lacks %q: %s", what, p, truncate(string(r.Body)))
		}
	}
}

func wantNoBody(t *testing.T, what string, r *testutil.Response, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if strings.Contains(string(r.Body), p) {
			t.Fatalf("%s: body has %q", what, p)
		}
	}
}

func truncate(s string) string {
	if len(s) > 800 {
		return s[:800] + "…"
	}
	return s
}

func resolve(c *testutil.Client, reportID, action string, extra ...string) *testutil.Response {
	form := url.Values{"action": {action}}
	for i := 0; i+1 < len(extra); i += 2 {
		form.Set(extra[i], extra[i+1])
	}
	return postForm(c, "/admin/reports/"+reportID+"/resolve", form)
}
