package stub

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/MaramHarsha/MangaVeyra/sdk/mvpack"
)

type serverKey struct {
	ID         string
	AccountID  string
	Name       string
	AppVersion string
	Public     ed25519.PublicKey
	CreatedAt  time.Time
	LastUsedAt time.Time
	RevokedAt  time.Time
}

type serverKeyJSON struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	PublicKey   string  `json:"public_key"`
	Fingerprint string  `json:"fingerprint"`
	AppVersion  string  `json:"app_version,omitempty"`
	CreatedAt   string  `json:"created_at"`
	LastUsedAt  *string `json:"last_used_at"`
	RevokedAt   *string `json:"revoked_at"`
}

func (k *serverKey) view() serverKeyJSON {
	return serverKeyJSON{
		ID: k.ID, Name: k.Name, PublicKey: base64.StdEncoding.EncodeToString(k.Public),
		Fingerprint: mvpack.Fingerprint(k.Public), AppVersion: k.AppVersion, CreatedAt: ts(k.CreatedAt),
		LastUsedAt: tsPtr(k.LastUsedAt), RevokedAt: tsPtr(k.RevokedAt),
	}
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	base := s.publicURL
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"api_version":        "1",
		"name":               "MangaVeyra World — development stub (not the real platform)",
		"stub":               true,
		"terms_url":          base + "/_stub",
		"content_policy_url": base + "/_stub",
		"max_upload_bytes":   s.opts.MaxUpload,
	})
}

func (s *Server) handleKeys(w http.ResponseWriter, r *http.Request) {
	key := func(id string, k ed25519.PrivateKey, use string) map[string]any {
		return map[string]any{"key_id": id, "alg": "ed25519", "use": []string{use}, "not_after": nil,
			"public_key": base64.StdEncoding.EncodeToString(k.Public().(ed25519.PublicKey))}
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": []any{
		key(s.packKeyID, s.packKey, "pack-signing"),
		key(s.licenceKeyID, s.licenceKey, "licence-tokens"),
	}})
}

func (s *Server) handleListServers(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.authorize(w, r, "servers")
	if !ok {
		return
	}
	keys := s.accountKeys(a.ID)
	items := make([]serverKeyJSON, 0, len(keys))
	for _, k := range keys {
		items = append(items, k.view())
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// accountKeys returns an account's keys, newest first; s.mu must be held.
func (s *Server) accountKeys(accountID string) []*serverKey {
	var out []*serverKey
	for _, k := range s.servers {
		if k.AccountID == accountID {
			out = append(out, k)
		}
	}
	slices.SortFunc(out, func(a, b *serverKey) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

type serverKeyCreate struct {
	Name       string `json:"name"`
	PublicKey  string `json:"public_key"`
	AppVersion string `json:"app_version"`
}

func (s *Server) handleRegisterServer(w http.ResponseWriter, r *http.Request) {
	var req serverKeyCreate
	if !decodeStrict(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.authorize(w, r, "servers")
	if !ok {
		return
	}
	var details []errorDetail
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 100 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		details = append(details, errorDetail{Field: "name", Code: "name_invalid", Message: "Give the server's host name (1–100 characters)."})
	}
	pub, err := mvpack.ParsePublicKey(req.PublicKey)
	if err != nil {
		details = append(details, errorDetail{Field: "public_key", Code: "public_key_invalid", Message: "Send the base64 of a 32-byte ed25519 public key."})
	}
	if len(req.AppVersion) > 40 {
		details = append(details, errorDetail{Field: "app_version", Code: "app_version_invalid", Message: "At most 40 characters."})
	}
	if len(details) > 0 {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", details[0].Message, details...)
		return
	}
	for _, k := range s.servers {
		if !bytes.Equal(k.Public, pub) {
			continue
		}
		switch {
		case k.AccountID != a.ID:
			writeError(w, http.StatusConflict, "key_in_use", "This public key is registered by another World account.")
		case !k.RevokedAt.IsZero():
			writeError(w, http.StatusConflict, "key_revoked", "This key was revoked. Generate a new key pair to link again.")
		default:
			writeJSON(w, http.StatusOK, k.view())
		}
		return
	}
	k := s.addServerKey(a.ID, name, req.AppVersion, pub, s.opts.Now())
	w.Header().Set("Location", "/v1/servers/"+k.ID)
	writeJSON(w, http.StatusCreated, k.view())
}

// addServerKey registers a key; s.mu must be held.
func (s *Server) addServerKey(accountID, name, appVersion string, pub ed25519.PublicKey, now time.Time) *serverKey {
	k := &serverKey{
		ID:        "srv_" + strings.ToLower(mvpack.Fingerprint(pub)[:16]),
		AccountID: accountID, Name: name, AppVersion: appVersion, Public: pub, CreatedAt: now,
	}
	s.servers[k.ID] = k
	return k
}

func (s *Server) handleRevokeServer(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.authorize(w, r, "servers")
	if !ok {
		return
	}
	k, found := s.servers[r.PathValue("id")]
	if !found || k.AccountID != a.ID {
		writeError(w, http.StatusNotFound, "not_found", "No such server key on this account.")
		return
	}
	if k.RevokedAt.IsZero() {
		k.RevokedAt = s.opts.Now()
	}
	w.WriteHeader(http.StatusNoContent)
}

// trustedKeys returns the account's non-revoked keys as a resolver bound to
// the server signer role, so a registered server key cannot pass itself off as
// a World (or developer) signature; s.mu must be held.
func (s *Server) trustedKeys(accountID string) mvpack.RoleKeySet {
	ks := mvpack.RoleKeySet{}
	for _, k := range s.servers {
		if k.AccountID == accountID && k.RevokedAt.IsZero() {
			ks[k.ID] = mvpack.RoleKey{Public: k.Public, Role: mvpack.SignerServer}
		}
	}
	return ks
}
