package ws_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
	"github.com/MaramHarsha/MangaVeyra/internal/ws"
)

type client struct {
	t    *testing.T
	conn *websocket.Conn
}

func dial(t *testing.T, ta *testutil.TestApp, c *testutil.Client) (*client, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h := http.Header{}
	if c != nil {
		u := mustParse(ta.URL)
		for _, ck := range c.Jar.Cookies(u) {
			h.Add("Cookie", ck.Name+"="+ck.Value)
		}
	}
	conn, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ta.URL, "http")+"/ws", &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		return nil, resp, err
	}
	conn.SetReadLimit(16 << 20)
	t.Cleanup(func() { conn.Close(websocket.StatusNormalClosure, "") })
	return &client{t: t, conn: conn}, resp, nil
}

func (c *client) send(v any) {
	c.t.Helper()
	b, _ := json.Marshal(v)
	if err := c.conn.Write(context.Background(), websocket.MessageText, b); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) sendBinary(b []byte) {
	c.t.Helper()
	if err := c.conn.Write(context.Background(), websocket.MessageBinary, b); err != nil {
		c.t.Fatal(err)
	}
}

type frame struct {
	text bool
	msg  ws.Message
	raw  json.RawMessage
	// binary
	topic   string
	kind    byte
	id      string
	payload []byte
}

