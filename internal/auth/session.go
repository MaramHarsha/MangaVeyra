package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// Session cookie and lifetimes.
const (
	SessionCookie = "mv_session"
	// RememberFor is the sliding lifetime of "stay signed in" sessions.
	RememberFor = 30 * 24 * time.Hour
	// SessionFor is the sliding idle lifetime of ordinary sessions (the cookie also ends with the browser).
	SessionFor = 12 * time.Hour
	// touchEvery throttles last-seen updates.
	touchEvery = 5 * time.Minute
)

// Session is a signed-in browser.
type Session struct {
	ID         string    `db:"id" json:"id"`
	UserID     string    `db:"user_id" json:"-"`
	Remember   bool      `db:"remember" json:"remember"`
	IP         string    `db:"ip" json:"ip"`
	UserAgent  string    `db:"user_agent" json:"user_agent"`
	CreatedAt  time.Time `db:"created_at" json:"created_at"`
	LastSeenAt time.Time `db:"last_seen_at" json:"last_seen_at"`
	ExpiresAt  time.Time `db:"expires_at" json:"expires_at"`
	// Current marks the session making the request (set by ListSessions).
	Current bool `db:"-" json:"current"`
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// CreateSession signs userID in and returns the raw session token for the cookie.
func (s *Service) CreateSession(ctx context.Context, userID string, remember bool, ip, userAgent string) (string, *Session, error) {
	token := randomToken(32)
	now := s.now()
	life := SessionFor
	if remember {
		life = RememberFor
	}
	if len(userAgent) > 300 {
		userAgent = userAgent[:300]
	}
	sess := &Session{ID: db.NewID(), UserID: userID, Remember: remember, IP: ip, UserAgent: userAgent, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(life)}
	_, err := s.db.Exec(ctx, `INSERT INTO sessions (id, user_id, token_hash, remember, ip, user_agent, created_at, last_seen_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, sess.ID, userID, hashToken(token), remember, ip, userAgent, now, now, sess.ExpiresAt)
	if err != nil {
		return "", nil, err
	}
	return token, sess, nil
}

// ResolveSession returns the user and session for a raw session token.
// Expired or unknown tokens return ErrTokenInvalid.
func (s *Service) ResolveSession(ctx context.Context, token string) (*User, *Session, error) {
	if token == "" || len(token) > 128 {
		return nil, nil, ErrTokenInvalid
	}
	var sess Session
	now := s.now()
	err := s.db.Get(ctx, &sess, `SELECT id, user_id, remember, ip, user_agent, created_at, last_seen_at, expires_at
		FROM sessions WHERE token_hash = ? AND expires_at > ?`, hashToken(token), now)
	if err != nil {
		if db.IsNotFound(err) {
			return nil, nil, ErrTokenInvalid
		}
		return nil, nil, err
	}
	r, err := s.row(ctx, s.db, `id = ?`, sess.UserID)
	if err != nil {
		return nil, nil, ErrTokenInvalid
	}
	if r.Status == StatusSuspended {
		return nil, nil, ErrTokenInvalid
	}
	if now.Sub(sess.LastSeenAt) > touchEvery {
		life := SessionFor
		if sess.Remember {
			life = RememberFor
		}
		sess.LastSeenAt, sess.ExpiresAt = now, now.Add(life)
		if _, err := s.db.Exec(ctx, `UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE id = ?`, now, sess.ExpiresAt, sess.ID); err != nil {
			s.log.WarnContext(ctx, "auth: touch session", "err", err)
		}
	}
	return r.user(), &sess, nil
}

// Revalidate re-checks a request identity that was resolved earlier (a
// long-lived WebSocket, for example): the session must still exist and not be
// expired, a token must not be revoked or expired, and the account must still
// be active. It returns the identity with a fresh copy of the user (role and
// status may have changed) or ErrTokenInvalid.
func (s *Service) Revalidate(ctx context.Context, id httpx.Identity) (httpx.Identity, error) {
	now := s.now()
	switch {
	case id.Token != nil:
		if id.Token.ID == EnvWorkerTokenID {
			return id, nil
		}
		var t TokenInfo
		if err := s.db.Get(ctx, &t, `SELECT `+tokenCols+` FROM api_tokens WHERE id = ?`, id.Token.ID); err != nil {
			if db.IsNotFound(err) {
				return id, ErrTokenInvalid
			}
			return id, err
		}
		if t.RevokedAt != nil || (t.ExpiresAt != nil && !t.ExpiresAt.After(now)) {
			return id, ErrTokenInvalid
		}
		if id.User == nil {
			return id, nil
		}
	case id.SessionID != "":
		var n int
		if err := s.db.Get(ctx, &n, `SELECT COUNT(*) FROM sessions WHERE id = ? AND expires_at > ?`, id.SessionID, now); err != nil {
			return id, err
		}
		if n == 0 {
			return id, ErrTokenInvalid
		}
	default:
		return id, ErrTokenInvalid
	}
	if id.User == nil {
		return id, ErrTokenInvalid
	}
	u, err := s.UserByID(ctx, id.User.ID)
	if err != nil {
		if err == ErrUserNotFound {
			return id, ErrTokenInvalid
		}
		return id, err
	}
	if u.Status != StatusActive {
		return id, ErrTokenInvalid
	}
	id.User = u
	return id, nil
}

// ListSessions lists a user's active sessions, newest first; currentID is flagged.
func (s *Service) ListSessions(ctx context.Context, userID, currentID string) ([]Session, error) {
	var out []Session
	err := s.db.Select(ctx, &out, `SELECT id, user_id, remember, ip, user_agent, created_at, last_seen_at, expires_at
		FROM sessions WHERE user_id = ? AND expires_at > ? ORDER BY last_seen_at DESC`, userID, s.now())
	for i := range out {
		out[i].Current = out[i].ID == currentID
	}
	return out, err
}

// RevokeSession signs one session of userID out.
func (s *Service) RevokeSession(ctx context.Context, userID, sessionID string) error {
	res, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE id = ? AND user_id = ?`, sessionID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return httpx.ErrNotFound()
	}
	return nil
}

// RevokeOtherSessions signs userID out everywhere except keepID.
func (s *Service) RevokeOtherSessions(ctx context.Context, userID, keepID string) (int64, error) {
	res, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, keepID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CleanupExpired deletes expired sessions and used/expired reset tokens.
func (s *Service) CleanupExpired(ctx context.Context) error {
	now := s.now()
	if _, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM password_resets WHERE expires_at <= ? OR used_at IS NOT NULL`, now.Add(-24*time.Hour))
	return err
}

// ---- cookies ---------------------------------------------------------------

// SetSessionCookie writes the session cookie (HttpOnly, SameSite=Lax, Secure on HTTPS).
func (s *Service) SetSessionCookie(w http.ResponseWriter, r *http.Request, token string, remember bool) {
	c := &http.Cookie{
		Name: SessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: s.opts.SecureCookies || httpx.IsHTTPS(r),
	}
	if remember {
		c.MaxAge = int(RememberFor.Seconds())
	}
	http.SetCookie(w, c)
}

// ClearSessionCookie deletes the session cookie.
func (s *Service) ClearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.opts.SecureCookies || httpx.IsHTTPS(r)})
}

// ---- login -----------------------------------------------------------------

// LoginInput is a login attempt.
type LoginInput struct {
	Identifier string // email or username
	Password   string
	TOTPCode   string // 6-digit code or a recovery code
	Remember   bool
	IP         string
	UserAgent  string
}

// LoginResult is a successful login.
type LoginResult struct {
	User    *User
	Token   string
	Session *Session
}

// Login verifies credentials (and the second factor when enabled) and creates a session.
// It enforces per-IP and per-account rate limits and runs in roughly constant
// time whether or not the account exists.
func (s *Service) Login(ctx context.Context, in LoginInput) (*LoginResult, error) {
	ident := strings.ToLower(strings.TrimSpace(in.Identifier))
	if ok, wait := s.loginIP.Allow("ip:" + in.IP); !ok {
		return nil, httpx.ErrRateLimited(wait)
	}
	if ok, wait := s.loginUser.Allow("id:" + ident); !ok {
		return nil, httpx.ErrRateLimited(wait)
	}
	r, err := s.rowByLogin(ctx, ident)
	if err != nil {
		VerifyPassword(in.Password, s.dummy)
		if err == ErrUserNotFound {
			s.Audit(ctx, AuditEntry{Action: "auth.login_failed", Data: map[string]any{"identifier": ident, "reason": "unknown"}})
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	if r.PasswordHash == "" || !VerifyPassword(in.Password, r.PasswordHash) {
		s.Audit(ctx, AuditEntry{ActorID: r.ID, Action: "auth.login_failed", TargetKind: "user", TargetID: r.ID, Data: map[string]any{"reason": "password"}})
		return nil, ErrInvalidCredentials
	}
	switch r.Status {
	case StatusSuspended:
		return nil, ErrSuspended
	case StatusPending:
		return nil, ErrPending
	}
	if r.TOTPEnabled {
		if strings.TrimSpace(in.TOTPCode) == "" {
			return nil, ErrTOTPRequired
		}
		if err := s.verifySecondFactor(ctx, r, in.TOTPCode); err != nil {
			s.Audit(ctx, AuditEntry{ActorID: r.ID, Action: "auth.login_failed", TargetKind: "user", TargetID: r.ID, Data: map[string]any{"reason": "totp"}})
			return nil, err
		}
	}
	s.loginUser.Reset("id:" + ident)
	if NeedsRehash(r.PasswordHash, s.params) {
		if h, err := HashPassword(in.Password, s.params); err == nil {
			_, _ = s.db.Exec(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, h, r.ID)
		}
	}
	token, sess, err := s.CreateSession(ctx, r.ID, in.Remember, in.IP, in.UserAgent)
	if err != nil {
		return nil, err
	}
	now := s.now()
	if _, err := s.db.Exec(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`, now, r.ID); err != nil {
		s.log.WarnContext(ctx, "auth: update last login", "err", err)
	}
	u := r.user()
	u.LastLoginAt = &now
	s.Audit(ctx, AuditEntry{ActorID: u.ID, Action: "auth.login", TargetKind: "user", TargetID: u.ID})
	return &LoginResult{User: u, Token: token, Session: sess}, nil
}

// Logout deletes the session behind token.
func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}

