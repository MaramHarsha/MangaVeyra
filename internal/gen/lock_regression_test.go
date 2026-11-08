package gen_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

const dinoModel = "character-lock-dinov2-v1"

var dinoOutput = map[string]any{"scores": []float64{.95}, "outfit_scores": []float64{.93}, "method": "dinov2-small-v1:" + strings.Repeat("a", 64)}

// lockWorkerSetup enables remote/worker routes and registers one controlled
// worker fixture. Fixture scores are protocol data, not quality evidence.
func lockWorkerSetup(t *testing.T, ta *testutil.TestApp, reg jobs.Registration) *jobs.Worker {
	t.Helper()
	ctx := context.Background()
	if _, err := ta.Deps.Settings.UpdateInstance(ctx, ta.Admin.ID, func(inst *settings.Instance) error { inst.LocalOnly = false; return nil }); err != nil {
		t.Fatal(err)
	}
	w, err := ta.Deps.Jobs.RegisterWorker(ctx, "lock-fixture-"+db.NewID(), "127.0.0.1", reg)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// workChild runs fn for the first worker child of parent in the background.
func workChild(ctx context.Context, ta *testutil.TestApp, w *jobs.Worker, kind, model string, fn func(child *jobs.Job) error) chan error {
	done := make(chan error, 1)
	go func() {
		for ctx.Err() == nil {
			child, err := ta.Deps.Jobs.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorWorker, WorkerID: w.ID, Types: []string{kind}, Models: []string{model}, MaxRunning: 1})
			if err != nil {
				done <- err
				return
			}
			if child == nil {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			done <- fn(child)
			return
		}
		done <- ctx.Err()
	}()
	return done
}

func jobFailedNotices(t *testing.T, ta *testutil.TestApp, userID string) int {
	t.Helper()
	var n int
	if err := ta.Deps.DB.Get(context.Background(), &n, `SELECT COUNT(*) FROM notifications WHERE user_id=? AND kind='job_failed'`, userID); err != nil {
		t.Fatal(err)
	}
	return n
}

// Assessment is read-only for artwork (ADR 0011): approved, locked art can be
// re-assessed without unlocking it, while repair keeps refusing the lock.
func TestLockedApprovedArtCanBeReassessedButNotRepaired(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		f, ps, pages := controlPages(t, ta, 1)
		target := pages[0]
		l := target.Panels[0].Layers[1]
		if err := ps.SetLock(ctx, target.ID, target.Panels[0].ID, l.ID, true, pagestore.Change{ActorKind: "user", ActorID: f.actor.UserID, Action: "lock", ExpectedVersion: target.Version}); err != nil {
			t.Fatal(err)
		}
		if err := ps.SetLock(ctx, target.ID, "", "", true, pagestore.Change{ActorKind: "user", ActorID: f.actor.UserID, Action: "lock"}); err != nil {
			t.Fatal(err)
		}
		locked, err := ps.GetPage(ctx, target.ID)
		if err != nil {
			t.Fatal(err)
		}
		w := lockWorkerSetup(t, ta, jobs.Registration{Name: "Scoring fixture", Backends: []string{"vision"}, JobTypes: []string{"character.score"}, Models: []string{dinoModel}})
		control := core.MustUse[gen.ControlService](ta.Deps)
		c, err := f.studio.GetCharacter(ctx, f.characters[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		parent, err := control.EnqueueScoring(ctx, f.actor, f.project.ID, c.ID, c.UpdatedAt)
		if err != nil {
			t.Fatalf("locked art could not be assessed: %v", err)
		}
		worked := workChild(ctx, ta, w, "character.score", dinoModel, func(child *jobs.Job) error {
			_, err := ta.Deps.Jobs.Complete(ctx, child.ID, w.ID, dinoOutput, jobs.Cost{})
			return err
		})
		done := runArtJob(t, ta, parent.ID)
		if err := <-worked; err != nil {
			t.Fatal(err)
		}
		if done.State != jobs.StateDone {
			t.Fatalf("locked art assessment: %+v", done.Error)
		}
		after, err := ps.GetPage(ctx, target.ID)
		if err != nil || pageBytes(t, after) != pageBytes(t, locked) {
			t.Fatal("assessment changed the locked page")
		}
		status, err := control.LockStatus(ctx, f.actor, f.project.ID, c.ID)
		if err != nil || status.Score == nil || *status.Score != .93 {
			t.Fatalf("assessment evidence missing: %+v %v", status, err)
		}
		repair := gen.RepairRequest{ProjectID: f.project.ID, ChapterID: f.chapter.ID, CharacterID: c.ID, Targets: []gen.RepairTarget{{PageID: target.ID, PanelID: target.Panels[0].ID, LayerID: l.ID, Version: locked.Version}}}
		if _, err := control.EnqueueRepair(ctx, f.actor, repair); !errors.Is(err, studio.ErrLocked) && !errors.Is(err, pagestore.ErrLocked) {
			t.Fatalf("locked repair accepted: %v", err)
		}
	})
}

