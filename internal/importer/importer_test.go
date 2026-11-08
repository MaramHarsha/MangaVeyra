package importer_test

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/importer"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

// pagePNG is a w×h PNG whose top-left pixel encodes n (page order checks).
func pagePNG(t testing.TB, n, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(n), G: uint8(y * 255 / h), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type entry struct {
	name string
	data []byte
	mode fs.FileMode
}

func makeZip(t testing.TB, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(e.data)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// makeRAR writes a RAR 1.5 archive with stored (uncompressed) files.
func makeRAR(entries []entry) []byte {
	le := binary.LittleEndian
	var buf bytes.Buffer
	buf.Write([]byte{0x52, 0x61, 0x72, 0x21, 0x1a, 0x07, 0x00})
	block := func(head []byte) {
		var crc [2]byte
		le.PutUint16(crc[:], uint16(crc32.ChecksumIEEE(head)))
		buf.Write(crc[:])
		buf.Write(head)
	}
	block([]byte{0x73, 0, 0, 13, 0, 0, 0, 0, 0, 0, 0})
	for _, e := range entries {
		var h bytes.Buffer
		name := []byte(e.name)
		h.WriteByte(0x74)
		_ = binary.Write(&h, le, uint16(0x8000))
		_ = binary.Write(&h, le, uint16(32+len(name)))
		_ = binary.Write(&h, le, uint32(len(e.data)))
		_ = binary.Write(&h, le, uint32(len(e.data)))
		h.WriteByte(3) // host OS: Unix
		_ = binary.Write(&h, le, crc32.ChecksumIEEE(e.data))
		_ = binary.Write(&h, le, uint32(0x5a210000))
		h.WriteByte(20)
		h.WriteByte(0x30) // stored
		_ = binary.Write(&h, le, uint16(len(name)))
		attrs := uint32(0o100644)
		if e.mode&fs.ModeSymlink != 0 {
			attrs = 0o120777
		}
		_ = binary.Write(&h, le, attrs)
		h.Write(name)
		block(h.Bytes())
		buf.Write(e.data)
	}
	block([]byte{0x7b, 0x00, 0x40, 7, 0})
	return buf.Bytes()
}

// lyingZip holds n zero bytes that its header says are 100 bytes.
func lyingZip(t testing.TB, n int) []byte {
	t.Helper()
	var comp bytes.Buffer
	fw, _ := flate.NewWriter(&comp, flate.BestCompression)
	_, _ = fw.Write(make([]byte, n))
	_ = fw.Close()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateRaw(&zip.FileHeader{Name: "bomb.png", Method: zip.Deflate, CompressedSize64: uint64(comp.Len()), UncompressedSize64: 100})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(comp.Bytes())
	_ = zw.Close()
	return buf.Bytes()
}

func writeFile(t testing.TB, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func testLimits() importer.Limits {
	l := importer.DefaultLimits
	l.MaxEntries, l.MaxPages, l.MaxEntryBytes, l.MaxTotalBytes = 20, 6, 256<<10, 1<<20
	return l
}

func TestEntryNames(t *testing.T) {
	for _, bad := range []string{"../evil.png", "a/../../evil.png", "/etc/passwd", `C:\Windows\x.png`, `..\x.png`, ""} {
		if _, err := importer.SafeName(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	if n, err := importer.SafeName(`vol1\p01.png`); err != nil || n != "vol1/p01.png" {
		t.Fatalf("windows separators %q %v", n, err)
	}
	names := []string{"p10.png", "p2.png", "P1.png", "ch2/p1.png", "ch1/p10.png", "ch1/p2.png", "p02b.png"}
	slices.SortFunc(names, func(a, b string) int {
		if importer.NaturalLess(a, b) {
			return -1
		}
		if importer.NaturalLess(b, a) {
			return 1
		}
		return 0
	})
	want := []string{"ch1/p2.png", "ch1/p10.png", "ch2/p1.png", "P1.png", "p2.png", "p02b.png", "p10.png"}
	if !slices.Equal(names, want) {
		t.Fatalf("natural order %v, want %v", names, want)
	}
}

func TestArchiveLimits(t *testing.T) {
	ctx := context.Background()
	lim := testLimits()
	img := pagePNG(t, 1, 40, 60)
	cases := []struct {
		name string
		kind string
		data []byte
		code string
	}{
		{"zip bomb entry", importer.KindCBZ, makeZip(t, []entry{{name: "p1.png", data: img}, {name: "zeros.png", data: make([]byte, 2<<20)}}), importer.CodeEntryTooLarge},
		{"zip bomb total", importer.KindCBZ, makeZip(t, []entry{{name: "a.bin", data: make([]byte, 200<<10)}, {name: "b.bin", data: make([]byte, 200<<10)},
			{name: "c.bin", data: make([]byte, 200<<10)}, {name: "d.bin", data: make([]byte, 200<<10)}, {name: "e.bin", data: make([]byte, 200<<10)},
			{name: "f.bin", data: make([]byte, 200<<10)}}), importer.CodeTooLarge},
		{"traversal", importer.KindCBZ, makeZip(t, []entry{{name: "p1.png", data: img}, {name: "../../outside.png", data: img}}), importer.CodeUnsafePath},
		{"absolute", importer.KindCBZ, makeZip(t, []entry{{name: "/tmp/abs.png", data: img}}), importer.CodeUnsafePath},
		{"symlink", importer.KindCBZ, makeZip(t, []entry{{name: "p1.png", data: img}, {name: "link.png", data: []byte("/etc/passwd"), mode: fs.ModeSymlink | 0o777}}), importer.CodeLink},
		{"too many entries", importer.KindCBZ, makeZip(t, func() []entry {
			var es []entry
			for i := 0; i < 25; i++ {
				es = append(es, entry{name: fmt.Sprintf("n%02d.txt", i), data: []byte("x")})
			}
			return es
		}()), importer.CodeTooManyEntries},
		{"too many pages", importer.KindCBZ, makeZip(t, func() []entry {
			var es []entry
			for i := 0; i < 7; i++ {
				es = append(es, entry{name: fmt.Sprintf("p%02d.png", i), data: img})
			}
			return es
		}()), importer.CodeTooManyPages},
		{"no images", importer.KindCBZ, makeZip(t, []entry{{name: "readme.txt", data: []byte("Iron Koi")}}), importer.CodeNoImages},
		{"not a zip", importer.KindCBZ, []byte("Moth & Mortar"), importer.CodeDamaged},
		{"zip bomb lying header", importer.KindCBZ, lyingZip(t, 4<<20), importer.CodeDamaged},
		{"rar traversal", importer.KindCBR, makeRAR([]entry{{name: "../x.png", data: img}}), importer.CodeUnsafePath},
		{"rar symlink", importer.KindCBR, makeRAR([]entry{{name: "link.png", data: []byte("/etc/passwd"), mode: fs.ModeSymlink}}), importer.CodeLink},
		{"rar bomb entry", importer.KindCBR, makeRAR([]entry{{name: "big.png", data: make([]byte, 300<<10)}}), importer.CodeEntryTooLarge},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, code, err := importer.Extract(ctx, c.kind, writeFile(t, c.data), t.TempDir(), lim)
			if err == nil || code != c.code {
				t.Fatalf("code %q (%v), want %q", code, err, c.code)
			}
		})
	}
}

func TestArchiveOrderAndSkippedEntries(t *testing.T) {
	ctx := context.Background()
	lim := testLimits()
	names := []string{"vol/p10.png", "vol/p2.png", "vol/p1.png"}
	var es []entry
	for i, n := range names {
		es = append(es, entry{name: n, data: pagePNG(t, i, 30, 40)})
	}
	es = append(es, entry{name: "vol/notes.txt", data: []byte("Harbour Ghosts draft")}, entry{name: "__MACOSX/vol/._p1.png", data: []byte{0, 5, 22}},
		entry{name: "vol/broken.png", data: append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 40)...)}, entry{name: "vol/", data: nil})
	want := []string{"vol/p1.png", "vol/p2.png", "vol/p10.png"}
	for _, kind := range []string{importer.KindCBZ, importer.KindCBR} {
		data := makeZip(t, es)
		if kind == importer.KindCBR {
			var rs []entry
			for _, e := range es {
				if !strings.HasSuffix(e.name, "/") {
					rs = append(rs, e)
				}
			}
			data = makeRAR(rs)
		}
		got, skipped, _, err := importer.Extract(ctx, kind, writeFile(t, data), t.TempDir(), lim)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if !slices.Equal(got, want) || skipped != 2 {
			t.Fatalf("%s: pages %v skipped %d, want %v and 2 (text, broken image)", kind, got, skipped, want)
		}
	}
}

// upload posts files to /imports as a browser form would.
func upload(t *testing.T, c *testutil.Client, files map[string][]byte) *testutil.Response {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, name := range keys {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files"; filename=%q`, name))
		h.Set("Content-Type", "application/octet-stream")
		w, _ := mw.CreatePart(h)
		_, _ = w.Write(files[name])
	}
	_ = mw.Close()
	return c.Do(http.MethodPost, "/imports?return=/admin/backup", &body, mw.FormDataContentType())
}

func runJobs(t *testing.T, ta *testutil.TestApp) {
	t.Helper()
	for i := 0; i < 10; i++ {
		ran, err := ta.Deps.Executor.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			return
		}
	}
}

