// Package ws is the realtime hub: one WebSocket per browser tab at GET /ws.
//
// # Protocol
//
// Text frames carry JSON. The client sends
//
//	{"op": "sub",      "topic": "job:01j..."}
//	{"op": "unsub",    "topic": "job:01j..."}
//	{"op": "presence", "topic": "doc:page:01j...", "data": {"cursor": [12.5, 40.1]}}
//	{"op": "ping"}
//
// and the server pushes {"topic": "...", "type": "...", "data": ...}, e.g.
// {"topic":"job:01j...","type":"job.progress","data":{...}}. Replies use the
// types "subscribed", "unsubscribed", "error", "pong", "presence",
// "presence.list", "presence.join" and "presence.leave".
//
// Topics are authorized per prefix by Authorizer callbacks that modules
// register (studio registers "project:" and "doc:page:", jobs registers
// "job:"). Topics without an authorizer are refused.
//
// # Collaborative documents (Yjs relay)
//
// Topics starting with "doc:" carry binary frames:
//
//	[1 byte topic length][topic][1 byte kind][1 byte id length][id][payload]
//
// kinds: 0 UPDATE (a Yjs update; relayed to peers and, for doc:page:{id},
// appended to page_doc_updates), 1 AWARENESS (relayed only), 2 SNAPSHOT
// (client → server: a Yjs state update covering everything up to the
// watermark id; older log rows are replaced by it), 3 REPLAY_DONE (server →
// client after the stored log was replayed on subscribe; id = last id),
// 4 ACK (server → sender: the id assigned to its UPDATE).
// Server → client UPDATE frames carry the stored id so clients can send it
// back as the SNAPSHOT watermark.
package ws

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// Binary frame kinds.
const (
	KindUpdate     byte = 0
	KindAwareness  byte = 1
	KindSnapshot   byte = 2
	KindReplayDone byte = 3
	KindAck        byte = 4
)

// DocPagePrefix is the topic prefix of persisted page documents.
const DocPagePrefix = "doc:page:"

// Limits.
const (
	maxTextFrame    = 64 << 10
	maxBinaryFrame  = 8 << 20
	maxTopics       = 256
	sendBuffer      = 256
	pingInterval    = 30 * time.Second
	writeTimeout    = 10 * time.Second
	maxPresenceSize = 4 << 10
)

// ErrForbidden is returned by authorizers to refuse a topic.
var ErrForbidden = errors.New("ws: forbidden")

// ErrSessionEnded is returned (wrapped) by Hub.Revalidate when the caller's
// session or token is no longer valid; the connection is then closed.
var ErrSessionEnded = errors.New("ws: session ended")

// recheckInterval is the default re-authorization period of open connections.
const recheckInterval = 30 * time.Second

// Authorizer decides whether id may subscribe to (write=false) or write
// binary updates to (write=true) topic.
type Authorizer func(ctx context.Context, id httpx.Identity, topic string, write bool) error

// Message is a server push.
type Message struct {
	Topic string `json:"topic,omitempty"`
	Type  string `json:"type"`
	Data  any    `json:"data,omitempty"`
}

// PresenceUser is one participant of a topic.
type PresenceUser struct {
	ConnID      string          `json:"conn"`
	UserID      string          `json:"id"`
	Username    string          `json:"username"`
	DisplayName string          `json:"display_name"`
	Agent       bool            `json:"agent,omitempty"`
	State       json.RawMessage `json:"state,omitempty"`
}

// Hub routes messages between server code and connected clients.
type Hub struct {
	db  *db.DB
	log *slog.Logger
	// OriginPatterns are extra allowed Origin hosts (the request host is always allowed).
	OriginPatterns []string
	// Revalidate, when set, is called periodically (SetRecheckInterval) for each connection
	// with the caller's identity. It returns a refreshed identity, an error
	// wrapping ErrSessionEnded to close the connection (session or token
	// revoked, account suspended), or another error to retry later.
	// Set it during wiring, before the hub serves connections.
	Revalidate func(ctx context.Context, id httpx.Identity) (httpx.Identity, error)

	recheckEvery atomic.Int64 // nanoseconds; 0 = recheckInterval

	mu          sync.RWMutex
	topics      map[string]map[*conn]struct{}
	conns       map[*conn]struct{}
	authorizers map[string]Authorizer
	presence    map[string]map[*conn]json.RawMessage
	closed      bool

	connSeq atomic.Int64
}

