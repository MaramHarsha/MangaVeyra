package notify_test

import (
	"context"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/notify"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

// A grouped notification updated in place moves to the top: lists follow the
// time shown, and paging by the last row's id keeps that order.
func TestNotificationsListByShownTimeAndPage(t *testing.T) {
	for _, d := range testdb.Dialects() {
		t.Run(d, func(t *testing.T) {
			ta := testutil.NewTestApp(t, testutil.WithDialect(d))
			ctx := context.Background()
			n := ta.Deps.Notify
			clock := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
			n.Now = func() time.Time { return clock }
			u := ta.CreateUser(t, "order-owner", auth.RoleCreator)
			grouped, err := n.Notify(ctx, notify.Input{UserID: u.ID, Kind: notify.KindJobDone, Title: "Generation finished", GroupKey: notify.KindJobDone})
			if err != nil {
				t.Fatal(err)
			}
			clock = clock.Add(time.Minute)
			later, err := n.Notify(ctx, notify.Input{UserID: u.ID, Kind: notify.KindComment, Title: "Kaito commented on panel 7.3"})
			if err != nil {
				t.Fatal(err)
			}
			clock = clock.Add(5 * time.Minute)
			if again, err := n.Notify(ctx, notify.Input{UserID: u.ID, Kind: notify.KindJobDone, Title: "Generation finished", GroupKey: notify.KindJobDone}); err != nil || again.ID != grouped.ID {
				t.Fatalf("grouping: %+v %v", again, err)
			}
			list, err := n.List(ctx, u.ID, notify.ListOptions{})
			if err != nil || len(list) != 2 || list[0].ID != grouped.ID || list[1].ID != later.ID {
				t.Fatalf("not newest first by shown time: %+v %v", list, err)
			}
			first, err := n.List(ctx, u.ID, notify.ListOptions{Limit: 1})
			if err != nil || len(first) != 1 || first[0].ID != grouped.ID {
				t.Fatalf("first page: %+v %v", first, err)
			}
			second, err := n.List(ctx, u.ID, notify.ListOptions{Limit: 1, Before: first[0].ID})
			if err != nil || len(second) != 1 || second[0].ID != later.ID {
				t.Fatalf("second page lost the older row: %+v %v", second, err)
			}
			if rest, err := n.List(ctx, u.ID, notify.ListOptions{Limit: 1, Before: later.ID}); err != nil || len(rest) != 0 {
				t.Fatalf("third page: %+v %v", rest, err)
			}
		})
	}
}

// Mark-all for one category only reads that category's notifications.
func TestNotificationsCategoryReadAndCount(t *testing.T) {
	for _, d := range testdb.Dialects() {
		t.Run(d, func(t *testing.T) {
			ta := testutil.NewTestApp(t, testutil.WithDialect(d))
			ctx := context.Background()
			n := ta.Deps.Notify
			u := ta.CreateUser(t, "category-owner", auth.RoleCreator)
			other := ta.CreateUser(t, "category-other", auth.RoleCreator)
			for _, in := range []notify.Input{
				{UserID: u.ID, Kind: notify.KindJobDone, Title: "Generation finished"},
				{UserID: u.ID, Kind: notify.KindChapter, Title: "Lanterns of Oru · ch. 42 is out"},
				{UserID: u.ID, Kind: notify.KindUpdate, Title: "Update available"},
				{UserID: other.ID, Kind: notify.KindChapter, Title: "Glass Orchard · ch. 3 is out"},
			} {
				if _, err := n.Notify(ctx, in); err != nil {
					t.Fatal(err)
				}
			}
			reading := []string{notify.KindChapter, notify.KindFollow}
			if c, err := n.UnreadCountKinds(ctx, u.ID, reading, nil); err != nil || c != 1 {
				t.Fatalf("reading unread: %d %v", c, err)
			}
			if c, err := n.UnreadCountKinds(ctx, u.ID, nil, []string{notify.KindJobDone, notify.KindChapter}); err != nil || c != 1 {
				t.Fatalf("system unread: %d %v", c, err)
			}
			if marked, err := n.MarkKindsRead(ctx, u.ID, reading, nil); err != nil || marked != 1 {
				t.Fatalf("mark reading: %d %v", marked, err)
			}
			if c, _ := n.UnreadCount(ctx, u.ID); c != 2 {
				t.Fatalf("other categories changed: %d unread", c)
			}
			if c, _ := n.UnreadCount(ctx, other.ID); c != 1 {
				t.Fatalf("another member's notification changed: %d unread", c)
			}
			if marked, err := n.MarkKindsRead(ctx, u.ID, nil, nil); err != nil || marked != 2 {
				t.Fatalf("mark all: %d %v", marked, err)
			}
		})
	}
}
