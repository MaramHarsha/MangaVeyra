package core

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/config"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

// Cookie names set by the web layer for anonymous visitors.
const (
	LangCookie  = "mv_lang"
	ThemeCookie = "mv_theme"
)

// Stack returns the global middleware chain in contract order:
// request id → real IP → recover → access log → security headers/CSP →
// maintenance gate → setup gate → session/user → locale/theme/prefs → CSRF →
// rate limit → handler.
func Stack(d *Deps) Middleware {
	var trusted []*net.IPNet
	for _, p := range d.Cfg.TrustedProxies {
		if n, err := config.ParseIPOrCIDR(p); err == nil {
			trusted = append(trusted, n)
		}
	}
	return Chain(
		RequestID,
		httpx.RealIP(trusted),
		Recover(d.Log),
		AccessLog(d.Log),
		SecurityHeaders(d.Cfg.PublicURL),
		MaintenanceGate(d),
		SetupGate(d),
		d.Auth.Identify,
		Localize(d),
		CSRF(d.Cfg.PublicURL),
		RateLimits(),
	)
}

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)

// RequestID assigns a request id (reusing a well-formed incoming X-Request-Id) and echoes it.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if !requestIDPattern.MatchString(id) {
			id = db.NewID()
		}
		w.Header().Set("X-Request-Id", id)
		ctx := httpx.WithValues(httpx.WithRequestID(r.Context(), id))
		ctx = context.WithValue(ctx, logInfoKey{}, &logInfo{})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Recover turns panics into 500 responses and logs the stack.
func Recover(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if p := recover(); p != nil {
					if p == http.ErrAbortHandler {
						panic(p)
					}
					log.ErrorContext(r.Context(), "panic serving request", "panic", fmt.Sprint(p), "path", httpx.RedactPath(r.URL.Path),
						"request_id", httpx.RequestIDFrom(r.Context()), "stack", string(debug.Stack()))
					httpx.WriteError(w, r, httpx.ErrInternal(fmt.Errorf("panic: %v", p)))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type logInfoKey struct{}

type logInfo struct {
	mu     sync.Mutex
	userID string
}

// AccessLog writes one structured log line per request.
func AccessLog(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &httpx.StatusWriter{ResponseWriter: w}
			next.ServeHTTP(sw, r)
			status := sw.Status
			if status == 0 {
				status = http.StatusOK
			}
			level := slog.LevelInfo
			switch {
			case status >= 500:
				level = slog.LevelError
			case r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || strings.HasPrefix(r.URL.Path, "/static/") ||
				strings.HasPrefix(r.URL.Path, "/media/") || strings.HasPrefix(r.URL.Path, "/api/worker/"):
				level = slog.LevelDebug
			}
			if !log.Enabled(r.Context(), level) {
				return
			}
			attrs := []slog.Attr{
				slog.String("method", r.Method), slog.String("path", httpx.RedactPath(r.URL.Path)), slog.Int("status", status),
				slog.Int64("bytes", sw.Bytes), slog.Duration("duration", time.Since(start)),
				slog.String("ip", httpx.ClientIP(r)), slog.String("request_id", httpx.RequestIDFrom(r.Context())),
			}
			if li, ok := r.Context().Value(logInfoKey{}).(*logInfo); ok {
				li.mu.Lock()
				if li.userID != "" {
					attrs = append(attrs, slog.String("user", li.userID))
				}
				li.mu.Unlock()
			}
			log.LogAttrs(r.Context(), level, "http", attrs...)
		})
	}
}

var (
	cspMu    sync.RWMutex
	cspExtra = map[string][]string{}
)

// AddCSPSource adds a source to a CSP directive for every response, e.g.
// AddCSPSource("style-src", "'sha256-…'"). Call it during init/wiring only.
func AddCSPSource(directive, source string) {
	cspMu.Lock()
	cspExtra[directive] = append(cspExtra[directive], source)
	cspMu.Unlock()
}

// ContentSecurityPolicy returns the strict default policy for a request host:
// everything from 'self', images also from data: and blob:, WebSockets to the same origin.
func ContentSecurityPolicy(host string) string {
	d := map[string][]string{
		"default-src":     {"'self'"},
		"base-uri":        {"'self'"},
		"object-src":      {"'none'"},
		"frame-ancestors": {"'none'"},
		"form-action":     {"'self'"},
		"img-src":         {"'self'", "data:", "blob:"},
		"media-src":       {"'self'", "blob:"},
		"font-src":        {"'self'", "data:"},
		"script-src":      {"'self'"},
		"style-src":       {"'self'"},
		// Style attributes (not <style> elements) are allowed so islands and
		// templates can position canvas overlays, bubbles and cursors.
		"style-src-attr": {"'unsafe-inline'"},
		"worker-src":     {"'self'", "blob:"},
		"manifest-src":   {"'self'"},
		"connect-src":    {"'self'"},
	}
	if host != "" {
		d["connect-src"] = append(d["connect-src"], "ws://"+host, "wss://"+host)
	}
	cspMu.RLock()
	for k, v := range cspExtra {
		d[k] = append(d[k], v...)
	}
	cspMu.RUnlock()
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(k + " " + strings.Join(d[k], " "))
	}
	return b.String()
}

// SecurityHeaders sets the CSP and hardening headers.
func SecurityHeaders(publicURL string) Middleware {
	hsts := strings.HasPrefix(publicURL, "https://")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", ContentSecurityPolicy(r.Host))
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=(), interest-cohort=()")
			if hsts && httpx.IsHTTPS(r) {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

func pathAllowed(path string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(path, p) || path == strings.TrimSuffix(p, "/") {
				return true
			}
		} else if path == p {
			return true
		}
	}
	return false
}

