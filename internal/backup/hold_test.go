package backup_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/admin/adminapi"
	"github.com/MaramHarsha/MangaVeyra/internal/backup"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func instMaintenance(t *testing.T, ta *testutil.TestApp) settings.Maintenance {
	t.Helper()
	ta.Deps.Settings.Invalidate()
	return ta.Deps.Settings.MustInstance(context.Background()).Maintenance
}

// An update's hold owns maintenance mode and the backup lease until it ends:
// the admin's toggle is refused naming the update, no other process can back
// up or restore, the job queue claims nothing, and the update's own backup
// and restore run under the hold without a lease of their own.
func TestHoldOwnsMaintenanceAndTheLease(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		seedServer(t, ta)
		s := svcOf(t, ta)
		guard := core.MustUse[adminapi.MaintenanceGuard](ta.Deps)
		l, err := core.MustUse[backup.Holder](ta.Deps).Hold(ctx, backup.HoldRequest{What: backup.HoldUpdate, Message: "Updating to 1.0.2", ActorID: ta.Admin.ID})
		if err != nil {
			t.Fatal(err)
		}
		if m := instMaintenance(t, ta); !m.Enabled || m.Message != "Updating to 1.0.2" {
			t.Fatalf("maintenance under the hold = %+v", m)
		}
		var held *adminapi.MaintenanceHeldError
		if err := guard.SetMaintenance(ctx, ta.Admin.ID, settings.Maintenance{}); !errors.As(err, &held) || held.What != backup.HoldUpdate {
			t.Fatalf("turning maintenance off under an update: %v", err)
		}
		if !instMaintenance(t, ta).Enabled {
			t.Fatal("maintenance was turned off under the hold")
		}
		other := backup.NewTestService(ta.Deps)
		if _, err := other.Create(ctx, backup.Request{Kind: backup.KindManual}); !errors.Is(err, backup.ErrBusy) {
			t.Fatalf("another process backed up under the hold: %v", err)
		}
		if _, err := other.Hold(ctx, backup.HoldRequest{What: backup.HoldRestore}); !errors.Is(err, backup.ErrBusy) {
			t.Fatalf("another process held the server: %v", err)
		}
		if ok, err := s.ClaimsAllowedNow(ctx); err != nil || ok {
			t.Fatalf("claims allowed under the hold: %v %v", ok, err)
		}
		b, err := s.Create(l.Context(ctx), backup.Request{Kind: backup.KindPreUpdate})
		if err != nil {
			t.Fatalf("the update's own backup: %v", err)
		}
		if err := s.Restore(l.Context(ctx), b.ID, ta.Admin.ID); err != nil {
			t.Fatalf("the update's own restore: %v", err)
		}
		// The restore keeps the hold (backup.* settings are this server's).
		if err := guard.SetMaintenance(ctx, ta.Admin.ID, settings.Maintenance{}); !errors.As(err, &held) {
			t.Fatalf("the hold did not survive the restore: %v", err)
		}
		before := l.Info().Before
		if err := l.Release(ctx, &before); err != nil {
			t.Fatal(err)
		}
		if instMaintenance(t, ta).Enabled {
			t.Fatal("maintenance stayed on after the hold")
		}
		if ok, _ := s.ClaimsAllowedNow(ctx); !ok {
			t.Fatal("claims refused after the hold")
		}
		if err := guard.SetMaintenance(ctx, ta.Admin.ID, settings.Maintenance{Enabled: true, Message: "Moving disks"}); err != nil {
			t.Fatalf("toggle after the hold: %v", err)
		}
		if m := instMaintenance(t, ta); !m.Enabled || m.Message != "Moving disks" {
			t.Fatalf("maintenance = %+v", m)
		}
		if _, err := other.Create(ctx, backup.Request{Kind: backup.KindManual}); err != nil {
			t.Fatalf("backup after the hold: %v", err)
		}
	})
}

// A hold waits (bounded, logging) for a backup another process is writing.
func TestHoldWaitsForRunningBackupWork(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		s := svcOf(t, ta)
		s.SetHoldPoll(50 * time.Millisecond)
		release, err := backup.NewTestService(ta.Deps).Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Hold(ctx, backup.HoldRequest{What: backup.HoldUpdate}); !errors.Is(err, backup.ErrBusy) {
			t.Fatalf("hold without waiting: %v", err)
		}
		if instMaintenance(t, ta).Enabled {
			t.Fatal("a refused hold turned maintenance on")
		}
		var mu sync.Mutex
		var lines []string
		go func() {
			time.Sleep(300 * time.Millisecond)
			release()
		}()
		l, err := s.Hold(ctx, backup.HoldRequest{What: backup.HoldUpdate, Wait: 10 * time.Second, Log: func(line string) {
			mu.Lock()
			lines = append(lines, line)
			mu.Unlock()
		}})
		if err != nil {
			t.Fatalf("hold after the backup: %v", err)
		}
		defer l.Release(ctx, nil)
		mu.Lock()
		defer mu.Unlock()
		if len(lines) == 0 || !strings.Contains(lines[0], "Waiting for a running test backup to finish") {
			t.Fatalf("wait log = %v", lines)
		}
	})
}

