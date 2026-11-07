package studioui_test

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func postWizard(t *testing.T, client *testutil.Client) *testutil.Response {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"title": "Harbor Ghosts", "chapter_title": "The Quiet Bell", "mode": "premise",
		"premise": "A courier follows a bell into the drowned streets beneath the harbor.",
		"style":   "seinen_bw", "format": "manga_rtl", "language": "en", "pages_target": "12",
	} {
		if err := mw.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return client.Do(http.MethodPost, "/studio/new/premise", &body, mw.FormDataContentType())
}

func scriptForm(doc studio.ScriptDoc, revision time.Time) url.Values {
	form := url.Values{"revision": {revision.Format(time.RFC3339Nano)}, "chapter_summary": {doc.ChapterSummary}}
	for _, scene := range doc.Scenes {
		form.Set("scene."+scene.ID+".title", scene.Title)
		form.Set("scene."+scene.ID+".summary", scene.Summary)
		form.Set("scene."+scene.ID+".time_of_day", scene.TimeOfDay)
		for _, panel := range scene.Panels {
			prefix := "panel." + panel.ID
			form.Set(prefix+".page_number", strconv.Itoa(panel.PageNumber))
			form.Set(prefix+".camera", panel.Camera)
			form.Set(prefix+".shot", panel.Shot)
			form.Set(prefix+".emotion", panel.Emotion)
			form.Set(prefix+".action", panel.Action)
			form.Set(prefix+".notes", panel.Notes)
			for i, line := range panel.Dialogue {
				linePrefix := prefix + ".dialogue." + strconv.Itoa(i)
				form.Set(linePrefix+".speaker", line.Speaker)
				form.Set(linePrefix+".kind", line.Kind)
				form.Set(linePrefix+".text", line.Text)
			}
			for i, effect := range panel.SFX {
				effectPrefix := prefix + ".sfx." + strconv.Itoa(i)
				form.Set(effectPrefix+".text", effect.Text)
				form.Set(effectPrefix+".note", effect.Note)
			}
		}
	}
	return form
}

