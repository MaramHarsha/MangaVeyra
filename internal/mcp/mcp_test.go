package mcp_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
)

func toolNames(t *testing.T, s *mcpsdk.ClientSession) []string {
	t.Helper()
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

// TestSDKClientDrivesTheServerEndToEnd connects the official SDK client over
// streamable HTTP and runs read → edit → label → G13 on both dialects.
func TestSDKClientDrivesTheServerEndToEnd(t *testing.T) {
	forEachDialect(t, func(t *testing.T, f *fixture) {
		raw, info := f.token("Claude desktop · Jerry", scopesGenerate, []string{f.project.ID}, nil)
		s := f.connect(raw)
		if init := s.InitializeResult(); init == nil || init.ServerInfo.Name != "mangaveyra" || !strings.Contains(init.Instructions, "request_human_action") {
			t.Fatalf("initialize: %+v", init)
		}
		want := []string{"create_character", "edit_character", "edit_script", "get_job", "get_pages", "get_project", "get_script",
			"list_characters", "list_projects", "list_recent_actions", "queue_generation", "request_human_action"}
		if got := toolNames(t, s); !slices.Equal(got, want) {
			t.Fatalf("tools %v, want %v", got, want)
		}

		projects := mustCall(t, s, "list_projects", nil)["projects"].([]any)
		if len(projects) != 1 || str(projects[0].(map[string]any)["id"]) != f.project.ID {
			t.Fatalf("projects %v", projects)
		}
		project := mustCall(t, s, "get_project", map[string]any{"project_id": f.project.ID})
		if chapters := project["chapters"].([]any); len(chapters) != 2 {
			t.Fatalf("chapters %v", chapters)
		}
		script := mustCall(t, s, "get_script", map[string]any{"project_id": f.project.ID, "chapter_id": f.chapter.ID})
		panel := script["scenes"].([]any)[0].(map[string]any)["panels"].([]any)[0].(map[string]any)
		if str(panel["label"]) != "1.1" {
			t.Fatalf("panel %v", panel)
		}
		edited := mustCall(t, s, "edit_script", map[string]any{"project_id": f.project.ID, "chapter_id": f.chapter.ID,
			"revision": script["revision"], "panels": []any{map[string]any{"panel_id": panel["id"],
				"dialogue": []any{map[string]any{"kind": "caption", "text": "At Tidebreak, every address drifts with the tide."}}}}})
		if labels := edited["changed"].([]any); len(labels) != 1 || labels[0] != "1.1" || str(edited["revision"]) == "" {
			t.Fatalf("edit %v", edited)
		}
		doc := f.script()
		if got := doc.Scenes[0].Panels[0].Dialogue; len(got) != 1 || got[0].Text != "At Tidebreak, every address drifts with the tide." {
			t.Fatalf("stored dialogue %+v", got)
		}
		c, _ := f.studio.GetChapter(f.ctx, f.chapter.ID)
		if c.AILabel["script"] != "ai_assisted" {
			t.Fatalf("an agent edit must label the script AI-assisted, got %q", c.AILabel["script"])
		}
		// Labelled in the audit log with the token.
		if n := f.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'agent.script.edit' AND actor_kind = 'agent' AND token_id = ? AND actor_id = ? AND project_id = ?`,
			info.ID, f.owner.ID, f.project.ID); n != 1 {
			t.Fatalf("audit rows %d", n)
		}
		recent := mustCall(t, s, "list_recent_actions", nil)["actions"].([]any)
		if len(recent) != 1 || str(recent[0].(map[string]any)["summary"]) != "Rewrote caption 1.1" {
			t.Fatalf("recent %v", recent)
		}
		// The next edit must use the new revision; the old one is refused.
		stale := call(t, s, "edit_script", map[string]any{"project_id": f.project.ID, "chapter_id": f.chapter.ID,
			"revision": script["revision"], "panels": []any{map[string]any{"panel_id": panel["id"], "notes": "late"}}})
		if !stale.Err || !strings.Contains(stale.Text, "changed since you read it") {
			t.Fatalf("stale revision accepted: %+v", stale)
		}

		page := f.browser.Get("/agents")
		if page.StatusCode != http.StatusOK {
			t.Fatalf("G13 %d", page.StatusCode)
		}
		body := string(page.Body)
		for _, want := range []string{`data-screen="G13"`, "Agents &amp; MCP", f.ta.URL + "/mcp", "Claude desktop · Jerry",
			"Tidebreak Courier · edit + generate", "No approval needed", "Rewrote caption 1.1", "Undo", "Always needs a person",
			"MCP client (v2)", "Connect a server"} {
			if !strings.Contains(body, want) {
				t.Errorf("G13 lacks %q", want)
			}
		}
	})
}

// TestMCPRefusesSessionsOtherTokensAndInvalidAgents checks the HTTP gate.
func TestMCPRefusesSessionsOtherTokensAndInvalidAgents(t *testing.T) {
	forEachDialect(t, func(t *testing.T, f *fixture) {
		initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`
		send := func(header http.Header) int {
			t.Helper()
			req, err := http.NewRequest(http.MethodPost, f.ta.URL+"/mcp", strings.NewReader(initialize))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			for k, v := range header {
				req.Header[k] = v
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			return resp.StatusCode
		}
		bearer := func(raw string) http.Header { return http.Header{"Authorization": {"Bearer " + raw}} }

		if got := send(nil); got != http.StatusUnauthorized {
			t.Fatalf("anonymous %d", got)
		}
		// A signed-in browser session is not an agent.
		cookie := http.Header{}
		for _, ck := range f.browser.Jar.Cookies(mustURL(t, f.ta.URL)) {
			cookie.Add("Cookie", ck.Name+"="+ck.Value)
		}
		if got := send(cookie); got != http.StatusUnauthorized {
			t.Fatalf("session %d", got)
		}
		api, _, err := f.ta.Deps.Auth.CreateToken(f.ctx, auth.TokenInput{UserID: f.owner.ID, Kind: auth.TokenAPI, Name: "api", Scopes: scopesGenerate})
		if err != nil {
			t.Fatal(err)
		}
		if got := send(bearer(api)); got != http.StatusForbidden {
			t.Fatalf("api token %d", got)
		}
		worker, _, err := f.ta.Deps.Auth.CreateToken(f.ctx, auth.TokenInput{UserID: f.ta.Admin.ID, Kind: auth.TokenWorker, Name: "gpu"})
		if err != nil {
			t.Fatal(err)
		}
		if got := send(bearer(worker)); got != http.StatusForbidden {
			t.Fatalf("worker token %d", got)
		}
		star, _ := f.token("star", []string{"*"}, nil, nil)
		if got := send(bearer(star)); got != http.StatusForbidden {
			t.Fatalf("broad token %d", got)
		}
		raw, info := f.token("ok", scopesRead, nil, nil)
		if got := send(bearer(raw)); got != http.StatusOK {
			t.Fatalf("agent token %d", got)
		}
		if err := f.ta.Deps.Auth.RevokeToken(f.ctx, f.owner.ID, info.ID, false); err != nil {
			t.Fatal(err)
		}
		if got := send(bearer(raw)); got != http.StatusUnauthorized {
			t.Fatalf("revoked token %d", got)
		}
		expired, expiredInfo := f.token("soon gone", scopesRead, nil, nil)
		if _, err := f.ta.Deps.DB.Exec(f.ctx, `UPDATE api_tokens SET expires_at = ? WHERE id = ?`, f.ta.Clock.Now().Add(-1), expiredInfo.ID); err != nil {
			t.Fatal(err)
		}
		if got := send(bearer(expired)); got != http.StatusUnauthorized {
			t.Fatalf("expired token %d", got)
		}
		// The owner's switch pauses every agent.
		live, _ := f.token("paused", scopesRead, nil, nil)
		if r := form(f.browser, "/agents/allow", nil); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("switch off %d %s", r.StatusCode, r.Body)
		}
		if got := send(bearer(live)); got != http.StatusForbidden {
			t.Fatalf("switched off %d", got)
		}
		if r := form(f.browser, "/agents/allow", map[string][]string{"allowed": {"on"}}); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("switch on %d", r.StatusCode)
		}
		if got := send(bearer(live)); got != http.StatusOK {
			t.Fatalf("switched on %d", got)
		}
	})
}

// TestEachAgentTokenIsRateLimited: one token cannot flood /mcp; another
// token of the same person is unaffected.
func TestEachAgentTokenIsRateLimited(t *testing.T) {
	f := newFixture(t, "sqlite")
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`
	send := func(raw string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, f.ta.URL+"/mcp", strings.NewReader(initialize))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+raw)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	busy, _ := f.token("busy", scopesRead, nil, nil)
	limited := false
	for range 80 {
		if send(busy) == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("80 rapid requests from one token were never limited")
	}
	calm, _ := f.token("calm", scopesRead, nil, nil)
	if got := send(calm); got != http.StatusOK {
		t.Fatalf("another token limited too: %d", got)
	}
}
