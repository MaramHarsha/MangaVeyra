// Package httpx is the leaf HTTP toolkit under internal/core: the Router,
// middleware type, request identity, JSON and error envelope helpers,
// pagination, rate limiting and the render hooks the web layer installs.
//
// Feature modules use these through the re-exports in package core
// (core.Router, core.UserFrom, core.JSON, ...). Foundation packages that core
// itself depends on (auth, jobs, ws, notify) import httpx directly, which
// keeps the import graph acyclic.
package httpx

import (
	"context"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Site roles.
const (
	RoleAdmin   = "admin"
	RoleCreator = "creator"
	RoleReader  = "reader"
)

// Token kinds.
const (
	TokenAgent  = "agent"  // MCP / AI agents acting for a user (mva_)
	TokenWorker = "worker" // AI workers pulling jobs (mvw_)
	TokenAPI    = "api"    // personal API tokens (mvk_)
)

// User is the signed-in account attached to a request. It never carries secrets.
type User struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	Email       string     `json:"email,omitempty"`
	DisplayName string     `json:"display_name"`
	Role        string     `json:"role"`
	Status      string     `json:"status"`
	AvatarKey   string     `json:"avatar_key,omitempty"`
	Bio         string     `json:"bio,omitempty"`
	Locale      string     `json:"locale,omitempty"`
	Timezone    string     `json:"timezone,omitempty"`
	TOTPEnabled bool       `json:"totp_enabled"`
	XP          int64      `json:"xp"`
	Level       int        `json:"level"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

// IsAdmin reports whether the user is a server admin.
func (u *User) IsAdmin() bool { return u != nil && u.Role == RoleAdmin }

// CanCreate reports whether the user may create projects (creators and admins).
func (u *User) CanCreate() bool { return u != nil && (u.Role == RoleCreator || u.Role == RoleAdmin) }

// Name returns the display name, falling back to the username.
func (u *User) Name() string {
	if u == nil {
		return ""
	}
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}

// Token is the bearer token that authenticated a request, if any.
type Token struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	UserID string `json:"user_id,omitempty"`
	// Scopes lists granted scopes, e.g. "projects:read", "pages:write", "generate". "*" grants all.
	Scopes []string `json:"scopes"`
	// ProjectIDs restricts the token to these projects (empty = all projects of the user).
	ProjectIDs []string `json:"project_ids"`
	// ApproveOverUSD requires human approval for actions estimated above this spend (nil = never).
	ApproveOverUSD *float64 `json:"approve_over_usd,omitempty"`
}

// HasScope reports whether the token grants scope (or "*").
func (t *Token) HasScope(scope string) bool {
	if t == nil {
		return false
	}
	return slices.Contains(t.Scopes, "*") || slices.Contains(t.Scopes, scope)
}

// AllowsProject reports whether the token may act on projectID.
func (t *Token) AllowsProject(projectID string) bool {
	if t == nil {
		return false
	}
	return len(t.ProjectIDs) == 0 || slices.Contains(t.ProjectIDs, projectID)
}

// Identity is who is making the request.
type Identity struct {
	// User is the account (for sessions and agent/api tokens); nil for anonymous and worker requests.
	User *User
	// Token is set when the request authenticated with a bearer token.
	Token *Token
	// SessionID is set when the request authenticated with a session cookie.
	SessionID string
}

// IsAgent reports whether the request is made by an agent token rather than a human.
func (id Identity) IsAgent() bool { return id.Token != nil && id.Token.Kind == TokenAgent }

// ActorKind returns "user", "agent", "worker" or "" for anonymous requests (audit log / version history).
func (id Identity) ActorKind() string {
	switch {
	case id.Token != nil && id.Token.Kind == TokenAgent:
		return "agent"
	case id.Token != nil && id.Token.Kind == TokenWorker:
		return "worker"
	case id.User != nil:
		return "user"
	}
	return ""
}

type ctxKey int

const (
	identityKey ctxKey = iota
	requestIDKey
	clientIPKey
	themeKey
	valuesKey
)

// WithIdentity attaches id to ctx.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey, id)
}

// IdentityFrom returns the request identity (zero value when anonymous).
func IdentityFrom(ctx context.Context) Identity {
	id, _ := ctx.Value(identityKey).(Identity)
	return id
}

// UserFrom returns the signed-in user, or nil.
func UserFrom(ctx context.Context) *User { return IdentityFrom(ctx).User }

// TokenFrom returns the bearer token of the request, or nil.
func TokenFrom(ctx context.Context) *Token { return IdentityFrom(ctx).Token }

// WithRequestID attaches a request id.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFrom returns the request id, or "".
func RequestIDFrom(ctx context.Context) string {
	s, _ := ctx.Value(requestIDKey).(string)
	return s
}

// WithClientIP attaches the resolved client IP.
func WithClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, clientIPKey, ip)
}

// ClientIP returns the client IP resolved by the RealIP middleware, or the peer address.
func ClientIP(r *http.Request) string {
	if ip, ok := r.Context().Value(clientIPKey).(string); ok && ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ClientIPFrom returns the client IP stored in ctx by RealIP (for services that only get a context).
func ClientIPFrom(ctx context.Context) string {
	s, _ := ctx.Value(clientIPKey).(string)
	return s
}

// WithTheme attaches the resolved theme ("dark", "light" or "system").
func WithTheme(ctx context.Context, theme string) context.Context {
	return context.WithValue(ctx, themeKey, theme)
}

// ThemeFrom returns the theme resolved for the request (default "dark").
func ThemeFrom(ctx context.Context) string {
	if s, ok := ctx.Value(themeKey).(string); ok && s != "" {
		return s
	}
	return "dark"
}

// RequestValues is a per-request scratch map for lazily computed values
// (e.g. the user's preferences) so they are loaded at most once per request.
type RequestValues struct {
	m map[any]any
}

// WithValues attaches an empty RequestValues to ctx.
func WithValues(ctx context.Context) context.Context {
	return context.WithValue(ctx, valuesKey, &RequestValues{m: map[any]any{}})
}

// Memo returns the value cached under key for this request, computing it with fn on first use.
// Without WithValues in the chain it just calls fn.
func Memo[T any](ctx context.Context, key any, fn func() T) T {
	rv, _ := ctx.Value(valuesKey).(*RequestValues)
	if rv == nil {
		return fn()
	}
	if v, ok := rv.m[key]; ok {
		return v.(T)
	}
	v := fn()
	rv.m[key] = v
	return v
}

// IsHTMX reports whether the request was made by HTMX.
func IsHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

// WantsJSON reports whether the client expects JSON (API paths, JSON Accept or JSON body).
func WantsJSON(r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/mcp") {
		return true
	}
	if IsHTMX(r) {
		return false
	}
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/json") && !strings.Contains(accept, "text/html") {
		return true
	}
	return false
}