func recent(t *testing.T, ta *testutil.TestApp, userID string) []importer.Import {
	t.Helper()
	list, err := core.MustUse[importer.Service](ta.Deps).Recent(context.Background(), userID, 10)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func chapterPages(t *testing.T, ta *testutil.TestApp, chapterID string) []pagedoc.Page {
	t.Helper()
	pages, err := core.MustUse[pagestore.Service](ta.Deps).ChapterPages(context.Background(), chapterID)
	if err != nil {
		t.Fatal(err)
	}
	return pages
}

func firstPixel(t *testing.T, ta *testutil.TestApp, assetID string) uint8 {
	t.Helper()
	var key string
	if err := ta.Deps.DB.Get(context.Background(), &key, `SELECT key FROM assets WHERE id = ?`, assetID); err != nil {
		t.Fatal(err)
	}
	b, err := storage.ReadAll(context.Background(), ta.Deps.Store, key, 1<<26)
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := storage.DecodeImage(b, storage.DefaultImageLimits)
	if err != nil {
		t.Fatal(err)
	}
	r, _, _, _ := img.At(0, 0).RGBA()
	return uint8(r >> 8)
}

func TestImportCreatesPrivateProjectWithOnePanelPerPage(t *testing.T) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect))
			ctx := context.Background()
			member, c := ta.UserClient(t, auth.RoleCreator)
			var es []entry
			for _, n := range []int{3, 1, 2} {
				es = append(es, entry{name: fmt.Sprintf("Salt & Static/p%d.png", n), data: pagePNG(t, n, 60, 90)})
			}
			es = append(es, entry{name: "Salt & Static/credits.txt", data: []byte("original")})
			r := upload(t, c, map[string][]byte{"salt-static-vol1.cbz": makeZip(t, es), "notes.txt": []byte("not a comic")})
			if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/admin/backup" {
				t.Fatalf("upload %d %s", r.StatusCode, r.Body)
			}
			list := recent(t, ta, member.ID)
			if len(list) != 1 || list[0].Status != importer.StatusQueued || list[0].Kind != importer.KindCBZ || list[0].Filename != "salt-static-vol1.cbz" {
				t.Fatalf("queued %+v", list)
			}
			runJobs(t, ta)
			imp := recent(t, ta, member.ID)[0]
			if imp.Status != importer.StatusDone || imp.Pages != 3 || imp.Result.Skipped != 1 || imp.Progress != 1 || imp.ProjectID == "" {
				t.Fatalf("import %+v", imp)
			}
			// The project is private: owned by the importer, no other member, nothing published.
			var owner, title string
			if err := ta.Deps.DB.QueryRow(ctx, `SELECT owner_id, title FROM projects WHERE id = ?`, imp.ProjectID).Scan(&owner, &title); err != nil {
				t.Fatal(err)
			}
			var members, series int
			_ = ta.Deps.DB.Get(ctx, &members, `SELECT COUNT(*) FROM project_members WHERE project_id = ?`, imp.ProjectID)
			_ = ta.Deps.DB.Get(ctx, &series, `SELECT COUNT(*) FROM series WHERE project_id = ?`, imp.ProjectID)
			if owner != member.ID || title != "salt-static-vol1" || members != 1 || series != 0 {
				t.Fatalf("project owner=%s title=%q members=%d series=%d", owner, title, members, series)
			}
			var label string
			_ = ta.Deps.DB.Get(ctx, &label, `SELECT ai_label FROM chapters WHERE id = ?`, imp.Result.ChapterID)
			if !strings.Contains(label, `"art":"human"`) {
				t.Fatalf("AI label %s", label)
			}
			pages := chapterPages(t, ta, imp.Result.ChapterID)
			if len(pages) != 3 {
				t.Fatalf("%d pages", len(pages))
			}
			for i, p := range pages {
				if p.Width != 60 || p.Height != 90 || len(p.Panels) != 1 || len(p.Panels[0].Layers) != 1 || p.Thumb == nil {
					t.Fatalf("page %d %+v", i, p)
				}
				l := p.Panels[0].Layers[0]
				if l.Kind != pagedoc.LayerBackground || l.Provenance != pagedoc.ProvenanceHuman || l.Asset == nil {
					t.Fatalf("layer %+v", l)
				}
				if got := firstPixel(t, ta, l.Asset.ID); got != uint8(i+1) {
					t.Fatalf("page %d holds image %d (natural order)", i+1, got)
				}
				var source string
				_ = ta.Deps.DB.Get(ctx, &source, `SELECT source FROM assets WHERE id = ?`, l.Asset.ID)
				if source != "import" {
					t.Fatalf("asset source %s", source)
				}
			}
			// The uploaded source is removed after a successful import.
			if _, err := ta.Deps.Store.Stat(ctx, imp.Result.SourceKey); !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("source kept: %v", err)
			}
			// Another member cannot see or retry it; readers cannot import.
			other, oc := ta.UserClient(t, auth.RoleCreator)
			if len(recent(t, ta, other.ID)) != 0 {
				t.Fatal("imports leak across members")
			}
			if r := oc.Do(http.MethodPost, "/imports/"+imp.ID+"/retry", nil, ""); r.StatusCode != http.StatusNotFound {
				t.Fatalf("retry by another member %d", r.StatusCode)
			}
			_, rc := ta.UserClient(t, auth.RoleReader)
			if r := upload(t, rc, map[string][]byte{"a.cbz": makeZip(t, es)}); r.StatusCode != http.StatusForbidden {
				t.Fatalf("reader import %d", r.StatusCode)
			}
		})
	}
}

