// SPDX-License-Identifier: MIT
// Copyright (c) 2026 MangaVeyra contributors

package mvpack

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// SignatureFormat is the value of "mvpack_signature" in signature.json.
const SignatureFormat = 1

// Signer roles recorded in a signature.
const (
	// SignerServer: the key a self-hosted server registered on World when it was linked.
	SignerServer = "server"
	// SignerDeveloper: a developer's release key (plugin packs).
	SignerDeveloper = "developer"
	// SignerWorld: World's own key, added when an item is approved.
	SignerWorld = "world"
)

// AlgEd25519 is the only signature algorithm of format version 1.
const AlgEd25519 = "ed25519"

// signingContext starts every signing payload so a pack signature can never be
// confused with any other ed25519 signature made by the same key.
const signingContext = "MVPACK-SIG-1"

var reKeyID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

// SignedFile is one entry of the per-file SHA-256 list covered by the signatures.
type SignedFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Signature is one ed25519 signature over the signing payload (see SigningPayload).
type Signature struct {
	KeyID     string    `json:"key_id"`
	Signer    string    `json:"signer"`
	Alg       string    `json:"alg"`
	PublicKey string    `json:"public_key"` // base64 (standard, padded) of the 32-byte key
	SignedAt  time.Time `json:"signed_at"`
	Sig       string    `json:"sig"` // base64 (standard, padded) of the 64-byte signature
}

// SignatureFile is the content of signature.json.
type SignatureFile struct {
	Format         int          `json:"mvpack_signature"`
	ManifestSHA256 string       `json:"manifest_sha256"`
	Files          []SignedFile `json:"files"`
	Signatures     []Signature  `json:"signatures"`
}

// Signer signs packs with an ed25519 private key.
type Signer struct {
	// KeyID is the id World returned when the public key was registered
	// (POST /v1/servers), or the developer/World key id.
	KeyID string
	// Key is the private key.
	Key ed25519.PrivateKey
	// Role is SignerServer, SignerDeveloper or SignerWorld. Empty means SignerServer.
	Role string
	// Now returns the signing time; nil means time.Now.
	Now func() time.Time
}

func (s *Signer) role() string {
	if s.Role == "" {
		return SignerServer
	}
	return s.Role
}

func (s *Signer) check() error {
	if s == nil || len(s.Key) != ed25519.PrivateKeySize {
		return fmt.Errorf("%w: signer needs an ed25519 private key", ErrBadSignature)
	}
	if !reKeyID.MatchString(s.KeyID) {
		return fmt.Errorf("%w: key id %q must be 1–64 characters of A–Z, a–z, 0–9, '_', '.', ':' and '-'", ErrBadSignature, s.KeyID)
	}
	switch s.role() {
	case SignerServer, SignerDeveloper, SignerWorld:
	default:
		return fmt.Errorf("%w: unknown signer role %q", ErrBadSignature, s.Role)
	}
	return nil
}

// Sign produces a signature over the canonical manifest bytes and file list.
func (s *Signer) Sign(canonicalManifest []byte, files []SignedFile) (Signature, error) {
	if err := s.check(); err != nil {
		return Signature{}, err
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	sig := Signature{
		KeyID:     s.KeyID,
		Signer:    s.role(),
		Alg:       AlgEd25519,
		PublicKey: base64.StdEncoding.EncodeToString(s.Key.Public().(ed25519.PublicKey)),
		SignedAt:  now().UTC().Truncate(time.Second),
	}
	payload := SigningPayload(sig.KeyID, sig.Signer, sig.SignedAt, canonicalManifest, files)
	sig.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(s.Key, payload))
	return sig, nil
}

// SigningPayload returns the exact bytes an ed25519 signature covers:
//
//	MVPACK-SIG-1\n
//	key_id <key id>\n
//	signer <server|developer|world>\n
//	signed_at <RFC 3339 UTC, seconds>\n
//	manifest <byte length>\n
//	<canonical manifest bytes (RFC 8785)>\n
//	files <count>\n
//	<sha256 hex> <size> <path>\n      (one line per payload file, sorted by path bytes)
//
// Paths cannot contain spaces or newlines (see CheckPath), so the encoding is
// unambiguous.
func SigningPayload(keyID, signer string, signedAt time.Time, canonicalManifest []byte, files []SignedFile) []byte {
	sorted := slices.Clone(files)
	slices.SortFunc(sorted, func(a, b SignedFile) int { return strings.Compare(a.Path, b.Path) })
	var b bytes.Buffer
	b.WriteString(signingContext + "\n")
	b.WriteString("key_id " + keyID + "\n")
	b.WriteString("signer " + signer + "\n")
	b.WriteString("signed_at " + signedAt.UTC().Format(time.RFC3339) + "\n")
	b.WriteString("manifest " + strconv.Itoa(len(canonicalManifest)) + "\n")
	b.Write(canonicalManifest)
	b.WriteString("\nfiles " + strconv.Itoa(len(sorted)) + "\n")
	for _, f := range sorted {
		fmt.Fprintf(&b, "%s %d %s\n", f.SHA256, f.Size, f.Path)
	}
	return b.Bytes()
}

