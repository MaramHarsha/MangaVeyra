package jobs_test

import (
	"context"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestWorkerClaimDoesNotExposeInputsWhenLocalOnlyChanges(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		j, err := ta.Deps.Jobs.Enqueue(ctx, jobs.EnqueueParams{Type: "image.generate", Executor: jobs.ExecutorWorker, Scope: none,
			Input: map[string]string{"prompt": "Private tide-gate sketch"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ta.Deps.Settings.Instance(ctx); err != nil {
			t.Fatal(err)
		}
		// Deliberately leave the process cache permissive. Claim authorization
		// must load the current policy inside its database transaction.
		if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO instance_settings (key,value) VALUES ('local_only','true')
			ON CONFLICT(key) DO UPDATE SET value=excluded.value`); err != nil {
			t.Fatal(err)
		}
		claim, err := ta.Deps.Jobs.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorWorker, Types: []string{"image.generate"}})
		if err != nil || claim != nil {
			t.Fatalf("Local only worker claim=%+v %v", claim, err)
		}
		stored, err := ta.Deps.Jobs.Get(ctx, j.ID)
		if err != nil || stored.State != jobs.StateQueued || stored.Attempts != 0 {
			t.Fatal("blocked handoff changed the job")
		}
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE instance_settings SET value='{' WHERE key='local_only'`); err != nil {
			t.Fatal(err)
		}
		if got, err := ta.Deps.Jobs.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorWorker, Types: []string{"image.generate"}}); err == nil || got != nil {
			t.Fatal("invalid policy allowed worker handoff")
		}
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE instance_settings SET value='false' WHERE key='local_only'`); err != nil {
			t.Fatal(err)
		}
		if _, err := ta.Deps.Settings.UpdateInstance(ctx, "", func(inst *settings.Instance) error { inst.Name = "Tidebreak Studio"; return nil }); err != nil {
			t.Fatal(err)
		}
		claim, err = ta.Deps.Jobs.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorWorker, Types: []string{"image.generate"}})
		if err != nil || claim == nil || claim.ID != j.ID {
			t.Fatalf("connected worker could not resume=%+v %v", claim, err)
		}
	})
}