func TestImageFolderUploadAndCBR(t *testing.T) {
	ta := testutil.NewTestApp(t)
	admin := ta.Login(t, "admin")
	r := upload(t, admin, map[string][]byte{
		"old-sketches/p2.png":   pagePNG(t, 2, 20, 30),
		"old-sketches/p10.png":  pagePNG(t, 3, 20, 30),
		"old-sketches/p1.png":   pagePNG(t, 1, 20, 30),
		"lanterns-of-oru.cbr":   makeRAR([]entry{{name: "b.png", data: pagePNG(t, 2, 30, 40)}, {name: "a.png", data: pagePNG(t, 1, 30, 40)}}),
		"old-sketches/todo.txt": []byte("x"),
	})
	if r.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload %d %s", r.StatusCode, r.Body)
	}
	list := recent(t, ta, ta.Admin.ID)
	if len(list) != 2 {
		t.Fatalf("imports %+v", list)
	}
	var folder *importer.Import
	for i := range list {
		if list[i].Kind == importer.KindImages {
			folder = &list[i]
		}
	}
	if folder == nil || folder.Filename != "old-sketches/" || folder.Result.Images != 3 {
		t.Fatalf("folder import %+v", folder)
	}
	page := admin.Get("/admin/backup")
	if !strings.Contains(string(page.Body), "Queued · 3 images") {
		t.Fatalf("queue row missing:\n%s", page.Body)
	}
	runJobs(t, ta)
	for _, imp := range recent(t, ta, ta.Admin.ID) {
		if imp.Status != importer.StatusDone {
			t.Fatalf("%s: %+v", imp.Filename, imp)
		}
		pages := chapterPages(t, ta, imp.Result.ChapterID)
		for i, p := range pages {
			if got := firstPixel(t, ta, p.Panels[0].Layers[0].Asset.ID); got != uint8(i+1) {
				t.Fatalf("%s page %d holds image %d", imp.Filename, i+1, got)
			}
		}
	}
	if body := string(admin.Get("/admin/backup").Body); !strings.Contains(body, "Done · 3 pages") || !strings.Contains(body, "Done · 2 pages") {
		t.Fatalf("done rows missing")
	}
}

