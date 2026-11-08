package update_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/backup"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/update"
)

// useRealBackups wires the update module to the real backup module.
func useRealBackups(e *env) backup.Service {
	svc := core.MustUse[backup.Service](e.ta.Deps)
	e.hooks.Backups = svc
	update.SetTestHooks(e.m, e.hooks)
	return svc
}

// waitStatus polls the attempt until it reaches status.
func waitStatus(t *testing.T, e *env, status string) *update.Record {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		if rec, err := update.LatestAttempt(e.m, e.ctx); err == nil && rec != nil {
			if rec.Status == status {
				return rec
			}
			if rec.Finished() {
				t.Fatalf("the install ended as %s before %s:\n%s", rec.Status, status, update.LogText(rec))
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the install never reached %s", status)
	return nil
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.ta.Deps.DB.Exec(e.ctx, q, args...); err != nil {
		e.t.Fatal(err)
	}
}

type memberExporter interface {
	MemberExport(ctx context.Context, userID string) (backup.Backup, error)
}

type claimGate interface {
	ClaimsAllowed(ctx context.Context, tx *db.Tx) (bool, error)
}

// The overlay the review used: while the new version fails its health check,
// other backup work tries to take the lease (another process backs up, a
// scheduled backup's lease row appears), a member exports their data and an
// admin tries to turn maintenance off. None of it may stop the rollback: the
// install holds the server, so the restore still runs and members never get
// in between the backup and the restore.
func TestRollbackRestoresWhateverOtherBackupWorkDoes(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect string) {
		t.Setenv("MVFAKE_MODE", "health_fail")
		e := newEnv(t, dialect, withHooks(func(h *update.TestHooks) { h.HealthTimeout = 4 * time.Second }))
		svc := useRealBackups(e)
		member := e.ta.CreateUser(t, "", auth.RoleCreator)
		e.enableChecks("")
		e.publish(publishOpts{})
		rec, err := e.m.Check(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.m.Install(e.ctx, rec.ID, e.ta.Admin.ID, update.InstallOptions{}); err != nil {
			t.Fatal(err)
		}
		waitStatus(t, e, update.StatusHealthCheck)

		// The job queue hands out nothing while the update holds the server.
		var allowed bool
		if err := e.ta.Deps.DB.WithTx(e.ctx, func(tx *db.Tx) error {
			var err error
			allowed, err = svc.(claimGate).ClaimsAllowed(e.ctx, tx)
			return err
		}); err != nil || allowed {
			t.Fatalf("claims allowed during the update: %v %v", allowed, err)
		}
		// The admin cannot turn maintenance off half-way.
		admin := e.adminClient()
		if r := post(admin, "/admin/maintenance", form("enabled", "0", "message", "")); r.StatusCode != http.StatusConflict ||
			!strings.Contains(string(r.Body), "An update is running") {
			t.Fatalf("turning maintenance off during the update = %d", r.StatusCode)
		}
		if !e.maintenance() {
			t.Fatal("maintenance went off during the update")
		}
		// Another process's "Back up now" waits its turn.
		if _, err := backup.NewService(e.ta.Deps).Create(e.ctx, backup.Request{Kind: backup.KindManual}); !errors.Is(err, backup.ErrBusy) {
			t.Fatalf("a backup ran during the update: %v", err)
		}
		// A member export holds no lease and does not get in the way.
		if _, err := svc.(memberExporter).MemberExport(e.ctx, member.ID); err != nil {
			t.Fatalf("member export during the update: %v", err)
		}
		// A scheduled backup in another process tries to insert its lease row,
		// then one is forced in over the update's.
		e.exec(`INSERT INTO instance_settings (key, value, updated_at) VALUES ('backup.lock', '{"holder":"p2","what":"scheduled"}', ?)
			ON CONFLICT (key) DO NOTHING`, db.Now())
		e.exec(`UPDATE instance_settings SET value = '{"holder":"p3","what":"scheduled"}', updated_at = ? WHERE key = 'backup.lock'`, db.Now())

		att := waitInstall(t, e)
		run := att.Run()
		if att.Status != update.StatusRolledBack || !run.Restored || run.Code != "UPD-HEALTH" {
			t.Fatalf("attempt = %s %s restored=%v\n%s", att.Status, run.Code, run.Restored, update.LogText(att))
		}
		if b, err := svc.Get(e.ctx, *att.BackupID); err != nil || b.Status != backup.StatusRestored {
			t.Fatalf("pre-update backup = %+v, %v", b, err)
		}
		if readFile(t, e.exe) != "old binary 1.0.0" || e.maintenance() {
			t.Fatal("the old binary must be kept and maintenance turned off")
		}
	})
}

// The pre-update backup waits (bounded, with a line in the log) for a backup
// another process is writing; if it does not finish in time nothing changes.
func TestInstallWaitsForRunningBackupWork(t *testing.T) {
	t.Run("finishes", func(t *testing.T) { testInstallWaits(t) })
	t.Run("times out", func(t *testing.T) { testInstallGivesUpWaiting(t) })
}

func testInstallWaits(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect string) {
		e := newEnv(t, dialect, withHooks(func(h *update.TestHooks) { h.BackupWait = 20 * time.Second }))
		useRealBackups(e)
		e.enableChecks("")
		e.publish(publishOpts{})
		rec, err := e.m.Check(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		e.exec(`INSERT INTO instance_settings (key, value, updated_at) VALUES ('backup.lock', '{"holder":"p2","what":"scheduled"}', ?)`, db.Now())
		if _, err := e.m.Install(e.ctx, rec.ID, e.ta.Admin.ID, update.InstallOptions{}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2500 * time.Millisecond)
		if e.maintenance() {
			t.Fatal("maintenance went on before the running backup finished")
		}
		e.exec(`DELETE FROM instance_settings WHERE key = 'backup.lock'`)
		att := waitInstall(t, e)
		select {
		case <-e.restarted:
		default:
			t.Fatalf("install after the backup = %s\n%s", att.Status, update.LogText(att))
		}
		if !strings.Contains(update.LogText(att), "Waiting for a running scheduled backup to finish.") {
			t.Fatalf("the wait is not in the log:\n%s", update.LogText(att))
		}
	})
}

