package admin_test

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

var adminPages = []string{"/admin", "/admin/reports", "/admin/disputes", "/admin/users", "/admin/rules", "/admin/server"}

// adminPosts are every state-changing admin route with a form that would
// change something if it were accepted.
func adminPosts(userID, reportID, inviteID string) map[string]url.Values {
	return map[string]url.Values{
		"/admin/reports/" + reportID + "/resolve": {"action": {"dismiss"}},
		"/admin/takedowns/restore":                {"key": {"admin.takedown.series.x"}},
		"/admin/users/" + userID + "/role":        {"role": {"admin"}},
		"/admin/users/" + userID + "/suspend":     {},
		"/admin/users/" + userID + "/reactivate":  {},
		"/admin/users/" + userID + "/approve":     {},
		"/admin/users/" + userID + "/decline":     {},
		"/admin/users/" + userID + "/reset-link":  {},
		"/admin/users/" + userID + "/delete":      {"confirm": {"x"}},
		"/admin/signup":                           {"sign_up": {"open"}},
		"/admin/limits":                           {"running_gpu_jobs": {"9"}, "running_jobs": {"9"}},
		"/admin/invites":                          {"role": {"admin"}, "expires": {"7d"}},
		"/admin/invites/" + inviteID + "/revoke":  {},
		"/admin/rules":                            {"part": {"content"}, "allow_adult": {"on"}},
		"/admin/rules/words":                      {"words": {"gloomweed"}},
		"/admin/server":                           {"name": {"Taken"}, "mode": {"public"}},
		"/admin/maintenance":                      {"enabled": {"1"}, "message": {"x"}},
	}
}

// Only active site admins with a browser session reach G5; everyone else gets
// 404 for every page and every change, and an admin's bearer token is refused.
func TestAdminOnly(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		ctx := context.Background()
		before := e.instance()
		reader, readerC := e.user(auth.RoleReader)
		_, creatorC := e.user(auth.RoleCreator)
		reportID := e.report(reader, "user", e.ta.Admin.ID, "spam", "")
		inv, err := e.ta.Deps.Auth.CreateInvite(ctx, e.ta.Admin.ID, auth.InviteInput{Role: auth.RoleReader})
		if err != nil {
			t.Fatal(err)
		}
		adminToken, _, err := e.ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: e.ta.Admin.ID, Kind: auth.TokenAPI, Name: "admin api",
			Scopes: []string{"*"}})
		if err != nil {
			t.Fatal(err)
		}
		readerToken, _, err := e.ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: reader.ID, Kind: auth.TokenAPI, Name: "reader api"})
		if err != nil {
			t.Fatal(err)
		}
		others := map[string]*testutil.Client{"anonymous": e.ta.NewClient(), "reader": readerC, "creator": creatorC,
			"reader token": e.ta.BearerClient(readerToken)}
		for who, c := range others {
			for _, p := range adminPages {
				wantStatus(t, who+" GET "+p, c.Get(p), http.StatusNotFound)
			}
			wantStatus(t, who+" GET review", c.Get("/admin/reports/"+reportID), http.StatusNotFound)
			for p, form := range adminPosts(reader.ID, reportID, inv.ID) {
				wantStatus(t, who+" POST "+p, postForm(c, p, form), http.StatusNotFound)
			}
		}
		token := e.ta.BearerClient(adminToken)
		for _, p := range adminPages {
			wantStatus(t, "admin token GET "+p, token.Get(p), http.StatusForbidden)
		}
		for p, form := range adminPosts(reader.ID, reportID, inv.ID) {
			wantStatus(t, "admin token POST "+p, postForm(token, p, form), http.StatusForbidden)
		}

		// Nothing changed.
		if after := e.instance(); !reflect.DeepEqual(before, after) {
			t.Fatalf("a refused request changed the settings:\n%+v\n%+v", before, after)
		}
		if got := e.reportState(reportID).Status; got != "open" {
			t.Fatalf("report status %q after refused requests", got)
		}
		if u, _ := e.ta.Deps.Auth.UserByID(ctx, reader.ID); u.Role != auth.RoleReader || u.Status != auth.StatusActive {
			t.Fatalf("reader changed: %+v", u)
		}

		// The admin's own session sees every tab.
		for _, p := range adminPages {
			r := e.admin.Get(p)
			wantStatus(t, "admin GET "+p, r, http.StatusOK)
			wantBody(t, "admin GET "+p, r, "Admin · ", `href="/admin/users"`, `href="/admin/rules"`)
			if r.Header.Get("Cache-Control") != "no-store" {
				t.Fatalf("%s: Cache-Control %q", p, r.Header.Get("Cache-Control"))
			}
		}

		// A suspended admin is a stranger too.
		other := e.ta.CreateUser(t, "", auth.RoleAdmin)
		otherC := e.ta.Login(t, other.Username)
		if err := e.ta.Deps.Auth.SetStatus(ctx, other.ID, auth.StatusSuspended, ""); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, "suspended admin", otherC.Get("/admin"), http.StatusNotFound)
	})
}

// The header shows real numbers: open reports, active members, storage from
// the assets table, and the sign-up mode; G5's update action is Soon until
// /admin/update exists.
func TestHeaderAndStats(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		reader, _ := e.user(auth.RoleReader)
		e.report(reader, "user", e.ta.Admin.ID, "spam", "")
		e.report(reader, "user", e.ta.Admin.ID, "harassment", "")
		e.exec(`INSERT INTO assets (id, key, bytes, owner_id) VALUES ('01m6yf00000000000000000a01', 'k1', 2500000000, ?)`, reader.ID)
		r := e.admin.Get("/admin")
		wantStatus(t, "reports tab", r, http.StatusOK)
		// 3 active members: the admin, the reader and nobody else.
		wantBody(t, "stats", r, "Public · 2 members · v", ">Reports 2<", "Open reports", "2.5 GB", "Invite only")
		// Up to date: with the update module providing adminapi.UpdateNotice, the header offers no update.
		if strings.Contains(string(r.Body), "Update to ") {
			t.Fatal("an up-to-date server must not offer an update in the G5 header")
		}
		wantNoBody(t, "stats", r, "AI-label disputes 1")
	})
}
