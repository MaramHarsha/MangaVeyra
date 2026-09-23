package ws_test

import (
	"context"
	"net/url"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func mustParse(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

func jobsParams(userID string) jobs.EnqueueParams {
	return jobs.EnqueueParams{Type: "image.generate", UserID: userID, Scope: jobs.Scope{Kind: jobs.ScopeNone}}
}

func seedPage(t *testing.T, ta *testutil.TestApp) string {
	t.Helper()
	ctx := context.Background()
	proj, ch, page := db.NewID(), db.NewID(), db.NewID()
	for _, s := range []struct {
		q    string
		args []any
	}{
		{`INSERT INTO projects (id, owner_id, title, slug) VALUES (?, ?, ?, ?)`, []any{proj, ta.Admin.ID, "Glass Orchard", "glass-orchard"}},
		{`INSERT INTO chapters (id, project_id, number) VALUES (?, ?, ?)`, []any{ch, proj, 1}},
		{`INSERT INTO pages (id, project_id, chapter_id, number) VALUES (?, ?, ?, ?)`, []any{page, proj, ch, 7}},
	} {
		if _, err := ta.Deps.DB.Exec(ctx, s.q, s.args...); err != nil {
			t.Fatal(err)
		}
	}
	return page
}
