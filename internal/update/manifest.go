package update

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TrustedKeys is the comma-separated list of base64 (standard encoding)
// Ed25519 public keys whose signatures this build accepts on release
// manifests. It is set at build time:
//
//	go build -ldflags "-X github.com/MaramHarsha/MangaVeyra/internal/update.TrustedKeys=<base64 public key>" ./cmd/mangaveyra
//
// The default is empty: a build without a key refuses every update (and does
// not contact the release address at all), because it cannot verify one.
// `mangaveyra release keygen` creates a key pair.
var TrustedKeys = ""

// Limits on what the server reads from the release address.
const (
	maxManifestBytes  = 1 << 20 // 1 MiB
	maxSignatureBytes = 4 << 10
	// MaxAssetBytes caps a release download (the manifest's size must also match).
	MaxAssetBytes = 1 << 30
)

// Asset formats.
const (
	FormatBinary = "binary" // the executable itself
	FormatTarGz  = "tar.gz" // a gzip tarball holding a regular file named "mangaveyra"
)

// Manifest describes one release on a channel. The server reads it from
// <release address>/<channel>.json and its detached Ed25519 signature from
// <release address>/<channel>.json.sig (the base64 signature of the exact
// manifest bytes).
type Manifest struct {
	// Version is the release's semantic version (no leading "v").
	Version string `json:"version"`
	// Channel is "stable" or "beta"; it must match the channel the server follows.
	Channel string `json:"channel"`
	// Published is the release time (RFC 3339).
	Published time.Time `json:"published"`
	// NotesURL optionally links the full release notes ("Full changelog").
	NotesURL string `json:"notes_url,omitempty"`
	// Changelog lists this and earlier releases, newest first.
	Changelog []Release `json:"changelog"`
	// Assets are the downloads per platform.
	Assets []Asset `json:"assets"`
}

// Release is one changelog entry.
type Release struct {
	Version string `json:"version"`
	// Patch marks a fixes-only release; automatic patch installs need it on every release they skip over.
	Patch bool `json:"patch,omitempty"`
	// Security marks a release with a security fix.
	Security bool     `json:"security,omitempty"`
	Notes    []string `json:"notes"`
}

// Asset is one platform's download.
type Asset struct {
	OS     string `json:"os"`   // GOOS, e.g. "linux"
	Arch   string `json:"arch"` // GOARCH, e.g. "amd64"
	URL    string `json:"url"`
	SHA256 string `json:"sha256"` // lowercase hex of the downloaded file
	Size   int64  `json:"size"`   // bytes of the downloaded file
	// Format is "binary" (default) or "tar.gz".
	Format string `json:"format,omitempty"`
}

// Verification errors. Every one of them refuses the update.
var (
	ErrNoKey        = errors.New("update: this build has no trusted release key")
	ErrBadSignature = errors.New("update: the release signature does not verify")
	ErrBadManifest  = errors.New("update: the release manifest is invalid")
	ErrChecksum     = errors.New("update: the download does not match the signed checksum")
	ErrSize         = errors.New("update: the download size does not match the signed size")
)

// ParseKeys decodes a comma-separated list of base64 Ed25519 public keys.
func ParseKeys(list string) ([]ed25519.PublicKey, error) {
	var keys []ed25519.PublicKey
	for _, part := range strings.Split(list, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(part)
		if err != nil || len(b) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("update: %q is not a base64 Ed25519 public key", part)
		}
		keys = append(keys, ed25519.PublicKey(b))
	}
	return keys, nil
}

// trustedKeys returns the compiled-in keys; a malformed value counts as none.
func trustedKeys() []ed25519.PublicKey {
	keys, err := ParseKeys(TrustedKeys)
	if err != nil {
		return nil
	}
	return keys
}

// HasTrustedKey reports whether this build can verify releases at all.
func HasTrustedKey() bool { return len(trustedKeys()) > 0 }