// After a crash the restarted server takes over the update's hold (even while
// its lease still looks live) and ends it, putting maintenance back.
func TestHoldIsResumedAndEndedByTheNextProcess(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		first := svcOf(t, ta)
		l, err := first.Hold(ctx, backup.HoldRequest{What: backup.HoldUpdate, Message: "Updating"})
		if err != nil {
			t.Fatal(err)
		}
		l.Detach() // the process stops
		next := backup.NewTestService(ta.Deps)
		if _, err := next.Hold(ctx, backup.HoldRequest{What: backup.HoldUpdate}); !errors.Is(err, backup.ErrBusy) {
			t.Fatalf("a live hold was taken without resuming it: %v", err)
		}
		r, err := next.Hold(ctx, backup.HoldRequest{What: backup.HoldUpdate, Resume: l.Info().ID})
		if err != nil {
			t.Fatalf("resume: %v", err)
		}
		if r.Info().ID != l.Info().ID || r.Info().Before.Enabled {
			t.Fatalf("resumed hold = %+v", r.Info())
		}
		r.Detach()
		// The restarted server finishes the update and ends the hold by id.
		if err := backup.NewTestService(ta.Deps).EndHold(ctx, l.Info().ID, &settings.Maintenance{}); err != nil {
			t.Fatal(err)
		}
		if instMaintenance(t, ta).Enabled {
			t.Fatal("maintenance stayed on after EndHold")
		}
		if err := core.MustUse[adminapi.MaintenanceGuard](ta.Deps).SetMaintenance(ctx, "", settings.Maintenance{Enabled: true}); err != nil {
			t.Fatalf("toggle after EndHold: %v", err)
		}
	})
}

// The 03:00 automatic patch waits for backup work: a lease held, or a
// scheduled backup due this hour that has not started.
func TestPendingNamesBackupWorkForAutomaticUpdates(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		s := svcOf(t, ta)
		if what, busy := s.Pending(ctx, time.Now()); busy {
			t.Fatalf("pending on an idle server: %s", what)
		}
		release, err := s.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if what, busy := s.Pending(ctx, time.Now()); !busy || what != "test" {
			t.Fatalf("pending with a lease held = %s %v", what, busy)
		}
		release()
		if _, err := s.SaveSchedule(ctx, backup.Schedule{Frequency: backup.FrequencyDaily, Hour: 3, TZ: "UTC", Keep: 3}, ta.Admin.ID); err != nil {
			t.Fatal(err)
		}
		slot := time.Now().UTC().Truncate(24 * time.Hour).Add(27 * time.Hour) // tomorrow 03:00 UTC
		if what, busy := s.Pending(ctx, slot.Add(2*time.Minute)); !busy || what != backup.KindScheduled {
			t.Fatalf("pending with the 03:00 backup due = %s %v", what, busy)
		}
		if won, err := s.ClaimSlot(ctx, slot); err != nil || !won {
			t.Fatalf("claim: %v %v", won, err)
		}
		if what, busy := s.Pending(ctx, slot.Add(2*time.Minute)); busy {
			t.Fatalf("pending after the slot started: %s", what)
		}
		if _, busy := s.Pending(ctx, slot.Add(24*time.Hour+2*time.Minute)); !busy {
			t.Fatal("the next day's slot is not pending")
		}
		if _, busy := s.Pending(ctx, slot.Add(90*time.Minute)); busy {
			t.Fatal("pending long after the slot")
		}
	})
}

