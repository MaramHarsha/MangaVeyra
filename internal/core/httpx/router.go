package httpx

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// Middleware wraps a handler.
type Middleware func(http.Handler) http.Handler

// Chain composes middlewares so the first one is the outermost.
func Chain(mws ...Middleware) Middleware {
	return func(h http.Handler) http.Handler {
		for i := len(mws) - 1; i >= 0; i-- {
			if mws[i] != nil {
				h = mws[i](h)
			}
		}
		return h
	}
}

// HandlerFunc is a handler that returns an error; errors are rendered with WriteError.
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// ServeHTTP implements http.Handler.
func (f HandlerFunc) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := f(w, r); err != nil {
		WriteError(w, r, err)
	}
}

// Route describes a registered route.
type Route struct {
	Pattern string `json:"pattern"`
	Kind    string `json:"kind"` // "handler", "page" or "api"
}

type routeTable struct {
	mu     sync.Mutex
	mux    *http.ServeMux
	routes []Route
}

// Router wraps http.ServeMux with Go 1.22 method+pattern routing, route
// groups with a path prefix and per-group middleware, and consistent 404/405
// rendering through WriteError.
//
//	r.Handle("GET /studio/projects", h, core.RequireUser)
//	api := r.Group("/api/v1", core.RequireUser)
//	api.API("GET /projects/{id}", m.getProject)
type Router struct {
	table  *routeTable
	prefix string
	mws    []Middleware
}

// NewRouter creates an empty router.
func NewRouter() *Router {
	return &Router{table: &routeTable{mux: http.NewServeMux()}}
}

// Group returns a sub-router whose patterns are prefixed with prefix and whose
// handlers are wrapped by mws (after the parent's middlewares).
func (r *Router) Group(prefix string, mws ...Middleware) *Router {
	return &Router{
		table:  r.table,
		prefix: r.prefix + strings.TrimRight(prefix, "/"),
		mws:    append(append([]Middleware{}, r.mws...), mws...),
	}
}

// Use appends middlewares to this router (affects routes registered afterwards).
func (r *Router) Use(mws ...Middleware) { r.mws = append(r.mws, mws...) }

// Handle registers h for pattern ("[METHOD ]/path", Go 1.22 syntax) with optional route middlewares.
// It panics on conflicting patterns, like http.ServeMux.
func (r *Router) Handle(pattern string, h http.Handler, mws ...Middleware) {
	r.register(pattern, "handler", h, mws)
}

// HandleFunc registers a plain handler function.
func (r *Router) HandleFunc(pattern string, fn func(http.ResponseWriter, *http.Request), mws ...Middleware) {
	r.register(pattern, "handler", http.HandlerFunc(fn), mws)
}

// Page registers an HTML page handler; returned errors render as HTML error pages.
func (r *Router) Page(pattern string, fn HandlerFunc, mws ...Middleware) {
	r.register(pattern, "page", fn, mws)
}

// API registers a JSON handler; returned errors render as the JSON error envelope.
func (r *Router) API(pattern string, fn HandlerFunc, mws ...Middleware) {
	r.register(pattern, "api", HandlerFunc(func(w http.ResponseWriter, req *http.Request) error {
		if err := fn(w, req); err != nil {
			writeJSONError(w, req, err)
		}
		return nil
	}), mws)
}

func writeJSONError(w http.ResponseWriter, r *http.Request, err error) {
	writeError(w, r, err, true)
}

func (r *Router) register(pattern, kind string, h http.Handler, mws []Middleware) {
	full := r.join(pattern)
	all := append(append([]Middleware{}, r.mws...), mws...)
	r.table.mu.Lock()
	defer r.table.mu.Unlock()
	r.table.mux.Handle(full, Chain(all...)(h))
	r.table.routes = append(r.table.routes, Route{Pattern: full, Kind: kind})
}

func (r *Router) join(pattern string) string {
	method, path := "", pattern
	if i := strings.IndexByte(pattern, ' '); i > 0 && !strings.HasPrefix(pattern, "/") {
		method, path = pattern[:i], strings.TrimSpace(pattern[i+1:])
	}
	if !strings.HasPrefix(path, "/") {
		panic(fmt.Sprintf("httpx: pattern %q must start with / (hosts are not supported)", pattern))
	}
	full := r.prefix + path
	if r.prefix != "" && path == "/" {
		full = r.prefix + "/"
	}
	if method != "" {
		return method + " " + full
	}
	return full
}

// routerKey carries the dispatching router in each request context.
type routerKey struct{}

// Serves reports whether a registered route matches method and path (path
// only, no query). Navigation uses it to render destinations that are not
// built yet as disabled "Soon" items instead of links to a 404, without
// registering placeholder routes that would later conflict (ADR 0004 §7).
func (r *Router) Serves(method, path string) bool {
	if r == nil || r.table == nil || !strings.HasPrefix(path, "/") {
		return false
	}
	req, err := http.NewRequest(method, path, nil)
	if err != nil {
		return false
	}
	_, pattern := r.table.mux.Handler(req)
	return pattern != ""
}

