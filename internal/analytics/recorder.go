package analytics

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
)

// Event bounds. Dwell time is clamped, never rejected: a tab left open on one
// page must not count as hours of reading.
const (
	// MaxPage is the highest page number an event may name.
	MaxPage = 2000
	// MaxDwellMS clamps a page event's dwell time (ten minutes).
	MaxDwellMS = 10 * 60 * 1000
	// MaxSessionID bounds the first-party session identifier.
	MaxSessionID = 128
	// SessionRate and SessionBurst limit events per viewer (members by user,
	// anonymous visitors by session hash): one per second sustained, bursts of
	// 200 (four full batches).
	SessionRate  = 1.0
	SessionBurst = 200
	// NewSessionRate and NewSessionBurst limit how many anonymous reading
	// sessions one client address may start: 20 at once, then one a minute.
	NewSessionRate  = 1.0 / 60
	NewSessionBurst = 20
)

// Recorder errors. They never reach readers as failures of the page itself.
var (
	// ErrInvalidEvent marks an event with an unknown kind, a malformed ID or an
	// out-of-range page, or an anonymous event without a session.
	ErrInvalidEvent = errors.New("analytics: invalid event")
	// ErrNotReadable means the viewer may not read the publication (or it does
	// not exist); the event is dropped without revealing which.
	ErrNotReadable = errors.New("analytics: publication not readable")
	// ErrRateLimited means the viewer sent more events than SessionRate allows.
	ErrRateLimited = errors.New("analytics: rate limited")
)

// sessionPurpose names the server-secret-derived HMAC key for session hashes.
const sessionPurpose = "analytics-session-hash-v1"

// recorder implements Recorder over read_events (ADR 0012 §8).
//
// Members are stored by user_id with an empty session_hash. Anonymous visitors
// are stored only by session_hash = HMAC-SHA256(HMAC-SHA256(key, salt),
// session ID), where key is derived from the server's master key and salt is
// the random salt of the UTC day (daySalts). The hash changes every UTC day;
// once the day is over its salt is deleted, so the hash cannot be recomputed,
// not even with the server secret and the visitor's cookie. The raw session
// ID, IP address and user agent are never written anywhere.
type recorder struct {
	db      *db.DB
	publish publish.Service
	key     []byte
	salts   *daySalts
	limiter *core.Limiter
	now     func() time.Time
}

var _ Recorder = (*recorder)(nil)

func newRecorder(d *db.DB, key []byte) *recorder {
	return &recorder{db: d, key: key, salts: &daySalts{db: d}, limiter: core.NewLimiter(SessionRate, SessionBurst), now: db.Now}
}

// sessionHash returns the salted hash of a session ID for the UTC day of t.
func (r *recorder) sessionHash(ctx context.Context, sessionID string, t time.Time) (string, error) {
	salt, err := r.salts.get(ctx, t)
	if err != nil {
		return "", err
	}
	dayKey := hmac.New(sha256.New, r.key)
	dayKey.Write(salt)
	m := hmac.New(sha256.New, dayKey.Sum(nil))
	m.Write([]byte(sessionID))
	return hex.EncodeToString(m.Sum(nil)), nil
}

// purgeSalts forgets the salts of finished days (run by the background loop
// right after midnight UTC and with every roll-up).
func (r *recorder) purgeSalts(ctx context.Context) error {
	return r.salts.purge(ctx, r.now())
}

// readKey caches CanRead answers within one batch.
type readKey struct{ series, publication string }

// Record implements Recorder.
func (r *recorder) Record(ctx context.Context, v publish.Viewer, sessionID string, e ReadEvent) error {
	return r.record(ctx, v, sessionID, e, nil)
}

