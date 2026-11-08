package update

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/backup"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

func authEntry(action, id string, data map[string]any) auth.AuditEntry {
	return auth.AuditEntry{Action: action, TargetKind: "update", TargetID: id, Data: data}
}

// resume runs once when the server starts: it finishes an update whose new
// binary is now running (clean up, maintenance off, delete the pre-update
// backup unless kept), finishes a container update once the new image runs,
// recovers an install that a crash interrupted and drops offers this version
// already satisfies.
func (m *Module) resume(ctx context.Context) error {
	running, err := ParseSemver(m.running())
	if err != nil {
		return nil // a development build: nothing to finish
	}
	rec, err := m.latestAttempt(ctx)
	if err != nil {
		return err
	}
	if rec != nil && isActive(rec.Status) {
		run := rec.Run()
		switch {
		case run.Phase == PhaseRestarting:
			if err := m.finishRestarted(ctx, rec, run, running); err != nil {
				return err
			}
		default:
			if err := m.recoverInterrupted(ctx, rec, run); err != nil {
				return err
			}
		}
	}
	offer, err := m.offer(ctx)
	if err != nil || offer == nil {
		return err
	}
	to, err := ParseSemver(offer.ToVersion)
	if err != nil || to.Compare(running) > 0 {
		return err
	}
	if run := offer.Run(); run.Mode == ModeContainer && offer.BackupID != nil && offer.StartedAt != nil {
		return m.finishContainer(ctx, offer, run, running)
	}
	return m.dropOffer(ctx, offer.ID)
}

// takeLock takes the install lock for rec on start: from the restart hand-off,
// after an expired lease, or fresh.
func (m *Module) takeLock(ctx context.Context, rec *Record) (lockValue, bool, error) {
	mine := lockValue{UpdateID: rec.ID, Owner: m.owner}
	raw, _, ok, err := m.readLock(ctx)
	if err != nil {
		return mine, false, err
	}
	if ok {
		if v := decodeLock(raw); v.UpdateID == rec.ID && v.Owner == restartOwner {
			got, err := m.swapLock(ctx, raw, mine)
			return mine, got, err
		}
	}
	got, err := m.acquireLock(ctx, mine)
	return mine, got, err
}

func (m *Module) finishRestarted(ctx context.Context, rec *Record, run Run, running Semver) error {
	lock, got, err := m.takeLock(ctx, rec)
	if err != nil || !got {
		return err // another process is finishing it
	}
	defer m.releaseLock(ctx, lock)
	to, _ := ParseSemver(rec.ToVersion)
	if running.Compare(to) != 0 {
		// The swap happened, but this process is another version (someone put
		// the previous binary back). Keep the backup and say so.
		run.setStep(StepCleanup, StateFailed, fmt.Sprintf("the server restarted on %s, not %s", running, rec.ToVersion))
		run.Code = "UPD-RESTART"
		run.logf(fmt.Sprintf("Restarted on %s instead of %s; the backup is kept.", running, rec.ToVersion))
		rec.Status, rec.Error = StatusFailed, "restarted on "+running.String()
		now := db.Now()
		rec.FinishedAt = &now
		m.endHold(ctx, run)
		return m.saveRun(ctx, rec, run)
	}
	run.logf(fmt.Sprintf("Version %s started.", rec.ToVersion))
	m.cleanup(ctx, rec, &run)
	m.endHold(ctx, run)
	run.logf("Maintenance mode off. Update finished.")
	if err := m.saveRun(ctx, rec, run); err != nil {
		return err
	}
	if m.d.Auth != nil {
		m.d.Auth.Audit(ctx, authEntry("update.done", rec.ID, map[string]any{"from": rec.FromVersion, "to": rec.ToVersion}))
	}
	return nil
}

