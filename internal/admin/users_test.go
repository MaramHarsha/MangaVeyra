package admin_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

// Invites are created, listed with a copyable link, used and revoked from the
// Users tab, without JavaScript.
func TestInviteLifecycle(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		ctx := context.Background()
		page := e.admin.Get("/admin/users?dialog=invite")
		wantStatus(t, "invite dialog", page, http.StatusOK)
		// Without JavaScript the dialog is rendered open in the page.
		wantBody(t, "invite dialog", page, `id="invite-dialog"`, `mv-modal--static`, `action="/admin/invites"`, `name="expires"`)

		bad := postForm(e.admin, "/admin/invites", url.Values{"role": {"owner"}, "expires": {"7d"}, "uses": {"1"}})
		wantStatus(t, "bad role", bad, http.StatusUnprocessableEntity)
		wantBody(t, "bad role", bad, "Choose a role.", `mv-modal--static`)
		wantStatus(t, "bad email", postForm(e.admin, "/admin/invites", url.Values{"role": {"reader"}, "expires": {"7d"}, "email": {"not-an-email"}}), http.StatusUnprocessableEntity)

		r := postForm(e.admin, "/admin/invites", url.Values{"role": {"reader"}, "expires": {"30d"}, "uses": {"2"}})
		wantStatus(t, "create invite", r, http.StatusSeeOther)
		invites, err := e.ta.Deps.Auth.ListInvites(ctx, false)
		if err != nil || len(invites) != 1 {
			t.Fatalf("invites: %d, %v", len(invites), err)
		}
		inv := invites[0]
		if inv.Role != auth.RoleReader || inv.MaxUses != 2 || inv.ExpiresAt == nil {
			t.Fatalf("invite %+v", inv)
		}
		if e.audits("invite.create", inv.ID) != 1 {
			t.Fatal("invite not audited")
		}
		list := e.admin.Get(r.Header.Get("Location"))
		wantBody(t, "invite listed", list, inv.URL, `data-copy="invite-`+inv.ID, "0 / 2 used", "mv-admin__invite is-current")

		u, err := e.ta.Deps.Auth.Signup(ctx, auth.SignupInput{Username: "tidebreak", Email: "tide@example.org", Password: "correct-horse-battery", InviteCode: inv.Code})
		if err != nil || u.Role != auth.RoleReader {
			t.Fatalf("sign up with invite: %+v, %v", u, err)
		}
		wantSaved(t, "revoke", postForm(e.admin, "/admin/invites/"+inv.ID+"/revoke", nil), "/admin/users")
		if _, err := e.ta.Deps.Auth.Signup(ctx, auth.SignupInput{Username: "tidebreak2", Email: "tide2@example.org", Password: "correct-horse-battery", InviteCode: inv.Code}); !errors.Is(err, auth.ErrInviteInvalid) {
			t.Fatalf("revoked invite still works: %v", err)
		}
		wantStatus(t, "revoke twice", postForm(e.admin, "/admin/invites/"+inv.ID+"/revoke", nil), http.StatusNotFound)
		wantNoBody(t, "revoked invite gone", e.admin.Get("/admin/users"), inv.URL)
	})
}

