package mcp_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/mcp"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
)

// Review fixes for ADR 0013 §7: approving an agent's generation never runs
// above the estimate the person saw; undoing an agent generation never wipes
// a person's edit and waits for the job; and the stdio bridge never sends the
// token to another origin.

// TestReviewApprovalNeverRunsAboveTheShownEstimate: when the estimate rose
// since the agent asked, approving keeps the request waiting with the new
// estimate; approving that runs it.
func TestReviewApprovalNeverRunsAboveTheShownEstimate(t *testing.T) {
	forEachDialect(t, func(t *testing.T, f *fixture) {
		threshold := 0.50
		raw, _ := f.token("Claude desktop · Jerry", scopesGenerate, []string{f.project.ID}, &threshold)
		s := f.connect(raw)
		pageID, version := f.firstPage()
		mcp.SetQuoteForTest(f.mod, 0.75)
		defer mcp.ResetQuoteForTest(f.mod)
		pending := mustCall(t, s, "queue_generation", map[string]any{"project_id": f.project.ID, "kind": "page", "page_id": pageID, "version": version})
		id := str(pending["request_id"])
		if str(pending["status"]) != "pending_approval" || !strings.Contains(string(f.browser.Get("/agents").Body), "~$0.75") {
			t.Fatalf("pending %v", pending)
		}

		// The route now costs more than the person was shown.
		mcp.SetQuoteForTest(f.mod, 0.90)
		jobsBefore := f.count(`SELECT COUNT(*) FROM jobs`)
		r := form(f.browser, "/agents/actions/"+id+"/approve", nil)
		if r.StatusCode != http.StatusSeeOther || f.actionStatus(id) != "pending" || f.count(`SELECT COUNT(*) FROM jobs`) != jobsBefore {
			t.Fatalf("approve above the shown estimate: %d status %s", r.StatusCode, f.actionStatus(id))
		}
		g13 := string(f.browser.Get("/agents").Body)
		for _, want := range []string{"The estimate rose to $0.90", "~$0.90"} {
			if !strings.Contains(g13, want) {
				t.Errorf("G13 lacks %q", want)
			}
		}
		// A one-in-a-thousand difference (rounding of per-token tariffs) is
		// within the documented tolerance.
		mcp.SetQuoteForTest(f.mod, 0.9009)
		if r := form(f.browser, "/agents/actions/"+id+"/approve", nil); r.StatusCode != http.StatusSeeOther || f.actionStatus(id) != "approved" {
			t.Fatalf("approve the new estimate: %d status %s", r.StatusCode, f.actionStatus(id))
		}
		var jobID string
		if err := f.ta.Deps.DB.Get(f.ctx, &jobID, `SELECT job_id FROM agent_actions WHERE id = ?`, id); err != nil || jobID == "" {
			t.Fatalf("approved job %q %v", jobID, err)
		}
		f.waitJob(jobID)
	})
}

