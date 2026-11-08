package importer

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/nwaples/rardecode/v2"

	"github.com/MaramHarsha/MangaVeyra/internal/storage"
)

// Limits bound what an import may read (ADR 0013 §5). Archives are
// attacker-shaped input: every limit refuses the import with a clear reason.
type Limits struct {
	// MaxEntries is the number of entries (files and folders) one archive may hold.
	MaxEntries int
	// MaxPages is the number of images one import may turn into pages (one chapter).
	MaxPages int
	// MaxEntryBytes is the largest uncompressed entry.
	MaxEntryBytes int64
	// MaxTotalBytes is the largest uncompressed total.
	MaxTotalBytes int64
	// MaxSourceBytes is the largest uploaded file.
	MaxSourceBytes int64
	// Image bounds decoding (the existing upload limits).
	Image storage.ImageLimits
	// PDFTimeout bounds pdftoppm.
	PDFTimeout time.Duration
}

// DefaultLimits are used by the module.
var DefaultLimits = Limits{
	MaxEntries:     4096,
	MaxPages:       256, // pagestore's page limit per chapter
	MaxEntryBytes:  64 << 20,
	MaxTotalBytes:  2 << 30,
	MaxSourceBytes: 2 << 30,
	Image:          storage.DefaultImageLimits,
	PDFTimeout:     10 * time.Minute,
}

// Refusal codes (imports.result.code; localized as importer.error.<code>).
const (
	CodeTooManyEntries = "too_many_entries"
	CodeTooManyPages   = "too_many_pages"
	CodeEntryTooLarge  = "entry_too_large"
	CodeTooLarge       = "too_large"
	CodeUnsafePath     = "unsafe_path"
	CodeLink           = "link"
	CodeEncrypted      = "encrypted"
	CodeDamaged        = "damaged"
	CodeNoImages       = "no_images"
	CodePDFUnavailable = "pdf_unavailable"
	CodeInternal       = "internal"
)

// refusal is an import that cannot proceed; Code is shown to the member.
type refusal struct {
	Code   string
	Detail string
}

func (r *refusal) Error() string { return "importer: " + r.Code + ": " + r.Detail }

func refuse(code, format string, a ...any) error {
	return &refusal{Code: code, Detail: fmt.Sprintf(format, a...)}
}

// image is one accepted page image, written to the work directory.
type pageImage struct {
	Name        string
	Path        string
	ContentType string
	Width       int
	Height      int
	Bytes       int64
}

// extraction is the result of reading a source.
type extraction struct {
	Images  []pageImage
	Skipped int
}

// safeName normalises an entry name and refuses absolute paths, drive
// letters and parent-directory components.
func safeName(name string) (string, error) {
	n := strings.ReplaceAll(name, "\\", "/")
	if n == "" || strings.ContainsRune(n, 0) {
		return "", refuse(CodeUnsafePath, "an entry has no name")
	}
	if strings.HasPrefix(n, "/") || (len(n) >= 2 && n[1] == ':' && unicode.IsLetter(rune(n[0]))) {
		return "", refuse(CodeUnsafePath, "%q is an absolute path", name)
	}
	for _, part := range strings.Split(n, "/") {
		if part == ".." {
			return "", refuse(CodeUnsafePath, "%q leaves the archive", name)
		}
	}
	return path.Clean(n), nil
}

// ignored reports system files that are skipped silently (macOS resource forks, thumbnails).
func ignored(name string) bool {
	base := path.Base(name)
	return strings.HasPrefix(name, "__MACOSX/") || strings.HasPrefix(base, ".") || strings.EqualFold(base, "Thumbs.db") ||
		strings.EqualFold(base, "desktop.ini")
}

// naturalLess orders "page2" before "page10" (case-insensitive, by path).
func naturalLess(a, b string) bool {
	ca, cb := chunks(strings.ToLower(a)), chunks(strings.ToLower(b))
	for i := 0; i < len(ca) && i < len(cb); i++ {
		x, y := ca[i], cb[i]
		if x == y {
			continue
		}
		xd, yd := isDigits(x), isDigits(y)
		if xd && yd {
			xs, ys := strings.TrimLeft(x, "0"), strings.TrimLeft(y, "0")
			if len(xs) != len(ys) {
				return len(xs) < len(ys)
			}
			if xs != ys {
				return xs < ys
			}
			return len(x) < len(y)
		}
		return x < y
	}
	if len(ca) != len(cb) {
		return len(ca) < len(cb)
	}
	return a < b
}

func isDigits(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) < 0
}

func chunks(s string) []string {
	var out []string
	start := 0
	for i := 1; i <= len(s); i++ {
		if i == len(s) || (s[i] >= '0' && s[i] <= '9') != (s[i-1] >= '0' && s[i-1] <= '9') || s[i] == '/' || s[i-1] == '/' {
			out = append(out, s[start:i])
			start = i
		}
	}
	return out
}

