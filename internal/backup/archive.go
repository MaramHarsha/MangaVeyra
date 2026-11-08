package backup

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/storage"
)

// Archive layout (ADR 0013 §4): a zip with manifest.json, one JSON-lines file
// per table under db/ and the media objects under media/<storage key>.
const (
	formatV1         = "mangaveyra.backup.v1"
	manifestName     = "manifest.json"
	dbPrefix         = "db/"
	mediaPrefix      = "media/"
	maxManifestBytes = 32 << 20
	// Extension is the archive file extension.
	Extension = ".mvbackup"
)

// Manifest describes an archive. Every other entry is listed with its
// SHA-256 so a damaged or edited archive is refused before anything changes.
type Manifest struct {
	Format     string              `json:"format"`
	ID         string              `json:"id"`
	AppVersion string              `json:"app_version"`
	Schema     string              `json:"schema"`
	Dialect    string              `json:"dialect"`
	Kind       string              `json:"kind"`
	UserID     string              `json:"user_id,omitempty"`
	CreatedAt  time.Time           `json:"created_at"`
	Includes   Includes            `json:"includes"`
	Tables     []TableEntry        `json:"tables"`
	Entries    map[string]EntrySum `json:"entries"`
}

// Includes summarises what an archive holds (also stored in backups.includes).
type Includes struct {
	Database     bool  `json:"database"`
	Media        bool  `json:"media"`
	Tables       int   `json:"tables"`
	Rows         int64 `json:"rows"`
	MediaObjects int   `json:"media_objects"`
	MediaBytes   int64 `json:"media_bytes"`
	// MediaMissing counts objects that disappeared while the archive was written.
	MediaMissing int `json:"media_missing,omitempty"`
	// Secrets is always false: the master key is never archived.
	Secrets bool `json:"secrets"`
	// Uploaded marks an archive added from outside (G10 upload or the CLI);
	// it is not counted as this server's last backup.
	Uploaded bool `json:"uploaded,omitempty"`
}

// TableEntry is one table file.
type TableEntry struct {
	Name    string   `json:"name"`
	File    string   `json:"file"`
	Rows    int64    `json:"rows"`
	Columns []column `json:"columns"`
}

// EntrySum is the size and checksum of one archive entry.
type EntrySum struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// archiveWriter streams entries into a zip and records their checksums.
type archiveWriter struct {
	zw      *zip.Writer
	entries map[string]EntrySum
	created time.Time
}

func newArchiveWriter(w io.Writer, created time.Time) *archiveWriter {
	return &archiveWriter{zw: zip.NewWriter(w), entries: map[string]EntrySum{}, created: created}
}

type hashCounter struct {
	h hash.Hash
	n int64
}

func (c *hashCounter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return c.h.Write(p)
}

// add writes one entry. Media are stored (already compressed images); table
// files are deflated.
func (w *archiveWriter) add(name string, store bool, fn func(io.Writer) error) error {
	method := zip.Deflate
	if store {
		method = zip.Store
	}
	ew, err := w.zw.CreateHeader(&zip.FileHeader{Name: name, Method: method, Modified: w.created})
	if err != nil {
		return err
	}
	hc := &hashCounter{h: sha256.New()}
	if err := fn(io.MultiWriter(ew, hc)); err != nil {
		return err
	}
	w.entries[name] = EntrySum{SHA256: hex.EncodeToString(hc.h.Sum(nil)), Bytes: hc.n}
	return nil
}

// finish writes the manifest last and closes the zip.
func (w *archiveWriter) finish(m *Manifest) error {
	m.Entries = w.entries
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	mw, err := w.zw.CreateHeader(&zip.FileHeader{Name: manifestName, Method: zip.Deflate, Modified: w.created})
	if err != nil {
		return err
	}
	if _, err := mw.Write(b); err != nil {
		return err
	}
	return w.zw.Close()
}

// archiveReader opens an archive and checks its structure. Entry contents are
// verified as they are read.
type archiveReader struct {
	zr    *zip.ReadCloser
	files map[string]*zip.File
	M     Manifest
}

func incompatible(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrIncompatible, fmt.Sprintf(format, a...))
}