func (c *client) read() frame {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typ, data, err := c.conn.Read(ctx)
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	if typ == websocket.MessageText {
		var m struct {
			Topic string          `json:"topic"`
			Type  string          `json:"type"`
			Data  json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(data, &m); err != nil {
			c.t.Fatal(err)
		}
		return frame{text: true, msg: ws.Message{Topic: m.Topic, Type: m.Type}, raw: m.Data}
	}
	topic, kind, id, payload, err := ws.DecodeFrame(data)
	if err != nil {
		c.t.Fatal(err)
	}
	return frame{topic: topic, kind: kind, id: id, payload: payload}
}

// expect reads until a text message of type typ arrives (skipping others).
func (c *client) expect(typ string) frame {
	c.t.Helper()
	for i := 0; i < 20; i++ {
		f := c.read()
		if f.text && f.msg.Type == typ {
			return f
		}
	}
	c.t.Fatalf("no %q message", typ)
	return frame{}
}

func TestHubSubscribeAuthorizeAndPush(t *testing.T) {
	ta := testutil.NewTestApp(t)
	// anonymous connections are refused
	if _, resp, err := dial(t, ta, nil); err == nil || resp == nil || resp.StatusCode != 401 {
		t.Fatalf("anonymous dial: %v %v", resp, err)
	}
	u, c := ta.UserClient(t, auth.RoleCreator)
	other := ta.CreateUser(t, "other", auth.RoleCreator)
	wc, _, err := dial(t, ta, c)
	if err != nil {
		t.Fatal(err)
	}
	hub := ta.Deps.Hub

	wc.send(map[string]string{"op": "sub", "topic": "user:" + u.ID})
	if f := wc.read(); f.msg.Type != "subscribed" || f.msg.Topic != "user:"+u.ID {
		t.Fatalf("subscribe: %+v", f)
	}
	for _, topic := range []string{"user:" + other.ID, "admin:queue", "nobody:owns-this", "project:x"} {
		wc.send(map[string]string{"op": "sub", "topic": topic})
		if f := wc.read(); f.msg.Type != "error" {
			t.Fatalf("%s must be forbidden: %+v", topic, f)
		}
	}
	// server push
	hub.PublishUser(u.ID, "notification.new", map[string]string{"title": "Old Wren replied"})
	if f := wc.read(); f.msg.Type != "notification.new" || !strings.Contains(string(f.raw), "Old Wren") {
		t.Fatalf("push: %+v", f)
	}
	hub.PublishUser(other.ID, "notification.new", map[string]string{"title": "not for you"})
	hub.PublishUser(u.ID, "marker", nil)
	if f := wc.read(); f.msg.Type != "marker" {
		t.Fatalf("received another user's message: %+v %s", f, f.raw)
	}

	// custom authorizer + presence
	hub.Authorize("project:", func(ctx context.Context, id httpx.Identity, topic string, write bool) error {
		if topic == "project:tidebreak" {
			return nil
		}
		return ws.ErrForbidden
	})
	wc.send(map[string]string{"op": "sub", "topic": "project:tidebreak"})
	wc.expect("subscribed")
	_, c2 := ta.UserClient(t, auth.RoleCreator)
	wc2, _, err := dial(t, ta, c2)
	if err != nil {
		t.Fatal(err)
	}
	wc2.send(map[string]string{"op": "sub", "topic": "project:tidebreak"})
	wc2.expect("subscribed")
	wc2.send(map[string]any{"op": "presence", "topic": "project:tidebreak", "data": map[string]any{"page": 7}})
	f := wc.expect("presence.join")
	if !strings.Contains(string(f.raw), `"page":7`) {
		t.Fatalf("presence payload: %s", f.raw)
	}
	if n := len(hub.Presence("project:tidebreak")); n != 1 {
		t.Fatalf("presence list %d", n)
	}
	hub.Publish("project:tidebreak", "job.progress", map[string]float64{"progress": 0.5})
	wc.expect("job.progress")
	wc2.expect("job.progress")
	// unsubscribe → leave
	wc2.send(map[string]string{"op": "unsub", "topic": "project:tidebreak"})
	wc.expect("presence.leave")
	if hub.Subscribers("project:tidebreak") != 1 {
		t.Fatalf("subscribers %d", hub.Subscribers("project:tidebreak"))
	}
	wc.send(map[string]string{"op": "ping"})
	wc.expect("pong")
}

func TestJobTopicAuthorization(t *testing.T) {
	ta := testutil.NewTestApp(t)
	u, c := ta.UserClient(t, auth.RoleCreator)
	j, err := ta.Deps.Jobs.Enqueue(context.Background(), jobsParams(u.ID))
	if err != nil {
		t.Fatal(err)
	}
	wc, _, _ := dial(t, ta, c)
	wc.send(map[string]string{"op": "sub", "topic": "job:" + j.ID})
	wc.expect("subscribed")
	ta.Deps.Jobs.Cancel(context.Background(), j.ID)
	if f := wc.expect("job.canceled"); !strings.Contains(string(f.raw), j.ID) {
		t.Fatalf("job event: %s", f.raw)
	}
	_, c2 := ta.UserClient(t, auth.RoleCreator)
	wc2, _, _ := dial(t, ta, c2)
	wc2.send(map[string]string{"op": "sub", "topic": "job:" + j.ID})
	if f := wc2.read(); f.msg.Type != "error" {
		t.Fatalf("other user subscribed to a job: %+v", f)
	}
}

func TestYjsRelayPersistenceAndCompaction(t *testing.T) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect))
			ctx := context.Background()
			page := seedPage(t, ta)
			topic := ws.DocPagePrefix + page
			hub := ta.Deps.Hub
			editor, ec := ta.UserClient(t, auth.RoleCreator)
			_, vc := ta.UserClient(t, auth.RoleReader)
			hub.Authorize(ws.DocPagePrefix, func(ctx context.Context, id httpx.Identity, tp string, write bool) error {
				if tp != topic {
					return ws.ErrForbidden
				}
				if write && id.User.ID != editor.ID {
					return ws.ErrForbidden // viewers may watch, not edit
				}
				return nil
			})
			a, _, _ := dial(t, ta, ec)
			b, _, _ := dial(t, ta, vc)
			for _, c := range []*client{a, b} {
				c.send(map[string]string{"op": "sub", "topic": topic})
				c.expect("subscribed")
				if f := c.read(); f.kind != ws.KindReplayDone || f.id != "" {
					t.Fatalf("empty replay: %+v", f)
				}
			}
			// A sends two updates: B receives them with ids; A gets acks.
			var ids []string
			for _, p := range [][]byte{{1, 2, 3}, {4, 5}} {
				a.sendBinary(ws.EncodeFrame(topic, ws.KindUpdate, "", p))
				ack := a.read()
				if ack.kind != ws.KindAck || !db.ValidID(ack.id) {
					t.Fatalf("ack: %+v", ack)
				}
				got := b.read()
				if got.kind != ws.KindUpdate || got.id != ack.id || string(got.payload) != string(p) {
					t.Fatalf("relay: %+v", got)
				}
				ids = append(ids, ack.id)
			}
			// awareness is relayed, not stored
			a.sendBinary(ws.EncodeFrame(topic, ws.KindAwareness, "", []byte("cursor")))
			if f := b.read(); f.kind != ws.KindAwareness || string(f.payload) != "cursor" {
				t.Fatalf("awareness: %+v", f)
			}
			// the viewer cannot write
			b.sendBinary(ws.EncodeFrame(topic, ws.KindUpdate, "", []byte{9}))
			if f := b.expect("error"); !strings.Contains(string(f.raw), "forbidden") {
				t.Fatalf("viewer write: %s", f.raw)
			}
			if u, s, _ := hub.DocUpdates(ctx, page); u != 2 || s != 0 {
				t.Fatalf("stored %d updates %d snapshots", u, s)
			}
			// A late joiner gets the log replayed in order.
			_, lc := ta.UserClient(t, auth.RoleReader)
			late, _, _ := dial(t, ta, lc)
			late.send(map[string]string{"op": "sub", "topic": topic})
			late.expect("subscribed")
			for i, want := range [][]byte{{1, 2, 3}, {4, 5}} {
				f := late.read()
				if f.kind != ws.KindUpdate || f.id != ids[i] || string(f.payload) != string(want) {
					t.Fatalf("replay %d: %+v", i, f)
				}
			}
			if f := late.read(); f.kind != ws.KindReplayDone || f.id != ids[1] {
				t.Fatalf("replay done: %+v", f)
			}
			// An update arriving after the snapshot's watermark survives compaction.
			a.sendBinary(ws.EncodeFrame(topic, ws.KindUpdate, "", []byte{6}))
			a.read() // ack
			a.sendBinary(ws.EncodeFrame(topic, ws.KindSnapshot, ids[1], []byte("STATE-1-5")))
			a.sendBinary(ws.EncodeFrame(topic, ws.KindSnapshot, "01jnotarealwatermark0000000", []byte("x")))
			if f := a.expect("error"); !strings.Contains(string(f.raw), "bad_watermark") {
				t.Fatalf("bad watermark: %s", f.raw)
			}
			if u, s, _ := hub.DocUpdates(ctx, page); u != 1 || s != 1 {
				t.Fatalf("after compaction: %d updates %d snapshots", u, s)
			}
			_, nc := ta.UserClient(t, auth.RoleReader)
			n, _, _ := dial(t, ta, nc)
			n.send(map[string]string{"op": "sub", "topic": topic})
			n.expect("subscribed")
			if f := n.read(); f.kind != ws.KindUpdate || string(f.payload) != "STATE-1-5" || f.id != ids[1] {
				t.Fatalf("snapshot replay: %+v", f)
			}
			if f := n.read(); f.kind != ws.KindUpdate || string(f.payload) != string([]byte{6}) {
				t.Fatalf("post-snapshot update: %+v", f)
			}
			if f := n.read(); f.kind != ws.KindReplayDone {
				t.Fatalf("done: %+v", f)
			}
		})
	}
}