// Serves reports whether the router dispatching the current request serves
// method and path. Without a router in ctx (for example when a component is
// rendered in a unit test) every destination counts as available.
func Serves(ctx context.Context, method, path string) bool {
	r, _ := ctx.Value(routerKey{}).(*Router)
	if r == nil {
		return true
	}
	return r.Serves(method, path)
}

// Routes lists the registered routes sorted by pattern.
func (r *Router) Routes() []Route {
	r.table.mu.Lock()
	defer r.table.mu.Unlock()
	out := append([]Route{}, r.table.routes...)
	sort.Slice(out, func(i, j int) bool { return out[i].Pattern < out[j].Pattern })
	return out
}

// ServeHTTP dispatches to the matching route. Unmatched paths and wrong methods
// are rendered through WriteError so they get the JSON envelope or the HTML error page.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	req = req.WithContext(context.WithValue(req.Context(), routerKey{}, r))
	h, pattern := r.table.mux.Handler(req)
	if pattern != "" {
		r.table.mux.ServeHTTP(w, req)
		return
	}
	// No route matched: let the mux decide between 404, 405 and redirects
	// (trailing-slash / path cleaning), but render errors ourselves.
	iw := &interceptWriter{ResponseWriter: w}
	h.ServeHTTP(iw, req)
	switch iw.status {
	case http.StatusNotFound:
		if nf := CurrentHooks().NotFound; nf != nil && !WantsJSON(req) {
			nf.ServeHTTP(w, req)
			return
		}
		WriteError(w, req, ErrNotFound())
	case http.StatusMethodNotAllowed:
		if allow := iw.Header().Get("Allow"); allow != "" {
			w.Header().Set("Allow", allow)
		}
		WriteError(w, req, NewError(http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed"))
	}
}

// interceptWriter swallows the mux's plain-text 404/405 bodies so ServeHTTP can render them.
type interceptWriter struct {
	http.ResponseWriter
	status int
}

func (w *interceptWriter) WriteHeader(code int) {
	w.status = code
	if code == http.StatusNotFound || code == http.StatusMethodNotAllowed {
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *interceptWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.status == http.StatusNotFound || w.status == http.StatusMethodNotAllowed {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

// ---- rendering -----------------------------------------------------------

// Component is anything that renders itself; templ.Component satisfies it.
type Component interface {
	Render(ctx context.Context, w io.Writer) error
}

// ComponentFunc adapts a function to Component.
type ComponentFunc func(ctx context.Context, w io.Writer) error

// Render implements Component.
func (f ComponentFunc) Render(ctx context.Context, w io.Writer) error { return f(ctx, w) }

// Render writes an HTML component. HTMX requests (except boosted navigations)
// get the fragment alone; other requests get it wrapped by the Layout hook.
// Output is buffered so a render error still produces a clean error page.
func Render(w http.ResponseWriter, r *http.Request, status int, c Component) error {
	if !IsHTMX(r) || r.Header.Get("HX-Boosted") == "true" {
		if layout := CurrentHooks().Layout; layout != nil {
			c = layout(r, c)
		}
	}
	var buf bytes.Buffer
	if err := c.Render(r.Context(), &buf); err != nil {
		return fmt.Errorf("render: %w", err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if IsHTMX(r) {
		w.Header().Add("Vary", "HX-Request")
	}
	w.WriteHeader(status)
	_, err := buf.WriteTo(w)
	return err
}

// Redirect sends the client to target: HX-Redirect for HTMX requests, 303 otherwise.
func Redirect(w http.ResponseWriter, r *http.Request, target string) {
	if IsHTMX(r) {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// SafeRedirectTarget returns next when it is a local path ("/..."), else fallback.
// It prevents open redirects through ?next= parameters.
func SafeRedirectTarget(next, fallback string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return fallback
	}
	if u, err := url.Parse(next); err != nil || u.Host != "" || u.Scheme != "" {
		return fallback
	}
	return next
}

func urlQueryEscape(s string) string { return url.QueryEscape(s) }

// ---- response writer helpers ------------------------------------------------

// StatusWriter records the status code and bytes written; it keeps
// http.Flusher, http.Hijacker and ResponseController support via Unwrap.
type StatusWriter struct {
	http.ResponseWriter
	Status int
	Bytes  int64
}

// WriteHeader records the status.
func (w *StatusWriter) WriteHeader(code int) {
	if w.Status == 0 {
		w.Status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

// Write records bytes.
func (w *StatusWriter) Write(b []byte) (int, error) {
	if w.Status == 0 {
		w.Status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.Bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *StatusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush implements http.Flusher.
func (w *StatusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack implements http.Hijacker (needed by WebSockets).
func (w *StatusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("httpx: hijack not supported")
}
