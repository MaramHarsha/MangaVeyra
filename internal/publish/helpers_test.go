package publish_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

// forEachDialect runs fn against a fresh app on SQLite and, when
// MV_TEST_POSTGRES_URL is set, PostgreSQL.
func forEachDialect(t *testing.T, fn func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture)) {
	t.Helper()
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			t.Parallel()
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect))
			fn(t, ta, publishtest.New(t, ta))
		})
	}
}

func TestServiceIsProvided(t *testing.T) {
	ta := testutil.NewTestApp(t)
	if _, ok := core.Use[publish.Service](ta.Deps); !ok {
		t.Fatal("publish.Service is not provided")
	}
}

func wantNotFound(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, publish.ErrNotFound) {
		t.Fatalf("%s: err = %v, want ErrNotFound", what, err)
	}
}

func mustOK(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// listAll pages through ListSeries and returns the IDs in order.
func listAll(t *testing.T, svc publish.Service, v publish.Viewer, f publish.SeriesFilter) []string {
	t.Helper()
	var ids []string
	seen := map[string]bool{}
	for range 100 {
		list, next, err := svc.ListSeries(context.Background(), v, f)
		if err != nil {
			t.Fatalf("ListSeries: %v", err)
		}
		for _, s := range list {
			if seen[s.ID] {
				t.Fatalf("ListSeries returned %s twice", s.ID)
			}
			seen[s.ID] = true
			ids = append(ids, s.ID)
		}
		if next == "" {
			return ids
		}
		f.Cursor = next
	}
	t.Fatal("ListSeries did not finish paging")
	return nil
}
