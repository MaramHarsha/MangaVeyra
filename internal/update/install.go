package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/backup"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/core/netguard"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

// Install errors returned before anything changes.
var (
	ErrBusy       = errors.New("update: another update is running")
	ErrNoBackups  = errors.New("update: backups are not available on this server")
	ErrNoDownload = errors.New("update: this release has no download for this server's platform")
	ErrNotOffered = errors.New("update: that update is no longer offered")
	ErrStale      = errors.New("update: the release changed since the last check")
)

// KeepFor is how long "Keep the backup for 7 days" keeps the pre-update backup.
const KeepFor = 7 * 24 * time.Hour

// InstallOptions are the admin's choices on G14.
type InstallOptions struct {
	// Keep keeps the pre-update backup for 7 days after success.
	Keep bool
	// Auto marks the 03:00 automatic patch install.
	Auto bool
}

type installPlan struct {
	exe   string
	asset Asset
}

func (m *Module) backups() (backup.Service, bool) {
	if m.hooks.Backups != nil {
		return m.hooks.Backups, true
	}
	if m.hooks.NoBackups {
		return nil, false
	}
	return core.Use[backup.Service](m.d)
}

// ContainerMode reports a container install (/.dockerenv, /run/.containerenv
// or MV_DEPLOYMENT=container): it cannot replace its own image.
func (m *Module) ContainerMode() bool {
	if m.hooks.Container != nil {
		return *m.hooks.Container
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv(EnvDeployment)), "container") {
		return true
	}
	for _, p := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

func (m *Module) activeAttempt(ctx context.Context) (*Record, error) {
	rec, err := m.latestAttempt(ctx)
	if err != nil || rec == nil || !isActive(rec.Status) {
		return nil, err
	}
	return rec, nil
}

// Install starts installing the offer offerID for actorID. Everything that
// can be checked without changing the server is checked first; the install
// then runs in the background and its progress is in the updates table.
func (m *Module) Install(ctx context.Context, offerID, actorID string, opt InstallOptions) (*Record, error) {
	offer, err := m.offer(ctx)
	if err != nil {
		return nil, err
	}
	if offer == nil || offer.ID != offerID {
		return nil, ErrNotOffered
	}
	if active, err := m.activeAttempt(ctx); err != nil || active != nil {
		return nil, errOr(err, ErrBusy)
	}
	svc, ok := m.backups()
	if !ok {
		return nil, ErrNoBackups
	}
	if _, ok := svc.(backup.Holder); !ok {
		return nil, ErrNoBackups // the install holds the server through the backup module
	}
	mode := ModeBinary
	if m.ContainerMode() {
		mode = ModeContainer
	}
	var plan installPlan
	if mode == ModeBinary {
		if plan.exe, err = m.executable(); err != nil {
			return nil, err
		}
		if err := checkReplaceable(plan.exe); err != nil {
			return nil, err
		}
		man, _, err := m.fetchManifest(ctx, m.config(ctx))
		if err != nil {
			return nil, err
		}
		if !sameVersion(man.Version, offer.ToVersion) {
			return nil, ErrStale
		}
		goos, goarch := m.platform()
		a, ok := man.AssetFor(goos, goarch)
		if !ok {
			return nil, ErrNoDownload
		}
		if m.hooks.Client == nil {
			// Refuse a download address the SSRF guard would block before taking a backup.
			policy := netguard.Policy{AllowPrivate: m.d.Cfg.AllowPrivateProviderURLs, AllowHTTP: true}
			if _, err := policy.CheckURL(ctx, a.URL, nil); err != nil {
				return nil, fmt.Errorf("the download address is not allowed: %w", err)
			}
		}
		plan.asset = a
	}
	lock := lockValue{UpdateID: offer.ID, Owner: m.owner}
	got, err := m.acquireLock(ctx, lock)
	if err != nil {
		return nil, err
	}
	if !got {
		return nil, ErrBusy
	}
	inst, err := m.d.Settings.Instance(ctx)
	if err != nil {
		m.releaseLock(ctx, lock)
		return nil, err
	}
	prev := inst.Maintenance
	run := newRun(mode)
	run.Keep, run.Auto, run.Maintenance = opt.Keep, opt.Auto, &prev
	who := "an admin"
	if opt.Auto {
		who = "the 03:00 automatic patch install"
	}
	run.logf(fmt.Sprintf("Update from %s to %s started by %s (%s install).", offer.FromVersion, offer.ToVersion, who, mode))
	rec, err := m.claim(ctx, offer.ID, actorID, run)
	if err != nil {
		m.releaseLock(ctx, lock)
		return nil, err
	}
	if m.d.Auth != nil {
		m.d.Auth.Audit(ctx, authEntry("update.start", rec.ID, map[string]any{"from": rec.FromVersion, "to": rec.ToVersion,
			"mode": mode, "keep_backup": opt.Keep, "automatic": opt.Auto}))
	}
	bg := context.WithoutCancel(ctx)
	m.wg.Add(1)
	m.here.Store(rec.ID, true)
	go func() {
		defer m.wg.Done()
		defer m.here.Delete(rec.ID)
		m.runInstall(bg, rec, run, plan, lock, svc)
	}()
	return rec, nil
}

