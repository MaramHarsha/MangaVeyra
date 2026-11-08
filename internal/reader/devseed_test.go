package reader_test

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/app"
	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/config"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/social"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// TestDevSeed fills a real data directory with published chapters for
// manual checks and the desktop design comparison. It is skipped unless
// MV_READER_DEVSEED=1; it uses the same MV_* environment as the server:
//
//	MV_READER_DEVSEED=1 MV_DATA_DIR=/tmp/r MV_DB=sqlite:///tmp/r/mv.db MV_STORAGE=local \
//	  go test -count=1 -run TestDevSeed ./internal/reader/
//
// It creates an admin (jerry) and a reader (jo.okafor), both with testutil.TestPassword,
// the manga "Tidebreak Courier" (Ch. 7 and Ch. 8 "The Lighthouse Debt", 34
// pages each, RTL), the webtoon "Low Tide Radio" (Ep. 10–12) and the mature
// "Harbour Ghosts". Pages are drawn like the design's sample page: screentone
// panels with ink borders, with the panel geometry frozen for guided view.
func TestDevSeed(t *testing.T) {
	if os.Getenv("MV_READER_DEVSEED") != "1" {
		t.Skip("set MV_READER_DEVSEED=1 to seed a data directory")
	}
	ctx := context.Background()
	cfg, err := config.Load("", os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	params := auth.FastHashParams
	a, err := app.New(ctx, cfg, app.Options{HashParams: &params, LogOutput: io.Discard, ExecutorSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ta := &testutil.TestApp{App: a, Deps: a.Deps}
	f := publishtest.New(t, ta)
	jerry := ta.CreateUser(t, "jerry", auth.RoleAdmin)
	ta.CreateUser(t, "jo.okafor", auth.RoleReader)
	f.SetMode(settings.ModePublic)

	label := map[string]string{"script": "human", "art": "ai_assisted", "lettering": "human"}
	tide := f.Series(publishtest.SeriesSpec{Owner: jerry, Title: "Tidebreak Courier", Slug: "tidebreak-courier",
		Synopsis: "A courier who can't read maps carries the letters nobody else will.", AILabel: label, Cover: true, AgeRating: publish.RatingTeen})
	seedChapter(t, ta, f, tide, 7, "The Night Ferry", 34, label, 1040, 1470)
	seedChapter(t, ta, f, tide, 8, "The Lighthouse Debt", 34, label, 1040, 1470)

	radio := f.Series(publishtest.SeriesSpec{Owner: jerry, Title: "Low Tide Radio", Slug: "low-tide-radio", Format: "webtoon", AILabel: label, Cover: true})
	seedChapter(t, ta, f, radio, 10, "Call-in", 6, label, 1440, 2400)
	seedChapter(t, ta, f, radio, 11, "Dead air", 6, label, 1440, 2400)
	seedChapter(t, ta, f, radio, 12, "Static", 6, label, 1440, 2400)

	ghosts := f.Series(publishtest.SeriesSpec{Owner: jerry, Title: "Harbour Ghosts", Slug: "harbour-ghosts", AgeRating: publish.RatingMature, AILabel: label})
	seedChapter(t, ta, f, ghosts, 1, "Low Water", 4, label, 1040, 1470)

	// More shelf titles for the library (E7), two short chapters each.
	for i, title := range []string{"Lanterns of Oru", "Glass Orchard", "Paper Crows", "Ninth Bell", "Iron Koi"} {
		spec := publishtest.SeriesSpec{Owner: jerry, Title: title, AILabel: label, PublishedAt: publishtest.Ago(time.Duration(30+i) * 24 * time.Hour)}
		if title == "Glass Orchard" {
			spec.Status = publish.SeriesCompleted
		}
		s := f.Series(spec)
		seedChapter(t, ta, f, s, 1, "Opening", 2, label, 520, 735)
		seedChapter(t, ta, f, s, 2, "Second tide", 2, label, 520, 735)
	}
	for _, s := range []publishtest.Series{tide, radio, ghosts} {
		seedCover(t, ta, s)
	}
	t.Logf("seeded %s, %s and %s", tide.Slug, radio.Slug, ghosts.Slug)
}

// seedCover gives a series a drawn 3:4 cover.
func seedCover(t *testing.T, ta *testutil.TestApp, s publishtest.Series) {
	t.Helper()
	ctx := context.Background()
	full := []pagedoc.Point{{X: 24, Y: 24}, {X: 576, Y: 24}, {X: 576, Y: 560}, {X: 24, Y: 620}}
	data := drawPage(600, 800, []publish.GuidedPanel{{Order: 1, Points: full, X: 24, Y: 24, W: 552, H: 596}}, 2)
	a, err := assets.From(ta.Deps).Save(ctx, assets.SaveParams{OwnerID: s.Owner.ID, ProjectID: s.ProjectID, Kind: "series_cover",
		Source: assets.SourceSystem, ContentType: "image/png", Data: data})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ta.Deps.DB.Exec(ctx, `UPDATE series SET cover_asset_id = ? WHERE id = ?`, a.ID, s.ID); err != nil {
		t.Fatal(err)
	}
}

// TestDevServeWithFollows serves the seeded data directory on
// 127.0.0.1:6352 with a fake social.Follows in which every member follows
// every series, so the E7 Following grid can be compared with the artboard
// before the social module lands. Skipped unless MV_READER_DEVSERVE=1;
// it stops after R_SERVE_SECONDS (default 300).
func TestDevServeWithFollows(t *testing.T) {
	if os.Getenv("MV_READER_DEVSERVE") != "1" {
		t.Skip("set MV_READER_DEVSERVE=1 to serve the seeded data directory")
	}
	ctx := context.Background()
	cfg, err := config.Load("", os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(ctx, cfg, app.Options{LogOutput: io.Discard, ExecutorSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	var ids []string
	if err := a.Deps.DB.Select(ctx, &ids, `SELECT id FROM series ORDER BY published_at DESC, id DESC`); err != nil {
		t.Fatal(err)
	}
	follows := &allFollows{ids: ids}
	core.Provide[social.Follows](a.Deps, follows)
	secs, _ := strconv.Atoi(os.Getenv("R_SERVE_SECONDS"))
	if secs <= 0 {
		secs = 300
	}
	srv := &http.Server{Addr: "127.0.0.1:6352", Handler: a.Handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		time.Sleep(time.Duration(secs) * time.Second)
		_ = srv.Close()
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}

type allFollows struct{ ids []string }

func (f *allFollows) IsFollowing(context.Context, string, string) (bool, error) { return true, nil }
func (f *allFollows) FollowedSeriesIDs(context.Context, string, string, int) ([]string, string, error) {
	return f.ids, "", nil
}
func (f *allFollows) Followers(context.Context, string, string, int) ([]string, string, error) {
	return nil, "", nil
}

// samplePanels are the five panels of the design's sample page (E3), in a 520 × 735 box.
var samplePanels = [][]pagedoc.Point{
	{{X: 31, Y: 31}, {X: 489, Y: 31}, {X: 489, Y: 226.2}, {X: 31, Y: 253.1}},
	{{X: 269.2, Y: 269.9}, {X: 489, Y: 243}, {X: 489, Y: 448.3}, {X: 269.2, Y: 448.3}},
	{{X: 31, Y: 273.3}, {X: 255.4, Y: 270.6}, {X: 255.4, Y: 448.3}, {X: 31, Y: 448.3}},
	{{X: 31, Y: 468.4}, {X: 305.8, Y: 468.4}, {X: 269.2, Y: 704}, {X: 31, Y: 704}},
	{{X: 319.5, Y: 468.4}, {X: 489, Y: 468.4}, {X: 489, Y: 704}, {X: 282.9, Y: 704}},
}

var seedAlt = []string{
	"Rin vaults a rooftop railing at dusk, coat flaring, harbour lights far below.",
	"Kaito Mbeki watches from the lighthouse gallery, one hand on the rusted rail.",
	"A parcel slips from Rin's satchel and tumbles toward the water.",
	"Old Wren leans out of the ferry window, shouting over the wind.",
	"Pip the Gull snatches the parcel string mid-air.",
}

var seedLines = [][]publish.GuidedBubble{
	{{Order: 1, Kind: "speech", Speaker: "Rin Asagiri", Text: "Then I'll take the roof!"}},
	{{Order: 1, Kind: "speech", Speaker: "Kaito Mbeki", Text: "The tide won't wait for you."}},
	{{Order: 1, Kind: "sfx", Text: "FWIP"}},
	{{Order: 1, Kind: "speech", Speaker: "Old Wren", Text: "Mind the debt, girl!"}},
	{{Order: 1, Kind: "sfx", Text: "SKREE"}},
}

func seedChapter(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture, s publishtest.Series, number float64, title string, pages int,
	label map[string]string, w, h int) {
	t.Helper()
	ctx := context.Background()
	pub := f.Publish(s, publishtest.PublicationSpec{Number: number, Title: title, Pages: pages, AILabel: label,
		PublishedAt: publishtest.Ago(time.Duration(13-number) * 24 * time.Hour)})
	sx, sy := float64(w)/520, float64(h)/735
	for i, pg := range pub.Pages {
		panels := make([]publish.GuidedPanel, len(samplePanels))
		for j, poly := range samplePanels {
			pts := make([]pagedoc.Point, len(poly))
			minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
			for k, p := range poly {
				pts[k] = pagedoc.Point{X: p.X * sx, Y: p.Y * sy}
				minX, minY = math.Min(minX, pts[k].X), math.Min(minY, pts[k].Y)
				maxX, maxY = math.Max(maxX, pts[k].X), math.Max(maxY, pts[k].Y)
			}
			panels[j] = publish.GuidedPanel{Order: j + 1, Points: pts, X: minX, Y: minY, W: maxX - minX, H: maxY - minY,
				AltText: seedAlt[j], Bubbles: seedLines[j]}
		}
		data := drawPage(w, h, panels, i)
		asset, err := assets.From(ta.Deps).Save(ctx, assets.SaveParams{OwnerID: s.Owner.ID, ProjectID: s.ProjectID, Kind: "publication_page",
			Source: assets.SourceSystem, ContentType: "image/png", Data: data})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE publication_pages SET asset_id = ?, width = ?, height = ?, panels = ?, alt_text = ? WHERE id = ?`,
			asset.ID, w, h, db.MustJSON(panels), fmt.Sprintf("Page %d of %s: Rin races the tide to the lighthouse.", pg.Number, title), pg.ID); err != nil {
			t.Fatal(err)
		}
	}
}

// drawPage renders a page like the design's sample page: paper, screentone
// panels and 2.5 px (at 520 px) ink borders.
func drawPage(w, h int, panels []publish.GuidedPanel, shade int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	paper := color.RGBA{0xE8, 0xEE, 0xFF, 0xFF}
	tone := color.RGBA{0xDC, 0xE3, 0xF3, 0xFF}
	ink := color.RGBA{0x07, 0x0A, 0x12, 0xFF}
	draw.Draw(img, img.Bounds(), &image.Uniform{paper}, image.Point{}, draw.Src)
	scale := float64(w) / 520
	border := 2.5 * scale
	step := 6 * scale
	for pi, pn := range panels {
		for y := int(pn.Y); y < int(pn.Y+pn.H) && y < h; y++ {
			for x := int(pn.X); x < int(pn.X+pn.W) && x < w; x++ {
				fx, fy := float64(x)+.5, float64(y)+.5
				if !inside(pn.Points, fx, fy) {
					continue
				}
				c := tone
				// Screentone dots: denser in alternating panels and pages for variety.
				r := 1.05 * scale
				if (pi+shade)%3 == 0 {
					r = 1.3 * scale
				}
				dx := math.Mod(fx, step) - step/2
				dy := math.Mod(fy, step) - step/2
				if dx*dx+dy*dy <= r*r {
					c = color.RGBA{0x7A, 0x80, 0x8F, 0xFF}
				}
				if edgeDistance(pn.Points, fx, fy) <= border {
					c = ink
				}
				img.SetRGBA(x, y, c)
			}
		}
	}
	var buf bytes.Buffer
	_ = (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, img)
	return buf.Bytes()
}

func inside(pts []pagedoc.Point, x, y float64) bool {
	in := false
	for i, j := 0, len(pts)-1; i < len(pts); j, i = i, i+1 {
		a, b := pts[i], pts[j]
		if (a.Y > y) != (b.Y > y) && x < (b.X-a.X)*(y-a.Y)/(b.Y-a.Y)+a.X {
			in = !in
		}
	}
	return in
}

func edgeDistance(pts []pagedoc.Point, x, y float64) float64 {
	best := math.Inf(1)
	for i := range pts {
		a, b := pts[i], pts[(i+1)%len(pts)]
		dx, dy := b.X-a.X, b.Y-a.Y
		l := dx*dx + dy*dy
		u := 0.0
		if l > 0 {
			u = math.Max(0, math.Min(1, ((x-a.X)*dx+(y-a.Y)*dy)/l))
		}
		px, py := a.X+u*dx-x, a.Y+u*dy-y
		best = math.Min(best, math.Hypot(px, py))
	}
	return best
}
