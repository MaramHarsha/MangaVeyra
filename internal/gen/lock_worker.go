package gen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

var errLockChildExists = errors.New("lock child exists")

// errLockOutput is a completed worker result that fails validation.
var errLockOutput = errors.New("gen: invalid Character Lock worker result")

// errLockArtifact is a training result that is not an owned, real, compatible
// LoRA for the current references and base model.
var errLockArtifact = errors.New("gen: rejected Character Lock training artifact")

// lockChildError is a worker child that finished without completing. Its
// state and safe protocol code decide the parent's outcome (mapLockJobError);
// the worker's own message is never copied to the parent.
type lockChildError struct{ State, Code string }

func (e lockChildError) Error() string {
	return "gen: Character Lock worker " + e.State + " (" + e.Code + ")"
}

// lockProgress mirrors a running worker child onto its parent job.
type lockProgress func(context.Context, *jobs.Job) error

// runLockWorker bridges to an explicitly configured worker. Inputs are private,
// short lived, owned assets. The parent lease, permission, local-only setting
// and exact target locks remain authoritative throughout the bridge.
func (s *service) runLockWorker(ctx context.Context, run *jobs.Run, kind, model, slot string, scope jobs.Scope, input map[string]any, data map[string][]byte, progress lockProgress) (*jobs.Job, error) {
	total := 0
	for _, v := range data {
		total += len(v)
	}
	if total > 48<<20 {
		return nil, studio.ErrInvalid
	}
	raw, _ := json.Marshal(input)
	h := sha256.New()
	h.Write(raw)
	names := make([]string, 0, len(data))
	for name := range data {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		h.Write([]byte(name))
		h.Write(data[name])
	}
	digest := hex.EncodeToString(h.Sum(nil))
	input["_parent_attempt"], input["_slot"], input["_digest"] = run.Job.Attempts, slot, digest
	files := map[string]string{}
	saved := []string{}
	defer func() {
		for _, id := range saved {
			_ = s.assets.Delete(context.WithoutCancel(ctx), id)
		}
	}()
	for _, name := range names {
		a, err := s.assets.Save(ctx, assets.SaveParams{OwnerID: run.Job.UserID, ProjectID: scope.ProjectID, Kind: "generation_input", Source: assets.SourceSystem, ContentType: "image/png", Data: data[name]})
		if err != nil {
			return nil, err
		}
		saved = append(saved, a.ID)
		files[name] = a.Key
	}
	existing := ""
	child, err := s.queue.Enqueue(ctx, jobs.EnqueueParams{Type: kind, Scope: scope, UserID: run.Job.UserID, Executor: jobs.ExecutorWorker, ProviderID: "local-worker", ModelID: model, ParentID: run.Job.ID, Input: input, Files: files, MaxAttempts: 1, Guard: func(ctx context.Context, tx *db.Tx) error {
		var cur struct {
			State    string `db:"state"`
			Attempts int    `db:"attempts"`
			Canceled bool   `db:"cancel_requested"`
		}
		if err := tx.Get(ctx, &cur, `SELECT state,attempts,cancel_requested FROM jobs WHERE id=?`+lockSQL(tx), run.Job.ID); err != nil {
			return err
		}
		if cur.State != jobs.StateRunning || cur.Attempts != run.Job.Attempts || cur.Canceled {
			return jobs.ErrLeaseLost
		}
		inst, err := s.settings.InstanceTx(ctx, tx)
		if err != nil {
			return err
		}
		if inst.LocalOnly {
			return studio.ErrForbidden
		}
		if err := s.authorizeImageTx(ctx, tx, studio.Actor{UserID: run.Job.UserID, TokenID: storedToken(run.Job.Input)}, scope.ProjectID); err != nil {
			return err
		}
		if err := s.studio.CheckScopeWritableTx(ctx, tx, scope); err != nil {
			return err
		}
		var children []struct {
			ID    string `db:"id"`
			Input string `db:"input"`
		}
		if err := tx.Select(ctx, &children, `SELECT id,input FROM jobs WHERE parent_id=? AND type=? ORDER BY id LIMIT 1025`, run.Job.ID, kind); err != nil {
			return err
		}
		if len(children) > 1024 {
			return studio.ErrInvalid
		}
		for _, c := range children {
			var p struct {
				Attempt int    `json:"_parent_attempt"`
				Slot    string `json:"_slot"`
				Digest  string `json:"_digest"`
			}
			if json.Unmarshal([]byte(c.Input), &p) == nil && p.Attempt == run.Job.Attempts && p.Slot == slot {
				if p.Digest != digest {
					return studio.ErrConflict
				}
				existing = c.ID
				return errLockChildExists
			}
		}
		return nil
	}})
	if errors.Is(err, errLockChildExists) {
		child, err = s.queue.Get(ctx, existing)
	}
	if err != nil {
		return nil, err
	}
	defer func() {
		if child != nil && !child.Finished() {
			bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_, _ = s.queue.Cancel(bg, child.ID)
		}
	}()
	timeout := 2 * time.Minute
	if kind == "character.train" {
		timeout = 4 * time.Hour
	}
	wait, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	mirrored := -1.0
	for !child.Finished() {
		select {
		case <-wait.Done():
			return child, wait.Err()
		case <-tick.C:
		}
		current, err := s.queue.Get(wait, child.ID)
		if err != nil {
			return child, err
		}
		child = current
		parent, err := s.queue.Get(wait, run.Job.ID)
		if err != nil || parent.State != jobs.StateRunning || parent.CancelRequested || parent.Attempts != run.Job.Attempts {
			return child, jobs.ErrLeaseLost
		}
		if progress != nil && !child.Finished() && child.Progress != mirrored {
			if err := progress(wait, child); err != nil {
				return child, err
			}
			mirrored = child.Progress
		}
	}
	if child.State != jobs.StateDone {
		code := ""
		if child.Error != nil {
			code = child.Error.Code
		}
		return child, lockChildError{State: child.State, Code: code}
	}
	return child, nil
}

