package gen_test

import (
	"context"
	"math"
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
	for _, scenario := range []struct {
		model, method string
		accepted      bool
		proof         string
	}{
		{"character-lock-dinov2-v1", "dinov2-small-v1:" + strings.Repeat("a", 64), true, ""},
		{"character-lock-ccip-v1", "ccip-caformer24-v1:" + strings.Repeat("b", 64), true, ""},
		{"character-lock-ccip-outfit-v3", "ccip-outfit-pair-v3:" + strings.Repeat("c", 64), true, "valid"},
		{"character-lock-ccip-outfit-v3", "ccip-outfit-pair-v3:" + strings.Repeat("c", 64), true, "gated-mismatch"},
		{"character-lock-ccip-outfit-v3", "ccip-outfit-pair-v3:" + strings.Repeat("c", 64), false, "skip-matching-identity"},
		{"character-lock-ccip-outfit-v3", "ccip-outfit-pair-v3:" + strings.Repeat("c", 64), false, "compare-wrong-identity"},
		{"character-lock-ccip-outfit-v3", "ccip-outfit-pair-v3:" + strings.Repeat("c", 64), false, "missing"},
		{"character-lock-ccip-outfit-v3", "ccip-outfit-pair-v3:" + strings.Repeat("c", 64), false, "wrong-identity"},
		{"character-lock-ccip-outfit-v3", "ccip-outfit-pair-v3:" + strings.Repeat("c", 64), false, "invalid-features"},
		{"character-lock-ccip-outfit-multiview-v1", "ccip-outfit-multiview-v1:" + strings.Repeat("b", 64), true, "mv-valid"},
		{"character-lock-ccip-outfit-multiview-v1", "ccip-outfit-multiview-v1:" + strings.Repeat("b", 64), true, "gated-mismatch"},
		{"character-lock-ccip-outfit-multiview-v1", "ccip-outfit-multiview-v1:" + strings.Repeat("b", 64), false, "mv-false-score"},
		{"character-lock-ccip-outfit-multiview-v1", "ccip-outfit-multiview-v1:" + strings.Repeat("b", 64), false, "mv-wrong-index"},
		{"character-lock-ccip-outfit-multiview-v1", "ccip-outfit-multiview-v1:" + strings.Repeat("b", 64), false, "mv-wrong-count"},
		{"character-lock-ccip-outfit-multiview-v1", "ccip-outfit-multiview-v1:" + strings.Repeat("b", 64), false, "mv-wrong-weights"},
		{"character-lock-ccip-outfit-multiview-v1", "ccip-outfit-multiview-v1:" + strings.Repeat("b", 64), false, "mv-pair-forgery"},
		{"character-lock-ccip-outfit-multiview-v1", "ccip-outfit-multiview-v1:" + strings.Repeat("b", 64), false, "mv-feature-forgery"},
		{"character-lock-ccip-outfit-multiview-v1", "ccip-outfit-multiview-v1:" + strings.Repeat("b", 64), false, "mv-missing-fit"},
		{"character-lock-ccip-outfit-multiview-v1", "ccip-outfit-multiview-v1:" + strings.Repeat("b", 64), false, "mv-invalid-fit"},
		{"character-lock-ccip-outfit-multiview-v1", "ccip-outfit-multiview-v1:" + strings.Repeat("b", 64), false, "skip-matching-identity"},
		{"character-lock-ccip-outfit-multiview-v1", "ccip-outfit-multiview-v1:" + strings.Repeat("b", 64), false, "compare-wrong-identity"},
		{"character-lock-ccip-v1", "dinov2-small-v1:" + strings.Repeat("a", 64), false, ""},
		{"character-lock-dinov2-v1", "mock", false, ""},
		{"character-lock-dinov2-v1", "structural", false, ""},
	} {
		method := scenario.method
		t.Run(scenario.model+"/"+method[:4], func(t *testing.T) {
			forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				f, _, pages := controlPages(t, ta, 1)
				if _, err := ta.Deps.Settings.UpdateInstance(ctx, ta.Admin.ID, func(inst *settings.Instance) error { inst.LocalOnly = false; return nil }); err != nil {
					t.Fatal(err)
				}
				w, err := ta.Deps.Jobs.RegisterWorker(ctx, "controlled-worker", "127.0.0.1", jobs.Registration{Name: "Controlled protocol fixture", Backends: []string{"vision"}, JobTypes: []string{"character.score"}, Models: []string{scenario.model}, MaxConcurrent: 1})
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
				output := map[string]any{"scores": []float64{.98}, "outfit_scores": []float64{.78}, "method": method}
				if scenario.proof != "" && scenario.proof != "missing" {
					identityMethod := "ccip-caformer24-v1:" + strings.Repeat("b", 64)
					output["identity_method"] = identityMethod
					output["feature_methods"] = map[string]string{"ccip": identityMethod, "fashion": "fashion-clip-v1:" + strings.Repeat("d", 64)}
					output["outfit_features"] = [][]float64{{.81, .79}}
					if scenario.proof == "wrong-identity" {
						output["identity_method"] = "dinov2-small-v1:" + strings.Repeat("a", 64)
					}
					if scenario.proof == "invalid-features" {
						output["outfit_features"] = [][]float64{{1.2, .79}}
					}
					if scenario.proof == "gated-mismatch" || scenario.proof == "skip-matching-identity" {
						output["outfit_scores"] = []*float64{nil}
						output["outfit_features"] = [][]float64{nil}
					}
					if scenario.proof == "gated-mismatch" || scenario.proof == "compare-wrong-identity" {
						output["scores"] = []float64{.6}
					}
					if scenario.model == "character-lock-ccip-outfit-multiview-v1" {
						index := 0
						output["outfit_reference_count"] = 1
						output["outfit_cosines"] = [][]float64{{.78}}
						output["outfit_reference_indices"] = []*int{&index}
						output["outfit_pair_features"] = [][][]float64{{{.78, .79}}}
						output["outfit_pair_scores"] = [][]float64{{.78}}
						output["outfit_pair_reference_indices"] = []*int{&index}
						output["outfit_fit"] = map[string]any{"mean": []float64{0, 0}, "scale": []float64{1, 1}, "weights": []float64{0, 0}, "intercept": math.Log(.78 / .22)}
						switch scenario.proof {
						case "gated-mismatch", "skip-matching-identity":
							output["outfit_cosines"] = [][]float64{nil}
							output["outfit_reference_indices"] = []*int{nil}
							output["outfit_pair_features"] = [][][]float64{nil}
							output["outfit_pair_scores"] = [][]float64{nil}
							output["outfit_pair_reference_indices"] = []*int{nil}
						case "mv-false-score":
							output["outfit_scores"] = []float64{.99}
						case "mv-wrong-index":
							index = 1
						case "mv-wrong-count":
							output["outfit_reference_count"] = 2
						case "mv-wrong-weights":
							output["identity_method"] = "ccip-caformer24-v1:" + strings.Repeat("c", 64)
						case "mv-pair-forgery":
							output["outfit_pair_scores"] = [][]float64{{.99}}
						case "mv-feature-forgery":
							output["outfit_pair_features"] = [][][]float64{{{.99, .79}}}
						case "mv-missing-fit":
							delete(output, "outfit_fit")
						case "mv-invalid-fit":
							output["outfit_fit"] = map[string]any{"mean": []float64{0, 0}, "scale": []float64{0, 1}, "weights": []float64{0, 0}, "intercept": 0}
						}
					}
				}
				go func() {
					for ctx.Err() == nil {
						child, err := ta.Deps.Jobs.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorWorker, WorkerID: w.ID, Types: []string{"character.score"}, Models: []string{scenario.model}, MaxRunning: 1})
						if err != nil {
							completed <- err
							return
						}
						if child == nil {
							time.Sleep(10 * time.Millisecond)
							continue
						}
						_, err = ta.Deps.Jobs.Complete(ctx, child.ID, w.ID, output, jobs.Cost{})
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
				if !scenario.accepted {
					if done.State == jobs.StateDone || status.Score != nil || len(status.Drift) != 0 {
						t.Fatal("diagnostic/mock score was accepted as learned identity")
					}
					return
				}
				want := .78
				if scenario.proof == "gated-mismatch" {
					want = .6
				}
				if done.State != jobs.StateDone || status.Score == nil || *status.Score != want || status.DriftTotal != 1 || len(status.Drift) != 1 {
					t.Fatalf("separate evidence missing: %+v job=%+v", status, done.Error)
				}
				if scenario.proof == "gated-mismatch" {
					if status.Drift[0].Outfit != nil || status.OutfitPending != 1 {
						t.Fatal("different identity was given an outfit judgment")
					}
				} else if status.Drift[0].Outfit == nil || *status.Drift[0].Outfit != .78 {
					t.Fatal("matching identity lost its mandatory outfit evidence")
				}
				if len(status.RecentScores) != 1 || status.RecentScores[0] != want {
					t.Fatal("designed chart did not use the current lower identity/outfit measurement")
				}
				p := pages[0]
				if err := control.WithLockScores(ctx, f.actor, &p); err != nil {
					t.Fatal(err)
				}
				if p.Panels[0].Layers[1].LockScore == nil || *p.Panels[0].Layers[1].LockScore != want {
					t.Fatal("UI did not use current evidence")
				}
				if scenario.model == "character-lock-dinov2-v1" || scenario.model == "character-lock-ccip-v1" {
					replacementModel := "character-lock-ccip-v1"
					if scenario.model == replacementModel {
						replacementModel = "character-lock-ccip-outfit-v3"
					}
					replacement, err := ta.Deps.Jobs.RegisterWorker(ctx, "replacement-worker", "127.0.0.1", jobs.Registration{Name: "Explicit scorer replacement fixture", Backends: []string{"vision"}, JobTypes: []string{"character.score"}, Models: []string{replacementModel}, MaxConcurrent: 1})
					if err != nil {
						t.Fatal(err)
					}
					pending, err := control.LockStatus(ctx, f.actor, f.project.ID, c.ID)
					if err != nil || pending.Score != nil || pending.Unscored != 1 || pending.DriftTotal != 0 || len(pending.RecentScores) != 0 {
						t.Fatalf("different model evidence was mixed: %+v %v", pending, err)
					}
					if err = control.WithLockScores(ctx, f.actor, &p); err != nil || p.Panels[0].Layers[1].LockScore != nil {
						t.Fatal("page displayed the previous model's percentage")
					}
					if err = ta.Deps.Jobs.SetWorkerStatus(ctx, replacement.ID, jobs.WorkerDraining); err != nil {
						t.Fatal(err)
					}
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
