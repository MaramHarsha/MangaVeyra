package reader_test

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/analytics"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/social"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

// env is one test app with publish fixtures on one dialect.
type env struct {
	t  *testing.T
	ta *testutil.TestApp
	f  *publishtest.Fixture
}

// each runs fn on SQLite and, with MV_TEST_POSTGRES_URL, PostgreSQL.
func each(t *testing.T, fn func(t *testing.T, e *env)) {
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

// member creates a reader account and signs it in.
func (e *env) member() (*auth.User, *testutil.Client) {
	e.t.Helper()
	u := e.f.User(auth.RoleReader)
	return u, e.ta.Login(e.t, u.Username)
}

func chapterPath(s publishtest.Series, p publishtest.Publication) string {
	return "/s/" + s.Slug + "/" + strconv.FormatFloat(p.Number, 'f', -1, 64)
}

func form(c *testutil.Client, path string, values url.Values) *testutil.Response {
	return c.Do(http.MethodPost, path, strings.NewReader(values.Encode()), "application/x-www-form-urlencoded")
}

func wantStatus(t *testing.T, what string, got *testutil.Response, want int) {
	t.Helper()
	if got.StatusCode != want {
		t.Fatalf("%s: status %d, want %d: %.300s", what, got.StatusCode, want, got.Body)
	}
}

// fakeFollows is a social.Follows with fixed follows.
type fakeFollows struct {
	mu   sync.Mutex
	byID map[string][]string
}

func (f *fakeFollows) IsFollowing(_ context.Context, userID, seriesID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range f.byID[userID] {
		if id == seriesID {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeFollows) FollowedSeriesIDs(_ context.Context, userID, _ string, _ int) ([]string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.byID[userID]...), "", nil
}

func (f *fakeFollows) Followers(context.Context, string, string, int) ([]string, string, error) {
	return nil, "", nil
}

// provideFollows registers a fake social.Follows unless the social module
// already provides a real one (then the test is skipped: it checks the
// reader against the fake's fixed answers).
func (e *env) provideFollows() *fakeFollows {
	e.t.Helper()
	if _, ok := core.Use[social.Follows](e.ta.Deps); ok {
		e.t.Skip("social.Follows is provided by the social module")
	}
	f := &fakeFollows{byID: map[string][]string{}}
	core.Provide[social.Follows](e.ta.Deps, f)
	return f
}

// fakeRecorder is an analytics.Recorder that keeps events in memory and
// checks access through publish.Service like the real one must.
type fakeRecorder struct {
	mu     sync.Mutex
	pub    publish.Service
	events []recorded
}

type recorded struct {
	viewer  publish.Viewer
	session string
	event   analytics.ReadEvent
}

func (f *fakeRecorder) Record(ctx context.Context, v publish.Viewer, sessionID string, e analytics.ReadEvent) error {
	s, err := f.pub.SeriesByID(ctx, v, e.SeriesID)
	if err != nil || !f.pub.CanRead(ctx, v, s, publish.Publication{ID: e.PublicationID, SeriesID: e.SeriesID}) {
		return publish.ErrNotFound
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, recorded{viewer: v, session: sessionID, event: e})
	return nil
}

func (f *fakeRecorder) kinds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.events))
	for i, e := range f.events {
		out[i] = e.event.Kind
	}
	return out
}

func (f *fakeRecorder) wait(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		got := len(f.events)
		f.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("recorder got %v, want %d events", f.kinds(), n)
}

func (e *env) provideRecorder() *fakeRecorder {
	e.t.Helper()
	if _, ok := core.Use[analytics.Recorder](e.ta.Deps); ok {
		e.t.Skip("analytics.Recorder is provided by the analytics module")
	}
	r := &fakeRecorder{pub: e.f.Service()}
	core.Provide[analytics.Recorder](e.ta.Deps, r)
	return r
}
