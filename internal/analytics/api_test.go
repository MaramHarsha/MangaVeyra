package analytics_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/analytics"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/publish/publishtest"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func batch(events ...analytics.ReadEvent) map[string]any { return map[string]any{"events": events} }

func TestEventsEndpoint(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp, f *publishtest.Fixture) {
		ctx := context.Background()
		s := f.Series(publishtest.SeriesSpec{})
		ch := f.Publish(s, publishtest.PublicationSpec{Pages: 3})
		members := f.Publish(s, publishtest.PublicationSpec{Visibility: publish.VisibilityMembers})
		events := []analytics.ReadEvent{
			{SeriesID: s.ID, PublicationID: ch.ID, Kind: analytics.EventOpen, Page: 1},
			{SeriesID: s.ID, PublicationID: ch.ID, Kind: analytics.EventPage, Page: 1, MS: 4000},
			{SeriesID: s.ID, PublicationID: ch.ID, Kind: analytics.EventPage, Page: 2, MS: 6000},
			{SeriesID: s.ID, PublicationID: members.ID, Kind: analytics.EventOpen, Page: 1}, // not readable anonymously
			{SeriesID: s.ID, PublicationID: ch.ID, Kind: "scroll"},                          // invalid
		}

		c := ta.NewClient()
		resp := c.PostJSON("/api/v1/analytics/events", batch(events...))
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("anonymous batch: %d %s", resp.StatusCode, resp.Body)
		}
		var res struct{ Accepted, Dropped int }
		resp.JSON(t, &res)
		if res.Accepted != 3 || res.Dropped != 2 {
			t.Fatalf("result = %+v, want 3 accepted and 2 dropped", res)
		}
		cookie := resp.Header.Get("Set-Cookie")
		for _, want := range []string{analytics.SessionCookie + "=", "HttpOnly", "SameSite=Lax", "Path=/"} {
			if !strings.Contains(cookie, want) {
				t.Errorf("Set-Cookie %q lacks %q", cookie, want)
			}
		}
		if strings.Contains(strings.ToLower(cookie), "expires") || strings.Contains(strings.ToLower(cookie), "max-age") {
			t.Errorf("the reading session cookie must end with the browser session: %q", cookie)
		}
		// The cookie is "<random id>.<server MAC>"; only the id is hashed.
		raw := strings.TrimPrefix(strings.SplitN(cookie, ";", 2)[0], analytics.SessionCookie+"=")
		id, mac, ok := strings.Cut(raw, ".")
		if !ok || len(id) != 32 || len(mac) != 22 {
			t.Fatalf("session cookie %q is not <32-character id>.<22-character MAC>", raw)
		}
		if n := count(t, ta, `SELECT COUNT(*) FROM read_events WHERE session_hash = ? AND user_id IS NULL`,
			analytics.HashSession(recorderOf(ta), id, ta.Clock.Now())); n != 3 {
			t.Errorf("events stored under the session's daily hash = %d, want 3", n)
		}
		if n := count(t, ta, `SELECT COUNT(*) FROM read_events WHERE session_hash IN (?, ?)`, raw, id); n != 0 {
			t.Error("the raw session cookie value was stored")
		}
		// The same browser keeps its session: no new cookie, one read per day.
		again := c.PostJSON("/api/v1/analytics/events", batch(events[0]))
		if again.StatusCode != http.StatusAccepted || again.Header.Get("Set-Cookie") != "" {
			t.Fatalf("second batch: %d, cookie %q", again.StatusCode, again.Header.Get("Set-Cookie"))
		}
		if n := count(t, ta, `SELECT COUNT(*) FROM read_events WHERE kind = 'open'`); n != 1 {
			t.Errorf("opens = %d, want 1", n)
		}
		// A changed cookie is not that session: the server starts a new one.
		forged := []byte(raw)
		forged[0] = map[bool]byte{true: 'B', false: 'A'}[forged[0] == 'A']
		fc := ta.NewClient()
		fc.Client.Jar = nil
		fc.Header.Set("Cookie", analytics.SessionCookie+"="+string(forged))
		if resp := fc.PostJSON("/api/v1/analytics/events", batch(events[1])); resp.StatusCode != http.StatusAccepted ||
			!strings.HasPrefix(resp.Header.Get("Set-Cookie"), analytics.SessionCookie+"=") {
			t.Fatalf("forged cookie: %d, cookie %q", resp.StatusCode, resp.Header.Get("Set-Cookie"))
		}

		// Members are stored by account and may record members-only reads.
		member, mc := ta.UserClient(t, auth.RoleReader)
		if resp := mc.PostJSON("/api/v1/analytics/events", batch(events[3])); resp.StatusCode != http.StatusAccepted {
			t.Fatalf("member batch: %d %s", resp.StatusCode, resp.Body)
		}
		if n := count(t, ta, `SELECT COUNT(*) FROM read_events WHERE user_id = ? AND session_hash = ''`, member.ID); n != 1 {
			t.Errorf("member events = %d, want 1 without a session hash", n)
		}

		// Batch bounds and malformed bodies.
		many := make([]analytics.ReadEvent, analytics.MaxBatchEvents+1)
		for i := range many {
			many[i] = events[1]
		}
		if resp := c.PostJSON("/api/v1/analytics/events", batch(many...)); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("oversized batch: %d", resp.StatusCode)
		}
		if resp := c.PostJSON("/api/v1/analytics/events", batch()); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("empty batch: %d", resp.StatusCode)
		}
		if resp := c.Do(http.MethodPost, "/api/v1/analytics/events", strings.NewReader(`{"events":`), "application/json"); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("malformed body: %d", resp.StatusCode)
		}
		if resp := c.Do(http.MethodPost, "/api/v1/analytics/events", strings.NewReader(`{"events":[`+strings.Repeat(" ", 40<<10)+`]}`), "application/json"); resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("large body: %d", resp.StatusCode)
		}
		// Cross-site browser posts are refused by the CSRF layer.
		cross := ta.NewClient()
		cross.Header.Set("Sec-Fetch-Site", "cross-site")
		if resp := cross.PostJSON("/api/v1/analytics/events", batch(events[0])); resp.StatusCode != http.StatusForbidden {
			t.Errorf("cross-site post: %d", resp.StatusCode)
		}
		_ = ctx
	})
}