// Two submits of the restore form: only one restore runs, and the other
// cannot turn maintenance mode off while it runs.
func TestRestoreSubmittedTwiceRunsOnce(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		seedServer(t, ta)
		s := svcOf(t, ta)
		b, err := s.Create(ctx, backup.Request{Kind: backup.KindManual})
		if err != nil {
			t.Fatal(err)
		}
		admin := ta.Login(t, "admin")
		submit := func() *testutil.Response {
			return form(admin, "/admin/backup/restore", url.Values{"archive": {b.ID}, "confirm": {"restore"}})
		}
		// While a restore holds the server, a second submit is refused and
		// maintenance stays on.
		l, err := s.Hold(ctx, backup.HoldRequest{What: backup.HoldRestore})
		if err != nil {
			t.Fatal(err)
		}
		if r := submit(); r.StatusCode != http.StatusSeeOther || !strings.Contains(string(admin.Get("/admin/backup").Body), "A backup or restore is already running") {
			t.Fatalf("second submit = %d", r.StatusCode)
		}
		if !instMaintenance(t, ta).Enabled {
			t.Fatal("the refused submit turned maintenance off under the running restore")
		}
		if err := l.Release(ctx, &settings.Maintenance{}); err != nil {
			t.Fatal(err)
		}
		// Submitted at once: exactly one restore runs.
		var restoresBefore int
		_ = ta.Deps.DB.Get(ctx, &restoresBefore, `SELECT COUNT(*) FROM audit_log WHERE action = 'backup.restore'`)
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				submit()
			}()
		}
		wg.Wait()
		// While a restore holds the server (the lease row says it owns
		// maintenance), maintenance is on: read both in one snapshot.
		heldButOff := func() bool {
			var lease, maint string
			_ = ta.Deps.DB.WithReadTx(ctx, func(tx *db.Tx) error {
				_ = tx.Get(ctx, &lease, `SELECT value FROM instance_settings WHERE key = 'backup.lock'`)
				_ = tx.Get(ctx, &maint, `SELECT value FROM instance_settings WHERE key = 'maintenance'`)
				return nil
			})
			return strings.Contains(lease, `"maintenance":true`) && !strings.Contains(maint, `"enabled":true`)
		}
		deadline := time.Now().Add(30 * time.Second)
		for s.State().Running && time.Now().Before(deadline) {
			if heldButOff() {
				t.Fatal("maintenance went off while the restore ran")
			}
			time.Sleep(5 * time.Millisecond)
		}
		if st := s.State(); st.Running || st.Err != nil {
			t.Fatalf("restore state %+v", st)
		}
		var restores int
		_ = ta.Deps.DB.Get(ctx, &restores, `SELECT COUNT(*) FROM audit_log WHERE action = 'backup.restore'`)
		if restores-restoresBefore < 1 || restores-restoresBefore > 4 {
			t.Fatalf("%d restores ran", restores-restoresBefore)
		}
		if instMaintenance(t, ta).Enabled {
			t.Fatal("maintenance left on after the restores")
		}
	})
}

// Temporary archives and uploads older than a day are removed from the
// backup directories; finished archives and fresh temporary files are kept.
func TestBackupTempFilesAreSwept(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-25 * time.Hour)
	mk := func(name string, at time.Time) string {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte("x"), 0o600)
		_ = os.Chtimes(p, at, at)
		return p
	}
	gone := []string{mk(".01jabc.mvbackup.tmp", old), mk(".upload-123.mvbackup", old)}
	kept := []string{mk(".01jdef.mvbackup.tmp", time.Now()), mk("mangaveyra-manual-20261101-000000-01jxyz.mvbackup", old), mk("notes.txt", old)}
	if n := backup.SweepTempIn([]string{dir}, time.Now().Add(-24*time.Hour)); n != 2 {
		t.Fatalf("removed %d", n)
	}
	for _, p := range gone {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s was kept", p)
		}
	}
	for _, p := range kept {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed", p)
		}
	}
}

// While an update or a restore holds the server, the job queue hands out no
// new work (backup registers its claim gate with the queue); the job waits and
// is claimed once the hold ends.
func TestHoldStopsJobClaims(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx, q := context.Background(), ta.Deps.Jobs
		job, err := q.Enqueue(ctx, jobs.EnqueueParams{Type: "hold.claim", UserID: ta.Admin.ID, Scope: jobs.Scope{Kind: jobs.ScopeNone}})
		if err != nil {
			t.Fatal(err)
		}
		l, err := core.MustUse[backup.Holder](ta.Deps).Hold(ctx, backup.HoldRequest{What: backup.HoldRestore, Message: "Restoring", ActorID: ta.Admin.ID})
		if err != nil {
			t.Fatal(err)
		}
		if j, err := q.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorGo, Types: []string{"hold.claim"}}); err != nil || j != nil {
			t.Fatalf("claim under a hold = %v, %v", j, err)
		}
		l.Release(ctx, nil)
		if j, err := q.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorGo, Types: []string{"hold.claim"}}); err != nil || j == nil || j.ID != job.ID {
			t.Fatalf("claim after the hold = %v, %v", j, err)
		}
	})
}
