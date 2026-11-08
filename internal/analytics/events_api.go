package analytics

import (
	"errors"
	"net/http"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
)

// Batch bounds for POST /api/v1/analytics/events.
const (
	MaxBatchEvents = 50
	maxBatchBytes  = 32 << 10
)

type eventBatch struct {
	Events []ReadEvent `json:"events"`
}

// batchResult reports how many events were stored or counted (accepted) and
// how many were dropped (invalid, not readable or over the rate limit),
// without saying which, so the endpoint is no oracle for unreadable IDs.
type batchResult struct {
	Accepted int `json:"accepted"`
	Dropped  int `json:"dropped"`
}

// postEvents is POST /api/v1/analytics/events for the reader island:
// {"events":[{"series_id","publication_id","kind","page","ms"}…]} with at most
// MaxBatchEvents events. Anonymous visitors and signed-in browser sessions may
// post; bearer tokens may not (agents do not read). It answers 202 with
// batchResult, or 429 when every event was over the rate limit or the client
// may not start another anonymous reading session.
func (m *Module) postEvents(w http.ResponseWriter, r *http.Request) error {
	if core.TokenFrom(r.Context()) != nil {
		return core.ErrForbidden("reading events come from the reader in a browser")
	}
	var in eventBatch
	if err := httpx.DecodeJSONLimit(w, r, &in, maxBatchBytes); err != nil {
		return err
	}
	if len(in.Events) == 0 || len(in.Events) > MaxBatchEvents {
		return core.ErrBadRequest("send between 1 and 50 events")
	}
	var v publish.Viewer
	if u := core.UserFrom(r.Context()); u != nil && u.Status == "active" {
		v = publish.Viewer{UserID: u.ID, Admin: u.IsAdmin()}
	}
	// Members are counted by account and need no reading session. An
	// anonymous request without a server-minted session starts one only
	// within the per-address limit: a client that drops (or makes up) its
	// cookie would otherwise be a new reader, and a new read, every time.
	session := ""
	if v.UserID == "" {
		id, ok := m.sessions.existing(r)
		if !ok {
			if allowed, wait := m.sessions.allowNew(r); !allowed {
				return httpx.ErrRateLimited(wait)
			}
			id = m.sessions.mint(w, r)
		}
		session = id
	}
	cache := map[readKey]bool{}
	var res batchResult
	limited := 0
	for _, e := range in.Events {
		err := m.rec.record(r.Context(), v, session, e, cache)
		switch {
		case err == nil:
			res.Accepted++
		case errors.Is(err, ErrRateLimited):
			limited++
			res.Dropped++
		case errors.Is(err, ErrInvalidEvent), errors.Is(err, ErrNotReadable):
			res.Dropped++
		default:
			m.d.Log.WarnContext(r.Context(), "analytics: record event", "err", err)
			res.Dropped++
		}
	}
	if limited == len(in.Events) {
		return httpx.ErrRateLimited(time.Second)
	}
	core.JSON(w, http.StatusAccepted, res)
	return nil
}
