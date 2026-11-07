package gen_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestScoringProtocolRecordsSeparateOutfitEvidenceAndRejectsMock(t *testing.T) {
	for _, method := range []string{"dinov2-small-v1:" + strings.Repeat("a", 64), "mock", "structural"} {
		t.Run(method[:4], func(t *testing.T) {
			forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				f, _, pages := controlPages(t, ta, 1)
				if _, err := ta.Deps.Settings.UpdateInstance(ctx, ta.Admin.ID, func(inst *settings.Instance) error { inst.LocalOnly = false; return nil }); err != nil {
					t.Fatal(err)
				}
				w, err := ta.Deps.Jobs.RegisterWorker(ctx, "controlled-worker", "127.0.0.1", jobs.Registration{Name: "Controlled protocol fixture", Backends: []string{"vision"}, JobTypes: []string{"character.score"}, Models: []string{"character-lock-dinov2-v1"}, MaxConcurrent: 1})
				if err != nil {
					t.Fatal(err)
				}
				c, err := f.studio.GetCharacter(ctx, f.characters[0].ID)
				if err != nil {
					t.Fatal(err)
				}
				control := core.MustUse[gen.ControlService](ta.Deps)
				parent, err := control.EnqueueScoring(ctx, f.actor, f.project.ID, c.ID, c.UpdatedAt)
				if err != nil {
					t.Fatal(err)
				}
				completed := make(chan error, 1)
				go func() {
					for ctx.Err() == nil {
						child, err := ta.Deps.Jobs.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorWorker, WorkerID: w.ID, Types: []string{"character.score"}, Models: []string{"character-lock-dinov2-v1"}, MaxRunning: 1})
						if err != nil {
							completed <- err
							return
						}
						if child == nil {
							time.Sleep(10 * time.Millisecond)
							continue
						}
						_, err = ta.Deps.Jobs.Complete(ctx, child.ID, w.ID, map[string]any{"scores": []float64{.98}, "outfit_scores": []float64{.78}, "method": method}, jobs.Cost{})
						completed <- err
						return
					}
					completed <- ctx.Err()
				}()
				done := runArtJob(t, ta, parent.ID)
				if err := <-completed; err != nil {
					t.Fatal(err)
				}
				status, err := control.LockStatus(ctx, f.actor, f.project.ID, c.ID)
				if err != nil {
					t.Fatal(err)
				}
				if method == "mock" || method == "structural" {
					if done.State == jobs.StateDone || status.Score != nil || len(status.Drift) != 0 {
						t.Fatal("diagnostic/mock score was accepted as learned identity")
					}
					return
				}
				if done.State != jobs.StateDone || status.Score == nil || *status.Score != .78 || status.DriftTotal != 1 || len(status.Drift) != 1 || status.Drift[0].Outfit == nil || *status.Drift[0].Outfit != .78 {
					t.Fatalf("separate evidence missing: %+v job=%+v", status, done.Error)
				}
				p := pages[0]
				if err := control.WithLockScores(ctx, f.actor, &p); err != nil {
					t.Fatal(err)
				}
				if p.Panels[0].Layers[1].LockScore == nil || *p.Panels[0].Layers[1].LockScore != .78 {
					t.Fatal("UI did not use current evidence")
				}
				// A reference change invalidates previous measurements, even when the
				// raster asset and old persisted layer percentage are unchanged.
				c, err = f.studio.GetCharacter(ctx, c.ID)
				if err != nil {
					t.Fatal(err)
				}
				bible := core.MustUse[studio.BibleService](ta.Deps)
				if _, err = bible.AddCharacterRef(ctx, f.actor, studio.NewCharacterRef{ProjectID: f.project.ID, CharacterID: c.ID, AssetID: pages[0].Panels[0].Layers[1].Asset.ID, Kind: "expression", ExpectedUpdatedAt: c.UpdatedAt}); err != nil {
					t.Fatal(err)
				}
				if err := control.WithLockScores(ctx, f.actor, &p); err != nil {
					t.Fatal(err)
				}
				if p.Panels[0].Layers[1].LockScore != nil {
					t.Fatal("old reference evidence survived a bible change")
				}
				status, err = control.LockStatus(ctx, f.actor, f.project.ID, c.ID)
				if err != nil || status.Score != nil || status.Unscored != 1 {
					t.Fatalf("stale status evidence: %+v %v", status, err)
				}
			})
		})
	}
}
