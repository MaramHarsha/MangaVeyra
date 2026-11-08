package social_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/social"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

// env is one test app on one dialect with publish fixtures.
type env struct {
	t  *testing.T
	ta *testutil.TestApp
	f  *publishtest.Fixture
}

// forEachDialect runs fn against a fresh app on SQLite and, when
// MV_TEST_POSTGRES_URL is set, PostgreSQL. The instance is in public mode.
func forEachDialect(t *testing.T, fn func(t *testing.T, e *env)) {
	t.Helper()
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			t.Parallel()
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect))
			e := &env{t: t, ta: ta, f: publishtest.New(t, ta)}
			e.f.SetMode(settings.ModePublic)
			fn(t, e)
		})
	}
}

func (e *env) follows() social.Follows { return core.MustUse[social.Follows](e.ta.Deps) }

// user creates an account and returns it with a signed-in client.
func (e *env) user(role string) (*auth.User, *testutil.Client) {
	e.t.Helper()
	u := e.f.User(role)
	return u, e.ta.Login(e.t, u.Username)
}

func (e *env) login(u *auth.User) *testutil.Client {
	e.t.Helper()
	return e.ta.Login(e.t, u.Username)
}

func (e *env) followerCount(seriesID string) int {
	e.t.Helper()
	var n int
	if err := e.ta.Deps.DB.Get(context.Background(), &n, `SELECT follower_count FROM series WHERE id = ?`, seriesID); err != nil {
		e.t.Fatalf("follower_count: %v", err)
	}
	return n
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

func (e *env) confirmAge(userID, seriesID string) {
	e.exec(`INSERT INTO age_confirmations (user_id, series_id, confirmed_at) VALUES (?, ?, ?)`, userID, seriesID, db.Now())
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

func truncate(s string) string {
	if len(s) > 600 {
		return s[:600] + "…"
	}
	return s
}

// commentsURL is the list endpoint for a target.
func commentsURL(seriesID, kind, targetID string, extra ...string) string {
	q := url.Values{"series": {seriesID}, "target_kind": {kind}, "target_id": {targetID}}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	return "/api/v1/social/comments?" + q.Encode()
}

type commentJSON struct {
	ID         string `json:"id"`
	SeriesID   string `json:"series_id"`
	TargetKind string `json:"target_kind"`
	TargetID   string `json:"target_id"`
	Anchor     struct {
		Page  int `json:"page"`
		Panel int `json:"panel"`
	} `json:"anchor"`
	ParentID string `json:"parent_id"`
	Body     string `json:"body"`
	Author   struct {
		ID          string `json:"id"`
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
	} `json:"author"`
	EditedAt  *string `json:"edited_at"`
	Hidden    bool    `json:"hidden"`
	CanEdit   bool    `json:"can_edit"`
	CanDelete bool    `json:"can_delete"`
	CanHide   bool    `json:"can_hide"`
	CanReport bool    `json:"can_report"`
}

type pageJSON struct {
	Items      []commentJSON `json:"items"`
	NextCursor string        `json:"next_cursor"`
}

func listComments(t *testing.T, c *testutil.Client, path string) pageJSON {
	t.Helper()
	r := c.Get(path)
	wantStatus(t, "list "+path, r, http.StatusOK)
	var p pageJSON
	r.JSON(t, &p)
	return p
}

func postComment(t *testing.T, c *testutil.Client, body map[string]any, status int) commentJSON {
	t.Helper()
	r := c.PostJSON("/api/v1/social/comments", body)
	wantStatus(t, "post comment", r, status)
	var out commentJSON
	if status == http.StatusCreated {
		r.JSON(t, &out)
	}
	return out
}