// fakeWorker registers a worker offering detect.panels and answers its jobs.
func fakeWorker(t *testing.T, ta *testutil.TestApp, answer func(j *jobs.Job) (any, *jobs.JobError)) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w, err := ta.Deps.Jobs.RegisterWorker(ctx, "env", "127.0.0.1", jobs.Registration{Name: "vision-box", JobTypes: []string{importer.JobDetect}, MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			j, err := ta.Deps.Jobs.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorWorker, Types: []string{importer.JobDetect}, WorkerID: w.ID})
			if err != nil || j == nil {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			out, jerr := answer(j)
			if jerr != nil {
				_, _ = ta.Deps.Jobs.Fail(ctx, j.ID, w.ID, *jerr, jobs.Cost{})
				continue
			}
			_, _ = ta.Deps.Jobs.Complete(ctx, j.ID, w.ID, out, jobs.Cost{GPUSeconds: 0.5})
		}
	}()
	return func() { cancel(); <-done }
}

func TestWorkerDetectionSplitsPanelsAndDraftsBubbles(t *testing.T) {
	ta := testutil.NewTestApp(t)
	var seen []string
	stop := fakeWorker(t, ta, func(j *jobs.Job) (any, *jobs.JobError) {
		var in struct {
			Reading string `json:"reading"`
		}
		_ = j.DecodeInput(&in)
		seen = append(seen, in.Reading+":"+j.Files["page"])
		return map[string]any{
			"panels": []map[string]any{
				{"points": [][]float64{{0, 45}, {60, 45}, {60, 90}, {0, 90}}, "order": 2},
				{"points": [][]float64{{0, 0}, {60, 0}, {60, 45}, {0, 45}}, "order": 1},
			},
			"bubbles": []map[string]any{{"points": [][]float64{{10, 10}, {40, 10}, {40, 30}, {10, 30}}, "text": "GARA GARA", "vertical": false}},
		}, nil
	})
	defer stop()
	admin := ta.Login(t, "admin")
	upload(t, admin, map[string][]byte{"glass-orchard.cbz": makeZip(t, []entry{{name: "p1.png", data: pagePNG(t, 7, 60, 90)}})})
	runJobs(t, ta)
	imp := recent(t, ta, ta.Admin.ID)[0]
	if imp.Status != importer.StatusDone || imp.Result.Detected != 1 || imp.Result.Bubbles != 1 {
		t.Fatalf("import %+v", imp)
	}
	if len(seen) != 1 || !strings.HasPrefix(seen[0], "rtl:projects/") {
		t.Fatalf("worker inputs %v", seen)
	}
	p := chapterPages(t, ta, imp.Result.ChapterID)[0]
	if len(p.Panels) != 2 || p.Panels[0].Points[0].Y != 0 || p.Panels[1].Points[0].Y != 45 {
		t.Fatalf("panels in reading order %+v", p.Panels)
	}
	for _, pn := range p.Panels {
		l := pn.Layers[0]
		if l.Asset == nil || l.Asset.Width != 60 || l.Asset.Height != 45 || l.Provenance != pagedoc.ProvenanceHuman {
			t.Fatalf("panel crop %+v", l)
		}
	}
	if len(p.Bubbles) != 1 || p.Bubbles[0].PanelID != p.Panels[0].ID {
		t.Fatalf("bubbles %+v", p.Bubbles)
	}
	text := p.Bubbles[0].Texts["en"]
	if text.Text != "GARA GARA" || text.Status != "draft" || text.Source != "ai" {
		t.Fatalf("OCR text must be a draft for review: %+v", p.Bubbles[0].Texts)
	}
}

