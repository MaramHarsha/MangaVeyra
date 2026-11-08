package gen_test

import (
	"context"
	"sync"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// peerApp starts a second server process on ta's database, as a multi-process
// deployment runs it: its own memory, connection pool and data directory.
func peerApp(t *testing.T, ta *testutil.TestApp) *testutil.TestApp {
	t.Helper()
	dialect, dsn, err := ta.Deps.Cfg.Database()
	if err != nil {
		t.Fatal(err)
	}
	url := dsn
	if dialect == "sqlite" {
		url = "sqlite://" + dsn
	}
	return testutil.NewTestApp(t, testutil.WithEnv("MV_DB", url), testutil.WithoutAdmin())
}

// raceEnqueue calls enqueue from several goroutines spread over the given
// processes. Every call must succeed with the same job, and exactly one of
// them may report that it stored new work.
func raceEnqueue[S any](t *testing.T, what string, services []S, enqueue func(S) (*jobs.Job, error)) *jobs.Job {
	t.Helper()
	const n = 6
	got := make([]*jobs.Job, n)
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got[i], errs[i] = enqueue(services[i%len(services)])
		}()
	}
	close(start)
	wg.Wait()
	fresh := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("%s %d: %v", what, i, errs[i])
		}
		if got[i].ID != got[0].ID {
			t.Fatalf("%s: concurrent identical requests stored different jobs", what)
		}
		if !got[i].Deduplicated {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("%s: %d calls reported new work", what, fresh)
	}
	return got[0]
}

func activeJobs(t *testing.T, ta *testutil.TestApp, typ string) int {
	t.Helper()
	var n int
	if err := ta.Deps.DB.Get(context.Background(), &n, `SELECT COUNT(*) FROM jobs WHERE type = ? AND state IN ('queued','running')`, typ); err != nil {
		t.Fatal(err)
	}
	return n
}

// Panel and layer generation are keyed in the database; page regeneration,
// Character Lock repair and assessment reuse an identical coordinator under
// row locks in the enqueue transaction. Neither depends on process memory.
func TestGenerationDeduplicationHoldsAcrossServerProcesses(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, _, pages := controlPages(t, ta, 2)
		lockWorkerSetup(t, ta, jobs.Registration{Name: "Scoring fixture", Backends: []string{"vision"}, JobTypes: []string{"character.score"}, Models: []string{dinoModel}})
		peer := peerApp(t, ta)
		images := []gen.Service{f.gen, core.MustUse[gen.Service](peer.Deps)}
		controls := []gen.ControlService{core.MustUse[gen.ControlService](ta.Deps), core.MustUse[gen.ControlService](peer.Deps)}

		page, pn := pages[0], pages[0].Panels[0]
		panel := gen.PanelRequest{ProjectID: f.project.ID, PageID: page.ID, PanelID: pn.ID, Variations: 4, Prompt: "Rin vaults the railing.", RoutingMode: "balanced", ExpectedVersion: page.Version}
		first := raceEnqueue(t, "panel", images, func(s gen.Service) (*jobs.Job, error) { return s.EnqueuePanel(ctx, f.actor, panel) })
		layer := panel
		layer.LayerID = pn.Layers[0].ID
		variant := panel
		variant.Variations = 2
		for name, req := range map[string]gen.PanelRequest{"layer": layer, "variations": variant} {
			j, err := images[1].EnqueuePanel(ctx, f.actor, req)
			if err != nil || j.Deduplicated || j.ID == first.ID {
				t.Fatalf("%s request merged with the panel job: %+v %v", name, j, err)
			}
		}
		if _, err := ta.Deps.Jobs.Cancel(ctx, first.ID); err != nil {
			t.Fatal(err)
		}
		if j, err := images[1].EnqueuePanel(ctx, f.actor, panel); err != nil || j.Deduplicated || j.ID == first.ID {
			t.Fatalf("canceled job absorbed a new request: %+v %v", j, err)
		}

		regenerate := gen.PageRequest{ProjectID: f.project.ID, PageID: pages[1].ID, ExpectedVersion: pages[1].Version}
		raceEnqueue(t, "page", controls, func(c gen.ControlService) (*jobs.Job, error) { return c.EnqueuePage(ctx, f.actor, regenerate) })
		l := page.Panels[0].Layers[1]
		repair := gen.RepairRequest{ProjectID: f.project.ID, ChapterID: f.chapter.ID, CharacterID: l.CharacterID, Targets: []gen.RepairTarget{{PageID: page.ID, PanelID: page.Panels[0].ID, LayerID: l.ID, Version: page.Version}}}
		// The page coordinator above covers another page, so the repair is separate work.
		raceEnqueue(t, "repair", controls, func(c gen.ControlService) (*jobs.Job, error) { return c.EnqueueRepair(ctx, f.actor, repair) })
		character, err := f.studio.GetCharacter(ctx, l.CharacterID)
		if err != nil {
			t.Fatal(err)
		}
		raceEnqueue(t, "assessment", controls, func(c gen.ControlService) (*jobs.Job, error) {
			return c.EnqueueScoring(ctx, f.actor, f.project.ID, character.ID, character.UpdatedAt)
		})
		for typ, want := range map[string]int{gen.JobPanel: 3, gen.JobPage: 1, gen.JobRepair: 1, gen.JobScore: 1} {
			if got := activeJobs(t, ta, typ); got != want {
				t.Fatalf("active %s jobs = %d, want %d", typ, got, want)
			}
		}
	})
}
