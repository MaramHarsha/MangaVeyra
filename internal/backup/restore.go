package backup

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/secrets"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
)

// RestoreReport describes a finished restore (G10 notice, CLI output, audit log).
type RestoreReport struct {
	BackupID   string   `json:"backup_id"`
	ArchiveID  string   `json:"archive_id"`
	Kind       string   `json:"kind"`
	FromSchema string   `json:"from_schema"`
	Dialect    string   `json:"dialect"`
	Tables     int      `json:"tables"`
	Rows       int64    `json:"rows"`
	Media      int      `json:"media"`
	Migrated   []string `json:"migrated,omitempty"`
	// CanceledJobs counts jobs that were queued or running in the archive.
	CanceledJobs int       `json:"canceled_jobs,omitempty"`
	At           time.Time `json:"at"`
	ActorID      string    `json:"actor_id,omitempty"`
	// MasterKeyDiffers is true when a sealed value in the restored data cannot
	// be opened with this server's master key.
	MasterKeyDiffers bool `json:"master_key_differs"`
	// Sealed counts encrypted values by kind: provider_keys, totp, world, push.
	Sealed map[string]int `json:"sealed,omitempty"`
}

// RestoreState is the progress of a restore started from G10.
type RestoreState struct {
	Running  bool           `json:"running"`
	BackupID string         `json:"backup_id,omitempty"`
	Progress float64        `json:"progress"`
	Err      error          `json:"-"`
	Report   *RestoreReport `json:"report,omitempty"`
}

// preservedKeys are instance settings a restore keeps from the running
// server: maintenance mode stays on until the caller turns it off, and the
// backup module's own state (lock, schedule, slot, presence) describes this
// machine, not the archive.
func preservedSetting(key string) bool {
	return key == "maintenance" || strings.HasPrefix(key, "backup.")
}

// Restore implements Service.
func (s *service) Restore(ctx context.Context, id, actorID string) error {
	_, err := s.restoreArchive(ctx, id, actorID)
	return err
}

// State returns the progress of the restore started in this process.
func (s *service) State() RestoreState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restore
}

func (s *service) setState(fn func(*RestoreState)) {
	s.mu.Lock()
	fn(&s.restore)
	s.mu.Unlock()
}

