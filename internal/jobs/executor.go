package jobs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// Handler runs one Go-executor job. Return nil on success (after SetOutput),
// a *JobError to control retry behaviour, or any other error (treated as a
// non-retryable internal failure). ctx is canceled when the user cancels.
type Handler func(ctx context.Context, run *Run) error

// Run is the handle a Handler uses to report progress and results.
type Run struct {
	Job *Job

	q      *Queue
	output any
	actual Cost
	mu     sync.Mutex

	commitMu  sync.Mutex
	committed bool
}

// DecodeInput unmarshals the job input into v.
func (r *Run) DecodeInput(v any) error { return r.Job.DecodeInput(v) }

// Progress reports progress (0–1) and an optional step count and message.
// It returns ErrCanceled when the user canceled the job.
func (r *Run) Progress(ctx context.Context, p float64, step, steps int, msg string) error {
	cancel, err := r.q.progress(ctx, r.Job.ID, r.fence(), ProgressUpdate{Progress: p, Step: step, Steps: steps, Message: msg})
	if err != nil {
		return err
	}
	if cancel {
		return ErrCanceled
	}
	return nil
}

// fence pins writes to the lease this run claimed (Go executor, this attempt).
func (r *Run) fence() fence {
	none := ""
	return fence{workerID: &none, attempt: r.Job.Attempts}
}

// Log appends a message to the job's event log.
func (r *Run) Log(ctx context.Context, msg string, data any) {
	_ = r.q.Log(ctx, r.Job.ID, "log", msg, data)
}

// SetOutput sets the job output stored on success.
func (r *Run) SetOutput(v any) {
	r.mu.Lock()
	r.output = v
	r.mu.Unlock()
}

// AddActual adds to the actual cost (GPU seconds, USD spent on the user's key).
func (r *Run) AddActual(c Cost) {
	r.mu.Lock()
	r.actual = r.actual.Add(c)
	r.mu.Unlock()
}

// CommitSideEffect atomically applies fn's database writes and completes the
// exact job attempt held by this Run. fn must use only tx for database access;
// it must not perform network or filesystem I/O, call Queue methods, or use a
// separate database handle. Keep it short because it holds the job row lock.
// Call AddActual before CommitSideEffect so that usage is included in the same
// transaction. The returned output is marshaled and stored before commit.
func (r *Run) CommitSideEffect(ctx context.Context, fn func(context.Context, *db.Tx) (output any, err error)) error {
	if fn == nil {
		return errors.New("jobs: side effect callback is nil")
	}

	r.commitMu.Lock()
	defer r.commitMu.Unlock()

	r.mu.Lock()
	if r.committed {
		r.mu.Unlock()
		return ErrAlreadyCommitted
	}
	actual := r.actual
	r.mu.Unlock()

	j, err := r.q.completeTx(ctx, r.Job.ID, r.fence(), actual, true, fn)
	if err != nil {
		return err
	}

	// Mark the Run before any after-commit observers execute. Pool.run uses this
	// flag to avoid a second terminal transition even if the handler later errs.
	r.mu.Lock()
	r.committed = true
	r.mu.Unlock()
	r.q.finished(ctx, j, "job.done")
	return nil
}

// Errors returned by a Run.
var (
	// ErrCanceled is returned after the user canceled the job.
	ErrCanceled = errors.New("jobs: canceled")
	// ErrAlreadyCommitted is returned by a second CommitSideEffect call.
	ErrAlreadyCommitted = errors.New("jobs: side effect already committed")
)

// Pool runs Go-executor jobs with registered handlers.
type Pool struct {
	q        *Queue
	log      *slog.Logger
	size     int
	mu       sync.RWMutex
	handlers map[string]Handler
	running  map[string]context.CancelFunc
	wg       sync.WaitGroup
	poll     time.Duration
}

// NewPool creates a pool with size concurrent runners.
func NewPool(q *Queue, size int, log *slog.Logger) *Pool {
	if size <= 0 {
		size = 4
	}
	if log == nil {
		log = slog.Default()
	}
	p := &Pool{q: q, log: log, size: size, handlers: map[string]Handler{}, running: map[string]context.CancelFunc{}, poll: 2 * time.Second}
	q.OnCancel(p.cancel)
	return p
}

// Register adds a handler for a job type. Registering a type twice panics.
func (p *Pool) Register(jobType string, h Handler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, dup := p.handlers[jobType]; dup {
		panic(fmt.Sprintf("jobs: handler for %q registered twice", jobType))
	}
	p.handlers[jobType] = h
}

