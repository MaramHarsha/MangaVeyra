package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
)

func slotWorkerFixture(t *testing.T, d *db.DB) (*Module, *aiDBFixture, ImagePlan, string, string) {
	t.Helper()
	ctx := context.Background()
	m, f := imageTestModule(t, d, &queuedDoer{})
	w, err := m.d.Jobs.RegisterWorker(ctx, "slot-test-token", "127.0.0.1", jobs.Registration{Name: "slot-test", Backends: []string{"mock"}, Models: []string{"sdxl-base"}, JobTypes: []string{"image.generate"}, GPUs: []jobs.GPU{{Name: "test", VRAMTotalMB: 8000}}, MaxConcurrent: 2})
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.image.PlanImage(ctx, ImagePlanRequest{Task: TaskPanel, ActorID: f.userID, ProjectID: f.projectID,
		RoutingMode: ModeFreeOnly, PinModelID: "sdxl-base", Needs: ImageNeeds{Width: 64, Height: 64, Count: 1}})
	if err != nil {
		t.Fatal(err)
	}
	id := imageTestJob(t, m, f, p)
	input := map[string]any{"version": 1, "plan": map[string]string{"task": TaskPanel}, "components": []any{
		map[string]string{"prompt": "Background component only"}, map[string]string{"prompt": "Effects component only"}}}
	if _, err := d.Exec(ctx, `UPDATE jobs SET input = ? WHERE id = ?`, db.MustJSON(input), id); err != nil {
		t.Fatal(err)
	}
	return m, f, p, w.ID, id
}

func TestImageWorkerComponentSlotsReuseWithoutDuplicateDispatch(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		m, f, plan, workerID, parentID := slotWorkerFixture(t, d)
		data := imageTestPNG(t, 64, 64, color.NRGBA{90, 100, 110, 255})
		done := make(chan error, 1)
		go func() {
			until := time.Now().Add(5 * time.Second)
			for completed := 0; completed < 2 && time.Now().Before(until); {
				child, err := m.d.Jobs.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorWorker, Types: []string{"image.generate"}, Models: []string{"sdxl-base"}, WorkerID: workerID, MaxRunning: 2})
				if err != nil {
					done <- err
					return
				}
				if child == nil {
					time.Sleep(10 * time.Millisecond)
					continue
				}
				if child.ParentID != parentID || child.MaxAttempts != 1 {
					done <- fmt.Errorf("invalid child envelope: %+v", child)
					return
				}
				a := assets.New(d, m.d.Store, m.d.Media)
				asset, err := a.Save(ctx, assets.SaveParams{OwnerID: f.userID, ProjectID: f.projectID, Kind: "job_artifact", Source: assets.SourceWorker, Prefix: "jobs/" + child.ID, Data: data})
				if err != nil {
					done <- err
					return
				}
				if _, err = m.d.Jobs.Complete(ctx, child.ID, workerID, map[string]any{"images": []any{map[string]any{"key": asset.Key, "seed": completed + 1}}}, jobs.Cost{GPUSeconds: 2.5}); err != nil {
					done <- err
					return
				}
				completed++
				if completed == 2 {
					done <- nil
					return
				}
			}
			done <- fmt.Errorf("two component children were not completed")
		}()
		for slot, prompt := range []string{"Background component only", "Effects component only"} {
			req := imageTestRequest(f, plan, parentID)
			req.CallSlot, req.Prompt, req.Seed = slot, prompt, int64(42+slot)
			first, err := m.image.GenerateImage(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if slot == 0 {
				// Old children have no _call_slot; they must still represent zero.
				var child struct{ ID, Input string }
				if err := d.Get(ctx, &child, `SELECT id, input FROM jobs WHERE parent_id = ?`, parentID); err != nil {
					t.Fatal(err)
				}
				var input map[string]any
				if err := json.Unmarshal([]byte(child.Input), &input); err != nil {
					t.Fatal(err)
				}
				delete(input, "_call_slot")
				if _, err := d.Exec(ctx, `UPDATE jobs SET input = ? WHERE id = ?`, db.MustJSON(input), child.ID); err != nil {
					t.Fatal(err)
				}
			}
			again, err := m.image.GenerateImage(ctx, req)
			if err != nil || len(again.Images) != 1 || !bytes.Equal(first.Images[0].Data, again.Images[0].Data) {
				t.Fatalf("slot %d did not reuse its child: %v", slot, err)
			}
			req.Seed++
			if _, err := m.image.GenerateImage(ctx, req); err == nil {
				t.Fatalf("slot %d accepted a changed request digest", slot)
			}
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		var count int
		if err := d.Get(ctx, &count, `SELECT COUNT(*) FROM jobs WHERE parent_id = ?`, parentID); err != nil || count != 2 {
			t.Fatalf("duplicate component dispatch: %d %v", count, err)
		}
	})
}