// Sign-up mode and approval: closed refuses everyone, open with approval puts
// new accounts in the pending list until an admin approves or declines them.
func TestSignupModesAndApprovals(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		ctx := context.Background()
		wantSaved(t, "closed", postForm(e.admin, "/admin/signup", url.Values{"sign_up": {"closed"}}), "/admin/users")
		if e.instance().SignUp != settings.SignUpClosed {
			t.Fatal("sign-up mode not saved")
		}
		if _, err := e.ta.Deps.Auth.Signup(ctx, auth.SignupInput{Username: "glassorchard", Password: "correct-horse-battery"}); !errors.Is(err, auth.ErrSignupClosed) {
			t.Fatalf("closed sign-up: %v", err)
		}
		wantSaved(t, "open with approval", postForm(e.admin, "/admin/signup", url.Values{"sign_up": {"open"}, "require_approval": {"on"}}), "/admin/users")
		inst := e.instance()
		if inst.SignUp != settings.SignUpOpen || !inst.RequireApproval {
			t.Fatalf("settings %+v", inst)
		}
		if e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'admin.settings.signup'`) != 2 {
			t.Fatal("sign-up changes not audited")
		}
		wantStatus(t, "bad mode", postForm(e.admin, "/admin/signup", url.Values{"sign_up": {"everyone"}}), http.StatusBadRequest)

		pending, err := e.ta.Deps.Auth.Signup(ctx, auth.SignupInput{Username: "ninthbell", Password: "correct-horse-battery"})
		if err != nil || pending.Status != auth.StatusPending {
			t.Fatalf("open sign-up with approval: %+v, %v", pending, err)
		}
		declined, err := e.ta.Deps.Auth.Signup(ctx, auth.SignupInput{Username: "papercrows", Password: "correct-horse-battery"})
		if err != nil {
			t.Fatal(err)
		}
		page := e.admin.Get("/admin/users")
		wantBody(t, "pending list", page, "Waiting for approval · 2", "/admin/users/"+pending.ID+"/approve", "/admin/users/"+declined.ID+"/decline")

		wantSaved(t, "approve", postForm(e.admin, "/admin/users/"+pending.ID+"/approve", nil), "/admin/users")
		if u, _ := e.ta.Deps.Auth.UserByID(ctx, pending.ID); u.Status != auth.StatusActive {
			t.Fatalf("approved status %q", u.Status)
		}
		wantSaved(t, "decline", postForm(e.admin, "/admin/users/"+declined.ID+"/decline", nil), "/admin/users")
		if _, err := e.ta.Deps.Auth.UserByID(ctx, declined.ID); !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("declined account still exists: %v", err)
		}
		// Only pending sign-ups can be declined.
		wantStatus(t, "decline active", postForm(e.admin, "/admin/users/"+pending.ID+"/decline", nil), http.StatusConflict)
	})
}

// Site roles, suspension and deletion keep at least one active admin, and the
// acting admin cannot suspend or delete themselves.
func TestRolesAndLastAdmin(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		ctx := context.Background()
		admin := e.ta.Admin
		r := postForm(e.admin, "/admin/users/"+admin.ID+"/role", url.Values{"role": {"creator"}})
		wantStatus(t, "demote the last admin", r, http.StatusConflict)
		wantBody(t, "demote the last admin", r, "The last admin cannot be demoted, suspended or deleted.")
		if u, _ := e.ta.Deps.Auth.UserByID(ctx, admin.ID); u.Role != auth.RoleAdmin {
			t.Fatal("the last admin was demoted")
		}

		member, _ := e.user(auth.RoleReader)
		wantSaved(t, "promote", postForm(e.admin, "/admin/users/"+member.ID+"/role", url.Values{"role": {"creator"}}), "/admin/users")
		if u, _ := e.ta.Deps.Auth.UserByID(ctx, member.ID); u.Role != auth.RoleCreator {
			t.Fatal("role not changed")
		}
		if e.audits("user.set_role", member.ID) != 1 {
			t.Fatal("role change not audited")
		}
		wantStatus(t, "bad role", postForm(e.admin, "/admin/users/"+member.ID+"/role", url.Values{"role": {"owner"}}), http.StatusConflict)

		wantStatus(t, "suspend self", postForm(e.admin, "/admin/users/"+admin.ID+"/suspend", nil), http.StatusConflict)
		wantStatus(t, "delete self", postForm(e.admin, "/admin/users/"+admin.ID+"/delete", url.Values{"confirm": {admin.Username}}), http.StatusConflict)

		wantSaved(t, "suspend", postForm(e.admin, "/admin/users/"+member.ID+"/suspend", nil), "/admin/users")
		if u, _ := e.ta.Deps.Auth.UserByID(ctx, member.ID); u.Status != auth.StatusSuspended {
			t.Fatal("not suspended")
		}
		wantBody(t, "suspended badge", e.admin.Get("/admin/users"), "Suspended", "/admin/users/"+member.ID+"/reactivate")
		wantSaved(t, "reactivate", postForm(e.admin, "/admin/users/"+member.ID+"/reactivate", nil), "/admin/users")
		if u, _ := e.ta.Deps.Auth.UserByID(ctx, member.ID); u.Status != auth.StatusActive {
			t.Fatal("not reactivated")
		}

		// A second admin may be demoted; then the first is the last again.
		second := e.ta.CreateUser(t, "", auth.RoleAdmin)
		wantSaved(t, "demote second admin", postForm(e.admin, "/admin/users/"+second.ID+"/role", url.Values{"role": {"reader"}}), "/admin/users")

		// Deleting asks for the username first (no JavaScript needed).
		confirm := e.admin.Get("/admin/users?delete=" + member.ID)
		wantBody(t, "delete dialog", confirm, `id="delete-dialog"`, "/admin/users/"+member.ID+"/delete", "@"+member.Username)
		wrong := postForm(e.admin, "/admin/users/"+member.ID+"/delete", url.Values{"confirm": {"someone-else"}})
		wantStatus(t, "wrong confirmation", wrong, http.StatusUnprocessableEntity)
		wantBody(t, "wrong confirmation", wrong, "Type the username exactly")
		wantSaved(t, "delete", postForm(e.admin, "/admin/users/"+member.ID+"/delete", url.Values{"confirm": {"@" + strings.ToUpper(member.Username)}}), "/admin/users")
		if _, err := e.ta.Deps.Auth.UserByID(ctx, member.ID); !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("deleted account still exists: %v", err)
		}
		if e.audits("user.delete", member.ID) != 1 {
			t.Fatal("deletion not audited")
		}
	})
}

// The reset-password link is shown once in the page and works.
func TestResetLink(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		member, _ := e.user(auth.RoleCreator)
		r := postForm(e.admin, "/admin/users/"+member.ID+"/reset-link", nil)
		wantStatus(t, "reset link", r, http.StatusOK)
		m := regexp.MustCompile(`/reset/([A-Za-z0-9_-]+)`).FindStringSubmatch(string(r.Body))
		if m == nil {
			t.Fatalf("no reset link in %s", truncate(string(r.Body)))
		}
		u, err := e.ta.Deps.Auth.CheckResetToken(context.Background(), m[1])
		if err != nil || u.ID != member.ID {
			t.Fatalf("reset token: %+v, %v", u, err)
		}
		if e.audits("user.reset_link", member.ID) != 1 {
			t.Fatal("reset link not audited")
		}
	})
}

// Members show their role, status, jobs, storage, projects and last visit.
func TestMembersList(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		member, _ := e.user(auth.RoleCreator)
		e.exec(`INSERT INTO assets (id, key, bytes, owner_id) VALUES ('01m6yf00000000000000000a02', 'k2', 1460000000, ?)`, member.ID)
		page := e.admin.Get("/admin/users?q=" + url.QueryEscape(member.Username))
		wantStatus(t, "members", page, http.StatusOK)
		wantBody(t, "members", page, member.Name(), "0 jobs · 1.5 GB", "0 projects · seen just now", "Active", `value="creator" selected`)
		wantNoBody(t, "search", page, "/admin/users/"+e.ta.Admin.ID+"/role")
	})
}

// Limits the server enforces are saved; bad values are refused with a message.
func TestLimits(t *testing.T) {
	forEachDialect(t, func(t *testing.T, e *env) {
		r := postForm(e.admin, "/admin/limits", url.Values{"running_gpu_jobs": {"3"}, "running_jobs": {"5"}, "monthly_spend": {"7.5"}, "server_spend": {"120"}})
		wantSaved(t, "limits", r, "/admin/users")
		l := e.instance().Limits
		if l.RunningGPUJobs != 3 || l.RunningJobs != 5 || l.MonthlyRemoteSpendUSD != 7.5 || l.ServerMonthlyRemoteSpendUSD != 120 {
			t.Fatalf("limits %+v", l)
		}
		bad := postForm(e.admin, "/admin/limits", url.Values{"running_gpu_jobs": {"0"}, "running_jobs": {"5"}, "monthly_spend": {"-1"}})
		wantStatus(t, "bad limits", bad, http.StatusUnprocessableEntity)
		wantBody(t, "bad limits", bad, "Use a whole number from 1 to 64.", "Use an amount of $0 or more.")
		if e.instance().Limits.RunningGPUJobs != 3 {
			t.Fatal("bad limits were saved")
		}
		if e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'admin.settings.limits'`) != 1 {
			t.Fatal("limits not audited once")
		}
	})
}
