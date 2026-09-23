package storage

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// MediaPrefix is the URL path under which signed media is served.
const MediaPrefix = "/media/"

// ErrBadSignature is returned for missing, expired or forged media URLs.
var ErrBadSignature = errors.New("storage: invalid or expired media signature")

// Signer creates and verifies HMAC-signed media URLs.
//
// Expiry times are rounded up to a bucket (a quarter of the TTL, at most one
// hour) so the same object yields the same URL for a while and browsers can cache it.
type Signer struct {
	key []byte
	now func() time.Time
}

// NewSigner creates a signer from a secret key (use secrets.Box.Derive("media-urls")).
func NewSigner(key []byte) *Signer {
	k := make([]byte, len(key))
	copy(k, key)
	return &Signer{key: k, now: time.Now}
}

// SetClock overrides the time source (tests).
func (s *Signer) SetClock(now func() time.Time) { s.now = now }

func (s *Signer) sign(key string, exp int64) string {
	m := hmac.New(sha256.New, s.key)
	fmt.Fprintf(m, "%s\n%d", key, exp)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:18])
}

// Expiry returns the rounded expiry time for a TTL.
func (s *Signer) Expiry(ttl time.Duration) time.Time {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	bucket := ttl / 4
	if bucket > time.Hour {
		bucket = time.Hour
	}
	if bucket < time.Second {
		bucket = time.Second
	}
	exp := s.now().Add(ttl)
	return exp.Truncate(bucket).Add(bucket).UTC()
}

// URL returns a root-relative signed URL "/media/<key>?exp=…&sig=…" valid for at least ttl.
func (s *Signer) URL(key string, ttl time.Duration) string {
	exp := s.Expiry(ttl).Unix()
	return MediaPrefix + escapeKey(key) + "?exp=" + strconv.FormatInt(exp, 10) + "&sig=" + s.sign(key, exp)
}

// AbsoluteURL is URL prefixed with base (e.g. the instance public URL), for workers and emails.
func (s *Signer) AbsoluteURL(base, key string, ttl time.Duration) string {
	return strings.TrimRight(base, "/") + s.URL(key, ttl)
}

// Verify checks exp and sig for key.
func (s *Signer) Verify(key, exp, sig string) error {
	e, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || sig == "" {
		return ErrBadSignature
	}
	if s.now().Unix() > e {
		return ErrBadSignature
	}
	want := s.sign(key, e)
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return ErrBadSignature
	}
	return nil
}

func escapeKey(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// inlineTypes may be displayed inline; everything else is served as an attachment.
var inlineTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true, "image/avif": true,
	"video/mp4": true, "video/webm": true, "audio/mpeg": true, "audio/ogg": true, "audio/wav": true,
	"application/pdf": true, "application/json": true, "text/plain": true,
}

// MediaHandler serves signed media. Mount it at "GET /media/{key...}".
// Requests without a valid signature get 403; missing objects 404.
func MediaHandler(store Store, signer *Signer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("key")
		if key == "" {
			key = strings.TrimPrefix(r.URL.Path, MediaPrefix)
		}
		if ValidateKey(key) != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		q := r.URL.Query()
		if err := signer.Verify(key, q.Get("exp"), q.Get("sig")); err != nil {
			http.Error(w, "forbidden: invalid or expired media link", http.StatusForbidden)
			return
		}
		obj, info, err := store.Get(r.Context(), key)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, "storage error", http.StatusInternalServerError)
			return
		}
		defer obj.Close()
		h := w.Header()
		ct := info.ContentType
		if ct == "" {
			ct = ContentTypeFor(key)
		}
		if inlineTypes[ct] {
			h.Set("Content-Disposition", "inline")
		} else {
			// SVG, HTML and unknown types never render in the app origin.
			ct = "application/octet-stream"
			h.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", path.Base(key)))
		}
		h.Set("Content-Type", ct)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; media-src 'self'; style-src 'unsafe-inline'; sandbox")
		h.Set("Cross-Origin-Resource-Policy", "same-site")
		if exp, err := strconv.ParseInt(q.Get("exp"), 10, 64); err == nil {
			maxAge := exp - time.Now().Unix()
			if maxAge > 86400 {
				maxAge = 86400
			}
			if maxAge < 0 {
				maxAge = 0
			}
			h.Set("Cache-Control", "private, max-age="+strconv.FormatInt(maxAge, 10))
		}
		if info.ETag != "" {
			h.Set("ETag", info.ETag)
		}
		http.ServeContent(w, r, path.Base(key), info.ModTime, obj)
	})
}
