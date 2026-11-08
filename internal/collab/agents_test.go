package collab_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/ws"
)

// Review fixes for ADR 0013 §7–8: review is for people. An agent token never
// reaches the collaboration API or the page topic (agents act only through
// /mcp, behind the owner's switch, the per-token limits and the agent log);
// personal API tokens keep reading and commenting within their scopes. And
// nothing in the product reads a page's Yjs document, so doc:page takes no
// document writes: presence and collab.changed stay.

func TestReviewAgentTokensNeverReachCollaboration(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		tm := newTeam(t, ta)
		c := tm.comment(t, tm.letterer, tm.page1, 2, "Bubble 2 crowds Kaito’s face.", "")
		token := func(kind string, scopes []string) (*testutil.Client, *auth.TokenInfo) {
			raw, info, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: tm.letterer.user.ID, Kind: kind, Name: kind + "-bot",
				Scopes: scopes, ProjectIDs: []string{tm.project.ID}})
			if err != nil {
				t.Fatal(err)
			}
			return ta.BearerClient(raw), info
		}
		agent, info := token(auth.TokenAgent, []string{"projects:read", "projects:write"})
		post := map[string]any{"panel_id": tm.page1.Panels[1].ID, "body": "Agent: bubble overflows."}
		for name, r := range map[string]*testutil.Response{
			"review":  agent.Get(tm.reviewPath(tm.page1)),
			"comment": agent.PostJSON(tm.collabBase+"/pages/"+tm.page1.ID+"/comments", post),
			"reply":   agent.PostJSON(tm.collabBase+"/comments/"+c.ID+"/replies", map[string]string{"body": "Agent reply."}),
			"members": agent.Get(tm.collabBase + "/members"),
		} {
			if r.StatusCode != http.StatusForbidden || !strings.Contains(string(r.Body), "MCP") {
				t.Fatalf("agent %s = %d %.300s", name, r.StatusCode, r.Body)
			}
		}
		var n int
		if err := ta.Deps.DB.Get(ctx, &n, `SELECT COUNT(*) FROM comments WHERE anchor LIKE ?`, "%"+info.ID+"%"); err != nil || n != 0 {
			t.Fatalf("agent comments stored = %d %v", n, err)
		}
		// Nor does it follow the page topic (presence, review events).
		tok := &httpx.Token{ID: info.ID, Kind: httpx.TokenAgent, Scopes: []string{"projects:read", "projects:write"}, ProjectIDs: []string{tm.project.ID}}
		agentID := httpx.Identity{User: tm.letterer.user, Token: tok}
		if err := ta.Deps.Hub.Allowed(ctx, agentID, ws.DocPagePrefix+tm.page1.ID, false); err == nil {
			t.Fatal("agent token follows doc:page")
		}

		// A personal API token still reads and comments within its scopes.
		api, apiInfo := token(auth.TokenAPI, []string{"projects:read", "projects:write"})
		review(t, api.Get(tm.reviewPath(tm.page1)), http.StatusOK)
		if r := api.PostJSON(tm.collabBase+"/pages/"+tm.page1.ID+"/comments", post); r.StatusCode != http.StatusCreated {
			t.Fatalf("api token comment = %d %.300s", r.StatusCode, r.Body)
		}
		apiTok := &httpx.Token{ID: apiInfo.ID, Kind: httpx.TokenAPI, Scopes: []string{"projects:read", "projects:write"}, ProjectIDs: []string{tm.project.ID}}
		if err := ta.Deps.Hub.Allowed(ctx, httpx.Identity{User: tm.letterer.user, Token: apiTok}, ws.DocPagePrefix+tm.page1.ID, false); err != nil {
			t.Fatalf("api token follow: %v", err)
		}
	})
}

func TestReviewPageDocumentsTakeNoWrites(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		tm := newTeam(t, ta)
		topic := ws.DocPagePrefix + tm.page1.ID
		hub := ta.Deps.Hub
		for _, m := range []member{tm.owner, tm.editor, tm.artist} {
			if err := hub.Allowed(ctx, identity(m), topic, true); err == nil {
				t.Fatalf("%s may write the page document", m.user.Username)
			}
			if err := hub.Allowed(ctx, identity(m), topic, false); err != nil {
				t.Fatalf("%s follow: %v", m.user.Username, err)
			}
		}
		// An owner's 1 MiB update is refused and stores nothing.
		owner := connect(t, ta, tm.owner.client)
		owner.send(map[string]string{"op": "sub", "topic": topic})
		owner.await("subscribed")
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := owner.conn.Write(wctx, websocket.MessageBinary, ws.EncodeFrame(topic, ws.KindUpdate, "", make([]byte, 1<<20))); err != nil {
			t.Fatal(err)
		}
		if e := owner.await("error"); !strings.Contains(string(e.Data), "forbidden") {
			t.Fatalf("document update = %s", e.Data)
		}
		if err := owner.conn.Write(wctx, websocket.MessageBinary, ws.EncodeFrame(topic, ws.KindSnapshot, "", []byte{1, 2, 3})); err != nil {
			t.Fatal(err)
		}
		if e := owner.await("error"); !strings.Contains(string(e.Data), "forbidden") {
			t.Fatalf("document snapshot = %s", e.Data)
		}
		if updates, snapshots, err := hub.DocUpdates(ctx, tm.page1.ID); err != nil || updates+snapshots != 0 {
			t.Fatalf("stored document rows = %d+%d %v", updates, snapshots, err)
		}
		// Presence still works.
		owner.send(map[string]any{"op": "presence", "topic": topic, "data": map[string]any{"x": 0.5, "y": 0.5, "color": 2}})
		viewer := connect(t, ta, tm.viewer.client)
		viewer.send(map[string]string{"op": "sub", "topic": topic})
		viewer.await("presence.list")
	})
}
