package gen_test

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func waitProgress(ctx context.Context, ta *testutil.TestApp, id string, want float64) error {
	for deadline := time.Now().Add(5 * time.Second); ; {
		j, err := ta.Deps.Jobs.Get(ctx, id)
		if err != nil {
			return err
		}
		if math.Abs(j.Progress-want) < 1e-6 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("parent progress %v (step %d/%d), want %v", j.Progress, j.Step, j.Steps, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// An assessment with more than eight targets reports every target as a step
// before its first worker child, and mirrors each batch onto the whole job:
// the first batch's 50% is 4/9 of the assessment, never 50%.
func TestMultiBatchAssessmentProgressCoversTheWholeJob(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		f, _, _ := controlPages(t, ta, 9)
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
		worked := make(chan error, 1)
		go func() {
			worked <- func() error {
				for batch, start := range []int{0, 8} {
					var child *jobs.Job
					for child == nil {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						var err error
						if child, err = ta.Deps.Jobs.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorWorker, WorkerID: w.ID, Types: []string{"character.score"}, Models: []string{dinoModel}, MaxRunning: 1}); err != nil {
							return err
						}
						if child == nil {
							time.Sleep(10 * time.Millisecond)
						}
					}
					p, err := ta.Deps.Jobs.Get(ctx, parent.ID)
					if err != nil {
						return err
					}
					if p.Steps != 9 || math.Abs(p.Progress-float64(start)/9) > 1e-6 {
						return fmt.Errorf("batch %d dispatched with parent %v at step %d/%d", batch, p.Progress, p.Step, p.Steps)
					}
					count := 0
					for name := range child.Files {
						if strings.HasPrefix(name, "candidate_") {
							count++
						}
					}
					if _, err := ta.Deps.Jobs.Progress(ctx, child.ID, w.ID, jobs.ProgressUpdate{Progress: .5}); err != nil {
						return err
					}
					if err := waitProgress(ctx, ta, parent.ID, (float64(start)+.5*float64(count))/9); err != nil {
						return fmt.Errorf("batch %d: %w", batch, err)
					}
					scores, outfits := make([]float64, count), make([]float64, count)
					for i := range scores {
						scores[i], outfits[i] = .95, .93
					}
					if _, err := ta.Deps.Jobs.Complete(ctx, child.ID, w.ID, map[string]any{"scores": scores, "outfit_scores": outfits, "method": dinoOutput["method"]}, jobs.Cost{}); err != nil {
						return err
					}
				}
				return nil
			}()
		}()
		done := runArtJob(t, ta, parent.ID)
		if err := <-worked; err != nil {
			t.Fatal(err)
		}
		if done.State != jobs.StateDone {
			t.Fatalf("assessment: %+v", done.Error)
		}
	})
}

// progressImages records the parent job's measured progress at every image
// call of a page or repair job.
type progressImages struct {
	fakeImages
	ta     *testutil.TestApp
	mu     sync.Mutex
	parent string
	seen   []jobs.Job
}

func (p *progressImages) GenerateImage(ctx context.Context, r ai.ImageGenerateRequest) (ai.ImageResult, error) {
	p.mu.Lock()
	if j, err := p.ta.Deps.Jobs.Get(ctx, p.parent); err == nil {
		p.seen = append(p.seen, *j)
	}
	p.mu.Unlock()
	return p.fakeImages.GenerateImage(ctx, r)
}

// A multi-panel repair reports all of its component calls (plus the commit)
// as steps before the first call, and its bar never restarts at a panel.
func TestMultiPanelControlArtProgressNeverRestartsAtAPanel(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, _, pages := controlPages(t, ta, 2)
		fake := &progressImages{ta: ta}
		gen.SetImageServiceForTest(f.gen, fake)
		controls := core.MustUse[gen.ControlService](ta.Deps)
		req := gen.RepairRequest{ProjectID: f.project.ID, ChapterID: f.chapter.ID, CharacterID: f.characters[0].ID}
		for _, p := range pages {
			req.Targets = append(req.Targets, gen.RepairTarget{PageID: p.ID, PanelID: p.Panels[0].ID, LayerID: p.Panels[0].Layers[1].ID, Version: p.Version})
		}
		j, err := controls.EnqueueRepair(ctx, f.actor, req)
		if err != nil {
			t.Fatal(err)
		}
		fake.parent = j.ID
		if done := runArtJob(t, ta, j.ID); done.State != jobs.StateDone {
			t.Fatalf("repair: %+v", done.Error)
		}
		if len(fake.seen) != 2 {
			t.Fatalf("image calls = %d", len(fake.seen))
		}
		for i, seen := range fake.seen {
			if seen.Steps != 3 || seen.Step != i || math.Abs(seen.Progress-float64(i)/3) > 1e-9 {
				t.Errorf("call %d saw parent step %d/%d at %v; want %d/3", i, seen.Step, seen.Steps, seen.Progress, i)
			}
		}
	})
}
