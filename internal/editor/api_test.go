package editor_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/lettering"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

type response struct {
	Page      pagedoc.Page             `json:"page"`
	Lettering []lettering.BubbleLayout `json:"lettering"`
}

func each(t *testing.T, fn func(*testing.T, *testutil.TestApp)) {
	t.Helper()
	for _, d := range testdb.Dialects() {
		t.Run(d, func(t *testing.T) { fn(t, testutil.NewTestApp(t, testutil.WithDialect(d))) })
	}
}
func fixture(t *testing.T, ta *testutil.TestApp) (*auth.User, *testutil.Client, studio.Project, studio.Chapter, string) {
	t.Helper()
	u, c := ta.UserClient(t, auth.RoleCreator)
	svc := core.MustUse[studio.Service](ta.Deps)
	a, _ := svc.ActorFromUser(context.Background(), u.ID)
	p, err := svc.CreateProject(context.Background(), a, studio.NewProject{Title: "Tidebreak Courier", Premise: "A courier crosses the flooded harbour.", Style: "seinen_bw", Format: "manga_rtl", Language: "ja", PageSize: "b5", PageWidth: 600, PageHeight: 800})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := svc.CreateChapter(context.Background(), a, p.ID, "The Lighthouse Debt")
	if err != nil {
		t.Fatal(err)
	}
	return u, c, p, ch, "/api/v1/editor/p/" + p.ID + "/chapters/" + ch.ID
}
func doc(t *testing.T, r *testutil.Response, status int) response {
	t.Helper()
	if r.StatusCode != status {
		t.Fatalf("HTTP %d want%d: %.1800s", r.StatusCode, status, r.Body)
	}
	var out response
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func save(c *testutil.Client, path string, p pagedoc.Page, version int) *testutil.Response {
	return c.SendJSON(http.MethodPut, path, map[string]any{"page": p, "base_version": version})
}
func art(t *testing.T, w, h int) []byte {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			im.SetNRGBA(x, y, color.NRGBA{R: 40, G: 120, B: 220, A: 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestEditorPageSaveLetteringRevisionLockHistoryAndIdentity(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		_, c, p, ch, base := fixture(t, ta)
		created := doc(t, c.PostJSON(base+"/pages", map[string]string{"layout_id": "reveal-5"}), 201)
		page := created.Page
		if len(page.Panels) != 5 || page.Direction != "rtl" {
			t.Fatalf("page=%#v", page)
		}
		path := base + "/pages/" + page.ID
		for _, chapter := range []string{ch.ID, "1"} {
			r := c.Get("/studio/p/" + p.ID + "/chapters/" + chapter + "/edit")
			if r.StatusCode != 200 || !strings.Contains(string(r.Body), `data-island="editor"`) {
				t.Fatalf("editor route=%d %.700s", r.StatusCode, r.Body)
			}
		}
		bubble := pagedoc.Bubble{ID: db.NewID(), PanelID: page.Panels[0].ID, Kind: "speech", Order: 0, Box: pagedoc.Rect{X: 340, Y: 60, W: 160, H: 100}, Shape: "ellipse", Tail: pagedoc.Tail{Enabled: true, Tip: pagedoc.Point{X: 410, Y: 185}}, Style: pagedoc.BubbleStyle{AutoFit: true, Rotate: 17}, Vertical: true, Texts: map[string]pagedoc.BubbleText{"ja": {Text: "海へ行こう", Source: "human", Status: "draft", Ruby: []pagedoc.Ruby{{Start: 0, End: 1, Text: "うみ"}}}}}
		page.Bubbles = []pagedoc.Bubble{bubble}
		page.Panels[0].Prompt = "Rin reaches the lighthouse."
		saved := doc(t, save(c, path, page, page.Version), 200)
		if saved.Page.Version != page.Version+1 || len(saved.Lettering) != 1 || saved.Lettering[0].TextPath == "" {
			t.Fatalf("saved=%#v", saved)
		}
		expected, err := core.MustUse[lettering.Engine](ta.Deps).LayoutPage(saved.Page, "ja")
		if err != nil || saved.Lettering[0].ShapePath != expected[0].ShapePath || saved.Lettering[0].TextPath != expected[0].TextPath || saved.Lettering[0].Rotate != 17 {
			t.Fatalf("shared lettering mismatch: %v", err)
		}
		rotated := saved.Page.Bubbles[0]
		rotated.Style.Rotate = 0
		plain, err := core.MustUse[lettering.Engine](ta.Deps).Layout(rotated, "ja")
		if err != nil || plain.ShapePath != saved.Lettering[0].ShapePath || plain.TextPath != saved.Lettering[0].TextPath {
			t.Fatalf("paths were rotated before SVG grouping: %v", err)
		}
		if r := save(c, path, page, page.Version); r.StatusCode != 409 {
			t.Fatalf("stale=%d", r.StatusCode)
		}
		locked := doc(t, c.PostJSON(path+"/lock", map[string]any{"locked": true, "base_version": saved.Page.Version}), 200)
		locked.Page.Panels[0].Prompt = "forbidden locked edit"
		if r := save(c, path, locked.Page, locked.Page.Version); r.StatusCode != 423 {
			t.Fatalf("locked save=%d %.700s", r.StatusCode, r.Body)
		}
		if r := c.PostJSON(path+"/lock", map[string]any{"locked": false, "base_version": locked.Page.Version}); r.StatusCode != 422 {
			t.Fatalf("unlock without confirmation=%d", r.StatusCode)
		}
		unlocked := doc(t, c.PostJSON(path+"/lock", map[string]any{"locked": false, "confirm": true, "base_version": locked.Page.Version}), 200)
		var versions struct {
			Versions []pagestore.Version `json:"versions"`
		}
		if err := json.Unmarshal(c.Get(path+"/history").Body, &versions); err != nil || len(versions.Versions) < 4 {
			t.Fatalf("versions=%#v %v", versions, err)
		}
		old := versions.Versions[len(versions.Versions)-1]
		restored := doc(t, c.PostJSON(path+"/revert", map[string]any{"version_id": old.ID, "base_version": unlocked.Page.Version, "confirm": true}), 200)
		if len(restored.Page.Bubbles) != 0 || restored.Page.Version <= unlocked.Page.Version {
			t.Fatal("revert failed")
		}
		latest, err := core.MustUse[pagestore.Service](ta.Deps).GetPage(ctx, page.ID)
		if err != nil || latest.Version != restored.Page.Version {
			t.Fatalf("reloaded=%#v %v", latest, err)
		}
		if r := c.SendJSON(http.MethodDelete, path, map[string]any{"base_version": latest.Version, "confirm": false}); r.StatusCode != 422 {
			t.Fatalf("delete without confirmation=%d", r.StatusCode)
		}
	})
}

func TestEditorProjectPermissionsProvenanceAndJobPrivacy(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		u, c, p, ch, base := fixture(t, ta)
		page := doc(t, c.PostJSON(base+"/pages", map[string]string{"layout_id": "splash"}), 201).Page
		path := base + "/pages/" + page.ID
		other, outsider := ta.UserClient(t, auth.RoleCreator)
		if r := outsider.Get(path); r.StatusCode != 404 {
			t.Fatalf("IDOR=%d", r.StatusCode)
		}
		if r := outsider.PostJSON(base+"/pages", map[string]string{}); r.StatusCode != 404 {
			t.Fatalf("foreign create=%d", r.StatusCode)
		}
		raw, _, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: u.ID, Kind: auth.TokenAPI, Name: "Editor bearer"})
		if err != nil {
			t.Fatal(err)
		}
		if r := ta.BearerClient(raw).Get(path); r.StatusCode != 403 {
			t.Fatalf("bearer=%d", r.StatusCode)
		}
		_, err = ta.Deps.DB.Exec(ctx, `INSERT INTO project_members(project_id,user_id,role) VALUES(?,?,?)`, p.ID, other.ID, studio.RoleViewer)
		if err != nil {
			t.Fatal(err)
		}
		changed := page
		changed.Panels[0].Prompt = "viewer cannot edit"
		if r := save(outsider, path, changed, page.Version); r.StatusCode != 404 {
			t.Fatalf("viewer write=%d", r.StatusCode)
		}
		// A generic document save cannot attach arbitrary art or fake its provenance.
		a, err := assets.From(ta.Deps).Save(ctx, assets.SaveParams{OwnerID: u.ID, ProjectID: p.ID, ContentType: "image/png", Data: art(t, 8, 6), Source: assets.SourceUpload})
		if err != nil {
			t.Fatal(err)
		}
		changed = page
		changed.Panels[0].Layers[0].Asset = &pagedoc.AssetRef{ID: a.ID}
		if r := save(c, path, changed, page.Version); r.StatusCode != 422 {
			t.Fatalf("direct art attachment=%d", r.StatusCode)
		}
		store := core.MustUse[pagestore.Service](ta.Deps)
		err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			_, err := store.SetLayerAssetTx(ctx, tx, pagestore.SetLayerAsset{PageID: page.ID, PanelID: page.Panels[0].ID, LayerID: page.Panels[0].Layers[0].ID, Kind: "background", AssetID: a.ID, Provenance: "human"}, pagestore.Change{ActorKind: "user", ActorID: u.ID, Action: "upload"})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		page = doc(t, c.Get(path), 200).Page
		page.Panels[0].Layers[0].Provenance = "ai"
		page.Panels[0].Layers[0].ModelID = "forged"
		saved := doc(t, save(c, path, page, page.Version), 200)
		if saved.Page.Panels[0].Layers[0].Provenance != "human" || saved.Page.Panels[0].Layers[0].ModelID != "" {
			t.Fatal("client forged generation metadata")
		}
		job, err := ta.Deps.Jobs.Enqueue(ctx, jobs.EnqueueParams{Type: gen.JobPanel, UserID: u.ID, Scope: jobs.Scope{Kind: jobs.ScopePanel, ProjectID: p.ID, PageID: page.ID, PanelID: page.Panels[0].ID}, Input: map[string]any{"version": 1, "request": gen.PanelRequest{ProjectID: p.ID, PageID: page.ID, PanelID: page.Panels[0].ID}, "private_token": "must-not-leak"}})
		if err != nil {
			t.Fatal(err)
		}
		r := c.Get(path + "/jobs/" + job.ID)
		if r.StatusCode != 200 || strings.Contains(string(r.Body), "must-not-leak") || strings.Contains(string(r.Body), "\"input\"") {
			t.Fatalf("job privacy=%d %s", r.StatusCode, r.Body)
		}
		if r := outsider.Get(path + "/jobs/" + job.ID); r.StatusCode != 404 {
			t.Fatalf("other user job=%d", r.StatusCode)
		}
		if r := c.Get("/api/v1/editor/p/" + p.ID + "/chapters/" + db.NewID() + "/pages/" + page.ID); r.StatusCode != 404 {
			t.Fatalf("forged chapter=%d", r.StatusCode)
		}
		_ = ch
	})
}