// backupWait is how long an install waits for a running backup or restore
// before it starts (the scheduled 03:00 backup, "Back up now").
const backupWait = 30 * time.Minute

func (m *Module) backupWait() time.Duration {
	if m.hooks.BackupWait > 0 {
		return m.hooks.BackupWait
	}
	return backupWait
}

// runInstall is the binary install flow (ADR 0013 §6): hold the server →
// pre-update backup → download and verify → the new binary's migrations →
// the new binary's health check on a loopback port → atomic swap → restart.
// Any failure before the swap restores the backup and keeps the old binary.
//
// The hold (backup.Holder) is taken first and kept until the end: maintenance
// mode is on and owned by the install (the G5 toggle refuses to turn it off),
// no other backup or restore can take the backup lease, and the job queue
// claims no new work. So nothing can block the rollback restore or write
// data the rollback would lose.
func (m *Module) runInstall(ctx context.Context, rec *Record, run Run, plan installPlan, lock lockValue, svc backup.Service) {
	hctx, stopHold := context.WithCancel(ctx)
	go m.holdLock(hctx, lock)
	handedOff := false
	defer func() {
		stopHold()
		if !handedOff {
			m.releaseLock(ctx, lock)
		}
	}()
	actor := ""
	if rec.StartedBy != nil {
		actor = *rec.StartedBy
	}
	save := func() {
		if err := m.saveRun(ctx, rec, run); err != nil {
			m.d.Log.Warn("update: save progress", "err", err)
		}
	}
	var lease backup.Lease
	fail := func(step, code string, err error) {
		m.fail(ctx, rec, &run, step, code, err, svc, lease, actor, plan.exe)
		// A restore keeps the update.* settings, so the lease is still ours;
		// renew it until the deferred release all the same.
		_, _ = m.renewLock(ctx, lock)
	}

	// 1. Hold the server, then the automatic backup.
	rec.Status = StatusBackingUp
	run.setStep(StepBackup, StateRunning, "")
	save()
	lease, err := svc.(backup.Holder).Hold(ctx, backup.HoldRequest{What: backup.HoldUpdate, Message: m.maintenanceMessage(ctx, rec.ToVersion),
		ActorID: actor, Wait: m.backupWait(), Log: func(line string) { run.logf(line); save() }})
	if err != nil {
		if errors.Is(err, backup.ErrBusy) {
			err = fmt.Errorf("another backup or restore was still running after %s: %w", m.backupWait(), err)
		}
		fail(StepBackup, "UPD-BACKUP", err)
		return
	}
	before := lease.Info().Before
	run.Maintenance, run.HoldID = &before, lease.Info().ID
	run.logf("Maintenance mode on: members see the maintenance page; no new jobs start until the update ends.")
	save()
	held := lease.Context(ctx)
	var keep time.Duration
	if run.Keep {
		keep = KeepFor
	}
	b, err := svc.Create(held, backup.Request{Kind: backup.KindPreUpdate, CreatedBy: actor, KeepFor: keep})
	if err != nil {
		fail(StepBackup, "UPD-BACKUP", err)
		return
	}
	rec.BackupID = &b.ID
	run.BackupBytes, run.BackupAt = b.Bytes, b.CreatedAt
	run.setStep(StepBackup, StateDone, "")
	run.logf(fmt.Sprintf("Backup %s saved (%d bytes).", b.ID, b.Bytes))
	save()

	if run.Mode == ModeContainer {
		// A container cannot replace its own image: keep the backup and show
		// the host commands. The update finishes when the new version starts.
		if err := lease.Release(ctx, run.Maintenance); err != nil {
			m.d.Log.Error("update: put maintenance back", "err", err)
		}
		run.logf("Container install: run the update commands on the host; the backup is kept until the new version starts.")
		rec.Status = StatusAvailable
		save()
		_, _ = m.d.DB.Exec(ctx, `DELETE FROM updates WHERE status = ? AND id <> ?`, StatusAvailable, rec.ID)
		return
	}

	// 2. Download and verify.
	dir := m.updatesDir(rec.ToVersion)
	rec.Status = StatusDownloading
	run.DownloadSize = plan.asset.Size
	run.setStep(StepDownload, StateRunning, "")
	save()
	bin, err := m.download(ctx, plan.asset, dir, func(done, total int64) {
		run.Downloaded = done
		save()
	})
	if err != nil {
		code := "UPD-DOWNLOAD"
		if errors.Is(err, ErrChecksum) || errors.Is(err, ErrSize) {
			code = "UPD-VERIFY"
		}
		fail(StepDownload, code, err)
		return
	}
	rec.Status = StatusVerifying
	save()
	out, err := m.runChild(ctx, time.Minute, bin, "version")
	if f := strings.Fields(out); err != nil || len(f) < 2 || !sameVersion(f[1], rec.ToVersion) {
		fail(StepDownload, "UPD-VERIFY", fmt.Errorf("the downloaded program reports %q, not version %s: %v", truncate(out, 200), rec.ToVersion, err))
		return
	}
	run.setStep(StepDownload, StateDone, "")
	run.logf(fmt.Sprintf("Downloaded %d bytes; signature valid; SHA-256 matches; the program reports %s.", plan.asset.Size, rec.ToVersion))
	save()

	// 3. Update and migrate.
	rec.Status = StatusMigrating
	run.setStep(StepMigrate, StateRunning, "")
	save()
	if out, err := m.runChild(ctx, time.Minute, bin, "migrate", "status"); err == nil {
		run.Pending = countPending(out)
		save()
	}
	out, err = m.runChild(ctx, migrateTimeout, bin, "migrate")
	if out != "" {
		run.logf("migrate: " + lastLines(out, 40))
	}
	if err != nil {
		code := "UPD-MIGRATE"
		if n := migrationNumber(out); n != "" {
			code += "-" + n
		}
		fail(StepMigrate, code, errors.New(firstLine(out, err)))
		return
	}
	run.Applied = countApplied(out)
	run.setStep(StepMigrate, StateDone, "")
	save()

	// 4. Health check.
	rec.Status = StatusHealthCheck
	run.setStep(StepHealth, StateRunning, "")
	save()
	res, err := m.checkCandidate(ctx, bin, rec.ToVersion, run.logf)
	if err != nil {
		fail(StepHealth, "UPD-HEALTH", err)
		return
	}
	run.Workers = res.WorkersOnline
	run.logf(fmt.Sprintf("Version %s passed /healthz and /readyz on a loopback port.", rec.ToVersion))
	if err := swapExecutable(plan.exe, bin); err != nil {
		fail(StepHealth, "UPD-SWAP", err)
		return
	}
	run.setStep(StepHealth, StateDone, "")
	run.Phase = PhaseRestarting
	run.logf(fmt.Sprintf("Replaced %s (the previous binary is kept as %s.prev); restarting.", plan.exe, plan.exe))
	save()
	// The restarted server ends the hold (finishRestarted); until then
	// maintenance stays on and owned by this update.
	lease.Detach()
	_ = os.RemoveAll(dir)
	if ok, err := m.swapLock(ctx, lock.encode(), lockValue{UpdateID: rec.ID, Owner: restartOwner}); err == nil && ok {
		handedOff = true
	}
	m.requestRestart(plan.exe)
}

