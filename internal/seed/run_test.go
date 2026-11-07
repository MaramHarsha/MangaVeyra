package seed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/lettering"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

func deps(t *testing.T, database *db.DB) *core.Deps {
	t.Helper()
	st, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := settings.New(database)
	// Match CLI withDeps: no feature service is registered and no HTTP server
	// or AI module exists. Page authorization resolves the actual database user.
	return &core.Deps{DB: database, Store: st, Media: storage.NewSigner(bytes.Repeat([]byte{7}, 32)), Settings: s,
		Jobs: jobs.New(database, s, nil, jobs.Options{})}
}

func user(t *testing.T, database *db.DB, name, role, status string, at time.Time) string {
	t.Helper()
	id := db.NewID()
	if _, err := database.Exec(context.Background(), `INSERT INTO users (id, username, role, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, id, name, role, status, at, at); err != nil {
		t.Fatal(err)
	}
	return id
}

func count(t *testing.T, database *db.DB, table string) int {
	t.Helper()
	var n int
	if err := database.Get(context.Background(), &n, `SELECT COUNT(*) FROM `+table); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDemoSuccess(t *testing.T) {
	testdb.ForEach(t, func(t *testing.T, database *db.DB) {
		ctx := context.Background()
		d := deps(t, database)
		owner := user(t, database, "harbor-admin", "admin", "active", db.Now())
		if err := Run(ctx, d, Options{Demo: true}); err != nil {
			t.Fatal(err)
		}
		for table, want := range map[string]int{"users": 1, "projects": 1, "project_members": 1, "arcs": 1, "chapters": 2,
			"characters": 5, "character_refs": 5, "character_outfits": 5, "setting_items": 4, "scenes": 2,
			"script_panels": 12, "pages": 4, "panels": 12, "versions": 4, "assets": 9, "jobs": 0, "series": 0, "spend_ledger": 0, "spend_reservations": 0} {
			if got := count(t, database, table); got != want {
				t.Fatalf("%s: got %d, want %d", table, got, want)
			}
		}
		var project struct {
			ID, OwnerID, Title, Settings, CoverAssetID string
		}
		if err := database.Get(ctx, &project, `SELECT id, owner_id AS ownerid, title, settings, cover_asset_id AS coverassetid FROM projects`); err != nil {
			t.Fatal(err)
		}
		if project.OwnerID != owner || project.Title != demoTitle || project.CoverAssetID == "" {
			t.Fatalf("project: %#v", project)
		}
		var marker struct {
			Marker demoMarker `json:"demo_seed"`
		}
		if err := json.Unmarshal([]byte(project.Settings), &marker); err != nil || marker.Marker != (demoMarker{demoID, demoVersion}) {
			t.Fatalf("marker: %s, %v", project.Settings, err)
		}
		s := core.MustUse[studio.Service](d)
		chapters, err := s.ListChapters(ctx, project.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, chapter := range chapters {
			script, err := s.GetScript(ctx, chapter.ID)
			if err != nil || s.ValidateScript(ctx, project.ID, script) != nil {
				t.Fatalf("script: %#v, %v", script, err)
			}
			docs, err := core.MustUse[pagestore.Service](d).ChapterPages(ctx, chapter.ID)
			if err != nil || len(docs) != 2 {
				t.Fatalf("pages: %d, %v", len(docs), err)
			}
			for _, doc := range docs {
				if err := doc.Validate(); err != nil {
					t.Fatal(err)
				}
				if doc.Locked || doc.Version != 1 || len(doc.Bubbles) == 0 || len(doc.Panels) != 3 {
					t.Fatalf("page: %#v", doc)
				}
				for _, panel := range doc.Panels {
					kinds := map[string]bool{}
					for _, layer := range panel.Layers {
						kinds[layer.Kind] = true
						if layer.Locked || layer.Provenance != pagedoc.ProvenanceHuman || layer.JobID != "" || layer.ModelID != "" || layer.ProviderID != "" {
							t.Fatalf("dishonest or locked fixture layer: %#v", layer)
						}
						if layer.Kind == pagedoc.LayerLettering && layer.Asset != nil {
							t.Fatal("lettering was flattened")
						}
					}
					if !kinds["background"] || !kinds["character"] || !kinds["fx"] || !kinds["lettering"] || panel.ScriptPanelID == "" {
						t.Fatalf("independent layers missing: %#v", panel)
					}
				}
				for _, bubble := range doc.Bubbles {
					if bubble.Texts["en"].Source != "human" || bubble.ScriptPanelID == "" {
						t.Fatalf("bubble provenance: %#v", bubble)
					}
					layout, err := core.MustUse[lettering.Engine](d).Layout(bubble, "en")
					if err != nil || layout.Overflow {
						t.Fatalf("demo lettering: overflow=%v, %v", layout.Overflow, err)
					}
				}
			}
		}
		var assetsRows []struct{ ID string }
		if err := database.Select(ctx, &assetsRows, `SELECT id FROM assets`); err != nil {
			t.Fatal(err)
		}
		for _, row := range assetsRows {
			data, a, err := core.MustUse[*assets.Service](d).Read(ctx, row.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if a.Source != assets.SourceSystem || a.ProjectID == nil || *a.ProjectID != project.ID || a.OwnerID == nil || *a.OwnerID != owner ||
				!strings.Contains(string(a.Meta), `"placeholder":true`) {
				t.Fatalf("asset provenance: %#v", a)
			}
			im, err := png.Decode(bytes.NewReader(data))
			if err != nil || im.Bounds().Dx() != 640 || im.Bounds().Dy() != 960 {
				t.Fatalf("placeholder PNG: %v", err)
			}
		}
	})
}

func TestDemoPreservesEditsAndSameTitle(t *testing.T) {
	testdb.ForEach(t, func(t *testing.T, database *db.DB) {
		ctx := context.Background()
		d := deps(t, database)
		owner := user(t, database, "admin", "admin", "active", db.Now())
		manual := db.NewID()
		if _, err := database.Exec(ctx, `INSERT INTO projects (id, owner_id, title, slug, settings) VALUES (?, ?, ?, ?, ?)`,
			manual, owner, demoTitle, "tidebreak-courier-demo", `{"my_note":"preserve me"}`); err != nil {
			t.Fatal(err)
		}
		if err := Run(ctx, d, Options{Demo: true}); err != nil {
			t.Fatal(err)
		}
		var id string
		if err := database.Get(ctx, &id, `SELECT id FROM projects WHERE id <> ?`, manual); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(ctx, `UPDATE projects SET title = 'My edited story', premise = 'My new premise' WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
		var pageID string
		if err := database.Get(ctx, &pageID, `SELECT id FROM pages LIMIT 1`); err != nil {
			t.Fatal(err)
		}
		p := core.MustUse[pagestore.Service](d)
		page, err := p.GetPage(ctx, pageID)
		if err != nil {
			t.Fatal(err)
		}
		page.Bubbles[0].Texts["en"] = pagedoc.BubbleText{Text: "My rewritten dialogue.", Status: "reviewed", Source: "human"}
		if _, err := p.SavePage(ctx, page, page.Version, pagestore.Change{ActorKind: "user", ActorID: owner, Action: "save", Summary: "Rewrite demo"}); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if err := Run(ctx, d, Options{Demo: true}); err != nil {
				t.Fatal(err)
			}
		}
		if count(t, database, "projects") != 2 || count(t, database, "assets") != 9 || count(t, database, "versions") != 5 {
			t.Fatal("repeat seed duplicated or rewrote content")
		}
		page, err = p.GetPage(ctx, pageID)
		if err != nil || page.Version != 2 || page.Bubbles[0].Texts["en"].Text != "My rewritten dialogue." {
			t.Fatalf("lost edit: %#v, %v", page, err)
		}
		var title, manualSettings, slug string
		_ = database.Get(ctx, &title, `SELECT title FROM projects WHERE id = ?`, id)
		_ = database.Get(ctx, &manualSettings, `SELECT settings FROM projects WHERE id = ?`, manual)
		_ = database.Get(ctx, &slug, `SELECT slug FROM projects WHERE id = ?`, id)
		if title != "My edited story" || manualSettings != `{"my_note":"preserve me"}` || slug != "tidebreak-courier-demo-2" {
			t.Fatalf("user project altered or slug collision: %q %q %q", title, manualSettings, slug)
		}
	})
}

