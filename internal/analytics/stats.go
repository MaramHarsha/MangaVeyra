package analytics

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// Aggregate reads shared with discover (rankings, rising creators and
// "Because you read"). A read is an open event; Record stores at most one per
// reader, publication and UTC day. Every function takes series IDs the caller
// already obtained through publish.Service for the viewer, so nothing here
// decides access.

// maxStatIDs bounds the IN lists below.
const maxStatIDs = 500

type idCount struct {
	ID string `db:"id"`
	N  int64  `db:"n"`
}

func inList(ids []string) (string, []any) {
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	return strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", "), args
}

func countBySeries(ctx context.Context, d *db.DB, query string, ids []string, from, to time.Time) (map[string]int64, error) {
	out := map[string]int64{}
	if len(ids) == 0 {
		return out, nil
	}
	if len(ids) > maxStatIDs {
		ids = ids[:maxStatIDs]
	}
	in, args := inList(ids)
	var rows []idCount
	if err := d.Select(ctx, &rows, strings.Replace(query, "(IDS)", "("+in+")", 1), append([]any{from, to}, args...)...); err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = r.N
	}
	return out, nil
}

// SeriesReads counts reads per series in [from, to).
func SeriesReads(ctx context.Context, d *db.DB, ids []string, from, to time.Time) (map[string]int64, error) {
	out, err := countBySeries(ctx, d, `SELECT series_id AS id, COUNT(*) AS n FROM read_events
		WHERE kind = 'open' AND created_at >= ? AND created_at < ? AND series_id IN (IDS) GROUP BY series_id`, ids, from, to)
	if err != nil {
		return nil, fmt.Errorf("analytics: series reads: %w", err)
	}
	return out, nil
}

// SeriesNewFollows counts follows created in [from, to) per series that still
// stand. It is a read-only aggregate over the social module's follows table.
func SeriesNewFollows(ctx context.Context, d *db.DB, ids []string, from, to time.Time) (map[string]int64, error) {
	out, err := countBySeries(ctx, d, `SELECT series_id AS id, COUNT(*) AS n FROM follows
		WHERE created_at >= ? AND created_at < ? AND series_id IN (IDS) GROUP BY series_id`, ids, from, to)
	if err != nil {
		return nil, fmt.Errorf("analytics: series follows: %w", err)
	}
	return out, nil
}

// RecentlyReadSeries lists the series a member opened most recently, newest
// first and without repeats (at most limit). Anonymous visitors have no
// history: their session hash rotates daily and is never linked to them.
func RecentlyReadSeries(ctx context.Context, d *db.DB, userID string, limit int) ([]string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" || limit <= 0 {
		return nil, nil
	}
	var rows []struct {
		ID   string    `db:"series_id"`
		Last time.Time `db:"created_at"`
	}
	// The newest opens first; a member's last 200 opens are plenty to find
	// the distinct series they read lately.
	if err := d.Select(ctx, &rows, `SELECT series_id, created_at FROM read_events
		WHERE user_id = ? AND kind = 'open' ORDER BY created_at DESC LIMIT 200`, userID); err != nil {
		return nil, fmt.Errorf("analytics: recent reads: %w", err)
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		out = append(out, r.ID)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}