// The parent assessment follows its worker child: a canceled child cancels
// the parent without a failure notice, a worker failure keeps its own safe
// code instead of "target changed" (bad_input), and progress is mirrored.
func TestCharacterAssessmentFollowsItsWorkerChild(t *testing.T) {
	for _, tc := range []struct {
		name, state, code string
		notices           int
		work              func(context.Context, *testutil.TestApp, *jobs.Worker, *jobs.Job) error
	}{
		{"canceled-queued", jobs.StateCanceled, jobs.ErrCodeCanceled, 0, nil},
		{"canceled-running", jobs.StateCanceled, jobs.ErrCodeCanceled, 0, func(ctx context.Context, ta *testutil.TestApp, w *jobs.Worker, child *jobs.Job) error {
			if _, err := ta.Deps.Jobs.Cancel(ctx, child.ID); err != nil {
				return err
			}
			_, err := ta.Deps.Jobs.Fail(ctx, child.ID, w.ID, jobs.JobError{Code: jobs.ErrCodeCanceled, Message: "canceled"}, jobs.Cost{})
			return err
		}},
		{"timeout", jobs.StateFailed, jobs.ErrCodeTimeout, 1, func(ctx context.Context, ta *testutil.TestApp, w *jobs.Worker, child *jobs.Job) error {
			_, err := ta.Deps.Jobs.Fail(ctx, child.ID, w.ID, jobs.JobError{Code: jobs.ErrCodeTimeout, Message: "private worker detail", Retryable: true}, jobs.Cost{})
			return err
		}},
		{"oom", jobs.StateFailed, jobs.ErrCodeOOM, 1, func(ctx context.Context, ta *testutil.TestApp, w *jobs.Worker, child *jobs.Job) error {
			_, err := ta.Deps.Jobs.Fail(ctx, child.ID, w.ID, jobs.JobError{Code: jobs.ErrCodeOOM, Message: "private worker detail"}, jobs.Cost{})
			return err
		}},
		{"internal", jobs.StateFailed, ai.CodeUpstream, 1, func(ctx context.Context, ta *testutil.TestApp, w *jobs.Worker, child *jobs.Job) error {
			_, err := ta.Deps.Jobs.Fail(ctx, child.ID, w.ID, jobs.JobError{Code: jobs.ErrCodeInternal, Message: "private worker detail"}, jobs.Cost{})
			return err
		}},
		{"invalid-output", jobs.StateFailed, ai.CodeBadOutput, 1, func(ctx context.Context, ta *testutil.TestApp, w *jobs.Worker, child *jobs.Job) error {
			_, err := ta.Deps.Jobs.Complete(ctx, child.ID, w.ID, map[string]any{"scores": []float64{.95}, "outfit_scores": []float64{.93}, "method": "mock"}, jobs.Cost{})
			return err
		}},
		{"progress", jobs.StateDone, "", 0, func(ctx context.Context, ta *testutil.TestApp, w *jobs.Worker, child *jobs.Job) error {
			if _, err := ta.Deps.Jobs.Progress(ctx, child.ID, w.ID, jobs.ProgressUpdate{Progress: .37, Step: 37, Steps: 100}); err != nil {
				return err
			}
			for deadline := time.Now().Add(5 * time.Second); ; {
				parent, err := ta.Deps.Jobs.Get(ctx, child.ParentID)
				if err != nil {
					return err
				}
				if math.Abs(parent.Progress-.37) < 1e-6 {
					break
				}
				if time.Now().After(deadline) {
					return errors.New("parent progress stayed at " + jsonText(parent.Progress) + " while the worker reported 37%")
				}
				time.Sleep(20 * time.Millisecond)
			}
			_, err := ta.Deps.Jobs.Complete(ctx, child.ID, w.ID, dinoOutput, jobs.Cost{})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				f, _, _ := controlPages(t, ta, 1)
				w := lockWorkerSetup(t, ta, jobs.Registration{Name: "Scoring fixture", Backends: []string{"vision"}, JobTypes: []string{"character.score"}, Models: []string{dinoModel}})
				control := core.MustUse[gen.ControlService](ta.Deps)
				c, err := f.studio.GetCharacter(ctx, f.characters[0].ID)
				if err != nil {
					t.Fatal(err)
				}
				parent, err := control.EnqueueScoring(ctx, f.actor, f.project.ID, c.ID, c.UpdatedAt)
				if err != nil {
					t.Fatal(err)
				}
				var worked chan error
				if tc.work == nil {
					// Cancel the child before any worker claims it.
					worked = make(chan error, 1)
					go func() {
						for ctx.Err() == nil {
							var id string
							if err := ta.Deps.DB.Get(ctx, &id, `SELECT id FROM jobs WHERE parent_id=?`, parent.ID); err == nil {
								_, err = ta.Deps.Jobs.Cancel(ctx, id)
								worked <- err
								return
							}
							time.Sleep(10 * time.Millisecond)
						}
						worked <- ctx.Err()
					}()
				} else {
					worked = workChild(ctx, ta, w, "character.score", dinoModel, func(child *jobs.Job) error { return tc.work(ctx, ta, w, child) })
				}
				done := runArtJob(t, ta, parent.ID)
				if err := <-worked; err != nil {
					t.Fatal(err)
				}
				if done.State != tc.state {
					t.Fatalf("parent state = %s, want %s (%+v)", done.State, tc.state, done.Error)
				}
				if tc.code != "" && (done.Error == nil || done.Error.Code != tc.code) {
					t.Fatalf("parent error = %+v, want code %s", done.Error, tc.code)
				}
				if done.Error != nil && (strings.Contains(done.Error.Message, "private worker detail") || strings.Contains(done.Error.Message, "target changed")) {
					t.Fatalf("parent error message = %q", done.Error.Message)
				}
				if tc.state == jobs.StateDone && math.Abs(done.Progress-1) > 1e-9 {
					t.Fatalf("finished assessment progress = %v", done.Progress)
				}
				if got := jobFailedNotices(t, ta, f.actor.UserID); tc.notices == 0 && got != 0 || tc.notices > 0 && got == 0 {
					t.Fatalf("job_failed notifications = %d, want %d", got, tc.notices)
				}
			})
		})
	}
}