func (r *recorder) record(ctx context.Context, v publish.Viewer, sessionID string, e ReadEvent, cache map[readKey]bool) error {
	ev, err := normalize(e)
	if err != nil {
		return err
	}
	userID := strings.TrimSpace(v.UserID)
	sessionID = strings.TrimSpace(sessionID)
	if len(sessionID) > MaxSessionID || len(userID) > 64 || (userID == "" && sessionID == "") {
		return ErrInvalidEvent
	}
	now := r.now().UTC()
	hash, limitKey := "", "u:"+userID
	if userID == "" {
		if hash, err = r.sessionHash(ctx, sessionID, now); err != nil {
			return err
		}
		limitKey = "s:" + hash
	}
	if ok, _ := r.limiter.Allow(limitKey); !ok {
		return ErrRateLimited
	}
	k := readKey{ev.SeriesID, ev.PublicationID}
	readable, cached := cache[k]
	if !cached {
		readable = r.publish.CanRead(ctx, v, publish.Series{ID: ev.SeriesID},
			publish.Publication{ID: ev.PublicationID, SeriesID: ev.SeriesID})
		if cache != nil {
			cache[k] = readable
		}
	}
	if !readable {
		return ErrNotReadable
	}
	return r.db.WithTx(ctx, func(tx *db.Tx) error { return r.insert(ctx, tx, ev, userID, hash, now) })
}

// insert stores one event. Opens and completions count once per viewer,
// publication and UTC day (a reread the same day is not another read): their
// ID is derived from exactly that, so the insert is one statement that does
// nothing for a repeat, even when two transactions race under PostgreSQL's
// READ COMMITTED (the second waits for the first, then finds the row). Only
// an open that was inserted adds one to series.view_count, in the same
// transaction.
func (r *recorder) insert(ctx context.Context, tx *db.Tx, e ReadEvent, userID, hash string, now time.Time) error {
	id := db.NewID()
	once := e.Kind == EventOpen || e.Kind == EventComplete
	if once {
		who := "u:" + userID
		if userID == "" {
			who = "s:" + hash
		}
		id = r.onceID(e, who, now)
	}
	res, err := tx.Exec(ctx, `INSERT INTO read_events (id, series_id, publication_id, user_id, session_hash, kind, page, ms, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (id) DO NOTHING`,
		id, e.SeriesID, e.PublicationID, db.NullString(userID), hash, e.Kind, e.Page, e.MS, now)
	if err != nil {
		return fmt.Errorf("analytics: insert event: %w", err)
	}
	if once {
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("analytics: insert event: %w", err)
		}
		if n == 0 {
			return nil // already counted today
		}
	}
	if e.Kind == EventOpen {
		// The one write outside read_events: the all-time view counter shown
		// on series cards and the All time ranking.
		if _, err := tx.Exec(ctx, `UPDATE series SET view_count = view_count + 1 WHERE id = ?`, e.SeriesID); err != nil {
			return fmt.Errorf("analytics: count view: %w", err)
		}
	}
	return nil
}

// onceID is the read_events ID of the one open or completion a viewer (who:
// "u:<user ID>" or "s:<daily session hash>") may have per publication and UTC
// day: a ULID whose time is the day and whose random part comes from an HMAC
// of the rest, so it holds nothing the row does not already hold.
func (r *recorder) onceID(e ReadEvent, who string, now time.Time) string {
	day := dayStart(now)
	m := hmac.New(sha256.New, r.key)
	m.Write([]byte("once|" + e.Kind + "|" + e.PublicationID + "|" + who + "|" + day.Format(time.DateOnly)))
	id, err := ulid.New(ulid.Timestamp(day), bytes.NewReader(m.Sum(nil)))
	if err != nil {
		return db.NewID() // unreachable: the day is a valid ULID time and the HMAC has 32 bytes
	}
	return strings.ToLower(id.String())
}

// normalize validates an event and clamps its dwell time.
func normalize(e ReadEvent) (ReadEvent, error) {
	e.SeriesID, e.PublicationID = strings.TrimSpace(e.SeriesID), strings.TrimSpace(e.PublicationID)
	if !db.ValidID(e.SeriesID) || !db.ValidID(e.PublicationID) {
		return ReadEvent{}, ErrInvalidEvent
	}
	switch e.Kind {
	case EventOpen:
		if e.Page < 0 || e.Page > MaxPage {
			return ReadEvent{}, ErrInvalidEvent
		}
		e.MS = 0
	case EventPage, EventDrop:
		if e.Page < 1 || e.Page > MaxPage {
			return ReadEvent{}, ErrInvalidEvent
		}
	case EventComplete:
		if e.Page < 1 || e.Page > MaxPage {
			return ReadEvent{}, ErrInvalidEvent
		}
		e.MS = 0
	default:
		return ReadEvent{}, ErrInvalidEvent
	}
	e.MS = min(max(e.MS, 0), MaxDwellMS)
	return e, nil
}

// dayStart is UTC midnight of t's day.
func dayStart(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
