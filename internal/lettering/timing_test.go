package lettering

import (
	"slices"
	"testing"
	"time"
)

// quietSmallLayout is a generous figure for one small English bubble on an
// idle machine (about 100 µs measured on an M-series laptop).
const quietSmallLayout = 250 * time.Microsecond

// timeBound scales a wall-clock bound by the race detector and by how loaded
// the machine is right now. The load is measured by timing a small layout:
// when the whole suite runs in parallel every layout slows down alike, so a
// bound relative to that keeps catching real regressions (which the work
// counters catch anyway) without failing on scheduling noise.
func timeBound(t *testing.T, e Engine, quiet time.Duration) time.Duration {
	t.Helper()
	samples := make([]time.Duration, 7)
	for i := range samples {
		start := time.Now()
		if _, err := e.Layout(testBubble(), "en"); err != nil {
			t.Fatal(err)
		}
		samples[i] = time.Since(start)
	}
	slices.Sort(samples)
	load := min(max(float64(samples[len(samples)/2])/float64(quietSmallLayout), 1), 20)
	bound := time.Duration(float64(quiet) * raceSlowdown * load)
	if load > 1 {
		t.Logf("machine is loaded (small layout %v): bound %v", samples[len(samples)/2], bound)
	}
	return bound
}
