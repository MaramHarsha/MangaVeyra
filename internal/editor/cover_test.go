package editor_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

type coverStatus struct {
	Job struct {
		ID    string `json:"id"`
		State string `json:"state"`
		Type  string `json:"type"`
	} `json:"job"`
	Candidates []struct {
		Index   int    `json:"index"`
		AssetID string `json:"asset_id"`
		URL     string `json:"url"`
	} `json:"candidates"`
	KeptIndex *int `json:"kept_index"`
}

func TestEditorCoverAPIAuthorizesQueuesAndKeeps(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		u, c, p, ch, base := fixture(t, ta)
		member := func(role string) *testutil.Client {
			m, mc := ta.UserClient(t, auth.RoleCreator)
			if role != "" {
				if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO project_members(project_id,user_id,role) VALUES(?,?,?)`, p.ID, m.ID, role); err != nil {
					t.Fatal(err)
				}
			}
			return mc
		}
		outsider, reader, writer, editor := member(""), member(studio.RoleViewer), member(studio.RoleWriter), member(studio.RoleEditor)
		generatePath := base + "/cover/generate"

		if r := outsider.PostJSON(generatePath, map[string]any{}); r.StatusCode != http.StatusNotFound {
			t.Fatalf("non-member generate=%d %s", r.StatusCode, r.Body)
		}
		for name, client := range map[string]*testutil.Client{"reader": reader, "writer": writer} {
			if r := client.PostJSON(generatePath, map[string]any{}); r.StatusCode != http.StatusForbidden || r.ErrorCode() != "forbidden" {
				t.Fatalf("%s generate=%d %s", name, r.StatusCode, r.Body)
			}
		}
		raw, _, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: u.ID, Kind: auth.TokenAPI, Name: "Cover bearer"})
		if err != nil {
			t.Fatal(err)
		}
		if r := ta.BearerClient(raw).PostJSON(generatePath, map[string]any{}); r.StatusCode != http.StatusForbidden {
			t.Fatalf("bearer generate=%d", r.StatusCode)
		}

		r := c.PostJSON(generatePath, map[string]any{"routing_mode": "balanced"})
		if r.StatusCode != http.StatusAccepted {
			t.Fatalf("generate=%d %s", r.StatusCode, r.Body)
		}
		var queued generated
		r.JSON(t, &queued)
		again := c.PostJSON(generatePath, map[string]any{"routing_mode": "balanced"})
		var repeat generated
		again.JSON(t, &repeat)
		if again.StatusCode != http.StatusOK || !repeat.Existing || repeat.Job.ID != queued.Job.ID {
			t.Fatalf("repeat=%d %#v", again.StatusCode, repeat)
		}
		jobPath := base + "/cover/jobs/" + queued.Job.ID
		var pending coverStatus
		c.Get(jobPath).JSON(t, &pending)
		if pending.Job.Type != gen.JobCover || pending.Job.State != jobs.StateQueued || len(pending.Candidates) != 0 {
			t.Fatalf("queued status %#v", pending)
		}
		if r := c.PostJSON(jobPath+"/keep", map[string]int{"index": 0}); r.StatusCode != http.StatusConflict {
			t.Fatalf("keep before done=%d %s", r.StatusCode, r.Body)
		}
		if ran, err := ta.Deps.Executor.RunOnce(ctx); err != nil || !ran {
			t.Fatalf("run: %v %v", ran, err)
		}
		var done coverStatus
		r = c.Get(jobPath)
		r.JSON(t, &done)
		if r.Header.Get("Cache-Control") != "no-store" || done.Job.State != jobs.StateDone || len(done.Candidates) != 4 || done.Candidates[3].URL == "" || done.KeptIndex != nil {
			t.Fatalf("done status %#v", done)
		}
		// The job and its keep belong to the member who generated it.
		for name, client := range map[string]*testutil.Client{"outsider": outsider, "editor": editor} {
			if r := client.Get(jobPath); r.StatusCode != http.StatusNotFound {
				t.Fatalf("%s status=%d", name, r.StatusCode)
			}
		}
		if r := editor.PostJSON(jobPath+"/keep", map[string]int{"index": 1}); r.StatusCode != http.StatusNotFound {
			t.Fatalf("editor keep of another member's job=%d", r.StatusCode)
		}
		if r := reader.PostJSON(jobPath+"/keep", map[string]int{"index": 1}); r.StatusCode != http.StatusForbidden {
			t.Fatalf("reader keep=%d", r.StatusCode)
		}
		if r := c.PostJSON(jobPath+"/keep", map[string]int{"index": 9}); r.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("bad index=%d %s", r.StatusCode, r.Body)
		}
		r = c.PostJSON(jobPath+"/keep", map[string]int{"index": 1})
		var kept struct {
			CoverAssetID string `json:"cover_asset_id"`
			URL          string `json:"url"`
		}
		r.JSON(t, &kept)
		project, err := core.MustUse[studio.Service](ta.Deps).GetProject(ctx, p.ID)
		if err != nil || r.StatusCode != http.StatusOK || kept.CoverAssetID != done.Candidates[1].AssetID || kept.URL == "" || project.CoverAssetID != kept.CoverAssetID {
			t.Fatalf("keep=%d %s cover=%q %v", r.StatusCode, r.Body, project.CoverAssetID, err)
		}
		r = c.PostJSON(jobPath+"/keep", map[string]int{"index": 2})
		var problem struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(r.Body, &problem)
		if r.StatusCode != http.StatusConflict || problem.Error.Message == "" {
			t.Fatalf("second keep=%d %s", r.StatusCode, r.Body)
		}

		// A locked chapter takes no new cover.
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE chapters SET locked = TRUE WHERE id = ?`, ch.ID); err != nil {
			t.Fatal(err)
		}
		if r := c.PostJSON(generatePath, map[string]any{}); r.StatusCode != http.StatusLocked {
			t.Fatalf("locked generate=%d %s", r.StatusCode, r.Body)
		}
	})
}

func TestEditorCoverRequestsShareOneJobAcrossServerProcesses(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		u, c, p, _, base := fixture(t, ta)
		other := sharedApp(t, ta)
		clients := []*testutil.Client{c, other.Login(t, u.Username), ta.Login(t, u.Username), other.Login(t, u.Username)}
		statuses, out := concurrentPosts(t, clients, base+"/cover/generate", map[string]any{})
		oneJob(t, "cover", statuses, out)
		if got := countJobs(t, ta, p.ID, gen.JobCover); got != 1 {
			t.Fatalf("cover jobs=%d want 1", got)
		}
	})
}
