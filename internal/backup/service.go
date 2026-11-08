package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
)

// Errors beside the frozen contract's.
var (
	// ErrNotInMaintenance refuses a restore while members can still change data.
	ErrNotInMaintenance = errors.New("backup: turn maintenance mode on before restoring")
	// ErrInvalid rejects a malformed request (unknown kind, missing member).
	ErrInvalid = errors.New("backup: invalid request")
	// ErrDestination means the backup directory cannot be used.
	ErrDestination = errors.New("backup: the destination directory is not writable")
)

// Default lifetimes when Request.KeepFor is zero (ADR 0013 §4, §6).
const (
	preUpdateKeep  = 7 * 24 * time.Hour
	userExportKeep = 7 * 24 * time.Hour
)

// lockKey is the cross-process lease that makes backups and restores
// exclusive; it lives in instance_settings under this module's namespace.
const (
	lockKey   = "backup.lock"
	lockStale = 90 * time.Second
	lockBeat  = 20 * time.Second
)

// service implements Service.
type service struct {
	d     *core.Deps
	db    *db.DB
	store storage.Store
	log   *slog.Logger
	now   func() time.Time

	busy atomic.Bool
	// exporting holds the members whose export this process is writing.
	exporting sync.Map

	mu      sync.Mutex
	restore RestoreState
}

func newService(d *core.Deps) *service {
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	return &service{d: d, db: d.DB, store: d.Store, log: log, now: func() time.Time { return db.Now() }}
}

type backupRow struct {
	ID         string         `db:"id"`
	Kind       string         `db:"kind"`
	Status     string         `db:"status"`
	Path       string         `db:"path"`
	Bytes      int64          `db:"bytes"`
	SHA256     string         `db:"sha256"`
	Includes   string         `db:"includes"`
	AppVersion string         `db:"app_version"`
	Schema     string         `db:"schema"`
	UserID     sql.NullString `db:"user_id"`
	CreatedBy  sql.NullString `db:"created_by"`
	Error      string         `db:"error"`
	CreatedAt  time.Time      `db:"created_at"`
	FinishedAt sql.NullTime   `db:"finished_at"`
	ExpiresAt  sql.NullTime   `db:"expires_at"`
}

const backupCols = `id, kind, status, path, bytes, sha256, includes, app_version, schema, user_id, created_by, error, created_at, finished_at, expires_at`

func (r backupRow) backup() Backup {
	b := Backup{ID: r.ID, Kind: r.Kind, Status: r.Status, Bytes: r.Bytes, SHA256: r.SHA256, AppVersion: r.AppVersion,
		Schema: r.Schema, UserID: r.UserID.String, CreatedBy: r.CreatedBy.String, Error: r.Error, CreatedAt: r.CreatedAt.UTC()}
	if r.FinishedAt.Valid {
		t := r.FinishedAt.Time.UTC()
		b.FinishedAt = &t
	}
	if r.ExpiresAt.Valid {
		t := r.ExpiresAt.Time.UTC()
		b.ExpiresAt = &t
	}
	return b
}

func (r backupRow) includes() Includes {
	var inc Includes
	_ = json.Unmarshal([]byte(r.Includes), &inc)
	return inc
}

// ownsFile reports whether the record's file is one this module wrote (or
// moved into its backup directory) and may delete: it carries the name the
// module gives an archive of this record's id, and it is not an archive the
// admin registered where it lies.
func (r backupRow) ownsFile() bool {
	if r.Path == "" || !filepath.IsAbs(r.Path) || r.includes().External {
		return false
	}
	base := filepath.Base(r.Path)
	return strings.HasPrefix(base, "mangaveyra-") && strings.HasSuffix(base, "-"+r.ID+Extension)
}

func validKind(k string) bool {
	switch k {
	case KindScheduled, KindManual, KindPreUpdate, KindExport, KindUserExport:
		return true
	}
	return false
}

// Get implements Service.
func (s *service) Get(ctx context.Context, id string) (Backup, error) {
	r, err := s.row(ctx, s.db, id)
	if err != nil {
		return Backup{}, err
	}
	return r.backup(), nil
}

func (s *service) row(ctx context.Context, q db.Querier, id string) (backupRow, error) {
	var r backupRow
	if !db.ValidID(id) {
		return r, ErrNotFound
	}
	if err := q.Get(ctx, &r, `SELECT `+backupCols+` FROM backups WHERE id = ?`, id); err != nil {
		if db.IsNotFound(err) {
			return r, ErrNotFound
		}
		return r, err
	}
	return r, nil
}

