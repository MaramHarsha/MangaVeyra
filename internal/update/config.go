package update

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Settings keys (instance_settings, module namespace "update.").
const (
	keyConfig = "update.config"
	keyCheck  = "update.check"
	keyLock   = "update.lock"
)

// Config is what the admin sets on /admin/update/settings. Everything is off
// by default, so a new server never contacts a release address (ADR 0013 §6).
type Config struct {
	// Checks turns on "Check for updates": once a day and on "Check now".
	Checks bool `json:"checks"`
	// ReleaseURL is the base address of the signed release feed; the
	// manifest is <ReleaseURL>/<Channel>.json. Empty until the owner publishes releases.
	ReleaseURL string `json:"release_url"`
	// Channel is "stable" (default) or "beta".
	Channel string `json:"channel"`
	// AutoPatch installs patch releases automatically at 03:00 server time.
	AutoPatch bool `json:"auto_patch"`
	// KeepBackup remembers "Keep the backup for 7 days instead of deleting it".
	KeepBackup bool `json:"keep_backup"`
}

// DefaultConfig is the configuration of a new server.
func DefaultConfig() Config { return Config{Channel: ChannelStable} }

// Validate checks the channel and the release address.
func (c Config) Validate() error {
	var errs []error
	if !ValidChannel(c.Channel) {
		errs = append(errs, fmt.Errorf("channel %q must be stable or beta", c.Channel))
	}
	if c.ReleaseURL != "" {
		u, err := url.Parse(c.ReleaseURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			errs = append(errs, errors.New("the release address must be an http(s) URL without a query or credentials"))
		}
	}
	if c.Checks && c.ReleaseURL == "" {
		errs = append(errs, errors.New("checks need a release address"))
	}
	return errors.Join(errs...)
}

// Normalize trims the release address and defaults the channel.
func (c *Config) Normalize() {
	c.ReleaseURL = strings.TrimRight(strings.TrimSpace(c.ReleaseURL), "/")
	if c.Channel == "" {
		c.Channel = ChannelStable
	}
}

// CheckState records the last check (instance_settings "update.check").
type CheckState struct {
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	// Error is a public code: no_key, fetch, signature, manifest, channel, version, platform.
	Error  string `json:"error,omitempty"`
	Detail string `json:"detail,omitempty"`
	// Latest is the newest version the feed offered at the last good check.
	Latest string `json:"latest,omitempty"`
	// Notified is the version admins were last told about.
	Notified string `json:"notified,omitempty"`
	// AutoDay is the server-local date (2006-01-02) of the last automatic patch attempt.
	AutoDay string `json:"auto_day,omitempty"`
}

func (m *Module) config(ctx context.Context) Config {
	c := DefaultConfig()
	if _, err := m.d.Settings.GetKey(ctx, keyConfig, &c); err != nil {
		m.d.Log.Warn("update: read settings", "err", err)
		return DefaultConfig()
	}
	c.Normalize()
	return c
}

func (m *Module) saveConfig(ctx context.Context, c Config, by string) error {
	c.Normalize()
	if err := c.Validate(); err != nil {
		return err
	}
	return m.d.Settings.SetKey(ctx, keyConfig, c, by)
}

func (m *Module) checkState(ctx context.Context) CheckState {
	var s CheckState
	if _, err := m.d.Settings.GetKey(ctx, keyCheck, &s); err != nil {
		m.d.Log.Warn("update: read check state", "err", err)
	}
	return s
}

func (m *Module) saveCheckState(ctx context.Context, s CheckState) {
	if err := m.d.Settings.SetKey(ctx, keyCheck, s, ""); err != nil {
		m.d.Log.Warn("update: save check state", "err", err)
	}
}
