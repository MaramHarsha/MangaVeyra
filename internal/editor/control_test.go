package editor_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"strconv"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestVisualHistoryScopesSnapshotsAndSketchUploadsToTheLoadedPage(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		_, client, project, _, base := fixture(t, ta)
		first := doc(t, client.PostJSON(base+"/pages", map[string]string{"layout_id": "splash"}), 201).Page
		second := doc(t, client.PostJSON(base+"/pages", map[string]string{"layout_id": "splash"}), 201).Page
		path := base + "/pages/" + first.ID
		other := base + "/pages/" + second.ID
		versions, err := core.MustUse[pagestore.Service](ta.Deps).Versions(ctx, first.ID, 10)
		if err != nil {
			t.Fatal(err)
		}
		snapshot := "/versions/" + versions[0].ID
		if r := client.Get(path + snapshot + "?image=1"); r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/png" {
			t.Fatalf("snapshot proof=%d", r.StatusCode)
		}
		if r := client.Get(other + snapshot); r.StatusCode != 404 {
			t.Fatalf("snapshot read through another page=%d", r.StatusCode)
		}
		_, outsider := ta.UserClient(t, auth.RoleCreator)
		if r := outsider.Get(path + snapshot); r.StatusCode != 404 {
			t.Fatalf("foreign snapshot=%d", r.StatusCode)
		}
		post := func(data []byte, version int) *testutil.Response {
			var body bytes.Buffer
			form := multipart.NewWriter(&body)
			_ = form.WriteField("base_version", strconv.Itoa(version))
			file, e := form.CreateFormFile("file", "original-sketch.png")
			if e != nil {
				t.Fatal(e)
			}
			_, _ = file.Write(data)
			_ = form.Close()
			return client.Do(http.MethodPost, path+"/sketch", &body, form.FormDataContentType())
		}
		if r := post(art(t, 64, 96), first.Version-1); r.StatusCode != 409 {
			t.Fatalf("stale sketch=%d", r.StatusCode)
		}
		if r := post(art(t, 32, 32), first.Version); r.StatusCode != 422 {
			t.Fatalf("unbounded sketch=%d", r.StatusCode)
		}
		if r := post(art(t, 64, 96), first.Version); r.StatusCode != 201 {
			t.Fatalf("sketch=%d %s", r.StatusCode, r.Body)
		}
		locked := doc(t, client.PostJSON(path+"/lock", map[string]any{"locked": true, "base_version": first.Version}), 200).Page
		if r := post(art(t, 64, 96), locked.Version); r.StatusCode != 423 {
			t.Fatalf("locked sketch=%d %s", r.StatusCode, r.Body)
		}
		var count int
		if err = ta.Deps.DB.Get(ctx, &count, `SELECT COUNT(*) FROM assets WHERE project_id=? AND kind='sketch'`, project.ID); err != nil || count != 1 {
			t.Fatalf("rejected upload persisted: %d %v", count, err)
		}
		var proof map[string]any
		if err = json.Unmarshal(client.Get(path+snapshot).Body, &proof); err != nil {
			t.Fatal(err)
		}
	})
}
