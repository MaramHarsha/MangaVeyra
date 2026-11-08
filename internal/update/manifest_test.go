package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func sampleManifest() Manifest {
	return Manifest{
		Version: "1.0.2", Channel: ChannelStable, Published: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC),
		Changelog: []Release{
			{Version: "1.0.2", Patch: true, Security: true, Notes: []string{"Security: tighter upload checks"}},
			{Version: "1.0.1", Patch: true, Notes: []string{"Fixes panel locks being ignored by batch jobs"}},
			{Version: "1.0.0", Notes: []string{"First release"}},
		},
		Assets: []Asset{{OS: "linux", Arch: "amd64", URL: "https://releases.example.org/mangaveyra-1.0.2-linux-amd64",
			SHA256: strings.Repeat("ab", 32), Size: 1234}},
	}
}

func encode(t *testing.T, m Manifest) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestVerifyManifest(t *testing.T) {
	pub, priv := testKey(t)
	raw := encode(t, sampleManifest())
	sig := SignManifest(raw, priv)
	keys := []ed25519.PublicKey{pub}

	m, err := VerifyManifest(raw, sig, keys)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if m.Version != "1.0.2" || len(m.Assets) != 1 {
		t.Fatalf("manifest = %+v", m)
	}

	tampered := bytes.Replace(raw, []byte("1.0.2"), []byte("1.0.3"), 1)
	if _, err := VerifyManifest(tampered, sig, keys); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered manifest: %v", err)
	}
	otherPub, _ := testKey(t)
	if _, err := VerifyManifest(raw, sig, []ed25519.PublicKey{otherPub}); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("wrong key: %v", err)
	}
	if _, err := VerifyManifest(raw, sig, nil); !errors.Is(err, ErrNoKey) {
		t.Fatalf("no key: %v", err)
	}
	if _, err := VerifyManifest(raw, []byte("not base64!"), keys); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("garbage signature: %v", err)
	}
	// A second trusted key (rotation) still verifies.
	if _, err := VerifyManifest(raw, sig, []ed25519.PublicKey{otherPub, pub}); err != nil {
		t.Fatalf("rotated keys: %v", err)
	}
	// A correctly signed but invalid manifest is still refused.
	bad := sampleManifest()
	bad.Assets[0].SHA256 = "nothex"
	braw := encode(t, bad)
	if _, err := VerifyManifest(braw, SignManifest(braw, priv), keys); !errors.Is(err, ErrBadManifest) {
		t.Fatalf("invalid manifest: %v", err)
	}
}

func TestManifestValidation(t *testing.T) {
	cases := map[string]func(*Manifest){
		"version":   func(m *Manifest) { m.Version = "latest" },
		"channel":   func(m *Manifest) { m.Channel = "nightly" },
		"url":       func(m *Manifest) { m.Assets[0].URL = "file:///etc/passwd" },
		"userinfo":  func(m *Manifest) { m.Assets[0].URL = "https://u:p@example.org/x" },
		"size":      func(m *Manifest) { m.Assets[0].Size = 0 },
		"too large": func(m *Manifest) { m.Assets[0].Size = MaxAssetBytes + 1 },
		"upper hex": func(m *Manifest) { m.Assets[0].SHA256 = strings.Repeat("AB", 32) },
		"duplicate": func(m *Manifest) { m.Assets = append(m.Assets, m.Assets[0]) },
		"format":    func(m *Manifest) { m.Assets[0].Format = "zip" },
		"notes url": func(m *Manifest) { m.NotesURL = "javascript:alert(1)" },
	}
	for name, mutate := range cases {
		m := sampleManifest()
		mutate(&m)
		if _, err := ParseManifest(encode(t, m)); !errors.Is(err, ErrBadManifest) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := ParseManifest([]byte(`{"version":"1.0.2","channel":"stable","extra":true}`)); !errors.Is(err, ErrBadManifest) {
		t.Errorf("unknown fields must be refused: %v", err)
	}
}

func TestManifestHelpers(t *testing.T) {
	m := sampleManifest()
	if a, ok := m.AssetFor("linux", "amd64"); !ok || a.Format != FormatBinary {
		t.Fatalf("AssetFor = %+v %v", a, ok)
	}
	if _, ok := m.AssetFor("darwin", "arm64"); ok {
		t.Fatal("no darwin asset")
	}
	rel := m.ReleasesAfter(Semver{Major: 1})
	if len(rel) != 2 || rel[0].Version != "1.0.2" || rel[1].Version != "1.0.1" {
		t.Fatalf("ReleasesAfter = %+v", rel)
	}
	if u := ManifestURL("https://releases.example.org/mv/", ChannelBeta); u != "https://releases.example.org/mv/beta.json" {
		t.Fatalf("ManifestURL = %s", u)
	}
}

func TestParseKeysAndDefault(t *testing.T) {
	pub, _ := testKey(t)
	enc := base64.StdEncoding.EncodeToString(pub)
	keys, err := ParseKeys(" " + enc + " , ")
	if err != nil || len(keys) != 1 {
		t.Fatalf("ParseKeys = %v %v", keys, err)
	}
	if _, err := ParseKeys("abc"); err == nil {
		t.Fatal("a short key must be refused")
	}
	if TrustedKeys != "" || HasTrustedKey() {
		t.Fatal("the default build must have no trusted key, so updates are refused")
	}
	old := TrustedKeys
	TrustedKeys = "not-a-key"
	defer func() { TrustedKeys = old }()
	if HasTrustedKey() {
		t.Fatal("a malformed compiled-in key must count as no key")
	}
}
