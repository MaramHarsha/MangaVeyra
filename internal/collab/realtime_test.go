package collab_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/collab"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/ws"
)

type socket struct {
	t    *testing.T
	conn *websocket.Conn
}

func connect(t *testing.T, ta *testutil.TestApp, c *testutil.Client) *socket {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u, _ := url.Parse(ta.URL)
	h := http.Header{}
	for _, ck := range c.Jar.Cookies(u) {
		h.Add("Cookie", ck.Name+"="+ck.Value)
	}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ta.URL, "http")+"/ws", &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close(websocket.StatusNormalClosure, "") })
	return &socket{t: t, conn: conn}
}

func (s *socket) send(v any) {
	s.t.Helper()
	b, _ := json.Marshal(v)
	if err := s.conn.Write(context.Background(), websocket.MessageText, b); err != nil {
		s.t.Fatal(err)
	}
}

type message struct {
	Topic string          `json:"topic"`
	Type  string          `json:"type"`
	Data  json.RawMessage `json:"data"`
}

// await reads text messages until one of type typ arrives.
func (s *socket) await(typ string) message { return s.awaitWith(typ, "") }

// awaitWith reads text messages until one of type typ whose data contains want arrives.
func (s *socket) awaitWith(typ, want string) message {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		kind, data, err := s.conn.Read(ctx)
		if err != nil {
			s.t.Fatalf("waiting for %s: %v", typ, err)
		}
		if kind != websocket.MessageText {
			continue
		}
		var m message
		if json.Unmarshal(data, &m) == nil && m.Type == typ && strings.Contains(string(m.Data), want) {
			return m
		}
	}
}

func TestRealtimePresenceAndReviewEvents(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		tm := newTeam(t, ta)
		topic := ws.DocPagePrefix + tm.page1.ID
		jun := connect(t, ta, tm.letterer.client)
		jun.send(map[string]string{"op": "sub", "topic": topic})
		jun.await("subscribed")

		jerry := connect(t, ta, tm.owner.client)
		jerry.send(map[string]string{"op": "sub", "topic": topic})
		jerry.await("subscribed")
		// The editor sends only a cursor position and a colour; the hub adds who it is.
		jerry.send(map[string]any{"op": "presence", "topic": topic, "data": map[string]any{"x": 0.42, "y": 0.31, "color": 1}})
		join := jun.await("presence.join")
		var who ws.PresenceUser
		if err := json.Unmarshal(join.Data, &who); err != nil || who.UserID != tm.owner.user.ID || who.Username != tm.owner.user.Username || string(who.State) != `{"color":1,"x":0.42,"y":0.31}` {
			t.Fatalf("presence join = %s (%v)", join.Data, err)
		}
		// The server caps presence state (no content in presence).
		jerry.send(map[string]any{"op": "presence", "topic": topic, "data": map[string]string{"x": strings.Repeat("9", 5000)}})
		if e := jerry.await("error"); !strings.Contains(string(e.Data), "too_large") {
			t.Fatalf("oversized presence = %s", e.Data)
		}

		// Review changes reach every open editor of the page as ids only.
		c := tm.comment(t, tm.letterer, tm.page1, 2, "Bubble 2 crowds Kaito’s face.", "")
		changed := jerry.await("collab.changed")
		if changed.Topic != topic || !strings.Contains(string(changed.Data), c.ID) || strings.Contains(string(changed.Data), "crowds") {
			t.Fatalf("comment event = %#v", changed)
		}
		review(t, tm.setStatus(tm.owner, c.ID, collab.StatusNeedsChanges), http.StatusOK)
		if ev := jun.awaitWith("collab.changed", `"change":"status"`); !strings.Contains(string(ev.Data), c.ID) {
			t.Fatalf("status event = %s", ev.Data)
		}

		// Closing the tab leaves the page.
		jerry.conn.Close(websocket.StatusNormalClosure, "")
		leave := jun.await("presence.leave")
		if !strings.Contains(string(leave.Data), tm.owner.user.ID) {
			t.Fatalf("leave = %s", leave.Data)
		}

		// People outside the project cannot follow the page.
		_, outsider := ta.UserClient(t, auth.RoleCreator)
		stranger := connect(t, ta, outsider)
		stranger.send(map[string]string{"op": "sub", "topic": topic})
		if e := stranger.await("error"); !strings.Contains(string(e.Data), "forbidden") {
			t.Fatalf("outsider subscribe = %s", e.Data)
		}
		// Nobody writes the page's Yjs document (nothing reads it; see
		// TestReviewPageDocumentsTakeNoWrites); readers still follow the page.
		hub := ta.Deps.Hub
		ctx := context.Background()
		for _, m := range []member{tm.artist, tm.viewer} {
			if err := hub.Allowed(ctx, identity(m), topic, true); err == nil {
				t.Fatalf("%s may write the document", m.user.Username)
			}
		}
		if err := hub.Allowed(ctx, identity(tm.viewer), topic, false); err != nil {
			t.Fatalf("viewer follow: %v", err)
		}
	})
}

func identity(m member) httpx.Identity { return httpx.Identity{User: m.user, SessionID: "test"} }