func TestDemoOwnerSelectionAndIsolation(t *testing.T) {
	testdb.ForEach(t, func(t *testing.T, database *db.DB) {
		ctx := context.Background()
		d := deps(t, database)
		if err := Run(ctx, d, Options{Demo: true}); !errors.Is(err, ErrOwner) {
			t.Fatalf("no owner: %v", err)
		}
		now := db.Now()
		user(t, database, "suspended", "admin", "suspended", now.Add(-3*time.Hour))
		first := user(t, database, "first", "admin", "active", now.Add(-2*time.Hour))
		user(t, database, "later", "admin", "active", now.Add(-time.Hour))
		creator := user(t, database, "courier", "creator", "active", now)
		user(t, database, "reader", "reader", "active", now)
		for _, name := range []string{"missing", "suspended", "reader"} {
			if err := Run(ctx, d, Options{Demo: true, OwnerUsername: name}); !errors.Is(err, ErrOwner) {
				t.Fatalf("ineligible owner %s: %v", name, err)
			}
		}
		if err := Run(ctx, d, Options{Demo: true}); err != nil {
			t.Fatal(err)
		}
		var actual string
		if err := database.Get(ctx, &actual, `SELECT owner_id FROM projects`); err != nil || actual != first {
			t.Fatalf("default owner: %s %v", actual, err)
		}
		if err := Run(ctx, d, Options{Demo: true, OwnerUsername: "courier"}); err != nil {
			t.Fatal(err)
		}
		if err := Run(ctx, d, Options{Demo: true, OwnerUsername: "courier"}); err != nil {
			t.Fatal(err)
		}
		var n int
		_ = database.Get(ctx, &n, `SELECT COUNT(*) FROM projects WHERE owner_id = ?`, creator)
		if n != 1 || count(t, database, "projects") != 2 || count(t, database, "users") != 5 {
			t.Fatal("owner marker was not isolated or created accounts")
		}
		var crossOwner int
		_ = database.Get(ctx, &crossOwner, `SELECT COUNT(*) FROM assets a JOIN projects p ON p.id = a.project_id WHERE a.owner_id <> p.owner_id`)
		if crossOwner != 0 {
			t.Fatal("cross-owner asset")
		}
	})
}

