package analytics

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
)

// SessionCookie is the first-party reading-session cookie of anonymous
// visitors. Its value is "<id>.<mac>": id is 24 random bytes (base64url, 32
// characters) and mac the first 16 bytes of HMAC-SHA256 over the id with a key
// derived from the server's master key (base64url, 22 characters). Only
// cookies the server minted are accepted, so a client cannot choose its own
// session to get around the new-session limit. The id is never stored: Record
// keeps only its salted daily hash.
const SessionCookie = "mv_rs"

// sessionCookiePurpose names the derived key that signs SessionCookie.
const sessionCookiePurpose = "analytics-session-cookie-v1"

const (
	sessionIDBytes  = 24
	sessionMACBytes = 16
)

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32}$`)

// sessions implements ReadingSessions.
type sessions struct {
	// key signs the cookie.
	key []byte
	// addrKey hashes client addresses for the limiter. It is random per
	// process and the limiter lives only in memory, so no address is kept.
	addrKey []byte
	// limiter limits how many sessions one client address may start.
	limiter *core.Limiter
}

var _ ReadingSessions = (*sessions)(nil)

func newSessions(key []byte) *sessions {
	addrKey := make([]byte, 32)
	_, _ = rand.Read(addrKey)
	return &sessions{key: key, addrKey: addrKey, limiter: core.NewLimiter(NewSessionRate, NewSessionBurst)}
}

// Session implements ReadingSessions.
func (s *sessions) Session(w http.ResponseWriter, r *http.Request) (string, bool) {
	if id, ok := s.existing(r); ok {
		return id, true
	}
	if ok, _ := s.allowNew(r); !ok {
		return "", false
	}
	return s.mint(w, r), true
}

// existing returns the session ID of a valid server-minted cookie.
func (s *sessions) existing(r *http.Request) (string, bool) {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return "", false
	}
	id, mac, ok := strings.Cut(c.Value, ".")
	if !ok || !sessionIDPattern.MatchString(id) {
		return "", false
	}
	got, err := base64.RawURLEncoding.DecodeString(mac)
	if err != nil || !hmac.Equal(got, s.mac(id)) {
		return "", false
	}
	return id, true
}

// allowNew takes one new-session token for the request's client address.
func (s *sessions) allowNew(r *http.Request) (bool, time.Duration) {
	m := hmac.New(sha256.New, s.addrKey)
	m.Write([]byte(core.ClientIP(r)))
	return s.limiter.Allow("a:" + hex.EncodeToString(m.Sum(nil)))
}

// mint starts a new session and sets its cookie (HttpOnly, SameSite=Lax,
// Secure over HTTPS, no expiry, so it ends with the browser session).
func (s *sessions) mint(w http.ResponseWriter, r *http.Request) string {
	b := make([]byte, sessionIDBytes)
	_, _ = rand.Read(b)
	id := base64.RawURLEncoding.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: id + "." + base64.RawURLEncoding.EncodeToString(s.mac(id)),
		Path: "/", HttpOnly: true, Secure: httpx.IsHTTPS(r), SameSite: http.SameSiteLaxMode})
	return id
}

func (s *sessions) mac(id string) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(SessionCookie + "|" + id))
	return m.Sum(nil)[:sessionMACBytes]
}
