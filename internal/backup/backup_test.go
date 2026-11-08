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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
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

func TestRestoreFromOlderSchemaRunsMigrations(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		seedServer(t, ta)
		s := svcOf(t, ta)
		b, err := s.Create(ctx, backup.Request{Kind: backup.KindManual})
		if err != nil {
			t.Fatal(err)
		}
		migs, err := db.LoadMigrations(db.Migrations(), ta.Deps.DB.Dialect())
		if err != nil {
			t.Fatal(err)
		}
		last := migs[len(migs)-1]
		prev := migs[len(migs)-2].Version
		if last.Version != "0403_analytics_day_salts" {
			t.Skipf("the newest migration is %s; adjust the older-schema fixture", last.Version)
		}
		before := fingerprints(t, ta)
		// Make the archive look like it came from before 0403 (which only adds a table).
		older := filepath.Join(t.TempDir(), "older.mvbackup")
		rewrite(t, pathOf(t, ta, b.ID), older, []string{"db/analytics_day_salts.jsonl"}, nil, func(m *backup.Manifest) {
			m.Schema = prev
			m.Tables = slices.DeleteFunc(m.Tables, func(te backup.TableEntry) bool { return te.Name == "analytics_day_salts" })
		}, true)
		ob, err := s.Register(ctx, older, "", false)
		if err != nil {
			t.Fatal(err)
		}
		maintenance(t, ta, true)
		rep, err := s.RestoreArchive(ctx, ob.ID, ta.Admin.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(rep.Migrated, []string{last.Version}) || rep.FromSchema != prev {
			t.Fatalf("migrated %v from %s", rep.Migrated, rep.FromSchema)
		}
		if newestSchema(t, ta.Deps.DB) != last.Version {
			t.Fatal("migrations did not run after the restore")
		}
		after := fingerprints(t, ta)
		for table, rows := range before {
			if !slices.Equal(rows, after[table]) {
				t.Fatalf("%s differs after an older-schema restore", table)
			}
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

// TestRestoreUndoesNewerMigrations is the failed-update rollback: a newer
// binary migrated the database, the old binary restores its pre-update archive.
func TestRestoreUndoesNewerMigrations(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		seedServer(t, ta)
		s := svcOf(t, ta)
		b, err := s.Create(ctx, backup.Request{Kind: backup.KindPreUpdate})
		if err != nil {
			t.Fatal(err)
		}
		before := fingerprints(t, ta)
		d := ta.Deps.DB
		if _, err := d.Exec(ctx, `CREATE TABLE future_panels (id TEXT PRIMARY KEY, note TEXT NOT NULL DEFAULT '')`); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(ctx, `ALTER TABLE projects ADD COLUMN future_flag TEXT NOT NULL DEFAULT ''`); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(ctx, `INSERT INTO schema_migrations (version, file, applied_at) VALUES ('9000_future', '9000_future.sql', ?)`, db.Now()); err != nil {
			t.Fatal(err)
		}
		maintenance(t, ta, true)
		if _, err := s.RestoreArchive(ctx, b.ID, ta.Admin.ID); err != nil {
			t.Fatal(err)
		}
		if newestSchema(t, d) != b.Schema {
			t.Fatalf("schema %s after rollback, want %s", newestSchema(t, d), b.Schema)
		}
		var n int
		if err := d.Get(ctx, &n, `SELECT COUNT(*) FROM future_panels`); err == nil {
			t.Fatal("the newer binary's table survived the rollback")
		}
		after := fingerprints(t, ta)
		for table, rows := range before {
			if !slices.Equal(rows, after[table]) {
				t.Fatalf("%s differs after the rollback", table)
			}
		}
		// The pre-update archive holds no media: media stay in place.
		if _, err := ta.Deps.Store.List(ctx, ""); err != nil {
			t.Fatal(err)
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
