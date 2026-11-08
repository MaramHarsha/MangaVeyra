package backup_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/backup"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/seed"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

const masterKeyHex = "abababababababababababababababababababababababababababababababab"

func forEach(t *testing.T, fn func(t *testing.T, ta *testutil.TestApp)) {
	for _, d := range testdb.Dialects() {
		t.Run(d, func(t *testing.T) { fn(t, testutil.NewTestApp(t, testutil.WithDialect(d))) })
	}
}

func svcOf(t *testing.T, ta *testutil.TestApp) *backup.TestService {
	t.Helper()
	s, ok := core.MustUse[backup.Service](ta.Deps).(*backup.TestService)
	if !ok {
		t.Fatal("backup.Service is not the module's service")
	}
	return s
}

// fixture is a seeded server: the original demo project plus rows that
// exercise every value type and a self-referencing foreign key.
type fixture struct {
	member      *auth.User
	childID     string
	parentID    string
	blob        []byte
	providerID  string
	totpSecret  string
	objectKey   string
	objectBytes []byte
}

func seedServer(t *testing.T, ta *testutil.TestApp) fixture {
	t.Helper()
	ctx := context.Background()
	if err := seed.Run(ctx, ta.Deps, seed.Options{Demo: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	f := fixture{member: ta.CreateUser(t, "rin", auth.RoleCreator)}
	d := ta.Deps.DB
	var projectID, pageID string
	if err := d.Get(ctx, &projectID, `SELECT id FROM projects ORDER BY created_at LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	if err := d.Get(ctx, &pageID, `SELECT id FROM pages ORDER BY number LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	// The reply's id sorts before its parent's, so a restore must defer the self reference.
	f.childID = db.NewID()
	f.parentID = db.NewID()
	now := db.Now()
	if _, err := d.Exec(ctx, `INSERT INTO comments (id, project_id, target_kind, target_id, body, author_id, created_at, updated_at)
		VALUES (?, ?, 'page', ?, 'Tidebreak quay looks right', ?, ?, ?)`, f.parentID, projectID, pageID, ta.Admin.ID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(ctx, `INSERT INTO comments (id, project_id, target_kind, target_id, body, author_id, parent_id, created_at, updated_at)
		VALUES (?, ?, 'page', ?, 'Agreed — keep the bell tower', ?, ?, ?, ?)`, f.childID, projectID, pageID, f.member.ID, f.parentID, now, now); err != nil {
		t.Fatal(err)
	}
	// Phase 4 data: a scoped collaborator, an assigned review comment and an agent action.
	if _, err := d.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role, scope, invited_by, created_at) VALUES (?, ?, 'letterer', ?, ?, ?)`,
		projectID, f.member.ID, `{"layers":["lettering"],"pages":{"from":1,"to":4}}`, ta.Admin.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(ctx, `UPDATE comments SET visibility = 'internal', status = 'needs_changes', assignee_id = ? WHERE id = ?`, f.member.ID, f.childID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(ctx, `INSERT INTO agent_actions (id, user_id, token_id, token_name, project_id, kind, action, status, detail, created_at, updated_at)
		VALUES (?, ?, 'tok', 'Script helper', ?, 'script_edit', 'script.edit', 'done', '{"lines":2}', ?, ?)`, db.NewID(), ta.Admin.ID, projectID, now, now); err != nil {
		t.Fatal(err)
	}
	f.blob = []byte{0, 1, 2, 255, 'Y', 'j', 's', 0}
	if _, err := d.Exec(ctx, `INSERT INTO page_doc_updates (id, page_id, kind, data, user_id, created_at) VALUES (?, ?, 'snapshot', ?, ?, ?)`,
		db.NewID(), pageID, f.blob, ta.Admin.ID, now); err != nil {
		t.Fatal(err)
	}
	f.providerID = db.NewID()
	if _, err := d.Exec(ctx, `INSERT INTO providers (id, kind, name, key_enc, key_hint, enabled, created_at, updated_at) VALUES (?, 'openai', 'Harbor key', ?, '…7f3a', TRUE, ?, ?)`,
		f.providerID, ta.Deps.Secrets.SealString("sk-test-harbor-7f3a", "provider:"+f.providerID+":key"), now, now); err != nil {
		t.Fatal(err)
	}
	f.totpSecret = "JBSWY3DPEHPK3PXP"
	if _, err := d.Exec(ctx, `UPDATE users SET totp_secret_enc = ?, totp_enabled = TRUE WHERE id = ?`,
		ta.Deps.Secrets.SealString(f.totpSecret, "user:"+f.member.ID+":totp"), f.member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ta.Deps.Settings.UpdateInstance(ctx, ta.Admin.ID, func(i *settings.Instance) error {
		i.Name = "Paper Crows Studio"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.objectKey = "assets/upload/loose-" + db.NewID() + ".bin"
	f.objectBytes = bytes.Repeat([]byte("Salt & Static "), 100)
	if err := storage.PutBytes(ctx, ta.Deps.Store, f.objectKey, f.objectBytes, "application/octet-stream"); err != nil {
		t.Fatal(err)
	}
	return f
}

// compareSkip are tables a restore deliberately keeps from the running
// server (backups) or appends to (audit_log), or that differ per login.
var compareSkip = []string{"backups", "sessions", "audit_log", "instance_settings"}

func fingerprints(t *testing.T, ta *testutil.TestApp) map[string][]string {
	t.Helper()
	fp, err := backup.Fingerprints(context.Background(), ta.Deps.DB, compareSkip, nil)
	if err != nil {
		t.Fatal(err)
	}
	settingsFP, err := backup.Fingerprints(context.Background(), ta.Deps.DB, nil,
		map[string]string{"instance_settings": `key <> 'maintenance' AND key NOT LIKE 'backup.%'`})
	if err != nil {
		t.Fatal(err)
	}
	fp["instance_settings"] = stripUpdatedBy(settingsFP["instance_settings"])
	return fp
}

// stripUpdatedBy drops instance_settings.updated_by, which a restore clears
// when the member is not in the archive.
func stripUpdatedBy(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		var m map[string]any
		_ = json.Unmarshal([]byte(l), &m)
		delete(m, "updated_by")
		b, _ := json.Marshal(m)
		out = append(out, string(b))
	}
	return out
}

func media(t *testing.T, ta *testutil.TestApp) map[string]string {
	t.Helper()
	ctx := context.Background()
	list, err := ta.Deps.Store.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, o := range list {
		b, err := storage.ReadAll(ctx, ta.Deps.Store, o.Key, 1<<30)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		out[o.Key] = hex.EncodeToString(sum[:])
	}
	return out
}

func maintenance(t *testing.T, ta *testutil.TestApp, on bool) {
	t.Helper()
	if _, err := ta.Deps.Settings.UpdateInstance(context.Background(), "", func(i *settings.Instance) error {
		i.Maintenance.Enabled = on
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func pathOf(t *testing.T, ta *testutil.TestApp, id string) string {
	t.Helper()
	var p string
	if err := ta.Deps.DB.Get(context.Background(), &p, `SELECT path FROM backups WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	return p
}

func newestSchema(t *testing.T, d *db.DB) string {
	t.Helper()
	var v string
	if err := d.Get(context.Background(), &v, `SELECT MAX(version) FROM schema_migrations`); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestArchiveContentsAndManifest(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f := seedServer(t, ta)
		s := svcOf(t, ta)
		start := time.Now()
		b, err := s.Create(ctx, backup.Request{Kind: backup.KindManual, CreatedBy: ta.Admin.ID})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s full backup of the seeded demo: %s, %d bytes", ta.Deps.DB.Dialect(), time.Since(start), b.Bytes)
		if b.Status != backup.StatusDone || b.Kind != backup.KindManual || b.Bytes == 0 || len(b.SHA256) != 64 || b.FinishedAt == nil {
			t.Fatalf("backup record %+v", b)
		}
		path := pathOf(t, ta, b.ID)
		if filepath.Dir(path) != ta.Deps.Cfg.BackupDir() || !strings.HasSuffix(path, backup.Extension) {
			t.Fatalf("archive path %s", path)
		}
		fi, err := os.Stat(path)
		if err != nil || fi.Mode().Perm() != 0o600 || fi.Size() != b.Bytes {
			t.Fatalf("archive file %v %v", fi, err)
		}
		m, err := backup.ReadManifest(path)
		if err != nil {
			t.Fatal(err)
		}
		if m.Format != "mangaveyra.backup.v1" || m.Schema != newestSchema(t, ta.Deps.DB) || m.Schema != b.Schema ||
			m.Dialect != string(ta.Deps.DB.Dialect()) || m.Kind != backup.KindManual || m.AppVersion != ta.Deps.Version.Version ||
			m.CreatedAt.IsZero() || !m.Includes.Database || !m.Includes.Media || m.Includes.Secrets {
			t.Fatalf("manifest %+v", m)
		}
		if err := backup.VerifyArchive(path); err != nil {
			t.Fatalf("verify: %v", err)
		}
		order, _, err := backup.TableOrder(ctx, ta.Deps.DB)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, te := range m.Tables {
			names = append(names, te.Name)
		}
		var want []string
		for _, n := range order {
			if n != "schema_migrations" {
				want = append(want, n)
			}
		}
		if !slices.Equal(names, want) {
			t.Fatalf("tables %v, want every table in FK order %v", names, want)
		}
		pos := map[string]int{}
		for i, n := range names {
			pos[n] = i
		}
		for _, pair := range [][2]string{{"users", "projects"}, {"projects", "chapters"}, {"chapters", "pages"}, {"pages", "panels"}, {"panels", "layers"}, {"assets", "layers"}} {
			if pos[pair[0]] >= pos[pair[1]] {
				t.Fatalf("%s must come before %s", pair[0], pair[1])
			}
		}
		// Every stored object is in the archive; nothing secret is.
		objects := media(t, ta)
		zr, err := zip.OpenReader(path)
		if err != nil {
			t.Fatal(err)
		}
		defer zr.Close()
		keyFile, _ := os.ReadFile(ta.Deps.Cfg.SecretKeyPath())
		needles := [][]byte{[]byte(masterKeyHex), []byte(base64.StdEncoding.EncodeToString(mustHex(masterKeyHex))),
			[]byte("sk-test-harbor-7f3a"), []byte(f.totpSecret)}
		if s := strings.TrimSpace(string(keyFile)); s != "" {
			needles = append(needles, []byte(s))
		}
		found := map[string]bool{}
		for _, zf := range zr.File {
			if strings.Contains(zf.Name, "secret") {
				t.Fatalf("archive holds %s", zf.Name)
			}
			rc, _ := zf.Open()
			data, _ := io.ReadAll(rc)
			_ = rc.Close()
			for _, n := range needles {
				if bytes.Contains(data, n) {
					t.Fatalf("archive entry %s contains a secret", zf.Name)
				}
			}
			if k, ok := strings.CutPrefix(zf.Name, "media/"); ok {
				found[k] = true
				sum := sha256.Sum256(data)
				if objects[k] != hex.EncodeToString(sum[:]) {
					t.Fatalf("media %s bytes differ", k)
				}
			}
		}
		for k := range objects {
			if !found[k] {
				t.Fatalf("media %s missing from the archive", k)
			}
		}
		if !found[f.objectKey] || m.Includes.MediaObjects != len(objects) {
			t.Fatalf("media count %d of %d", m.Includes.MediaObjects, len(objects))
		}
		// Binary columns are base64, booleans and numbers keep their JSON types.
		rows := tableRows(t, zr, "page_doc_updates")
		if len(rows) != 1 || rows[0]["data"] != base64.StdEncoding.EncodeToString(f.blob) {
			t.Fatalf("blob row %v", rows)
		}
		for _, u := range tableRows(t, zr, "users") {
			if _, ok := u["totp_enabled"].(bool); !ok {
				t.Fatalf("boolean column %v", u["totp_enabled"])
			}
		}
	})
}

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func tableRows(t *testing.T, zr *zip.ReadCloser, table string) []map[string]any {
	t.Helper()
	for _, zf := range zr.File {
		if zf.Name != "db/"+table+".jsonl" {
			continue
		}
		rc, _ := zf.Open()
		defer rc.Close()
		dec := json.NewDecoder(rc)
		var out []map[string]any
		for {
			var m map[string]any
			if err := dec.Decode(&m); err != nil {
				return out
			}
			out = append(out, m)
		}
	}
	t.Fatalf("no table %s", table)
	return nil
}

func TestPreUpdateBackupHoldsDatabaseOnly(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		seedServer(t, ta)
		s := svcOf(t, ta)
		start := time.Now()
		b, err := s.Create(ctx, backup.Request{Kind: backup.KindPreUpdate})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s pre-update (database only) backup of the seeded demo: %s, %d bytes", ta.Deps.DB.Dialect(), time.Since(start), b.Bytes)
		m, err := backup.ReadManifest(pathOf(t, ta, b.ID))
		if err != nil {
			t.Fatal(err)
		}
		if m.Includes.Media || m.Includes.MediaObjects != 0 || !m.Includes.Database || b.ExpiresAt == nil || b.ExpiresAt.Sub(b.CreatedAt) != 7*24*time.Hour {
			t.Fatalf("pre-update archive %+v %+v", m.Includes, b)
		}
		for name := range m.Entries {
			if strings.HasPrefix(name, "media/") {
				t.Fatalf("media entry %s in a pre-update archive", name)
			}
		}
	})
}

