package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

// Errors.
var (
	ErrNotFound  = httpx.ErrNotFound()
	ErrLeaseLost = errors.New("jobs: job is no longer leased to this worker")
	ErrNotActive = httpx.ErrConflict("job is not in a state that allows this")
)

// Publisher receives job events for realtime delivery (the ws hub implements it).
type Publisher interface {
	Publish(topic, typ string, data any)
}

// Validator checks an enqueue request before it is stored. The studio
// registers its scope/lock check (studio.CheckScopeWritable) here; the AI
// module registers spend guards. Returning an error rejects the job.
type Validator func(ctx context.Context, p *EnqueueParams) error

// Reroute is a fallback decision: run the job again on another model/provider.
type Reroute struct {
	ModelID    string
	ProviderID string
	Executor   string // "" keeps the current executor
	Decision   json.RawMessage
	Reason     string
}

// Failover is consulted when an attempt fails; returning a Reroute requeues
// the job on the next candidate (the router's automatic fallback).
type Failover func(ctx context.Context, j *Job, jerr JobError) (*Reroute, error)

// EnqueueParams describes a new job.
type EnqueueParams struct {
	Type        string
	Scope       Scope
	UserID      string
	Executor    string // default "go"
	ModelID     string
	ProviderID  string
	Decision    any
	Input       any
	Files       map[string]string // name → storage key handed to workers as signed URLs
	Estimate    Cost
	Priority    int  // higher runs first (interactive single-panel work > batch)
	Batch       bool // deferrable to night mode
	MaxAttempts int  // default 3
	ParentID    string
	RunAfter    time.Time
	// Guard, when set, runs inside the enqueue transaction before the insert.
	// Returning an error aborts the enqueue. Use it for atomic de-duplication:
	// lock the parent row, then check for an active job of the same kind.
	Guard func(ctx context.Context, tx *db.Tx) error
}

// Options configure the queue.
type Options struct {
	// LeaseWorker is the lease handed to workers (default 120 s).
	LeaseWorker time.Duration
	// LeaseGo is the lease of Go-executor jobs, renewed by the pool (default 60 s).
	LeaseGo time.Duration
	// Backoff is the base retry delay, doubled per attempt (default 5 s).
	Backoff time.Duration
	Logger  *slog.Logger
}

// Queue is the database-backed job queue.
type Queue struct {
	db       *db.DB
	settings *settings.Service
	pub      Publisher
	log      *slog.Logger
	opts     Options

	mu         sync.RWMutex
	validators []Validator
	failover   Failover
	onFinish   []func(context.Context, *Job)
	onCancel   []func(jobID string)
	access     func(ctx context.Context, id httpx.Identity, j *Job) bool
	// privateInput lists job types whose input never leaves the server
	// through the jobs API (it may carry private project material).
	privateInput map[string]bool

	wakeMu sync.Mutex
	wake   chan struct{}

	// Now is the clock (tests replace it).
	Now func() time.Time
}

