package update

import (
	"context"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

// Page is the G14 update page for admins.
const Page = "/admin/update"

// SettingsPage is the update settings page ("Update settings" in G5 Server).
const SettingsPage = "/admin/update/settings"

// Status is the update state other modules show, provided through
// core.Provide[update.Status] (use core.Use[update.Status](d) in Init or a
// handler). G5's Server tab shows "Version 1.0.2 · up to date" or an
// "Update to {Version}" button linking to update.Page.
type Status interface {
	// UpdateStatus never contacts the release address: it reads what the
	// last check stored.
	UpdateStatus(ctx context.Context) Info
}

// Info is a snapshot of the update state.
type Info struct {
	// Running is this server's version.
	Running string `json:"running"`
	// ChecksOn reports that the admin turned "Check for updates" on.
	ChecksOn bool `json:"checks_on"`
	// CheckedAt is the last check (nil before the first).
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	// CheckError is the last check's error code ("" when it worked).
	CheckError string `json:"check_error,omitempty"`
	// Available reports a verified release newer than Running.
	Available bool `json:"available"`
	// Version and Kind (patch, minor, major) describe that release.
	Version string `json:"version,omitempty"`
	Kind    string `json:"kind,omitempty"`
	// Installing reports an install in progress (Step is the updates.status).
	Installing bool   `json:"installing"`
	Step       string `json:"step,omitempty"`
}

// UpdateStatus implements Status.
func (m *Module) UpdateStatus(ctx context.Context) Info {
	cfg := m.config(ctx)
	state := m.checkState(ctx)
	info := Info{Running: m.running(), ChecksOn: cfg.Checks && cfg.ReleaseURL != "", CheckedAt: state.CheckedAt, CheckError: state.Error}
	if rec, err := m.activeAttempt(ctx); err == nil && rec != nil {
		info.Installing, info.Step, info.Version = true, rec.Status, rec.ToVersion
		return info
	}
	if offer, err := m.offer(ctx); err == nil && offer != nil {
		info.Available, info.Version, info.Kind = true, offer.ToVersion, offer.Offer().Kind
	}
	return info
}

// AvailableUpdate implements adminapi.UpdateNotice for G5's "Update to {version}"
// header button: the verified release on offer (or being installed), and
// nothing when the server is up to date or checks found nothing.
func (m *Module) AvailableUpdate(ctx context.Context) (string, bool) {
	info := m.UpdateStatus(ctx)
	if info.Available || info.Installing {
		return info.Version, true
	}
	return "", false
}

// MaintenanceProgress is G6's progress line while an update runs, in the
// request's language ("Started 21:04 · step 3 of 5 · migrating database"),
// and "" otherwise. The maintenance page shows it once the web layer asks for
// it (see the root hook in the Phase 4 report).
func (m *Module) MaintenanceProgress(ctx context.Context) string {
	rec, err := m.activeAttempt(ctx)
	if err != nil || rec == nil || rec.StartedAt == nil {
		return ""
	}
	n, key := 1, rec.Status
	switch rec.Status {
	case StatusDownloading, StatusVerifying:
		n = 2
	case StatusMigrating:
		n = 3
	case StatusHealthCheck:
		n = 4
		if rec.Run().Phase == PhaseRestarting {
			n, key = 5, PhaseRestarting
		}
	}
	v := view{Loc: userLocation(ctx)}
	return i18n.T(ctx, "update.maintenance.progress", "time", v.clock(rec.StartedAt), "n", n,
		"step", i18n.T(ctx, "update.maintenance.step."+key))
}