func TestFrameCodec(t *testing.T) {
	b := ws.EncodeFrame("doc:page:abc", ws.KindSnapshot, "01jid", []byte("payload"))
	topic, kind, id, payload, err := ws.DecodeFrame(b)
	if err != nil || topic != "doc:page:abc" || kind != ws.KindSnapshot || id != "01jid" || string(payload) != "payload" {
		t.Fatal(topic, kind, id, payload, err)
	}
	for _, bad := range [][]byte{{}, {0, 0, 0}, {5, 'a'}, {1, 'a', 0, 9}} {
		if _, _, _, _, err := ws.DecodeFrame(bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

// TestRevalidationRevokesAccess: an open socket loses topics when the role
// that granted them is taken away, and is closed when its session ends.
func TestRevalidationRevokesAccess(t *testing.T) {
	ta := testutil.NewTestApp(t)
	ta.Deps.Hub.SetRecheckInterval(100 * time.Millisecond)
	ctx := context.Background()
	mod := ta.CreateUser(t, "wren", auth.RoleAdmin)
	c := ta.Login(t, "wren")
	wc, _, err := dial(t, ta, c)
	if err != nil {
		t.Fatal(err)
	}
	wc.send(map[string]string{"op": "sub", "topic": "admin:queue"})
	wc.expect("subscribed")
	wc.send(map[string]string{"op": "sub", "topic": "user:" + mod.ID})
	wc.expect("subscribed")

	// Demoted: admin topics go away, the own user topic stays.
	if err := ta.Deps.Auth.SetRole(ctx, mod.ID, auth.RoleCreator); err != nil {
		t.Fatal(err)
	}
	if f := wc.expect("error"); f.msg.Topic != "admin:queue" {
		t.Fatalf("expected admin:queue to be revoked: %+v %s", f.msg, f.raw)
	}
	deadline := time.Now().Add(3 * time.Second)
	for ta.Deps.Hub.Subscribers("admin:queue") != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := ta.Deps.Hub.Subscribers("admin:queue"); n != 0 {
		t.Fatalf("still subscribed to admin:queue (%d)", n)
	}
	ta.Deps.Hub.PublishUser(mod.ID, "marker", nil)
	wc.expect("marker")

	// Signed out everywhere: the socket is closed.
	if _, err := ta.Deps.DB.Exec(ctx, `DELETE FROM sessions WHERE user_id = ?`, mod.ID); err != nil {
		t.Fatal(err)
	}
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for {
		if _, _, err := wc.conn.Read(rctx); err != nil {
			if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Fatalf("socket not closed for the ended session: %v", err)
			}
			break
		}
	}
}

func dialBearer(t *testing.T, ta *testutil.TestApp, token string) *client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h := http.Header{"Authorization": {"Bearer " + token}}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ta.URL, "http")+"/ws", &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatal(err)
	}
	conn.SetReadLimit(16 << 20)
	t.Cleanup(func() { conn.Close(websocket.StatusNormalClosure, "") })
	return &client{t: t, conn: conn}
}