func postForm(client *testutil.Client, path string, form url.Values) *testutil.Response {
	return client.Do(http.MethodPost, path, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
}

func TestPlaceholderWizardStableEditConflictAndIDOR(t *testing.T) {
	ta := testutil.NewTestApp(t, testutil.WithBackground())
	ctx := context.Background()
	creator, creatorClient := ta.UserClient(t, auth.RoleCreator)

	if got := ta.NewClient().Get("/studio/new/premise"); got.StatusCode != http.StatusSeeOther || !strings.HasPrefix(got.Header.Get("Location"), "/login?next=") {
		t.Fatalf("anonymous = %d %q", got.StatusCode, got.Header.Get("Location"))
	}
	rawToken, _, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: creator.ID, Kind: auth.TokenAPI, Name: "studio-browser-test"})
	if err != nil {
		t.Fatal(err)
	}
	if got := ta.BearerClient(rawToken).Get("/studio/new/premise"); got.StatusCode != http.StatusForbidden {
		t.Fatalf("bearer = %d", got.StatusCode)
	}

	created := postWizard(t, creatorClient)
	if created.StatusCode != http.StatusSeeOther || !strings.Contains(created.Header.Get("Location"), "/story/") {
		t.Fatalf("create = %d %q: %.1200s", created.StatusCode, created.Header.Get("Location"), created.Body)
	}
	var projectID, chapterID string
	if err := ta.Deps.DB.Get(ctx, &projectID, `SELECT id FROM projects WHERE title = ?`, "Harbor Ghosts"); err != nil {
		t.Fatal(err)
	}
	if err := ta.Deps.DB.Get(ctx, &chapterID, `SELECT id FROM chapters WHERE project_id = ?`, projectID); err != nil {
		t.Fatal(err)
	}
	storyURL := "/studio/p/" + projectID + "/story/" + chapterID
	studioService := core.MustUse[studio.Service](ta.Deps)

	var doc studio.ScriptDoc
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		doc, err = studioService.GetScript(ctx, chapterID)
		if err == nil && len(doc.Scenes) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(doc.Scenes) == 0 {
		t.Fatal("placeholder job did not persist a script")
	}
	chapter, err := studioService.GetChapter(ctx, chapterID)
	if err != nil {
		t.Fatal(err)
	}
	page := creatorClient.Get(storyURL)
	if page.StatusCode != http.StatusOK || !strings.Contains(string(page.Body), "Script editor") || strings.Contains(string(page.Body), "Generate script") {
		t.Fatalf("story = %d: %.2500s", page.StatusCode, page.Body)
	}
	oversized := creatorClient.Do(http.MethodPost, storyURL+"/script",
		strings.NewReader("chapter_summary="+strings.Repeat("x", (4<<20)+1)), "application/x-www-form-urlencoded")
	if oversized.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized edit = %d, want 413", oversized.StatusCode)
	}
	if dashboard := creatorClient.Get("/studio"); dashboard.StatusCode != http.StatusOK || !strings.Contains(string(dashboard.Body), "Harbor Ghosts") {
		t.Fatalf("dashboard = %d: %.2000s", dashboard.StatusCode, dashboard.Body)
	}

	beforeSceneID, beforePanelID := doc.Scenes[0].ID, doc.Scenes[0].Panels[0].ID
	form := scriptForm(doc, chapter.UpdatedAt)
	form.Set("chapter_summary", "A human-reviewed harbor summary.")
	form.Set("panel."+beforePanelID+".action", "Rin stops beneath the silent bell.")
	form.Set("panel.forged-id.action", "must be ignored")
	saved := postForm(creatorClient, storyURL+"/script", form)
	if saved.StatusCode != http.StatusSeeOther || saved.Header.Get("Location") != storyURL {
		t.Fatalf("save = %d %q: %.1500s", saved.StatusCode, saved.Header.Get("Location"), saved.Body)
	}
	after, err := studioService.GetScript(ctx, chapterID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Scenes[0].ID != beforeSceneID || after.Scenes[0].Panels[0].ID != beforePanelID ||
		after.Scenes[0].Panels[0].Action != "Rin stops beneath the silent bell." || after.ChapterSummary != "A human-reviewed harbor summary." {
		t.Fatalf("stable edit failed: %#v", after.Scenes[0].Panels[0])
	}

	stale := scriptForm(after, chapter.UpdatedAt)
	stale.Set("panel."+beforePanelID+".action", "UNSAVED CONFLICT VALUE")
	conflict := postForm(creatorClient, storyURL+"/script", stale)
	if conflict.StatusCode != http.StatusConflict || !strings.Contains(string(conflict.Body), "UNSAVED CONFLICT VALUE") ||
		!strings.Contains(string(conflict.Body), "Reload latest") {
		t.Fatalf("conflict = %d: %.2500s", conflict.StatusCode, conflict.Body)
	}

	jobsFound, err := ta.Deps.Jobs.List(ctx, jobs.Filter{UserID: creator.ID, ProjectID: projectID, ChapterID: chapterID,
		Types: []string{gen.JobScript}, Limit: 1})
	if err != nil || len(jobsFound) != 1 {
		t.Fatalf("job lookup = %v/%d", err, len(jobsFound))
	}
	jobURL := storyURL + "/jobs/" + jobsFound[0].ID
	_, attacker := ta.UserClient(t, auth.RoleCreator)
	for _, path := range []string{storyURL, jobURL} {
		if got := attacker.Get(path); got.StatusCode != http.StatusNotFound {
			t.Fatalf("attacker GET %s = %d", path, got.StatusCode)
		}
	}
	if got := postForm(attacker, storyURL+"/script", stale); got.StatusCode != http.StatusNotFound {
		t.Fatalf("attacker edit = %d", got.StatusCode)
	}

	currentChapter, err := studioService.GetChapter(ctx, chapterID)
	if err != nil {
		t.Fatal(err)
	}
	lockedForm := scriptForm(after, currentChapter.UpdatedAt)
	lockedForm.Set("panel."+beforePanelID+".action", "LOCKED UNSAVED VALUE")
	if _, err := ta.Deps.DB.Exec(ctx, `UPDATE chapters SET locked = TRUE WHERE id = ?`, chapterID); err != nil {
		t.Fatal(err)
	}
	locked := postForm(creatorClient, storyURL+"/script", lockedForm)
	if locked.StatusCode != http.StatusLocked || !strings.Contains(string(locked.Body), "LOCKED UNSAVED VALUE") ||
		strings.Contains(string(locked.Body), ">Save changes<") {
		t.Fatalf("locked edit = %d: %.2200s", locked.StatusCode, locked.Body)
	}

	reader, readerClient := ta.UserClient(t, auth.RoleReader)
	if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role, created_at) VALUES (?, ?, 'viewer', ?)`,
		projectID, reader.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	readPage := readerClient.Get(storyURL)
	readBody := string(readPage.Body)
	if readPage.StatusCode != http.StatusOK || !strings.Contains(readBody, "read-only") || strings.Contains(readBody, ">Save changes<") {
		t.Fatalf("reader page = %d readonly=%v save=%v: %.2200s", readPage.StatusCode,
			strings.Contains(readBody, "read-only"), strings.Contains(readBody, ">Save changes<"), readPage.Body)
	}
	if got := postForm(readerClient, storyURL+"/script", stale); got.StatusCode != http.StatusNotFound {
		t.Fatalf("reader edit = %d", got.StatusCode)
	}
}