func openArchive(path string) (*archiveReader, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, incompatible("not a MangaVeyra backup (%v)", err)
	}
	a := &archiveReader{zr: zr, files: map[string]*zip.File{}}
	fail := func(err error) (*archiveReader, error) {
		_ = zr.Close()
		return nil, err
	}
	for _, f := range zr.File {
		if _, dup := a.files[f.Name]; dup {
			return fail(incompatible("duplicate entry %q", f.Name))
		}
		a.files[f.Name] = f
	}
	mf := a.files[manifestName]
	if mf == nil {
		return fail(incompatible("the manifest is missing"))
	}
	if mf.UncompressedSize64 > maxManifestBytes {
		return fail(incompatible("the manifest is too large"))
	}
	rc, err := mf.Open()
	if err != nil {
		return fail(incompatible("the manifest cannot be read"))
	}
	b, err := io.ReadAll(io.LimitReader(rc, maxManifestBytes+1))
	_ = rc.Close()
	if err != nil || len(b) > maxManifestBytes {
		return fail(incompatible("the manifest cannot be read"))
	}
	if err := json.Unmarshal(b, &a.M); err != nil {
		return fail(incompatible("the manifest is damaged"))
	}
	if a.M.Format != formatV1 {
		return fail(incompatible("unknown archive format %q", a.M.Format))
	}
	for name := range a.files {
		if name == manifestName {
			continue
		}
		if _, ok := a.M.Entries[name]; !ok {
			return fail(incompatible("entry %q is not in the manifest", name))
		}
	}
	for name, sum := range a.M.Entries {
		f := a.files[name]
		if f == nil {
			return fail(incompatible("entry %q is missing", name))
		}
		if sum.Bytes < 0 || uint64(sum.Bytes) != f.UncompressedSize64 {
			return fail(incompatible("entry %q has the wrong size", name))
		}
		if strings.HasPrefix(name, mediaPrefix) {
			if err := storage.ValidateKey(strings.TrimPrefix(name, mediaPrefix)); err != nil {
				return fail(incompatible("entry %q is not a media key", name))
			}
		} else if !strings.HasPrefix(name, dbPrefix) {
			return fail(incompatible("unexpected entry %q", name))
		}
	}
	seen := map[string]bool{}
	for _, t := range a.M.Tables {
		if !identPattern.MatchString(t.Name) || seen[t.Name] || t.File != dbPrefix+t.Name+".jsonl" {
			return fail(incompatible("table entry %q is invalid", t.Name))
		}
		seen[t.Name] = true
		if _, ok := a.M.Entries[t.File]; !ok {
			return fail(incompatible("table file %q is missing", t.File))
		}
		for _, c := range t.Columns {
			if !identPattern.MatchString(c.Name) {
				return fail(incompatible("column %s.%q is invalid", t.Name, c.Name))
			}
		}
	}
	return a, nil
}

func (a *archiveReader) Close() error { return a.zr.Close() }

// mediaKeys lists the media entries' storage keys.
func (a *archiveReader) mediaKeys() []string {
	var keys []string
	for name := range a.M.Entries {
		if strings.HasPrefix(name, mediaPrefix) {
			keys = append(keys, strings.TrimPrefix(name, mediaPrefix))
		}
	}
	return keys
}

// open returns a reader that fails with ErrIncompatible at the end of the
// entry when its size or SHA-256 differs from the manifest.
func (a *archiveReader) open(name string) (io.ReadCloser, EntrySum, error) {
	sum, ok := a.M.Entries[name]
	f := a.files[name]
	if !ok || f == nil {
		return nil, EntrySum{}, incompatible("entry %q is missing", name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, sum, incompatible("entry %q cannot be read", name)
	}
	return &verifyingReader{name: name, rc: rc, r: io.LimitReader(rc, sum.Bytes+1), want: sum, h: sha256.New()}, sum, nil
}

// verify reads a whole entry and checks it.
func (a *archiveReader) verify(name string) error {
	rc, _, err := a.open(name)
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.Copy(io.Discard, rc)
	return err
}

type verifyingReader struct {
	name string
	rc   io.ReadCloser
	r    io.Reader
	want EntrySum
	h    hash.Hash
	n    int64
}

func (v *verifyingReader) Read(p []byte) (int, error) {
	n, err := v.r.Read(p)
	v.n += int64(n)
	v.h.Write(p[:n])
	if v.n > v.want.Bytes {
		return n, incompatible("entry %q is longer than recorded", v.name)
	}
	if errors.Is(err, io.EOF) {
		if v.n != v.want.Bytes || hex.EncodeToString(v.h.Sum(nil)) != v.want.SHA256 {
			return n, incompatible("entry %q does not match its checksum", v.name)
		}
	} else if err != nil {
		return n, incompatible("entry %q is damaged (%v)", v.name, err)
	}
	return n, err
}

func (v *verifyingReader) Close() error { return v.rc.Close() }
