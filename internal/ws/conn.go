package ws

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
)

type frame struct {
	text bool
	data []byte
}

type conn struct {
	hub *Hub
	ws  *websocket.Conn
	// ident is the caller; recheck replaces it with a fresh copy (role changes).
	ident  atomic.Pointer[httpx.Identity]
	connID string
	ctx    context.Context
	cancel context.CancelFunc
	send   chan frame

	mu       sync.Mutex
	topics   map[string]bool
	writable map[string]bool
	closed   bool
}

type clientMsg struct {
	Op    string          `json:"op"`
	Topic string          `json:"topic"`
	Data  json.RawMessage `json:"data"`
}

// identity returns the caller's current identity.
func (c *conn) identity() httpx.Identity {
	if p := c.ident.Load(); p != nil {
		return *p
	}
	return httpx.Identity{}
}

func (c *conn) presenceUser(state json.RawMessage) PresenceUser {
	id := c.identity()
	u := PresenceUser{ConnID: c.connID, State: state, Agent: id.IsAgent()}
	if id.User != nil {
		u.UserID, u.Username, u.DisplayName = id.User.ID, id.User.Username, id.User.Name()
	}
	return u
}

// recheckLoop periodically re-authorizes the connection (see recheck).
func (c *conn) recheckLoop(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
			c.recheck()
		}
	}
}

// recheck re-validates the caller (a revoked session, revoked token or
// suspended account closes the connection) and re-authorizes every
// subscription, so losing access to a project or job also stops its
// updates. Cached write permissions are dropped and asked for again.
func (c *conn) recheck() {
	if fn := c.hub.Revalidate; fn != nil {
		id, err := fn(c.ctx, c.identity())
		if err != nil {
			if c.ctx.Err() != nil {
				return
			}
			if errors.Is(err, ErrSessionEnded) {
				c.close(websocket.StatusPolicyViolation, "session ended")
				return
			}
			c.hub.log.Warn("ws: revalidate", "conn", c.connID, "err", err)
			return // transient (database) error: keep the connection
		}
		c.ident.Store(&id)
	}
	c.mu.Lock()
	topics := make([]string, 0, len(c.topics))
	for t := range c.topics {
		topics = append(topics, t)
	}
	c.writable = map[string]bool{}
	c.mu.Unlock()
	for _, t := range topics {
		if err := c.hub.Allowed(c.ctx, c.identity(), t, false); err == nil {
			continue
		}
		c.mu.Lock()
		had := c.topics[t]
		delete(c.topics, t)
		c.mu.Unlock()
		if had {
			c.hub.unsubscribe(c, t)
			c.sendError(t, "forbidden", "access to this topic was revoked")
		}
	}
}

// enqueue queues a frame without blocking; a client that cannot keep up is disconnected.
func (c *conn) enqueue(f frame) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return
	}
	select {
	case c.send <- f:
	default:
		c.hub.log.Warn("ws: slow consumer disconnected", "conn", c.connID)
		go c.close(websocket.StatusPolicyViolation, "slow consumer")
	}
}

// sendWait queues a frame, waiting for buffer space; false when the connection is gone.
func (c *conn) sendWait(f frame) bool {
	select {
	case c.send <- f:
		return true
	case <-c.ctx.Done():
		return false
	}
}

func (c *conn) sendJSON(m Message) {
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	c.enqueue(frame{text: true, data: b})
}

func (c *conn) sendError(topic, code, msg string) {
	c.sendJSON(Message{Topic: topic, Type: "error", Data: map[string]string{"code": code, "message": msg}})
}

func (c *conn) close(code websocket.StatusCode, reason string) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()
	// Send the close frame before cancelling the context: the read loop reads
	// with c.ctx, and a cancelled read makes the library drop the TCP
	// connection at once, so the client would see EOF instead of the code.
	_ = c.ws.Close(code, reason)
	c.cancel()
}

func (c *conn) writeLoop() {
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case f := <-c.send:
			ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
			typ := websocket.MessageBinary
			if f.text {
				typ = websocket.MessageText
			}
			err := c.ws.Write(ctx, typ, f.data)
			cancel()
			if err != nil {
				c.close(websocket.StatusInternalError, "write failed")
				return
			}
		case <-ping.C:
			ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
			err := c.ws.Ping(ctx)
			cancel()
			if err != nil {
				c.close(websocket.StatusGoingAway, "ping timeout")
				return
			}
		}
	}
}

func (c *conn) readLoop() {
	for {
		typ, data, err := c.ws.Read(c.ctx)
		if err != nil {
			return
		}
		switch typ {
		case websocket.MessageText:
			if len(data) > maxTextFrame {
				c.sendError("", "too_large", "text frame too large")
				continue
			}
			c.handleText(data)
		case websocket.MessageBinary:
			c.handleBinary(data)
		}
	}
}