// The user topic carries job summaries from every project, so a bearer token
// may follow it only with projects:read and no project allow-list; an admin
// token needs the admin scope for admin topics.
func TestBearerTokensFollowJobRulesOnUserAndAdminTopics(t *testing.T) {
	ta := testutil.NewTestApp(t)
	ctx := context.Background()
	u := ta.CreateUser(t, "kite", auth.RoleAdmin)
	mk := func(name string, scopes, projects []string) string {
		raw, _, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: u.ID, Kind: auth.TokenAgent, Name: name,
			Scopes: scopes, ProjectIDs: projects})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	cases := []struct {
		name   string
		token  string
		topic  string
		expect string
	}{
		{"no projects:read", mk("narrow", []string{"generate"}, nil), "user:" + u.ID, "error"},
		{"project allow-list", mk("allowlist", []string{"projects:read"}, []string{db.NewID()}), "user:" + u.ID, "error"},
		{"full read", mk("reader", []string{"projects:read"}, nil), "user:" + u.ID, "subscribed"},
		{"admin topic without admin scope", mk("not-admin", []string{"projects:read"}, nil), "admin:queue", "error"},
		{"admin topic with admin scope", mk("admin", []string{"projects:read", "admin"}, nil), "admin:queue", "subscribed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wc := dialBearer(t, ta, tc.token)
			wc.send(map[string]string{"op": "sub", "topic": tc.topic})
			wc.expect(tc.expect)
		})
	}
}