// List implements Service: newest first, deleted archives left out; kind ""
// lists every kind.
func (s *service) List(ctx context.Context, kind string, limit int) ([]Backup, error) {
	rows, err := s.listRows(ctx, kind, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Backup, len(rows))
	for i, r := range rows {
		out[i] = r.backup()
	}
	return out, nil
}

func (s *service) listRows(ctx context.Context, kind string, limit int) ([]backupRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := `SELECT ` + backupCols + ` FROM backups WHERE status <> 'deleted'`
	var args []any
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	var rows []backupRow
	if err := s.db.Select(ctx, &rows, q, args...); err != nil {
		return nil, err
	}
	return rows, nil
}

// Path returns the archive file of a finished backup (downloads).
func (s *service) Path(ctx context.Context, id string) (string, Backup, error) {
	r, err := s.row(ctx, s.db, id)
	if err != nil {
		return "", Backup{}, err
	}
	if r.Status != StatusDone && r.Status != StatusRestored || r.Path == "" {
		return "", Backup{}, ErrNotFound
	}
	return r.Path, r.backup(), nil
}

// Delete implements Service.
func (s *service) Delete(ctx context.Context, id string) error {
	r, err := s.row(ctx, s.db, id)
	if err != nil {
		return err
	}
	switch r.Status {
	case StatusDeleted:
		return nil
	case StatusRunning:
		return ErrBusy
	}
	if r.ownsFile() {
		if err := os.Remove(r.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("backup: delete archive: %w", err)
		}
	}
	_, err = s.db.Exec(ctx, `UPDATE backups SET status = 'deleted', finished_at = COALESCE(finished_at, ?) WHERE id = ?`, s.now(), id)
	return err
}

// acquire takes the exclusive backup/restore lease: an in-process flag and a
// row in instance_settings that other processes sharing the database honour.
func (s *service) acquire(ctx context.Context, what string) (func(), error) {
	if !s.busy.CompareAndSwap(false, true) {
		return nil, ErrBusy
	}
	holder := db.MustJSON(map[string]string{"holder": db.NewID(), "what": what})
	now := s.now()
	res, err := s.db.Exec(ctx, `INSERT INTO instance_settings (key, value, updated_by, updated_at) VALUES (?, ?, NULL, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
		WHERE instance_settings.updated_at < ?`, lockKey, holder, now, now.Add(-lockStale))
	if err != nil {
		s.busy.Store(false)
		return nil, fmt.Errorf("backup: take the backup lock: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		s.busy.Store(false)
		return nil, ErrBusy
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(lockBeat)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				_, _ = s.db.Exec(bctx, `UPDATE instance_settings SET updated_at = ? WHERE key = ? AND value = ?`, s.now(), lockKey, holder)
				cancel()
			}
		}
	}()
	return func() {
		close(stop)
		<-done
		bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, _ = s.db.Exec(bctx, `DELETE FROM instance_settings WHERE key = ? AND value = ?`, lockKey, holder)
		s.busy.Store(false)
	}, nil
}

