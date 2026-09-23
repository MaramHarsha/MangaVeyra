// SPDX-License-Identifier: MIT
// Copyright (c) 2026 MangaVeyra contributors

package mvpack

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// licenceTokenPrefix starts every licence token and the bytes its signature covers.
const licenceTokenPrefix = "mvlt1."

// ErrLicenceToken reports a malformed, badly signed or untrusted licence token.
var ErrLicenceToken = errors.New("mvpack: invalid licence token")

// LicenceClaims is the payload of a World licence token: proof that an account
// may use an item. It never contains a price.
type LicenceClaims struct {
	Version   int    `json:"v"`
	KeyID     string `json:"kid"`
	ItemID    string `json:"item_id"`
	AccountID string `json:"account_id"`
	Handle    string `json:"handle"`
	// ItemVersion is the version the token was issued for; tokens stay valid for later versions.
	ItemVersion string `json:"version"`
	Licence     string `json:"licence"`
	// Acquisition is "free", "paid" or "creator".
	Acquisition string `json:"acquisition"`
	IssuedAt    int64  `json:"iat"`
}

// Issued returns IssuedAt as a time.
func (c *LicenceClaims) Issued() time.Time { return time.Unix(c.IssuedAt, 0).UTC() }

// SignLicenceToken encodes and signs claims as "mvlt1.<payload>.<signature>",
// both parts unpadded base64url; the ed25519 signature covers the ASCII bytes
// "mvlt1.<payload>". claims.KeyID and claims.Version are set from the arguments.
func SignLicenceToken(claims LicenceClaims, keyID string, key ed25519.PrivateKey) (string, error) {
	if len(key) != ed25519.PrivateKeySize || !reKeyID.MatchString(keyID) {
		return "", fmt.Errorf("%w: bad signing key", ErrLicenceToken)
	}
	claims.Version, claims.KeyID = 1, keyID
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signed := licenceTokenPrefix + base64.RawURLEncoding.EncodeToString(payload)
	sig := ed25519.Sign(key, []byte(signed))
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// ParseLicenceToken verifies a licence token against the trusted World keys
// and returns its claims.
func ParseLicenceToken(token string, keys KeyResolver) (*LicenceClaims, error) {
	rest, ok := strings.CutPrefix(token, licenceTokenPrefix)
	if !ok || keys == nil {
		return nil, fmt.Errorf("%w: wrong prefix", ErrLicenceToken)
	}
	payloadB64, sigB64, ok := strings.Cut(rest, ".")
	if !ok {
		return nil, fmt.Errorf("%w: missing signature", ErrLicenceToken)
	}
	payload, err1 := base64.RawURLEncoding.DecodeString(payloadB64)
	sig, err2 := base64.RawURLEncoding.DecodeString(sigB64)
	if err1 != nil || err2 != nil || len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("%w: bad encoding", ErrLicenceToken)
	}
	if _, err := Canonicalize(payload); err != nil {
		return nil, fmt.Errorf("%w: bad payload", ErrLicenceToken)
	}
	if err := CheckMemberNames(payload, ""); err != nil {
		return nil, fmt.Errorf("%w: bad payload", ErrLicenceToken)
	}
	var c LicenceClaims
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil || c.Version != 1 {
		return nil, fmt.Errorf("%w: bad payload", ErrLicenceToken)
	}
	pub, ok := keys.PublicKey(c.KeyID)
	if !ok {
		return nil, fmt.Errorf("%w: unknown key %q", ErrLicenceToken, c.KeyID)
	}
	if sr, isRoles := keys.(SignerResolver); isRoles {
		if role, _ := sr.SignerRole(c.KeyID); role != SignerWorld {
			return nil, fmt.Errorf("%w: key %q is not a World key", ErrLicenceToken, c.KeyID)
		}
	}
	if !ed25519.Verify(pub, []byte(licenceTokenPrefix+payloadB64), sig) {
		return nil, fmt.Errorf("%w: signature does not verify", ErrLicenceToken)
	}
	return &c, nil
}
