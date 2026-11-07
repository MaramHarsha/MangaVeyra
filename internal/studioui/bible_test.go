package studioui_test

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

func bibleEach(t *testing.T, fn func(*testing.T, *testutil.TestApp)) {
	t.Helper()
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) { fn(t, testutil.NewTestApp(t, testutil.WithDialect(dialect))) })
	}
}
func bibleProject(t *testing.T, ta *testutil.TestApp, userID, title string) studio.Project {
	t.Helper()
	svc := core.MustUse[studio.Service](ta.Deps)
	actor, _ := svc.ActorFromUser(context.Background(), userID)
	p, err := svc.CreateProject(context.Background(), actor, studio.NewProject{Title: title, Premise: "A courier crosses the flooded city.", Style: "seinen_bw", Format: "manga_rtl", Language: "en", PageSize: "b5", PageWidth: 1600, PageHeight: 2400})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func bibleImageBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 9, 12))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func bibleUploadPost(t *testing.T, c *testutil.Client, path string, fields map[string]string, data []byte) *testutil.Response {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if data != nil {
		f, err := mw.CreateFormFile("file", "reference.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return c.Do(http.MethodPost, path, &b, mw.FormDataContentType())
}
func bibleCount(t *testing.T, ta *testutil.TestApp, query string, args ...any) int {
	t.Helper()
	var n int
	if err := ta.Deps.DB.Get(context.Background(), &n, query, args...); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBibleCreateUploadProfileOutfitSettingAndRestore(t *testing.T) {
	bibleEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		u, c := ta.UserClient(t, auth.RoleCreator)
		p := bibleProject(t, ta, u.ID, "Tidebreak Courier")
		base := "/studio/p/" + p.ID
		svc := core.MustUse[studio.Service](ta.Deps)
		for _, path := range []string{"/studio/projects", "/studio/characters", "/studio/setting", base + "/characters", base + "/characters/new", base + "/setting"} {
			resp := c.Get(path)
			if resp.StatusCode != 200 {
				t.Fatalf("GET %s=%d %.1500s", path, resp.StatusCode, resp.Body)
			}
			mustNotContain(t, string(resp.Body), "bible.new.", "bible.projects.", "bible.setting.")
		}
		created := bibleUploadPost(t, c, base+"/characters/new", map[string]string{"name": "凛 Asagiri", "role": "Courier", "category": "main", "mode": "upload", "style": "series", "look": "Blue coat", "personality": "Reckless, loyal."}, bibleImageBytes(t))
		if created.StatusCode != 303 {
			t.Fatalf("create=%d %.2000s", created.StatusCode, created.Body)
		}
		characters, err := svc.ListCharacters(ctx, p.ID)
		if err != nil || len(characters) != 1 {
			t.Fatalf("characters=%#v %v", characters, err)
		}
		character := characters[0]
		detail := base + "/characters/" + character.ID
		if character.PortraitAssetID == "" || len(character.Refs) != 1 {
			t.Fatalf("upload not attached=%#v", character)
		}
		page := c.Get(detail)
		mustContain(t, string(page.Body), "凛 Asagiri", "MAIN REFERENCE", `src="/media/`, "Outfits", "Relationships", `disabled`)
		profile := url.Values{"name": {"凛 朝霧"}, "role": {"Courier"}, "role_line": {"LEAD · COURIER"}, "age": {"19"}, "look": {"Blue coat"}, "personality": {"Loyal"}, "speech_style": {"Short sentences"}, "category": {"main"}, "revision": {character.UpdatedAt.Format(time.RFC3339Nano)}}
		if resp := postForm(c, detail, profile); resp.StatusCode != 303 {
			t.Fatalf("profile=%d %.1000s", resp.StatusCode, resp.Body)
		}
		stale := postForm(c, detail, profile)
		if stale.StatusCode != 409 {
			t.Fatalf("stale=%d", stale.StatusCode)
		}
		mustContain(t, string(stale.Body), `value="凛 朝霧"`, "Reload latest")
		character, _ = svc.GetCharacter(ctx, character.ID)
		outfit := bibleUploadPost(t, c, detail+"/outfits", map[string]string{"name": "Courier coat", "description": "Brass clasps", "default": "on", "revision": character.UpdatedAt.Format(time.RFC3339Nano)}, bibleImageBytes(t))
		if outfit.StatusCode != 303 {
			t.Fatalf("outfit=%d %.1200s", outfit.StatusCode, outfit.Body)
		}
		character, _ = svc.GetCharacter(ctx, character.ID)
		if len(character.Outfits) != 1 || !character.Outfits[0].IsDefault {
			t.Fatalf("outfits=%#v", character.Outfits)
		}
		beforeAssets := bibleCount(t, ta, `SELECT COUNT(*) FROM assets WHERE project_id = ?`, p.ID)
		bad := bibleUploadPost(t, c, detail+"/references", map[string]string{"kind": "front", "label": "正面", "revision": character.UpdatedAt.Format(time.RFC3339Nano)}, []byte("not a png"))
		if bad.StatusCode != 422 || bibleCount(t, ta, `SELECT COUNT(*) FROM assets WHERE project_id = ?`, p.ID) != beforeAssets {
			t.Fatalf("bad upload=%d leaked metadata", bad.StatusCode)
		}
		mustContain(t, string(bad.Body), "Reselect the file", `value="正面"`, `aria-invalid="true"`)
		ref := bibleUploadPost(t, c, detail+"/references", map[string]string{"kind": "front", "label": "正面", "revision": character.UpdatedAt.Format(time.RFC3339Nano)}, bibleImageBytes(t))
		if ref.StatusCode != 303 {
			t.Fatalf("ref=%d %.1000s", ref.StatusCode, ref.Body)
		}
		// A stale upload stages the bytes but must roll back its row and attachment.
		beforeAssets = bibleCount(t, ta, `SELECT COUNT(*) FROM assets WHERE project_id = ?`, p.ID)
		staleRef := bibleUploadPost(t, c, detail+"/references", map[string]string{"kind": "front", "revision": character.UpdatedAt.Format(time.RFC3339Nano)}, bibleImageBytes(t))
		if staleRef.StatusCode != 409 || bibleCount(t, ta, `SELECT COUNT(*) FROM assets WHERE project_id = ?`, p.ID) != beforeAssets {
			t.Fatalf("stale upload=%d leaked metadata", staleRef.StatusCode)
		}
		setting := bibleUploadPost(t, c, base+"/setting", map[string]string{"kind": "location", "name": "Salt City rooftops", "description": "Night & dusk", "tags": "night, dusk"}, bibleImageBytes(t))
		if setting.StatusCode != 303 {
			t.Fatalf("setting=%d %.1000s", setting.StatusCode, setting.Body)
		}
		settings, err := svc.ListSettingItems(ctx, p.ID, "location")
		if err != nil || len(settings) != 1 || settings[0].AssetID == "" {
			t.Fatalf("settings=%#v %v", settings, err)
		}
		locationPage := c.Get(base + "/setting?kind=location")
		mustContain(t, string(locationPage.Body), "Salt City rooftops", "Locations 1", "Props 0", `src="/media/`)
		mustNotContain(t, string(c.Get(base+"/setting?kind=prop").Body), "Salt City rooftops")
		mustContain(t, string(c.Get(base+"/characters?category=main").Body), "凛", "朝霧", "LEAD · COURIER")
		mustNotContain(t, string(c.Get(base+"/characters?category=extra").Body), "凛 朝霧")
		c.Header.Set("Accept-Language", "ja")
		japanese := c.Get(base + "/characters")
		mustContain(t, string(japanese.Body), "キャラクター", "キャラクターを作成")
		mustNotContain(t, string(japanese.Body), "bible.characters.")
	})
}

func TestBibleLibrariesFilterAndDenyCrossProjectOrBearer(t *testing.T) {
	bibleEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		u, c := ta.UserClient(t, auth.RoleCreator)
		p := bibleProject(t, ta, u.ID, "Tidebreak Courier")
		bibleProject(t, ta, u.ID, "Glass Orchard")
		other, otherClient := ta.UserClient(t, auth.RoleCreator)
		foreign := bibleProject(t, ta, other.ID, "Private Lantern")
		svc := core.MustUse[studio.Service](ta.Deps)
		actor, _ := svc.ActorFromUser(ctx, u.ID)
		character, err := svc.CreateCharacter(ctx, actor, studio.NewCharacter{ProjectID: p.ID, Name: "Rin", Category: "main"})
		if err != nil {
			t.Fatal(err)
		}
		paths := []string{"/studio/p/" + p.ID + "/characters", "/studio/p/" + p.ID + "/characters/" + character.ID, "/studio/p/" + p.ID + "/setting"}
		for _, path := range paths {
			if resp := otherClient.Get(path); resp.StatusCode != 404 {
				t.Fatalf("cross-project %s=%d", path, resp.StatusCode)
			}
		}
		forged := "/studio/p/" + foreign.ID + "/characters/" + character.ID
		if resp := otherClient.Get(forged); resp.StatusCode != 404 {
			t.Fatalf("foreign ancestry=%d", resp.StatusCode)
		}
		if resp := ta.NewClient().Get("/studio/projects"); resp.StatusCode != 303 {
			t.Fatalf("anonymous=%d", resp.StatusCode)
		}
		raw, _, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: u.ID, Kind: auth.TokenAPI, Name: "Bible scope"})
		if err != nil {
			t.Fatal(err)
		}
		if resp := ta.BearerClient(raw).Get(paths[0]); resp.StatusCode != 403 {
			t.Fatalf("bearer=%d", resp.StatusCode)
		}
		filtered := c.Get("/studio/projects?q=Courier&sort=title&view=list")
		mustContain(t, string(filtered.Body), "Tidebreak Courier", "mv-bible__projects--list", "Clear project search")
		mustNotContain(t, string(filtered.Body), "Glass Orchard", "Private Lantern")
		if resp := c.Get("/studio/characters?project=" + p.ID); resp.StatusCode != 303 || resp.Header.Get("Location") != paths[0] {
			t.Fatalf("choose=%d %s", resp.StatusCode, resp.Header.Get("Location"))
		}
		if resp := c.Get("/studio/characters?project=" + foreign.ID); resp.StatusCode != 404 {
			t.Fatalf("foreign choose=%d", resp.StatusCode)
		}
		// A saved job's nested request binds the UI to this exact character.
		job, err := ta.Deps.Jobs.Enqueue(ctx, jobs.EnqueueParams{Type: gen.JobCharacterSheet, UserID: u.ID, Scope: jobs.Scope{Kind: jobs.ScopeProject, ProjectID: p.ID}, Input: map[string]any{"version": 1, "request": gen.CharacterSheetRequest{ProjectID: p.ID, CharacterID: character.ID, Count: 4}}})
		if err != nil {
			t.Fatal(err)
		}
		statusPath := paths[1] + "/sheets/" + job.ID
		resp := c.Get(statusPath)
		if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("job status=%d %v", resp.StatusCode, resp.Header)
		}
		mustContain(t, string(resp.Body), "hx-get=", job.ID, "Refresh")
		second, err := svc.CreateCharacter(ctx, actor, studio.NewCharacter{ProjectID: p.ID, Name: "Kaito"})
		if err != nil {
			t.Fatal(err)
		}
		if resp := c.Get("/studio/p/" + p.ID + "/characters/" + second.ID + "/sheets/" + job.ID); resp.StatusCode != 404 {
			t.Fatalf("wrong character job=%d", resp.StatusCode)
		}
		if resp := otherClient.Get(statusPath); resp.StatusCode != 404 {
			t.Fatalf("foreign job=%d", resp.StatusCode)
		}
	})
}