// Types lists registered job types.
func (p *Pool) Types() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.handlers))
	for t := range p.handlers {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func (p *Pool) cancel(jobID string) {
	p.mu.RLock()
	c := p.running[jobID]
	p.mu.RUnlock()
	if c != nil {
		c()
	}
}

// Start runs the pool until ctx is canceled, then waits for running handlers.
func (p *Pool) Start(ctx context.Context) error {
	for i := 0; i < p.size; i++ {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.loop(ctx)
		}()
	}
	<-ctx.Done()
	p.wg.Wait()
	return nil
}

func (p *Pool) loop(ctx context.Context) {
	for ctx.Err() == nil {
		types := p.Types()
		if len(types) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(p.poll):
			}
			continue
		}
		j, err := p.q.ClaimWait(ctx, ClaimParams{Executor: ExecutorGo, Types: types}, p.poll)
		if err != nil {
			p.log.Error("jobs: pool claim", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(p.poll):
			}
			continue
		}
		if j != nil {
			p.run(ctx, j)
		}
	}
}

// RunOnce claims and runs a single job synchronously (tests and CLI tools).
// It returns false when nothing was runnable.
func (p *Pool) RunOnce(ctx context.Context) (bool, error) {
	j, err := p.q.Claim(ctx, ClaimParams{Executor: ExecutorGo, Types: p.Types()})
	if err != nil || j == nil {
		return false, err
	}
	p.run(ctx, j)
	return true, nil
}

func (p *Pool) run(parent context.Context, j *Job) {
	p.mu.RLock()
	h := p.handlers[j.Type]
	p.mu.RUnlock()
	// Handlers keep running through shutdown until their own context ends;
	// the lease makes an interrupted job come back after a restart.
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	p.mu.Lock()
	p.running[j.ID] = cancel
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.running, j.ID)
		p.mu.Unlock()
		cancel()
	}()
	if j.CancelRequested {
		cancel()
	}

	// Keep the lease alive while the handler runs.
	stopRenew := make(chan struct{})
	go func() {
		t := time.NewTicker(p.q.opts.LeaseGo / 3)
		defer t.Stop()
		for {
			select {
			case <-stopRenew:
				return
			case <-t.C:
				if err := p.q.heartbeat(ctx, j.ID, "", j.Attempts); errors.Is(err, ErrLeaseLost) {
					cancel()
					return
				}
				if cur, err := p.q.Get(ctx, j.ID); err == nil && cur.CancelRequested {
					cancel()
				}
			}
		}
	}()

	run := &Run{Job: j, q: p.q}
	err := p.invoke(ctx, h, run)
	close(stopRenew)

	// Serialize the ordinary terminal path with CommitSideEffect. Synchronous
	// handlers have already released this lock; it also makes a mistakenly
	// detached commit race safe by allowing only one path to win the lease.
	run.commitMu.Lock()
	defer run.commitMu.Unlock()

	bg := context.WithoutCancel(parent)
	run.mu.Lock()
	out, actual, committed := run.output, run.actual, run.committed
	run.mu.Unlock()
	if committed {
		if err != nil {
			p.log.Warn("jobs: handler returned an error after committing", "job", j.ID, "err", err)
		}
		return
	}
	switch {
	case h == nil:
		_, err = p.q.fail(bg, j.ID, run.fence(), JobError{Code: ErrCodeInternal, Message: "no handler registered for " + j.Type}, actual)
	case err == nil:
		_, err = p.q.complete(bg, j.ID, run.fence(), out, actual)
	default:
		_, err = p.q.fail(bg, j.ID, run.fence(), toJobError(ctx, err), actual)
	}
	if err != nil && !errors.Is(err, ErrLeaseLost) {
		p.log.Error("jobs: record result", "job", j.ID, "err", err)
	}
}

func (p *Pool) invoke(ctx context.Context, h Handler, run *Run) (err error) {
	if h == nil {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			p.log.Error("jobs: handler panicked", "job", run.Job.ID, "type", run.Job.Type, "panic", r, "stack", string(debug.Stack()))
			err = &JobError{Code: ErrCodeInternal, Message: fmt.Sprintf("handler panicked: %v", r)}
		}
	}()
	return h(ctx, run)
}

func toJobError(ctx context.Context, err error) JobError {
	var je *JobError
	switch {
	case errors.As(err, &je):
		return *je
	case errors.Is(err, ErrCanceled), errors.Is(err, context.Canceled) && ctx.Err() != nil:
		return JobError{Code: ErrCodeCanceled, Message: "canceled"}
	case errors.Is(err, context.DeadlineExceeded):
		return JobError{Code: ErrCodeTimeout, Message: err.Error(), Retryable: true}
	}
	return JobError{Code: ErrCodeInternal, Message: err.Error()}
}

func base64Std(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// MarshalOutput is a helper for handlers that build outputs incrementally.
func MarshalOutput(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