// Create implements Service.
func (s *service) Create(ctx context.Context, req Request) (Backup, error) {
	if !validKind(req.Kind) || (req.Kind == KindUserExport) != (req.UserID != "") || req.KeepFor < 0 {
		return Backup{}, ErrInvalid
	}
	if req.UserID != "" {
		var n int
		if err := s.db.Get(ctx, &n, `SELECT COUNT(*) FROM users WHERE id = ?`, req.UserID); err != nil {
			return Backup{}, err
		}
		if n == 0 {
			return Backup{}, ErrNotFound
		}
	}
	export := req.Kind == KindUserExport
	if !export {
		// Member exports read one database snapshot and never take the
		// server-wide lease (see MemberExport); every other kind does.
		release, err := s.acquire(ctx, "create")
		if err != nil {
			return Backup{}, err
		}
		defer release()
	}
	dir, err := s.destination(ctx)
	if err != nil {
		return Backup{}, err
	}
	now := s.now()
	id := db.NewID()
	var expires *time.Time
	keep := req.KeepFor
	if keep == 0 {
		switch req.Kind {
		case KindPreUpdate:
			keep = preUpdateKeep
		case KindUserExport:
			keep = userExportKeep
		}
	}
	if keep > 0 {
		t := now.Add(keep)
		expires = &t
	}
	version := s.d.Version.Version
	if export {
		if err := s.startExport(ctx, id, req, now, expires); err != nil {
			return Backup{}, err
		}
	} else if _, err := s.db.Exec(ctx, `INSERT INTO backups (id, kind, status, app_version, user_id, created_by, created_at, expires_at)
		VALUES (?, ?, 'running', ?, ?, ?, ?, ?)`, id, req.Kind, version, db.NullString(req.UserID), s.userOrNull(ctx, req.CreatedBy), now, expires); err != nil {
		return Backup{}, fmt.Errorf("backup: record: %w", err)
	}
	ok := false
	if export {
		defer func() { s.finishExport(context.WithoutCancel(ctx), req.UserID, id, ok) }()
	}
	final := filepath.Join(dir, fileName(req.Kind, now, id))
	m, size, sum, err := s.write(ctx, dir, final, id, req, now)
	if err != nil {
		msg := err.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		_, _ = s.db.Exec(context.WithoutCancel(ctx), `UPDATE backups SET status = 'failed', error = ?, finished_at = ? WHERE id = ?`, msg, s.now(), id)
		return Backup{}, err
	}
	inc, _ := json.Marshal(m.Includes)
	if _, err := s.db.Exec(context.WithoutCancel(ctx), `UPDATE backups SET status = 'done', path = ?, bytes = ?, sha256 = ?, includes = ?,
		schema = ?, finished_at = ? WHERE id = ?`, final, size, sum, string(inc), m.Schema, s.now(), id); err != nil {
		_ = os.Remove(final)
		return Backup{}, fmt.Errorf("backup: record: %w", err)
	}
	ok = true
	if s.d.Auth != nil && !export {
		s.d.Auth.Audit(ctx, auth.AuditEntry{ActorID: req.CreatedBy, Action: "backup.create", TargetKind: "backup", TargetID: id,
			Data: map[string]any{"kind": req.Kind, "bytes": size}})
	}
	return s.Get(ctx, id)
}

func (s *service) userOrNull(ctx context.Context, id string) any {
	if id == "" {
		return nil
	}
	var n int
	if err := s.db.Get(ctx, &n, `SELECT COUNT(*) FROM users WHERE id = ?`, id); err != nil || n == 0 {
		return nil
	}
	return id
}

func fileName(kind string, t time.Time, id string) string {
	return "mangaveyra-" + strings.ReplaceAll(kind, "_", "-") + "-" + t.UTC().Format("20060102-150405") + "-" + id + Extension
}