// signedFiles derives the signed file list from manifest entries, sorted by path.
func signedFiles(files []File) []SignedFile {
	out := make([]SignedFile, len(files))
	for i, f := range files {
		out[i] = SignedFile{Path: f.Path, Size: f.Size, SHA256: f.SHA256}
	}
	slices.SortFunc(out, func(a, b SignedFile) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// newSignatureFile builds signature.json content for a manifest.
func newSignatureFile(canonical []byte, files []File, signers []*Signer) (*SignatureFile, error) {
	sum := sha256.Sum256(canonical)
	sf := &SignatureFile{Format: SignatureFormat, ManifestSHA256: hexSum(sum[:]), Files: signedFiles(files)}
	for _, s := range signers {
		sig, err := s.Sign(canonical, sf.Files)
		if err != nil {
			return nil, err
		}
		sf.Signatures = append(sf.Signatures, sig)
	}
	return sf, nil
}

// ParseSignatureFile decodes signature.json strictly and checks its structure.
// It does not verify the signatures; see Pack.Verify.
func ParseSignatureFile(data []byte) (*SignatureFile, error) {
	if _, err := Canonicalize(data); err != nil {
		return nil, fmt.Errorf("%w: signature.json: %v", ErrBadSignature, err)
	}
	if err := CheckMemberNames(data, ""); err != nil {
		return nil, fmt.Errorf("%w: signature.json: %v", ErrBadSignature, err)
	}
	var sf SignatureFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sf); err != nil {
		return nil, fmt.Errorf("%w: signature.json: %v", ErrBadSignature, err)
	}
	if sf.Format != SignatureFormat {
		return nil, fmt.Errorf("%w: signature.json: mvpack_signature must be %d", ErrBadSignature, SignatureFormat)
	}
	if !reSHA256.MatchString(sf.ManifestSHA256) {
		return nil, fmt.Errorf("%w: signature.json: manifest_sha256 must be 64 lowercase hex digits", ErrBadSignature)
	}
	if len(sf.Signatures) == 0 || len(sf.Signatures) > 8 {
		return nil, fmt.Errorf("%w: signature.json: needs 1–8 signatures", ErrBadSignature)
	}
	for i, f := range sf.Files {
		if CheckPath(f.Path) != nil || !reSHA256.MatchString(f.SHA256) || f.Size < 0 {
			return nil, fmt.Errorf("%w: signature.json: files[%d] is malformed", ErrBadSignature, i)
		}
		if i > 0 && sf.Files[i-1].Path >= f.Path {
			return nil, fmt.Errorf("%w: signature.json: files must be sorted by path without duplicates", ErrBadSignature)
		}
	}
	for i, s := range sf.Signatures {
		// signed_at enters the signing payload as whole UTC seconds; any other spelling of the same instant
		// would let the stored value change without breaking the signature.
		if _, off := s.SignedAt.Zone(); off != 0 || s.SignedAt.Nanosecond() != 0 {
			return nil, fmt.Errorf("%w: signature.json: signatures[%d].signed_at must be whole seconds in UTC", ErrBadSignature, i)
		}
		if !reKeyID.MatchString(s.KeyID) || s.Alg != AlgEd25519 || s.SignedAt.IsZero() {
			return nil, fmt.Errorf("%w: signature.json: signatures[%d] is malformed", ErrBadSignature, i)
		}
		switch s.Signer {
		case SignerServer, SignerDeveloper, SignerWorld:
		default:
			return nil, fmt.Errorf("%w: signature.json: signatures[%d] has unknown signer %q", ErrBadSignature, i, s.Signer)
		}
		if pk, err := base64.StdEncoding.DecodeString(s.PublicKey); err != nil || len(pk) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%w: signature.json: signatures[%d].public_key is not a base64 ed25519 key", ErrBadSignature, i)
		}
		if sg, err := base64.StdEncoding.DecodeString(s.Sig); err != nil || len(sg) != ed25519.SignatureSize {
			return nil, fmt.Errorf("%w: signature.json: signatures[%d].sig is not a base64 ed25519 signature", ErrBadSignature, i)
		}
	}
	return &sf, nil
}