// VerifyManifest checks the detached signature over raw with keys and then
// parses and validates the manifest. Nothing in raw is trusted before the
// signature verifies.
func VerifyManifest(raw, sig []byte, keys []ed25519.PublicKey) (*Manifest, error) {
	if len(keys) == 0 {
		return nil, ErrNoKey
	}
	s, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(s) != ed25519.SignatureSize {
		return nil, ErrBadSignature
	}
	ok := false
	for _, k := range keys {
		if ed25519.Verify(k, raw, s) {
			ok = true
			break
		}
	}
	if !ok {
		return nil, ErrBadSignature
	}
	return ParseManifest(raw)
}

// ParseManifest decodes and validates a manifest (without checking a signature).
func ParseManifest(raw []byte) (*Manifest, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadManifest, err)
	}
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadManifest, err)
	}
	return &m, nil
}

func (m *Manifest) validate() error {
	if _, err := ParseSemver(m.Version); err != nil {
		return err
	}
	if !ValidChannel(m.Channel) {
		return fmt.Errorf("channel %q must be stable or beta", m.Channel)
	}
	if m.NotesURL != "" && !httpURL(m.NotesURL) {
		return fmt.Errorf("notes_url %q is not an http(s) URL", m.NotesURL)
	}
	for _, r := range m.Changelog {
		if _, err := ParseSemver(r.Version); err != nil {
			return fmt.Errorf("changelog: %w", err)
		}
		if len(r.Notes) > 100 {
			return fmt.Errorf("changelog %s: too many notes", r.Version)
		}
		for _, n := range r.Notes {
			if len(n) > 500 {
				return fmt.Errorf("changelog %s: a note is longer than 500 bytes", r.Version)
			}
		}
	}
	if len(m.Changelog) > 200 {
		return errors.New("changelog: too many releases")
	}
	seen := map[string]bool{}
	for i, a := range m.Assets {
		key := a.OS + "/" + a.Arch
		switch {
		case a.OS == "" || a.Arch == "":
			return fmt.Errorf("asset %d: os and arch are required", i)
		case seen[key]:
			return fmt.Errorf("asset %s listed twice", key)
		case !httpURL(a.URL):
			return fmt.Errorf("asset %s: url %q is not an http(s) URL", key, a.URL)
		case !validSHA256(a.SHA256):
			return fmt.Errorf("asset %s: sha256 must be 64 lowercase hex digits", key)
		case a.Size <= 0 || a.Size > MaxAssetBytes:
			return fmt.Errorf("asset %s: size %d is outside 1 byte–1 GiB", key, a.Size)
		case a.Format != "" && a.Format != FormatBinary && a.Format != FormatTarGz:
			return fmt.Errorf("asset %s: format %q must be binary or tar.gz", key, a.Format)
		}
		seen[key] = true
	}
	return nil
}

func httpURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
}

func validSHA256(s string) bool {
	if len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// AssetFor returns the download for goos/goarch.
func (m *Manifest) AssetFor(goos, goarch string) (Asset, bool) {
	for _, a := range m.Assets {
		if a.OS == goos && a.Arch == goarch {
			if a.Format == "" {
				a.Format = FormatBinary
			}
			return a, true
		}
	}
	return Asset{}, false
}

// ReleasesAfter returns the changelog entries newer than cur up to and
// including the manifest's version, newest first.
func (m *Manifest) ReleasesAfter(cur Semver) []Release {
	top, _ := ParseSemver(m.Version)
	var out []Release
	for _, r := range m.Changelog {
		v, err := ParseSemver(r.Version)
		if err != nil || v.Compare(cur) <= 0 || v.Compare(top) > 0 {
			continue
		}
		out = append(out, r)
	}
	return out
}

// SignManifest returns the base64 detached signature of raw (release helper and tests).
func SignManifest(raw []byte, key ed25519.PrivateKey) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, raw)) + "\n")
}

// ManifestURL returns the manifest address for a release base URL and channel.
func ManifestURL(base, channel string) string {
	return strings.TrimRight(base, "/") + "/" + channel + ".json"
}
