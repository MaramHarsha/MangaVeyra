package secrets

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealOpen(t *testing.T) {
	b, err := Load(GenerateKey(), "")
	if err != nil {
		t.Fatal(err)
	}
	s := b.SealString("sk-live-abc123", "provider:1:key")
	if strings.Contains(s, "abc123") || !strings.HasPrefix(s, "v1:") {
		t.Fatalf("sealed value leaks plaintext: %s", s)
	}
	pt, err := b.OpenString(s, "provider:1:key")
	if err != nil || pt != "sk-live-abc123" {
		t.Fatalf("open: %q %v", pt, err)
	}
	if _, err := b.OpenString(s, "provider:2:key"); err != ErrDecrypt {
		t.Fatalf("aad not enforced: %v", err)
	}
	tampered := s[:len(s)-2] + "AA"
	if _, err := b.OpenString(tampered, "provider:1:key"); err != ErrDecrypt {
		t.Fatalf("tampering not detected: %v", err)
	}
	if s2 := b.SealString("sk-live-abc123", "provider:1:key"); s2 == s {
		t.Fatal("nonce reuse")
	}
	if v, err := b.OpenString("", "x"); v != "" || err != nil {
		t.Fatal("empty round trip")
	}
	other, _ := Load(GenerateKey(), "")
	if _, err := other.OpenString(s, "provider:1:key"); err != ErrDecrypt {
		t.Fatal("different key opened value")
	}
}

func TestKeyFileCreatedWithPrivateMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "secret.key")
	b1, err := Load("", path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	b2, err := Load("", path)
	if err != nil {
		t.Fatal(err)
	}
	s := b1.SealString("hello", "a")
	if v, err := b2.OpenString(s, "a"); err != nil || v != "hello" {
		t.Fatal("reloaded key differs")
	}
	if !bytes.Equal(b1.Derive("media"), b2.Derive("media")) || bytes.Equal(b1.Derive("media"), b1.Derive("other")) {
		t.Fatal("derive not deterministic/separated")
	}
}

func TestParseKey(t *testing.T) {
	hexKey := strings.Repeat("ab", 32)
	if k, err := ParseKey(hexKey); err != nil || len(k) != 32 {
		t.Fatalf("hex: %v", err)
	}
	if k, err := ParseKey("a long passphrase for the server"); err != nil || len(k) != 32 {
		t.Fatalf("passphrase: %v", err)
	}
	if _, err := ParseKey("short"); err == nil {
		t.Fatal("short key accepted")
	}
	if Hint("sk-1234567890abcd") != "…abcd" {
		t.Fatal(Hint("sk-1234567890abcd"))
	}
}
