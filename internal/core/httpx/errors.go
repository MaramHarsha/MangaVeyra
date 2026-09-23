package httpx

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

// Error codes used in the JSON error envelope.
const (
	CodeBadRequest       = "bad_request"
	CodeUnauthorized     = "unauthorized"
	CodeForbidden        = "forbidden"
	CodeNotFound         = "not_found"
	CodeMethodNotAllowed = "method_not_allowed"
	CodeConflict         = "conflict"
	CodeValidation       = "validation"
	CodeRateLimited      = "rate_limited"
	CodeTooLarge         = "too_large"
	CodeUnsupportedType  = "unsupported_type"
	CodeLocked           = "locked"
	CodeMaintenance      = "maintenance"
	CodeSetupRequired    = "setup_required"
	CodeInternal         = "internal"
)

// APIError is an error with an HTTP status and a stable machine-readable code.
// Handlers return it (or wrap it) and the router renders it as
//
//	{"error": {"code": "...", "message": "...", "fields": {...}}}
//
// for API requests, or through the web layer's error page for HTML requests.
type APIError struct {
	Status  int               `json:"-"`
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
	// Key is the i18n key used for Message when Message is empty (default "error.<code>").
	Key string `json:"-"`
	// RetryAfter is sent as a Retry-After header when non-zero.
	RetryAfter time.Duration `json:"-"`
	// Err is the underlying cause, logged but never sent to the client.
	Err error `json:"-"`
}

// Error implements error.
func (e *APIError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return e.Code + ": " + e.Message
}

// Unwrap returns the cause.
func (e *APIError) Unwrap() error { return e.Err }

// NewError creates an APIError. An empty message is filled from the i18n
// catalog key "error.<code>" at render time.
func NewError(status int, code, message string) *APIError {
	return &APIError{Status: status, Code: code, Message: message}
}

// KeyedError creates an APIError whose message is the i18n text of key.
func KeyedError(status int, code, key string) *APIError {
	return &APIError{Status: status, Code: code, Key: key}
}

// ErrBadRequest returns a 400 error.
func ErrBadRequest(msg string) *APIError { return NewError(http.StatusBadRequest, CodeBadRequest, msg) }

// ErrUnauthorized returns a 401 error.
func ErrUnauthorized() *APIError { return NewError(http.StatusUnauthorized, CodeUnauthorized, "") }

// ErrForbidden returns a 403 error.
func ErrForbidden(msg string) *APIError { return NewError(http.StatusForbidden, CodeForbidden, msg) }

// ErrNotFound returns a 404 error.
func ErrNotFound() *APIError { return NewError(http.StatusNotFound, CodeNotFound, "") }

// ErrConflict returns a 409 error.
func ErrConflict(msg string) *APIError { return NewError(http.StatusConflict, CodeConflict, msg) }

// ErrLocked returns a 423 error for changes to locked panels/pages.
func ErrLocked(msg string) *APIError { return NewError(http.StatusLocked, CodeLocked, msg) }

// ErrValidation returns a 422 error with per-field messages.
func ErrValidation(fields map[string]string) *APIError {
	e := NewError(http.StatusUnprocessableEntity, CodeValidation, "")
	e.Fields = fields
	return e
}

// ErrRateLimited returns a 429 error.
func ErrRateLimited(retryAfter time.Duration) *APIError {
	e := NewError(http.StatusTooManyRequests, CodeRateLimited, "")
	e.RetryAfter = retryAfter
	return e
}

// ErrInternal wraps an unexpected error as a 500 (the cause is logged, not shown).
func ErrInternal(err error) *APIError {
	e := NewError(http.StatusInternalServerError, CodeInternal, "")
	e.Err = err
	return e
}

// AsAPIError converts any error into an APIError: APIErrors pass through,
// sql.ErrNoRows becomes 404, request-body-too-large becomes 413 and
// everything else a 500.
func AsAPIError(err error) *APIError {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae
	}
	var mbe *http.MaxBytesError
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound()
	case errors.As(err, &mbe):
		return NewError(http.StatusRequestEntityTooLarge, CodeTooLarge, "")
	case errors.Is(err, context.Canceled):
		return &APIError{Status: 499, Code: "canceled", Message: "request canceled", Err: err}
	}
	return ErrInternal(err)
}

// Hooks are installed once by the web layer to render HTML for errors,
// wrap page fragments in the layout and resolve the login URL. Every hook has
// a plain fallback so the server works before the web layer exists.
type Hooks struct {
	// ErrorPage renders an HTML error page (404, 403, 500, 503 maintenance...).
	ErrorPage func(w http.ResponseWriter, r *http.Request, e *APIError)
	// NotFound handles HTML requests that match no route (the G6 404 page).
	// When nil, ErrorPage (or plain text) renders the 404.
	NotFound http.Handler
	// Layout wraps a page body in the full document layout for non-HTMX requests.
	Layout func(r *http.Request, body Component) Component
	// LoginURL returns where to send anonymous visitors of protected pages.
	LoginURL func(r *http.Request) string
}

