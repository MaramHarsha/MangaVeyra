package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// Worker liveness.
const (
	HeartbeatInterval = 15 * time.Second
	// MissedHeartbeats after which a worker is marked offline and its jobs return to the queue.
	MissedHeartbeats = 3
)

// Worker statuses.
const (
	WorkerOnline   = "online"
	WorkerOffline  = "offline"
	WorkerDraining = "draining"
)

// GPU describes one GPU reported by a worker.
type GPU struct {
	Name        string `json:"name"`
	VRAMTotalMB int    `json:"vram_total_mb"`
	VRAMFreeMB  int    `json:"vram_free_mb"`
}

// Worker is a registered AI worker.
type Worker struct {
	ID            string            `db:"id" json:"id"`
	Name          string            `db:"name" json:"name"`
	TokenKey      string            `db:"token_key" json:"-"`
	Version       string            `db:"version" json:"version"`
	Backends      db.JSON[[]string] `db:"backends" json:"backends"`
	GPUs          db.JSON[[]GPU]    `db:"gpus" json:"gpus"`
	JobTypes      db.JSON[[]string] `db:"job_types" json:"job_types"`
	Models        db.JSON[[]string] `db:"models" json:"models"`
	MaxConcurrent int               `db:"max_concurrent" json:"max_concurrent"`
	Status        string            `db:"status" json:"status"`
	IP            string            `db:"ip" json:"ip"`
	LastSeenAt    time.Time         `db:"last_seen_at" json:"last_seen_at"`
	RegisteredAt  time.Time         `db:"registered_at" json:"registered_at"`
	CreatedAt     time.Time         `db:"created_at" json:"created_at"`
	Running       int               `db:"-" json:"running"`
}

// Registration is the body of POST /api/worker/v1/register.
type Registration struct {
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Backends      []string `json:"backends"`
	GPUs          []GPU    `json:"gpus"`
	JobTypes      []string `json:"job_types"`
	Models        []string `json:"models"`
	MaxConcurrent int      `json:"max_concurrent"`
}

const workerCols = `id, name, token_key, version, backends, gpus, job_types, models, max_concurrent, status, ip, last_seen_at, registered_at, created_at`

// ErrWorkerNotFound is returned for unknown worker ids or ids that belong to another token.
var ErrWorkerNotFound = httpx.NewError(404, "worker_not_found", "unknown worker; register again")

// RegisterWorker upserts a worker by (token, name) and returns it. Re-registering
// with the same token and name returns the same id.
func (q *Queue) RegisterWorker(ctx context.Context, tokenKey, ip string, reg Registration) (*Worker, error) {
	if reg.Name == "" || len(reg.Name) > 64 {
		return nil, httpx.ErrValidation(map[string]string{"name": "name is required (max 64 characters)"})
	}
	if reg.MaxConcurrent <= 0 {
		reg.MaxConcurrent = 1
	}
	if reg.MaxConcurrent > 64 {
		reg.MaxConcurrent = 64
	}
	for _, s := range []*[]string{&reg.Backends, &reg.JobTypes, &reg.Models} {
		if *s == nil {
			*s = []string{}
		}
	}
	if reg.GPUs == nil {
		reg.GPUs = []GPU{}
	}
	now := q.now()
	var w Worker
	err := q.db.WithTx(ctx, func(tx *db.Tx) error {
		var id string
		err := tx.Get(ctx, &id, `SELECT id FROM workers WHERE token_key = ? AND name = ?`, tokenKey, reg.Name)
		switch {
		case db.IsNotFound(err):
			id = db.NewID()
			if _, err := tx.Exec(ctx, `INSERT INTO workers (id, name, token_key, version, backends, gpus, job_types, models, max_concurrent,
				status, ip, last_seen_at, registered_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'online', ?, ?, ?, ?)`,
				id, reg.Name, tokenKey, reg.Version, db.MustJSON(reg.Backends), db.MustJSON(reg.GPUs), db.MustJSON(reg.JobTypes),
				db.MustJSON(reg.Models), reg.MaxConcurrent, ip, now, now, now); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			if _, err := tx.Exec(ctx, `UPDATE workers SET version = ?, backends = ?, gpus = ?, job_types = ?, models = ?, max_concurrent = ?,
				status = CASE WHEN status = 'draining' THEN 'draining' ELSE 'online' END, ip = ?, last_seen_at = ?, registered_at = ? WHERE id = ?`,
				reg.Version, db.MustJSON(reg.Backends), db.MustJSON(reg.GPUs), db.MustJSON(reg.JobTypes), db.MustJSON(reg.Models),
				reg.MaxConcurrent, ip, now, now, id); err != nil {
				return err
			}
		}
		return tx.Get(ctx, &w, `SELECT `+workerCols+` FROM workers WHERE id = ?`, id)
	})
	if err != nil {
		return nil, err
	}
	q.publishWorkers(&w, "worker.online")
	return &w, nil
}

