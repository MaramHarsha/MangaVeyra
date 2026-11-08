package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/config"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
)

func testModule(t *testing.T, c *http.Client) *Module {
	t.Helper()
	cfg, err := config.Load("", func(k string) string {
		if k == "MV_DATA_DIR" {
			return t.TempDir()
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	return &Module{d: &core.Deps{Cfg: cfg}, hooks: hooks{Client: c}, children: map[*exec.Cmd]struct{}{}}
}

func TestDownloadVerifiesSizeAndChecksum(t *testing.T) {
	payload := bytes.Repeat([]byte("mangaveyra-binary "), 4096)
	var serve func(w http.ResponseWriter)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serve(w) }))
	defer srv.Close()
	m := testModule(t, srv.Client())
	ctx := context.Background()
	good := Asset{URL: srv.URL + "/a", SHA256: sum(payload), Size: int64(len(payload)), Format: FormatBinary}

	serve = func(w http.ResponseWriter) { _, _ = w.Write(payload) }
	var seen int64
	out, err := m.download(ctx, good, t.TempDir(), func(done, total int64) { seen = done })
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if b, _ := os.ReadFile(out); !bytes.Equal(b, payload) || seen != good.Size {
		t.Fatalf("downloaded %d bytes, progress %d", len(b), seen)
	}
	if fi, _ := os.Stat(out); runtime.GOOS != "windows" && fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("the binary is not executable: %v", fi.Mode())
	}

	// Truncated: the server sends half the file.
	serve = func(w http.ResponseWriter) {
		w.(http.Flusher).Flush()
		_, _ = w.Write(payload[:len(payload)/2])
	}
	if _, err := m.download(ctx, good, t.TempDir(), nil); !errors.Is(err, ErrSize) || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("truncated download: %v", err)
	}
	// Larger than signed: refused while reading.
	serve = func(w http.ResponseWriter) {
		w.(http.Flusher).Flush()
		_, _ = w.Write(append(payload, 'x'))
	}
	if _, err := m.download(ctx, good, t.TempDir(), nil); !errors.Is(err, ErrSize) {
		t.Fatalf("oversized download: %v", err)
	}
	// Tampered: same size, different bytes.
	tampered := append([]byte{}, payload...)
	tampered[10] ^= 1
	serve = func(w http.ResponseWriter) { _, _ = w.Write(tampered) }
	if _, err := m.download(ctx, good, t.TempDir(), nil); !errors.Is(err, ErrChecksum) {
		t.Fatalf("tampered download: %v", err)
	}
}

func tarball(t *testing.T, entries ...tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range entries {
		body := []byte(strings.Repeat("x", int(h.Size)))
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Typeflag != tar.TypeReg {
			h.Size = 0
			body = nil
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(body)
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func TestExtractBinary(t *testing.T) {
	dir := t.TempDir()
	write := func(b []byte) string {
		p := filepath.Join(dir, "a.tar.gz")
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ok := write(tarball(t, tar.Header{Name: "README.md", Size: 3, Mode: 0o644}, tar.Header{Name: "mangaveyra-1.0.2/mangaveyra", Size: 7, Mode: 0o755}))
	out := filepath.Join(dir, "out")
	if err := extractBinary(ok, out); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if b, _ := os.ReadFile(out); string(b) != "xxxxxxx" {
		t.Fatalf("extracted %q", b)
	}
	for name, entries := range map[string][]tar.Header{
		"traversal": {{Name: "../mangaveyra", Size: 3}},
		"symlink":   {{Name: "mangaveyra", Typeflag: tar.TypeSymlink, Linkname: "/bin/sh"}},
		"missing":   {{Name: "other", Size: 3}},
	} {
		_ = os.Remove(out)
		if err := extractBinary(write(tarball(t, entries...)), out); err == nil {
			t.Errorf("%s: extracted", name)
		}
	}
}

func TestSwapExecutableKeepsPrevious(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no self-update on Windows")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "mangaveyra")
	next := filepath.Join(t.TempDir(), "new")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(next, []byte("new"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := checkReplaceable(exe); err != nil {
		t.Fatalf("replaceable: %v", err)
	}
	if err := swapExecutable(exe, next); err != nil {
		t.Fatalf("swap: %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new" {
		t.Fatalf("exe = %q", b)
	}
	if b, _ := os.ReadFile(exe + ".prev"); string(b) != "old" {
		t.Fatalf("prev = %q", b)
	}
	if fi, _ := os.Stat(exe); fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v", fi.Mode())
	}
	if _, err := os.Stat(exe + ".new"); !os.IsNotExist(err) {
		t.Fatal("the staged copy must not stay behind")
	}
	// A read-only directory is refused before anything changes.
	ro := t.TempDir()
	roExe := filepath.Join(ro, "mangaveyra")
	_ = os.WriteFile(roExe, []byte("old"), 0o755)
	_ = os.Chmod(ro, 0o555)
	defer os.Chmod(ro, 0o755)
	if os.Geteuid() != 0 {
		if err := checkReplaceable(roExe); err == nil {
			t.Fatal("a read-only directory must be refused")
		}
	}
}

func TestOutputParsers(t *testing.T) {
	status := "VERSION  FILE  APPLIED\n0001_core  0001_core.sql  2026-01-01 00:00:00\n0601_a  0601_a.sql  pending\n0602_b  0602_b.sql  pending\n"
	if n := countPending(status); n != 2 {
		t.Fatalf("countPending = %d", n)
	}
	if n := countApplied("applied 3 migration(s): a, b, c"); n != 3 {
		t.Fatalf("countApplied = %d", n)
	}
	if n := countApplied("database is up to date"); n != 0 {
		t.Fatalf("up to date = %d", n)
	}
	if n := migrationNumber("error: db: migrate 0602_fake.sql: statement 1: boom"); n != "0602" {
		t.Fatalf("migrationNumber = %q", n)
	}
	if s := redact("dial postgres://mv:s3cret@db:5432/mv failed"); strings.Contains(s, "s3cret") {
		t.Fatalf("redact = %q", s)
	}
}

func TestRestartMethod(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	if m := restartMethod(env(nil)); m != "exec" {
		t.Fatalf("manual start = %s", m)
	}
	if m := restartMethod(env(map[string]string{"INVOCATION_ID": "abc"})); m != "exit" {
		t.Fatalf("systemd = %s", m)
	}
	if m := restartMethod(env(map[string]string{"INVOCATION_ID": "abc", EnvRestart: "exec"})); m != "exec" {
		t.Fatalf("forced exec = %s", m)
	}
	if m := restartMethod(env(map[string]string{EnvRestart: "exit"})); m != "exit" {
		t.Fatalf("forced exit = %s", m)
	}
}