// New creates a queue. pub may be nil.
func New(d *db.DB, st *settings.Service, pub Publisher, opts Options) *Queue {
	if opts.LeaseWorker <= 0 {
		opts.LeaseWorker = 120 * time.Second
	}
	if opts.LeaseGo <= 0 {
		opts.LeaseGo = 60 * time.Second
	}
	if opts.Backoff <= 0 {
		opts.Backoff = 5 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Queue{db: d, settings: st, pub: pub, log: opts.Logger, opts: opts, wake: make(chan struct{}), Now: db.Now}
}

// MarkPrivateInput hides the input of these job types from the jobs API. The
// stored payload may carry private project material, such as an adaptation
// source, that only the job handler needs.
func (q *Queue) MarkPrivateInput(types ...string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.privateInput == nil {
		q.privateInput = map[string]bool{}
	}
	for _, t := range types {
		q.privateInput[t] = true
	}
}

// PublicView returns j as the jobs API may show it: a copy without the input
// when the job type's input is private.
func (q *Queue) PublicView(j *Job) *Job {
	if j == nil {
		return nil
	}
	q.mu.RLock()
	private := q.privateInput[j.Type]
	q.mu.RUnlock()
	if !private {
		return j
	}
	c := *j
	c.Input = nil
	return &c
}

// SetPublisher sets the realtime publisher (wiring creates the hub after the queue).
func (q *Queue) SetPublisher(p Publisher) {
	q.mu.Lock()
	q.pub = p
	q.mu.Unlock()
}

// AddValidator registers an enqueue validator.
func (q *Queue) AddValidator(v Validator) {
	q.mu.Lock()
	q.validators = append(q.validators, v)
	q.mu.Unlock()
}

// SetFailover installs the fallback decision function (the AI router).
func (q *Queue) SetFailover(f Failover) {
	q.mu.Lock()
	q.failover = f
	q.mu.Unlock()
}

// OnFinish registers fn to run after a job reaches done, failed or canceled
// (spend ledger, model stats, notifications). It runs synchronously; keep it quick.
func (q *Queue) OnFinish(fn func(context.Context, *Job)) {
	q.mu.Lock()
	q.onFinish = append(q.onFinish, fn)
	q.mu.Unlock()
}

// OnCancel registers fn to run when cancellation of a running job is requested.
func (q *Queue) OnCancel(fn func(jobID string)) {
	q.mu.Lock()
	q.onCancel = append(q.onCancel, fn)
	q.mu.Unlock()
}

// SetAccess replaces who may see and cancel a job (default: its owner and admins).
func (q *Queue) SetAccess(fn func(ctx context.Context, id httpx.Identity, j *Job) bool) {
	q.mu.Lock()
	q.access = fn
	q.mu.Unlock()
}

// CanAccess reports whether id may see j.
func (q *Queue) CanAccess(ctx context.Context, id httpx.Identity, j *Job) bool {
	if id.User == nil || j == nil {
		return false
	}
	if token := id.Token; token != nil {
		if token.UserID != "" && token.UserID != id.User.ID {
			return false
		}
		if j.Scope.ProjectID == "" {
			if len(token.ProjectIDs) != 0 {
				return false
			}
		} else if !token.AllowsProject(j.Scope.ProjectID) {
			return false
		}
		if j.UserID == id.User.ID {
			return true
		}
		if id.User.IsAdmin() && token.HasScope("admin") {
			return true
		}
		return false
	}
	if id.User.IsAdmin() || j.UserID == id.User.ID {
		return true
	}
	q.mu.RLock()
	fn := q.access
	q.mu.RUnlock()
	return fn != nil && fn(ctx, id, j)
}

func (q *Queue) now() time.Time { return q.Now().UTC().Truncate(time.Microsecond) }

// wakeCh returns a channel closed at the next enqueue.
func (q *Queue) wakeCh() <-chan struct{} {
	q.wakeMu.Lock()
	defer q.wakeMu.Unlock()
	return q.wake
}

func (q *Queue) signal() {
	q.wakeMu.Lock()
	close(q.wake)
	q.wake = make(chan struct{})
	q.wakeMu.Unlock()
}

// Enqueue validates and stores a job.
func (q *Queue) Enqueue(ctx context.Context, p EnqueueParams) (*Job, error) {
	if p.Type == "" {
		return nil, httpx.ErrBadRequest("job type is required")
	}
	if p.Executor == "" {
		p.Executor = ExecutorGo
	}
	if p.Executor != ExecutorGo && p.Executor != ExecutorWorker {
		return nil, httpx.ErrBadRequest("executor must be go or worker")
	}
	if err := p.Scope.Validate(); err != nil {
		return nil, &httpx.APIError{Status: 422, Code: "invalid_scope", Key: "jobs.error.scope", Err: err}
	}
	q.mu.RLock()
	validators := append([]Validator{}, q.validators...)
	q.mu.RUnlock()
	for _, v := range validators {
		if err := v(ctx, &p); err != nil {
			return nil, err
		}
	}
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 3
	}
	input, err := marshalObj(p.Input)
	if err != nil {
		return nil, err
	}
	decision, err := marshalObj(p.Decision)
	if err != nil {
		return nil, err
	}
	if p.Files == nil {
		p.Files = map[string]string{}
	}
	id, now := db.NewID(), q.now()
	var runAfter any
	if !p.RunAfter.IsZero() {
		runAfter = p.RunAfter.UTC()
	}
	err = q.db.WithTx(ctx, func(tx *db.Tx) error {
		if p.Guard != nil {
			if err := p.Guard(ctx, tx); err != nil {
				return guardError{err}
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO jobs (id, type, scope, scope_kind, project_id, chapter_id, page_id, panel_id, layer_id,
			user_id, state, executor, model_id, provider_id, decision, input, files, estimate, max_attempts, parent_id, priority, batch,
			run_after, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, p.Type, db.MustJSON(p.Scope), p.Scope.Kind, db.NullString(p.Scope.ProjectID), db.NullString(p.Scope.ChapterID),
			db.NullString(p.Scope.PageID), db.NullString(p.Scope.PanelID), db.NullString(p.Scope.LayerID), db.NullString(p.UserID),
			p.Executor, p.ModelID, p.ProviderID, decision, input, db.MustJSON(p.Files), db.MustJSON(p.Estimate), p.MaxAttempts,
			db.NullString(p.ParentID), p.Priority, p.Batch, runAfter, now, now); err != nil {
			return err
		}
		return addEvent(ctx, tx, id, "created", "", map[string]any{"model_id": p.ModelID, "provider_id": p.ProviderID, "estimate": p.Estimate})
	})
	if err != nil {
		var guarded guardError
		if errors.As(err, &guarded) {
			return nil, guarded.err
		}
		if db.IsForeignKeyViolation(err) {
			return nil, httpx.ErrBadRequest("job references an unknown project, user or parent job")
		}
		return nil, fmt.Errorf("jobs: enqueue: %w", err)
	}
	j, err := q.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	q.publish(j, "job.queued", "")
	q.signal()
	return j, nil
}

// guardError carries an EnqueueParams.Guard rejection out of the transaction
// unchanged, so callers can match their own sentinel errors.
type guardError struct{ err error }

func (e guardError) Error() string { return e.err.Error() }
func (e guardError) Unwrap() error { return e.err }

func marshalObj(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "{}", nil
	case json.RawMessage:
		if len(t) == 0 {
			return "{}", nil
		}
		return string(t), nil
	case []byte:
		return string(t), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("jobs: marshal: %w", err)
	}
	return string(b), nil
}

func addEvent(ctx context.Context, q db.Querier, jobID, kind, msg string, data any) error {
	if data == nil {
		data = map[string]any{}
	}
	_, err := q.Exec(ctx, `INSERT INTO job_events (id, job_id, kind, message, data, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		db.NewID(), jobID, kind, msg, db.MustJSON(data), db.Now())
	return err
}

// Log appends an event to a job's log.
func (q *Queue) Log(ctx context.Context, jobID, kind, msg string, data any) error {
	return addEvent(ctx, q.db, jobID, kind, msg, data)
}

// Get returns a job.
func (q *Queue) Get(ctx context.Context, id string) (*Job, error) {
	return q.get(ctx, q.db, id)
}

func (q *Queue) get(ctx context.Context, qr db.Querier, id string) (*Job, error) {
	var r jobRow
	if err := qr.Get(ctx, &r, `SELECT `+jobCols+` FROM jobs WHERE id = ?`, id); err != nil {
		if db.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return r.job(), nil
}

// getForUpdate locks the selected job until tx ends on PostgreSQL. SQLite
// transactions already start with BEGIN IMMEDIATE and are serialized by db.DB.
func (q *Queue) getForUpdate(ctx context.Context, tx *db.Tx, id string) (*Job, error) {
	qs := `SELECT ` + jobCols + ` FROM jobs WHERE id = ?`
	if tx.Dialect() == db.Postgres {
		qs += ` FOR UPDATE`
	}
	return q.getQuery(ctx, tx, qs, id)
}

func (q *Queue) getQuery(ctx context.Context, qr db.Querier, query string, args ...any) (*Job, error) {
	var r jobRow
	if err := qr.Get(ctx, &r, query, args...); err != nil {
		if db.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return r.job(), nil
}

// Events returns a job's log, oldest first.
func (q *Queue) Events(ctx context.Context, jobID string) ([]Event, error) {
	var out []Event
	if err := q.db.Select(ctx, &out, `SELECT id, job_id, kind, message, data, created_at FROM job_events WHERE job_id = ? ORDER BY id`, jobID); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].RawData != "" && out[i].RawData != "{}" {
			out[i].Data = json.RawMessage(out[i].RawData)
		}
	}
	return out, nil
}

// Filter selects jobs for List.
type Filter struct {
	UserID     string
	ProjectID  string
	ProjectIDs []string
	ChapterID  string
	PageID     string
	States     []string
	Types      []string
	Executor   string
	Before     string // id cursor (exclusive)
	Limit      int
}

// List returns jobs newest first.
func (q *Queue) List(ctx context.Context, f Filter) ([]*Job, error) {
	where, args := []string{"1=1"}, []any{}
	add := func(cond string, a ...any) { where, args = append(where, cond), append(args, a...) }
	if f.UserID != "" {
		add("user_id = ?", f.UserID)
	}
	if f.ProjectID != "" {
		add("project_id = ?", f.ProjectID)
	}
	if len(f.ProjectIDs) != 0 {
		add("project_id IN (?)", f.ProjectIDs)
	}
	if f.ChapterID != "" {
		add("chapter_id = ?", f.ChapterID)
	}
	if f.PageID != "" {
		add("page_id = ?", f.PageID)
	}
	if f.Executor != "" {
		add("executor = ?", f.Executor)
	}
	if f.Before != "" {
		add("id < ?", f.Before)
	}
	qs := `SELECT ` + jobCols + ` FROM jobs WHERE ` + strings.Join(where, " AND ")
	if len(f.States) > 0 {
		qs += ` AND state IN (?)`
		args = append(args, f.States)
	}
	if len(f.Types) > 0 {
		qs += ` AND type IN (?)`
		args = append(args, f.Types)
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	qs += ` ORDER BY id DESC LIMIT ?`
	args = append(args, f.Limit)
	qs, args, err := db.In(qs, args...)
	if err != nil {
		return nil, err
	}
	var rows []jobRow
	if err := q.db.Select(ctx, &rows, qs, args...); err != nil {
		return nil, err
	}
	out := make([]*Job, len(rows))
	for i := range rows {
		out[i] = rows[i].job()
	}
	return out, nil
}

// Stats summarises the queue for the compute meter and admin screens.
type Stats struct {
	Queued         int `json:"queued"`
	Running        int `json:"running"`
	RunningWorker  int `json:"running_worker"`
	WorkersOnline  int `json:"workers_online"`
	FailedLastHour int `json:"failed_last_hour"`
}

// Stats returns queue counts (for one user when userID is set).
func (q *Queue) Stats(ctx context.Context, userID string) (Stats, error) {
	var s Stats
	cond, args := "", []any{}
	if userID != "" {
		cond, args = " AND user_id = ?", []any{userID}
	}
	rows := []struct {
		State    string `db:"state"`
		Executor string `db:"executor"`
		N        int    `db:"n"`
	}{}
	if err := q.db.Select(ctx, &rows, `SELECT state, executor, COUNT(*) AS n FROM jobs WHERE state IN ('queued', 'running')`+cond+` GROUP BY state, executor`, args...); err != nil {
		return s, err
	}
	for _, r := range rows {
		switch r.State {
		case StateQueued:
			s.Queued += r.N
		case StateRunning:
			s.Running += r.N
			if r.Executor == ExecutorWorker {
				s.RunningWorker += r.N
			}
		}
	}
	if err := q.db.Get(ctx, &s.FailedLastHour, `SELECT COUNT(*) FROM jobs WHERE state = 'failed' AND finished_at > ?`+cond,
		append([]any{q.now().Add(-time.Hour)}, args...)...); err != nil {
		return s, err
	}
	err := q.db.Get(ctx, &s.WorkersOnline, `SELECT COUNT(*) FROM workers WHERE status = 'online'`)
	return s, err
}

// Position returns how many queued jobs of the same executor run before j.
func (q *Queue) Position(ctx context.Context, j *Job) (int, error) {
	if j.State != StateQueued {
		return 0, nil
	}
	var n int
	err := q.db.Get(ctx, &n, `SELECT COUNT(*) FROM jobs WHERE state = 'queued' AND executor = ? AND id <> ?
		AND (priority > ? OR (priority = ? AND created_at < ?))`, j.Executor, j.ID, j.Priority, j.Priority, j.CreatedAt)
	return n, err
}

// ---- claiming ----------------------------------------------------------------

// ClaimParams select what a claimer can run.
type ClaimParams struct {
	Executor string
	Types    []string
	// Models restricts worker claims to jobs without a model or with one of these models.
	Models []string
	// WorkerID is set for worker claims.
	WorkerID string
	// MaxRunning caps jobs running on this worker (0 = unlimited).
	MaxRunning int
}

// Claim atomically picks the next runnable job and marks it running. It
// returns (nil, nil) when nothing is runnable. Ordering: priority, then the
// user with the fewest running jobs (fair share), then age. Users at their
// running limit are skipped; batch jobs wait for the night window when night mode is on.
func (q *Queue) Claim(ctx context.Context, p ClaimParams) (*Job, error) {
	if len(p.Types) == 0 {
		return nil, nil
	}
	inst := q.settings.MustInstance(ctx)
	limit := inst.Limits.RunningJobs
	lease := q.opts.LeaseGo
	if p.Executor == ExecutorWorker {
		limit = inst.Limits.RunningGPUJobs
		lease = q.opts.LeaseWorker
	}
	if limit <= 0 {
		limit = math.MaxInt32
	}
	now := q.now()
	batchOK := !inst.NightMode.Enabled || inst.NightMode.InWindow(time.Now())

	var claimed *Job
	err := q.db.WithTx(ctx, func(tx *db.Tx) error {
		if tx.Dialect() == db.Postgres {
			// Serialise claimers so per-user running limits cannot be overshot by concurrent claims.
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7468326820)`); err != nil {
				return err
			}
		}
		if p.WorkerID != "" && p.MaxRunning > 0 {
			var running int
			if err := tx.Get(ctx, &running, `SELECT COUNT(*) FROM jobs WHERE worker_id = ? AND state = 'running'`, p.WorkerID); err != nil {
				return err
			}
			if running >= p.MaxRunning {
				return nil
			}
		}
		qs := `SELECT j.id FROM jobs j
			WHERE j.state = 'queued' AND j.executor = ? AND j.type IN (?)
			AND (j.run_after IS NULL OR j.run_after <= ?)
			AND (j.user_id IS NULL OR (SELECT COUNT(*) FROM jobs r WHERE r.user_id = j.user_id AND r.state = 'running' AND r.executor = j.executor) < ?)`
		args := []any{p.Executor, p.Types, now, limit}
		if len(p.Models) > 0 {
			qs += ` AND (j.model_id = '' OR j.model_id IN (?))`
			args = append(args, p.Models)
		}
		if !batchOK {
			qs += ` AND j.batch = FALSE`
		}
		qs += ` ORDER BY j.priority DESC,
			(SELECT COUNT(*) FROM jobs r WHERE r.user_id = j.user_id AND r.state = 'running') ASC,
			j.created_at ASC, j.id ASC LIMIT 1`
		if tx.Dialect() == db.Postgres {
			qs += ` FOR UPDATE OF j SKIP LOCKED`
		}
		qs, args, err := db.In(qs, args...)
		if err != nil {
			return err
		}
		var id string
		if err := tx.Get(ctx, &id, qs, args...); err != nil {
			if db.IsNotFound(err) {
				return nil
			}
			return err
		}
		res, err := tx.Exec(ctx, `UPDATE jobs SET state = 'running', worker_id = ?, attempts = attempts + 1,
			started_at = COALESCE(started_at, ?), lease_expires_at = ?, updated_at = ?, message = '' WHERE id = ? AND state = 'queued'`,
			db.NullString(p.WorkerID), now, now.Add(lease), now, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		if err := addEvent(ctx, tx, id, "claimed", "", map[string]any{"executor": p.Executor, "worker_id": p.WorkerID}); err != nil {
			return err
		}
		claimed, err = q.get(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("jobs: claim: %w", err)
	}
	if claimed != nil {
		q.publish(claimed, "job.started", "")
	}
	return claimed, nil
}

// ClaimWait long-polls Claim for up to wait.
func (q *Queue) ClaimWait(ctx context.Context, p ClaimParams, wait time.Duration) (*Job, error) {
	deadline := time.Now().Add(wait)
	for {
		ch := q.wakeCh()
		j, err := q.Claim(ctx, p)
		if err != nil || j != nil {
			return j, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, nil
		}
		// Poll at least every second so jobs enqueued by other server instances are seen too.
		t := time.NewTimer(min(remaining, time.Second))
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, nil
		case <-ch:
			t.Stop()
		case <-t.C:
		}
	}
}