// New creates a hub. d may be nil to disable document persistence (tests).
func New(d *db.DB, log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	h := &Hub{
		db: d, log: log,
		topics:      map[string]map[*conn]struct{}{},
		conns:       map[*conn]struct{}{},
		authorizers: map[string]Authorizer{},
		presence:    map[string]map[*conn]json.RawMessage{},
	}
	h.Authorize("user:", func(ctx context.Context, id httpx.Identity, topic string, write bool) error {
		if id.User == nil || write {
			return ErrForbidden
		}
		// The user topic carries job summaries from every project. A bearer
		// token may follow it only with projects:read and no project
		// allow-list, the same rule as listing all of the user's jobs.
		if id.Token != nil && (!id.Token.HasScope("projects:read") || len(id.Token.ProjectIDs) != 0) {
			return ErrForbidden
		}
		if topic == "user:"+id.User.ID || (id.User.IsAdmin() && (id.Token == nil || id.Token.HasScope("admin"))) {
			return nil
		}
		return ErrForbidden
	})
	h.Authorize("admin:", func(ctx context.Context, id httpx.Identity, topic string, write bool) error {
		if id.User != nil && id.User.IsAdmin() && !write && (id.Token == nil || id.Token.HasScope("admin")) {
			return nil
		}
		return ErrForbidden
	})
	return h
}

// SetRecheckInterval changes how often new connections re-authorize their
// identity and subscriptions (default 30 s; tests use shorter periods).
func (h *Hub) SetRecheckInterval(d time.Duration) { h.recheckEvery.Store(int64(d)) }

// Authorize registers fn for topics starting with prefix. The longest matching prefix wins.
func (h *Hub) Authorize(prefix string, fn Authorizer) {
	h.mu.Lock()
	h.authorizers[prefix] = fn
	h.mu.Unlock()
}

func (h *Hub) authorizer(topic string) Authorizer {
	h.mu.RLock()
	defer h.mu.RUnlock()
	best, bestLen := Authorizer(nil), -1
	for p, fn := range h.authorizers {
		if strings.HasPrefix(topic, p) && len(p) > bestLen {
			best, bestLen = fn, len(p)
		}
	}
	return best
}

// Allowed reports whether id may subscribe to (or write to) topic.
func (h *Hub) Allowed(ctx context.Context, id httpx.Identity, topic string, write bool) error {
	if topic == "" || len(topic) > 200 {
		return ErrForbidden
	}
	fn := h.authorizer(topic)
	if fn == nil {
		return ErrForbidden
	}
	return fn(ctx, id, topic, write)
}

// Publish sends a JSON message to every subscriber of topic. It never blocks.
func (h *Hub) Publish(topic, typ string, data any) {
	b, err := json.Marshal(Message{Topic: topic, Type: typ, Data: data})
	if err != nil {
		h.log.Error("ws: marshal publish", "topic", topic, "err", err)
		return
	}
	h.broadcast(topic, frame{text: true, data: b}, nil)
}

// PublishUser sends a message to topic "user:{userID}".
func (h *Hub) PublishUser(userID, typ string, data any) { h.Publish("user:"+userID, typ, data) }

func (h *Hub) broadcast(topic string, f frame, except *conn) {
	h.mu.RLock()
	subs := make([]*conn, 0, len(h.topics[topic]))
	for c := range h.topics[topic] {
		if c != except {
			subs = append(subs, c)
		}
	}
	h.mu.RUnlock()
	for _, c := range subs {
		c.enqueue(f)
	}
}

