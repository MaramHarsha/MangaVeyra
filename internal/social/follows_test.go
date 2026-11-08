package social_test

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/social"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestFollowsIsProvided(t *testing.T) {
	ta := testutil.NewTestApp(t)
	if _, ok := core.Use[social.Follows](ta.Deps); !ok {
		t.Fatal("social.Follows is not provided")
	}
}

// Publish notifies followers through publish.FollowerSource (it cannot import
// social), so social must provide its follows under that type too.
func TestFollowersReachPublishNotifications(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		src, ok := core.Use[publish.FollowerSource](e.ta.Deps)
		if !ok {
			t.Fatal("publish.FollowerSource is not provided")
		}
		s := e.f.Series(publishtest.SeriesSpec{})
		e.f.Publish(s, publishtest.PublicationSpec{})
		u, c := e.user(auth.RoleReader)
		wantStatus(t, "follow", c.SendJSON(http.MethodPut, "/api/v1/social/series/"+s.ID+"/follow", nil), http.StatusOK)
		ids, _, err := src.Followers(context.Background(), s.ID, "", 10)
		if err != nil || len(ids) != 1 || ids[0] != u.ID {
			t.Fatalf("FollowerSource.Followers = %v, %v; want [%s]", ids, err, u.ID)
		}
	})
}

func TestFollowUnfollowIsIdempotentAndKeepsFollowerCount(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		ctx := context.Background()
		s := e.f.Series(publishtest.SeriesSpec{FollowerCount: 3})
		e.f.Publish(s, publishtest.PublicationSpec{})
		u, c := e.user(auth.RoleReader)
		path := "/api/v1/social/series/" + s.ID + "/follow"

		for i := range 2 {
			r := c.SendJSON(http.MethodPut, path, nil)
			wantStatus(t, "follow", r, http.StatusOK)
			m := r.Map(t)
			if m["following"] != true || m["notify"] != true || m["follower_count"] != float64(4) {
				t.Fatalf("follow #%d = %v", i, m)
			}
		}
		if got := e.followerCount(s.ID); got != 4 {
			t.Fatalf("follower_count after two follows = %d, want 4", got)
		}
		if ok, err := e.follows().IsFollowing(ctx, u.ID, s.ID); err != nil || !ok {
			t.Fatalf("IsFollowing = %v, %v", ok, err)
		}
		// Turning notifications off keeps the follow and the count.
		r := c.SendJSON(http.MethodPut, path, map[string]any{"notify": false})
		wantStatus(t, "notify off", r, http.StatusOK)
		if m := r.Map(t); m["notify"] != false || m["follower_count"] != float64(4) {
			t.Fatalf("notify off = %v", m)
		}
		ids, _, err := e.follows().Followers(ctx, s.ID, "", 10)
		if err != nil || len(ids) != 0 {
			t.Fatalf("Followers with notify off = %v, %v", ids, err)
		}

		for range 2 {
			wantStatus(t, "unfollow", c.Do(http.MethodDelete, path, nil, ""), http.StatusNoContent)
		}
		if got := e.followerCount(s.ID); got != 3 {
			t.Fatalf("follower_count after two unfollows = %d, want 3", got)
		}
		if ok, _ := e.follows().IsFollowing(ctx, u.ID, s.ID); ok {
			t.Fatal("still following after unfollow")
		}
		r = c.Get(path)
		wantStatus(t, "state", r, http.StatusOK)
		if m := r.Map(t); m["following"] != false {
			t.Fatalf("state = %v", m)
		}
		// The count never goes negative.
		e.exec(`UPDATE series SET follower_count = 0 WHERE id = ?`, s.ID)
		wantStatus(t, "follow", c.SendJSON(http.MethodPut, path, nil), http.StatusOK)
		e.exec(`UPDATE series SET follower_count = 0 WHERE id = ?`, s.ID)
		wantStatus(t, "unfollow", c.Do(http.MethodDelete, path, nil, ""), http.StatusNoContent)
		if got := e.followerCount(s.ID); got != 0 {
			t.Fatalf("follower_count clamped = %d", got)
		}
	})
}