// MaintenanceGate answers 503 while maintenance mode is on, except for
// admins, health checks, static assets, login and workers.
func MaintenanceGate(d *Deps) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			inst, err := d.Settings.Instance(r.Context())
			if err != nil || !inst.Maintenance.Enabled ||
				pathAllowed(r.URL.Path, "/healthz", "/readyz", "/static/", "/login/", "/logout", "/api/v1/auth/login",
					"/api/v1/auth/logout", "/api/worker/", "/favicon.ico", "/manifest.webmanifest", "/sw.js") ||
				d.Auth.PeekIsAdmin(r) {
				next.ServeHTTP(w, r)
				return
			}
			e := httpx.NewError(http.StatusServiceUnavailable, httpx.CodeMaintenance, inst.Maintenance.Message)
			e.RetryAfter = 5 * time.Minute
			httpx.WriteError(w, r, e)
		})
	}
}

// SetupGate sends every request to the first-run wizard until an admin exists:
// HTML requests are redirected to /setup, API requests get 503 setup_required.
func SetupGate(d *Deps) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if pathAllowed(r.URL.Path, "/setup/", "/static/", "/healthz", "/readyz", "/api/v1/setup/", "/api/worker/",
				"/favicon.ico", "/manifest.webmanifest", "/sw.js", "/media/") {
				next.ServeHTTP(w, r)
				return
			}
			ok, err := d.Auth.HasAdmin(r.Context())
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			if ok {
				next.ServeHTTP(w, r)
				return
			}
			if !httpx.WantsJSON(r) && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
				httpx.Redirect(w, r, "/setup")
				return
			}
			httpx.WriteError(w, r, httpx.NewError(http.StatusServiceUnavailable, httpx.CodeSetupRequired, ""))
		})
	}
}

// Localize negotiates the interface language (user preference → cookie →
// Accept-Language → instance default), resolves the theme and exposes the
// user's preferences through PrefsFrom.
func Localize(d *Deps) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			u := httpx.UserFrom(ctx)
			if li, ok := ctx.Value(logInfoKey{}).(*logInfo); ok && u != nil {
				li.mu.Lock()
				li.userID = u.ID
				li.mu.Unlock()
			}
			var once sync.Once
			var prefs settings.UserPrefs
			load := func() settings.UserPrefs {
				once.Do(func() {
					prefs = settings.DefaultUserPrefs()
					if u != nil {
						if p, err := d.Settings.UserPrefs(ctx, u.ID); err == nil {
							prefs = p
						}
					}
				})
				return prefs
			}
			ctx = context.WithValue(ctx, prefsKey{}, load)

			userLang, theme := "", ""
			if u != nil {
				p := load()
				userLang, theme = firstNonEmpty(p.Languages.UI, u.Locale), p.Theme
			}
			cookieLang := ""
			if c, err := r.Cookie(LangCookie); err == nil {
				cookieLang = c.Value
			}
			if theme == "" {
				if c, err := r.Cookie(ThemeCookie); err == nil && (c.Value == "dark" || c.Value == "light" || c.Value == "system") {
					theme = c.Value
				}
			}
			def := "en"
			if inst, err := d.Settings.Instance(ctx); err == nil && inst.DefaultLocale != "" {
				def = inst.DefaultLocale
			}
			bundle := d.I18n
			if bundle == nil {
				bundle = i18n.Default()
			}
			lang := i18n.Negotiate(bundle.Languages(), def, userLang, cookieLang, r.Header.Get("Accept-Language"))
			w.Header().Set("Content-Language", lang)
			w.Header().Add("Vary", "Accept-Language, Cookie")
			ctx = i18n.WithLocale(ctx, lang)
			ctx = httpx.WithTheme(ctx, firstNonEmpty(theme, "dark"))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// CSRF rejects cross-origin state-changing browser requests with Go's
// http.CrossOriginProtection (Sec-Fetch-Site / Origin checks). Session cookies
// are SameSite=Lax as a second layer. Non-browser clients (bearer tokens,
// workers) send neither header and pass.
func CSRF(publicURL string) Middleware {
	cop := http.NewCrossOriginProtection()
	if publicURL != "" {
		if err := cop.AddTrustedOrigin(strings.TrimRight(publicURL, "/")); err != nil {
			slog.Warn("core: CSRF trusted origin", "url", publicURL, "err", err)
		}
	}
	cop.AddInsecureBypassPattern("/api/worker/")
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, httpx.KeyedError(http.StatusForbidden, "csrf", "error.csrf"))
	}))
	return cop.Handler
}

// RateLimits applies a strict limit to authentication endpoints and a
// generous one to the JSON API (per user, or per IP for anonymous calls).
func RateLimits() Middleware {
	authL := NewLimiter(1.0/3, 20) // ~20/min per IP
	apiL := NewLimiter(30, 120)
	authPaths := []string{"/login/", "/signup", "/forgot", "/reset/", "/api/v1/auth/login", "/api/v1/auth/signup", "/api/v1/auth/password/", "/setup/"}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			switch {
			case r.Method == http.MethodPost && pathAllowed(path, authPaths...):
				if ok, wait := authL.Allow("ip:" + httpx.ClientIP(r)); !ok {
					httpx.WriteError(w, r, httpx.ErrRateLimited(wait))
					return
				}
			case strings.HasPrefix(path, "/api/v1/") || strings.HasPrefix(path, "/mcp"):
				key := "ip:" + httpx.ClientIP(r)
				if u := httpx.UserFrom(r.Context()); u != nil {
					key = "u:" + u.ID
				}
				if ok, wait := apiL.Allow(key); !ok {
					httpx.WriteError(w, r, httpx.ErrRateLimited(wait))
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