type failingPages struct{ pagestore.Service }

var errFixture = errors.New("test fixture failure")

func (p failingPages) CreatePagesTx(ctx context.Context, tx *db.Tx, chapter string, docs []pagedoc.Page, change pagestore.Change) ([]pagedoc.Page, error) {
	if _, err := p.Service.CreatePagesTx(ctx, tx, chapter, docs, change); err != nil {
		return nil, err
	}
	return nil, errFixture
}

func TestDemoRollback(t *testing.T) {
	testdb.ForEach(t, func(t *testing.T, database *db.DB) {
		ctx := context.Background()
		d := deps(t, database)
		user(t, database, "admin", "admin", "active", db.Now())
		if err := ensureServices(ctx, d); err != nil {
			t.Fatal(err)
		}
		failed := &core.Deps{DB: d.DB, Jobs: d.Jobs, Store: d.Store, Media: d.Media}
		core.Provide[*assets.Service](failed, core.MustUse[*assets.Service](d))
		core.Provide[studio.Service](failed, core.MustUse[studio.Service](d))
		core.Provide[lettering.Engine](failed, core.MustUse[lettering.Engine](d))
		core.Provide[pagestore.Service](failed, failingPages{core.MustUse[pagestore.Service](d)})
		if err := Run(ctx, failed, Options{Demo: true}); !errors.Is(err, errFixture) {
			t.Fatalf("rollback failure: %v", err)
		}
		for _, table := range []string{"projects", "project_members", "assets", "chapters", "characters", "character_refs", "character_outfits", "setting_items", "scenes", "script_panels", "pages", "panels", "layers", "bubbles", "versions"} {
			if count(t, database, table) != 0 {
				t.Fatalf("partial %s after rollback", table)
			}
		}
		objects, err := d.Store.List(ctx, "projects/")
		if err != nil || len(objects) != 0 {
			t.Fatalf("orphan fixture files: %d, %v", len(objects), err)
		}
		if err := Run(ctx, d, Options{Demo: true}); err != nil {
			t.Fatalf("rollback left a marker: %v", err)
		}
	})
}