// rewrite copies an archive with edits: drop removes entries, edit replaces
// an entry's bytes, and manifest edits the manifest (checksums are updated
// only when fix is true).
func rewrite(t *testing.T, src, dst string, drop []string, edit map[string][]byte, manifest func(*backup.Manifest), fix bool) {
	t.Helper()
	zr, err := zip.OpenReader(src)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(out)
	var m backup.Manifest
	for _, zf := range zr.File {
		if zf.Name == "manifest.json" {
			rc, _ := zf.Open()
			_ = json.NewDecoder(rc).Decode(&m)
			_ = rc.Close()
		}
	}
	for _, zf := range zr.File {
		if zf.Name == "manifest.json" || slices.Contains(drop, zf.Name) {
			continue
		}
		rc, _ := zf.Open()
		data, _ := io.ReadAll(rc)
		_ = rc.Close()
		if e, ok := edit[zf.Name]; ok {
			data = e
			if fix {
				sum := sha256.Sum256(data)
				m.Entries[zf.Name] = backup.EntrySum{SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data))}
			}
		}
		w, _ := zw.Create(zf.Name)
		_, _ = w.Write(data)
	}
	for _, name := range drop {
		delete(m.Entries, name)
	}
	if manifest != nil {
		manifest(&m)
	}
	w, _ := zw.Create("manifest.json")
	_ = json.NewEncoder(w).Encode(m)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	_ = out.Close()
}