func TestConcurrentFollowsCountOnce(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		s := e.f.Series(publishtest.SeriesSpec{})
		var clients []*testutil.Client
		for range 4 {
			_, c := e.user(auth.RoleReader)
			clients = append(clients, c)
		}
		var wg sync.WaitGroup
		for _, c := range clients {
			for range 3 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					c.SendJSON(http.MethodPut, "/api/v1/social/series/"+s.ID+"/follow", nil)
				}()
			}
		}
		wg.Wait()
		if got, rows := e.followerCount(s.ID), e.count(`SELECT COUNT(*) FROM follows WHERE series_id = ?`, s.ID); got != 4 || rows != 4 {
			t.Fatalf("follower_count = %d, rows = %d, want 4", got, rows)
		}
	})
}

func TestFollowNeedsAccessAndSignIn(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		private := e.f.Series(publishtest.SeriesSpec{Visibility: publish.VisibilityPrivate})
		draft := e.f.Series(publishtest.SeriesSpec{NeverPublished: true, Status: publish.SeriesDraft})
		public := e.f.Series(publishtest.SeriesSpec{})
		_, c := e.user(auth.RoleReader)
		for _, id := range []string{private.ID, draft.ID, "01nosuchseries0000000000000"} {
			r := c.SendJSON(http.MethodPut, "/api/v1/social/series/"+id+"/follow", nil)
			wantStatus(t, "follow unreadable", r, http.StatusNotFound)
		}
		if n := e.count(`SELECT COUNT(*) FROM follows`); n != 0 {
			t.Fatalf("follows written for unreadable series: %d", n)
		}
		anon := e.ta.NewClient()
		wantStatus(t, "anonymous follow", anon.SendJSON(http.MethodPut, "/api/v1/social/series/"+public.ID+"/follow", nil), http.StatusUnauthorized)
		// A follow survives the series becoming private and can still be removed.
		_, c2 := e.user(auth.RoleReader)
		wantStatus(t, "follow", c2.SendJSON(http.MethodPut, "/api/v1/social/series/"+public.ID+"/follow", nil), http.StatusOK)
		e.exec(`UPDATE series SET visibility = 'private' WHERE id = ?`, public.ID)
		wantStatus(t, "unfollow private", c2.Do(http.MethodDelete, "/api/v1/social/series/"+public.ID+"/follow", nil, ""), http.StatusNoContent)
		if n := e.count(`SELECT COUNT(*) FROM follows WHERE series_id = ?`, public.ID); n != 0 {
			t.Fatalf("follow not removed: %d", n)
		}
	})
}

func TestFollowedSeriesIDsPaging(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		ctx := context.Background()
		u, _ := e.user(auth.RoleReader)
		base := db.Now().Add(-time.Hour)
		var want []string
		for i := range 7 {
			s := e.f.Series(publishtest.SeriesSpec{})
			// Two follows share a timestamp so the ID tie-break is exercised.
			at := base.Add(time.Duration(i/2) * time.Minute)
			e.exec(`INSERT INTO follows (user_id, series_id, notify, created_at) VALUES (?, ?, ?, ?)`, u.ID, s.ID, true, at)
			want = append(want, s.ID)
		}
		// Expected order: created_at DESC, series_id DESC.
		type pair struct {
			id string
			at time.Time
		}
		var pairs []pair
		for i, id := range want {
			pairs = append(pairs, pair{id, base.Add(time.Duration(i/2) * time.Minute)})
		}
		slices.SortFunc(pairs, func(a, b pair) int {
			if c := b.at.Compare(a.at); c != 0 {
				return c
			}
			if a.id > b.id {
				return -1
			}
			return 1
		})
		var expected []string
		for _, p := range pairs {
			expected = append(expected, p.id)
		}
		var got []string
		cursor := ""
		for range 10 {
			ids, next, err := e.follows().FollowedSeriesIDs(ctx, u.ID, cursor, 3)
			if err != nil {
				t.Fatalf("FollowedSeriesIDs: %v", err)
			}
			if len(ids) > 3 {
				t.Fatalf("page of %d", len(ids))
			}
			got = append(got, ids...)
			if next == "" {
				break
			}
			cursor = next
		}
		if !slices.Equal(got, expected) {
			t.Fatalf("FollowedSeriesIDs = %v, want %v", got, expected)
		}
		if _, _, err := e.follows().FollowedSeriesIDs(ctx, u.ID, "not-a-cursor", 3); err == nil {
			t.Fatal("bad cursor accepted")
		}
		if ids, next, err := e.follows().FollowedSeriesIDs(ctx, "", "", 3); err != nil || len(ids) != 0 || next != "" {
			t.Fatalf("empty user = %v %q %v", ids, next, err)
		}
	})
}