type slotCountingStore struct {
	storage.Store
	puts atomic.Int64
}

func (s *slotCountingStore) Put(ctx context.Context, key string, data io.Reader, size int64, contentType string) error {
	s.puts.Add(1)
	return s.Store.Put(ctx, key, data, size, contentType)
}

func TestImageWorkerRejectsUndeclaredSlotsBeforeInputStorage(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		m, f, plan, _, parentID := slotWorkerFixture(t, d)
		store := &slotCountingStore{Store: m.d.Store}
		m.d.Store = store
		req := imageTestRequest(f, plan, parentID)
		req.Prompt = "Effects component only"
		// Direct bridge calls include a valid reference so a late-only guard
		// would perform observable storage writes before rejecting the slot.
		req.References = []ImageRef{{Data: imageTestPNG(t, 64, 64, color.NRGBA{A: 255}), ContentType: "image/png", Role: "reference"}}
		for _, slot := range []int{-1, 2, 15, 16} {
			req.CallSlot = slot
			if _, err := m.image.workerImage(ctx, req); err == nil {
				t.Fatalf("undeclared slot %d accepted", slot)
			}
		}
		req.CallSlot = 1
		for _, input := range []any{map[string]any{}, map[string]any{"version": 2},
			map[string]any{"version": 1, "plan": map[string]string{"task": TaskRegionEdit}, "components": []any{map[string]string{"prompt": "first"}, map[string]string{"prompt": req.Prompt}}}} {
			if _, err := d.Exec(ctx, `UPDATE jobs SET input = ? WHERE id = ?`, db.MustJSON(input), parentID); err != nil {
				t.Fatal(err)
			}
			if _, err := m.image.workerImage(ctx, req); err == nil {
				t.Fatal("untyped/mismatched parent accepted a second slot")
			}
		}
		if store.puts.Load() != 0 {
			t.Fatal("invalid slot wrote input files")
		}
		var count int
		if err := d.Get(ctx, &count, `SELECT COUNT(*) FROM jobs WHERE parent_id = ?`, parentID); err != nil || count != 0 {
			t.Fatalf("invalid slot created child: %d %v", count, err)
		}
	})
}

func TestImageWorkerComponentSlotsKeepAttemptAndLocalOnlyFences(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		m, f, plan, _, parentID := slotWorkerFixture(t, d)
		req := imageTestRequest(f, plan, parentID)
		req.CallSlot, req.Prompt, req.Attempt = 1, "Effects component only", 2
		if _, err := m.image.GenerateImage(ctx, req); err == nil {
			t.Fatal("stale parent attempt accepted")
		}
		req.Attempt = 1
		if _, err := f.settings.UpdateInstance(ctx, f.userID, func(i *settings.Instance) error { i.LocalOnly = true; return nil }); err != nil {
			t.Fatal(err)
		}
		if _, err := m.image.GenerateImage(ctx, req); err == nil {
			t.Fatal("Local only accepted a worker component")
		}
		var count int
		if err := d.Get(ctx, &count, `SELECT COUNT(*) FROM jobs WHERE parent_id = ?`, parentID); err != nil || count != 0 {
			t.Fatalf("fenced component created child: %d %v", count, err)
		}
	})
}

