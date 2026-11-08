package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// binaryName is the executable inside a tar.gz asset.
const binaryName = "mangaveyra"

// updatesDir is where downloads are kept while an update runs.
func (m *Module) updatesDir(version string) string {
	return filepath.Join(m.d.Cfg.DataDir, "updates", version)
}

// sweepDownloads removes download directories under $MV_DATA_DIR/updates
// that nothing touched since cutoff: an install the server stopped in the
// middle of leaves them behind. It runs only while no install is active.
func (m *Module) sweepDownloads(cutoff time.Time) {
	root := filepath.Join(m.d.Cfg.DataDir, "updates")
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		if !e.IsDir() || newestChange(dir).After(cutoff) {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			m.d.Log.Warn("update: remove a leftover download", "dir", dir, "err", err)
		}
	}
}

// newestChange is the latest modification time of dir and its files.
func newestChange(dir string) time.Time {
	var newest time.Time
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
		return nil
	})
	return newest
}

// executable returns the resolved path of the running binary.
func (m *Module) executable() (string, error) {
	if m.hooks.Executable != "" {
		return m.hooks.Executable, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// checkReplaceable fails early when the server cannot replace its own
// executable (read-only directory, e.g. systemd's ProtectSystem=strict).
func checkReplaceable(exe string) error {
	if runtime.GOOS == "windows" {
		return errors.New("this server cannot replace its own program on Windows")
	}
	f, err := os.CreateTemp(filepath.Dir(exe), ".mangaveyra-update-*")
	if err != nil {
		return fmt.Errorf("cannot replace %s: %w", exe, err)
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

type progressFunc func(done, total int64)

// downloadLimits bound a release download, so a release server that accepts
// the connection and then stalls cannot keep the update (and the maintenance
// page) waiting: the response headers must arrive within Header, no pause
// between bytes may last longer than Idle, and the whole download must finish
// within Base plus the signed size at MinRate bytes per second.
type downloadLimits struct {
	Header  time.Duration
	Idle    time.Duration
	Base    time.Duration
	MinRate int64
}

func defaultDownloadLimits() downloadLimits {
	return downloadLimits{Header: 30 * time.Second, Idle: time.Minute, Base: 2 * time.Minute, MinRate: 64 << 10}
}

// overall is the deadline for a download of size bytes.
func (l downloadLimits) overall(size int64) time.Duration {
	return l.Base + time.Duration(size/max(1, l.MinRate)+1)*time.Second
}

func (m *Module) downloadLimits() downloadLimits {
	l, h := defaultDownloadLimits(), m.hooks.Download
	if h.Header > 0 {
		l.Header = h.Header
	}
	if h.Idle > 0 {
		l.Idle = h.Idle
	}
	if h.Base > 0 {
		l.Base = h.Base
	}
	if h.MinRate > 0 {
		l.MinRate = h.MinRate
	}
	return l
}

// download fetches a's file into dir, refusing anything larger than the
// signed size, and checks the signed size and SHA-256. It returns the path of
// the verified executable.
func (m *Module) download(ctx context.Context, a Asset, dir string, progress progressFunc) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	lim := m.downloadLimits()
	total := lim.overall(a.Size)
	ctx, cancelAll := context.WithTimeoutCause(ctx, total, fmt.Errorf("the download took longer than %s", total))
	defer cancelAll()
	// One context for the request and its body: the watchdogs cancel it with
	// the reason, which the error then reports.
	rctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	headers := time.AfterFunc(lim.Header, func() {
		cancel(fmt.Errorf("the release server did not answer within %s", lim.Header))
	})
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, a.URL, nil)
	if err != nil {
		headers.Stop()
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	client := m.client(0)
	resp, err := client.Do(req)
	headers.Stop()
	if err != nil {
		return "", downloadErr(rctx, err)
	}
	defer resp.Body.Close()
	var n int64
	idle := time.AfterFunc(lim.Idle, func() {
		cancel(fmt.Errorf("the download stalled: no data for %s", lim.Idle))
	})
	defer idle.Stop()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s", a.URL, resp.Status)
	}
	if resp.ContentLength > a.Size {
		return "", fmt.Errorf("%w: the server announces %d bytes, the manifest %d", ErrSize, resp.ContentLength, a.Size)
	}
	tmp := filepath.Join(dir, "download.part")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	buf := make([]byte, 256<<10)
	body := io.LimitReader(resp.Body, a.Size+1)
	last := time.Time{}
	for {
		k, rerr := body.Read(buf)
		if k > 0 {
			idle.Reset(lim.Idle)
			n += int64(k)
			if n > a.Size {
				_ = f.Close()
				return "", fmt.Errorf("%w: more than %d bytes", ErrSize, a.Size)
			}
			h.Write(buf[:k])
			if _, err := f.Write(buf[:k]); err != nil {
				_ = f.Close()
				return "", err
			}
			if progress != nil && time.Since(last) > 500*time.Millisecond {
				progress(n, a.Size)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_ = f.Close()
			return "", fmt.Errorf("download interrupted after %d of %d bytes: %w", n, a.Size, downloadErr(rctx, rerr))
		}
	}
	idle.Stop()
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if progress != nil {
		progress(n, a.Size)
	}
	if n != a.Size {
		return "", fmt.Errorf("%w: got %d of %d bytes (truncated)", ErrSize, n, a.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.SHA256 {
		return "", fmt.Errorf("%w: sha256 %s, signed %s", ErrChecksum, got, a.SHA256)
	}
	out := filepath.Join(dir, binaryName)
	_ = os.Remove(out)
	switch a.Format {
	case FormatTarGz:
		if err := extractBinary(tmp, out); err != nil {
			return "", err
		}
		_ = os.Remove(tmp)
	default:
		if err := os.Rename(tmp, out); err != nil {
			return "", err
		}
	}
	if err := os.Chmod(out, 0o755); err != nil {
		return "", err
	}
	return out, nil
}

// downloadErr reports why a watchdog ended the download (no answer, a stall,
// the overall deadline) instead of the bare "context canceled".
func downloadErr(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}
	return err
}

// extractBinary copies the regular file named "mangaveyra" out of a gzip
// tarball. Other entries are skipped; links, traversal and oversized entries
// are refused.
func extractBinary(archive, out string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("update: the tarball is not gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("update: the tarball has no %q file", binaryName)
		}
		if err != nil {
			return fmt.Errorf("update: read tarball: %w", err)
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if strings.HasPrefix(name, "../") || path.IsAbs(name) || name == ".." {
			return fmt.Errorf("update: the tarball entry %q leaves the archive", h.Name)
		}
		if path.Base(name) != binaryName {
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return fmt.Errorf("update: %q in the tarball is not a regular file", h.Name)
		}
		if h.Size <= 0 || h.Size > MaxAssetBytes {
			return fmt.Errorf("update: %q in the tarball has size %d", h.Name, h.Size)
		}
		w, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
		if err != nil {
			return err
		}
		n, err := io.Copy(w, io.LimitReader(tr, h.Size+1))
		if err == nil && n != h.Size {
			err = fmt.Errorf("update: %q is truncated", h.Name)
		}
		if err == nil {
			err = w.Sync()
		}
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		return err
	}
}

// swapExecutable replaces exe with next atomically: next is copied beside
// exe (same filesystem), the current binary is kept as exe.prev (a hard link,
// or a copy where links are not supported) and the copy is renamed over exe.
// The running process keeps its open image; the next start uses the new one.
func swapExecutable(exe, next string) error {
	dir := filepath.Dir(exe)
	staged := exe + ".new"
	_ = os.Remove(staged)
	if err := copyFile(next, staged, 0o755); err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("stage the new binary in %s: %w", dir, err)
	}
	prev := exe + ".prev"
	_ = os.Remove(prev)
	if err := os.Link(exe, prev); err != nil {
		if err := copyFile(exe, prev, 0o755); err != nil {
			_ = os.Remove(staged)
			return fmt.Errorf("keep the current binary as %s: %w", prev, err)
		}
	}
	if err := os.Rename(staged, exe); err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("replace %s: %w", exe, err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}
