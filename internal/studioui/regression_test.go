package studioui_test

import (
	"context"
	"net/http"
	"regexp"
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

var formRevisionRe = regexp.MustCompile(`name="revision" value="([^"]+)"`)

type storyFixture struct {
	ta                   *testutil.TestApp
	client               *testutil.Client
	projectID, chapterID string
	storyURL             string
	studio               studio.Service
}

// forEachStory runs fn on every available dialect with a creator who has
// generated the placeholder script for one chapter.
func forEachStory(t *testing.T, fn func(*testing.T, storyFixture)) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			ta := testutil.NewTestApp(t, testutil.WithBackground(), testutil.WithDialect(dialect))
			ctx := context.Background()
			_, client := ta.UserClient(t, auth.RoleCreator)
			if r := postWizard(t, client); r.StatusCode != http.StatusSeeOther {
				t.Fatalf("wizard = %d", r.StatusCode)
			}
			f := storyFixture{ta: ta, client: client, studio: core.MustUse[studio.Service](ta.Deps)}
			if err := ta.Deps.DB.Get(ctx, &f.projectID, `SELECT id FROM projects WHERE title = ?`, "Harbor Ghosts"); err != nil {
				t.Fatal(err)
			}
			if err := ta.Deps.DB.Get(ctx, &f.chapterID, `SELECT id FROM chapters WHERE project_id = ?`, f.projectID); err != nil {
				t.Fatal(err)
			}
			f.storyURL = "/studio/p/" + f.projectID + "/story/" + f.chapterID
			for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
				doc, err := f.studio.GetScript(ctx, f.chapterID)
				if err == nil && len(doc.Scenes) > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("placeholder script not generated: %v", err)
				}
			}
			fn(t, f)
		})
	}
}

// A save rejected by form validation must re-render with the revision the
// user started from, so fixing the field and resubmitting still conflicts
// with a save that landed in between instead of silently overwriting it.
func TestRejectedSaveKeepsSubmittedRevision(t *testing.T) {
	forEachStory(t, func(t *testing.T, f storyFixture) {
		ctx := context.Background()
		doc, err := f.studio.GetScript(ctx, f.chapterID)
		if err != nil {
			t.Fatal(err)
		}
		chapter, err := f.studio.GetChapter(ctx, f.chapterID)
		if err != nil {
			t.Fatal(err)
		}
		rev1 := chapter.UpdatedAt
		panelID := doc.Scenes[0].Panels[0].ID

		other := scriptForm(doc, rev1)
		other.Set("chapter_summary", "Saved from the other tab.")
		if r := postForm(f.client, f.storyURL+"/script", other); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("other tab save = %d", r.StatusCode)
		}

		stale := scriptForm(doc, rev1)
		stale.Set("panel."+panelID+".page_number", "0")
		stale.Set("panel."+panelID+".action", "Stale tab edit.")
		rejected := postForm(f.client, f.storyURL+"/script", stale)
		if rejected.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("invalid save = %d, want 422", rejected.StatusCode)
		}
		m := formRevisionRe.FindStringSubmatch(string(rejected.Body))
		if m == nil || m[1] != rev1.Format(time.RFC3339Nano) {
			t.Fatalf("re-rendered revision = %v, want the submitted %s", m, rev1.Format(time.RFC3339Nano))
		}

		stale.Set("panel."+panelID+".page_number", "1")
		stale.Set("revision", m[1])
		if r := postForm(f.client, f.storyURL+"/script", stale); r.StatusCode != http.StatusConflict {
			t.Fatalf("resubmitted stale save = %d, want 409", r.StatusCode)
		}
		after, err := f.studio.GetScript(ctx, f.chapterID)
		if err != nil || after.ChapterSummary != "Saved from the other tab." {
			t.Fatalf("other tab's save was overwritten: %q, %v", after.ChapterSummary, err)
		}
	})
}

// A failed script job explains its cause, offers Retry only when retrying the
// same route can help, and always lets the user generate a fresh job.
func TestFailedScriptJobExplainsCauseAndOffersGenerate(t *testing.T) {
	forEachStory(t, func(t *testing.T, f storyFixture) {
		ctx := context.Background()
		found, err := f.ta.Deps.Jobs.List(ctx, jobs.Filter{ProjectID: f.projectID, ChapterID: f.chapterID,
			Types: []string{gen.JobScript}, Limit: 1})
		if err != nil || len(found) != 1 {
			t.Fatalf("script jobs = %v, %v", found, err)
		}
		job := found[0]
		for _, q := range []string{`DELETE FROM script_panels WHERE chapter_id = ?`, `DELETE FROM scenes WHERE chapter_id = ?`} {
			if _, err := f.ta.Deps.DB.Exec(ctx, q, f.chapterID); err != nil {
				t.Fatal(err)
			}
		}
		setFailure := func(code string, retryable bool) {
			t.Helper()
			errJSON := `{"code":"` + code + `","message":"provider detail","retryable":` + map[bool]string{true: "true", false: "false"}[retryable] + `}`
			if _, err := f.ta.Deps.DB.Exec(ctx, `UPDATE jobs SET state = 'failed', error = ? WHERE id = ?`, errJSON, job.ID); err != nil {
				t.Fatal(err)
			}
		}
		retryURL := f.storyURL + "/jobs/" + job.ID + "/retry"

		setFailure("spend_limit", false)
		page := string(f.client.Get(f.storyURL).Body)
		if !strings.Contains(page, "monthly remote-spend limit") {
			t.Fatal("spend-limit failure is not explained")
		}
		if strings.Contains(page, retryURL) {
			t.Fatal("Retry offered for a failure that would repeat")
		}
		if !strings.Contains(page, f.storyURL+"/generate") {
			t.Fatal("no way to generate a new script after a failed job")
		}
		// The job states live in C1's centre pane with a polite live region.
		if !strings.Contains(page, `<div class="mv-c1__gen">`) || !strings.Contains(page, `id="job-`+job.ID+`" aria-live="polite"`) {
			t.Fatal("failed job is not shown in the centre pane's live region")
		}
		if r := postForm(f.client, retryURL, nil); r.StatusCode != http.StatusConflict {
			t.Fatalf("retry of a non-retryable failure = %d, want 409", r.StatusCode)
		}

		setFailure("upstream", true)
		page = string(f.client.Get(f.storyURL).Body)
		if !strings.Contains(page, retryURL) || !strings.Contains(page, "did not respond correctly") {
			t.Fatal("retryable upstream failure lacks its explanation or Retry")
		}

		setFailure("spend_limit", false)
		if r := postForm(f.client, f.storyURL+"/generate", map[string][]string{"pages_target": {"8"}}); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("generate after a failed job = %d, want 303", r.StatusCode)
		}
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			doc, err := f.studio.GetScript(ctx, f.chapterID)
			if err == nil && len(doc.Scenes) > 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("regenerated script not saved: %v", err)
			}
		}
	})
}
