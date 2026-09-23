package httpx

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RequireUser rejects anonymous requests: API clients get 401, HTMX gets an
// HX-Redirect to the login page, browsers a 303 to the login page.
// Worker tokens do not count as users.
func RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := UserFrom(r.Context())
		if u == nil {
			denyAnonymous(w, r)
			return
		}
		if u.Status != "active" {
			WriteError(w, r, ErrForbidden("account is not active"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func denyAnonymous(w http.ResponseWriter, r *http.Request) {
	if WantsJSON(r) || (r.Method != http.MethodGet && r.Method != http.MethodHead && !IsHTMX(r)) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="mangaveyra"`)
		WriteError(w, r, ErrUnauthorized())
		return
	}
	Redirect(w, r, LoginURL(r))
}

// RequireRole allows signed-in users whose site role is one of roles. Admins always pass.
func RequireRole(roles ...string) Middleware {
	return func(next http.Handler) http.Handler {
		return RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := UserFrom(r.Context())
			if u.Role != RoleAdmin && !slices.Contains(roles, u.Role) {
				WriteError(w, r, ErrForbidden(""))
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}

// RequireHuman rejects requests authenticated with an agent token. Publish,
// delete, share and unlock always need a human (product rule).
func RequireHuman(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if IdentityFrom(r.Context()).IsAgent() {
			WriteError(w, r, ErrForbidden("this action needs a human; agents cannot do it"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireSession allows only signed-in browser sessions (session cookie). It
// guards account-security actions (password, two-factor, sessions, tokens)
// so a leaked or narrowly scoped bearer token cannot take over the account
// or mint broader tokens. Anonymous callers are treated like RequireUser.
func RequireSession(next http.Handler) http.Handler {
	return RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := IdentityFrom(r.Context()); id.Token != nil || id.SessionID == "" {
			WriteError(w, r, ErrForbidden("this action needs a signed-in browser session; tokens cannot do it"))
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// RequireToken allows only requests authenticated by a bearer token of kind
// that grants every listed scope.
func RequireToken(kind string, scopes ...string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t := TokenFrom(r.Context())
			if t == nil || t.Kind != kind {
				w.Header().Set("WWW-Authenticate", `Bearer realm="mangaveyra"`)
				WriteError(w, r, ErrUnauthorized())
				return
			}
			for _, s := range scopes {
				if !t.HasScope(s) {
					WriteError(w, r, ErrForbidden("token lacks scope "+s))
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireScope lets session users through and requires scope for token-authenticated requests.
func RequireScope(scope string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if t := TokenFrom(r.Context()); t != nil && !t.HasScope(scope) {
				WriteError(w, r, ErrForbidden("token lacks scope "+scope))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RealIP resolves the client IP. X-Forwarded-For is honoured only when the
// direct peer is one of trusted (IPs/CIDRs); the right-most untrusted address wins.
func RealIP(trusted []*net.IPNet) Middleware {
	isTrusted := func(ip net.IP) bool {
		for _, n := range trusted {
			if n.Contains(ip) {
				return true
			}
		}
		return false
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				host = r.RemoteAddr
			}
			client := host
			// X-Mv-Https is internal: only this middleware may set it.
			r.Header.Del("X-Mv-Https")
			if peer := net.ParseIP(host); peer != nil && len(trusted) > 0 && isTrusted(peer) {
				if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
					parts := strings.Split(xff, ",")
					for i := len(parts) - 1; i >= 0; i-- {
						cand := strings.TrimSpace(parts[i])
						ip := net.ParseIP(cand)
						if ip == nil {
							break
						}
						client = cand
						if !isTrusted(ip) {
							break
						}
					}
				}
				if proto := r.Header.Get("X-Forwarded-Proto"); proto == "https" && r.TLS == nil {
					r.Header.Set("X-Mv-Https", "1")
				}
			}
			next.ServeHTTP(w, r.WithContext(WithClientIP(r.Context(), client)))
		})
	}
}

// IsHTTPS reports whether the client connection is HTTPS (directly or via a trusted proxy).
func IsHTTPS(r *http.Request) bool { return r.TLS != nil || r.Header.Get("X-Mv-Https") == "1" }

// ---- rate limiting ---------------------------------------------------------

// Limiter is an in-memory token-bucket rate limiter keyed by string.
type Limiter struct {
	rate  float64 // tokens per second
	burst float64
	now   func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
	swept   time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewLimiter allows rate events per second with bursts of burst.
func NewLimiter(rate float64, burst int) *Limiter {
	return &Limiter{rate: rate, burst: float64(burst), now: time.Now, buckets: map[string]*bucket{}}
}

// SetClock overrides the time source (tests).
func (l *Limiter) SetClock(now func() time.Time) { l.now = now }

// Allow takes one token for key. When none is left it returns false and how long until one is.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	return l.AllowN(key, 1)
}

// AllowN takes n tokens for key.
func (l *Limiter) AllowN(key string, n float64) (bool, time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.swept) > time.Minute {
		l.sweep(now)
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens >= n {
		b.tokens -= n
		return true, 0
	}
	wait := time.Duration((n - b.tokens) / l.rate * float64(time.Second))
	return false, wait
}

// Reset forgets key (e.g. after a successful login).
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.buckets, key)
	l.mu.Unlock()
}

func (l *Limiter) sweep(now time.Time) {
	full := time.Duration(l.burst / l.rate * float64(time.Second))
	for k, b := range l.buckets {
		if now.Sub(b.last) > full {
			delete(l.buckets, k)
		}
	}
	l.swept = now
}

// RateLimit applies l to requests, keyed by key(r) (skip when key returns "").
func RateLimit(l *Limiter, key func(r *http.Request) string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			k := key(r)
			if k != "" {
				if ok, wait := l.Allow(k); !ok {
					WriteError(w, r, ErrRateLimited(wait))
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---- pagination --------------------------------------------------------------

// PageRequest is a parsed ?limit=&cursor=&page= query.
type PageRequest struct {
	Limit  int
	Cursor string // decoded cursor (usually the last id of the previous page)
	Page   int    // 1-based page number for offset pagination
}

// Offset returns the row offset for offset pagination.
func (p PageRequest) Offset() int { return (p.Page - 1) * p.Limit }

// ParsePage reads limit (default def, capped at max), cursor and page from the query.
func ParsePage(r *http.Request, def, max int) PageRequest {
	q := r.URL.Query()
	p := PageRequest{Limit: def, Page: 1}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		p.Limit = n
	}
	if p.Limit > max {
		p.Limit = max
	}
	if n, err := strconv.Atoi(q.Get("page")); err == nil && n > 0 {
		p.Page = n
	}
	p.Cursor = DecodeCursor(q.Get("cursor"))
	return p
}

// EncodeCursor makes an opaque cursor from a sort key.
func EncodeCursor(v string) string {
	if v == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(v))
}

// DecodeCursor reverses EncodeCursor ("" for invalid input).
func DecodeCursor(c string) string {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return ""
	}
	return string(b)
}

// PageResult is the standard list response.
type PageResult[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// Paginate trims items (fetched with LIMIT limit+1) to limit and sets the next
// cursor from the last kept item when more rows exist.
func Paginate[T any](items []T, limit int, cursorOf func(T) string) PageResult[T] {
	if items == nil {
		items = []T{}
	}
	res := PageResult[T]{Items: items}
	if limit > 0 && len(items) > limit {
		res.Items = items[:limit]
		res.NextCursor = EncodeCursor(cursorOf(items[limit-1]))
	}
	return res
}

// MarshalJSONString is a helper for embedding JSON in HTML data attributes.
func MarshalJSONString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}
