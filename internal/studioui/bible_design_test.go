package studioui_test

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// C2–C5 follow the artboards: "ROLE · ROLE LINE" eyebrows without dangling
// separators, the C4 role select (with its role line) on the C3 profile form,
// a pluralized reference count, relationship chips in the Soon state and the
// kit's upload area instead of a bare native file input.
func TestBibleScreensFollowTheArtboards(t *testing.T) {
	bibleEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		u, c := ta.UserClient(t, auth.RoleCreator)
		p := bibleProject(t, ta, u.ID, "Tidebreak Courier")
		svc := core.MustUse[studio.Service](ta.Deps)
		actor, _ := svc.ActorFromUser(ctx, u.ID)
		rin, err := svc.CreateCharacter(ctx, actor, studio.NewCharacter{ProjectID: p.ID, Name: "Rin Asagiri", Kanji: "凛", Role: "lead", RoleLine: "Courier", Category: "main"})
		if err != nil {
			t.Fatal(err)
		}
		sora, err := svc.CreateCharacter(ctx, actor, studio.NewCharacter{ProjectID: p.ID, Name: "Sora Vell", Role: "antagonist", Category: "main"})
		if err != nil {
			t.Fatal(err)
		}
		base := "/studio/p/" + p.ID + "/characters"

		library := string(c.Get(base).Body)
		mustContain(t, library, "Lead · Courier", ">Antagonist<")

		detail := string(c.Get(base + "/" + rin.ID).Body)
		mustContain(t, detail, "Lead · Courier · Tidebreak Courier", `class="mv-bible__kanji"`, "凛",
			"mv-bible__relationship--add", "Relationships between characters are coming soon.")
		mustNotContain(t, detail, "1 references")
		bare := string(c.Get(base + "/" + sora.ID).Body)
		eyebrow := regexp.MustCompile(`(?s)<p class="mv-bible__eyebrow">\s*(.*?)\s*</p>`).FindStringSubmatch(bare)
		if eyebrow == nil || eyebrow[1] != "Antagonist · Tidebreak Courier" {
			t.Fatalf("eyebrow without role line = %q", eyebrow)
		}

		edit := string(c.Get(base + "/" + sora.ID + "?edit=profile").Body)
		role := regexp.MustCompile(`(?s)<select[^>]*name="role".*?</select>`).FindString(edit)
		if !strings.Contains(role, `<option value="antagonist" selected>Antagonist</option>`) {
			t.Fatalf("profile role is not the C4 select: %.400s", role)
		}
		mustContain(t, edit, "mv-bible__rolefield", `name="role_line"`)
		mustNotContain(t, edit, `name="role" value="antagonist"`)

		upload := string(c.Get(base + "/" + rin.ID + "?add=reference").Body)
		mustContain(t, upload, `class="mv-filedrop"`, `class="mv-filedrop__input" id="bible-file" type="file" name="file"`, "Choose an image to upload")
		setting := string(c.Get("/studio/p/" + p.ID + "/setting?add=location").Body)
		if strings.Contains(setting, `name="file"`) {
			mustContain(t, setting, `class="mv-filedrop__input"`)
		}

		newChar := string(c.Get(base + "/new").Body)
		mustContain(t, newChar, "mv-bible__rolefield", `id="bible-role_line"`)
		if strings.Count(newChar, `name="role_line"`) != 1 {
			t.Fatal("C4 renders the role line twice")
		}
	})
}
