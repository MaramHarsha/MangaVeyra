package jobs

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// CompletedUsage totals recorded actuals for completed top-level jobs in the
// window. Parent actuals already include child work, so children are excluded.
// Rows are streamed to keep the queue screen's memory bounded.
func (q *Queue) CompletedUsage(ctx context.Context, userID string, since time.Time) (Cost, error) {
	query := `SELECT actual FROM jobs WHERE parent_id IS NULL AND finished_at >= ?`
	args := []any{since.UTC()}
	if userID != "" {
		query += ` AND user_id = ?`
		args = append(args, userID)
	}
	rows, err := q.db.Query(ctx, query, args...)
	if err != nil {
		return Cost{}, err
	}
	defer rows.Close()
	var total Cost
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return Cost{}, err
		}
		var c Cost
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return Cost{}, err
		}
		if !math.IsNaN(c.USD) && !math.IsInf(c.USD, 0) && c.USD >= 0 {
			total.USD += c.USD
		}
		if !math.IsNaN(c.GPUSeconds) && !math.IsInf(c.GPUSeconds, 0) && c.GPUSeconds >= 0 {
			total.GPUSeconds += c.GPUSeconds
		}
	}
	return total, rows.Err()
}

// ActiveWorkerChildren returns the unfinished worker jobs started by each of
// the given parents, oldest first. Activity views fold them into their parent
// (one row per job) and show the child's measured progress on it.
func (q *Queue) ActiveWorkerChildren(ctx context.Context, parentIDs []string) (map[string][]*Job, error) {
	out := map[string][]*Job{}
	if len(parentIDs) == 0 {
		return out, nil
	}
	qs, args, err := db.In(`SELECT `+jobCols+` FROM jobs WHERE parent_id IN (?) AND executor = ? AND state IN ('queued', 'running')
		ORDER BY id LIMIT 1000`, parentIDs, ExecutorWorker)
	if err != nil {
		return nil, err
	}
	var rows []jobRow
	if err := q.db.Select(ctx, &rows, qs, args...); err != nil {
		return nil, err
	}
	for i := range rows {
		j := rows[i].job()
		out[j.ParentID] = append(out[j.ParentID], j)
	}
	return out, nil
}

// RecentEvents returns at most limit recent events, in chronological order.
// Browser views project only event kinds and timestamps, never raw messages or
// data that a provider or worker may have supplied.
func (q *Queue) RecentEvents(ctx context.Context, jobID string, limit int) ([]Event, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var out []Event
	err := q.db.Select(ctx, &out, `SELECT id, job_id, kind, created_at FROM job_events WHERE job_id = ? ORDER BY id DESC LIMIT ?`, jobID, limit)
	slices.Reverse(out)
	return out, err
}