// TestReviewUndoKeepsPeoplesEditsAndWaitsForTheJob: no undo while the job
// runs, and none once a person changed the page between the job's versions.
func TestReviewUndoKeepsPeoplesEditsAndWaitsForTheJob(t *testing.T) {
	forEachDialect(t, func(t *testing.T, f *fixture) {
		_, info := f.token("Local agent · Ollama", scopesGenerate, nil, nil)
		pageID, _ := f.firstPage()
		jobID, actionID := db.NewID(), db.NewID()
		lease := time.Now().UTC().Add(time.Hour)
		if _, err := f.ta.Deps.DB.Exec(f.ctx, `INSERT INTO jobs (id, type, scope, scope_kind, project_id, page_id, user_id, state, lease_expires_at)
			VALUES (?, 'page.generate', '{}', 'page', ?, ?, ?, 'running', ?)`, jobID, f.project.ID, pageID, f.owner.ID, lease); err != nil {
			t.Fatal(err)
		}
		if _, err := f.ta.Deps.DB.Exec(f.ctx, `INSERT INTO agent_actions (id, user_id, token_id, token_name, project_id, kind, action, target_kind, target_id, status, detail, undo, job_id, estimate, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'generate', 'page', 'page', ?, 'done', '{"chapter_number":1,"page_number":1}', ?, ?, '{}', ?, ?)`,
			actionID, f.owner.ID, info.ID, info.Name, f.project.ID, pageID, `{"page_id":"`+pageID+`"}`, jobID, db.Now(), db.Now()); err != nil {
			t.Fatal(err)
		}
		save := func(c pagestore.Change) int {
			doc, err := f.pages.GetPage(f.ctx, pageID)
			if err != nil {
				t.Fatal(err)
			}
			doc.Panels[0].Prompt += "."
			saved, err := f.pages.SavePage(f.ctx, doc, doc.Version, c)
			if err != nil {
				t.Fatal(err)
			}
			return saved.Version
		}
		agent := pagestore.Change{ActorKind: "agent", ActorID: f.owner.ID, TokenID: info.ID, JobID: jobID, Action: "generate_layer", Summary: "Agent art"}
		undoForm := `action="/agents/actions/` + actionID + `/undo"`

		// While the job runs nothing can be undone, even by posting directly.
		v := save(agent)
		if strings.Contains(string(f.browser.Get("/agents").Body), undoForm) {
			t.Fatal("G13 offers Undo while the job runs")
		}
		form(f.browser, "/agents/actions/"+actionID+"/undo", nil)
		if doc, _ := f.pages.GetPage(f.ctx, pageID); doc.Version != v || f.actionStatus(actionID) != "done" {
			t.Fatalf("undone while running: v%d→v%d %s", v, doc.Version, f.actionStatus(actionID))
		}

		// A person edits the page while the job is still writing to it.
		save(pagestore.Change{ActorKind: "user", ActorID: f.owner.ID, Action: "save", Summary: "Fix the lamp"})
		last := save(agent)
		if _, err := f.ta.Deps.DB.Exec(f.ctx, `UPDATE jobs SET state = 'done', finished_at = ? WHERE id = ?`, db.Now(), jobID); err != nil {
			t.Fatal(err)
		}
		g13 := string(f.browser.Get("/agents").Body)
		if strings.Contains(g13, undoForm) || !strings.Contains(g13, "Can’t undo: edited since") {
			t.Fatal("G13 offers Undo over a person's edit or does not say why it cannot")
		}
		form(f.browser, "/agents/actions/"+actionID+"/undo", nil)
		if doc, _ := f.pages.GetPage(f.ctx, pageID); doc.Version != last || f.actionStatus(actionID) != "done" {
			t.Fatalf("undo wiped a person's edit: v%d→v%d %s", last, doc.Version, f.actionStatus(actionID))
		}
		var j *jobs.Job
		j, err := f.ta.Deps.Jobs.Get(f.ctx, jobID)
		if err != nil || j.State != jobs.StateDone {
			t.Fatalf("job %v %v", j, err)
		}
	})
}

// TestReviewStdioNeverSendsTheTokenToAnotherOrigin: a redirect to another
// host (or scheme) is refused; the token never leaves for it. G13 suggests
// MV_AGENT_TOKEN rather than --token on the command line.
func TestReviewStdioNeverSendsTheTokenToAnotherOrigin(t *testing.T) {
	var leaked, reached atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
		if strings.Contains(r.Header.Get("Authorization"), "mva_") {
			leaked.Store(true)
		}
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/mcp", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"desktop","version":"1"}}}` + "\n")
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = mcp.RunStdio(ctx, mcp.StdioOptions{URL: origin.URL, Token: "mva_secret_token_value"}, in, &out)
	if leaked.Load() || reached.Load() {
		t.Fatalf("the bridge followed a redirect to another origin (token sent: %v)", leaked.Load())
	}
	if !strings.Contains(out.String(), `"id":1`) || !strings.Contains(out.String(), "error") {
		t.Fatalf("the desktop app got no error for the refused redirect: %s", out.String())
	}

	f := newFixture(t, "sqlite")
	good := form(f.browser, "/agents/tokens", url.Values{"name": {"Desktop"}, "projects": {"all"}, "expires": {"never"}})
	secret := regexp.MustCompile(`mva_[A-Za-z0-9_-]{20,}`).FindString(string(good.Body))
	if good.StatusCode != http.StatusCreated || secret == "" || !strings.Contains(string(good.Body), "MV_AGENT_TOKEN="+secret+" mangaveyra mcp-stdio --url ") ||
		strings.Contains(string(good.Body), "--token "+secret) {
		t.Fatalf("stdio instructions: %d %.2000s", good.StatusCode, good.Body)
	}
}