// Training follows its worker child too, and a rejected training artifact has
// its own clear code instead of "target changed".
func TestCharacterTrainingRejectsArtifactsWithItsOwnCode(t *testing.T) {
	for _, tc := range []struct{ name, state, code string }{
		{"rejected-artifact", jobs.StateFailed, gen.ErrCodeArtifactRejected},
		{"foreign-key", jobs.StateFailed, gen.ErrCodeArtifactRejected},
		{"canceled", jobs.StateCanceled, jobs.ErrCodeCanceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				f, _, _ := controlPages(t, ta, 1)
				store := core.MustUse[*assets.Service](ta.Deps)
				for i := range 3 {
					a, err := store.Save(ctx, assets.SaveParams{OwnerID: f.actor.UserID, ProjectID: f.project.ID, Kind: "reference", Source: assets.SourceUpload, Data: imagePNG(t, 120+i*8, 180)})
					if err != nil {
						t.Fatal(err)
					}
					if err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
						_, err := f.studio.AddCharacterRefTx(ctx, tx, f.characters[0].ID, a.ID, "expression", "Kept view")
						return err
					}); err != nil {
						t.Fatal(err)
					}
				}
				w := lockWorkerSetup(t, ta, jobs.Registration{Name: "Training fixture", Backends: []string{"diffusers"}, JobTypes: []string{"character.train"}, Models: []string{"sdxl-base"}})
				control := core.MustUse[gen.ControlService](ta.Deps)
				c, err := f.studio.GetCharacter(ctx, f.characters[0].ID)
				if err != nil {
					t.Fatal(err)
				}
				parent, err := control.EnqueueTraining(ctx, f.actor, f.project.ID, c.ID, c.UpdatedAt)
				if err != nil {
					t.Fatal(err)
				}
				worked := workChild(ctx, ta, w, "character.train", "sdxl-base", func(child *jobs.Job) error {
					if tc.name == "canceled" {
						if _, err := ta.Deps.Jobs.Cancel(ctx, child.ID); err != nil {
							return err
						}
						_, err := ta.Deps.Jobs.Fail(ctx, child.ID, w.ID, jobs.JobError{Code: jobs.ErrCodeCanceled, Message: "canceled"}, jobs.Cost{})
						return err
					}
					key := "projects/elsewhere/lora.safetensors"
					if tc.name == "rejected-artifact" {
						// A mock artifact with plausible metadata is still not a real LoRA.
						a, err := store.Save(ctx, assets.SaveParams{OwnerID: f.actor.UserID, ProjectID: f.project.ID, Kind: "character_lora", Source: assets.SourceWorker, ContentType: "application/octet-stream", Data: []byte("mock safetensors payload"), Meta: map[string]any{"base_model": "sdxl-base", "trainer": "peft-sdxl"}, Prefix: "jobs/" + child.ID})
						if err != nil {
							return err
						}
						key = a.Key
					}
					_, err := ta.Deps.Jobs.Complete(ctx, child.ID, w.ID, map[string]any{"lora": map[string]string{"key": key}}, jobs.Cost{})
					return err
				})
				done := runArtJob(t, ta, parent.ID)
				if err := <-worked; err != nil {
					t.Fatal(err)
				}
				if done.State != tc.state || done.Error == nil || done.Error.Code != tc.code {
					t.Fatalf("training = %s %+v, want %s/%s", done.State, done.Error, tc.state, tc.code)
				}
				status, err := control.LockStatus(ctx, f.actor, f.project.ID, c.ID)
				if err != nil || status.Status == "ready" {
					t.Fatalf("rejected training reported ready: %+v %v", status, err)
				}
				if got := jobFailedNotices(t, ta, f.actor.UserID); tc.state == jobs.StateCanceled && got != 0 {
					t.Fatalf("canceled training sent %d failure notifications", got)
				}
			})
		})
	}
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
