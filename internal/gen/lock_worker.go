package gen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

var errLockChildExists = errors.New("lock child exists")

// runLockWorker bridges to an explicitly configured worker. Inputs are private,
// short lived, owned assets. The parent lease, permission, local-only setting
// and exact target locks remain authoritative throughout the bridge.
func (s *service) runLockWorker(ctx context.Context, run *jobs.Run, kind, model, slot string, scope jobs.Scope, input map[string]any, data map[string][]byte) (*jobs.Job, error) {
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
		if kind == "character.train" {
			if err := run.Progress(wait, child.Progress, child.Step, child.Steps, "Training Character Lock"); err != nil {
				return child, err
			}
		}
	}
	if child.State != jobs.StateDone {
		return child, fmt.Errorf("%w: worker did not complete", studio.ErrInvalid)
	}
	return child, nil
}
func storedToken(raw json.RawMessage) string {
	var p struct {
		Actor storedActor `json:"actor"`
	}
	_ = json.Unmarshal(raw, &p)
	return p.Actor.TokenID
}
