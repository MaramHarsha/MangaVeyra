package ws_test

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
	"github.com/MaramHarsha/MangaVeyra/internal/ws"
)

// Review fixes: a stored document log is replayed in bounded batches (never
// loaded whole for each subscriber), and binary awareness frames, which are
// relayed to every subscriber, are capped like JSON presence.
func TestReviewDocReplayIsBatchedAndAwarenessIsCapped(t *testing.T) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect))
			ctx := context.Background()
			page := seedPage(t, ta)
			topic := ws.DocPagePrefix + page
			ta.Deps.Hub.Authorize(ws.DocPagePrefix, func(ctx context.Context, id httpx.Identity, tp string, write bool) error {
				if tp != topic || write {
					return ws.ErrForbidden
				}
				return nil
			})
			// A log written before document writes were refused.
			ids := []string{}
			for i := 0; i < 150; i++ {
				id := db.NewID()
				if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO page_doc_updates (id, page_id, kind, data, user_id, created_at) VALUES (?, ?, 'update', ?, NULL, ?)`,
					id, page, []byte{byte(i)}, db.Now()); err != nil {
					t.Fatal(err)
				}
				ids = append(ids, id)
			}
			sort.Strings(ids)
			_, rc := ta.UserClient(t, auth.RoleReader)
			reader, _, _ := dial(t, ta, rc)
			reader.send(map[string]string{"op": "sub", "topic": topic})
			reader.expect("subscribed")
			for i, want := range ids {
				if f := reader.read(); f.kind != ws.KindUpdate || f.id != want {
					t.Fatalf("replay %d = %+v, want %s", i, f, want)
				}
			}
			if f := reader.read(); f.kind != ws.KindReplayDone || f.id != ids[len(ids)-1] {
				t.Fatalf("replay done = %+v", f)
			}

			_, oc := ta.UserClient(t, auth.RoleReader)
			other, _, _ := dial(t, ta, oc)
			other.send(map[string]string{"op": "sub", "topic": topic})
			other.expect("subscribed")
			for range ids {
				other.read()
			}
			other.read() // replay done
			reader.sendBinary(ws.EncodeFrame(topic, ws.KindAwareness, "", make([]byte, 5<<10)))
			if f := reader.expect("error"); !strings.Contains(string(f.raw), "too_large") {
				t.Fatalf("oversized awareness = %s", f.raw)
			}
			reader.sendBinary(ws.EncodeFrame(topic, ws.KindAwareness, "", []byte("cursor")))
			if f := other.read(); f.kind != ws.KindAwareness || string(f.payload) != "cursor" {
				t.Fatalf("awareness after the oversized one = %+v", f)
			}
		})
	}
}