// ---- progress and completion ---------------------------------------------------

// ProgressUpdate is a progress report.
type ProgressUpdate struct {
	Progress float64
	Step     int
	Steps    int
	Message  string
	// Preview is an optional PNG preview (≤ 64 KB) forwarded to subscribers, not stored.
	Preview []byte
}

// Progress records progress and extends the lease. workerID must hold the job
// ("" for Go-executor jobs). It returns whether cancellation was requested.
func (q *Queue) Progress(ctx context.Context, id, workerID string, u ProgressUpdate) (bool, error) {
	return q.progress(ctx, id, fence{workerID: &workerID}, u)
}

// fence identifies one lease of a job: the holder (worker id, "" for the Go
// executor) and the attempt it claimed. Writes that carry a fence only apply
// while that exact lease is current, so a holder whose lease expired (and
// whose job was re-claimed by someone else) can no longer report progress,
// complete or fail the job.
type fence struct {
	// workerID must match the job's worker ("" = Go executor); nil skips the check.
	workerID *string
	// attempt must match the job's attempt counter; 0 skips the check.
	attempt int
	// expiredBefore, when set, only matches while the lease expired before it (lease-expiry sweeps).
	expiredBefore time.Time
}

// holds reports whether j is running under this lease.
func (f fence) holds(j *Job) bool {
	if j.State != StateRunning {
		return false
	}
	if f.workerID != nil && j.WorkerID != *f.workerID {
		return false
	}
	if !f.expiredBefore.IsZero() && (j.LeaseExpiresAt == nil || !j.LeaseExpiresAt.Before(f.expiredBefore)) {
		return false
	}
	return f.attempt == 0 || j.Attempts == f.attempt
}

