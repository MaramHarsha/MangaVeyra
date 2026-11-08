package update

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"time"

	"github.com/a-h/templ"

	"github.com/MaramHarsha/MangaVeyra/internal/backup"
)

// TestHooks replace the environment for the integration tests in update_test.
type TestHooks struct {
	Client        *http.Client
	Keys          []ed25519.PublicKey
	Now           func() time.Time
	Version       string
	Backups       backup.Service
	NoBackups     bool
	Executable    string
	Container     *bool
	HealthTimeout time.Duration
	Restart       func(exe string)
	// DownloadIdle replaces the longest pause a download may make.
	DownloadIdle time.Duration
}

// SetTestHooks installs h on m.
func SetTestHooks(m *Module, h TestHooks) {
	m.hooks = hooks{Client: h.Client, Keys: h.Keys, Now: h.Now, Version: h.Version, Backups: h.Backups, NoBackups: h.NoBackups,
		Executable: h.Executable, Container: h.Container, HealthTimeout: h.HealthTimeout, Restart: h.Restart,
		Download: downloadLimits{Idle: h.DownloadIdle}}
}

// SetVersion changes the version m believes it runs (a restarted process).
func SetVersion(m *Module, v string) { m.hooks.Version = v }

// Resume runs the start-up work of a new process.
func Resume(m *Module, ctx context.Context) error { return m.resume(ctx) }

// Tick runs the background loop once.
func Tick(m *Module, ctx context.Context) { m.tick(ctx) }

// SaveConfig stores the admin settings.
func SaveConfig(m *Module, ctx context.Context, c Config) error { return m.saveConfig(ctx, c, "") }

// CurrentOffer returns the available offer.
func CurrentOffer(m *Module, ctx context.Context) (*Record, error) { return m.offer(ctx) }

// LatestAttempt returns the newest install attempt.
func LatestAttempt(m *Module, ctx context.Context) (*Record, error) { return m.latestAttempt(ctx) }

// GetCheckState returns the last check's result.
func GetCheckState(m *Module, ctx context.Context) CheckState { return m.checkState(ctx) }

// HoldLockAs takes the install lock as another process would.
func HoldLockAs(m *Module, ctx context.Context, updateID, owner string) (bool, error) {
	return m.acquireLock(ctx, lockValue{UpdateID: updateID, Owner: owner})
}

// TakeOverLock replaces an expired lease whose value was observed as raw, as
// acquireLock does after reading it.
func TakeOverLock(m *Module, ctx context.Context, raw, updateID, owner string) (bool, error) {
	return m.takeOverLock(ctx, raw, lockValue{UpdateID: updateID, Owner: owner})
}

// LockValue is the stored value of the install lock held by owner.
func LockValue(updateID, owner string) string {
	return lockValue{UpdateID: updateID, Owner: owner}.encode()
}

// SnoozeCookie is the "Remind me next login" cookie value for a session.
func SnoozeCookie(sessionID, version string) string { return snoozeValue(sessionID, version) }

// ShellFor exposes the app-shell extras for a request.
func ShellFor(m *Module, r *http.Request) (templ.Component, templ.Component) { return m.Shell(r) }
