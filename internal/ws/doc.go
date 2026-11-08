package ws

import (
	"context"
	"errors"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

var errBadWatermark = errors.New("ws: snapshot watermark is not a known update id")

func isDoc(topic string) bool { return strings.HasPrefix(topic, "doc:") }

type docFrame struct {
	topic   string
	kind    byte
	id      string
	payload []byte
}

// EncodeFrame builds a binary document frame (exported for clients written in Go and for tests).
func EncodeFrame(topic string, kind byte, id string, payload []byte) []byte {
	return encodeFrame(topic, kind, id, payload)
}

func encodeFrame(topic string, kind byte, id string, payload []byte) []byte {
	b := make([]byte, 0, 3+len(topic)+len(id)+len(payload))
	b = append(b, byte(len(topic)))
	b = append(b, topic...)
	b = append(b, kind, byte(len(id)))
	b = append(b, id...)
	return append(b, payload...)
}

// DecodeFrame parses a binary document frame.
func DecodeFrame(data []byte) (topic string, kind byte, id string, payload []byte, err error) {
	f, err := decodeFrame(data)
	return f.topic, f.kind, f.id, f.payload, err
}

func decodeFrame(data []byte) (docFrame, error) {
	if len(data) < 3 {
		return docFrame{}, errors.New("frame too short")
	}
	tl := int(data[0])
	if tl == 0 || len(data) < 1+tl+2 {
		return docFrame{}, errors.New("bad topic length")
	}
	f := docFrame{topic: string(data[1 : 1+tl]), kind: data[1+tl]}
	il := int(data[2+tl])
	start := 3 + tl
	if len(data) < start+il {
		return docFrame{}, errors.New("bad id length")
	}
	f.id = string(data[start : start+il])
	f.payload = data[start+il:]
	if len(f.payload) > maxBinaryFrame {
		return docFrame{}, errors.New("payload too large")
	}
	return f, nil
}

func pageIDOf(topic string) string {
	if strings.HasPrefix(topic, DocPagePrefix) {
		return strings.TrimPrefix(topic, DocPagePrefix)
	}
	return ""
}

// persistUpdate appends an update of a page document and returns its id ("" when the topic is not persisted).
func (h *Hub) persistUpdate(ctx context.Context, topic string, payload []byte, userID string) (string, error) {
	pageID := pageIDOf(topic)
	if pageID == "" || h.db == nil {
		return "", nil
	}
	if len(payload) == 0 {
		return "", errors.New("empty update")
	}
	id := db.NewID()
	_, err := h.db.Exec(ctx, `INSERT INTO page_doc_updates (id, page_id, kind, data, user_id, created_at) VALUES (?, ?, 'update', ?, ?, ?)`,
		id, pageID, payload, db.NullString(userID), db.Now())
	return id, err
}

// compact replaces every stored row up to watermark with a snapshot.
func (h *Hub) compact(ctx context.Context, topic, watermark string, state []byte, userID string) error {
	pageID := pageIDOf(topic)
	if pageID == "" || h.db == nil {
		return nil
	}
	if len(state) == 0 {
		return errors.New("empty snapshot")
	}
	return h.db.WithTx(ctx, func(tx *db.Tx) error {
		id := watermark
		if id == "" {
			id = db.NewID()
		} else {
			if !db.ValidID(id) {
				return errBadWatermark
			}
			var n int
			if err := tx.Get(ctx, &n, `SELECT COUNT(*) FROM page_doc_updates WHERE page_id = ? AND id = ?`, pageID, id); err != nil {
				return err
			}
			if n == 0 {
				return errBadWatermark
			}
			if _, err := tx.Exec(ctx, `DELETE FROM page_doc_updates WHERE page_id = ? AND id <= ?`, pageID, id); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO page_doc_updates (id, page_id, kind, data, user_id, created_at) VALUES (?, ?, 'snapshot', ?, ?, ?)`,
			id, pageID, state, db.NullString(userID), db.Now())
		return err
	})
}

// replayBatch is how many stored rows one replay query loads: the log is
// streamed in id order, never loaded whole for each subscriber.
const replayBatch = 16

// replay sends the stored log of a page document to one subscriber, then REPLAY_DONE.
func (h *Hub) replay(c *conn, topic string) {
	pageID := pageIDOf(topic)
	last := ""
	for pageID != "" && h.db != nil {
		var rows []struct {
			ID   string `db:"id"`
			Data []byte `db:"data"`
		}
		if err := h.db.Select(c.ctx, &rows, `SELECT id, data FROM page_doc_updates WHERE page_id = ? AND id > ? ORDER BY id LIMIT ?`,
			pageID, last, replayBatch); err != nil {
			h.log.Error("ws: replay", "topic", topic, "err", err)
			c.sendError(topic, "replay_failed", "could not load document")
			return
		}
		// Blocking sends: a long log must not trip the slow-consumer guard.
		for _, r := range rows {
			if !c.sendWait(frame{data: encodeFrame(topic, KindUpdate, r.ID, r.Data)}) {
				return
			}
			last = r.ID
		}
		if len(rows) < replayBatch {
			break
		}
	}
	c.sendWait(frame{data: encodeFrame(topic, KindReplayDone, last, nil)})
}

// DocUpdates returns the stored log size of a page document (used by admin tools and tests).
func (h *Hub) DocUpdates(ctx context.Context, pageID string) (updates, snapshots int, err error) {
	rows := []struct {
		Kind string `db:"kind"`
		N    int    `db:"n"`
	}{}
	if err := h.db.Select(ctx, &rows, `SELECT kind, COUNT(*) AS n FROM page_doc_updates WHERE page_id = ? GROUP BY kind`, pageID); err != nil {
		return 0, 0, err
	}
	for _, r := range rows {
		if r.Kind == "snapshot" {
			snapshots = r.N
		} else {
			updates = r.N
		}
	}
	return updates, snapshots, nil
}