// where returns the SQL condition (and args) pinning the row to the lease observed in cur.
func (f fence) where(cur *Job) (string, []any) {
	cond, args := ` AND state = 'running' AND COALESCE(worker_id, '') = ? AND attempts = ?`, []any{cur.WorkerID, cur.Attempts}
	if !f.expiredBefore.IsZero() {
		cond += ` AND lease_expires_at IS NOT NULL AND lease_expires_at < ?`
		args = append(args, f.expiredBefore)
	}
	return cond, args
}

func (q *Queue) progress(ctx context.Context, id string, f fence, u ProgressUpdate) (bool, error) {
	j, err := q.Get(ctx, id)
	if err != nil {
		return true, err
	}
	if !f.holds(j) {
		return true, ErrLeaseLost
	}
	lease := q.opts.LeaseGo
	if j.Executor == ExecutorWorker {
		lease = q.opts.LeaseWorker
	}
	p := math.Max(0, math.Min(1, u.Progress))
	now := q.now()
	cond, cargs := f.where(j)
	res, err := q.db.Exec(ctx, `UPDATE jobs SET progress = ?, step = ?, steps = ?, message = ?, lease_expires_at = ?, updated_at = ?
		WHERE id = ?`+cond, append([]any{p, u.Step, u.Steps, truncate(u.Message, 300), now.Add(lease), now, id}, cargs...)...)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return true, ErrLeaseLost
	}
	if u.Message != "" && u.Message != j.Message {
		_ = addEvent(ctx, q.db, id, "progress", truncate(u.Message, 300), map[string]any{"progress": p, "step": u.Step, "steps": u.Steps})
	}
	j.Progress, j.Step, j.Steps, j.Message, j.UpdatedAt = p, u.Step, u.Steps, truncate(u.Message, 300), now
	preview := ""
	if len(u.Preview) > 0 && len(u.Preview) <= 64<<10 {
		preview = "data:image/png;base64," + base64Std(u.Preview)
	}
	q.publish(j, "job.progress", preview)
	return j.CancelRequested, nil
}