func TestImageWorkerCompletedChildActualSurvivesParentFence(t *testing.T) {
	for _, scenario := range []string{"parent_cancel", "context_cancel", "stale_attempt"} {
		t.Run(scenario, func(t *testing.T) {
			forEachAIDialect(t, func(t *testing.T, d *db.DB) {
				ctx := context.Background()
				m, f, plan, workerID, parentID := slotWorkerFixture(t, d)
				requestCtx, cancel := context.WithCancel(ctx)
				defer cancel()
				data := imageTestPNG(t, 64, 64, color.NRGBA{90, 100, 110, 255})
				done := make(chan error, 1)
				go func() {
					until := time.Now().Add(5 * time.Second)
					for time.Now().Before(until) {
						child, err := m.d.Jobs.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorWorker, Types: []string{"image.generate"}, Models: []string{"sdxl-base"}, WorkerID: workerID, MaxRunning: 2})
						if err != nil {
							done <- err
							return
						}
						if child == nil {
							time.Sleep(10 * time.Millisecond)
							continue
						}
						a := assets.New(d, m.d.Store, m.d.Media)
						asset, err := a.Save(ctx, assets.SaveParams{OwnerID: f.userID, ProjectID: f.projectID, Kind: "job_artifact", Source: assets.SourceWorker, Prefix: "jobs/" + child.ID, Data: data})
						if err != nil {
							done <- err
							return
						}
						if _, err = m.d.Jobs.Complete(ctx, child.ID, workerID, map[string]any{"images": []any{map[string]any{"key": asset.Key, "seed": 1}}}, jobs.Cost{GPUSeconds: 2.5}); err != nil {
							done <- err
							return
						}
						switch scenario {
						case "parent_cancel":
							_, err = m.d.Jobs.Cancel(ctx, parentID)
						case "context_cancel":
							cancel()
						case "stale_attempt":
							_, err = d.Exec(ctx, `UPDATE jobs SET attempts = attempts + 1 WHERE id = ?`, parentID)
						}
						done <- err
						return
					}
					done <- fmt.Errorf("child was not completed")
				}()
				req := imageTestRequest(f, plan, parentID)
				req.CallSlot, req.Prompt = 1, "Effects component only"
				result, err := m.image.GenerateImage(requestCtx, req)
				var generated *GenerateError
				if len(result.Images) != 0 || !errors.As(err, &generated) || generated.Actual.GPUSeconds != 2.5 {
					t.Fatalf("fence lost observed worker cost or returned stale pixels: %+v %+v", result, err)
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestImageWorkerParentCancelCleansPendingSlots(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		m, f, plan, _, parentID := slotWorkerFixture(t, d)
		results := make(chan error, 2)
		for slot, prompt := range []string{"Background component only", "Effects component only"} {
			req := imageTestRequest(f, plan, parentID)
			req.CallSlot, req.Prompt = slot, prompt
			go func() {
				_, err := m.image.GenerateImage(ctx, req)
				results <- err
			}()
		}
		until := time.Now().Add(5 * time.Second)
		for {
			var count int
			if err := d.Get(ctx, &count, `SELECT COUNT(*) FROM jobs WHERE parent_id = ? AND state = 'queued'`, parentID); err != nil {
				t.Fatal(err)
			}
			if count == 2 {
				break
			}
			if time.Now().After(until) {
				t.Fatal("two pending slots were not created")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if _, err := m.d.Jobs.Cancel(ctx, parentID); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			select {
			case err := <-results:
				if err == nil {
					t.Fatal("canceled parent returned image results")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("pending slot ignored parent cancellation")
			}
		}
		var canceled int
		if err := d.Get(ctx, &canceled, `SELECT COUNT(*) FROM jobs WHERE parent_id = ? AND state = 'canceled'`, parentID); err != nil || canceled != 2 {
			t.Fatalf("pending children leaked: canceled=%d %v", canceled, err)
		}
	})
}