func firstLine(out string, err error) string {
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "error:"))
		if l != "" {
			return truncate(l, 300)
		}
	}
	if err != nil {
		return err.Error()
	}
	return "unknown error"
}

// fail records the failed step, restores the pre-update backup when one was
// taken, keeps the old binary and ends the hold, putting maintenance back (G14
// failed, restored). The restore runs under the hold, so no other backup work
// and no maintenance change can stand in its way. Without a hold (it could
// not be taken) nothing changed and maintenance is left alone.
func (m *Module) fail(ctx context.Context, rec *Record, run *Run, step, code string, cause error, svc backup.Service, lease backup.Lease,
	actor, exe string) {
	m.d.Log.Error("update failed", "update", rec.ID, "step", step, "code", code, "err", redact(cause.Error()))
	run.setStep(step, StateFailed, truncate(redact(cause.Error()), 500))
	run.Code = code
	run.logf(fmt.Sprintf("%s failed (%s): %v", step, code, cause))
	rec.Error = truncate(redact(cause.Error()), 500)
	if id := rec.backupID(); id != "" && svc != nil {
		run.setStep(StepRestore, StateRunning, "")
		_ = m.saveRun(ctx, rec, *run)
		run.logf("Restoring backup " + id + ".")
		rctx := ctx
		if lease != nil {
			rctx = lease.Context(ctx)
		}
		err := svc.Restore(rctx, id, actor)
		m.d.Settings.Invalidate()
		if err != nil {
			run.RestoreFailed = true
			run.setStep(StepRestore, StateFailed, truncate(redact(err.Error()), 500))
			run.logf("Restore failed: " + err.Error())
			rec.Status = StatusFailed
		} else {
			run.Restored = true
			run.setStep(StepRestore, StateDone, "")
			run.logf("Restored backup " + id + ".")
			rec.Status = StatusRolledBack
		}
	} else {
		rec.Status = StatusFailed
	}
	now := db.Now()
	rec.FinishedAt = &now
	switch {
	case lease == nil:
	case run.RestoreFailed:
		// The data may be half-migrated: members stay on the maintenance page
		// until an admin restores the backup from G10 or the command line.
		run.logf("Maintenance mode stays on until the backup is restored.")
		if err := lease.Release(ctx, nil); err != nil {
			m.d.Log.Error("update: end the hold", "err", err)
		}
	default:
		if err := lease.Release(ctx, run.Maintenance); err != nil {
			m.d.Log.Error("update: put maintenance back", "err", err)
		}
	}
	_ = os.RemoveAll(m.updatesDir(rec.ToVersion))
	if exe != "" {
		_ = os.Remove(exe + ".new")
	}
	if err := m.saveRun(ctx, rec, *run); err != nil {
		m.d.Log.Error("update: save failed state", "err", err)
	}
	if m.d.Auth != nil {
		m.d.Auth.Audit(ctx, authEntry("update.fail", rec.ID, map[string]any{"code": code, "restored": run.Restored}))
	}
	m.reoffer(ctx, rec)
}