func TestBibleValidationRetainsFieldsAndRejectsInvalidFiles(t *testing.T) {
	bibleEach(t, func(t *testing.T, ta *testutil.TestApp) {
		u, c := ta.UserClient(t, auth.RoleCreator)
		p := bibleProject(t, ta, u.ID, "Salt City")
		path := "/studio/p/" + p.ID + "/characters/new"
		resp := bibleUploadPost(t, c, path, map[string]string{"name": "", "mode": "upload", "style": "series", "look": "Keep my coat description"}, bibleImageBytes(t))
		if resp.StatusCode != 422 {
			t.Fatalf("invalid name=%d", resp.StatusCode)
		}
		mustContain(t, string(resp.Body), "Keep my coat description", `id="bible-name"`, `aria-invalid="true"`)
		if n := bibleCount(t, ta, `SELECT COUNT(*) FROM characters WHERE project_id = ?`, p.ID); n != 0 {
			t.Fatalf("invalid created %d characters", n)
		}
		resp = bibleUploadPost(t, c, path, map[string]string{"name": "Sora", "mode": "upload", "style": "series"}, []byte("not image"))
		if resp.StatusCode != 422 {
			t.Fatalf("invalid image=%d", resp.StatusCode)
		}
		if n := bibleCount(t, ta, `SELECT COUNT(*) FROM characters WHERE project_id = ?`, p.ID); n != 0 {
			t.Fatalf("invalid image created %d characters", n)
		}
		resp = bibleUploadPost(t, c, "/studio/p/"+p.ID+"/setting", map[string]string{"name": "Keep setting", "kind": "other", "description": "Preserve this text"}, nil)
		if resp.StatusCode != 422 {
			t.Fatalf("invalid setting=%d", resp.StatusCode)
		}
		mustContain(t, string(resp.Body), "Keep setting", "Preserve this text")
		if n := bibleCount(t, ta, `SELECT COUNT(*) FROM setting_items WHERE project_id = ?`, p.ID); n != 0 {
			t.Fatalf("invalid setting created %d", n)
		}
		// The request size is bounded before multipart files can be staged.
		resp = c.Do(http.MethodPost, path, strings.NewReader(strings.Repeat("x", 18<<20)), "multipart/form-data; boundary=x")
		if resp.StatusCode != 413 {
			t.Fatalf("oversized=%d", resp.StatusCode)
		}
	})
}

