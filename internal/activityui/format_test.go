package activityui

import (
	"context"
	"math"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

func TestSpendUsesTheDesignedTwoDecimals(t *testing.T) {
	for in, want := range map[float64]string{0: "$0.00", -1: "$0.00", math.NaN(): "$0.00", math.Inf(1): "$0.00",
		0.000123: "<$0.01", 0.0049: "<$0.01", 0.005: "$0.01", 0.24: "$0.24", 4.18: "$4.18", 4.181234: "$4.18"} {
		if got := spend(in); got != want {
			t.Errorf("spend(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestJobTimesReadLikeTheDesign(t *testing.T) {
	en, ja := i18n.WithLocale(context.Background(), "en"), i18n.WithLocale(context.Background(), "ja")
	for in, want := range map[float64]string{0: "1 s", 0.2: "1 s", 38: "38 s", 120: "2 min", 179: "3 min", 3600: "1 h 0 m", 3900: "1 h 5 m"} {
		if got := shortDuration(en, in); got != want {
			t.Errorf("shortDuration(%v) = %q, want %q", in, got, want)
		}
	}
	if got := shortDuration(ja, 38); got != "38秒" {
		t.Errorf("ja seconds = %q", got)
	}
	if got := costText(en, jobs.Cost{GPUSeconds: 180}, true); got != "~3 min" {
		t.Errorf("GPU estimate = %q", got)
	}
	if got := costText(en, jobs.Cost{USD: .24}, true); got != "~$0.24" {
		t.Errorf("spend estimate = %q", got)
	}
	if got := costText(en, jobs.Cost{GPUSeconds: 120}, false); got != "2 min" {
		t.Errorf("recorded GPU time = %q", got)
	}
	if got := gpuTime(en, 3*3600+600); got != "3 h 10 m" {
		t.Errorf("weekly GPU = %q", got)
	}
}

func TestFoldedProgressFollowsTheRunningWorkerChild(t *testing.T) {
	parent := &jobs.Job{State: jobs.StateRunning}
	child := &jobs.Job{State: jobs.StateRunning, Progress: .62}
	if got := percent(foldedProgress(parent, []*jobs.Job{child})); got != 62 {
		t.Fatalf("single step parent = %d, want the child's 62", got)
	}
	// Two of four steps done (50%); the next step's child at 50% adds at
	// most a step's share, never running ahead of the measured work.
	parent = &jobs.Job{State: jobs.StateRunning, Progress: .5, Step: 2, Steps: 4}
	child.Progress = .5
	if got := percent(foldedProgress(parent, []*jobs.Job{child})); got != 60 {
		t.Fatalf("multi-step parent = %d, want 60", got)
	}
	if got := percent(foldedProgress(parent, []*jobs.Job{{State: jobs.StateQueued}})); got != 50 {
		t.Fatalf("a queued child added progress: %d", got)
	}
	// Training mirrors its child's progress and counters into the parent.
	mirrored := &jobs.Job{State: jobs.StateRunning, Progress: .7, Step: 70, Steps: 100}
	if got := percent(foldedProgress(mirrored, []*jobs.Job{{State: jobs.StateRunning, Progress: .7, Step: 70, Steps: 100}})); got != 70 {
		t.Fatalf("mirrored parent = %d, want 70", got)
	}
	if got := percent(foldedProgress(&jobs.Job{State: jobs.StateRunning, Progress: .7}, []*jobs.Job{{State: jobs.StateRunning, Progress: .7}})); got != 70 {
		t.Fatalf("parent measuring without steps = %d, want 70", got)
	}
	if got := percent(foldedProgress(&jobs.Job{State: jobs.StateRunning, Progress: math.NaN()}, nil)); got != 0 {
		t.Fatalf("NaN progress = %d", got)
	}
}

func TestJobLabelsNeverShowRawTypes(t *testing.T) {
	ctx := i18n.WithLocale(context.Background(), "en")
	for typ, want := range map[string]string{"page.generate": "Page art", "character.lock_score": "Character assessment",
		"character.score": "Character assessment", "character.repair": "Character repair", "export.pdf": "Export chapter",
		"image.generate": "Worker job", "future.kind": "Job"} {
		if got := i18n.T(ctx, jobLabelKey(typ, "")); got != want {
			t.Errorf("%s = %q, want %q", typ, got, want)
		}
	}
	for s, want := range map[string]bool{"page.generate": true, "character.lock_score": true, "Tidebreak Courier · Page art": false, "t": false, "Notifications work. BA-DUM!": false} {
		if looksLikeJobType(s) != want {
			t.Errorf("looksLikeJobType(%q) = %v", s, !want)
		}
	}
}