// accept checks one entry's bytes and writes an image to dir.
func (x *extraction) accept(name string, data []byte, dir string, lim Limits) error {
	if ignored(name) {
		return nil
	}
	ct := storage.DetectContentType(data)
	if storage.CheckType(ct, storage.ImageTypes...) != nil {
		x.Skipped++
		return nil
	}
	cfg, _, err := storage.ImageConfig(data, lim.Image)
	if err != nil {
		x.Skipped++
		return nil
	}
	if _, _, err := storage.DecodeImage(data, lim.Image); err != nil {
		x.Skipped++
		return nil
	}
	if len(x.Images) >= lim.MaxPages {
		return refuse(CodeTooManyPages, "more than %d images", lim.MaxPages)
	}
	p := filepath.Join(dir, fmt.Sprintf("%05d%s", len(x.Images), storage.ExtensionFor(ct)))
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return err
	}
	x.Images = append(x.Images, pageImage{Name: name, Path: p, ContentType: ct, Width: cfg.Width, Height: cfg.Height, Bytes: int64(len(data))})
	return nil
}

func readEntry(r io.Reader, name string, lim Limits) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, lim.MaxEntryBytes+1))
	if err != nil {
		return nil, refuse(CodeDamaged, "%q cannot be read (%v)", name, err)
	}
	if int64(len(data)) > lim.MaxEntryBytes {
		return nil, refuse(CodeEntryTooLarge, "%q is larger than %d MB", name, lim.MaxEntryBytes>>20)
	}
	return data, nil
}

// extractZip reads a CBZ or zip (also the zip an image upload is stored as).
func extractZip(ctx context.Context, src, dir string, lim Limits) (*extraction, error) {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return nil, refuse(CodeDamaged, "not a zip archive (%v)", err)
	}
	defer zr.Close()
	if len(zr.File) > lim.MaxEntries {
		return nil, refuse(CodeTooManyEntries, "%d entries (at most %d)", len(zr.File), lim.MaxEntries)
	}
	type entry struct {
		name string
		f    *zip.File
	}
	var entries []entry
	var total uint64
	for _, f := range zr.File {
		name, err := safeName(f.Name)
		if err != nil {
			return nil, err
		}
		mode := f.Mode()
		if mode&fs.ModeSymlink != 0 {
			return nil, refuse(CodeLink, "%q is a link", f.Name)
		}
		if f.FileInfo().IsDir() || strings.HasSuffix(f.Name, "/") {
			continue
		}
		if !mode.IsRegular() {
			return nil, refuse(CodeLink, "%q is not a regular file", f.Name)
		}
		if f.Flags&0x1 != 0 {
			return nil, refuse(CodeEncrypted, "%q is encrypted", f.Name)
		}
		if f.UncompressedSize64 > uint64(lim.MaxEntryBytes) {
			return nil, refuse(CodeEntryTooLarge, "%q is larger than %d MB", f.Name, lim.MaxEntryBytes>>20)
		}
		total += f.UncompressedSize64
		if total > uint64(lim.MaxTotalBytes) {
			return nil, refuse(CodeTooLarge, "the archive unpacks to more than %d MB", lim.MaxTotalBytes>>20)
		}
		entries = append(entries, entry{name: name, f: f})
	}
	sort.SliceStable(entries, func(i, j int) bool { return naturalLess(entries[i].name, entries[j].name) })
	x := &extraction{}
	var read int64
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if ignored(e.name) {
			continue
		}
		rc, err := e.f.Open()
		if err != nil {
			return nil, refuse(CodeDamaged, "%q cannot be read (%v)", e.name, err)
		}
		data, err := readEntry(rc, e.name, lim)
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		read += int64(len(data))
		if read > lim.MaxTotalBytes {
			return nil, refuse(CodeTooLarge, "the archive unpacks to more than %d MB", lim.MaxTotalBytes>>20)
		}
		if err := x.accept(e.name, data, dir, lim); err != nil {
			return nil, err
		}
	}
	return x, nil
}

