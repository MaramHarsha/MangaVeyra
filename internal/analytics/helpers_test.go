package analytics_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/analytics"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

// forEachDialect runs fn against a fresh app on SQLite and, when
// MV_TEST_POSTGRES_URL is set, PostgreSQL. The instance is in public mode.
func forEachDialect(t *testing.T, fn func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture)) {
	t.Helper()
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			t.Parallel()
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect))
			f := publishtest.New(t, ta)
			f.SetMode(settings.ModePublic)
			fn(t, ta, f)
		})
	}
}

func recorderOf(ta *testutil.TestApp) analytics.Recorder {
	return core.MustUse[analytics.Recorder](ta.Deps)
}

// createAggregates applies the proposed migration 0500 (testdata), unless the
// table already exists because the migration has landed.
func createAggregates(t *testing.T, ta *testutil.TestApp) {
	t.Helper()
	ctx := context.Background()
	var n int
	if ta.Deps.DB.Get(ctx, &n, `SELECT COUNT(*) FROM read_event_days WHERE 1 = 0`) == nil {
		return
	}
	b, err := os.ReadFile("testdata/0500_read_event_days.sql")
	if err != nil {
		t.Fatal(err)
	}
	stmts, err := db.SplitStatements(string(b))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stmts {
		if strings.TrimSpace(s) == "" {
			continue
		}
		if _, err := ta.Deps.DB.Exec(ctx, s); err != nil {
			t.Fatalf("create aggregates: %v", err)
		}
	}
}

// rawEvent inserts a read event directly (bypassing Record) at a given time.
type rawEvent struct {
	Series, Publication, User, Session, Kind string
	Page, MS                                 int
	At                                       time.Time
}

func insertEvents(t *testing.T, ta *testutil.TestApp, events ...rawEvent) {
	t.Helper()
	for _, e := range events {
		if _, err := ta.Deps.DB.Exec(context.Background(), `INSERT INTO read_events (id, series_id, publication_id, user_id,
			session_hash, kind, page, ms, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			db.NewID(), e.Series, e.Publication, db.NullString(e.User), e.Session, e.Kind, e.Page, e.MS, e.At.UTC()); err != nil {
			t.Fatalf("insert event: %v", err)
		}
	}
}

func insertFollow(t *testing.T, ta *testutil.TestApp, userID, seriesID string, at time.Time) {
	t.Helper()
	if _, err := ta.Deps.DB.Exec(context.Background(), `INSERT INTO follows (user_id, series_id, notify, created_at) VALUES (?, ?, TRUE, ?)`,
		userID, seriesID, at.UTC()); err != nil {
		t.Fatalf("insert follow: %v", err)
	}
}

func count(t *testing.T, ta *testutil.TestApp, query string, args ...any) int {
	t.Helper()
	var n int
	if err := ta.Deps.DB.Get(context.Background(), &n, query, args...); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func mustContain(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("body does not contain %q", w)
		}
	}
}

func mustNotContain(t *testing.T, body string, unwanted ...string) {
	t.Helper()
	for _, w := range unwanted {
		if strings.Contains(body, w) {
			t.Errorf("body contains %q", w)
		}
	}
}
