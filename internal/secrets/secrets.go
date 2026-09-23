// Package secrets encrypts values at rest (provider API keys, World tokens,
// TOTP secrets, VAPID keys) with AES-256-GCM, and derives purpose-specific
// keys (media URL signing, cookie signing) from the one master key.
//
// The master key comes from MV_MASTER_KEY or, when unset, from
// <data>/secret.key, which is generated on first run with mode 0600.
// Losing the master key makes every sealed value unreadable, so backups
// include it only when the admin asks for it.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// KeySize is the master key size in bytes.
const KeySize = 32

const prefixV1 = "v1:"

// ErrDecrypt is returned when a sealed value cannot be opened (wrong key, tampering, truncation).
var ErrDecrypt = errors.New("secrets: cannot decrypt value (wrong master key or corrupted data)")

// Box seals and opens values with AES-256-GCM.
type Box struct {
	master []byte
	aead   cipher.AEAD
}

// New creates a box from a 32-byte master key.
func New(master []byte) (*Box, error) {
	if len(master) != KeySize {
		return nil, fmt.Errorf("secrets: master key must be %d bytes, got %d", KeySize, len(master))
	}
	encKey, err := hkdf.Key(sha256.New, master, nil, "mangaveyra/secrets/aes-gcm/v1", KeySize)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	m := make([]byte, KeySize)
	copy(m, master)
	return &Box{master: m, aead: aead}, nil
}

// ParseKey decodes a master key given as 64 hex characters or standard/URL
// base64 of 32 bytes. Any other non-empty string is treated as a passphrase
// and stretched with SHA-256 (acceptable because it is a server secret, not a
// user password; prefer a generated key).
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("secrets: empty master key")
	}
	if len(s) == 64 {
		if b, err := hex.DecodeString(s); err == nil {
			return b, nil
		}
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == KeySize {
			return b, nil
		}
	}
	if len(s) < 16 {
		return nil, errors.New("secrets: MV_MASTER_KEY is too short (use `openssl rand -base64 32`)")
	}
	sum := sha256.Sum256([]byte("mangaveyra-master-key:" + s))
	return sum[:], nil
}

// GenerateKey returns a new random master key encoded as base64.
func GenerateKey() string {
	b := make([]byte, KeySize)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// Load returns a box for the configured key: masterKey when non-empty,
// otherwise the key stored at path, created (0600) when missing.
func Load(masterKey, path string) (*Box, error) {
	if masterKey != "" {
		k, err := ParseKey(masterKey)
		if err != nil {
			return nil, err
		}
		return New(k)
	}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		k, err := ParseKey(string(b))
		if err != nil {
			return nil, fmt.Errorf("secrets: %s: %w", path, err)
		}
		return New(k)
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, err
		}
		enc := GenerateKey()
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			if errors.Is(err, os.ErrExist) { // another process won the race
				return Load("", path)
			}
			return nil, fmt.Errorf("secrets: create %s: %w", path, err)
		}
		if _, err := f.WriteString(enc + "\n"); err != nil {
			_ = f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
		k, _ := ParseKey(enc)
		return New(k)
	default:
		return nil, fmt.Errorf("secrets: read %s: %w", path, err)
	}
}

// Seal encrypts plaintext bound to aad (additional authenticated data, e.g.
// "provider:01H...:key") and returns "v1:" + base64url(nonce || ciphertext).
// Binding to aad stops a sealed value being copied into another row.
func (b *Box) Seal(plaintext, aad []byte) string {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	out := b.aead.Seal(nonce, nonce, plaintext, aad)
	return prefixV1 + base64.RawURLEncoding.EncodeToString(out)
}

// Open decrypts a value produced by Seal with the same aad.
func (b *Box) Open(sealed string, aad []byte) ([]byte, error) {
	if !strings.HasPrefix(sealed, prefixV1) {
		return nil, ErrDecrypt
	}
	raw, err := base64.RawURLEncoding.DecodeString(sealed[len(prefixV1):])
	if err != nil || len(raw) < b.aead.NonceSize()+b.aead.Overhead() {
		return nil, ErrDecrypt
	}
	ns := b.aead.NonceSize()
	pt, err := b.aead.Open(nil, raw[:ns], raw[ns:], aad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// SealString is Seal for strings; the empty string seals to "".
func (b *Box) SealString(plaintext, aad string) string {
	if plaintext == "" {
		return ""
	}
	return b.Seal([]byte(plaintext), []byte(aad))
}

// OpenString is Open for strings; "" opens to "".
func (b *Box) OpenString(sealed, aad string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	pt, err := b.Open(sealed, []byte(aad))
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// Derive returns a 32-byte key for purpose (e.g. "media-urls"), derived from
// the master key with HKDF-SHA256. The same purpose always yields the same key.
func (b *Box) Derive(purpose string) []byte {
	k, err := hkdf.Key(sha256.New, b.master, nil, "mangaveyra/derive/"+purpose, 32)
	if err != nil {
		panic(err)
	}
	return k
}

// Hint returns a safe display hint for a secret such as an API key: the last
// four characters, e.g. "…7f3a". Keys are never shown in full after saving.
func Hint(secret string) string {
	secret = strings.TrimSpace(secret)
	if len(secret) <= 8 {
		return "…"
	}
	return "…" + secret[len(secret)-4:]
}