func (s *service) restoreArchive(ctx context.Context, id, actorID string) (*RestoreReport, error) {
	r, err := s.row(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	if r.Kind == KindUserExport {
		return nil, incompatible("a member export is not a server backup")
	}
	if r.Status != StatusDone && r.Status != StatusRestored || r.Path == "" {
		return nil, incompatible("the archive is %s", r.Status)
	}
	s.d.Settings.Invalidate()
	inst, err := s.d.Settings.Instance(ctx)
	if err != nil {
		return nil, err
	}
	if !inst.Maintenance.Enabled {
		return nil, ErrNotInMaintenance
	}
	release, err := s.acquire(ctx, "restore")
	if err != nil {
		return nil, err
	}
	defer release()
	prog := progressFrom(ctx)
	if r.SHA256 != "" {
		sum, err := fileSHA256(r.Path)
		if err != nil {
			return nil, incompatible("the archive cannot be read")
		}
		if sum != r.SHA256 {
			return nil, incompatible("the archive changed after it was written")
		}
	}
	a, err := openArchive(r.Path)
	if err != nil {
		return nil, err
	}
	defer a.Close()
	m := a.M
	if m.Kind == KindUserExport || !m.Includes.Database {
		return nil, incompatible("the archive holds no server database")
	}
	known, err := db.LoadMigrations(db.Migrations(), s.db.Dialect())
	if err != nil {
		return nil, err
	}
	var current string
	if err := s.db.Get(ctx, &current, `SELECT COALESCE(MAX(version), '') FROM schema_migrations`); err != nil {
		return nil, err
	}
	if m.Schema > current {
		return nil, incompatible("the archive comes from a newer version (schema %s, this server %s)", m.Schema, current)
	}
	found := false
	for _, mg := range known {
		if mg.Version == m.Schema {
			found = true
			break
		}
	}
	if !found {
		return nil, incompatible("unknown schema %q", m.Schema)
	}
	// Verify every table file before anything changes.
	for _, t := range m.Tables {
		if err := a.verify(t.File); err != nil {
			return nil, err
		}
	}
	prog.report(0.05)
	// Media first: objects are only added (keys are unique), so a failure
	// here leaves the server's data as it was.
	mediaKeys := a.mediaKeys()
	sort.Strings(mediaKeys)
	if m.Includes.Media {
		for i, key := range mediaKeys {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if err := s.restoreObject(ctx, a, key); err != nil {
				return nil, err
			}
			prog.report(0.05 + 0.5*float64(i+1)/float64(len(mediaKeys)))
		}
	}
	rep := &RestoreReport{BackupID: id, ArchiveID: m.ID, Kind: m.Kind, FromSchema: m.Schema, Dialect: m.Dialect,
		Media: len(mediaKeys), At: s.now(), ActorID: actorID}
	err = s.db.WithTx(ctx, func(tx *db.Tx) error {
		return s.replace(ctx, tx, a, current, rep, prog)
	})
	if err != nil {
		if !errors.Is(err, ErrIncompatible) && !errors.Is(err, context.Canceled) {
			err = fmt.Errorf("backup: restore failed, nothing was changed: %w", err)
		}
		return nil, err
	}
	s.d.Settings.Invalidate()
	if m.Includes.Media {
		s.removeOrphans(ctx, mediaKeys)
	}
	prog.report(1)
	return rep, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *service) restoreObject(ctx context.Context, a *archiveReader, key string) error {
	name := mediaPrefix + key
	sum := a.M.Entries[name]
	if info, err := s.store.Stat(ctx, key); err == nil && info.Size == sum.Bytes {
		// Keys are unique per stored object: the same key and size is the same file.
		return a.verify(name)
	}
	rc, _, err := a.open(name)
	if err != nil {
		return err
	}
	defer rc.Close()
	if err := s.store.Put(ctx, key, rc, sum.Bytes, storage.ContentTypeFor(key)); err != nil {
		if errors.Is(err, ErrIncompatible) {
			return incompatible("media %s does not match its checksum", key)
		}
		return fmt.Errorf("backup: write media %s: %w", key, err)
	}
	return nil
}

// removeOrphans deletes stored objects the restored data does not know.
func (s *service) removeOrphans(ctx context.Context, keep []string) {
	want := make(map[string]bool, len(keep))
	for _, k := range keep {
		want[k] = true
	}
	list, err := s.store.List(ctx, "")
	if err != nil {
		s.log.Warn("backup: list media after restore", "err", err)
		return
	}
	for _, o := range list {
		if !want[o.Key] {
			if err := s.store.Delete(ctx, o.Key); err != nil {
				s.log.Warn("backup: remove media after restore", "key", o.Key, "err", err)
			}
		}
	}
}

// preserved holds rows a restore keeps from the running server.
type preserved struct {
	backups  []map[string]any
	settings []map[string]any
	sessions []map[string]any
}

func snapshot(ctx context.Context, tx *db.Tx, query string, args ...any) ([]map[string]any, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		m := map[string]any{}
		if err := rows.MapScan(m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// replace swaps the database contents inside tx. With an archive from the
// current schema it deletes and reloads every table; with an older archive it
// recreates the schema at the archive's version, loads it and runs the newer
// migrations, all in the same transaction.
func (s *service) replace(ctx context.Context, tx *db.Tx, a *archiveReader, current string, rep *RestoreReport, prog *progress) error {
	cur, err := loadCatalog(ctx, tx)
	if err != nil {
		return err
	}
	var keep preserved
	if keep.backups, err = snapshot(ctx, tx, `SELECT * FROM backups`); err != nil {
		return err
	}
	if keep.settings, err = snapshot(ctx, tx, `SELECT * FROM instance_settings WHERE key = 'maintenance' OR key LIKE 'backup.%'`); err != nil {
		return err
	}
	if rep.ActorID != "" {
		if keep.sessions, err = snapshot(ctx, tx, `SELECT * FROM sessions WHERE user_id = ?`, rep.ActorID); err != nil {
			return err
		}
	}
	m := a.M
	target := cur
	if m.Schema == current {
		for i := len(cur.Order) - 1; i >= 0; i-- {
			name := cur.Order[i]
			if name == metaTable {
				continue
			}
			if _, err := tx.Exec(ctx, `DELETE FROM `+quote(name)); err != nil {
				return fmt.Errorf("clear %s: %w", name, err)
			}
		}
	} else {
		if err := dropAll(ctx, tx, cur); err != nil {
			return err
		}
		if _, err := migrateTx(ctx, tx, m.Schema); err != nil {
			return err
		}
		if target, err = loadCatalog(ctx, tx); err != nil {
			return err
		}
	}
	prog.report(0.6)
	archived := map[string]TableEntry{}
	for _, t := range m.Tables {
		archived[t.Name] = t
	}
	type pending struct {
		table, col string
		pk, value  any
	}
	var later []pending
	for i, name := range target.Order {
		if name == metaTable || name == "backups" {
			continue
		}
		te, ok := archived[name]
		if !ok {
			continue
		}
		t := target.Tables[name]
		deferred := map[string]bool{}
		for _, c := range target.Deferred[name] {
			deferred[c] = true
		}
		var cols []column
		for _, ac := range te.Columns {
			if tc, ok := t.column(ac.Name); ok {
				cols = append(cols, tc)
			}
		}
		if len(cols) == 0 {
			continue
		}
		ins := newInserter(tx, name, cols)
		rc, _, err := a.open(te.File)
		if err != nil {
			return err
		}
		dec := json.NewDecoder(bufio.NewReaderSize(rc, 1<<20))
		dec.UseNumber()
		var n int64
		for {
			var row map[string]any
			if err := dec.Decode(&row); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				_ = rc.Close()
				if errors.Is(err, ErrIncompatible) {
					return err
				}
				return incompatible("table %s is damaged (%v)", name, err)
			}
			if name == "instance_settings" {
				if k, _ := row["key"].(string); preservedSetting(k) {
					continue
				}
			}
			vals := make([]any, len(cols))
			for j, c := range cols {
				v, err := decodeValue(row[c.Name], c.Type)
				if err != nil {
					_ = rc.Close()
					return incompatible("%s.%s: %v", name, c.Name, err)
				}
				if deferred[c.Name] && v != nil {
					pk, err := decodeValue(row[t.PK[0]], typeText)
					if err != nil {
						_ = rc.Close()
						return incompatible("%s primary key: %v", name, err)
					}
					later = append(later, pending{table: name, col: c.Name, pk: pk, value: v})
					v = nil
				}
				vals[j] = v
			}
			if err := ins.add(ctx, vals); err != nil {
				_ = rc.Close()
				return fmt.Errorf("load %s: %w", name, err)
			}
			n++
		}
		_ = rc.Close()
		if err := ins.flush(ctx); err != nil {
			return fmt.Errorf("load %s: %w", name, err)
		}
		rep.Tables++
		rep.Rows += n
		prog.report(0.6 + 0.3*float64(i+1)/float64(len(target.Order)))
	}
	for _, p := range later {
		pk := target.Tables[p.table].PK[0]
		if _, err := tx.Exec(ctx, `UPDATE `+quote(p.table)+` SET `+quote(p.col)+` = ? WHERE `+quote(pk)+` = ?`, p.value, p.pk); err != nil {
			return fmt.Errorf("link %s.%s: %w", p.table, p.col, err)
		}
	}
	if m.Schema != current {
		applied, err := migrateTx(ctx, tx, "")
		if err != nil {
			return err
		}
		rep.Migrated = applied
		if target, err = loadCatalog(ctx, tx); err != nil {
			return err
		}
	}
	// Work that was waiting or running when the archive was written is
	// canceled: a restore never starts generation (or spend) by itself.
	now := s.now()
	res, err := tx.Exec(ctx, `UPDATE jobs SET state = 'canceled', cancel_requested = TRUE, lease_expires_at = NULL, worker_id = NULL,
		error = ?, finished_at = ?, updated_at = ? WHERE state IN ('queued', 'running')`,
		`{"code":"canceled","message":"canceled by a restore","retryable":false}`, now, now)
	if err != nil {
		return fmt.Errorf("cancel restored jobs: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		rep.CanceledJobs = int(n)
	}
	if err := restorePreserved(ctx, tx, target, keep, now); err != nil {
		return err
	}
	rep.Sealed, rep.MasterKeyDiffers = checkSealed(ctx, tx, s.d.Secrets)
	if _, err := tx.Exec(ctx, `UPDATE backups SET status = 'restored' WHERE id = ?`, rep.BackupID); err != nil {
		return err
	}
	var actor any
	var n int
	if rep.ActorID != "" {
		if err := tx.Get(ctx, &n, `SELECT COUNT(*) FROM users WHERE id = ?`, rep.ActorID); err != nil {
			return err
		}
		if n > 0 {
			actor = rep.ActorID
		}
	}
	data, _ := json.Marshal(rep)
	kind := "system"
	if actor != nil {
		kind = "user"
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_log (id, actor_id, actor_kind, action, target_kind, target_id, data, created_at)
		VALUES (?, ?, ?, 'backup.restore', 'backup', ?, ?, ?)`, db.NewID(), actor, kind, rep.BackupID, string(data), rep.At)
	return err
}

// inserter batches multi-row INSERT statements.
type inserter struct {
	tx    *db.Tx
	table string
	cols  []column
	rows  [][]any
	max   int
}

func newInserter(tx *db.Tx, table string, cols []column) *inserter {
	per := 4000 / len(cols)
	per = max(1, min(per, 200))
	return &inserter{tx: tx, table: table, cols: cols, max: per}
}

func (in *inserter) add(ctx context.Context, vals []any) error {
	in.rows = append(in.rows, vals)
	if len(in.rows) >= in.max {
		return in.flush(ctx)
	}
	return nil
}

func (in *inserter) flush(ctx context.Context) error {
	if len(in.rows) == 0 {
		return nil
	}
	names := make([]string, len(in.cols))
	for i, c := range in.cols {
		names[i] = quote(c.Name)
	}
	ph := "(" + strings.TrimSuffix(strings.Repeat("?, ", len(in.cols)), ", ") + ")"
	var b strings.Builder
	b.WriteString(`INSERT INTO ` + quote(in.table) + ` (` + strings.Join(names, ", ") + `) VALUES `)
	args := make([]any, 0, len(in.rows)*len(in.cols))
	for i, r := range in.rows {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(ph)
		args = append(args, r...)
	}
	in.rows = in.rows[:0]
	_, err := in.tx.Exec(ctx, b.String(), args...)
	return err
}

// dropAll removes every table (and SQLite's FTS index) before an older schema is rebuilt.
func dropAll(ctx context.Context, tx *db.Tx, c *catalog) error {
	if tx.Dialect() == db.Postgres {
		for _, name := range c.Order {
			if _, err := tx.Exec(ctx, `DROP TABLE IF EXISTS `+quote(name)+` CASCADE`); err != nil {
				return fmt.Errorf("drop %s: %w", name, err)
			}
		}
		return nil
	}
	for _, v := range c.Virtual {
		if !identPattern.MatchString(v) {
			continue
		}
		if _, err := tx.Exec(ctx, `DROP TABLE IF EXISTS `+quote(v)); err != nil {
			return fmt.Errorf("drop %s: %w", v, err)
		}
	}
	for i := len(c.Order) - 1; i >= 0; i-- {
		if _, err := tx.Exec(ctx, `DROP TABLE IF EXISTS `+quote(c.Order[i])); err != nil {
			return fmt.Errorf("drop %s: %w", c.Order[i], err)
		}
	}
	return nil
}

// migrationsDDL matches the migration runner's bookkeeping table.
const migrationsDDL = `CREATE TABLE IF NOT EXISTS schema_migrations (
	version TEXT PRIMARY KEY,
	file TEXT NOT NULL,
	applied_at TIMESTAMP NOT NULL
)`

// migrateTx applies embedded migrations up to and including upTo ("" = all)
// inside tx, so a restore from an older schema stays one transaction.
func migrateTx(ctx context.Context, tx *db.Tx, upTo string) ([]string, error) {
	migs, err := db.LoadMigrations(db.Migrations(), tx.Dialect())
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, migrationsDDL); err != nil {
		return nil, err
	}
	var have []string
	if err := tx.Select(ctx, &have, `SELECT version FROM schema_migrations`); err != nil {
		return nil, err
	}
	applied := map[string]bool{}
	for _, v := range have {
		applied[v] = true
	}
	var done []string
	for _, mg := range migs {
		if upTo != "" && mg.Version > upTo {
			break
		}
		if applied[mg.Version] {
			continue
		}
		stmts, err := db.SplitStatements(mg.SQL)
		if err != nil {
			return done, fmt.Errorf("migration %s: %w", mg.File, err)
		}
		for i, st := range stmts {
			if _, err := tx.X.ExecContext(ctx, st); err != nil {
				return done, fmt.Errorf("migration %s statement %d: %w", mg.File, i+1, err)
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, file, applied_at) VALUES (?, ?, ?)`, mg.Version, mg.File, db.Now()); err != nil {
			return done, err
		}
		done = append(done, mg.Version)
	}
	return done, nil
}

// restorePreserved puts back the running server's backup records, its
// maintenance and backup settings, and the restoring admin's sessions.
// References to members the archive does not have are cleared.
func restorePreserved(ctx context.Context, tx *db.Tx, c *catalog, keep preserved, now time.Time) error {
	userExists := map[string]bool{}
	exists := func(id any) bool {
		s, ok := id.(string)
		if !ok {
			if b, isBytes := id.([]byte); isBytes {
				s = string(b)
			} else {
				return false
			}
		}
		if v, ok := userExists[s]; ok {
			return v
		}
		var n int
		_ = tx.Get(ctx, &n, `SELECT COUNT(*) FROM users WHERE id = ?`, s)
		userExists[s] = n > 0
		return n > 0
	}
	if _, err := tx.Exec(ctx, `DELETE FROM backups`); err != nil {
		return err
	}
	for _, r := range keep.backups {
		for _, col := range []string{"user_id", "created_by"} {
			if v, ok := r[col]; ok && v != nil && !exists(v) {
				r[col] = nil
			}
		}
		if err := insertRaw(ctx, tx, c.Tables["backups"], r, false); err != nil {
			return fmt.Errorf("keep backups: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM instance_settings WHERE key = 'maintenance' OR key LIKE 'backup.%'`); err != nil {
		return err
	}
	for _, r := range keep.settings {
		if v, ok := r["updated_by"]; ok && v != nil && !exists(v) {
			r["updated_by"] = nil
		}
		if k, _ := r["key"].(string); k == lockKey {
			r["updated_at"] = now
		}
		if err := insertRaw(ctx, tx, c.Tables["instance_settings"], r, false); err != nil {
			return fmt.Errorf("keep settings: %w", err)
		}
	}
	for _, r := range keep.sessions {
		if !exists(r["user_id"]) {
			continue
		}
		if err := insertRaw(ctx, tx, c.Tables["sessions"], r, true); err != nil {
			return fmt.Errorf("keep session: %w", err)
		}
	}
	return nil
}

func insertRaw(ctx context.Context, tx *db.Tx, t *table, row map[string]any, ignoreConflict bool) error {
	if t == nil {
		return nil
	}
	var names []string
	var args []any
	for _, c := range t.Columns {
		v, ok := row[c.Name]
		if !ok {
			continue
		}
		names = append(names, quote(c.Name))
		if b, isBytes := v.([]byte); isBytes && c.Type != typeBlob {
			v = string(b)
		}
		args = append(args, v)
	}
	q := `INSERT INTO ` + quote(t.Name) + ` (` + strings.Join(names, ", ") + `) VALUES (` +
		strings.TrimSuffix(strings.Repeat("?, ", len(names)), ", ") + `)`
	if ignoreConflict {
		q += ` ON CONFLICT DO NOTHING`
	}
	_, err := tx.Exec(ctx, q, args...)
	return err
}

// checkSealed counts encrypted values in the restored data and tries to open
// one of each kind with this server's master key. Values are never logged.
func checkSealed(ctx context.Context, tx *db.Tx, box *secrets.Box) (map[string]int, bool) {
	counts := map[string]int{}
	differs := false
	try := func(kind, query, aadPrefix, aadSuffix string) {
		var rows []struct {
			ID     string `db:"id"`
			Sealed string `db:"sealed"`
		}
		if err := tx.Select(ctx, &rows, query); err != nil {
			return
		}
		counts[kind] = len(rows)
		if len(rows) > 0 && box != nil && aadPrefix != "" {
			if _, err := box.OpenString(rows[0].Sealed, aadPrefix+rows[0].ID+aadSuffix); err != nil {
				differs = true
			}
		}
	}
	try("provider_keys", `SELECT id, key_enc AS sealed FROM providers WHERE key_enc <> ''`, "provider:", ":key")
	try("totp", `SELECT id, totp_secret_enc AS sealed FROM users WHERE totp_secret_enc <> ''`, "user:", ":totp")
	try("world", `SELECT id, token_enc AS sealed FROM world_links WHERE token_enc <> ''`, "", "")
	var vapid string
	if err := tx.Get(ctx, &vapid, `SELECT value FROM instance_settings WHERE key = 'notify.vapid'`); err == nil {
		var st struct {
			PrivateEnc string `json:"private_enc"`
		}
		if json.Unmarshal([]byte(vapid), &st) == nil && st.PrivateEnc != "" {
			counts["push"] = 1
			if box != nil {
				if _, err := box.OpenString(st.PrivateEnc, "notify.vapid"); err != nil {
					differs = true
				}
			}
		}
	}
	for k, v := range counts {
		if v == 0 {
			delete(counts, k)
		}
	}
	return counts, differs
}