func testInstallGivesUpWaiting(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect string) {
		e := newEnv(t, dialect, withHooks(func(h *update.TestHooks) { h.BackupWait = time.Second }))
		useRealBackups(e)
		e.enableChecks("")
		e.publish(publishOpts{})
		rec, _ := e.m.Check(e.ctx)
		e.exec(`INSERT INTO instance_settings (key, value, updated_at) VALUES ('backup.lock', '{"holder":"p2","what":"manual"}', ?)`, db.Now())
		if _, err := e.m.Install(e.ctx, rec.ID, e.ta.Admin.ID, update.InstallOptions{}); err != nil {
			t.Fatal(err)
		}
		att := waitInstall(t, e)
		if att.Status != update.StatusFailed || att.Run().Code != "UPD-BACKUP" || att.BackupID != nil {
			t.Fatalf("attempt = %s %s\n%s", att.Status, att.Run().Code, update.LogText(att))
		}
		if e.maintenance() || readFile(t, e.exe) != "old binary 1.0.0" {
			t.Fatal("a refused hold must leave the server as it was")
		}
	})
}

type scheduler interface {
	SaveSchedule(ctx context.Context, sc backup.Schedule, by string) (backup.Schedule, error)
}

// 03:00 is the default backup hour: the automatic patch install lets the
// scheduled backup start first instead of colliding with it.
func TestAutoPatchLetsTheScheduledBackupGoFirst(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect string) {
		var clock time.Time
		e := newEnv(t, dialect, withHooks(func(h *update.TestHooks) { h.Now = func() time.Time { return clock } }))
		svc := useRealBackups(e)
		if _, err := svc.(scheduler).SaveSchedule(e.ctx, backup.Schedule{Frequency: backup.FrequencyDaily, Hour: 3, TZ: "UTC", Keep: 3}, e.ta.Admin.ID); err != nil {
			t.Fatal(err)
		}
		if err := update.SaveConfig(e.m, e.ctx, update.Config{Checks: true, ReleaseURL: e.release.URL, Channel: update.ChannelStable, AutoPatch: true}); err != nil {
			t.Fatal(err)
		}
		e.publish(publishOpts{version: "1.0.2", patch: true})
		slot := time.Now().UTC().Truncate(24 * time.Hour).Add(27 * time.Hour) // tomorrow 03:00 UTC
		clock = slot.Add(time.Minute)
		if _, err := e.m.Check(e.ctx); err != nil {
			t.Fatal(err)
		}
		update.Tick(e.m, e.ctx)
		e.m.Wait()
		if rec, _ := update.LatestAttempt(e.m, e.ctx); rec != nil {
			t.Fatalf("the patch install started before the 03:00 backup: %s", rec.Status)
		}
		// The scheduled backup started (its slot is claimed): the install follows.
		e.exec(`INSERT INTO instance_settings (key, value, updated_at) VALUES ('backup.slot', ?, ?)`, `"`+slot.Format(time.RFC3339)+`"`, db.Now())
		clock = slot.Add(2 * time.Minute)
		update.Tick(e.m, e.ctx)
		att := waitInstall(t, e)
		select {
		case <-e.restarted:
		default:
			t.Fatalf("automatic patch after the backup = %s\n%s", att.Status, update.LogText(att))
		}
		if !att.Run().Auto {
			t.Fatal("not marked automatic")
		}
	})
}