// directoryOnlyZip is a zip made of n empty central directory headers whose
// end record declares records entries (zip64 when records ≥ 0xffff).
func directoryOnlyZip(n int, records uint64) []byte {
	var buf bytes.Buffer
	put32 := func(b []byte, v uint32) { b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24) }
	put16 := func(b []byte, v uint16) { b[0], b[1] = byte(v), byte(v>>8) }
	put64 := func(b []byte, v uint64) { put32(b, uint32(v)); put32(b[4:], uint32(v>>32)) }
	for i := 0; i < n; i++ {
		h := make([]byte, 46)
		put32(h, 0x02014b50)
		put16(h[28:], 9)
		buf.Write(h)
		buf.WriteString("db/x.json")
	}
	size := uint64(buf.Len())
	zip64 := records >= 0xffff
	if zip64 {
		end := make([]byte, 56)
		put32(end, 0x06064b50)
		put64(end[4:], 44)
		put64(end[24:], records)
		put64(end[32:], records)
		put64(end[40:], size)
		buf.Write(end)
		loc := make([]byte, 20)
		put32(loc, 0x07064b50)
		put64(loc[8:], size)
		put32(loc[16:], 1)
		buf.Write(loc)
	}
	eocd := make([]byte, 22)
	put32(eocd, 0x06054b50)
	put16(eocd[8:], uint16(records))
	put16(eocd[10:], uint16(records))
	put32(eocd[12:], uint32(size))
	if zip64 {
		put16(eocd[8:], 0xffff)
		put16(eocd[10:], 0xffff)
		put32(eocd[12:], 0xffffffff)
	}
	buf.Write(eocd)
	return buf.Bytes()
}