var hooks atomic.Pointer[Hooks]

// SetHooks installs the web layer's hooks (nil fields keep the fallbacks).
func SetHooks(h Hooks) { hooks.Store(&h) }

// CurrentHooks returns the installed hooks.
func CurrentHooks() Hooks {
	if h := hooks.Load(); h != nil {
		return *h
	}
	return Hooks{}
}

// LoginURL returns the login URL for r (default "/login?next=<path>").
func LoginURL(r *http.Request) string {
	if h := CurrentHooks().LoginURL; h != nil {
		return h(r)
	}
	next := r.URL.RequestURI()
	return "/login?next=" + urlQueryEscape(next)
}

// Logger is used to log 5xx causes; app wiring replaces it with the configured logger.
var Logger atomic.Pointer[slog.Logger]

func logger() *slog.Logger {
	if l := Logger.Load(); l != nil {
		return l
	}
	return slog.Default()
}

type envelope struct {
	Error *APIError `json:"error"`
}

// WriteError renders err for the client: a JSON envelope for API requests,
// the web layer's error page for HTML requests, or plain text as a fallback.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	writeError(w, r, err, WantsJSON(r))
}

// WriteJSONError always renders the JSON error envelope.
func WriteJSONError(w http.ResponseWriter, r *http.Request, err error) {
	writeError(w, r, err, true)
}

func writeError(w http.ResponseWriter, r *http.Request, err error, asJSON bool) {
	e := AsAPIError(err)
	if e.Status >= 500 && e.Status != http.StatusServiceUnavailable {
		logger().ErrorContext(r.Context(), "request failed",
			"err", err, "method", r.Method, "path", RedactPath(r.URL.Path), "request_id", RequestIDFrom(r.Context()))
	}
	if e.Message == "" {
		e = localized(r.Context(), e)
	}
	if e.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(e.RetryAfter.Seconds()+0.999)))
	}
	if e.Status == 499 { // client went away; nothing useful to send
		return
	}
	if asJSON {
		JSON(w, e.Status, envelope{Error: e})
		return
	}
	if h := CurrentHooks().ErrorPage; h != nil {
		h(w, r, e)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(e.Status)
	_, _ = io.WriteString(w, e.Message+"\n")
}

func localized(ctx context.Context, e *APIError) *APIError {
	c := *e
	args := []any{}
	if e.RetryAfter > 0 {
		args = append(args, "seconds", int(e.RetryAfter.Seconds()+0.999))
	}
	key := e.Key
	if key == "" {
		key = "error." + e.Code
	}
	c.Message = i18n.T(ctx, key, args...)
	if c.Message == key {
		c.Message = http.StatusText(e.Status)
	}
	return &c
}

// JSON writes v as JSON with the given status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v == nil || status == http.StatusNoContent {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(v)
}

// OK writes {"ok": true}.
func OK(w http.ResponseWriter) { JSON(w, http.StatusOK, map[string]bool{"ok": true}) }

// MaxJSONBody is the default request body limit for DecodeJSON.
const MaxJSONBody = 1 << 20

// DecodeJSON decodes the request body into dst (limit 1 MiB). Errors are 400/413 APIErrors.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	return DecodeJSONLimit(w, r, dst, MaxJSONBody)
}

// DecodeJSONLimit is DecodeJSON with an explicit byte limit.
func DecodeJSONLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) error {
	if r.Body == nil {
		return ErrBadRequest("request body is empty")
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return NewError(http.StatusRequestEntityTooLarge, CodeTooLarge, "")
		}
		if errors.Is(err, io.EOF) {
			return ErrBadRequest("request body is empty")
		}
		return &APIError{Status: http.StatusBadRequest, Code: CodeBadRequest, Message: "invalid JSON: " + err.Error()}
	}
	if dec.More() {
		return ErrBadRequest("request body must contain a single JSON value")
	}
	return nil
}

// secretPathPrefixes are routes whose trailing path segments are bearer
// secrets (password-reset tokens, invite codes).
var secretPathPrefixes = []string{"/reset/", "/api/v1/auth/password/reset/", "/api/v1/auth/invites/"}

// RedactPath replaces secret path segments (password-reset tokens, invite
// codes) with "[redacted]" so access logs and error reports never contain them.
func RedactPath(p string) string {
	for _, pre := range secretPathPrefixes {
		if strings.HasPrefix(p, pre) && len(p) > len(pre) {
			return pre + "[redacted]"
		}
	}
	return p
}
