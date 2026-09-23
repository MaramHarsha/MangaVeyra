package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// userAgent identifies MangaVeyra to providers.
const userAgent = "MangaVeyra/1 (+https://github.com/MaramHarsha/MangaVeyra)"

// newRequest builds a request with a JSON body (nil = no body).
func newRequest(ctx context.Context, method, url string, body any, headers map[string]string) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, Errf(CodeInternal, "encode request: %v", err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return nil, Errf(CodeBadRequest, "invalid URL: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	return req, nil
}

// send performs req and returns the body of a 2xx response (errors are mapped to *Error).
func (e Env) send(req *http.Request) ([]byte, http.Header, error) {
	resp, err := e.client().Do(req)
	if err != nil {
		return nil, nil, transportError(req.Context(), err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, e.maxBytes()+1))
	if err != nil {
		return nil, nil, transportError(req.Context(), err)
	}
	if int64(len(body)) > e.maxBytes() {
		return nil, nil, Errf(CodeBadRequest, "response larger than %d MiB", e.maxBytes()>>20)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, nil, statusError(resp.StatusCode, body)
	}
	return body, resp.Header, nil
}

// call sends a JSON request and decodes a JSON response into out (may be nil).
func (e Env) call(ctx context.Context, method, url string, headers map[string]string, in, out any) error {
	req, err := newRequest(ctx, method, url, in, headers)
	if err != nil {
		return err
	}
	body, _, err := e.send(req)
	if err != nil {
		return err
	}
	if out == nil || len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return Errf(CodeInternal, "unexpected response: %v", err)
	}
	return nil
}

func transportError(ctx context.Context, err error) error {
	var ne net.Error
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.As(err, &ne) && ne.Timeout():
		return Errf(CodeTimeout, "provider did not answer in time")
	case strings.Contains(err.Error(), "netguard"), strings.Contains(err.Error(), "blocked"):
		return Errf(CodeBadRequest, "address not allowed: %v", err)
	}
	return Errf(CodeUnavailable, "cannot reach provider: %v", err)
}

var safetyWords = []string{"safety", "content_policy", "content policy", "moderation", "nsfw", "prohibited", "flagged", "blocked by"}

// looksLikeSafety reports whether a provider message describes a safety block.
func looksLikeSafety(msg string) bool {
	m := strings.ToLower(msg)
	for _, w := range safetyWords {
		if strings.Contains(m, w) {
			return true
		}
	}
	return false
}

// statusError maps an HTTP error response to an *Error.
func statusError(status int, body []byte) *Error {
	msg := errorMessage(body)
	if msg == "" {
		msg = http.StatusText(status)
	}
	code := CodeInternal
	switch {
	case status == 401 || status == 403:
		code = CodeAuth
	case status == 429:
		code = CodeRateLimit
	case status == 408 || status == 504:
		code = CodeTimeout
	case looksLikeSafety(msg):
		code = CodeSafety
	case status == 400 || status == 404 || status == 409 || status == 413 || status == 415 || status == 422:
		code = CodeBadRequest
	case status >= 500:
		code = CodeUnavailable
	}
	return &Error{Code: code, Status: status, Message: msg}
}