// ErrCodeArtifactRejected is the job error code of a training job whose
// worker returned an artifact that cannot become a ready Character Lock.
const ErrCodeArtifactRejected = "artifact_rejected"

// mapLockJobError maps Character Lock assessment/training failures to safe job
// errors. A canceled worker child cancels the parent (no failure notice); a
// worker failure keeps its own code instead of claiming the target changed.
func mapLockJobError(ctx context.Context, err error) error {
	var child lockChildError
	switch {
	case errors.Is(err, jobs.ErrLeaseLost):
		return jobs.ErrLeaseLost
	case ctx.Err() != nil || errors.Is(err, jobs.ErrCanceled):
		return jobs.Permanent(jobs.ErrCodeCanceled, errors.New("Character Lock job canceled."))
	case errors.As(err, &child):
		switch {
		case child.State == jobs.StateCanceled || child.Code == jobs.ErrCodeCanceled:
			return jobs.Permanent(jobs.ErrCodeCanceled, errors.New("The worker job was canceled."))
		case child.Code == jobs.ErrCodeTimeout:
			return jobs.Retryable(jobs.ErrCodeTimeout, errors.New("The Character Lock worker timed out."))
		case child.Code == jobs.ErrCodeOOM:
			return jobs.Retryable(jobs.ErrCodeOOM, errors.New("The Character Lock worker ran out of memory."))
		case child.Code == jobs.ErrCodeSafety:
			return jobs.Permanent(jobs.ErrCodeSafety, errors.New("The Character Lock worker declined these images."))
		case child.Code == jobs.ErrCodeBadInput:
			return jobs.Permanent(jobs.ErrCodeBadInput, errors.New("The Character Lock worker could not read these images."))
		}
		return jobs.Permanent(ai.CodeUpstream, errors.New("The Character Lock worker could not complete this job."))
	case errors.Is(err, context.DeadlineExceeded):
		return jobs.Retryable(jobs.ErrCodeTimeout, errors.New("The Character Lock worker did not finish in time."))
	case errors.Is(err, errLockOutput):
		return jobs.Permanent(ai.CodeBadOutput, errors.New("The worker returned an invalid Character Lock result."))
	case errors.Is(err, errLockArtifact):
		return jobs.Permanent(ErrCodeArtifactRejected, errors.New("The worker's training artifact was rejected: it is not a real LoRA for this character's current references and base model."))
	case errors.Is(err, ErrDeferredFeature):
		return jobs.Permanent(ai.CodeNoProvider, errors.New("No compatible Character Lock worker is available."))
	}
	return mapImageJobError(ctx, err)
}
func storedToken(raw json.RawMessage) string {
	var p struct {
		Actor storedActor `json:"actor"`
	}
	_ = json.Unmarshal(raw, &p)
	return p.Actor.TokenID
}