// cleanup is step 5: delete the pre-update backup unless kept (and the
// backups of earlier failed attempts, kept "until an update succeeds"),
// remove the download and mark the attempt done.
func (m *Module) cleanup(ctx context.Context, rec *Record, run *Run) {
	run.setStep(StepCleanup, StateRunning, "")
	svc, ok := m.backups()
	if id := rec.backupID(); id != "" && !run.Keep && ok {
		if err := svc.Delete(ctx, id); err != nil && !errors.Is(err, backup.ErrNotFound) {
			run.logf("Could not delete the pre-update backup: " + err.Error())
		} else {
			run.BackupDeleted = true
			run.logf("Deleted the pre-update backup " + id + ".")
		}
	} else if run.Keep {
		run.logf("Kept the pre-update backup for 7 days.")
	}
	if ok {
		var old []struct {
			ID       string `db:"id"`
			BackupID string `db:"backup_id"`
		}
		_ = m.d.DB.Select(ctx, &old, `SELECT id, backup_id FROM updates WHERE id <> ? AND status = ? AND backup_id IS NOT NULL`,
			rec.ID, StatusRolledBack)
		for _, o := range old {
			if err := svc.Delete(ctx, o.BackupID); err == nil || errors.Is(err, backup.ErrNotFound) {
				_, _ = m.d.DB.Exec(ctx, `UPDATE updates SET backup_id = NULL WHERE id = ?`, o.ID)
			}
		}
	}
	_ = os.RemoveAll(m.updatesDir(rec.ToVersion))
	run.setStep(StepCleanup, StateDone, "")
	run.Phase = ""
	rec.Status = StatusDone
	now := db.Now()
	rec.FinishedAt = &now
}

func (m *Module) finishContainer(ctx context.Context, rec *Record, run Run, running Semver) error {
	lock, got, err := m.takeLock(ctx, rec)
	if err != nil || !got {
		return err
	}
	defer m.releaseLock(ctx, lock)
	run.setStep(StepDownload, StateSkipped, "")
	run.setStep(StepMigrate, StateDone, "")
	run.setStep(StepHealth, StateDone, "")
	run.logf(fmt.Sprintf("The container now runs %s.", running))
	m.cleanup(ctx, rec, &run)
	if err := m.saveRun(ctx, rec, run); err != nil {
		return err
	}
	if m.d.Auth != nil {
		m.d.Auth.Audit(ctx, authEntry("update.done", rec.ID, map[string]any{"from": rec.FromVersion, "to": rec.ToVersion, "mode": ModeContainer}))
	}
	return nil
}

// recoverInterrupted handles an install whose process died (its lease ran
// out): the backup is restored when one was taken, as for any failure before
// the swap.
func (m *Module) recoverInterrupted(ctx context.Context, rec *Record, run Run) error {
	lock, got, err := m.takeLock(ctx, rec)
	if err != nil || !got {
		return err // still running in another process
	}
	defer m.releaseLock(ctx, lock)
	svc, _ := m.backups()
	step := StepBackup
	for _, s := range run.Steps {
		if s.State == StateRunning {
			step = s.Key
		}
	}
	actor := ""
	if rec.StartedBy != nil {
		actor = *rec.StartedBy
	}
	// Take the interrupted install's hold over (it may still look live after a
	// quick restart), so the restore runs with maintenance owned and nothing
	// else holding the backup lease. If other backup work holds the server,
	// the next tick tries again; maintenance stays on meanwhile.
	var lease backup.Lease
	if h, ok := svc.(backup.Holder); ok {
		l, err := h.Hold(ctx, backup.HoldRequest{What: backup.HoldUpdate, Message: m.maintenanceMessage(ctx, rec.ToVersion), ActorID: actor,
			Resume: run.HoldID})
		if err != nil {
			return fmt.Errorf("update: hold the server to recover the interrupted update: %w", err)
		}
		lease = l
		if run.Maintenance == nil {
			before := l.Info().Before
			run.Maintenance = &before
		}
	}
	m.fail(ctx, rec, &run, step, "UPD-INTERRUPTED", errors.New("the server stopped while the update was running"), svc, lease, actor, "")
	return nil
}