func TestFollowersPagingSkipsMuted(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		ctx := context.Background()
		s := e.f.Series(publishtest.SeriesSpec{})
		var want []string
		for i := range 6 {
			u := e.f.User(auth.RoleReader)
			notify := i != 2
			e.exec(`INSERT INTO follows (user_id, series_id, notify, created_at) VALUES (?, ?, ?, ?)`, u.ID, s.ID, notify, db.Now())
			if notify {
				want = append(want, u.ID)
			}
		}
		slices.Sort(want)
		var got []string
		cursor := ""
		for range 10 {
			ids, next, err := e.follows().Followers(ctx, s.ID, cursor, 2)
			if err != nil {
				t.Fatalf("Followers: %v", err)
			}
			got = append(got, ids...)
			if next == "" {
				break
			}
			cursor = next
		}
		if !slices.Equal(got, want) {
			t.Fatalf("Followers = %v, want %v", got, want)
		}
	})
}

func TestUserFollows(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		creator := e.f.User(auth.RoleCreator)
		me, c := e.user(auth.RoleReader)
		path := "/api/v1/social/users/" + creator.Username + "/follow"
		for range 2 {
			r := c.SendJSON(http.MethodPut, path, nil)
			wantStatus(t, "follow user", r, http.StatusOK)
			if m := r.Map(t); m["following"] != true || m["follower_count"] != float64(1) {
				t.Fatalf("follow user = %v", m)
			}
		}
		wantStatus(t, "self follow", c.SendJSON(http.MethodPut, "/api/v1/social/users/"+me.Username+"/follow", nil), http.StatusUnprocessableEntity)
		wantStatus(t, "unknown", c.SendJSON(http.MethodPut, "/api/v1/social/users/nobody-here/follow", nil), http.StatusNotFound)
		for range 2 {
			wantStatus(t, "unfollow user", c.Do(http.MethodDelete, path, nil, ""), http.StatusNoContent)
		}
		if n := e.count(`SELECT COUNT(*) FROM user_follows WHERE followee_id = ?`, creator.ID); n != 0 {
			t.Fatalf("user follows after unfollow = %d", n)
		}
		r := c.Get(path)
		wantStatus(t, "user follow state", r, http.StatusOK)
		if m := r.Map(t); m["following"] != false || m["follower_count"] != float64(0) {
			t.Fatalf("state = %v", m)
		}
		wantStatus(t, "follow again", c.SendJSON(http.MethodPut, path, nil), http.StatusOK)
		if n := e.count(`SELECT COUNT(*) FROM user_follows WHERE follower_id = ? AND followee_id = ?`, me.ID, creator.ID); n != 1 {
			t.Fatalf("user follows = %d", n)
		}
		// Suspended accounts cannot be followed.
		if err := e.ta.Deps.Auth.SetStatus(context.Background(), creator.ID, auth.StatusSuspended, "test"); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, "suspended", c.SendJSON(http.MethodPut, path, nil), http.StatusNotFound)
	})
}

func TestFollowWritesAreRateLimited(t *testing.T) {
	ta := testutil.NewTestApp(t)
	e := &env{t: t, ta: ta, f: publishtest.New(t, ta)}
	s := e.f.Series(publishtest.SeriesSpec{})
	_, c := e.user(auth.RoleReader)
	limited := false
	for range 40 {
		r := c.SendJSON(http.MethodPut, "/api/v1/social/series/"+s.ID+"/follow", nil)
		if r.StatusCode == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("40 follow toggles were never rate limited")
	}
}

// Deleting an account removes its follows (ON DELETE CASCADE); the series'
// follower_count must drop with them instead of drifting upward.
func TestDeletedAccountsLeaveFollowerCount(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		s := e.f.Series(publishtest.SeriesSpec{})
		e.f.Publish(s, publishtest.PublicationSpec{})
		gone, goneClient := e.user(auth.RoleReader)
		_, stays := e.user(auth.RoleReader)
		for _, c := range []*testutil.Client{goneClient, stays} {
			wantStatus(t, "follow", c.SendJSON(http.MethodPut, "/api/v1/social/series/"+s.ID+"/follow", nil), http.StatusOK)
		}
		if got := e.followerCount(s.ID); got != 2 {
			t.Fatalf("follower_count = %d, want 2", got)
		}
		if err := e.ta.Deps.Auth.DeleteUser(context.Background(), gone.ID); err != nil {
			t.Fatal(err)
		}
		if got := e.followerCount(s.ID); got != 1 {
			t.Fatalf("follower_count after deleting a follower = %d, want 1", got)
		}
	})
}