func TestEventsEndpointLimitsNewSessionsPerAddress(t *testing.T) {
	ta := testutil.NewTestApp(t)
	f := publishtest.New(t, ta)
	f.SetMode(settings.ModePublic)
	analytics.SetNewSessionLimit(core.MustUse[analytics.ReadingSessions](ta.Deps), 0.001, 3)
	s := f.Series(publishtest.SeriesSpec{})
	ch := f.Publish(s, publishtest.PublicationSpec{})
	open := batch(analytics.ReadEvent{SeriesID: s.ID, PublicationID: ch.ID, Kind: analytics.EventOpen, Page: 1})
	// A client that never keeps the cookie starts a new reader each time.
	c := ta.NewClient()
	c.Client.Jar = nil
	for i := 1; i <= 3; i++ {
		if resp := c.PostJSON("/api/v1/analytics/events", open); resp.StatusCode != http.StatusAccepted {
			t.Fatalf("request %d: %d", i, resp.StatusCode)
		}
	}
	if resp := c.PostJSON("/api/v1/analytics/events", open); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("4th new session from one address: %d", resp.StatusCode)
	}
	// A session the client made up itself is no session: it cannot get
	// around the limit (only cookies the server minted and signed count).
	for i := range 5 {
		c.Header.Set("Cookie", analytics.SessionCookie+"="+strings.Repeat(string(rune('a'+i)), 32))
		if resp := c.PostJSON("/api/v1/analytics/events", open); resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("self-made session %d: %d", i, resp.StatusCode)
		}
	}
	c.Header.Del("Cookie")
	got, err := f.Service().SeriesByID(context.Background(), publish.Viewer{}, s.ID)
	if err != nil || got.ViewCount != 3 {
		t.Fatalf("view_count = %d, %v; want 3", got.ViewCount, err)
	}
	// Another address, and members, are not affected.
	if resp := ta.NewClient().PostJSON("/api/v1/analytics/events", open); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("other address: %d", resp.StatusCode)
	}
	_, mc := ta.UserClient(t, auth.RoleReader)
	mc.Header.Set("X-Forwarded-For", c.Header.Get("X-Forwarded-For"))
	if resp := mc.PostJSON("/api/v1/analytics/events", open); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("member: %d", resp.StatusCode)
	}
}

func TestEventsEndpointRefusesTokens(t *testing.T) {
	ta := testutil.NewTestApp(t)
	f := publishtest.New(t, ta)
	s := f.Series(publishtest.SeriesSpec{})
	ch := f.Publish(s, publishtest.PublicationSpec{})
	tok, _, err := ta.Deps.Auth.CreateToken(context.Background(), auth.TokenInput{UserID: ta.Admin.ID, Kind: auth.TokenAPI, Name: "reader", Scopes: []string{"projects:read"}})
	if err != nil {
		t.Fatal(err)
	}
	resp := ta.BearerClient(tok).PostJSON("/api/v1/analytics/events", batch(analytics.ReadEvent{SeriesID: s.ID, PublicationID: ch.ID, Kind: analytics.EventOpen}))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bearer token: %d %s", resp.StatusCode, resp.Body)
	}
}