func (c *conn) handleText(data []byte) {
	var m clientMsg
	if err := json.Unmarshal(data, &m); err != nil {
		c.sendError("", "bad_request", "invalid JSON")
		return
	}
	switch m.Op {
	case "ping":
		c.sendJSON(Message{Type: "pong"})
	case "sub":
		c.subscribe(m.Topic)
	case "unsub":
		c.mu.Lock()
		had := c.topics[m.Topic]
		delete(c.topics, m.Topic)
		delete(c.writable, m.Topic)
		c.mu.Unlock()
		if had {
			c.hub.unsubscribe(c, m.Topic)
		}
		c.sendJSON(Message{Topic: m.Topic, Type: "unsubscribed"})
	case "presence":
		c.mu.Lock()
		subscribed := c.topics[m.Topic]
		c.mu.Unlock()
		if !subscribed {
			c.sendError(m.Topic, "not_subscribed", "subscribe before sending presence")
			return
		}
		if len(m.Data) > maxPresenceSize {
			c.sendError(m.Topic, "too_large", "presence state too large")
			return
		}
		c.hub.setPresence(c, m.Topic, m.Data)
	default:
		c.sendError(m.Topic, "bad_request", "unknown op")
	}
}

func (c *conn) subscribe(topic string) {
	c.mu.Lock()
	already, n := c.topics[topic], len(c.topics)
	c.mu.Unlock()
	if already {
		c.sendJSON(Message{Topic: topic, Type: "subscribed"})
		return
	}
	if n >= maxTopics {
		c.sendError(topic, "too_many_topics", "too many subscriptions")
		return
	}
	if err := c.hub.Allowed(c.ctx, c.identity(), topic, false); err != nil {
		c.sendError(topic, "forbidden", "not allowed to subscribe to this topic")
		return
	}
	c.mu.Lock()
	c.topics[topic] = true
	c.mu.Unlock()
	c.hub.subscribe(c, topic)
	c.sendJSON(Message{Topic: topic, Type: "subscribed"})
	if list := c.hub.Presence(topic); len(list) > 0 {
		c.sendJSON(Message{Topic: topic, Type: "presence.list", Data: list})
	}
	if isDoc(topic) {
		c.hub.replay(c, topic)
	}
}

func (c *conn) canWrite(topic string) bool {
	c.mu.Lock()
	sub, ok := c.topics[topic], c.writable[topic]
	c.mu.Unlock()
	if !sub {
		return false
	}
	if ok {
		return true
	}
	if err := c.hub.Allowed(c.ctx, c.identity(), topic, true); err != nil {
		return false
	}
	c.mu.Lock()
	c.writable[topic] = true
	c.mu.Unlock()
	return true
}

func (c *conn) handleBinary(data []byte) {
	f, err := decodeFrame(data)
	if err != nil {
		c.sendError("", "bad_frame", err.Error())
		return
	}
	if !isDoc(f.topic) {
		c.sendError(f.topic, "bad_frame", "binary frames are only accepted on doc: topics")
		return
	}
	switch f.kind {
	case KindAwareness:
		c.mu.Lock()
		sub := c.topics[f.topic]
		c.mu.Unlock()
		if !sub {
			c.sendError(f.topic, "not_subscribed", "subscribe first")
			return
		}
		// Awareness is relayed to every subscriber: capped like presence.
		if len(f.payload) > maxPresenceSize {
			c.sendError(f.topic, "too_large", "awareness state too large")
			return
		}
		c.hub.broadcast(f.topic, frame{data: encodeFrame(f.topic, KindAwareness, "", f.payload)}, c)
	case KindUpdate:
		if !c.canWrite(f.topic) {
			c.sendError(f.topic, "forbidden", "no write access to this document")
			return
		}
		id, err := c.hub.persistUpdate(c.ctx, f.topic, f.payload, c.userID())
		if err != nil {
			c.hub.log.Error("ws: persist update", "topic", f.topic, "err", err)
			c.sendError(f.topic, "persist_failed", "update could not be saved")
			return
		}
		c.hub.broadcast(f.topic, frame{data: encodeFrame(f.topic, KindUpdate, id, f.payload)}, c)
		if id != "" {
			c.enqueue(frame{data: encodeFrame(f.topic, KindAck, id, nil)})
		}
	case KindSnapshot:
		if !c.canWrite(f.topic) {
			c.sendError(f.topic, "forbidden", "no write access to this document")
			return
		}
		if err := c.hub.compact(c.ctx, f.topic, f.id, f.payload, c.userID()); err != nil {
			if errors.Is(err, errBadWatermark) {
				c.sendError(f.topic, "bad_watermark", err.Error())
				return
			}
			c.hub.log.Error("ws: compact", "topic", f.topic, "err", err)
			c.sendError(f.topic, "persist_failed", "snapshot could not be saved")
		}
	default:
		c.sendError(f.topic, "bad_frame", "unknown frame kind")
	}
}

func (c *conn) userID() string {
	if u := c.identity().User; u != nil {
		return u.ID
	}
	return ""
}
