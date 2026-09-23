package web

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

// FormErrors maps field names to translated messages; "" holds a form-level message.
type FormErrors map[string]string

// Add records a message for field (keeping the first one).
func (fe FormErrors) Add(field, msg string) {
	if _, ok := fe[field]; !ok {
		fe[field] = msg
	}
}

// Has reports whether any error was recorded.
func (fe FormErrors) Has() bool { return len(fe) > 0 }

// Form returns the form-level message.
func (fe FormErrors) Form() string { return fe[""] }

// AddError converts a service error into a field or form message in the
// request language: auth field errors go to their field, API errors to the form.
// It returns false for unexpected errors the caller should surface as a 500.
func (fe FormErrors) AddError(ctx context.Context, err error) bool {
	var fld *auth.FieldError
	if errors.As(err, &fld) {
		fe.Add(fld.Field, i18n.T(ctx, fld.Key, fld.Args...))
		return true
	}
	var ae *httpx.APIError
	if errors.As(err, &ae) && ae.Status < 500 {
		fe.Add("", ErrorText(ctx, ae))
		return true
	}
	return false
}

// ErrorText returns the translated message of an API error.
func ErrorText(ctx context.Context, ae *httpx.APIError) string {
	if ae.Message != "" {
		return ae.Message
	}
	key := ae.Key
	if key == "" {
		key = "error." + ae.Code
	}
	args := []any{}
	if ae.RetryAfter > 0 {
		args = append(args, "seconds", int(ae.RetryAfter.Seconds()+0.999))
	}
	msg := i18n.T(ctx, key, args...)
	if msg == key {
		msg = http.StatusText(ae.Status)
	}
	return msg
}

// FormValue returns a trimmed form value.
func FormValue(r *http.Request, name string) string { return strings.TrimSpace(r.PostFormValue(name)) }

// FormBool reports whether a checkbox is on.
func FormBool(r *http.Request, name string) bool {
	switch strings.ToLower(r.PostFormValue(name)) {
	case "on", "true", "1", "yes":
		return true
	}
	return false
}

// ParseForm parses a url-encoded or multipart form with a 1 MiB limit.
func ParseForm(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		return r.ParseMultipartForm(1 << 20)
	}
	return r.ParseForm()
}