// reoffer makes the release available again after a failed attempt ("Try again").
func (m *Module) reoffer(ctx context.Context, rec *Record) {
	if cur, err := m.offer(ctx); err != nil || cur != nil {
		return
	}
	o := &Record{FromVersion: rec.FromVersion, ToVersion: rec.ToVersion, Channel: rec.Channel, Changelog: rec.Changelog,
		DownloadURL: rec.DownloadURL, Signature: rec.Signature}
	if err := m.saveOffer(ctx, o); err != nil {
		m.d.Log.Warn("update: offer again", "err", err)
	}
}

// maintenanceMessage is the maintenance page's update message in the
// instance's default language.
func (m *Module) maintenanceMessage(ctx context.Context, version string) string {
	inst := m.d.Settings.MustInstance(ctx)
	lctx := i18n.WithLocale(ctx, i18n.Negotiate(m.d.I18n.Languages(), inst.DefaultLocale, "", "", ""))
	return i18n.T(lctx, "update.maintenance.message", "version", version)
}

// endHold ends the install's hold from the restarted server and puts the
// admin's maintenance setting back.
func (m *Module) endHold(ctx context.Context, run Run) {
	if svc, ok := m.backups(); ok && run.HoldID != "" {
		if h, ok := svc.(backup.Holder); ok {
			if err := h.EndHold(ctx, run.HoldID, maintenanceOrOff(run.Maintenance)); err != nil {
				m.d.Log.Error("update: end the hold", "err", err)
			}
			return
		}
	}
	m.putBackMaintenance(ctx, run.Maintenance)
}

func maintenanceOrOff(prev *settings.Maintenance) *settings.Maintenance {
	if prev == nil {
		return &settings.Maintenance{}
	}
	return prev
}

// putBackMaintenance restores the admin's maintenance setting from before the update.
func (m *Module) putBackMaintenance(ctx context.Context, prev *settings.Maintenance) {
	p := settings.Maintenance{}
	if prev != nil {
		p = *prev
	}
	m.d.Settings.Invalidate()
	if _, err := m.d.Settings.UpdateInstance(ctx, "", func(i *settings.Instance) error {
		i.Maintenance = p
		return nil
	}); err != nil {
		m.d.Log.Error("update: put maintenance back", "err", err)
	}
}
