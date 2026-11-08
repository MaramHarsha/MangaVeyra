package analytics

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// Retention of raw events (ADR 0012 §8).
const (
	// RawRetentionDays is how long raw read_events are kept before they are
	// rolled into daily aggregates.
	RawRetentionDays = 90
	// maxDaysPerRun bounds one roll-up pass so a long backlog is spread over
	// several runs.
	maxDaysPerRun = 31
	// aggregateTable holds the daily aggregates (proposed migration 0500, see
	// testdata/0500_read_event_days.sql).
	aggregateTable = "read_event_days"
)

// RollUpResult reports one roll-up pass.
type RollUpResult struct {
	// Skipped is true when the aggregates table does not exist yet; raw
	// events are then kept (never deleted without being aggregated).
	Skipped bool
	Days    int
	Events  int
}

// hasAggregates reports whether the aggregates table exists. The probe runs
// outside any transaction so a missing table cannot abort one on Postgres.
func hasAggregates(ctx context.Context, d *db.DB) bool {
	var n int
	return d.Get(ctx, &n, `SELECT COUNT(*) FROM `+aggregateTable+` WHERE 1 = 0`) == nil
}

// RollUp moves raw events older than RawRetentionDays (whole UTC days before
// now) into daily per-series, per-publication aggregates and deletes them.
// Each day is claimed with DELETE … RETURNING inside one transaction, so two
// server processes never count a day twice and a failure keeps the raw rows.
func RollUp(ctx context.Context, d *db.DB, now time.Time) (RollUpResult, error) {
	var res RollUpResult
	if !hasAggregates(ctx, d) {
		res.Skipped = true
		return res, nil
	}
	cutoff := dayStart(now).AddDate(0, 0, -RawRetentionDays)
	for res.Days < maxDaysPerRun {
		// A plain column (not MIN()) keeps its TIMESTAMP type on SQLite.
		var oldest time.Time
		if err := d.Get(ctx, &oldest, `SELECT created_at FROM read_events WHERE created_at < ? ORDER BY created_at LIMIT 1`, cutoff); err != nil {
			if db.IsNotFound(err) {
				return res, nil
			}
			return res, fmt.Errorf("analytics: oldest event: %w", err)
		}
		day := dayStart(oldest)
		end := day.AddDate(0, 0, 1)
		if end.After(cutoff) {
			end = cutoff
		}
		n, err := rollUpDay(ctx, d, day, end)
		if err != nil {
			return res, err
		}
		res.Days++
		res.Events += n
	}
	return res, nil
}

type rawEvent struct {
	SeriesID      string         `db:"series_id"`
	PublicationID sql.NullString `db:"publication_id"`
	UserID        sql.NullString `db:"user_id"`
	SessionHash   string         `db:"session_hash"`
	Kind          string         `db:"kind"`
	Page          int            `db:"page"`
	MS            int64          `db:"ms"`
}

type dayKey struct {
	series, publication, kind string
	page                      int
}

type dayAgg struct {
	events, ms int64
	readers    map[string]bool
}

func rollUpDay(ctx context.Context, d *db.DB, from, to time.Time) (int, error) {
	n := 0
	err := d.WithTx(ctx, func(tx *db.Tx) error {
		rows, err := tx.Query(ctx, `DELETE FROM read_events WHERE created_at >= ? AND created_at < ?
			RETURNING series_id, publication_id, user_id, session_hash, kind, page, ms`, from, to)
		if err != nil {
			return fmt.Errorf("analytics: claim day: %w", err)
		}
		groups := map[dayKey]*dayAgg{}
		var order []dayKey
		for rows.Next() {
			var e rawEvent
			if err := rows.StructScan(&e); err != nil {
				_ = rows.Close()
				return fmt.Errorf("analytics: scan event: %w", err)
			}
			n++
			if !e.PublicationID.Valid {
				continue // not produced by Record; nothing to attribute it to
			}
			k := dayKey{e.SeriesID, e.PublicationID.String, e.Kind, e.Page}
			g := groups[k]
			if g == nil {
				g = &dayAgg{readers: map[string]bool{}}
				groups[k] = g
				order = append(order, k)
			}
			g.events++
			g.ms += e.MS
			if reader := readerKey(e.UserID, e.SessionHash); reader != "" {
				g.readers[reader] = true
			}
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("analytics: read day: %w", err)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("analytics: read day: %w", err)
		}
		for _, k := range order {
			g := groups[k]
			if _, err := tx.Exec(ctx, `INSERT INTO `+aggregateTable+` (day, series_id, publication_id, kind, page, events, readers, ms)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (day, series_id, publication_id, kind, page) DO UPDATE SET
					events = `+aggregateTable+`.events + excluded.events,
					readers = `+aggregateTable+`.readers + excluded.readers,
					ms = `+aggregateTable+`.ms + excluded.ms`,
				from, k.series, k.publication, k.kind, k.page, g.events, int64(len(g.readers)), g.ms); err != nil {
				return fmt.Errorf("analytics: store aggregate: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

func readerKey(user sql.NullString, session string) string {
	if user.Valid && user.String != "" {
		return "u:" + user.String
	}
	if session != "" {
		return "s:" + session
	}
	return ""
}

// rollUpLoop runs RollUp shortly after start and then every interval.
func (m *Module) rollUpLoop(ctx context.Context, first, interval time.Duration) error {
	t := time.NewTimer(first)
	defer t.Stop()
	warned := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		res, err := RollUp(ctx, m.d.DB, m.rec.now())
		switch {
		case err != nil:
			m.d.Log.Warn("analytics: roll up read events", "err", err)
		case res.Skipped && !warned:
			warned = true
			m.d.Log.Info("analytics: daily aggregates table missing; raw read events are kept")
		case res.Events > 0:
			m.d.Log.Info("analytics: rolled up read events", "days", res.Days, "events", res.Events)
		}
		t.Reset(interval)
	}
}
