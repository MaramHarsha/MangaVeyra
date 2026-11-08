package update_test

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/backup"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/update"
)

// fakeBinDir holds the fake "new release" binary built once for the package.
var (
	fakeOnce sync.Once
	fakeBin  []byte
	fakeErr  error
	fakeDir  string
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mv-update-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fakeDir = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// fakeRelease builds testdata/fakeserver (version 1.0.2) once.
func fakeRelease(t *testing.T) []byte {
	t.Helper()
	fakeOnce.Do(func() {
		out := filepath.Join(fakeDir, "fakeserver")
		goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
		cmd := exec.Command(goBin, "build", "-o", out, "./testdata/fakeserver")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			fakeErr = fmt.Errorf("build fake release: %v\n%s", err, b)
			return
		}
		fakeBin, fakeErr = os.ReadFile(out)
	})
	if fakeErr != nil {
		t.Fatal(fakeErr)
	}
	return fakeBin
}

// releaseServer is a local release feed (never a real release server).
type releaseServer struct {
	*httptest.Server
	mu       sync.Mutex
	files    map[string][]byte
	truncate bool
	tamper   bool
	hits     atomic.Int64
}

func newReleaseServer(t *testing.T) *releaseServer {
	rs := &releaseServer{files: map[string][]byte{}}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.hits.Add(1)
		rs.mu.Lock()
		b, ok := rs.files[r.URL.Path]
		trunc := rs.truncate && strings.HasPrefix(r.URL.Path, "/assets/")
		if rs.tamper && strings.HasPrefix(r.URL.Path, "/assets/") {
			b = append([]byte{}, b...)
			b[len(b)/2] ^= 0xff
		}
		rs.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		if trunc {
			w.(http.Flusher).Flush()
			_, _ = w.Write(b[:len(b)/2])
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(rs.Close)
	return rs
}

func (rs *releaseServer) put(path string, b []byte) {
	rs.mu.Lock()
	rs.files[path] = b
	rs.mu.Unlock()
}

type publishOpts struct {
	version  string
	channel  string
	patch    bool
	security bool
	key      ed25519.PrivateKey
	tamper   bool
}

// publish signs and serves a manifest whose asset is the fake release binary.
func (e *env) publish(o publishOpts) update.Manifest {
	e.t.Helper()
	if o.version == "" {
		o.version = "1.0.2"
	}
	if o.channel == "" {
		o.channel = update.ChannelStable
	}
	if o.key == nil {
		o.key = e.priv
	}
	bin := fakeRelease(e.t)
	h := sha256.Sum256(bin)
	man := update.Manifest{
		Version: o.version, Channel: o.channel, Published: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC),
		NotesURL: "https://releases.example.org/mangaveyra/notes/" + o.version,
		Changelog: []update.Release{
			{Version: o.version, Patch: o.patch, Security: o.security, Notes: []string{"Security: tighter upload checks (recommended)"}},
			{Version: "1.0.1", Patch: o.patch, Notes: []string{"Fixes panel locks being ignored by batch jobs", "Faster region edits on 16 GB GPUs"}},
			{Version: "1.0.0", Notes: []string{"First release"}},
		},
		Assets: []update.Asset{{OS: runtime.GOOS, Arch: runtime.GOARCH, URL: e.release.URL + "/assets/mangaveyra-" + o.version,
			SHA256: hex.EncodeToString(h[:]), Size: int64(len(bin))}},
	}
	raw, _ := json.Marshal(man)
	sig := update.SignManifest(raw, o.key)
	if o.tamper {
		raw = []byte(strings.Replace(string(raw), `"Fixes panel`, `"Breaks panel`, 1))
	}
	e.release.put("/"+o.channel+".json", raw)
	e.release.put("/"+o.channel+".json.sig", sig)
	e.release.put("/assets/mangaveyra-"+o.version, bin)
	return man
}

// fakeBackups implements backup.Service against the backups table. Restore
// puts back the instance settings and update rows as they were at Create,
// like a real archive would, and removes the fake migration's marker.
type fakeBackups struct {
	d           *db.DB
	dataDir     string
	mu          sync.Mutex
	created     []backup.Request
	restored    []string
	deleted     []string
	failCreate  error
	failRestore error
	snapshot    map[string]snap
}

type snap struct {
	settings map[string]string
	updates  map[string][2]string
}