// ---- identity middleware ---------------------------------------------------

// Identify resolves the caller from "Authorization: Bearer <token>" or the
// session cookie and stores it with httpx.WithIdentity. An invalid bearer
// token is rejected with 401 (no fallback to the cookie); an invalid cookie
// is cleared and the request continues anonymously.
func (s *Service) Identify(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if h := r.Header.Get("Authorization"); h != "" {
			scheme, raw, _ := strings.Cut(h, " ")
			if !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(raw) == "" {
				httpx.WriteError(w, r, ErrTokenInvalid)
				return
			}
			tok, u, err := s.VerifyToken(ctx, strings.TrimSpace(raw), httpx.ClientIP(r))
			if err != nil {
				w.Header().Set("WWW-Authenticate", `Bearer realm="mangaveyra", error="invalid_token"`)
				httpx.WriteError(w, r, ErrTokenInvalid)
				return
			}
			next.ServeHTTP(w, r.WithContext(httpx.WithIdentity(ctx, httpx.Identity{User: u, Token: tok})))
			return
		}
		if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
			u, sess, err := s.ResolveSession(ctx, c.Value)
			if err == nil {
				ctx = httpx.WithIdentity(ctx, httpx.Identity{User: u, SessionID: sess.ID})
				r = r.WithContext(ctx)
			} else if err == ErrTokenInvalid {
				s.ClearSessionCookie(w, r)
			} else {
				s.log.ErrorContext(ctx, "auth: resolve session", "err", err)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// PeekIsAdmin reports whether the request carries a valid admin session or
// admin-owned token. It is used by gates that run before Identify.
func (s *Service) PeekIsAdmin(r *http.Request) bool {
	if u := httpx.UserFrom(r.Context()); u != nil {
		return u.IsAdmin()
	}
	if h := r.Header.Get("Authorization"); h != "" {
		_, raw, _ := strings.Cut(h, " ")
		if _, u, err := s.VerifyToken(r.Context(), strings.TrimSpace(raw), ""); err == nil && u != nil {
			return u.IsAdmin()
		}
		return false
	}
	if c, err := r.Cookie(SessionCookie); err == nil {
		if u, _, err := s.ResolveSession(r.Context(), c.Value); err == nil {
			return u.IsAdmin()
		}
	}
	return false
}
