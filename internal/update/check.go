package update

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core/netguard"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/notify"
)

// Check errors.
var (
	// ErrChecksOff refuses a check while "Check for updates" is off: nothing is contacted.
	ErrChecksOff = errors.New("update: update checks are off")
	// ErrVersion refuses checks on a build whose version is not a release version.
	ErrVersion = errors.New("update: this build's version is not a semantic version")
	// ErrChannel refuses a manifest published for another channel.
	ErrChannel = errors.New("update: the manifest is for another channel")
)

// Check error codes (CheckState.Error).
const (
	checkNoKey     = "no_key"
	checkFetch     = "fetch"
	checkSignature = "signature"
	checkManifest  = "manifest"
	checkChannel   = "channel"
	checkVersion   = "version"
)

const userAgent = "MangaVeyra-Updater"

// client returns the HTTP client for the release address: the SSRF guard
// (private networks only when the admin allows them) unless a test replaced it.
func (m *Module) client(timeout time.Duration) *http.Client {
	if m.hooks.Client != nil {
		return m.hooks.Client
	}
	return netguard.Policy{AllowPrivate: m.d.Cfg.AllowPrivateProviderURLs, AllowHTTP: true}.Client(timeout)
}

func (m *Module) keys() []ed25519.PublicKey {
	if m.hooks.Keys != nil {
		return m.hooks.Keys
	}
	return trustedKeys()
}

func (m *Module) now() time.Time {
	if m.hooks.Now != nil {
		return m.hooks.Now()
	}
	return time.Now()
}

func (m *Module) running() string {
	if m.hooks.Version != "" {
		return m.hooks.Version
	}
	return m.d.Version.Version
}

func (m *Module) platform() (string, string) {
	if m.hooks.GOOS != "" {
		return m.hooks.GOOS, m.hooks.GOARCH
	}
	return runtime.GOOS, runtime.GOARCH
}

// fetchManifest downloads and verifies the channel's manifest. It never
// contacts anything when checks are off or when this build has no key.
func (m *Module) fetchManifest(ctx context.Context, cfg Config) (*Manifest, []byte, error) {
	if !cfg.Checks || cfg.ReleaseURL == "" {
		return nil, nil, ErrChecksOff
	}
	keys := m.keys()
	if len(keys) == 0 {
		return nil, nil, ErrNoKey
	}
	client := m.client(30 * time.Second)
	murl := ManifestURL(cfg.ReleaseURL, cfg.Channel)
	raw, err := fetch(ctx, client, murl, maxManifestBytes)
	if err != nil {
		return nil, nil, err
	}
	sig, err := fetch(ctx, client, murl+".sig", maxSignatureBytes)
	if err != nil {
		return nil, nil, err
	}
	man, err := VerifyManifest(raw, sig, keys)
	if err != nil {
		return nil, nil, err
	}
	if man.Channel != cfg.Channel {
		return nil, nil, fmt.Errorf("%w (%s)", ErrChannel, man.Channel)
	}
	return man, sig, nil
}

type fetchError struct{ err error }

func (e fetchError) Error() string { return e.err.Error() }
func (e fetchError) Unwrap() error { return e.err }