func TestEditorMasksAreDecodedSameLayerPixelsAndVersionFenced(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		u, c, p, _, base := fixture(t, ta)
		page := doc(t, c.PostJSON(base+"/pages", map[string]string{"layout_id": "splash"}), 201).Page
		store := core.MustUse[pagestore.Service](ta.Deps)
		a, err := assets.From(ta.Deps).Save(ctx, assets.SaveParams{OwnerID: u.ID, ProjectID: p.ID, Data: art(t, 8, 6), Source: assets.SourceUpload})
		if err != nil {
			t.Fatal(err)
		}
		err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			_, err := store.SetLayerAssetTx(ctx, tx, pagestore.SetLayerAsset{PageID: page.ID, PanelID: page.Panels[0].ID, LayerID: page.Panels[0].Layers[0].ID, Kind: "background", AssetID: a.ID, Provenance: "human"}, pagestore.Change{ActorKind: "user", ActorID: u.ID, Action: "upload"})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		page, err = store.GetPage(ctx, page.ID)
		if err != nil {
			t.Fatal(err)
		}
		post := func(data []byte, version int) *testutil.Response {
			var b bytes.Buffer
			mw := multipart.NewWriter(&b)
			_ = mw.WriteField("layer_id", page.Panels[0].Layers[0].ID)
			_ = mw.WriteField("base_version", fmtInt(version))
			f, err := mw.CreateFormFile("file", "mask.png")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = f.Write(data)
			_ = mw.Close()
			return c.Do(http.MethodPost, base+"/pages/"+page.ID+"/masks", &b, mw.FormDataContentType())
		}
		if r := post(art(t, 4, 4), page.Version); r.StatusCode != 422 {
			t.Fatalf("wrong mask dimensions=%d", r.StatusCode)
		}
		if r := post([]byte("not png"), page.Version); r.StatusCode != 422 {
			t.Fatalf("invalid image=%d", r.StatusCode)
		}
		if r := post(art(t, 8, 6), page.Version-1); r.StatusCode != 409 {
			t.Fatalf("stale mask=%d", r.StatusCode)
		}
		if r := post(art(t, 8, 6), page.Version); r.StatusCode != 201 {
			t.Fatalf("mask upload=%d %s", r.StatusCode, r.Body)
		}
		var count int
		if err := ta.Deps.DB.Get(ctx, &count, `SELECT COUNT(*) FROM assets WHERE project_id = ? AND kind = 'region_mask'`, p.ID); err != nil || count != 1 {
			t.Fatalf("mask rows=%d %v", count, err)
		}
	})
}

func fmtInt(v int) string { return strconv.Itoa(v) }