// write streams the archive to a private temporary file and renames it into place.
func (s *service) write(ctx context.Context, dir, final, id string, req Request, now time.Time) (*Manifest, int64, string, error) {
	tmp := filepath.Join(dir, "."+id+Extension+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, 0, "", fmt.Errorf("%w: %v", ErrDestination, err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	h := sha256.New()
	cw := &hashCounter{h: h}
	aw := newArchiveWriter(io.MultiWriter(f, cw), now)
	m := &Manifest{Format: formatV1, ID: id, AppVersion: s.d.Version.Version, Dialect: string(s.db.Dialect()), Kind: req.Kind,
		UserID: req.UserID, CreatedAt: now}
	prog := progressFrom(ctx)
	var keys []storage.Info
	err = s.db.WithReadTx(ctx, func(tx *db.Tx) error {
		if err := tx.Get(ctx, &m.Schema, `SELECT COALESCE(MAX(version), '') FROM schema_migrations`); err != nil {
			return err
		}
		cat, err := loadCatalog(ctx, tx)
		if err != nil {
			return err
		}
		var scope *exportScope
		if req.Kind == KindUserExport {
			scope = userScope(req.UserID)
		}
		order := make([]string, 0, len(cat.Order))
		for _, name := range cat.Order {
			if name == metaTable {
				continue
			}
			if scope != nil && !scope.has(name) {
				continue
			}
			order = append(order, name)
		}
		dbShare := 0.2
		if req.Kind == KindPreUpdate {
			dbShare = 0.95
		}
		for i, name := range order {
			t := cat.Tables[name]
			where, args := "", []any(nil)
			if scope != nil {
				where, args = scope.where(name)
			}
			te := TableEntry{Name: name, File: dbPrefix + name + ".jsonl", Columns: t.Columns}
			if err := aw.add(te.File, false, func(w io.Writer) error {
				n, err := dumpTable(ctx, tx, t, where, args, scope.redact(name), w)
				te.Rows = n
				return err
			}); err != nil {
				return fmt.Errorf("backup: table %s: %w", name, err)
			}
			m.Tables = append(m.Tables, te)
			m.Includes.Rows += te.Rows
			prog.report(dbShare * float64(i+1) / float64(len(order)))
		}
		m.Includes.Database = true
		m.Includes.Tables = len(m.Tables)
		switch {
		case req.Kind == KindPreUpdate:
			// Updates never change media: the pre-update archive holds the database only, so it is fast.
		case scope != nil:
			keys, err = scope.mediaKeys(ctx, tx, s.store)
		default:
			// The store may be shared: only MangaVeyra's objects are archived.
			if keys, err = s.store.List(ctx, ""); err == nil {
				keys = appObjects(ctx, tx, keys)
			}
		}
		return err
	})
	if err != nil {
		return nil, 0, "", err
	}
	if req.Kind != KindPreUpdate {
		m.Includes.Media = true
		var total int64
		for _, k := range keys {
			total += k.Size
		}
		var done int64
		for _, k := range keys {
			if err := ctx.Err(); err != nil {
				return nil, 0, "", err
			}
			if storage.ValidateKey(k.Key) != nil {
				continue
			}
			obj, _, err := s.store.Get(ctx, k.Key)
			if errors.Is(err, storage.ErrNotFound) {
				m.Includes.MediaMissing++
				continue
			}
			if err != nil {
				return nil, 0, "", fmt.Errorf("backup: read media %s: %w", k.Key, err)
			}
			var n int64
			err = aw.add(mediaPrefix+k.Key, true, func(w io.Writer) error {
				var err error
				n, err = io.Copy(w, obj)
				return err
			})
			_ = obj.Close()
			if err != nil {
				return nil, 0, "", fmt.Errorf("backup: media %s: %w", k.Key, err)
			}
			m.Includes.MediaObjects++
			m.Includes.MediaBytes += n
			done += k.Size
			if total > 0 {
				prog.report(0.2 + 0.75*float64(done)/float64(total))
			}
		}
	}
	if err := aw.finish(m); err != nil {
		return nil, 0, "", err
	}
	if err := f.Sync(); err != nil {
		return nil, 0, "", err
	}
	if err := f.Close(); err != nil {
		return nil, 0, "", err
	}
	if err := os.Rename(tmp, final); err != nil {
		return nil, 0, "", fmt.Errorf("%w: %v", ErrDestination, err)
	}
	ok = true
	prog.report(1)
	return m, cw.n, hex.EncodeToString(h.Sum(nil)), nil
}

// dumpTable writes one JSON object per row, columns in table order.
func dumpTable(ctx context.Context, tx *db.Tx, t *table, where string, args []any, redact map[string]any, w io.Writer) (int64, error) {
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = quote(c.Name)
	}
	q := `SELECT ` + strings.Join(cols, ", ") + ` FROM ` + quote(t.Name)
	if where != "" {
		q += ` WHERE ` + where
	}
	if len(t.PK) > 0 {
		pk := make([]string, len(t.PK))
		for i, p := range t.PK {
			pk[i] = quote(p)
		}
		q += ` ORDER BY ` + strings.Join(pk, ", ")
	}
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var buf bytes.Buffer
	var n int64
	for rows.Next() {
		vals, err := rows.SliceScan()
		if err != nil {
			return n, err
		}
		buf.Reset()
		buf.WriteByte('{')
		for i, c := range t.Columns {
			v := vals[i]
			if r, ok := redact[c.Name]; ok {
				v = r
			}
			enc, err := encodeValue(v, c.Type)
			if err != nil {
				return n, fmt.Errorf("%s.%s: %w", t.Name, c.Name, err)
			}
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, _ := json.Marshal(c.Name)
			buf.Write(kb)
			buf.WriteByte(':')
			vb, err := json.Marshal(enc)
			if err != nil {
				return n, err
			}
			buf.Write(vb)
		}
		buf.WriteString("}\n")
		if _, err := w.Write(buf.Bytes()); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

// progress reporting: G10 starts backups as jobs and mirrors progress there.
type progressKey struct{}

// ProgressFunc receives progress between 0 and 1.
type ProgressFunc func(p float64)

// WithProgress makes Create and Restore report progress to fn.
func WithProgress(ctx context.Context, fn ProgressFunc) context.Context {
	return context.WithValue(ctx, progressKey{}, &progress{fn: fn})
}

type progress struct {
	fn   ProgressFunc
	mu   sync.Mutex
	last float64
	at   time.Time
}

func progressFrom(ctx context.Context) *progress {
	p, _ := ctx.Value(progressKey{}).(*progress)
	return p
}

func (p *progress) report(v float64) {
	if p == nil || p.fn == nil {
		return
	}
	p.mu.Lock()
	if v < 1 && v-p.last < 0.01 && time.Since(p.at) < time.Second {
		p.mu.Unlock()
		return
	}
	p.last, p.at = v, time.Now()
	p.mu.Unlock()
	p.fn(min(1, max(0, v)))
}