// An uploaded archive is opened only after its end record shows a table of
// contents within the archive limits, and no larger directory is ever read.
func TestArchivesWithHugeDirectoriesAreRefusedCheaply(t *testing.T) {
	ta := testutil.NewTestApp(t)
	ctx := context.Background()
	s := svcOf(t, ta)
	dir := t.TempDir()
	declared := filepath.Join(dir, "declared.mvbackup")
	if err := os.WriteFile(declared, directoryOnlyZip(1, 80_000_000), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Register(ctx, declared, "", false); !errors.Is(err, backup.ErrIncompatible) || !strings.Contains(err.Error(), "too many entries") {
		t.Fatalf("archive declaring 80 million entries: %v", err)
	}
	defer backup.SetArchiveLimits(1000, 64<<10)()
	stuffed := filepath.Join(dir, "stuffed.mvbackup")
	if err := os.WriteFile(stuffed, directoryOnlyZip(65_537, 1), 0o600); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := s.Register(ctx, stuffed, "", false)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, backup.ErrIncompatible) || !strings.Contains(err.Error(), "too many entries") {
		t.Fatalf("stuffed directory: %v", err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 8<<20 {
		t.Fatalf("opening the stuffed directory allocated %d MB", grew>>20)
	}
}

func TestDamagedAndNewerArchivesAreRefused(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		seedServer(t, ta)
		s := svcOf(t, ta)
		b, err := s.Create(ctx, backup.Request{Kind: backup.KindManual})
		if err != nil {
			t.Fatal(err)
		}
		src := pathOf(t, ta, b.ID)
		before := fingerprints(t, ta)
		maintenance(t, ta, true)
		dir := t.TempDir()

		// A table file edited without updating the manifest.
		tampered := filepath.Join(dir, "tampered.mvbackup")
		rewrite(t, src, tampered, nil, map[string][]byte{"db/users.jsonl": []byte(`{"id":"01jxxxxxxxxxxxxxxxxxxxxxxx","username":"mallory"}` + "\n")}, nil, false)
		if _, err := s.Register(ctx, tampered, "", false); !errors.Is(err, backup.ErrIncompatible) {
			t.Fatalf("tampered archive registered: %v", err)
		}
		// The recorded archive changed on disk after it was written.
		data, _ := os.ReadFile(src)
		data[len(data)/2] ^= 0xff
		if err := os.WriteFile(src, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := s.Restore(ctx, b.ID, ta.Admin.ID); !errors.Is(err, backup.ErrIncompatible) {
			t.Fatalf("changed archive restored: %v", err)
		}
		data[len(data)/2] ^= 0xff
		_ = os.WriteFile(src, data, 0o600)
		// An archive from a newer schema, with consistent checksums.
		newer := filepath.Join(dir, "newer.mvbackup")
		rewrite(t, src, newer, nil, nil, func(m *backup.Manifest) { m.Schema = "9999_from_the_future" }, true)
		nb, err := s.Register(ctx, newer, "", false)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Restore(ctx, nb.ID, ta.Admin.ID); !errors.Is(err, backup.ErrIncompatible) || !strings.Contains(err.Error(), "newer") {
			t.Fatalf("newer archive restored: %v", err)
		}
		// A member export is not a server backup.
		ue, err := s.Create(ctx, backup.Request{Kind: backup.KindUserExport, UserID: ta.Admin.ID})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Restore(ctx, ue.ID, ta.Admin.ID); !errors.Is(err, backup.ErrIncompatible) {
			t.Fatalf("member export restored: %v", err)
		}
		after := fingerprints(t, ta)
		for table, rows := range before {
			if !slices.Equal(rows, after[table]) {
				t.Fatalf("a refused restore changed %s", table)
			}
		}
	})
}

func TestRestoreNeedsMaintenanceAndIsExclusive(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		s := svcOf(t, ta)
		b, err := s.Create(ctx, backup.Request{Kind: backup.KindManual})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Restore(ctx, b.ID, ta.Admin.ID); !errors.Is(err, backup.ErrNotInMaintenance) {
			t.Fatalf("restore outside maintenance: %v", err)
		}
		maintenance(t, ta, true)
		release, err := s.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Create(ctx, backup.Request{Kind: backup.KindManual}); !errors.Is(err, backup.ErrBusy) {
			t.Fatalf("second backup in process: %v", err)
		}
		// Another process sharing the database sees the lease.
		other := backup.NewTestService(ta.Deps)
		if _, err := other.Create(ctx, backup.Request{Kind: backup.KindManual}); !errors.Is(err, backup.ErrBusy) {
			t.Fatalf("backup in another process: %v", err)
		}
		if err := other.Restore(ctx, b.ID, ta.Admin.ID); !errors.Is(err, backup.ErrBusy) {
			t.Fatalf("restore in another process: %v", err)
		}
		release()
		if _, err := other.Create(ctx, backup.Request{Kind: backup.KindManual}); err != nil {
			t.Fatalf("backup after release: %v", err)
		}
		// Two concurrent creates: exactly one wins.
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, svc := range []*backup.TestService{s, other} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = svc.Create(ctx, backup.Request{Kind: backup.KindManual})
			}()
		}
		wg.Wait()
		busy := 0
		for _, e := range errs {
			if errors.Is(e, backup.ErrBusy) {
				busy++
			} else if e != nil {
				t.Fatal(e)
			}
		}
		if busy > 1 {
			t.Fatalf("both concurrent backups refused")
		}
	})
}

func TestRestoreRoundTripSameDialect(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f := seedServer(t, ta)
		s := svcOf(t, ta)
		b, err := s.Create(ctx, backup.Request{Kind: backup.KindManual, CreatedBy: ta.Admin.ID})
		if err != nil {
			t.Fatal(err)
		}
		before, objects := fingerprints(t, ta), media(t, ta)
		// Change the server after the backup.
		d := ta.Deps.DB
		if _, err := d.Exec(ctx, `DELETE FROM comments`); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(ctx, `UPDATE projects SET title = 'Low Tide Radio'`); err != nil {
			t.Fatal(err)
		}
		ta.CreateUser(t, "sora", auth.RoleReader)
		if err := ta.Deps.Store.Delete(ctx, f.objectKey); err != nil {
			t.Fatal(err)
		}
		lateKey := "assets/upload/late-" + db.NewID() + ".bin"
		if err := storage.PutBytes(ctx, ta.Deps.Store, lateKey, []byte("made after the backup"), ""); err != nil {
			t.Fatal(err)
		}
		admin := ta.Login(t, "admin")
		later, err := s.Create(ctx, backup.Request{Kind: backup.KindManual})
		if err != nil {
			t.Fatal(err)
		}
		maintenance(t, ta, true)
		var progress []float64
		var mu sync.Mutex
		pctx := backup.WithProgress(ctx, func(p float64) { mu.Lock(); progress = append(progress, p); mu.Unlock() })
		rep, err := s.RestoreArchive(pctx, b.ID, ta.Admin.ID)
		if err != nil {
			t.Fatal(err)
		}
		if rep.MasterKeyDiffers || rep.Sealed["provider_keys"] != 1 || rep.Sealed["totp"] != 1 || rep.Tables == 0 || rep.Rows == 0 {
			t.Fatalf("report %+v", rep)
		}
		if len(progress) == 0 || progress[len(progress)-1] != 1 {
			t.Fatalf("progress %v", progress)
		}
		after := fingerprints(t, ta)
		for table, rows := range before {
			if !slices.Equal(rows, after[table]) {
				t.Fatalf("%s differs after restore:\nbefore %d rows\nafter  %d rows", table, len(rows), len(after[table]))
			}
		}
		if got := media(t, ta); len(got) != len(objects) {
			t.Fatalf("media after restore %d, want %d", len(got), len(objects))
		} else {
			for k, sum := range objects {
				if got[k] != sum {
					t.Fatalf("media %s differs", k)
				}
			}
		}
		if _, err := ta.Deps.Store.Stat(ctx, lateKey); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("object made after the backup survived: %v", err)
		}
		// The server stays in maintenance until the caller turns it off, keeps
		// its own archive records and the restoring admin stays signed in.
		ta.Deps.Settings.Invalidate()
		if !ta.Deps.Settings.MustInstance(ctx).Maintenance.Enabled {
			t.Fatal("maintenance mode was replaced by the archive's")
		}
		got, err := s.Get(ctx, later.ID)
		if err != nil || got.Status != backup.StatusDone {
			t.Fatalf("later archive record %+v %v", got, err)
		}
		if got, _ := s.Get(ctx, b.ID); got.Status != backup.StatusRestored {
			t.Fatalf("restored archive status %s", got.Status)
		}
		if r := admin.Get("/admin/backup"); r.StatusCode != 200 {
			t.Fatalf("restoring admin signed out: %d", r.StatusCode)
		}
		var n int
		_ = d.Get(ctx, &n, `SELECT COUNT(*) FROM users WHERE username = 'sora'`)
		var parent string
		_ = d.Get(ctx, &parent, `SELECT parent_id FROM comments WHERE id = ?`, f.childID)
		if n != 0 || parent != f.parentID {
			t.Fatalf("users sora=%d, reply parent=%q", n, parent)
		}
		var title string
		_ = d.Get(ctx, &title, `SELECT title FROM projects ORDER BY created_at LIMIT 1`)
		if title == "Low Tide Radio" {
			t.Fatal("project title was not restored")
		}
		last, err := s.LastRestore(ctx)
		if err != nil || last == nil || last.BackupID != b.ID {
			t.Fatalf("restore record %+v %v", last, err)
		}
		// Search still works on SQLite (the FTS index follows restored series).
		if ta.Deps.DB.Dialect() == db.SQLite {
			if _, err := d.Exec(ctx, `INSERT INTO series_fts (series_fts) VALUES ('integrity-check')`); err != nil {
				t.Fatalf("search index: %v", err)
			}
		}
	})
}

