package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// HashParams are argon2id parameters. The encoded hash records them, so
// changing the defaults later still verifies old hashes (and NeedsRehash
// upgrades them at the next login).
type HashParams struct {
	Memory  uint32 // KiB
	Time    uint32
	Threads uint8
	SaltLen uint32
	KeyLen  uint32
}

// DefaultHashParams follow the OWASP argon2id recommendation with extra memory (64 MiB, t=2, p=2).
var DefaultHashParams = HashParams{Memory: 64 * 1024, Time: 2, Threads: 2, SaltLen: 16, KeyLen: 32}

// FastHashParams are for tests only.
var FastHashParams = HashParams{Memory: 1024, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}

// Password length limits (runes). The maximum bounds hashing cost.
const (
	MinPasswordLen = 10
	MaxPasswordLen = 256
)

// hashSem bounds concurrent argon2 computations so a login flood cannot exhaust memory.
var hashSem = make(chan struct{}, 4)

// HashPassword returns an encoded argon2id hash:
// $argon2id$v=19$m=65536,t=2,p=2$<salt>$<hash> (raw std base64).
func HashPassword(password string, p HashParams) (string, error) {
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hashSem <- struct{}{}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	<-hashSem
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.Memory, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

var errBadHash = errors.New("auth: malformed password hash")

func decodeHash(encoded string) (HashParams, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return HashParams{}, nil, nil, errBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return HashParams{}, nil, nil, errBadHash
	}
	var p HashParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return HashParams{}, nil, nil, errBadHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return HashParams{}, nil, nil, errBadHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return HashParams{}, nil, nil, errBadHash
	}
	p.SaltLen, p.KeyLen = uint32(len(salt)), uint32(len(key))
	if p.Memory > 1<<21 || p.Time > 16 || p.Threads == 0 || p.KeyLen == 0 { // refuse absurd params from a tampered row
		return HashParams{}, nil, nil, errBadHash
	}
	return p, salt, key, nil
}

// VerifyPassword reports whether password matches encoded, in constant time.
func VerifyPassword(password, encoded string) bool {
	p, salt, key, err := decodeHash(encoded)
	if err != nil {
		return false
	}
	hashSem <- struct{}{}
	other := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	<-hashSem
	return subtle.ConstantTimeCompare(key, other) == 1
}

// NeedsRehash reports whether encoded was made with parameters other than p.
func NeedsRehash(encoded string, p HashParams) bool {
	hp, _, _, err := decodeHash(encoded)
	if err != nil {
		return true
	}
	return hp.Memory != p.Memory || hp.Time != p.Time || hp.Threads != p.Threads || hp.KeyLen != p.KeyLen
}

// CheckPasswordPolicy validates a new password's length.
func CheckPasswordPolicy(password string) error {
	n := utf8.RuneCountInString(password)
	if n < MinPasswordLen {
		return fieldError("password", "auth.password.too_short", "min", MinPasswordLen)
	}
	if n > MaxPasswordLen {
		return fieldError("password", "auth.password.too_long", "max", MaxPasswordLen)
	}
	return nil
}
