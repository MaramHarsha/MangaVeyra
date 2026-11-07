package gen_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestModuleRegistersGateAServiceAndOnlyScriptHandler(t *testing.T) {
	d := &core.Deps{}
	module := gen.New(d)
	registered, ok := core.Use[gen.Service](d)
	if !ok || registered == nil {
		t.Fatal("New did not provide the concrete gen.Service")
	}
	if got := d.Services(); len(got) != 1 || got[0] != "gen.Service" {
		t.Fatalf("New provided services %v, want exactly gen.Service", got)
	}
	handlers := module.JobHandlers()
	if len(handlers) != 1 || handlers[gen.JobScript] == nil {
		t.Fatalf("JobHandlers = %v, want only %q", handlers, gen.JobScript)
	}
}

func TestWiredModuleResolvesDependenciesAndDefersGateBWithoutJobs(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		svc := core.MustUse[gen.Service](ta.Deps)
		var before int
		if err := ta.Deps.DB.Get(context.Background(), &before, `SELECT COUNT(*) FROM jobs`); err != nil {
			t.Fatal(err)
		}
		actor := studio.Actor{}
		checks := []error{}
		_, err := svc.EnqueueCharacterSheet(context.Background(), actor, gen.CharacterSheetRequest{})
		checks = append(checks, err)
		_, err = svc.EnqueuePagesFromScript(context.Background(), actor, gen.PagesFromScriptRequest{})
		checks = append(checks, err)
		_, err = svc.EnqueuePanel(context.Background(), actor, gen.PanelRequest{})
		checks = append(checks, err)
		_, err = svc.EnqueueRegionEdit(context.Background(), actor, gen.RegionEditRequest{})
		checks = append(checks, err)
		_, err = svc.Candidates(context.Background(), actor, "")
		checks = append(checks, err)
		checks = append(checks, svc.ApplyVariation(context.Background(), actor, "", 0))
		for i, err := range checks {
			if !errors.Is(err, gen.ErrDeferredFeature) {
				t.Fatalf("deferred call %d returned %v", i, err)
			}
		}
		var after int
		if err := ta.Deps.DB.Get(context.Background(), &after, `SELECT COUNT(*) FROM jobs`); err != nil {
			t.Fatal(err)
		}
		if after != before {
			t.Fatalf("deferred methods changed job count from %d to %d", before, after)
		}
	})
}