// Heartbeat extends the lease of a running job without changing progress.
func (q *Queue) Heartbeat(ctx context.Context, id, workerID string) error {
	return q.heartbeat(ctx, id, workerID, 0)
}

// heartbeat extends the lease held by workerID; attempt > 0 also pins the attempt.
func (q *Queue) heartbeat(ctx context.Context, id, workerID string, attempt int) error {
	lease := q.opts.LeaseGo
	if workerID != "" {
		lease = q.opts.LeaseWorker
	}
	now := q.now()
	qs, args := `UPDATE jobs SET lease_expires_at = ? WHERE id = ? AND state = 'running' AND COALESCE(worker_id, '') = ?`, []any{now.Add(lease), id, workerID}
	if attempt > 0 {
		qs += ` AND attempts = ?`
		args = append(args, attempt)
	}
	res, err := q.db.Exec(ctx, qs, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrLeaseLost
	}
	return nil
}

// Complete marks a running job done with its output and actual cost.
func (q *Queue) Complete(ctx context.Context, id, workerID string, output any, actual Cost) (*Job, error) {
	return q.complete(ctx, id, fence{workerID: &workerID}, output, actual)
}

func (q *Queue) complete(ctx context.Context, id string, f fence, output any, actual Cost) (*Job, error) {
	j, err := q.completeTx(ctx, id, f, actual, false, func(context.Context, *db.Tx) (any, error) {
		return output, nil
	})
	if err != nil {
		return nil, err
	}
	q.finished(ctx, j, "job.done")
	return j, nil
}