// WorkerFor returns a worker, checking it belongs to tokenKey.
func (q *Queue) WorkerFor(ctx context.Context, workerID, tokenKey string) (*Worker, error) {
	var w Worker
	if err := q.db.Get(ctx, &w, `SELECT `+workerCols+` FROM workers WHERE id = ?`, workerID); err != nil {
		if db.IsNotFound(err) {
			return nil, ErrWorkerNotFound
		}
		return nil, err
	}
	if w.TokenKey != tokenKey {
		return nil, ErrWorkerNotFound
	}
	return &w, nil
}

// WorkerHeartbeat records liveness, extends the leases of the jobs the worker
// reports as running, and returns the ids it must stop (canceled, or no
// longer leased to it).
func (q *Queue) WorkerHeartbeat(ctx context.Context, w *Worker, gpus []GPU, running []string) ([]string, error) {
	now := q.now()
	gpuJSON := db.MustJSON(w.GPUs.V)
	if gpus != nil {
		gpuJSON = db.MustJSON(gpus)
	}
	wasOffline := w.Status == WorkerOffline
	if _, err := q.db.Exec(ctx, `UPDATE workers SET last_seen_at = ?, gpus = ?, status = CASE WHEN status = 'offline' THEN 'online' ELSE status END WHERE id = ?`,
		now, gpuJSON, w.ID); err != nil {
		return nil, err
	}
	if wasOffline {
		q.publishWorkers(w, "worker.online")
	}
	stop := []string{}
	var owned []struct {
		ID              string `db:"id"`
		CancelRequested bool   `db:"cancel_requested"`
	}
	if err := q.db.Select(ctx, &owned, `SELECT id, cancel_requested FROM jobs WHERE worker_id = ? AND state = 'running'`, w.ID); err != nil {
		return nil, err
	}
	ownedSet := map[string]bool{}
	for _, o := range owned {
		ownedSet[o.ID] = true
		if o.CancelRequested {
			stop = append(stop, o.ID)
		}
	}
	for _, id := range running {
		if !ownedSet[id] {
			stop = append(stop, id)
			continue
		}
		if err := q.Heartbeat(ctx, id, w.ID); err != nil && !errors.Is(err, ErrLeaseLost) {
			return nil, err
		}
	}
	return stop, nil
}

