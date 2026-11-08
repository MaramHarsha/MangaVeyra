package update

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/admin/adminapi"
	"github.com/MaramHarsha/MangaVeyra/internal/backup"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

// Module is the update feature module (G14, ADR 0013 §6).
type Module struct {
	d     *core.Deps
	owner string // this process's lock owner id
	hooks hooks

	wg       sync.WaitGroup
	childMu  sync.Mutex
	children map[*exec.Cmd]struct{}
	here     sync.Map // update ids whose install runs in this process
}

func (m *Module) runningHere(id string) bool {
	_, ok := m.here.Load(id)
	return ok
}

// hooks replace the environment in tests (export_test.go).
type hooks struct {
	Client        *http.Client
	Keys          []ed25519.PublicKey
	Now           func() time.Time
	Version       string
	GOOS, GOARCH  string
	Backups       backup.Service
	NoBackups     bool
	Executable    string
	Container     *bool
	HealthTimeout time.Duration
	Restart       func(exe string)
	// Download replaces the download timeouts (zero fields keep the defaults).
	Download downloadLimits
	// BackupWait replaces how long an install waits for running backup work.
	BackupWait time.Duration
}

// New creates the module and provides update.Status and the G5 header's
// adminapi.UpdateNotice to other modules.
func New(d *core.Deps) *Module {
	m := &Module{d: d, owner: db.NewID(), children: map[*exec.Cmd]struct{}{}}
	core.Provide[Status](d, m)
	core.Provide[adminapi.UpdateNotice](d, m)
	core.Provide[web.MaintenanceProgress](d, m)
	// G14's member banner and admin login dialog ride on every signed-in page.
	if ui, ok := core.Use[*web.UI](d); ok {
		ui.AddShellExtras(m.Shell)
	}
	return m
}

// Name implements core.Module.
func (m *Module) Name() string { return "update" }

// candidate reports a new binary being health-checked by an update: it stays
// idle so it never acts on the update that started it.
func candidate() bool { return os.Getenv(EnvCandidate) == "1" }

// Init finishes an update the previous process handed over (the new binary
// now runs) before any request is served.
func (m *Module) Init(ctx context.Context) error {
	if candidate() {
		return nil
	}
	if err := m.resume(ctx); err != nil {
		m.d.Log.Error("update: finish the previous update", "err", err)
	}
	return nil
}

// Start runs the daily check (when the admin turned checks on) and the
// opt-in 03:00 patch install. It returns when ctx ends.
func (m *Module) Start(ctx context.Context) error {
	if candidate() {
		return nil
	}
	defer m.killChildren()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	m.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			m.tick(ctx)
		}
	}
}

// tick runs the scheduled work once.
func (m *Module) tick(ctx context.Context) {
	if active, err := m.activeAttempt(ctx); err != nil || active != nil {
		// An install another process started and then lost (its lease ran
		// out) is recovered here as well as at start; takeLock refuses while
		// the lease is live.
		if active != nil && active.Run().Phase != PhaseRestarting && !m.runningHere(active.ID) {
			if err := m.recoverInterrupted(ctx, active, active.Run()); err != nil {
				m.d.Log.Warn("update: recover", "err", err)
			}
		}
		return
	}
	m.sweepDownloads(time.Now().Add(-24 * time.Hour))
	cfg := m.config(ctx)
	if !cfg.Checks || cfg.ReleaseURL == "" {
		return // off by default: no network call at all
	}
	state := m.checkState(ctx)
	now := m.now()
	if state.CheckedAt == nil || now.Sub(*state.CheckedAt) >= 24*time.Hour {
		if _, err := m.Check(ctx); err != nil {
			m.d.Log.Info("update: check", "err", err)
		}
		state = m.checkState(ctx)
	}
	m.autoPatch(ctx, cfg, state, now)
}

// autoPatch installs an eligible patch release once, at 03:00 server time,
// when the admin opted in. Only patch releases whose every changelog entry is
// flagged as a fixes-only patch qualify; containers never update themselves.
func (m *Module) autoPatch(ctx context.Context, cfg Config, state CheckState, now time.Time) {
	if !cfg.AutoPatch || now.Hour() != 3 || m.ContainerMode() {
		return
	}
	day := now.Format("2006-01-02")
	if state.AutoDay == day {
		return
	}
	offer, err := m.offer(ctx)
	if err != nil || offer == nil {
		return
	}
	o := offer.Offer()
	to, err := ParseSemver(offer.ToVersion)
	if err != nil || to.Prerelease() || o.Kind != KindPatch || !o.AllPatch || o.Asset == nil {
		return
	}
	// 03:00 is also the default backup hour: let a running backup, or the
	// scheduled one that is due, go first (this runs again every minute of the hour).
	if svc, ok := m.backups(); ok {
		if h, ok := svc.(backup.Holder); ok {
			if what, busy := h.Pending(ctx, now); busy {
				m.d.Log.Info("update: the automatic patch install waits for backup work", "work", what)
				return
			}
		}
	}
	state.AutoDay = day
	m.saveCheckState(ctx, state)
	if _, err := m.Install(ctx, offer.ID, "", InstallOptions{Keep: cfg.KeepBackup, Auto: true}); err != nil {
		m.d.Log.Warn("update: automatic patch install", "version", offer.ToVersion, "err", err)
	}
}

// Wait blocks until background installs started by this process returned
// (they hand over to the next process at the restart).
func (m *Module) Wait() { m.wg.Wait() }
