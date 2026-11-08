package analytics

import (
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
)

// Test hooks for the external analytics_test package (it cannot be an
// internal test: testutil wires the whole app, which imports this package).

// SetClock replaces the recorder's clock.
func SetClock(r Recorder, now func() time.Time) { r.(*recorder).now = now }

// SetLimit replaces the recorder's per-viewer rate limiter.
func SetLimit(r Recorder, rate float64, burst int, now func() time.Time) {
	l := core.NewLimiter(rate, burst)
	l.SetClock(now)
	r.(*recorder).limiter = l
}

// SetNewSessionLimit replaces the per-address new-session limiter.
func SetNewSessionLimit(s ReadingSessions, rate float64, burst int) {
	s.(*sessions).limiter = core.NewLimiter(rate, burst)
}

// HashSession is the stored hash of an anonymous session ID on t's UTC day.
func HashSession(r Recorder, sessionID string, t time.Time) string {
	return r.(*recorder).sessionHash(sessionID, t)
}