type completionEffect func(context.Context, *db.Tx) (output any, err error)

// completeTx runs effect and completes the job in one transaction. When
// rejectCanceled is true, a pending cancellation wins before effect is called.
func (q *Queue) completeTx(ctx context.Context, id string, f fence, actual Cost, rejectCanceled bool, effect completionEffect) (*Job, error) {
	if effect == nil {
		return nil, errors.New("jobs: completion effect is nil")
	}
	var j *Job
	err := q.db.WithTx(ctx, func(tx *db.Tx) error {
		cur, err := q.getForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if !f.holds(cur) {
			return ErrLeaseLost
		}
		if rejectCanceled && cur.CancelRequested {
			return ErrCanceled
		}
		output, err := effect(ctx, tx)
		if err != nil {
			return err
		}
		out, err := marshalObj(output)
		if err != nil {
			return err
		}
		now := q.now()
		total := cur.Actual.Add(actual)
		cond, cargs := f.where(cur)
		if rejectCanceled {
			cond += ` AND cancel_requested = FALSE`
		}
		res, err := tx.Exec(ctx, `UPDATE jobs SET state = 'done', progress = 1, output = ?, actual = ?, error = '{}', message = '',
			lease_expires_at = NULL, finished_at = ?, updated_at = ? WHERE id = ?`+cond, append([]any{out, db.MustJSON(total), now, now, id}, cargs...)...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrLeaseLost
		}
		if err := addEvent(ctx, tx, id, "done", "", map[string]any{"actual": total}); err != nil {
			return err
		}
		j, err = q.get(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return j, nil
}

// Fail records a failed attempt. The queue then retries (with backoff), falls
// back to another model through the Failover hook, cancels (code "canceled"
// or a pending cancel request) or fails the job for good.
func (q *Queue) Fail(ctx context.Context, id, workerID string, jerr JobError, actual Cost) (*Job, error) {
	return q.fail(ctx, id, fence{workerID: &workerID}, jerr, actual)
}

func (q *Queue) fail(ctx context.Context, id string, f fence, jerr JobError, actual Cost) (*Job, error) {
	if jerr.Code == "" {
		jerr.Code = ErrCodeInternal
	}
	jerr.Message = truncate(jerr.Message, 1000)
	cur, err := q.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !f.holds(cur) {
		return nil, ErrLeaseLost
	}
	// Every UPDATE below is pinned to the lease observed here, so a concurrent
	// claim, completion, heartbeat or requeue turns this call into ErrLeaseLost.
	cond, cargs := f.where(cur)
	// Ask the router for a fallback outside the transaction (it may query other tables).
	var reroute *Reroute
	canceled := jerr.Code == ErrCodeCanceled || cur.CancelRequested
	if !canceled && jerr.Code != ErrCodeBadInput && cur.RoundAttempt() < cur.MaxAttempts*2 {
		q.mu.RLock()
		fo := q.failover
		q.mu.RUnlock()
		if fo != nil {
			rr, ferr := fo(ctx, cur, jerr)
			if ferr != nil {
				q.log.WarnContext(ctx, "jobs: failover", "job", id, "err", ferr)
			} else {
				reroute = rr
			}
		}
	}
	var j *Job
	var event string
	err = q.db.WithTx(ctx, func(tx *db.Tx) error {
		now := q.now()
		total := cur.Actual.Add(actual)
		errJSON := db.MustJSON(jerr)
		var res interface{ RowsAffected() (int64, error) }
		var err error
		switch {
		case canceled:
			event = "job.canceled"
			res, err = tx.Exec(ctx, `UPDATE jobs SET state = 'canceled', error = ?, actual = ?, lease_expires_at = NULL, worker_id = NULL,
				finished_at = ?, updated_at = ? WHERE id = ?`+cond, append([]any{errJSON, db.MustJSON(total), now, now, id}, cargs...)...)
			if err == nil {
				err = addEvent(ctx, tx, id, "canceled", jerr.Message, nil)
			}
		case reroute != nil:
			event = "job.fallback"
			exec := cur.Executor
			if reroute.Executor != "" {
				exec = reroute.Executor
			}
			decision, _ := marshalObj(reroute.Decision)
			if decision == "{}" && cur.Decision != nil {
				decision = string(cur.Decision)
			}
			res, err = tx.Exec(ctx, `UPDATE jobs SET state = 'queued', executor = ?, model_id = ?, provider_id = ?, decision = ?, error = ?,
				actual = ?, progress = 0, step = 0, message = '', worker_id = NULL, lease_expires_at = NULL, run_after = NULL, updated_at = ?
				WHERE id = ?`+cond, append([]any{exec, reroute.ModelID, reroute.ProviderID, decision, errJSON, db.MustJSON(total), now, id}, cargs...)...)
			if err == nil {
				err = addEvent(ctx, tx, id, "fallback", reroute.Reason, map[string]any{"from_model": cur.ModelID, "from_provider": cur.ProviderID,
					"to_model": reroute.ModelID, "to_provider": reroute.ProviderID, "error": jerr})
			}
		case jerr.Retryable && cur.RoundAttempt() < cur.MaxAttempts:
			event = "job.retry"
			delay := q.opts.Backoff * time.Duration(1<<min(max(cur.RoundAttempt()-1, 0), 6))
			res, err = tx.Exec(ctx, `UPDATE jobs SET state = 'queued', error = ?, actual = ?, progress = 0, step = 0, message = '',
				worker_id = NULL, lease_expires_at = NULL, run_after = ?, updated_at = ? WHERE id = ?`+cond, append([]any{errJSON, db.MustJSON(total), now.Add(delay), now, id}, cargs...)...)
			if err == nil {
				err = addEvent(ctx, tx, id, "retry", jerr.Message, map[string]any{"error": jerr, "attempt": cur.Attempts, "delay_s": delay.Seconds()})
			}
		default:
			event = "job.failed"
			res, err = tx.Exec(ctx, `UPDATE jobs SET state = 'failed', error = ?, actual = ?, lease_expires_at = NULL, worker_id = NULL,
				finished_at = ?, updated_at = ? WHERE id = ?`+cond, append([]any{errJSON, db.MustJSON(total), now, now, id}, cargs...)...)
			if err == nil {
				err = addEvent(ctx, tx, id, "failed", jerr.Message, map[string]any{"error": jerr})
			}
		}
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrLeaseLost
		}
		j, err = q.get(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	if j.Finished() {
		q.finished(ctx, j, event)
	} else {
		q.publish(j, event, "")
		q.signal()
	}
	return j, nil
}

// Cancel cancels a queued job immediately, or requests cancellation of a
// running one (workers learn it from heartbeat/progress responses, Go
// handlers through their context).
func (q *Queue) Cancel(ctx context.Context, id string) (*Job, error) {
	for try := 0; ; try++ {
		j, requested, err := q.cancelOnce(ctx, id)
		if errors.Is(err, errStateChanged) && try < 5 {
			continue
		}
		if errors.Is(err, errStateChanged) {
			err = ErrNotActive
		}
		if err != nil {
			return nil, err
		}
		if requested {
			q.mu.RLock()
			hooks := append([]func(string){}, q.onCancel...)
			q.mu.RUnlock()
			for _, h := range hooks {
				h(id)
			}
			q.publish(j, "job.cancel_requested", "")
		} else {
			q.finished(ctx, j, "job.canceled")
		}
		return j, nil
	}
}

// errStateChanged means a job changed state between reading and writing it.
var errStateChanged = errors.New("jobs: state changed concurrently")

func (q *Queue) cancelOnce(ctx context.Context, id string) (*Job, bool, error) {
	var j *Job
	var requested bool
	err := q.db.WithTx(ctx, func(tx *db.Tx) error {
		cur, err := q.get(ctx, tx, id)
		if err != nil {
			return err
		}
		now := q.now()
		switch cur.State {
		case StateQueued:
			res, err := tx.Exec(ctx, `UPDATE jobs SET state = 'canceled', finished_at = ?, updated_at = ? WHERE id = ? AND state = 'queued'`, now, now, id)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return errStateChanged // claimed meanwhile: retry as a running job
			}
			if err := addEvent(ctx, tx, id, "canceled", "canceled before start", nil); err != nil {
				return err
			}
		case StateRunning:
			res, err := tx.Exec(ctx, `UPDATE jobs SET cancel_requested = TRUE, updated_at = ? WHERE id = ? AND state = 'running'`, now, id)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return errStateChanged // finished or requeued meanwhile
			}
			requested = true
			if err := addEvent(ctx, tx, id, "cancel_requested", "", nil); err != nil {
				return err
			}
		default:
			return ErrNotActive
		}
		j, err = q.get(ctx, tx, id)
		return err
	})
	return j, requested, err
}

// Retry requeues a failed or canceled job with a fresh attempt budget. The
// attempt counter is not reset: a new round starts at attempt_base, so a
// handler still running from an earlier round can never match the lease of
// the next claim (heartbeat, spend reservation and completion all pin it).
func (q *Queue) Retry(ctx context.Context, id string) (*Job, error) {
	var j *Job
	err := q.db.WithTx(ctx, func(tx *db.Tx) error {
		now := q.now()
		res, err := tx.Exec(ctx, `UPDATE jobs SET state = 'queued', attempt_base = attempts, progress = 0, step = 0, message = '', error = '{}',
			output = '{}', cancel_requested = FALSE, worker_id = NULL, lease_expires_at = NULL, run_after = NULL, finished_at = NULL, updated_at = ?
			WHERE id = ? AND state IN ('failed', 'canceled')`, now, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			if _, err := q.get(ctx, tx, id); err != nil {
				return err
			}
			return ErrNotActive
		}
		if err := addEvent(ctx, tx, id, "retried", "", nil); err != nil {
			return err
		}
		j, err = q.get(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	q.publish(j, "job.queued", "")
	q.signal()
	return j, nil
}

// RequeueExpired handles running jobs whose lease ran out (worker crashed,
// network gone): they are retried, rerouted, canceled or failed like a
// timeout. It returns how many jobs were affected.
func (q *Queue) RequeueExpired(ctx context.Context) (int, error) {
	var ids []string
	now := q.now()
	if err := q.db.Select(ctx, &ids, `SELECT id FROM jobs WHERE state = 'running' AND lease_expires_at IS NOT NULL AND lease_expires_at < ?`, now); err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		// The fence re-checks expiry at write time: a heartbeat that lands in between keeps the job.
		if _, err := q.fail(ctx, id, fence{expiredBefore: now}, JobError{Code: ErrCodeTimeout, Message: "lease expired (worker stopped responding)", Retryable: true}, Cost{}); err == nil {
			n++
		} else if !errors.Is(err, ErrLeaseLost) {
			q.log.WarnContext(ctx, "jobs: requeue expired", "job", id, "err", err)
		}
	}
	return n, nil
}

// Wait blocks until the job finishes or ctx ends, polling every interval.
func (q *Queue) Wait(ctx context.Context, id string, interval time.Duration) (*Job, error) {
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	for {
		j, err := q.Get(ctx, id)
		if err != nil || j.Finished() {
			return j, err
		}
		select {
		case <-ctx.Done():
			return j, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func (q *Queue) finished(ctx context.Context, j *Job, event string) {
	defer q.signal() // a finished job frees a fair-share slot
	func() {
		defer func() {
			if r := recover(); r != nil {
				q.log.ErrorContext(ctx, "jobs: publisher panicked", "job", j.ID, "panic", r)
			}
		}()
		q.publish(j, event, "")
	}()
	q.mu.RLock()
	hooks := append([]func(context.Context, *Job){}, q.onFinish...)
	q.mu.RUnlock()
	for _, h := range hooks {
		func() {
			defer func() {
				if r := recover(); r != nil {
					q.log.ErrorContext(ctx, "jobs: finish hook panicked", "job", j.ID, "panic", r)
				}
			}()
			h(context.WithoutCancel(ctx), j)
		}()
	}
}

func (q *Queue) publish(j *Job, typ, preview string) {
	q.mu.RLock()
	pub := q.pub
	q.mu.RUnlock()
	if pub == nil {
		return
	}
	s := j.Summary()
	s.Preview = preview
	pub.Publish("job:"+j.ID, typ, s)
	if j.UserID != "" {
		pub.Publish("user:"+j.UserID, typ, s)
	}
	if j.Scope.ProjectID != "" {
		pub.Publish("project:"+j.Scope.ProjectID, typ, s)
	}
	pub.Publish("admin:queue", typ, s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
