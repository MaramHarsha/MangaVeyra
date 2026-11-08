package publish_test

import (
	"bytes"
	"context"
	"image/png"
	"net/http"
	"net/url"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func seriesCover(t *testing.T, ta *testutil.TestApp, projectID string) string {
	t.Helper()
	var id *string
	if err := ta.Deps.DB.Get(context.Background(), &id, `SELECT cover_asset_id FROM series WHERE project_id = ?`, projectID); err != nil {
		t.Fatal(err)
	}
	if id == nil {
		return ""
	}
	return *id
}

func TestCoverChooseGenerateAndKeep(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, pf *publishtest.Fixture) {
		ctx := context.Background()
		f := newChapter(t, ta, false)
		store := assets.From(ta.Deps)

		// The page picker shows rendered pages of the chapter.
		if img := f.client.Get(f.base + "/publish/pages/" + f.pages[1].ID + "/image?lang=ja"); img.StatusCode != http.StatusOK || img.Header.Get("Content-Type") != "image/png" {
			t.Fatalf("page image = %d", img.StatusCode)
		}
		if got := f.client.Get(f.base + "/publish/pages/01hzzzzzzzzzzzzzzzzzzzzzzz/image").StatusCode; got != http.StatusNotFound {
			t.Fatalf("unknown page image = %d", got)
		}

		// Choose from pages: a 3:4 crop of the rendered page becomes the series cover (creating the series).
		resp := postForm(f.client, f.base+"/publish/cover/page", url.Values{"page": {f.pages[1].ID}, "lang": {"en"}})
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("choose = %d %s", resp.StatusCode, resp.Body)
		}
		chosen := seriesCover(t, ta, f.project.ID)
		data, a, err := store.Read(ctx, chosen, 0)
		if err != nil || a.Kind != "series_cover" {
			t.Fatalf("chosen cover %+v %v", a, err)
		}
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil || img.Bounds().Dx() != pageW || img.Bounds().Dy() != pageW*4/3 {
			t.Fatalf("cover size %v %v", img.Bounds(), err)
		}
		if page := f.client.Get(f.base + "/publish"); !bytes.Contains(page.Body, []byte("Cover of Tidebreak Courier")) {
			t.Fatal("E1 does not show the chosen cover")
		}
		if got := postForm(f.client, f.base+"/publish/cover/page", url.Values{"page": {"01hzzzzzzzzzzzzzzzzzzzzzzz"}}).StatusCode; got != http.StatusNotFound {
			t.Fatalf("choose unknown page = %d", got)
		}

		// Generate cover runs the existing cover.generate job; keeping a candidate sets the project and series cover.
		resp = postForm(f.client, f.base+"/publish/cover/generate", url.Values{"lang": {"en"}})
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("generate = %d %s", resp.StatusCode, resp.Body)
		}
		loc, _ := url.Parse(resp.Header.Get("Location"))
		jobID := loc.Query().Get("cover_job")
		j, err := ta.Deps.Jobs.Get(ctx, jobID)
		if err != nil || j.Type != gen.JobCover || j.Scope.ChapterID != f.chapter.ID {
			t.Fatalf("cover job %+v %v", j, err)
		}
		if page := f.client.Get(resp.Header.Get("Location")); !bytes.Contains(page.Body, []byte("Generating four cover candidates")) {
			t.Fatal("E1 does not show the running cover job")
		}
		if done := runJob(t, ta, jobID); done.State != jobs.StateDone {
			t.Fatalf("cover job: %s %+v", done.State, done.Error)
		}
		page := f.client.Get(resp.Header.Get("Location"))
		if !bytes.Contains(page.Body, []byte("Keep candidate 4")) {
			t.Fatalf("E1 does not offer the candidates")
		}
		res, err := core.MustUse[gen.CoverService](ta.Deps).CoverCandidates(ctx, f.actor, jobID)
		if err != nil || len(res.Candidates) != 4 {
			t.Fatalf("candidates %+v %v", res, err)
		}
		resp = postForm(f.client, f.base+"/publish/cover/keep", url.Values{"cover_job": {jobID}, "index": {"2"}, "lang": {"en"}})
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("keep = %d %s", resp.StatusCode, resp.Body)
		}
		project, err := core.MustUse[studio.Service](ta.Deps).GetProject(ctx, f.project.ID)
		if err != nil || project.CoverAssetID != res.Candidates[2].AssetID || seriesCover(t, ta, f.project.ID) != res.Candidates[2].AssetID {
			t.Fatalf("kept cover: project %q series %q want %q", project.CoverAssetID, seriesCover(t, ta, f.project.ID), res.Candidates[2].AssetID)
		}
		if got := postForm(f.client, f.base+"/publish/cover/keep", url.Values{"cover_job": {jobID}, "index": {"1"}}).StatusCode; got != http.StatusConflict {
			t.Fatalf("second keep = %d", got)
		}

		// A locked chapter cannot generate a cover (gen refuses), but stays publishable.
		exec(t, ta, `UPDATE chapters SET locked = TRUE WHERE id = ?`, f.chapter.ID)
		if got := postForm(f.client, f.base+"/publish/cover/generate", url.Values{}).StatusCode; got != http.StatusLocked {
			t.Fatalf("locked generate = %d", got)
		}
		if j := f.publish(t, f.publishForm("en")); j.State != jobs.StateDone {
			t.Fatalf("locked chapter publish: %s %+v", j.State, j.Error)
		}
	})
}