// KeyResolver looks up trusted public keys by key id. Implementations return
// ok=false for unknown or revoked keys.
type KeyResolver interface {
	PublicKey(keyID string) (pub ed25519.PublicKey, ok bool)
}

// SignerResolver is a KeyResolver that also knows which signer role each
// trusted key holds. When VerifyOptions.Keys implements it, a signature only
// counts as trusted if its self-declared "signer" equals the key's role, so a
// linked server's key cannot pass RequireSigner: SignerWorld by writing
// "signer": "world", and ParseLicenceToken only accepts World keys.
//
// Prefer RoleKeySet over KeySet whenever the trusted keys have different roles
// (for example World's keys together with the keys of linked servers).
type SignerResolver interface {
	KeyResolver
	// SignerRole returns the role of a trusted key (SignerServer, SignerDeveloper or SignerWorld).
	SignerRole(keyID string) (role string, ok bool)
}

// KeySet is a KeyResolver backed by a map from key id to public key. It does
// not bind keys to signer roles: every key in it is trusted for whatever role a
// signature declares. Use RoleKeySet when mixing keys of different roles.
type KeySet map[string]ed25519.PublicKey

// PublicKey implements KeyResolver.
func (k KeySet) PublicKey(keyID string) (ed25519.PublicKey, bool) {
	pub, ok := k[keyID]
	return pub, ok
}

// RoleKey is a trusted public key bound to one signer role.
type RoleKey struct {
	Public ed25519.PublicKey
	// Role is SignerServer, SignerDeveloper or SignerWorld.
	Role string
}

// RoleKeySet is a SignerResolver backed by a map from key id to a role-bound key.
type RoleKeySet map[string]RoleKey

// PublicKey implements KeyResolver.
func (k RoleKeySet) PublicKey(keyID string) (ed25519.PublicKey, bool) {
	rk, ok := k[keyID]
	return rk.Public, ok && len(rk.Public) == ed25519.PublicKeySize
}

// SignerRole implements SignerResolver.
func (k RoleKeySet) SignerRole(keyID string) (string, bool) {
	rk, ok := k[keyID]
	return rk.Role, ok
}

// Fingerprint returns the uppercase hex SHA-256 of a public key.
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// ShortFingerprint returns the display form of a key fingerprint, e.g. "7B1D…E0A4".
func ShortFingerprint(pub ed25519.PublicKey) string {
	f := Fingerprint(pub)
	return f[:4] + "…" + f[len(f)-4:]
}

// ParsePublicKey decodes a base64 (standard or URL, padded or not) ed25519 public key.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == ed25519.PublicKeySize {
			return ed25519.PublicKey(b), nil
		}
	}
	return nil, fmt.Errorf("mvpack: not a base64 ed25519 public key")
}

// verifySignature checks one signature against the payload. It returns whether
// the key is trusted by keys (nil keys: never trusted).
func verifySignature(s Signature, canonical []byte, files []SignedFile, keys KeyResolver) (trusted bool, err error) {
	pub, _ := base64.StdEncoding.DecodeString(s.PublicKey)
	sig, _ := base64.StdEncoding.DecodeString(s.Sig)
	payload := SigningPayload(s.KeyID, s.Signer, s.SignedAt, canonical, files)
	if !ed25519.Verify(ed25519.PublicKey(pub), payload, sig) {
		return false, fmt.Errorf("%w: signature by key %s does not verify", ErrBadSignature, s.KeyID)
	}
	if keys == nil {
		return false, nil
	}
	want, ok := keys.PublicKey(s.KeyID)
	if !ok {
		return false, nil
	}
	if !bytes.Equal(want, pub) {
		return false, fmt.Errorf("%w: key %s in the pack differs from the trusted key with that id", ErrUntrustedKey, s.KeyID)
	}
	if sr, ok := keys.(SignerResolver); ok {
		if role, known := sr.SignerRole(s.KeyID); !known || role != s.Signer {
			return false, fmt.Errorf("%w: key %s is trusted as %q but the signature claims %q", ErrUntrustedKey, s.KeyID, role, s.Signer)
		}
	}
	return true, nil
}