// olderRelease is the schema of the last release before Phase 4 (0700 adds
// agent_actions, 1000 adds comments.assignee_id).
const olderRelease = "0403_analytics_day_salts"

// TestRestoreFromOlderSchemaRunsMigrations restores an archive written by a
// server still at an older schema: the schema is rebuilt at that version, the
// rows are loaded and the newer migrations run, in one transaction.
func TestRestoreFromOlderSchemaRunsMigrations(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		dialect := string(ta.Deps.DB.Dialect())
		// An older server: its own database migrated only up to olderRelease.
		target := testdb.NewTarget(t, dialect)
		old, err := db.Open(ctx, dialect, target.DSN, db.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer old.Close()
		migs, err := db.LoadMigrations(db.Migrations(), old.Dialect())
		if err != nil {
			t.Fatal(err)
		}
		files := fstest.MapFS{}
		var newer []string
		for _, m := range migs {
			if m.Version > olderRelease {
				newer = append(newer, m.Version)
				continue
			}
			b, err := fs.ReadFile(db.Migrations(), m.File)
			if err != nil {
				t.Fatal(err)
			}
			files[m.File] = &fstest.MapFile{Data: b}
		}
		if len(newer) == 0 {
			t.Skip("no migration newer than " + olderRelease)
		}
		if _, err := old.MigrateFS(ctx, files); err != nil {
			t.Fatal(err)
		}
		now := db.Now()
		userID, projectID, parentID, childID := db.NewID(), db.NewID(), db.NewID(), db.NewID()
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO users (id, username, display_name, password_hash, role, created_at, updated_at) VALUES (?, 'wren', 'Old Wren', 'x', 'admin', ?, ?)`, []any{userID, now, now}},
			{`INSERT INTO projects (id, owner_id, title, slug, created_at, updated_at) VALUES (?, ?, 'Tidebreak Courier', 'tidebreak-courier', ?, ?)`, []any{projectID, userID, now, now}},
			{`INSERT INTO comments (id, project_id, target_kind, target_id, body, author_id, created_at, updated_at) VALUES (?, ?, 'chapter', 'c1', 'Bell tower', ?, ?, ?)`, []any{parentID, projectID, userID, now, now}},
			{`INSERT INTO comments (id, project_id, target_kind, target_id, body, author_id, parent_id, created_at, updated_at) VALUES (?, ?, 'chapter', 'c1', 'Reply', ?, ?, ?, ?)`, []any{childID, projectID, userID, parentID, now, now}},
		} {
			if _, err := old.Exec(ctx, q.sql, q.args...); err != nil {
				t.Fatal(err)
			}
		}
		media, err := storage.NewLocal(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		od := &core.Deps{Cfg: ta.Deps.Cfg, Log: ta.Deps.Log, DB: old, Store: media, Secrets: ta.Deps.Secrets, Settings: settings.New(old), Version: ta.Deps.Version}
		ob, err := backup.NewTestService(od).Create(ctx, backup.Request{Kind: backup.KindExport})
		if err != nil {
			t.Fatal(err)
		}
		if ob.Schema != olderRelease {
			t.Fatalf("older archive schema %s", ob.Schema)
		}
		var oldPath string
		if err := old.Get(ctx, &oldPath, `SELECT path FROM backups WHERE id = ?`, ob.ID); err != nil {
			t.Fatal(err)
		}
		// Restore it on the current server.
		s := svcOf(t, ta)
		rb, err := s.Register(ctx, oldPath, "", false)
		if err != nil {
			t.Fatal(err)
		}
		maintenance(t, ta, true)
		rep, err := s.RestoreArchive(ctx, rb.ID, ta.Admin.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(rep.Migrated, newer) || rep.FromSchema != olderRelease {
			t.Fatalf("migrated %v from %s, want %v", rep.Migrated, rep.FromSchema, newer)
		}
		d := ta.Deps.DB
		if newestSchema(t, d) != newer[len(newer)-1] {
			t.Fatal("migrations did not run after the restore")
		}
		var username, title, parent string
		if err := d.QueryRow(ctx, `SELECT username FROM users WHERE id = ?`, userID).Scan(&username); err != nil || username != "wren" {
			t.Fatalf("user %q %v", username, err)
		}
		if err := d.QueryRow(ctx, `SELECT title FROM projects WHERE id = ?`, projectID).Scan(&title); err != nil || title != "Tidebreak Courier" {
			t.Fatalf("project %q %v", title, err)
		}
		if err := d.QueryRow(ctx, `SELECT parent_id FROM comments WHERE id = ?`, childID).Scan(&parent); err != nil || parent != parentID {
			t.Fatalf("reply parent %q %v", parent, err)
		}
		var users, assigned int
		_ = d.Get(ctx, &users, `SELECT COUNT(*) FROM users`)
		if err := d.Get(ctx, &assigned, `SELECT COUNT(*) FROM comments WHERE assignee_id IS NOT NULL`); err != nil || assigned != 0 || users != 1 {
			t.Fatalf("after migrating forward: users=%d assigned=%d %v", users, assigned, err)
		}
	})
}

// TestRestoreAcrossDialects moves a SQLite server to PostgreSQL and back.
func TestRestoreAcrossDialects(t *testing.T) {
	if len(testdb.Dialects()) < 2 {
		t.Skip("MV_TEST_POSTGRES_URL not set")
	}
	for _, pair := range [][2]string{{"sqlite", "postgres"}, {"postgres", "sqlite"}} {
		t.Run(pair[0]+"_to_"+pair[1], func(t *testing.T) {
			ctx := context.Background()
			from := testutil.NewTestApp(t, testutil.WithDialect(pair[0]))
			to := testutil.NewTestApp(t, testutil.WithDialect(pair[1]))
			f := seedServer(t, from)
			b, err := svcOf(t, from).Create(ctx, backup.Request{Kind: backup.KindExport})
			if err != nil {
				t.Fatal(err)
			}
			want, objects := fingerprints(t, from), media(t, from)
			copyPath := filepath.Join(t.TempDir(), "move.mvbackup")
			data, err := os.ReadFile(pathOf(t, from, b.ID))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(copyPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			ts := svcOf(t, to)
			rb, err := ts.Register(ctx, copyPath, to.Admin.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			maintenance(t, to, true)
			rep, err := ts.RestoreArchive(ctx, rb.ID, to.Admin.ID)
			if err != nil {
				t.Fatal(err)
			}
			if rep.Dialect != pair[0] || rep.MasterKeyDiffers {
				t.Fatalf("report %+v", rep)
			}
			got := fingerprints(t, to)
			for table, rows := range want {
				if !slices.Equal(rows, got[table]) {
					for i := range rows {
						if i >= len(got[table]) || rows[i] != got[table][i] {
							t.Fatalf("%s row %d differs:\n%s\n%v", table, i, rows[i], safeIndex(got[table], i))
						}
					}
					t.Fatalf("%s has %d rows, want %d", table, len(got[table]), len(rows))
				}
			}
			gotMedia := media(t, to)
			for k, sum := range objects {
				if gotMedia[k] != sum {
					t.Fatalf("media %s not restored", k)
				}
			}
			// Spot values through ordinary queries on the target dialect.
			var blob []byte
			if err := to.Deps.DB.Get(ctx, &blob, `SELECT data FROM page_doc_updates LIMIT 1`); err != nil || !bytes.Equal(blob, f.blob) {
				t.Fatalf("blob %v %v", blob, err)
			}
			var parent string
			if err := to.Deps.DB.Get(ctx, &parent, `SELECT parent_id FROM comments WHERE id = ?`, f.childID); err != nil || parent != f.parentID {
				t.Fatalf("reply parent %q %v", parent, err)
			}
			var enabled bool
			if err := to.Deps.DB.Get(ctx, &enabled, `SELECT totp_enabled FROM users WHERE id = ?`, f.member.ID); err != nil || !enabled {
				t.Fatalf("boolean %v %v", enabled, err)
			}
			to.Deps.Settings.Invalidate()
			if inst := to.Deps.Settings.MustInstance(ctx); inst.Name != "Paper Crows Studio" || !inst.Maintenance.Enabled {
				t.Fatalf("settings %+v", inst)
			}
			// The moved server signs the original members in.
			if r := to.Login(t, "admin").Get("/studio"); r.StatusCode != 200 {
				t.Fatalf("studio after move: %d", r.StatusCode)
			}
		})
	}
}

func safeIndex(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return "<missing>"
}

func TestMasterKeyMismatchIsReported(t *testing.T) {
	ctx := context.Background()
	from := testutil.NewTestApp(t)
	to := testutil.NewTestApp(t, testutil.WithEnv("MV_MASTER_KEY", strings.Repeat("cd", 32)))
	seedServer(t, from)
	b, err := svcOf(t, from).Create(ctx, backup.Request{Kind: backup.KindExport})
	if err != nil {
		t.Fatal(err)
	}
	ts := svcOf(t, to)
	rb, err := ts.Register(ctx, pathOf(t, from, b.ID), "", false)
	if err != nil {
		t.Fatal(err)
	}
	maintenance(t, to, true)
	rep, err := ts.RestoreArchive(ctx, rb.ID, to.Admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.MasterKeyDiffers {
		t.Fatalf("mismatch not detected: %+v", rep)
	}
	page := to.Login(t, "admin").Get("/admin/backup")
	if page.StatusCode != 200 || !strings.Contains(string(page.Body), "different master key") {
		t.Fatalf("G10 warning missing: %d", page.StatusCode)
	}
}

func TestScheduleClaimAcrossConnections(t *testing.T) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			ctx := context.Background()
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect))
			kind, dsn, err := ta.Deps.Cfg.Database()
			if err != nil {
				t.Fatal(err)
			}
			second, err := db.Open(ctx, kind, dsn, db.Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer second.Close()
			d2 := &core.Deps{Cfg: ta.Deps.Cfg, Log: ta.Deps.Log, DB: second, Store: ta.Deps.Store, Secrets: ta.Deps.Secrets,
				Settings: settings.New(second), Version: ta.Deps.Version}
			a, b := svcOf(t, ta), backup.NewTestService(d2)
			slot := time.Date(2026, 11, 9, 3, 0, 0, 0, time.UTC)
			var wg sync.WaitGroup
			wins := make([]bool, 8)
			for i := range wins {
				wg.Add(1)
				go func() {
					defer wg.Done()
					svc := a
					if i%2 == 1 {
						svc = b
					}
					won, err := svc.ClaimSlot(ctx, slot)
					if err != nil && !db.IsTransient(err) {
						t.Error(err)
					}
					wins[i] = won
				}()
			}
			wg.Wait()
			n := 0
			for _, w := range wins {
				if w {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("%d claims won the same slot", n)
			}
			if won, _ := b.ClaimSlot(ctx, slot); won {
				t.Fatal("a claimed slot was claimed again")
			}
			if won, err := b.ClaimSlot(ctx, slot.Add(24*time.Hour)); err != nil || !won {
				t.Fatalf("next slot: %v %v", won, err)
			}
		})
	}
}

func TestScheduledBackupsRunOnceAndKeepTheNewest(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		s := svcOf(t, ta)
		other := backup.NewTestService(ta.Deps)
		now := time.Date(2026, 11, 9, 2, 0, 0, 0, time.UTC)
		clock := func() time.Time { return now }
		s.SetNow(clock)
		other.SetNow(clock)
		sc := backup.Schedule{Frequency: backup.FrequencyDaily, Hour: 3, TZ: "UTC", Keep: 2}
		if _, err := s.SaveSchedule(ctx, sc, ta.Admin.ID); err != nil {
			t.Fatal(err)
		}
		count := func() int {
			list, err := s.List(ctx, backup.KindScheduled, 100)
			if err != nil {
				t.Fatal(err)
			}
			return len(list)
		}
		// Before 03:00 nothing is due (the 03:00 slot of the day before predates the schedule).
		if err := s.RunDue(ctx); err != nil || count() != 0 {
			t.Fatalf("ran before the slot: %d %v", count(), err)
		}
		for day := 0; day < 4; day++ {
			now = time.Date(2026, 11, 9+day, 3, 1, 0, 0, time.UTC)
			for _, svc := range []*backup.TestService{s, other, s} {
				if err := svc.RunDue(ctx); err != nil {
					t.Fatal(err)
				}
			}
		}
		if count() != 2 {
			t.Fatalf("kept %d scheduled archives, want 2", count())
		}
		var deleted int
		_ = ta.Deps.DB.Get(ctx, &deleted, `SELECT COUNT(*) FROM backups WHERE kind = 'scheduled' AND status = 'deleted'`)
		if deleted != 2 {
			t.Fatalf("%d pruned, want 2 (one backup per day across both processes)", deleted)
		}
	})
}

// An archive registered from elsewhere (`backup restore <path>` or an upload)
// is never pruned by the schedule, and deleting its record never removes a
// file the module did not write into its own backup directory.
func TestPruneAndDeleteNeverTouchOutsideArchives(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		s := svcOf(t, ta)
		now := time.Date(2026, 11, 9, 2, 0, 0, 0, time.UTC)
		s.SetNow(func() time.Time { return now })
		if _, err := s.SaveSchedule(ctx, backup.Schedule{Frequency: backup.FrequencyDaily, Hour: 3, TZ: "UTC", Keep: 1}, ta.Admin.ID); err != nil {
			t.Fatal(err)
		}
		now = time.Date(2026, 11, 9, 3, 1, 0, 0, time.UTC)
		if err := s.RunDue(ctx); err != nil {
			t.Fatal(err)
		}
		list, err := s.List(ctx, backup.KindScheduled, 10)
		if err != nil || len(list) != 1 {
			t.Fatalf("scheduled = %v %v", list, err)
		}
		// The admin keeps an old scheduled archive in their home directory and registers it.
		data, _ := os.ReadFile(pathOf(t, ta, list[0].ID))
		outside := filepath.Join(t.TempDir(), "old.mvbackup")
		if err := os.WriteFile(outside, data, 0o600); err != nil {
			t.Fatal(err)
		}
		reg, err := s.Register(ctx, outside, ta.Admin.ID, false)
		if err != nil || reg.Kind != backup.KindScheduled {
			t.Fatalf("register = %+v %v", reg, err)
		}
		for day := 1; day <= 3; day++ {
			now = time.Date(2026, 11, 9+day, 3, 1, 0, 0, time.UTC)
			if err := s.RunDue(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if got, _ := s.Get(ctx, reg.ID); got.Status != backup.StatusDone {
			t.Fatalf("the registered archive was pruned: %s", got.Status)
		}
		if _, err := os.Stat(outside); err != nil {
			t.Fatalf("the outside file was removed: %v", err)
		}
		var kept int
		_ = ta.Deps.DB.Get(ctx, &kept, `SELECT COUNT(*) FROM backups WHERE kind = 'scheduled' AND status = 'done' AND id <> ?`, reg.ID)
		if kept != 1 {
			t.Fatalf("the schedule kept %d of its own archives, want 1", kept)
		}
		// Deleting the registered record forgets it but leaves the admin's file.
		if err := s.Delete(ctx, reg.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(outside); err != nil {
			t.Fatalf("delete removed a file outside the backup directory: %v", err)
		}
		// A record pointing at a file the module did not name is never removed either.
		stray := filepath.Join(t.TempDir(), "notes.txt")
		_ = os.WriteFile(stray, []byte("keep me"), 0o600)
		id := db.NewID()
		if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO backups (id, kind, status, path, created_at) VALUES (?, 'manual', 'done', ?, ?)`, id, stray, db.Now()); err != nil {
			t.Fatal(err)
		}
		if err := s.Delete(ctx, id); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(stray); err != nil {
			t.Fatalf("delete removed %s: %v", stray, err)
		}
		// The module's own archives are deleted from disk.
		own, err := s.Create(ctx, backup.Request{Kind: backup.KindManual})
		if err != nil {
			t.Fatal(err)
		}
		p := pathOf(t, ta, own.ID)
		if err := s.Delete(ctx, own.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("the module's own archive stayed on disk: %v", err)
		}
	})
}