func TestBibleUnchangedSavedCharacterKeepsRevisionAndIdentity(t *testing.T) {
	bibleEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		u, c := ta.UserClient(t, auth.RoleCreator)
		p := bibleProject(t, ta, u.ID, "Stable reference brief")
		svc := core.MustUse[studio.Service](ta.Deps)
		actor, _ := svc.ActorFromUser(ctx, u.ID)
		character, err := svc.CreateCharacter(ctx, actor, studio.NewCharacter{ProjectID: p.ID, Name: "Rin"})
		if err != nil {
			t.Fatal(err)
		}
		resp := bibleUploadPost(t, c, "/studio/p/"+p.ID+"/characters/new", map[string]string{"name": "Rin", "mode": "save", "style": "series", "category": "supporting", "character_id": character.ID, "revision": character.UpdatedAt.Format(time.RFC3339Nano)}, nil)
		if resp.StatusCode != 303 {
			t.Fatalf("resubmit=%d %.1000s", resp.StatusCode, resp.Body)
		}
		after, err := svc.GetCharacter(ctx, character.ID)
		if err != nil || !after.UpdatedAt.Equal(character.UpdatedAt) {
			t.Fatalf("unchanged brief invalidated sheet fence: %v %v", after.UpdatedAt, err)
		}
		if n := bibleCount(t, ta, `SELECT COUNT(*) FROM characters WHERE project_id = ?`, p.ID); n != 1 {
			t.Fatalf("duplicate characters=%d", n)
		}
	})
}