func fetch(ctx context.Context, c *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fetchError{err}
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.Do(req)
	if err != nil {
		return nil, fetchError{err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fetchError{fmt.Errorf("GET %s: %s", url, resp.Status)}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fetchError{err}
	}
	if int64(len(b)) > limit {
		return nil, fetchError{fmt.Errorf("GET %s: larger than %d bytes", url, limit)}
	}
	return b, nil
}

func checkCode(err error) string {
	var fe fetchError
	switch {
	case errors.Is(err, ErrNoKey):
		return checkNoKey
	case errors.Is(err, ErrBadSignature):
		return checkSignature
	case errors.Is(err, ErrBadManifest):
		return checkManifest
	case errors.Is(err, ErrChannel):
		return checkChannel
	case errors.Is(err, ErrVersion):
		return checkVersion
	case errors.As(err, &fe):
		return checkFetch
	}
	return checkFetch
}

// Check contacts the release address once and records what it found. It
// returns the current offer (nil when the server is up to date).
func (m *Module) Check(ctx context.Context) (*Record, error) {
	cfg := m.config(ctx)
	if !cfg.Checks || cfg.ReleaseURL == "" {
		return nil, ErrChecksOff
	}
	if active, err := m.activeAttempt(ctx); err != nil || active != nil {
		return nil, ErrBusy // the result is checked again after the install
	}
	state := m.checkState(ctx)
	now := m.now().UTC()
	state.CheckedAt = &now
	fail := func(err error) (*Record, error) {
		state.Error, state.Detail = checkCode(err), truncate(err.Error(), 300)
		m.saveCheckState(ctx, state)
		return nil, err
	}
	cur, err := ParseSemver(m.running())
	if err != nil {
		return fail(fmt.Errorf("%w: %s", ErrVersion, m.running()))
	}
	man, sig, err := m.fetchManifest(ctx, cfg)
	if err != nil {
		return fail(err)
	}
	state.Error, state.Detail, state.Latest = "", "", man.Version
	next, _ := ParseSemver(man.Version)
	offer, err := m.offer(ctx)
	if err != nil {
		return fail(err)
	}
	if !Eligible(cur, next, cfg.Channel) {
		if offer != nil && offer.BackupID == nil {
			_ = m.dropOffer(ctx, offer.ID)
		}
		m.saveCheckState(ctx, state)
		return nil, nil
	}
	o := Offer{Kind: Classify(cur, next), Published: man.Published, NotesURL: man.NotesURL, Releases: man.ReleasesAfter(cur), AllPatch: true}
	for _, r := range o.Releases {
		o.Security = o.Security || r.Security
		o.AllPatch = o.AllPatch && r.Patch
	}
	if len(o.Releases) == 0 {
		o.AllPatch = false
	}
	goos, goarch := m.platform()
	rec := &Record{FromVersion: cur.String(), ToVersion: next.String(), Channel: cfg.Channel, Signature: string(sig)}
	if a, ok := man.AssetFor(goos, goarch); ok {
		o.Asset = &a
		rec.DownloadURL = a.URL
	}
	rec.Changelog = mustJSON(o)
	if err := m.saveOffer(ctx, rec); err != nil {
		return fail(err)
	}
	rec.Status = StatusAvailable
	if state.Notified != rec.ToVersion {
		m.notifyAdmins(ctx, rec)
		state.Notified = rec.ToVersion
	}
	m.saveCheckState(ctx, state)
	return rec, nil
}

// notifyAdmins tells every active admin once per release (notification kind "update").
func (m *Module) notifyAdmins(ctx context.Context, rec *Record) {
	if m.d.Notify == nil {
		return
	}
	var admins []struct {
		ID     string `db:"id"`
		Locale string `db:"locale"`
	}
	if err := m.d.DB.Select(ctx, &admins, `SELECT id, locale FROM users WHERE role = 'admin' AND status = 'active'`); err != nil {
		m.d.Log.Warn("update: list admins", "err", err)
		return
	}
	def := m.d.Settings.MustInstance(ctx).DefaultLocale
	for _, a := range admins {
		lctx := i18n.WithLocale(ctx, i18n.Negotiate(m.d.I18n.Languages(), def, a.Locale, "", ""))
		_, err := m.d.Notify.Notify(ctx, notify.Input{UserID: a.ID, Kind: notify.KindUpdate,
			Title: i18n.T(lctx, "update.notify.title", "version", rec.ToVersion),
			Body:  i18n.T(lctx, "update.notify.body", "current", rec.FromVersion),
			Link:  "/admin/update", GroupKey: "update:" + rec.ToVersion})
		if err != nil {
			m.d.Log.Warn("update: notify admin", "err", err)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
