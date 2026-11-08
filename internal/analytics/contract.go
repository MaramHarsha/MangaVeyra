package analytics

// Frozen Phase 3 contract (docs/plans/0010-phase-3-publish-and-read.md, ADR 0012).
//
// Analytics owns read_events. The reader records events only through Recorder;
// nothing leaves the server, no IP address or fingerprint is stored, and
// anonymous readers are counted by a salted session hash that rotates daily.

import (
	"context"
	"net/http"

	"github.com/MaramHarsha/MangaVeyra/internal/publish"
)

// Read event kinds (read_events.kind).
const (
	EventOpen     = "open"     // a chapter was opened
	EventPage     = "page"     // a page was shown for at least the dwell threshold
	EventComplete = "complete" // the last page was reached
	EventDrop     = "drop"     // the reader left before the last page
)

// ReadEvent is one reading event from the reader island or server pages.
type ReadEvent struct {
	SeriesID      string `json:"series_id"`
	PublicationID string `json:"publication_id"`
	Kind          string `json:"kind"`
	Page          int    `json:"page"`
	MS            int    `json:"ms"` // dwell time on the page, clamped server-side
}

// Recorder stores reading events. SessionID is the reader's first-party
// session identifier; implementations store only a salted, daily-rotated hash
// of it for anonymous viewers and never the raw value. Record must verify with
// publish.Service.CanRead that the viewer may read the publication, and must be
// cheap and rate limited: a failure never breaks reading.
type Recorder interface {
	Record(ctx context.Context, v publish.Viewer, sessionID string, e ReadEvent) error
}

// ReadingSessions hands out the first-party reading sessions of anonymous
// visitors (added during implementation, ADR 0012 §8). The reader asks it for
// the session of a server-rendered open, so the open and the island's batches
// share one daily hash, and every new session goes through the same
// per-address limit as the events endpoint.
type ReadingSessions interface {
	// Session returns the request's session ID from a valid, server-minted
	// SessionCookie. Without one it starts a new session and sets the cookie
	// when the client address may start another (NewSessionRate,
	// NewSessionBurst); otherwise ok is false and the caller records nothing.
	Session(w http.ResponseWriter, r *http.Request) (id string, ok bool)
}