// MarkOfflineWorkers flags workers that missed MissedHeartbeats heartbeats as
// offline and returns their running jobs to the queue (or fails them over).
func (q *Queue) MarkOfflineWorkers(ctx context.Context) (int, error) {
	cutoff := q.now().Add(-HeartbeatInterval * MissedHeartbeats)
	var stale []Worker
	if err := q.db.Select(ctx, &stale, `SELECT `+workerCols+` FROM workers WHERE status <> 'offline' AND last_seen_at < ?`, cutoff); err != nil {
		return 0, err
	}
	for i := range stale {
		w := &stale[i]
		if _, err := q.db.Exec(ctx, `UPDATE workers SET status = 'offline' WHERE id = ? AND last_seen_at < ?`, w.ID, cutoff); err != nil {
			return 0, err
		}
		w.Status = WorkerOffline
		q.publishWorkers(w, "worker.offline")
		var ids []string
		if err := q.db.Select(ctx, &ids, `SELECT id FROM jobs WHERE worker_id = ? AND state = 'running'`, w.ID); err != nil {
			return 0, err
		}
		for _, id := range ids {
			wid := w.ID
			if _, err := q.fail(ctx, id, fence{workerID: &wid}, JobError{Code: ErrCodeTimeout, Message: "worker " + w.Name + " went offline", Retryable: true}, Cost{}); err != nil && !errors.Is(err, ErrLeaseLost) {
				q.log.WarnContext(ctx, "jobs: requeue job of offline worker", "job", id, "err", err)
			}
		}
	}
	return len(stale), nil
}

// ListWorkers returns every known worker with its running job count.
func (q *Queue) ListWorkers(ctx context.Context) ([]Worker, error) {
	var out []Worker
	if err := q.db.Select(ctx, &out, `SELECT `+workerCols+` FROM workers ORDER BY status, name`); err != nil {
		return nil, err
	}
	counts := []struct {
		WorkerID string `db:"worker_id"`
		N        int    `db:"n"`
	}{}
	if err := q.db.Select(ctx, &counts, `SELECT worker_id, COUNT(*) AS n FROM jobs WHERE state = 'running' AND worker_id IS NOT NULL GROUP BY worker_id`); err != nil {
		return nil, err
	}
	for _, c := range counts {
		for i := range out {
			if out[i].ID == c.WorkerID {
				out[i].Running = c.N
			}
		}
	}
	return out, nil
}

// SetWorkerStatus drains (no new claims) or resumes an active worker. An
// offline worker becomes online only by registering or sending a heartbeat;
// a stale admin action must not fabricate liveness.
func (q *Queue) SetWorkerStatus(ctx context.Context, id, status string) error {
	if status != WorkerOnline && status != WorkerDraining {
		return httpx.ErrBadRequest("status must be online or draining")
	}
	res, err := q.db.Exec(ctx, `UPDATE workers SET status = ? WHERE id = ? AND status IN ('online', 'draining')`, status, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var existing string
		if err := q.db.Get(ctx, &existing, `SELECT id FROM workers WHERE id = ?`, id); db.IsNotFound(err) {
			return ErrWorkerNotFound
		} else if err != nil {
			return err
		}
		return ErrNotActive
	}
	return nil
}

// DeleteWorker forgets a worker (its running jobs are requeued).
func (q *Queue) DeleteWorker(ctx context.Context, id string) error {
	var ids []string
	if err := q.db.Select(ctx, &ids, `SELECT id FROM jobs WHERE worker_id = ? AND state = 'running'`, id); err != nil {
		return err
	}
	for _, jid := range ids {
		wid := id
		_, _ = q.fail(ctx, jid, fence{workerID: &wid}, JobError{Code: ErrCodeTimeout, Message: "worker removed", Retryable: true}, Cost{})
	}
	_, err := q.db.Exec(ctx, `DELETE FROM workers WHERE id = ?`, id)
	return err
}

// claimTypes intersects requested job types with those declared at registration.
func claimTypes(w *Worker, requested []string) []string {
	if len(requested) == 0 {
		return w.JobTypes.V
	}
	out := []string{}
	for _, t := range requested {
		if slices.Contains(w.JobTypes.V, t) {
			out = append(out, t)
		}
	}
	return out
}

func (q *Queue) publishWorkers(w *Worker, typ string) {
	q.mu.RLock()
	pub := q.pub
	q.mu.RUnlock()
	if pub == nil {
		return
	}
	data, _ := json.Marshal(w)
	pub.Publish("admin:workers", typ, json.RawMessage(data))
}