func TestScheduleSlotsAndDestination(t *testing.T) {
	ta := testutil.NewTestApp(t)
	ctx := context.Background()
	s := svcOf(t, ta)
	weekly := backup.Schedule{Frequency: backup.FrequencyWeekly, Weekday: 1, Hour: 3, TZ: "Asia/Tokyo", Keep: 4}
	// Wednesday 2026-11-11 12:00 Tokyo → last Monday 03:00 Tokyo.
	slot, ok := weekly.Slot(time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC))
	if !ok || !slot.Equal(time.Date(2026, 11, 8, 18, 0, 0, 0, time.UTC)) {
		t.Fatalf("weekly slot %v", slot)
	}
	next, _ := weekly.Next(time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC))
	if !next.Equal(slot.Add(7 * 24 * time.Hour)) {
		t.Fatalf("next %v", next)
	}
	if _, ok := backup.DefaultSchedule().Slot(time.Now()); ok {
		t.Fatal("the default schedule is off")
	}
	if _, err := s.SaveSchedule(ctx, backup.Schedule{Frequency: backup.FrequencyDaily, Keep: 3, TZ: "UTC", Destination: "relative/dir"}, ""); !errors.Is(err, backup.ErrDestination) {
		t.Fatalf("relative destination: %v", err)
	}
	if _, err := s.SaveSchedule(ctx, backup.Schedule{Frequency: backup.FrequencyDaily, Keep: 3, TZ: "UTC", Destination: filepath.Join(t.TempDir(), "missing")}, ""); !errors.Is(err, backup.ErrDestination) {
		t.Fatalf("missing destination: %v", err)
	}
	dest := t.TempDir()
	if _, err := s.SaveSchedule(ctx, backup.Schedule{Frequency: backup.FrequencyDaily, Keep: 3, TZ: "UTC", Destination: dest}, ""); err != nil {
		t.Fatal(err)
	}
	b, err := s.Create(ctx, backup.Request{Kind: backup.KindManual})
	if err != nil {
		t.Fatal(err)
	}
	if p := pathOf(t, ta, b.ID); filepath.Dir(p) != dest {
		t.Fatalf("archive written to %s, want %s", p, dest)
	}
	if err := os.Chmod(dest, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dest, 0o700)
	if os.Getuid() != 0 {
		if _, err := s.Create(ctx, backup.Request{Kind: backup.KindManual}); !errors.Is(err, backup.ErrDestination) {
			t.Fatalf("unwritable destination: %v", err)
		}
	}
}