// errorMessage extracts a readable message from common error envelopes.
func errorMessage(body []byte) string {
	var v any
	if json.Unmarshal(body, &v) != nil {
		s := strings.TrimSpace(string(body))
		if len(s) > 300 {
			s = s[:300]
		}
		return s
	}
	var find func(any) string
	find = func(x any) string {
		switch t := x.(type) {
		case string:
			return t
		case map[string]any:
			for _, k := range []string{"message", "error", "detail", "msg", "title", "errors"} {
				if s := find(t[k]); s != "" {
					return s
				}
			}
		case []any:
			for _, it := range t {
				if s := find(it); s != "" {
					return s
				}
			}
		}
		return ""
	}
	s := find(v)
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// fetch downloads a URL (or decodes a data: URI) as media.
func (e Env) fetch(ctx context.Context, url string, headers map[string]string) (Image, error) {
	if strings.HasPrefix(url, "data:") {
		return decodeDataURI(url)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Image{}, Errf(CodeInternal, "invalid result URL: %v", err)
	}
	req.Header.Set("User-Agent", userAgent)
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	b, h, err := e.send(req)
	if err != nil {
		return Image{}, err
	}
	mime := h.Get("Content-Type")
	if mime == "" || strings.HasPrefix(mime, "application/octet-stream") || strings.HasPrefix(mime, "binary/") {
		mime = http.DetectContentType(b)
	}
	if i := strings.Index(mime, ";"); i > 0 {
		mime = mime[:i]
	}
	return Image{Data: b, MIME: mime}, nil
}

// decodeB64 decodes base64 image data (with or without a data: prefix).
func decodeB64(s string) (Image, error) {
	if strings.HasPrefix(s, "data:") {
		return decodeDataURI(s)
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(s))
		if err != nil {
			return Image{}, Errf(CodeInternal, "invalid base64 image: %v", err)
		}
	}
	return Image{Data: b, MIME: http.DetectContentType(b)}, nil
}

func decodeDataURI(s string) (Image, error) {
	comma := strings.Index(s, ",")
	if comma < 0 {
		return Image{}, Errf(CodeInternal, "invalid data URI")
	}
	meta, data := s[5:comma], s[comma+1:]
	mime := strings.TrimSuffix(meta, ";base64")
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return Image{}, Errf(CodeInternal, "invalid data URI: %v", err)
	}
	if mime == "" {
		mime = http.DetectContentType(b)
	}
	return Image{Data: b, MIME: mime}, nil
}

// DataURI encodes media as a data: URI (inputs for APIs that accept them).
func DataURI(img Image) string {
	mime := img.MIME
	if mime == "" {
		mime = http.DetectContentType(img.Data)
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(img.Data)
}

// sleep waits d or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// joinURL joins a base URL and a path with exactly one slash.
func joinURL(base, path string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

// baseOr returns cfg.BaseURL or def.
func baseOr(cfg Config, def string) string {
	if strings.TrimSpace(cfg.BaseURL) != "" {
		return strings.TrimSpace(cfg.BaseURL)
	}
	return def
}

// jsonUnmarshal decodes a provider response.
func jsonUnmarshal(b []byte, v any) error {
	if err := json.Unmarshal(b, v); err != nil {
		return Errf(CodeInternal, "unexpected response: %v", err)
	}
	return nil
}

func itoa(n int) string { return strconv.Itoa(n) }

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// sizeString formats "WxH".
func sizeString(w, h int) string { return fmt.Sprintf("%dx%d", w, h) }

// aspect returns the closest common aspect ratio string for w×h.
func aspect(w, h int) string {
	if w <= 0 || h <= 0 {
		return "1:1"
	}
	r := float64(w) / float64(h)
	best, bestD := "1:1", 1e9
	for _, a := range []struct {
		s string
		v float64
	}{{"1:1", 1}, {"2:3", 2.0 / 3}, {"3:2", 1.5}, {"3:4", 0.75}, {"4:3", 4.0 / 3}, {"4:5", 0.8}, {"5:4", 1.25},
		{"9:16", 9.0 / 16}, {"16:9", 16.0 / 9}, {"21:9", 21.0 / 9}, {"1:4", 0.25}, {"4:1", 4}, {"1:8", 0.125}, {"8:1", 8}} {
		d := r/a.v + a.v/r
		if d < bestD {
			best, bestD = a.s, d
		}
	}
	return best
}

// stylePrompt appends the requested finish to a prompt.
func stylePrompt(prompt, style string) string {
	switch style {
	case "bw_screentone":
		return prompt + ", black and white manga, clean ink lines, screentone shading"
	case "flat_grey":
		return prompt + ", greyscale manga, flat grey tones"
	case "colour":
		return prompt + ", full colour"
	}
	return prompt
}