func TestWorkerFailureAndLocalOnlyFallBackToWholePage(t *testing.T) {
	ta := testutil.NewTestApp(t)
	defer importer.SetDetectTimeout(300 * time.Millisecond)()
	stop := fakeWorker(t, ta, func(j *jobs.Job) (any, *jobs.JobError) {
		return nil, &jobs.JobError{Code: jobs.ErrCodeOOM, Message: "out of memory"}
	})
	admin := ta.Login(t, "admin")
	upload(t, admin, map[string][]byte{"ninth-bell.cbz": makeZip(t, []entry{{name: "p1.png", data: pagePNG(t, 1, 40, 60)}})})
	runJobs(t, ta)
	stop()
	imp := recent(t, ta, ta.Admin.ID)[0]
	if imp.Status != importer.StatusDone || imp.Result.Detected != 0 {
		t.Fatalf("import %+v", imp)
	}
	if p := chapterPages(t, ta, imp.Result.ChapterID)[0]; len(p.Panels) != 1 || len(p.Bubbles) != 0 {
		t.Fatalf("fallback page %+v", p)
	}
	// Local only keeps page images on the server: an online worker is not asked.
	asked := false
	stop = fakeWorker(t, ta, func(j *jobs.Job) (any, *jobs.JobError) {
		asked = true
		return map[string]any{"panels": []map[string]any{{"points": [][]float64{{0, 0}, {40, 0}, {40, 30}, {0, 30}}, "order": 1}}}, nil
	})
	defer stop()
	if _, err := ta.Deps.Settings.UpdateInstance(context.Background(), "", func(i *settings.Instance) error { i.LocalOnly = true; return nil }); err != nil {
		t.Fatal(err)
	}
	countDetect := func() int {
		var n int
		_ = ta.Deps.DB.Get(context.Background(), &n, `SELECT COUNT(*) FROM jobs WHERE type = ?`, importer.JobDetect)
		return n
	}
	before := countDetect()
	upload(t, admin, map[string][]byte{"iron-koi.cbz": makeZip(t, []entry{{name: "p1.png", data: pagePNG(t, 1, 40, 60)}})})
	runJobs(t, ta)
	imp = recent(t, ta, ta.Admin.ID)[0]
	if p := chapterPages(t, ta, imp.Result.ChapterID)[0]; imp.Status != importer.StatusDone || len(p.Panels) != 1 || asked || countDetect() != before {
		t.Fatalf("local only import %+v asked=%v detect jobs %d→%d", imp, asked, before, countDetect())
	}
}

