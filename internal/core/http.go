package core

import (
	"context"
	"net/http"

	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

// Re-exports of the HTTP toolkit so feature modules only import core.
type (
	// Router registers routes; see httpx.Router.
	Router = httpx.Router
	// Middleware wraps a handler.
	Middleware = httpx.Middleware
	// HandlerFunc is a handler returning an error.
	HandlerFunc = httpx.HandlerFunc
	// APIError is an error with HTTP status and code.
	APIError = httpx.APIError
	// User is the signed-in account.
	User = httpx.User
	// Token is the bearer token of a request.
	Token = httpx.Token
	// Identity is who makes a request.
	Identity = httpx.Identity
	// Component renders HTML (templ.Component satisfies it).
	Component = httpx.Component
	// Hooks are the web layer's render hooks.
	Hooks = httpx.Hooks
	// PageRequest is a parsed pagination query.
	PageRequest = httpx.PageRequest
	// Limiter is a token-bucket rate limiter.
	Limiter = httpx.Limiter
)

// PageResult is the standard list response.
type PageResult[T any] = httpx.PageResult[T]

// Site roles.
const (
	RoleAdmin   = httpx.RoleAdmin
	RoleCreator = httpx.RoleCreator
	RoleReader  = httpx.RoleReader
)

// NewRouter creates an empty router.
func NewRouter() *Router { return httpx.NewRouter() }

// Chain composes middlewares (first = outermost).
func Chain(mws ...Middleware) Middleware { return httpx.Chain(mws...) }

// RequireUser rejects anonymous requests (401 for APIs, redirect to /login for pages).
func RequireUser(next http.Handler) http.Handler { return httpx.RequireUser(next) }

// RequireRole allows users with one of roles (admins always pass).
func RequireRole(roles ...string) Middleware { return httpx.RequireRole(roles...) }

// RequireHuman rejects agent tokens (publish, delete, share and unlock need a human).
func RequireHuman(next http.Handler) http.Handler { return httpx.RequireHuman(next) }

// RequireSession allows only signed-in browser sessions (no bearer tokens).
func RequireSession(next http.Handler) http.Handler { return httpx.RequireSession(next) }

// RequireToken allows only bearer tokens of kind granting all scopes.
func RequireToken(kind string, scopes ...string) Middleware {
	return httpx.RequireToken(kind, scopes...)
}

// RequireScope requires scope from token-authenticated requests (sessions pass).
func RequireScope(scope string) Middleware { return httpx.RequireScope(scope) }

// UserFrom returns the signed-in user or nil.
func UserFrom(ctx context.Context) *User { return httpx.UserFrom(ctx) }

// IdentityFrom returns the request identity.
func IdentityFrom(ctx context.Context) Identity { return httpx.IdentityFrom(ctx) }

// TokenFrom returns the bearer token or nil.
func TokenFrom(ctx context.Context) *Token { return httpx.TokenFrom(ctx) }

// RequestIDFrom returns the request id.
func RequestIDFrom(ctx context.Context) string { return httpx.RequestIDFrom(ctx) }

// LocaleFrom returns the negotiated interface language.
func LocaleFrom(ctx context.Context) string { return i18n.LocaleFrom(ctx) }

// ThemeFrom returns the resolved theme: "dark" (Gate night), "light" (Paper) or "system".
func ThemeFrom(ctx context.Context) string { return httpx.ThemeFrom(ctx) }

type prefsKey struct{}

// PrefsFrom returns the signed-in user's preferences (defaults for anonymous
// visitors), loaded at most once per request.
func PrefsFrom(ctx context.Context) settings.UserPrefs {
	if f, ok := ctx.Value(prefsKey{}).(func() settings.UserPrefs); ok {
		return f()
	}
	return settings.DefaultUserPrefs()
}

// ClientIP returns the resolved client IP.
func ClientIP(r *http.Request) string { return httpx.ClientIP(r) }

// IsHTMX reports whether the request came from HTMX.
func IsHTMX(r *http.Request) bool { return httpx.IsHTMX(r) }

// WantsJSON reports whether the client expects JSON.
func WantsJSON(r *http.Request) bool { return httpx.WantsJSON(r) }

// JSON writes v as JSON.
func JSON(w http.ResponseWriter, status int, v any) { httpx.JSON(w, status, v) }

// DecodeJSON decodes a JSON request body (1 MiB limit).
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	return httpx.DecodeJSON(w, r, dst)
}

// WriteError renders an error as JSON envelope, HTML error page or text.
func WriteError(w http.ResponseWriter, r *http.Request, err error) { httpx.WriteError(w, r, err) }

// Render writes an HTML component (fragment for HTMX, wrapped in the layout otherwise).
func Render(w http.ResponseWriter, r *http.Request, status int, c Component) error {
	return httpx.Render(w, r, status, c)
}

// Redirect redirects (HX-Redirect for HTMX requests).
func Redirect(w http.ResponseWriter, r *http.Request, target string) { httpx.Redirect(w, r, target) }

// SafeRedirectTarget returns next if it is a local path, else fallback.
func SafeRedirectTarget(next, fallback string) string {
	return httpx.SafeRedirectTarget(next, fallback)
}

// SetHooks installs the web layer's hooks (HTML error page, layout, login URL).
func SetHooks(h Hooks) { httpx.SetHooks(h) }

// ParsePage parses ?limit=&cursor=&page=.
func ParsePage(r *http.Request, def, max int) PageRequest { return httpx.ParsePage(r, def, max) }

// Paginate trims a limit+1 result set and sets the next cursor.
func Paginate[T any](items []T, limit int, cursorOf func(T) string) PageResult[T] {
	return httpx.Paginate(items, limit, cursorOf)
}

// Error constructors.
var (
	ErrBadRequest   = httpx.ErrBadRequest
	ErrUnauthorized = httpx.ErrUnauthorized
	ErrForbidden    = httpx.ErrForbidden
	ErrNotFound     = httpx.ErrNotFound
	ErrConflict     = httpx.ErrConflict
	ErrLocked       = httpx.ErrLocked
	ErrValidation   = httpx.ErrValidation
	ErrInternal     = httpx.ErrInternal
	NewError        = httpx.NewError
)

// NewLimiter creates a rate limiter (rate events per second, burst).
func NewLimiter(rate float64, burst int) *Limiter { return httpx.NewLimiter(rate, burst) }