// Subscribers returns the number of connections subscribed to topic.
func (h *Hub) Subscribers(topic string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.topics[topic])
}

// Connections returns the number of open connections.
func (h *Hub) Connections() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}

// Presence lists the participants of topic.
func (h *Hub) Presence(topic string) []PresenceUser {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]PresenceUser, 0, len(h.presence[topic]))
	for c, st := range h.presence[topic] {
		out = append(out, c.presenceUser(st))
	}
	return out
}

// Close disconnects every client (graceful shutdown).
func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	conns := make([]*conn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		c.close(websocket.StatusGoingAway, "server shutting down")
	}
}

// Routes registers GET /ws.
func (h *Hub) Routes(r *httpx.Router) { r.Handle("GET /ws", h) }

// Name implements core.Module.
func (h *Hub) Name() string { return "ws" }

// ServeHTTP upgrades the request. Callers must be signed in (session cookie or bearer token).
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := httpx.IdentityFrom(r.Context())
	if id.User == nil {
		httpx.WriteError(w, r, httpx.ErrUnauthorized())
		return
	}
	h.mu.RLock()
	closed := h.closed
	h.mu.RUnlock()
	if closed {
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}
	wsc, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: h.OriginPatterns})
	if err != nil {
		h.log.Debug("ws: accept", "err", err)
		return
	}
	wsc.SetReadLimit(maxBinaryFrame + 512)
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	c := &conn{
		hub: h, ws: wsc, ctx: ctx, cancel: cancel,
		connID: "c" + itoa(h.connSeq.Add(1)),
		send:   make(chan frame, sendBuffer),
		topics: map[string]bool{}, writable: map[string]bool{},
	}
	c.ident.Store(&id)
	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.mu.Unlock()
	every := time.Duration(h.recheckEvery.Load())
	if every <= 0 {
		every = recheckInterval
	}
	go c.recheckLoop(every)
	go c.writeLoop()
	c.readLoop()
	h.drop(c)
}

func (h *Hub) subscribe(c *conn, topic string) {
	h.mu.Lock()
	if h.topics[topic] == nil {
		h.topics[topic] = map[*conn]struct{}{}
	}
	h.topics[topic][c] = struct{}{}
	h.mu.Unlock()
}

func (h *Hub) unsubscribe(c *conn, topic string) {
	h.mu.Lock()
	if subs := h.topics[topic]; subs != nil {
		delete(subs, c)
		if len(subs) == 0 {
			delete(h.topics, topic)
		}
	}
	_, hadPresence := h.presence[topic][c]
	if hadPresence {
		delete(h.presence[topic], c)
		if len(h.presence[topic]) == 0 {
			delete(h.presence, topic)
		}
	}
	h.mu.Unlock()
	if hadPresence {
		h.publishPresence(topic, "presence.leave", c.presenceUser(nil), c)
	}
}

func (h *Hub) drop(c *conn) {
	c.mu.Lock()
	topics := make([]string, 0, len(c.topics))
	for t := range c.topics {
		topics = append(topics, t)
	}
	c.mu.Unlock()
	for _, t := range topics {
		h.unsubscribe(c, t)
	}
	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()
	c.close(websocket.StatusNormalClosure, "")
}

func (h *Hub) setPresence(c *conn, topic string, state json.RawMessage) {
	h.mu.Lock()
	if h.presence[topic] == nil {
		h.presence[topic] = map[*conn]json.RawMessage{}
	}
	_, existed := h.presence[topic][c]
	h.presence[topic][c] = state
	h.mu.Unlock()
	typ := "presence"
	if !existed {
		typ = "presence.join"
	}
	h.publishPresence(topic, typ, c.presenceUser(state), c)
}

func (h *Hub) publishPresence(topic, typ string, u PresenceUser, except *conn) {
	b, _ := json.Marshal(Message{Topic: topic, Type: typ, Data: u})
	h.broadcast(topic, frame{text: true, data: b}, except)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