func TestFailedImportResumes(t *testing.T) {
	ta := testutil.NewTestApp(t)
	admin := ta.Login(t, "admin")
	var es []entry
	for i := 1; i <= 3; i++ {
		es = append(es, entry{name: fmt.Sprintf("p%d.png", i), data: pagePNG(t, i, 30, 40)})
	}
	restore := importer.FailAfter(2)
	upload(t, admin, map[string][]byte{"paper-crows.cbz": makeZip(t, es)})
	runJobs(t, ta)
	restore()
	imp := recent(t, ta, ta.Admin.ID)[0]
	if imp.Status != importer.StatusFailed || imp.Pages != 2 || imp.Result.Code != importer.CodeInternal {
		t.Fatalf("interrupted import %+v", imp)
	}
	body := string(admin.Get("/admin/backup").Body)
	if !strings.Contains(body, "/imports/"+imp.ID+"/retry") {
		t.Fatal("retry action missing")
	}
	if r := admin.Do(http.MethodPost, "/imports/"+imp.ID+"/retry?return=/admin/backup", nil, ""); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("retry %d", r.StatusCode)
	}
	runJobs(t, ta)
	again := recent(t, ta, ta.Admin.ID)[0]
	if again.Status != importer.StatusDone || again.ProjectID != imp.ProjectID || again.Pages != 3 {
		t.Fatalf("resumed import %+v", again)
	}
	pages := chapterPages(t, ta, again.Result.ChapterID)
	if len(pages) != 3 {
		t.Fatalf("%d pages after resuming", len(pages))
	}
	for i, p := range pages {
		if got := firstPixel(t, ta, p.Panels[0].Layers[0].Asset.ID); got != uint8(i+1) {
			t.Fatalf("page %d holds image %d", i+1, got)
		}
	}
	var projects int
	_ = ta.Deps.DB.Get(context.Background(), &projects, `SELECT COUNT(*) FROM projects WHERE owner_id = ?`, ta.Admin.ID)
	if projects != 1 {
		t.Fatalf("%d projects after resuming", projects)
	}
}

