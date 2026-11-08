package mcp_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/mcp"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
)

func (f *fixture) waitJob(id string) *jobs.Job {
	f.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		j, err := f.ta.Deps.Jobs.Get(f.ctx, id)
		if err != nil {
			f.t.Fatal(err)
		}
		if j.Finished() {
			return j
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("job %s still %s", id, j.State)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestGenerationAboveThresholdWaitsForAPerson: over the threshold nothing
// runs; approval runs exactly the request as the approving person; decline
// runs nothing; equal to the threshold runs at once.
func TestGenerationAboveThresholdWaitsForAPerson(t *testing.T) {
	forEachDialect(t, func(t *testing.T, f *fixture) {
		threshold := 0.50
		raw, info := f.token("Claude desktop · Jerry", scopesGenerate, []string{f.project.ID}, &threshold)
		s := f.connect(raw)
		pageID, version := f.firstPage()
		page := map[string]any{"project_id": f.project.ID, "kind": "page", "page_id": pageID, "version": version}

		// The real estimate comes from the generation service's own planning:
		// the built-in placeholder costs nothing, so it is below any threshold.
		estimate := mustCall(t, s, "queue_generation", map[string]any{"project_id": f.project.ID, "kind": "page", "page_id": pageID, "version": version, "estimate_only": true})
		if str(estimate["status"]) != "estimate" || estimate["needs_approval"] != false {
			t.Fatalf("placeholder estimate %v", estimate)
		}

		mcp.SetQuoteForTest(f.mod, 0.75)
		defer mcp.ResetQuoteForTest(f.mod)
		jobsBefore := f.count(`SELECT COUNT(*) FROM jobs`)
		only := mustCall(t, s, "queue_generation", map[string]any{"project_id": f.project.ID, "kind": "page", "page_id": pageID, "version": version, "estimate_only": true})
		if only["needs_approval"] != true || f.count(`SELECT COUNT(*) FROM agent_actions WHERE status = 'pending'`) != 0 {
			t.Fatalf("estimate only %v", only)
		}
		pending := mustCall(t, s, "queue_generation", page)
		if str(pending["status"]) != "pending_approval" || pending["job"] != nil {
			t.Fatalf("above threshold %v", pending)
		}
		if f.count(`SELECT COUNT(*) FROM jobs`) != jobsBefore {
			t.Fatal("generation above the threshold queued a job")
		}
		g13 := string(f.browser.Get("/agents").Body)
		for _, want := range []string{"Asked to generate page 1 art in ch. 1 · ~$0.75", "Blocked · waiting for you", "Approve over $0.50"} {
			if !strings.Contains(g13, want) {
				t.Errorf("G13 lacks %q", want)
			}
		}
		requestID := str(pending["request_id"])
		r := form(f.browser, "/agents/actions/"+requestID+"/approve", nil)
		if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/agents" {
			t.Fatalf("approve: %d %s", r.StatusCode, r.Header.Get("Location"))
		}
		var row struct {
			Status    string `db:"status"`
			JobID     string `db:"job_id"`
			DecidedBy string `db:"decided_by"`
		}
		if err := f.ta.Deps.DB.Get(f.ctx, &row, `SELECT status, job_id, decided_by FROM agent_actions WHERE id = ?`, requestID); err != nil {
			t.Fatal(err)
		}
		if row.Status != "approved" || row.JobID == "" || row.DecidedBy != f.owner.ID {
			t.Fatalf("approved row %+v", row)
		}
		j, err := f.ta.Deps.Jobs.Get(f.ctx, row.JobID)
		if err != nil || j.UserID != f.owner.ID || j.Scope.PageID != pageID || j.Type != "page.generate" {
			t.Fatalf("approved job %+v %v", j, err)
		}
		if f.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'agent.request.approve' AND actor_kind = 'user' AND actor_id = ?`, f.owner.ID) != 1 {
			t.Fatal("approval not audited")
		}
		// Deciding twice is refused.
		if r := form(f.browser, "/agents/actions/"+requestID+"/decline", nil); r.StatusCode != http.StatusSeeOther || f.actionStatus(requestID) != "approved" {
			t.Fatal("a decided request changed")
		}
		f.waitJob(row.JobID)

		// Decline: nothing runs.
		_, version = f.firstPage()
		second := mustCall(t, s, "queue_generation", map[string]any{"project_id": f.project.ID, "kind": "page", "page_id": pageID, "version": version})
		jobsBefore = f.count(`SELECT COUNT(*) FROM jobs`)
		if r := form(f.browser, "/agents/actions/"+str(second["request_id"])+"/decline", nil); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("decline %d", r.StatusCode)
		}
		if f.actionStatus(str(second["request_id"])) != "declined" || f.count(`SELECT COUNT(*) FROM jobs`) != jobsBefore {
			t.Fatal("declined generation ran")
		}
		if r := form(f.browser, "/agents/actions/"+str(second["request_id"])+"/approve", nil); r.StatusCode != http.StatusSeeOther || f.count(`SELECT COUNT(*) FROM jobs`) != jobsBefore {
			t.Fatal("a declined request could still be approved")
		}

		// A request whose page changed before the person approved does not run.
		stale := mustCall(t, s, "queue_generation", map[string]any{"project_id": f.project.ID, "kind": "page", "page_id": pageID, "version": version})
		doc, err := f.pages.GetPage(f.ctx, pageID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.pages.SavePage(f.ctx, doc, doc.Version, pagestore.Change{ActorKind: "user", ActorID: f.owner.ID, Action: "save", Summary: "save"}); err != nil {
			t.Fatal(err)
		}
		jobsBefore = f.count(`SELECT COUNT(*) FROM jobs`)
		if r := form(f.browser, "/agents/actions/"+str(stale["request_id"])+"/approve", nil); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("stale approve %d", r.StatusCode)
		}
		if f.actionStatus(str(stale["request_id"])) != "pending" || f.count(`SELECT COUNT(*) FROM jobs`) != jobsBefore {
			t.Fatal("a stale request ran or was closed")
		}
		// Estimating a region edit leaves no mask behind.
		masks := f.count(`SELECT COUNT(*) FROM assets WHERE kind = 'region_mask'`)
		layer := ""
		for _, pn := range doc.Panels {
			for _, l := range pn.Layers {
				if l.Kind == "background" && l.Asset != nil && layer == "" {
					layer = l.ID
				}
			}
		}
		_, version = f.firstPage()
		if out := mustCall(t, s, "queue_generation", map[string]any{"project_id": f.project.ID, "kind": "region", "page_id": pageID, "version": version,
			"layer_id": layer, "prompt": "Gulls over the harbour", "region": map[string]any{"x": 0, "y": 0, "width": 0.5, "height": 0.5}, "estimate_only": true}); out["needs_approval"] != true {
			t.Fatalf("region estimate %v", out)
		}
		if f.count(`SELECT COUNT(*) FROM assets WHERE kind = 'region_mask'`) != masks {
			t.Fatal("an estimate left a mask asset")
		}

		// Exactly at the threshold runs at once, as the agent.
		mcp.SetQuoteForTest(f.mod, 0.50)
		queued := mustCall(t, s, "queue_generation", map[string]any{"project_id": f.project.ID, "kind": "page", "page_id": pageID, "version": version})
		if str(queued["status"]) != "queued" || str(queued["action_id"]) == "" {
			t.Fatalf("at threshold %v", queued)
		}
		jobID := str(queued["job"].(map[string]any)["id"])
		done := f.waitJob(jobID)
		if done.State != jobs.StateDone {
			t.Fatalf("agent job %s %+v", done.State, done.Error)
		}
		got := mustCall(t, s, "get_job", map[string]any{"job_id": jobID})
		if str(got["state"]) != "done" || got["input"] != nil || got["output"] != nil {
			t.Fatalf("get_job %v", got)
		}
		// The page change is labelled with the token in version history.
		if n := f.count(`SELECT COUNT(*) FROM versions WHERE page_id = ? AND job_id = ? AND actor_kind = 'agent' AND token_id = ? AND actor_id = ?`,
			pageID, jobID, info.ID, f.owner.ID); n == 0 {
			t.Fatal("agent page version not labelled")
		}
		if n := f.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'agent.generate' AND actor_kind = 'agent' AND token_id = ?`, info.ID); n != 1 {
			t.Fatalf("generation audit rows %d", n)
		}

		// A token without a threshold never waits.
		free, _ := f.token("no threshold", scopesGenerate, nil, nil)
		fs := f.connect(free)
		mcp.SetQuoteForTest(f.mod, 9.99)
		_, version = f.firstPage()
		if out := mustCall(t, fs, "queue_generation", map[string]any{"project_id": f.project.ID, "kind": "page", "page_id": pageID, "version": version}); str(out["status"]) != "queued" {
			t.Fatalf("no threshold %v", out)
		}
	})
}
