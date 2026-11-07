package export

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/compose"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/lettering"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

// Isolated page source keeps these tests runnable before the separately owned
// page store is integrated. Real Studio authorization, DB revisions, storage,
// queue leasing and staged persistence are used in every data-layer test.
type pageSource struct {
	pagestore.Service
	docs []pagedoc.Page
}

func (p *pageSource) ChapterPages(context.Context, string) ([]pagedoc.Page, error) {
	return append([]pagedoc.Page(nil), p.docs...), nil
}
func (p *pageSource) ListPages(context.Context, string) ([]pagestore.PageSummary, error) {
	var out []pagestore.PageSummary
	for _, d := range p.docs {
		out = append(out, pagestore.PageSummary{ID: d.ID, Number: d.Number, Version: d.Version})
	}
	return out, nil
}
func (p *pageSource) GetPage(_ context.Context, id string) (pagedoc.Page, error) {
	for _, d := range p.docs {
		if d.ID == id {
			return d, nil
		}
	}
	return pagedoc.Page{}, pagestore.ErrNotFound
}

type fixture struct {
	s       *service
	actor   studio.Actor
	project studio.Project
	chapter studio.Chapter
	source  *pageSource
	store   storage.Store
}

func newFixture(t *testing.T, d *db.DB) fixture {
	t.Helper()
	ctx := context.Background()
	st := settings.New(d)
	hp := auth.FastHashParams
	au := auth.New(d, st, nil, auth.Options{HashParams: &hp})
	name := "courier" + db.NewID()[14:]
	user, err := au.CreateUser(ctx, auth.NewUser{Username: name, Email: name + "@example.org", Password: "courier-test-password", Role: auth.RoleCreator})
	if err != nil {
		t.Fatal(err)
	}
	local, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	queue := jobs.New(d, st, nil, jobs.Options{})
	deps := &core.Deps{DB: d, Auth: au, Settings: st, Jobs: queue, Store: local, Media: storage.NewSigner([]byte("export test media key"))}
	assets.NewModule(deps)
	sm := studio.New(deps)
	if err = sm.Init(ctx); err != nil {
		t.Fatal(err)
	}
	ss := core.MustUse[studio.Service](deps)
	actor := studio.Actor{UserID: user.ID}
	project, err := ss.CreateProject(ctx, actor, studio.NewProject{Title: "Tidebreak Courier", PageWidth: 200, PageHeight: 300})
	if err != nil {
		t.Fatal(err)
	}
	chapter, err := ss.CreateChapter(ctx, actor, project.ID, "The lighthouse debt")
	if err != nil {
		t.Fatal(err)
	}
	source := &pageSource{}
	for n := 1; n <= 2; n++ {
		p := pagedoc.New(db.NewID(), project.ID, chapter.ID, n, 200, 300, "rtl")
		p.Panels = []pagedoc.Panel{{ID: db.NewID(), Order: 0, Points: pagedoc.RectPoints(pagedoc.Rect{X: 10, Y: 10, W: 180, H: 280}), Layers: []pagedoc.Layer{}}}
		p.Bubbles = []pagedoc.Bubble{{ID: db.NewID(), PanelID: p.Panels[0].ID, Kind: "speech", Shape: "ellipse", Box: pagedoc.Rect{X: 30, Y: 30, W: 140, H: 100}, Style: pagedoc.BubbleStyle{AutoFit: true, Size: 24}, Texts: map[string]pagedoc.BubbleText{"en": {Text: "Follow the light!", Source: "human", Status: "approved"}}}}
		if _, err = d.Exec(ctx, `INSERT INTO pages (id,project_id,chapter_id,number,width,height) VALUES (?,?,?,?,?,?)`, p.ID, project.ID, chapter.ID, n, p.Width, p.Height); err != nil {
			t.Fatal(err)
		}
		source.docs = append(source.docs, p)
	}
	e, err := lettering.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	a := assets.From(deps)
	s := &service{db: d, queue: queue, studio: ss, pages: source, renderer: compose.NewRenderer(a, e), assets: a, lettering: e}
	return fixture{s: s, actor: actor, project: project, chapter: chapter, source: source, store: local}
}
func (f fixture) req(format string) Request {
	return Request{ProjectID: f.project.ID, ChapterID: f.chapter.ID, Format: format, TrimID: "b5", DPI: 72, Lang: "en"}
}
func TestPNGAndCBZArchivesOrderMetadataAndCredits(t *testing.T) {
	testdb.ForEach(t, func(t *testing.T, d *db.DB) {
		f := newFixture(t, d)
		for _, format := range []string{FormatPNG, FormatCBZ} {
			req := f.req(format)
			req.PageIDs = []string{f.source.docs[1].ID, f.source.docs[0].ID}
			req.CreditsPage = true
			p, err := f.s.prepare(context.Background(), f.actor, req)
			if err != nil {
				t.Fatal(err)
			}
			if p.pages[0].ID != f.source.docs[0].ID {
				t.Fatal("selection reordered chapter")
			}
			data, result, err := f.s.build(context.Background(), p, nil)
			if err != nil {
				t.Fatal(err)
			}
			if result.Pages != 3 || result.Bytes != int64(len(data)) {
				t.Fatal(result)
			}
			z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			want := 3
			if format == FormatCBZ {
				want = 4
			}
			if len(z.File) != want {
				t.Fatalf("archive entries %d", len(z.File))
			}
			for i := 0; i < 3; i++ {
				r, err := z.File[i].Open()
				if err != nil {
					t.Fatal(err)
				}
				cfg, _, err := image.DecodeConfig(r)
				r.Close()
				if err != nil || cfg.Width == 0 {
					t.Fatal("invalid page image", err)
				}
			}
			if format == FormatCBZ {
				r, _ := z.File[3].Open()
				xml, _ := io.ReadAll(r)
				r.Close()
				if !bytes.Contains(xml, []byte("<Manga>YesAndRightToLeft</Manga>")) || !bytes.Contains(xml, []byte("<PageCount>3</PageCount>")) {
					t.Fatalf("ComicInfo: %s", xml)
				}
			}
		}
		// Never invent an unrecorded role as human in provenance credits.
		p, err := f.s.prepare(context.Background(), f.actor, f.req(FormatPNG))
		if err != nil {
			t.Fatal(err)
		}
		credits := f.s.credits(p)
		if !strings.Contains(credits.Bubbles[0].Texts["en"].Text, "Not recorded") {
			t.Fatal("unrecorded provenance was invented")
		}
	})
}
func TestPDFBoxesIndependentPopplerAndRaster(t *testing.T) {
	e, err := lettering.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	s := &service{renderer: compose.NewRenderer(nil, e)}
	for _, trim := range Trims {
		t.Run(trim.ID, func(t *testing.T) {
			p := pagedoc.New("page", "project", "chapter", 1, 200, 300, "rtl")
			p.Bubbles = []pagedoc.Bubble{{ID: "b", Kind: "speech", Shape: "ellipse", Box: pagedoc.Rect{X: 30, Y: 30, W: 140, H: 100}, Style: pagedoc.BubbleStyle{Size: 24, AutoFit: true}, Texts: map[string]pagedoc.BubbleText{"en": {Text: "Rin finds the light."}}}}
			prep := prepared{project: studio.Project{Title: "Tidebreak Courier"}, chapter: studio.Chapter{Title: "Lighthouse", Number: 1}, pages: []pagedoc.Page{p, p}, input: exportInput{Request: Request{Format: FormatPDF, TrimID: trim.ID, DPI: 72, Lang: "en"}}}
			data, result, err := s.build(context.Background(), prep, nil)
			if err != nil {
				t.Fatal(err)
			}
			if dir := os.Getenv("MV_RENDER_QA_DIR"); dir != "" {
				if err := os.MkdirAll(dir, 0750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, trim.ID+".pdf"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if result.Pages != 2 || !bytes.Contains(data, []byte("/TrimBox")) || !bytes.Contains(data, []byte("/Prev")) {
				t.Fatal("missing effective PDF boxes")
			}
			path := filepath.Join(t.TempDir(), "chapter.pdf")
			if err = os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if bin, err := exec.LookPath("pdfinfo"); err == nil {
				out, err := exec.Command(bin, "-box", "-f", "1", "-l", "2", path).CombinedOutput()
				if err != nil {
					t.Fatalf("pdfinfo: %v\n%s", err, out)
				}
				text := string(out)
				if strings.Contains(text, "Syntax Error") || !strings.Contains(text, "TrimBox") || !strings.Contains(text, "BleedBox") {
					t.Fatalf("invalid boxes: %s", out)
				}
				t.Logf("independent Poppler box inspection:\n%s", out)
			} else {
				t.Log("pdfinfo unavailable: independent inspection skipped")
			}
			if bin, err := exec.LookPath("pdftoppm"); err == nil {
				prefix := filepath.Join(t.TempDir(), "page")
				out, err := exec.Command(bin, "-f", "1", "-singlefile", "-r", "36", "-png", path, prefix).CombinedOutput()
				if err != nil {
					t.Fatalf("PDF rendering: %v %s", err, out)
				}
				f, err := os.Open(prefix + ".png")
				if err != nil {
					t.Fatal(err)
				}
				im, _, err := image.Decode(f)
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
				dark := 0
				for y := 0; y < im.Bounds().Dy(); y++ {
					for x := 0; x < im.Bounds().Dx(); x++ {
						r, g, b, _ := im.At(x, y).RGBA()
						if r < 20000 && g < 20000 && b < 20000 {
							dark++
						}
					}
				}
				if dir := os.Getenv("MV_RENDER_QA_DIR"); dir != "" {
					data, err := os.ReadFile(prefix + ".png")
					if err != nil {
						t.Fatal(err)
					}
					if err = os.WriteFile(filepath.Join(dir, trim.ID+".png"), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if dark < 20 {
					t.Fatal("rendered PDF lacks lettering/outline")
				}
			}
		})
	}
}
func TestPDFBoxingFailsClosedOnUntrustedOrChangedWriter(t *testing.T) {
	for _, b := range [][]byte{nil, []byte("%PDF fake /Type/Page /TrimBox[0 0 1 1]"), []byte("%PDF\nstartxref\n4\n%%EOF")} {
		if _, err := boxCanvasPDF(b, Trims[0], 1); err == nil {
			t.Fatal("unexpected PDF accepted")
		}
	}
}

type hookRenderer struct {
	compose.Renderer
	hook   func()
	called bool
}

func (h *hookRenderer) PNG(ctx context.Context, p pagedoc.Page, o compose.Options) ([]byte, error) {
	if !h.called {
		h.called = true
		h.hook()
	}
	return h.Renderer.PNG(ctx, p, o)
}
func TestExportJobFencedPersistenceAndReauthorization(t *testing.T) {
	testdb.ForEach(t, func(t *testing.T, d *db.DB) {
		for _, scenario := range []string{"success", "stale_before", "stale_during", "canceled", "revoked"} {
			t.Run(scenario, func(t *testing.T) {
				f := newFixture(t, d)
				ctx := context.Background()
				j, err := f.s.Enqueue(ctx, f.actor, f.req(FormatPNG))
				if err != nil {
					t.Fatal(err)
				}
				var before int
				if err = d.Get(ctx, &before, `SELECT COUNT(*) FROM assets WHERE kind = 'export'`); err != nil {
					t.Fatal(err)
				}
				hook := func() {
					switch scenario {
					case "stale_during":
						_, err = d.Exec(ctx, `UPDATE pages SET version = version + 1 WHERE id = ?`, f.source.docs[0].ID)
					case "canceled":
						_, err = f.s.queue.Cancel(ctx, j.ID)
					case "revoked":
						_, err = d.Exec(ctx, `UPDATE users SET status = 'suspended' WHERE id = ?`, f.actor.UserID)
					}
					if err != nil {
						t.Error(err)
					}
				}
				f.s.renderer = &hookRenderer{Renderer: f.s.renderer, hook: hook}
				if scenario == "stale_before" {
					if _, err = d.Exec(ctx, `UPDATE pages SET version=version+1 WHERE id=?`, f.source.docs[0].ID); err != nil {
						t.Fatal(err)
					}
				}
				pool := jobs.NewPool(f.s.queue, 1, nil)
				pool.Register(JobType, f.s.handle)
				ran, err := pool.RunOnce(ctx)
				if err != nil || !ran {
					t.Fatal("job not run", err)
				}
				done, err := f.s.queue.Get(ctx, j.ID)
				if err != nil {
					t.Fatal(err)
				}
				var count int
				if err = d.Get(ctx, &count, `SELECT COUNT(*) FROM assets WHERE kind = 'export'`); err != nil {
					t.Fatal(err)
				}
				if scenario == "success" {
					if done.State != jobs.StateDone || count != before+1 {
						t.Fatalf("job: %+v assets %d", done, count)
					}
					var result Result
					if err = json.Unmarshal(done.Output, &result); err != nil || result.AssetID == "" || result.URL == "" {
						t.Fatal(result, err)
					}
					if _, _, err = f.s.assets.Read(ctx, result.AssetID, 0); err != nil {
						t.Fatal(err)
					}
				} else {
					if done.State == jobs.StateDone || count != before {
						t.Fatalf("fence failed: %s %+v count%d", scenario, done, count)
					}
					objects, err := f.store.List(ctx, "projects/"+f.project.ID+"/export/")
					if err != nil || len(objects) != 0 {
						t.Fatalf("staged object leaked: %v %v", objects, err)
					}
				}
			})
		}
	})
}
func TestRequestAndRangeValidation(t *testing.T) {
	p, c := db.NewID(), db.NewID()
	for _, req := range []Request{{ProjectID: p, ChapterID: c, Format: "epub"}, {ProjectID: p, ChapterID: c, Format: FormatPDF, DPI: 601}, {ProjectID: p, ChapterID: c, Format: FormatPDF, TrimID: "unknown"}, {ProjectID: p, ChapterID: c, Format: FormatPDF, PageIDs: []string{c, c}}} {
		if _, err := normalize(req); err == nil {
			t.Fatal("invalid request accepted", req)
		}
	}
	ids := []pagestore.PageSummary{{ID: p, Number: 1}, {ID: c, Number: 2}}
	got, err := parseRange("2, 1", ids)
	if err != nil || got[0] != p {
		t.Fatal(got, err)
	}
	for _, r := range []string{"", "0", "3", "2-1", "1-257", "1-2-3"} {
		if _, err := parseRange(r, ids); err == nil {
			t.Fatal("invalid range", r)
		}
	}
}
