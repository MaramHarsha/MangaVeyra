package studio_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

type scopeFixture struct {
	project, chapter, page, panel, layer, lettering string
}

func makeScopeFixture(t *testing.T, ta *testutil.TestApp) scopeFixture {
	t.Helper()
	ctx := context.Background()
	svc := studioService(ta)
	_, actor, project := createProject(t, ta, "Lock Ancestry")
	chapter, err := svc.CreateChapter(ctx, actor, project.ID, "Locked Targets")
	if err != nil {
		t.Fatal(err)
	}
	f := scopeFixture{project: project.ID, chapter: chapter.ID, page: db.NewID(), panel: db.NewID(), layer: db.NewID(), lettering: db.NewID()}
	now := db.Now()
	if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO pages (id, project_id, chapter_id, number, created_at, updated_at)
		VALUES (?, ?, ?, 1, ?, ?)`, f.page, f.project, f.chapter, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO panels (id, project_id, page_id, order_idx, created_at, updated_at)
		VALUES (?, ?, ?, 0, ?, ?)`, f.panel, f.project, f.page, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO layers (id, panel_id, kind, order_idx, created_at, updated_at)
		VALUES (?, ?, 'background', 0, ?, ?), (?, ?, 'lettering', 1, ?, ?)`,
		f.layer, f.panel, now, now, f.lettering, f.panel, now, now); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestCheckScopeWritableAncestryLocksAndSpoofing(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		svc := studioService(ta)
		f := makeScopeFixture(t, ta)
		region := json.RawMessage(`{"mask_asset_id":"mask"}`)
		scopes := []jobs.Scope{
			{Kind: jobs.ScopeNone},
			{Kind: jobs.ScopeProject, ProjectID: f.project},
			{Kind: jobs.ScopeChapter, ProjectID: f.project, ChapterID: f.chapter},
			{Kind: jobs.ScopePage, ProjectID: f.project, PageID: f.page},
			{Kind: jobs.ScopePanel, ProjectID: f.project, PageID: f.page, PanelID: f.panel},
			{Kind: jobs.ScopeLayer, ProjectID: f.project, PanelID: f.panel, LayerID: f.layer},
			{Kind: jobs.ScopeRegion, ProjectID: f.project, LayerID: f.layer, Region: region},
		}
		for _, scope := range scopes {
			if err := svc.CheckScopeWritable(ctx, scope); err != nil {
				t.Errorf("unlocked scope %s: %v", scope.Kind, err)
			}
		}

		spoofs := []jobs.Scope{
			{Kind: jobs.ScopeChapter, ProjectID: "spoof", ChapterID: f.chapter},
			{Kind: jobs.ScopePage, ProjectID: "spoof", PageID: f.page},
			{Kind: jobs.ScopePanel, ProjectID: f.project, PageID: "spoof", PanelID: f.panel},
			{Kind: jobs.ScopeLayer, ProjectID: f.project, PanelID: "spoof", LayerID: f.layer},
			{Kind: jobs.ScopeRegion, ProjectID: "spoof", LayerID: f.layer, Region: region},
		}
		for _, scope := range spoofs {
			if err := svc.CheckScopeWritable(ctx, scope); !errors.Is(err, studio.ErrForbidden) {
				t.Errorf("spoofed %s error = %v, want ErrForbidden", scope.Kind, err)
			}
		}
		if err := svc.CheckScopeWritable(ctx, jobs.Scope{Kind: jobs.ScopeRegion, ProjectID: f.project, LayerID: f.lettering, Region: region}); !errors.Is(err, studio.ErrForbidden) {
			t.Errorf("lettering region error = %v, want ErrForbidden", err)
		}
		if err := svc.CheckScopeWritable(ctx, jobs.Scope{Kind: jobs.ScopePage, ProjectID: f.project, PageID: "missing"}); !errors.Is(err, studio.ErrNotFound) {
			t.Errorf("missing target error = %v, want ErrNotFound", err)
		}

		_, _, otherProject := createProject(t, ta, "Corrupt Ancestry Guard")
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE panels SET project_id = ? WHERE id = ?`, otherProject.ID, f.panel); err != nil {
			t.Fatal(err)
		}
		for _, projectID := range []string{f.project, otherProject.ID} {
			if err := svc.CheckScopeWritable(ctx, jobs.Scope{Kind: jobs.ScopeRegion, ProjectID: projectID, LayerID: f.layer, Region: region}); !errors.Is(err, studio.ErrForbidden) {
				t.Errorf("inconsistent panel ancestry for project %s error = %v, want ErrForbidden", projectID, err)
			}
		}
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE panels SET project_id = ? WHERE id = ?`, f.project, f.panel); err != nil {
			t.Fatal(err)
		}
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE pages SET project_id = ? WHERE id = ?`, otherProject.ID, f.page); err != nil {
			t.Fatal(err)
		}
		for _, projectID := range []string{f.project, otherProject.ID} {
			if err := svc.CheckScopeWritable(ctx, jobs.Scope{Kind: jobs.ScopePage, ProjectID: projectID, PageID: f.page}); !errors.Is(err, studio.ErrForbidden) {
				t.Errorf("inconsistent page ancestry for project %s error = %v, want ErrForbidden", projectID, err)
			}
		}
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE pages SET project_id = ? WHERE id = ?`, f.project, f.page); err != nil {
			t.Fatal(err)
		}

		lockCases := []struct {
			name, update string
			args         []any
			scope        jobs.Scope
		}{
			{"chapter", `UPDATE chapters SET locked = TRUE WHERE id = ?`, []any{f.chapter}, jobs.Scope{Kind: jobs.ScopeLayer, ProjectID: f.project, PanelID: f.panel, LayerID: f.layer}},
			{"page", `UPDATE pages SET locked = TRUE WHERE id = ?`, []any{f.page}, jobs.Scope{Kind: jobs.ScopePanel, ProjectID: f.project, PageID: f.page, PanelID: f.panel}},
			{"panel", `UPDATE panels SET locked = TRUE WHERE id = ?`, []any{f.panel}, jobs.Scope{Kind: jobs.ScopeLayer, ProjectID: f.project, PanelID: f.panel, LayerID: f.layer}},
			{"layer", `UPDATE layers SET locked = TRUE WHERE id = ?`, []any{f.layer}, jobs.Scope{Kind: jobs.ScopeRegion, ProjectID: f.project, LayerID: f.layer, Region: region}},
		}
		for _, tc := range lockCases {
			t.Run(tc.name, func(t *testing.T) {
				if _, err := ta.Deps.DB.Exec(ctx, tc.update, tc.args...); err != nil {
					t.Fatal(err)
				}
				if err := svc.CheckScopeWritable(ctx, tc.scope); !errors.Is(err, studio.ErrLocked) {
					t.Fatalf("lock error = %v, want ErrLocked", err)
				}
				table := map[string]string{"chapter": "chapters", "page": "pages", "panel": "panels", "layer": "layers"}[tc.name]
				if _, err := ta.Deps.DB.Exec(ctx, `UPDATE `+table+` SET locked = FALSE WHERE id = ?`, tc.args[0]); err != nil {
					t.Fatal(err)
				}
			})
		}
	})
}

func TestStudioValidatorIsWiredIntoQueue(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f := makeScopeFixture(t, ta)
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE panels SET locked = TRUE WHERE id = ?`, f.panel); err != nil {
			t.Fatal(err)
		}
		scope := jobs.Scope{Kind: jobs.ScopePanel, ProjectID: f.project, PageID: f.page, PanelID: f.panel}
		if _, err := ta.Deps.Jobs.Enqueue(ctx, jobs.EnqueueParams{Type: "studio.validator.test", Scope: scope}); !errors.Is(err, studio.ErrLocked) {
			t.Fatalf("enqueue locked scope error = %v, want ErrLocked", err)
		}
		var count int
		if err := ta.Deps.DB.Get(ctx, &count, `SELECT COUNT(*) FROM jobs WHERE type = ?`, "studio.validator.test"); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("locked job was stored")
		}
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE panels SET locked = FALSE WHERE id = ?`, f.panel); err != nil {
			t.Fatal(err)
		}
		if _, err := ta.Deps.Jobs.Enqueue(ctx, jobs.EnqueueParams{Type: "studio.validator.test", Scope: scope}); err != nil {
			t.Fatalf("enqueue unlocked scope: %v", err)
		}
	})
}
