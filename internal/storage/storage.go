// Package storage stores media files (page art, layers, masks, references,
// exports, worker artifacts) behind one Store interface with a local-disk and
// an S3-compatible implementation, and serves them to browsers and workers
// through short-lived HMAC-signed URLs at GET /media/{key...}.
//
// Keys are slash-separated relative paths such as
// "projects/01j.../pages/01j....png". Callers choose keys; NewKey helps.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// ErrNotFound is returned when a key does not exist.
var ErrNotFound = errors.New("storage: not found")

// ErrInvalidKey is returned for keys that are empty, absolute, contain ".." or unusual characters.
var ErrInvalidKey = errors.New("storage: invalid key")

// Info describes a stored object.
type Info struct {
	Key         string    `json:"key"`
	Size        int64     `json:"size"`
	ContentType string    `json:"content_type"`
	ModTime     time.Time `json:"mod_time"`
	ETag        string    `json:"etag,omitempty"`
}

// Object is an open stored file. It supports seeking so it can be served with range requests.
type Object interface {
	io.ReadSeekCloser
}

// Store is a flat key/value blob store.
type Store interface {
	// Put stores r under key. size may be -1 when unknown. contentType may be
	// empty, in which case it is derived from the key's extension.
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// Get opens key for reading. The caller must close the object.
	Get(ctx context.Context, key string) (Object, Info, error)
	// Stat returns metadata without opening the object.
	Stat(ctx context.Context, key string) (Info, error)
	// Delete removes key. Deleting a missing key is not an error.
	Delete(ctx context.Context, key string) error
	// List returns objects whose key starts with prefix, sorted by key.
	List(ctx context.Context, prefix string) ([]Info, error)
	// Kind names the backend ("local" or "s3").
	Kind() string
}

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._\-/]*$`)

// ValidateKey checks that key is a safe relative path.
func ValidateKey(key string) error {
	if key == "" || len(key) > 512 || !keyPattern.MatchString(key) {
		return fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	if strings.Contains(key, "//") || strings.HasSuffix(key, "/") {
		return fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	for _, part := range strings.Split(key, "/") {
		if part == "." || part == ".." || strings.HasPrefix(part, ".") {
			return fmt.Errorf("%w: %q", ErrInvalidKey, key)
		}
	}
	return nil
}

// NewKey builds a unique key "<prefix>/<ulid><ext>" for a content type, e.g.
// NewKey("projects/01j.../layers", "image/png") → "projects/01j.../layers/01j....png".
func NewKey(prefix, contentType string) string {
	prefix = strings.Trim(prefix, "/")
	name := db.NewID() + ExtensionFor(contentType)
	if prefix == "" {
		return name
	}
	return prefix + "/" + name
}

var preferredExt = map[string]string{
	"image/png":        ".png",
	"image/jpeg":       ".jpg",
	"image/gif":        ".gif",
	"image/webp":       ".webp",
	"image/avif":       ".avif",
	"image/svg+xml":    ".svg",
	"video/mp4":        ".mp4",
	"video/webm":       ".webm",
	"audio/mpeg":       ".mp3",
	"audio/wav":        ".wav",
	"audio/ogg":        ".ogg",
	"application/pdf":  ".pdf",
	"application/zip":  ".zip",
	"application/json": ".json",
	"text/plain":       ".txt",
}

// ExtensionFor returns the file extension (with dot) for a content type, or ".bin".
func ExtensionFor(contentType string) string {
	ct, _, _ := mime.ParseMediaType(contentType)
	if ext, ok := preferredExt[ct]; ok {
		return ext
	}
	if exts, _ := mime.ExtensionsByType(ct); len(exts) > 0 {
		return exts[0]
	}
	return ".bin"
}

// ContentTypeFor returns the content type implied by a key's extension.
func ContentTypeFor(key string) string {
	ext := strings.ToLower(path.Ext(key))
	for ct, e := range preferredExt {
		if e == ext {
			return ct
		}
	}
	switch ext {
	case ".jpeg":
		return "image/jpeg"
	case ".safetensors", ".bin", "":
		return "application/octet-stream"
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// PutBytes is a convenience wrapper around Store.Put for in-memory data.
func PutBytes(ctx context.Context, s Store, key string, b []byte, contentType string) error {
	return s.Put(ctx, key, strings.NewReader(string(b)), int64(len(b)), contentType)
}

// ReadAll reads a whole object into memory, refusing objects larger than max bytes.
func ReadAll(ctx context.Context, s Store, key string, max int64) ([]byte, error) {
	obj, info, err := s.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	if max > 0 && info.Size > max {
		return nil, fmt.Errorf("storage: %s is %d bytes, limit %d", key, info.Size, max)
	}
	lr := io.Reader(obj)
	if max > 0 {
		lr = io.LimitReader(obj, max+1)
	}
	b, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if max > 0 && int64(len(b)) > max {
		return nil, fmt.Errorf("storage: %s exceeds %d bytes", key, max)
	}
	return b, nil
}