type brokenStore struct {
	storage.Store
	writes int
}

func (s *brokenStore) Put(ctx context.Context, key string, data io.Reader, size int64, contentType string) error {
	s.writes++
	if s.writes == 3 {
		return errFixture
	}
	return s.Store.Put(ctx, key, data, size, contentType)
}

func TestDemoStorageFailureCleansStaging(t *testing.T) {
	testdb.ForEach(t, func(t *testing.T, database *db.DB) {
		d := deps(t, database)
		d.Store = &brokenStore{Store: d.Store}
		user(t, database, "admin", "admin", "active", db.Now())
		if err := Run(context.Background(), d, Options{Demo: true}); !errors.Is(err, errFixture) {
			t.Fatal(err)
		}
		objects, err := d.Store.List(context.Background(), "projects/")
		if err != nil || len(objects) != 0 || count(t, database, "projects") != 0 || count(t, database, "assets") != 0 {
			t.Fatalf("failed staging left fixture data: %d, %v", len(objects), err)
		}
	})
}

func TestDemoConcurrentIdempotency(t *testing.T) {
	testdb.ForEach(t, func(t *testing.T, database *db.DB) {
		d := deps(t, database)
		user(t, database, "admin", "admin", "active", db.Now())
		var wg sync.WaitGroup
		errs := make(chan error, 3)
		for i := 0; i < 3; i++ {
			wg.Go(func() { errs <- Run(context.Background(), d, Options{Demo: true}) })
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		objects, err := d.Store.List(context.Background(), "projects/")
		if err != nil || len(objects) != 9 || count(t, database, "projects") != 1 || count(t, database, "assets") != 9 {
			t.Fatalf("concurrent seed: objects %d, %v", len(objects), err)
		}
	})
}

func TestPlaceholderDeterministicAndLabelled(t *testing.T) {
	for _, spec := range newFixture(db.NewID()).artSpecs() {
		a, err := renderArt(spec)
		if err != nil {
			t.Fatal(err)
		}
		b, err := renderArt(spec)
		if err != nil || !bytes.Equal(a, b) {
			t.Fatalf("nondeterministic %s: %v", spec.key, err)
		}
		im, err := png.Decode(bytes.NewReader(a))
		if err != nil {
			t.Fatal(err)
		}
		// The universal label plate and its ink are inside the stored PNG,
		// including transparent character and FX assets.
		_, _, _, alpha := im.At(18, 910).RGBA()
		if alpha != 65535 {
			t.Fatalf("missing opaque placeholder plate: %s", spec.key)
		}
		inkPixels := 0
		for y := 900; y < 922; y++ {
			for x := 28; x < 330; x++ {
				r, _, _, a := im.At(x, y).RGBA()
				if a > 0 && r < 10000 {
					inkPixels++
				}
			}
		}
		if inkPixels < 100 {
			t.Fatalf("placeholder text was not drawn: %s", spec.key)
		}
	}
}
