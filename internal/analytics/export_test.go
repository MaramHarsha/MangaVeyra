package analytics

import (
	"context"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
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

// NewTestRecorder is another recorder over the same database, like a second
// server process (or this one after a restart).
func NewTestRecorder(d *db.DB, key []byte, pub publish.Service) Recorder {
	r := newRecorder(d, key)
	r.publish = pub
	return r
}

// PurgeDaySalts runs the background loop's deletion of finished days' salts.
func PurgeDaySalts(r Recorder) error { return r.(*recorder).purgeSalts(context.Background()) }

// HashSession is the stored hash of an anonymous session ID on t's UTC day
// ("" when the day's salt cannot be read).
func HashSession(r Recorder, sessionID string, t time.Time) string {
	h, err := r.(*recorder).sessionHash(context.Background(), sessionID, t)
	if err != nil {
		return ""
	}
	return h
}
