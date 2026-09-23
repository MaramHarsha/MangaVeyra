package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Local stores objects as files under a root directory.
type Local struct {
	root string
}

// NewLocal creates (if needed) and opens a local store rooted at dir.
func NewLocal(dir string) (*Local, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, fmt.Errorf("storage: create %s: %w", abs, err)
	}
	return &Local{root: abs}, nil
}

// Root returns the absolute root directory.
func (l *Local) Root() string { return l.root }

// Kind implements Store.
func (l *Local) Kind() string { return "local" }

func (l *Local) path(key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	p := filepath.Join(l.root, filepath.FromSlash(key))
	if !strings.HasPrefix(p, l.root+string(filepath.Separator)) {
		return "", ErrInvalidKey
	}
	return p, nil
}

// Put implements Store. The file is written to a temp file and renamed, so
// readers never see a partial object.
func (l *Local) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	n, err := io.Copy(tmp, readerWithContext(ctx, r))
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("storage: write %s: %w", key, err)
	}
	if size >= 0 && n != size {
		_ = tmp.Close()
		return fmt.Errorf("storage: write %s: got %d bytes, expected %d", key, n, size)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// Get implements Store.
func (l *Local) Get(ctx context.Context, key string) (Object, Info, error) {
	p, err := l.path(key)
	if err != nil {
		return nil, Info{}, err
	}
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, Info{}, ErrNotFound
		}
		return nil, Info{}, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, Info{}, err
	}
	if fi.IsDir() {
		_ = f.Close()
		return nil, Info{}, ErrNotFound
	}
	return f, l.info(key, fi), nil
}

// Stat implements Store.
func (l *Local) Stat(ctx context.Context, key string) (Info, error) {
	p, err := l.path(key)
	if err != nil {
		return Info{}, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Info{}, ErrNotFound
		}
		return Info{}, err
	}
	if fi.IsDir() {
		return Info{}, ErrNotFound
	}
	return l.info(key, fi), nil
}

func (l *Local) info(key string, fi fs.FileInfo) Info {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", key, fi.Size(), fi.ModTime().UnixNano())))
	return Info{
		Key:         key,
		Size:        fi.Size(),
		ContentType: ContentTypeFor(key),
		ModTime:     fi.ModTime().UTC(),
		ETag:        `"` + hex.EncodeToString(h[:8]) + `"`,
	}
}

// Delete implements Store.
func (l *Local) Delete(ctx context.Context, key string) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// Remove now-empty parent directories up to the root (best effort).
	for dir := filepath.Dir(p); dir != l.root && strings.HasPrefix(dir, l.root); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil {
			break
		}
	}
	return nil
}

// List implements Store.
func (l *Local) List(ctx context.Context, prefix string) ([]Info, error) {
	var out []Info
	err := filepath.WalkDir(l.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		rel, err := filepath.Rel(l.root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if !strings.HasPrefix(key, prefix) {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		out = append(out, l.info(key, fi))
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, err
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func readerWithContext(ctx context.Context, r io.Reader) io.Reader { return ctxReader{ctx: ctx, r: r} }