func (f *fakeBackups) Create(ctx context.Context, req backup.Request) (backup.Backup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, req)
	if f.failCreate != nil {
		return backup.Backup{}, f.failCreate
	}
	b := backup.Backup{ID: db.NewID(), Kind: req.Kind, Status: backup.StatusDone, Bytes: 18_200_000_000, AppVersion: "1.0.0",
		CreatedAt: db.Now()}
	if _, err := f.d.Exec(ctx, `INSERT INTO backups (id, kind, status, bytes, app_version, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		b.ID, b.Kind, b.Status, b.Bytes, b.AppVersion, b.CreatedAt); err != nil {
		return backup.Backup{}, err
	}
	s := snap{settings: map[string]string{}, updates: map[string][2]string{}}
	var rows []struct {
		Key   string `db:"key"`
		Value string `db:"value"`
	}
	_ = f.d.Select(ctx, &rows, `SELECT key, value FROM instance_settings`)
	for _, r := range rows {
		s.settings[r.Key] = r.Value
	}
	var ups []struct {
		ID     string `db:"id"`
		Status string `db:"status"`
		Steps  string `db:"steps"`
	}
	_ = f.d.Select(ctx, &ups, `SELECT id, status, steps FROM updates`)
	for _, u := range ups {
		s.updates[u.ID] = [2]string{u.Status, u.Steps}
	}
	if f.snapshot == nil {
		f.snapshot = map[string]snap{}
	}
	f.snapshot[b.ID] = s
	return b, nil
}

func (f *fakeBackups) Get(ctx context.Context, id string) (backup.Backup, error) {
	return backup.Backup{}, backup.ErrNotFound
}

func (f *fakeBackups) List(ctx context.Context, kind string, limit int) ([]backup.Backup, error) {
	return []backup.Backup{{ID: "older", Kind: backup.KindScheduled, Status: backup.StatusDone, Bytes: 18_100_000_000}}, nil
}

func (f *fakeBackups) Restore(ctx context.Context, id, actorID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restored = append(f.restored, id)
	if f.failRestore != nil {
		return f.failRestore
	}
	s, ok := f.snapshot[id]
	if !ok {
		return backup.ErrNotFound
	}
	for k, v := range s.settings {
		if _, err := f.d.Exec(ctx, `UPDATE instance_settings SET value = ? WHERE key = ?`, v, k); err != nil {
			return err
		}
	}
	for idv, st := range s.updates {
		if _, err := f.d.Exec(ctx, `UPDATE updates SET status = ?, steps = ? WHERE id = ?`, st[0], st[1], idv); err != nil {
			return err
		}
	}
	_ = os.Remove(filepath.Join(f.dataDir, "fake-migrated"))
	return nil
}

func (f *fakeBackups) Delete(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	_, err := f.d.Exec(ctx, `UPDATE backups SET status = 'deleted' WHERE id = ?`, id)
	return err
}

// env is one test server with the update module wired to local fakes.
type env struct {
	t         *testing.T
	ta        *testutil.TestApp
	m         *update.Module
	ctx       context.Context
	backups   *fakeBackups
	release   *releaseServer
	exe       string
	priv      ed25519.PrivateKey
	pub       ed25519.PublicKey
	restarted chan string
	hooks     update.TestHooks
}

type envOpt func(*env)

func withHooks(fn func(*update.TestHooks)) envOpt { return func(e *env) { fn(&e.hooks) } }

func newEnv(t *testing.T, dialect string, opts ...envOpt) *env {
	t.Helper()
	ta := testutil.NewTestApp(t, testutil.WithDialect(dialect), testutil.WithEnv("MV_ALLOW_PRIVATE_PROVIDER_URLS", "true"))
	var m *update.Module
	for _, mod := range ta.Modules {
		if um, ok := mod.(*update.Module); ok {
			m = um
		}
	}
	if m == nil {
		t.Fatal("update module not wired")
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	e := &env{t: t, ta: ta, m: m, ctx: context.Background(), release: newReleaseServer(t), priv: priv, pub: pub,
		restarted: make(chan string, 1)}
	e.backups = &fakeBackups{d: ta.Deps.DB, dataDir: ta.Deps.Cfg.DataDir}
	binDir := t.TempDir()
	e.exe = filepath.Join(binDir, "mangaveyra")
	if err := os.WriteFile(e.exe, []byte("old binary 1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.hooks = update.TestHooks{Keys: []ed25519.PublicKey{pub}, Version: "1.0.0", Backups: e.backups, Executable: e.exe,
		HealthTimeout: 20 * time.Second, Restart: func(exe string) { e.restarted <- exe }}
	off := false
	e.hooks.Container = &off
	for _, o := range opts {
		o(e)
	}
	update.SetTestHooks(m, e.hooks)
	t.Cleanup(m.Wait)
	return e
}

func (e *env) enableChecks(channel string) {
	e.t.Helper()
	if channel == "" {
		channel = update.ChannelStable
	}
	if err := update.SaveConfig(e.m, e.ctx, update.Config{Checks: true, ReleaseURL: e.release.URL, Channel: channel}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) offer() *update.Record {
	e.t.Helper()
	rec, err := update.CurrentOffer(e.m, e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return rec
}

func (e *env) attempt() *update.Record {
	e.t.Helper()
	rec, err := update.LatestAttempt(e.m, e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	if rec == nil {
		e.t.Fatal("no install attempt")
	}
	return rec
}

func (e *env) maintenance() bool {
	e.ta.Deps.Settings.Invalidate()
	return e.ta.Deps.Settings.MustInstance(e.ctx).Maintenance.Enabled
}

func (e *env) adminClient() *testutil.Client { return e.ta.Login(e.t, e.ta.Admin.Username) }

func form(kv ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Add(kv[i], kv[i+1])
	}
	return v.Encode()
}

func post(c *testutil.Client, path string, body string) *testutil.Response {
	return c.Do(http.MethodPost, path, strings.NewReader(body), "application/x-www-form-urlencoded")
}

// failingTransport fails the test on any outbound request.
type failingTransport struct{ t *testing.T }

func (f failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Errorf("unexpected outbound request to %s", r.URL)
	return nil, errors.New("no network in this test")
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}
