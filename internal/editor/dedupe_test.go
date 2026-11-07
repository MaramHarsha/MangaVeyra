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
	"sync"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

type generated struct {
	Job struct {
		ID    string `json:"id"`
		State string `json:"state"`
	} `json:"job"`
	Existing bool `json:"existing"`
}

func generate(t *testing.T, c *testutil.Client, path string, body map[string]any) (int, generated) {
	t.Helper()
	r := c.PostJSON(path+"/generate", body)
	var out generated
	if r.StatusCode == http.StatusOK || r.StatusCode == http.StatusAccepted {
		if err := json.Unmarshal(r.Body, &out); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Fatalf("generate=%d %s", r.StatusCode, r.Body)
	}
	return r.StatusCode, out
}

func countJobs(t *testing.T, ta *testutil.TestApp, projectID, typ string) int {
	t.Helper()
	var n int
	if err := ta.Deps.DB.Get(context.Background(), &n, `SELECT COUNT(*) FROM jobs WHERE project_id = ? AND type = ?`, projectID, typ); err != nil {
		t.Fatal(err)
	}
	return n
}

// mask returns a black PNG with a white rectangle starting at x.
func mask(t *testing.T, w, h, x int) []byte {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	for py := range h {
		for px := range w {
			c := color.NRGBA{A: 255}
			if px >= x && px < x+12 && py >= 4 && py < h-4 {
				c = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
			}
			im.SetNRGBA(px, py, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestEditorGenerationReusesTheActiveJobForTheSameRequest(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		_, c, p, _, base := fixture(t, ta)
		page := doc(t, c.PostJSON(base+"/pages", map[string]string{"layout_id": "reveal-5"}), 201).Page
		path := base + "/pages/" + page.ID
		request := func(panel, layer, kind, prompt string) map[string]any {
			return map[string]any{"kind": kind, "panel_id": panel, "layer_id": layer, "prompt": prompt, "base_version": page.Version, "routing_mode": "balanced"}
		}
		first := request(page.Panels[0].ID, "", "panel", "Rin vaults the railing.")

		// Concurrent identical submissions (a double click, a retried request)
		// all receive the one job; only one generation is queued.
		const n = 6
		var wg sync.WaitGroup
		statuses := make([]int, n)
		ids := make([]string, n)
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := c.PostJSON(path+"/generate", first)
				statuses[i] = r.StatusCode
				var out generated
				_ = json.Unmarshal(r.Body, &out)
				ids[i] = out.Job.ID
			}()
		}
		wg.Wait()
		accepted := 0
		for i := range n {
			if statuses[i] == http.StatusAccepted {
				accepted++
			} else if statuses[i] != http.StatusOK {
				t.Fatalf("concurrent generate %d = %d", i, statuses[i])
			}
			if ids[i] == "" || ids[i] != ids[0] {
				t.Fatalf("concurrent submissions got different jobs: %v", ids)
			}
		}
		if accepted != 1 || countJobs(t, ta, p.ID, gen.JobPanel) != 1 {
			t.Fatalf("accepted=%d jobs=%d, want one queued generation", accepted, countJobs(t, ta, p.ID, gen.JobPanel))
		}
		status, again := generate(t, c, path, first)
		if status != http.StatusOK || !again.Existing || again.Job.ID != ids[0] {
			t.Fatalf("repeat=%d %#v", status, again)
		}

		// A different prompt, panel or layer scope is different work.
		_, otherPrompt := generate(t, c, path, request(page.Panels[0].ID, "", "panel", "Rin lands on the roof."))
		_, otherPanel := generate(t, c, path, request(page.Panels[1].ID, "", "panel", "Rin vaults the railing."))
		bg := page.Panels[0].Layers[0]
		if bg.Kind != pagedoc.LayerBackground {
			t.Fatalf("first layer=%s", bg.Kind)
		}
		_, layer := generate(t, c, path, request(page.Panels[0].ID, bg.ID, "layer", "Rin vaults the railing."))
		_, layerAgain := generate(t, c, path, request(page.Panels[0].ID, bg.ID, "layer", "Rin vaults the railing."))
		seen := map[string]bool{ids[0]: true}
		for _, id := range []string{otherPrompt.Job.ID, otherPanel.Job.ID, layer.Job.ID} {
			if seen[id] {
				t.Fatalf("distinct requests shared job %s", id)
			}
			seen[id] = true
		}
		if layerAgain.Job.ID != layer.Job.ID || !layerAgain.Existing {
			t.Fatalf("repeated layer generation queued again: %#v", layerAgain)
		}
		if got := countJobs(t, ta, p.ID, gen.JobPanel); got != 4 {
			t.Fatalf("jobs=%d want 4", got)
		}

		// Another member making the same request gets their own job.
		member, mc := ta.UserClient(t, auth.RoleCreator)
		if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO project_members(project_id,user_id,role) VALUES(?,?,?)`, p.ID, member.ID, studio.RoleEditor); err != nil {
			t.Fatal(err)
		}
		_, theirs := generate(t, mc, path, first)
		if theirs.Existing || theirs.Job.ID == ids[0] {
			t.Fatalf("another user's request was merged: %#v", theirs)
		}

		// Once the job is finished the same request starts a new generation.
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE jobs SET state = 'done', finished_at = ? WHERE id = ?`, db.Now(), ids[0]); err != nil {
			t.Fatal(err)
		}
		status, fresh := generate(t, c, path, first)
		if status != http.StatusAccepted || fresh.Existing || fresh.Job.ID == ids[0] {
			t.Fatalf("finished job was reused: %d %#v", status, fresh)
		}
	})
}