func TestPDFNeedsPdftoppm(t *testing.T) {
	ta := testutil.NewTestApp(t)
	admin := ta.Login(t, "admin")
	pdf := []byte("%PDF-1.7\n1 0 obj << >> endobj\ntrailer << >>\n%%EOF\n")
	t.Setenv("PATH", t.TempDir())
	body := string(admin.Get("/admin/backup").Body)
	if !strings.Contains(body, "Drop CBZ, CBR or image folders") || !strings.Contains(body, `class="mv-soon"`) {
		t.Fatal("PDF option must be Soon without pdftoppm")
	}
	upload(t, admin, map[string][]byte{"low-tide-radio.pdf": pdf})
	if len(recent(t, ta, ta.Admin.ID)) != 0 {
		t.Fatal("PDF accepted without pdftoppm")
	}
	// A pdftoppm on PATH renders pages (a stand-in script writing two PNGs).
	bin := t.TempDir()
	fixture := filepath.Join(bin, "page.png")
	if err := os.WriteFile(fixture, pagePNG(t, 9, 50, 70), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nfor a; do last=$a; done\ncp \"$MV_TEST_PNG\" \"$last-01.png\"\ncp \"$MV_TEST_PNG\" \"$last-02.png\"\n"
	if err := os.WriteFile(filepath.Join(bin, "pdftoppm"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/bin:/usr/bin")
	t.Setenv("MV_TEST_PNG", fixture)
	if body := string(admin.Get("/admin/backup").Body); !strings.Contains(body, "Drop CBZ, CBR, PDF or image folders") {
		t.Fatal("PDF option missing with pdftoppm")
	}
	upload(t, admin, map[string][]byte{"low-tide-radio.pdf": pdf})
	runJobs(t, ta)
	imp := recent(t, ta, ta.Admin.ID)[0]
	if imp.Kind != importer.KindPDF || imp.Status != importer.StatusDone || imp.Pages != 2 {
		t.Fatalf("pdf import %+v", imp)
	}
}

// A long Japanese file name is cut at a character boundary: the stored name
// stays valid UTF-8 (PostgreSQL refuses anything else) and the import starts.
func TestLongMultibyteNameIsCutAtACharacter(t *testing.T) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect))
			admin := ta.Login(t, "admin")
			name := strings.Repeat("潮騒の灯台守", 20) + ".cbz" // 364 bytes
			r := upload(t, admin, map[string][]byte{name: makeZip(t, []entry{{name: "p1.png", data: pagePNG(t, 1, 20, 30)}})})
			if r.StatusCode != http.StatusSeeOther {
				t.Fatalf("upload %d %s", r.StatusCode, r.Body)
			}
			list := recent(t, ta, ta.Admin.ID)
			if len(list) != 1 {
				t.Fatalf("imports %+v", list)
			}
			got := list[0].Filename
			if !utf8.ValidString(got) || len(got) > 200 || !strings.HasPrefix(name, got) || len(got) < 190 {
				t.Fatalf("stored name %q (%d bytes)", got, len(got))
			}
		})
	}
}
