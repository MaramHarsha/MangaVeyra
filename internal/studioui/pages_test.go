package studioui_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

func TestStoryMakePagesRevisionAuthorizationAndEditorHandoff(t *testing.T) {
	forEachStory(t, func(t *testing.T, f storyFixture) {
		ctx := context.Background()
		chapter, err := f.studio.GetChapter(ctx, f.chapterID)
		if err != nil {
			t.Fatal(err)
		}
		form := url.Values{"revision": {chapter.UpdatedAt.Format(time.RFC3339Nano)}}
		_, outsider := f.ta.UserClient(t, auth.RoleCreator)
		if got := postForm(outsider, f.storyURL+"/pages", form); got.StatusCode != http.StatusNotFound {
			t.Fatalf("foreign chapter=%d", got.StatusCode)
		}
		writer, writerClient := f.ta.UserClient(t, auth.RoleCreator)
		if _, err := f.ta.Deps.DB.Exec(ctx, `INSERT INTO project_members (project_id,user_id,role) VALUES (?,?,'writer')`, f.projectID, writer.ID); err != nil {
			t.Fatal(err)
		}
		if got := postForm(writerClient, f.storyURL+"/pages", form); got.StatusCode != http.StatusNotFound {
			t.Fatalf("writer without art/letter permissions=%d", got.StatusCode)
		}
		if got := postForm(f.client, f.storyURL+"/pages", url.Values{}); got.StatusCode != http.StatusBadRequest {
			t.Fatalf("missing fence=%d", got.StatusCode)
		}
		if got := postForm(f.client, f.storyURL+"/pages", url.Values{"revision": {chapter.UpdatedAt.Add(-time.Second).Format(time.RFC3339Nano)}}); got.StatusCode != http.StatusConflict {
			t.Fatalf("stale fence=%d", got.StatusCode)
		}
		if _, err := f.ta.Deps.DB.Exec(ctx, `UPDATE chapters SET locked = TRUE WHERE id = ?`, chapter.ID); err != nil {
			t.Fatal(err)
		}
		if got := postForm(f.client, f.storyURL+"/pages", form); got.StatusCode != http.StatusLocked {
			t.Fatalf("locked chapter=%d", got.StatusCode)
		}
		if _, err := f.ta.Deps.DB.Exec(ctx, `UPDATE chapters SET locked = FALSE WHERE id = ?`, chapter.ID); err != nil {
			t.Fatal(err)
		}
		got := postForm(f.client, f.storyURL+"/pages", form)
		editor := "/studio/p/" + f.projectID + "/chapters/" + f.chapterID + "/edit"
		if got.StatusCode != http.StatusSeeOther || !strings.HasPrefix(got.Header.Get("Location"), editor+"?job=") {
			t.Fatalf("handoff=%d %s: %.1000s", got.StatusCode, got.Header.Get("Location"), got.Body)
		}
		id := strings.TrimPrefix(got.Header.Get("Location"), editor+"?job=")
		var job *jobs.Job
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			job, err = f.ta.Deps.Jobs.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if job.State == jobs.StateDone || job.State == jobs.StateFailed || time.Now().After(deadline) {
				break
			}
		}
		if job.Type != gen.JobPagesFromScript || job.State != jobs.StateDone {
			t.Fatalf("pages job=%+v", job)
		}
		pages, err := core.MustUse[pagestore.Service](f.ta.Deps).ChapterPages(ctx, chapter.ID)
		if err != nil || len(pages) == 0 || len(pages[0].Panels) == 0 {
			t.Fatalf("pages=%d %v", len(pages), err)
		}
		chapter, err = f.studio.GetChapter(ctx, chapter.ID)
		if err != nil {
			t.Fatal(err)
		}
		form.Set("revision", chapter.UpdatedAt.Format(time.RFC3339Nano))
		if got := postForm(f.client, f.storyURL+"/pages", form); got.StatusCode != http.StatusSeeOther || got.Header.Get("Location") != editor {
			t.Fatalf("reopen=%d %s", got.StatusCode, got.Header.Get("Location"))
		}
		count, err := f.ta.Deps.Jobs.List(ctx, jobs.Filter{ProjectID: f.projectID, ChapterID: chapter.ID, Types: []string{gen.JobPagesFromScript}, Limit: 10})
		if err != nil || len(count) != 1 {
			t.Fatalf("duplicate page batches=%d %v", len(count), err)
		}
		if err := f.studio.Authorize(ctx, studio.Actor{UserID: job.UserID}, f.projectID, studio.ActGenerate); err != nil {
			t.Fatal(err)
		}
	})
}