func TestEditorRegionGenerationReusesTheActiveJobForTheSameMask(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		u, c, p, _, base := fixture(t, ta)
		page := doc(t, c.PostJSON(base+"/pages", map[string]string{"layout_id": "splash"}), 201).Page
		store := core.MustUse[pagestore.Service](ta.Deps)
		a, err := assets.From(ta.Deps).Save(ctx, assets.SaveParams{OwnerID: u.ID, ProjectID: p.ID, Data: art(t, 64, 64), Source: assets.SourceUpload})
		if err != nil {
			t.Fatal(err)
		}
		layerID := page.Panels[0].Layers[0].ID
		err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			_, err := store.SetLayerAssetTx(ctx, tx, pagestore.SetLayerAsset{PageID: page.ID, PanelID: page.Panels[0].ID, LayerID: layerID, Kind: "background", AssetID: a.ID, Provenance: "human"}, pagestore.Change{ActorKind: "user", ActorID: u.ID, Action: "upload"})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		page, err = store.GetPage(ctx, page.ID)
		if err != nil {
			t.Fatal(err)
		}
		path := base + "/pages/" + page.ID
		upload := func(data []byte) string {
			var b bytes.Buffer
			mw := multipart.NewWriter(&b)
			_ = mw.WriteField("layer_id", layerID)
			_ = mw.WriteField("base_version", strconv.Itoa(page.Version))
			f, err := mw.CreateFormFile("file", "mask.png")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = f.Write(data)
			_ = mw.Close()
			r := c.Do(http.MethodPost, path+"/masks", &b, mw.FormDataContentType())
			if r.StatusCode != http.StatusCreated {
				t.Fatalf("mask=%d %s", r.StatusCode, r.Body)
			}
			var out struct {
				ID string `json:"id"`
			}
			r.JSON(t, &out)
			return out.ID
		}
		region := func(maskID, prompt string) map[string]any {
			return map[string]any{"kind": "region", "panel_id": page.Panels[0].ID, "layer_id": layerID, "mask_id": maskID, "prompt": prompt, "base_version": page.Version, "routing_mode": "balanced"}
		}
		// Each submission uploads its own copy of the same mask.
		one, two, three := upload(mask(t, 64, 64, 8)), upload(mask(t, 64, 64, 8)), upload(mask(t, 64, 64, 8))
		if one == two {
			t.Fatal("expected separate mask assets")
		}
		var wg sync.WaitGroup
		ids := make([]string, 2)
		for i, id := range []string{two, three} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := c.PostJSON(path+"/generate", region(id, "Add a wave emblem."))
				var out generated
				_ = json.Unmarshal(r.Body, &out)
				ids[i] = out.Job.ID
			}()
		}
		wg.Wait()
		_, first := generate(t, c, path, region(one, "Add a wave emblem."))
		if ids[0] == "" || ids[0] != ids[1] || first.Job.ID != ids[0] || !first.Existing {
			t.Fatalf("same mask and prompt queued twice: %v %#v", ids, first)
		}
		if got := countJobs(t, ta, p.ID, gen.JobRegionEdit); got != 1 {
			t.Fatalf("region jobs=%d want 1", got)
		}
		_, otherMask := generate(t, c, path, region(upload(mask(t, 64, 64, 30)), "Add a wave emblem."))
		_, otherPrompt := generate(t, c, path, region(one, "Add a gull feather."))
		if otherMask.Existing || otherPrompt.Existing || otherMask.Job.ID == first.Job.ID || otherPrompt.Job.ID == first.Job.ID || otherMask.Job.ID == otherPrompt.Job.ID {
			t.Fatalf("different masks or prompts were merged: %#v %#v", otherMask, otherPrompt)
		}
		if got := countJobs(t, ta, p.ID, gen.JobRegionEdit); got != 3 {
			t.Fatalf("region jobs=%d want 3", got)
		}
	})
}
