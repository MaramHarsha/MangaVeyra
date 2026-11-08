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
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func apiError(t *testing.T, r *testutil.Response, status int) (string, map[string]string) {
	t.Helper()
	if r.StatusCode != status {
		t.Fatalf("HTTP %d want %d: %.600s", r.StatusCode, status, r.Body)
	}
	var out struct {
		Error struct {
			Message string            `json:"message"`
			Fields  map[string]string `json:"fields"`
		} `json:"error"`
	}
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatal(err)
	}
	return out.Error.Message, out.Error.Fields
}

// D4 names the actual limits for every rejected sketch, instead of the generic
// "Some fields need attention." validation summary.
func TestSketchUploadsOutsideTheLimitsNameTheLimits(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		_, client, _, _, base := fixture(t, ta)
		page := doc(t, client.PostJSON(base+"/pages", map[string]string{"layout_id": "splash"}), 201).Page
		path := base + "/pages/" + page.ID
		want := i18n.T(context.Background(), "editor.control.sketch_invalid")
		if want == "" || want == "editor.control.sketch_invalid" {
			t.Fatalf("missing catalogue text %q", want)
		}
		post := func(data []byte) *testutil.Response {
			var body bytes.Buffer
			form := multipart.NewWriter(&body)
			_ = form.WriteField("base_version", strconv.Itoa(page.Version))
			file, err := form.CreateFormFile("file", "sketch.png")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = file.Write(data)
			_ = form.Close()
			return client.Do(http.MethodPost, path+"/sketch", &body, form.FormDataContentType())
		}
		for name, data := range map[string][]byte{"too wide": art(t, 5000, 300), "too small": art(t, 32, 32), "not an image": []byte("not an image")} {
			msg, fields := apiError(t, post(data), 422)
			if msg != want || fields["file"] != want {
				t.Fatalf("%s: message %q fields %v, want %q", name, msg, fields, want)
			}
		}
	})
}

// A sketch reference that is malformed or does not exist is a validation
// error, never a server fault.
func TestRegenerateRejectsUnknownSketchReferences(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		_, client, _, _, base := fixture(t, ta)
		page := doc(t, client.PostJSON(base+"/pages", map[string]string{"layout_id": "splash"}), 201).Page
		want := i18n.T(context.Background(), "editor.control.sketch_invalid")
		for _, id := range []string{"not-an-id", db.NewID()} {
			msg, _ := apiError(t, client.PostJSON(base+"/pages/"+page.ID+"/regenerate", map[string]any{"base_version": page.Version, "sketch_asset_id": id, "strength": .62, "finish": "screentone"}), 422)
			if msg != want {
				t.Fatalf("%s: message %q want %q", id, msg, want)
			}
		}
	})
}

// D12 opened for a character outside the project gets a specific not-found
// message so it can offer the project's characters.
func TestLockStatusNamesAMissingCharacter(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		_, client, _, _, base := fixture(t, ta)
		want := i18n.T(context.Background(), "editor.control.character_missing")
		u, _ := ta.UserClient(t, auth.RoleCreator)
		svc := core.MustUse[studio.Service](ta.Deps)
		a, _ := svc.ActorFromUser(context.Background(), u.ID)
		other, err := svc.CreateProject(context.Background(), a, studio.NewProject{Title: "Glass Orchard", Premise: "An orchard keeper hears the glass.", Style: "seinen_bw", Format: "manga_rtl", Language: "en", PageSize: "b5", PageWidth: 600, PageHeight: 800})
		if err != nil {
			t.Fatal(err)
		}
		foreign, err := svc.CreateCharacter(context.Background(), a, studio.NewCharacter{ProjectID: other.ID, Name: "Sora Vell", Category: "main"})
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"not-an-id", db.NewID(), foreign.ID} {
			msg, _ := apiError(t, client.Get(base+"/lock/"+id), 404)
			if msg != want {
				t.Fatalf("%s: message %q want %q", id, msg, want)
			}
		}
	})
}