// extractRAR reads a CBR. RAR is read in archive order (solid archives
// cannot seek), so accepted images are buffered in dir and sorted afterwards.
func extractRAR(ctx context.Context, src, dir string, lim Limits) (*extraction, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rr, err := rardecode.NewReader(f, rardecode.MaxDictionarySize(64<<20))
	if err != nil {
		if errors.Is(err, rardecode.ErrArchiveEncrypted) {
			return nil, refuse(CodeEncrypted, "the archive is encrypted")
		}
		return nil, refuse(CodeDamaged, "not a RAR archive (%v)", err)
	}
	type held struct {
		name string
		path string // "" for an entry that is not an image
	}
	var keep []held
	var total int64
	entries := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h, err := rr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if errors.Is(err, rardecode.ErrArchiveEncrypted) || errors.Is(err, rardecode.ErrArchivedFileEncrypted) {
				return nil, refuse(CodeEncrypted, "the archive is encrypted")
			}
			return nil, refuse(CodeDamaged, "the archive is damaged (%v)", err)
		}
		entries++
		if entries > lim.MaxEntries {
			return nil, refuse(CodeTooManyEntries, "more than %d entries", lim.MaxEntries)
		}
		name, err := safeName(h.Name)
		if err != nil {
			return nil, err
		}
		if h.LinkType != 0 || h.Mode()&fs.ModeSymlink != 0 {
			return nil, refuse(CodeLink, "%q is a link", h.Name)
		}
		if h.IsDir {
			continue
		}
		if h.Encrypted {
			return nil, refuse(CodeEncrypted, "%q is encrypted", h.Name)
		}
		if !h.UnKnownSize && h.UnPackedSize > lim.MaxEntryBytes {
			return nil, refuse(CodeEntryTooLarge, "%q is larger than %d MB", h.Name, lim.MaxEntryBytes>>20)
		}
		if ignored(name) {
			continue
		}
		data, err := readEntry(rr, name, lim)
		if err != nil {
			return nil, err
		}
		total += int64(len(data))
		if total > lim.MaxTotalBytes {
			return nil, refuse(CodeTooLarge, "the archive unpacks to more than %d MB", lim.MaxTotalBytes>>20)
		}
		if storage.CheckType(storage.DetectContentType(data), storage.ImageTypes...) != nil {
			keep = append(keep, held{name: name})
			continue
		}
		// Candidate images wait on disk until the archive is sorted.
		p := filepath.Join(dir, fmt.Sprintf("rar-%05d", entries))
		if err := os.WriteFile(p, data, 0o600); err != nil {
			return nil, err
		}
		keep = append(keep, held{name: name, path: p})
	}
	sort.SliceStable(keep, func(i, j int) bool { return naturalLess(keep[i].name, keep[j].name) })
	x := &extraction{}
	for _, k := range keep {
		if k.path == "" {
			x.Skipped++
			continue
		}
		data, err := os.ReadFile(k.path)
		if err != nil {
			return nil, err
		}
		_ = os.Remove(k.path)
		if err := x.accept(k.name, data, dir, lim); err != nil {
			return nil, err
		}
	}
	return x, nil
}

// PDFTool is the pdftoppm binary when installed (PDF import is Soon otherwise).
func PDFTool() string {
	p, err := exec.LookPath("pdftoppm")
	if err != nil {
		return ""
	}
	return p
}

// extractPDF renders pages with pdftoppm (scaled so the long side is 3000 px).
func extractPDF(ctx context.Context, src, dir string, lim Limits) (*extraction, error) {
	tool := PDFTool()
	if tool == "" {
		return nil, refuse(CodePDFUnavailable, "pdftoppm is not installed")
	}
	out := filepath.Join(dir, "pdf")
	if err := os.MkdirAll(out, 0o700); err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, lim.PDFTimeout)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(cctx, tool, "-png", "-scale-to", "3000", "-l", strconv.Itoa(lim.MaxPages+1), src, filepath.Join(out, "page"))
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard
	if err := cmd.Run(); err != nil {
		if cctx.Err() != nil && ctx.Err() == nil {
			return nil, refuse(CodeDamaged, "rendering the PDF took too long")
		}
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, refuse(CodeDamaged, "the PDF cannot be rendered (%s)", msg)
	}
	files, err := os.ReadDir(out)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, f := range files {
		if f.Type().IsRegular() {
			names = append(names, f.Name())
		}
	}
	if len(names) > lim.MaxPages {
		return nil, refuse(CodeTooManyPages, "more than %d pages", lim.MaxPages)
	}
	sort.SliceStable(names, func(i, j int) bool { return naturalLess(names[i], names[j]) })
	x := &extraction{}
	var total int64
	for _, n := range names {
		p := filepath.Join(out, n)
		fi, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if fi.Size() > lim.MaxEntryBytes {
			return nil, refuse(CodeEntryTooLarge, "page %s is larger than %d MB", n, lim.MaxEntryBytes>>20)
		}
		total += fi.Size()
		if total > lim.MaxTotalBytes {
			return nil, refuse(CodeTooLarge, "the PDF renders to more than %d MB", lim.MaxTotalBytes>>20)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		_ = os.Remove(p)
		if err := x.accept(n, data, dir, lim); err != nil {
			return nil, err
		}
	}
	return x, nil
}

// extract dispatches on the import kind.
func extract(ctx context.Context, kind, src, dir string, lim Limits) (*extraction, error) {
	var x *extraction
	var err error
	switch kind {
	case KindCBZ, KindImages:
		x, err = extractZip(ctx, src, dir, lim)
	case KindCBR:
		x, err = extractRAR(ctx, src, dir, lim)
	case KindPDF:
		x, err = extractPDF(ctx, src, dir, lim)
	default:
		return nil, refuse(CodeDamaged, "unknown import kind %q", kind)
	}
	if err != nil {
		return nil, err
	}
	if len(x.Images) == 0 {
		return nil, refuse(CodeNoImages, "no PNG, JPEG, WebP or GIF images were found")
	}
	return x, nil
}