func TestExpiredArchivesAreDeleted(t *testing.T) {
	ta := testutil.NewTestApp(t)
	ctx := context.Background()
	s := svcOf(t, ta)
	b, err := s.Create(ctx, backup.Request{Kind: backup.KindPreUpdate})
	if err != nil {
		t.Fatal(err)
	}
	path := pathOf(t, ta, b.ID)
	s.SetNow(func() time.Time { return time.Now().Add(8 * 24 * time.Hour) })
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, b.ID); got.Status != backup.StatusDeleted {
		t.Fatalf("expired archive %s", got.Status)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired file kept: %v", err)
	}
}

// withProbeMigration is the embedded migrations plus a test-only migration
// N+1 that adds a table and a column, as a newer binary's migrate would.
func withProbeMigration(t *testing.T) fs.FS {
	t.Helper()
	files := fstest.MapFS{}
	entries, err := fs.ReadDir(db.Migrations(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := fs.ReadFile(db.Migrations(), e.Name())
		if err != nil {
			t.Fatal(err)
		}
		files[e.Name()] = &fstest.MapFile{Data: b}
	}
	files["9900_rollback_probe.sql"] = &fstest.MapFile{Data: []byte(`
CREATE TABLE rollback_probe (
    id         TEXT PRIMARY KEY,
    project_id TEXT REFERENCES projects (id) ON DELETE CASCADE,
    note       TEXT NOT NULL DEFAULT ''
);
ALTER TABLE projects ADD COLUMN probe_flag TEXT NOT NULL DEFAULT '';
`)}
	return files
}

// TestRestoreUndoesNewerMigrations is the failed-update rollback: the new
// binary's migrate moved the live schema to N+1, then the old binary restores
// its pre-update archive at N. The schema goes back to exactly N (objects and
// schema_migrations), the rows match, and migrating forward again works.
func TestRestoreUndoesNewerMigrations(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		seedServer(t, ta)
		s := svcOf(t, ta)
		b, err := s.Create(ctx, backup.Request{Kind: backup.KindPreUpdate})
		if err != nil {
			t.Fatal(err)
		}
		n := b.Schema
		before := fingerprints(t, ta)
		var versionsBefore []string
		d := ta.Deps.DB
		if err := d.Select(ctx, &versionsBefore, `SELECT version FROM schema_migrations ORDER BY version`); err != nil {
			t.Fatal(err)
		}
		probe := withProbeMigration(t)
		applied, err := d.MigrateFS(ctx, probe)
		if err != nil || !slices.Equal(applied, []string{"9900_rollback_probe"}) {
			t.Fatalf("N+1 migration %v %v", applied, err)
		}
		var projectID string
		if err := d.Get(ctx, &projectID, `SELECT id FROM projects LIMIT 1`); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(ctx, `INSERT INTO rollback_probe (id, project_id, note) VALUES (?, ?, 'written by the new binary')`, db.NewID(), projectID); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(ctx, `UPDATE projects SET probe_flag = 'new'`); err != nil {
			t.Fatal(err)
		}
		maintenance(t, ta, true)
		rep, err := s.RestoreArchive(ctx, b.ID, ta.Admin.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Migrated) != 0 {
			t.Fatalf("a rollback migrated forward: %v", rep.Migrated)
		}
		var versionsAfter []string
		if err := d.Select(ctx, &versionsAfter, `SELECT version FROM schema_migrations ORDER BY version`); err != nil {
			t.Fatal(err)
		}
		if newestSchema(t, d) != n || !slices.Equal(versionsAfter, versionsBefore) {
			t.Fatalf("schema_migrations after rollback %v, want %v (newest %s)", versionsAfter, versionsBefore, n)
		}
		var count int
		if err := d.Get(ctx, &count, `SELECT COUNT(*) FROM rollback_probe`); err == nil {
			t.Fatal("the N+1 table survived the rollback")
		}
		if err := d.Get(ctx, &count, `SELECT COUNT(*) FROM projects WHERE probe_flag = 'new'`); err == nil {
			t.Fatal("the N+1 column survived the rollback")
		}
		after := fingerprints(t, ta)
		for table, rows := range before {
			if !slices.Equal(rows, after[table]) {
				t.Fatalf("%s differs after the rollback (%d rows, want %d)", table, len(after[table]), len(rows))
			}
		}
		// A later update can migrate forward again.
		applied, err = d.MigrateFS(ctx, probe)
		if err != nil || !slices.Equal(applied, []string{"9900_rollback_probe"}) {
			t.Fatalf("migrating forward after the rollback: %v %v", applied, err)
		}
		if err := d.Get(ctx, &count, `SELECT COUNT(*) FROM rollback_probe`); err != nil || count != 0 {
			t.Fatalf("N+1 table after migrating again: %d %v", count, err)
		}
	})
}

func TestRestoreCancelsUnfinishedJobs(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		s := svcOf(t, ta)
		queued := db.NewID()
		now := db.Now()
		if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO jobs (id, type, scope, scope_kind, user_id, state, executor, created_at, updated_at)
			VALUES (?, 'image.generate', '{"kind":"none"}', 'none', ?, 'queued', 'worker', ?, ?)`, queued, ta.Admin.ID, now, now); err != nil {
			t.Fatal(err)
		}
		b, err := s.Create(ctx, backup.Request{Kind: backup.KindManual})
		if err != nil {
			t.Fatal(err)
		}
		maintenance(t, ta, true)
		rep, err := s.RestoreArchive(ctx, b.ID, ta.Admin.ID)
		if err != nil {
			t.Fatal(err)
		}
		var state string
		var cancel bool
		if err := ta.Deps.DB.QueryRow(ctx, `SELECT state, cancel_requested FROM jobs WHERE id = ?`, queued).Scan(&state, &cancel); err != nil {
			t.Fatal(err)
		}
		if state != "canceled" || !cancel || rep.CanceledJobs != 1 {
			t.Fatalf("restored queued job is %s (cancel %v, report %d): a restore must not start work", state, cancel, rep.CanceledJobs)
		}
	})
}
