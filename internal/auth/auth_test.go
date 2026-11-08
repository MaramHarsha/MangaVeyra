package auth_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

func forEach(t *testing.T, fn func(t *testing.T, ta *testutil.TestApp)) {
	for _, d := range testdb.Dialects() {
		t.Run(d, func(t *testing.T) { fn(t, testutil.NewTestApp(t, testutil.WithDialect(d))) })
	}
}

func TestPasswordHashing(t *testing.T) {
	h, err := auth.HashPassword("a very long password", auth.FastHashParams)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=1024,t=1,p=1$") {
		t.Fatalf("unexpected encoding %s", h)
	}
	if !auth.VerifyPassword("a very long password", h) || auth.VerifyPassword("a very long passworD", h) {
		t.Fatal("verify")
	}
	if !auth.NeedsRehash(h, auth.DefaultHashParams) || auth.NeedsRehash(h, auth.FastHashParams) {
		t.Fatal("rehash detection")
	}
	if auth.VerifyPassword("x", "$argon2id$v=19$m=999999999,t=1,p=1$AAAA$AAAA") {
		t.Fatal("tampered params accepted")
	}
	if err := auth.CheckPasswordPolicy("short"); err == nil {
		t.Fatal("short password accepted")
	}
}

func TestProjectRoles(t *testing.T) {
	cases := []struct {
		have, need string
		ok         bool
	}{
		{"owner", "editor", true}, {"editor", "artist", true}, {"artist", "artist", true}, {"artist", "writer", false},
		{"letterer", "viewer", true}, {"viewer", "letterer", false}, {"editor", "owner", false}, {"", "viewer", false},
	}
	for _, c := range cases {
		if got := auth.ProjectRoleAllows(c.have, c.need); got != c.ok {
			t.Errorf("%s ⊇ %s = %v, want %v", c.have, c.need, got, c.ok)
		}
	}
}

func TestLoginSessionsAndLogout(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ta.CreateUser(t, "rin", auth.RoleCreator)
		c := ta.NewClient()
		// wrong password
		resp := c.PostJSON("/api/v1/auth/login", map[string]any{"identifier": "rin", "password": "nope-nope-nope"})
		if resp.StatusCode != 401 || resp.ErrorCode() != "invalid_credentials" {
			t.Fatalf("bad login: %d %s", resp.StatusCode, resp.Body)
		}
		// unknown user gives the same answer
		resp = c.PostJSON("/api/v1/auth/login", map[string]any{"identifier": "ghost", "password": "nope-nope-nope"})
		if resp.StatusCode != 401 || resp.ErrorCode() != "invalid_credentials" {
			t.Fatalf("unknown login: %d %s", resp.StatusCode, resp.Body)
		}
		// login by email
		resp = c.PostJSON("/api/v1/auth/login", map[string]any{"identifier": "RIN@example.org", "password": testutil.TestPassword, "remember": true})
		if resp.StatusCode != 200 {
			t.Fatalf("login: %d %s", resp.StatusCode, resp.Body)
		}
		cookie := resp.Header.Get("Set-Cookie")
		if !strings.Contains(cookie, "mv_session=") || !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "SameSite=Lax") || !strings.Contains(cookie, "Max-Age=2592000") {
			t.Fatalf("cookie attributes: %s", cookie)
		}
		me := c.Get("/api/v1/auth/me").Map(t)
		if me["user"].(map[string]any)["username"] != "rin" {
			t.Fatalf("me: %v", me)
		}
		// second session, list + revoke others
		c2 := ta.Login(t, "rin")
		var list struct {
			Items []auth.Session `json:"items"`
		}
		c2.Get("/api/v1/auth/sessions").JSON(t, &list)
		if len(list.Items) != 2 {
			t.Fatalf("sessions: %+v", list)
		}
		current := 0
		for _, s := range list.Items {
			if s.Current {
				current++
			}
		}
		if current != 1 {
			t.Fatal("exactly one session must be current")
		}
		if r := c2.PostJSON("/api/v1/auth/sessions/revoke-others", nil); r.StatusCode != 200 {
			t.Fatal(r.StatusCode)
		}
		if r := c.Get("/api/v1/auth/me"); r.StatusCode != 401 {
			t.Fatalf("revoked session still works: %d", r.StatusCode)
		}
		// logout
		c2.PostJSON("/api/v1/auth/logout", nil)
		if r := c2.Get("/api/v1/auth/me"); r.StatusCode != 401 {
			t.Fatalf("after logout: %d", r.StatusCode)
		}
	})
}

func TestSuspendedUserCannotLogIn(t *testing.T) {
	ta := testutil.NewTestApp(t)
	u, c := ta.UserClient(t, auth.RoleCreator)
	ctx := context.Background()
	if err := ta.Deps.Auth.SetStatus(ctx, u.ID, auth.StatusSuspended, "spam"); err != nil {
		t.Fatal(err)
	}
	if r := c.Get("/api/v1/auth/me"); r.StatusCode != 401 {
		t.Fatalf("suspended session still valid: %d", r.StatusCode)
	}
	_, err := ta.Deps.Auth.Login(ctx, auth.LoginInput{Identifier: u.Username, Password: testutil.TestPassword, IP: "1.2.3.4"})
	if !errors.Is(err, auth.ErrSuspended) {
		t.Fatalf("got %v", err)
	}
	// the last admin cannot be demoted or suspended
	if err := ta.Deps.Auth.SetRole(ctx, ta.Admin.ID, auth.RoleReader); !errors.Is(err, auth.ErrLastAdmin) {
		t.Fatalf("last admin demoted: %v", err)
	}
	if err := ta.Deps.Auth.SetStatus(ctx, ta.Admin.ID, auth.StatusSuspended, ""); !errors.Is(err, auth.ErrLastAdmin) {
		t.Fatalf("last admin suspended: %v", err)
	}
}

func TestSignupModesAndInvites(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		st := ta.Deps.Settings
		setMode := func(mode string, approval bool) {
			if _, err := st.UpdateInstance(ctx, "", func(i *settings.Instance) error { i.SignUp = mode; i.RequireApproval = approval; return nil }); err != nil {
				t.Fatal(err)
			}
		}
		c := ta.NewClient()
		body := func(u, invite string) map[string]any {
			return map[string]any{"username": u, "email": u + "@example.org", "password": "long enough password", "invite": invite}
		}
		// default is invite-only
		if r := c.PostJSON("/api/v1/auth/signup", body("kaito", "")); r.ErrorCode() != "invite_required" {
			t.Fatalf("invite mode without invite: %s", r.Body)
		}
		inv, err := ta.Deps.Auth.CreateInvite(ctx, ta.Admin.ID, auth.InviteInput{Role: auth.RoleReader, MaxUses: 1})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(inv.URL, "/signup?invite="+inv.Code) {
			t.Fatalf("invite url %s", inv.URL)
		}
		// public invite check
		if r := c.Get("/api/v1/auth/invites/" + inv.Code); r.StatusCode != 200 {
			t.Fatalf("invite check %d", r.StatusCode)
		}
		r := c.PostJSON("/api/v1/auth/signup", body("kaito", strings.ToUpper(inv.Code)))
		if r.StatusCode != 201 {
			t.Fatalf("signup with invite: %d %s", r.StatusCode, r.Body)
		}
		var out struct{ User auth.User }
		r.JSON(t, &out)
		if out.User.Role != auth.RoleReader || out.User.Status != auth.StatusActive {
			t.Fatalf("invite role not applied: %+v", out.User)
		}
		if me := c.Get("/api/v1/auth/me"); me.StatusCode != 200 {
			t.Fatal("signup should sign in")
		}
		// invite used up
		if r := ta.NewClient().PostJSON("/api/v1/auth/signup", body("oldwren", inv.Code)); r.ErrorCode() != "invite_invalid" {
			t.Fatalf("reused invite: %s", r.Body)
		}
		// email-restricted invite
		inv2, _ := ta.Deps.Auth.CreateInvite(ctx, ta.Admin.ID, auth.InviteInput{Email: "sora@example.org"})
		if r := ta.NewClient().PostJSON("/api/v1/auth/signup", body("pip", inv2.Code)); r.ErrorCode() != "invite_invalid" {
			t.Fatalf("email restriction ignored: %s", r.Body)
		}
		// revoked invite
		if err := ta.Deps.Auth.RevokeInvite(ctx, inv2.ID); err != nil {
			t.Fatal(err)
		}
		if r := ta.NewClient().PostJSON("/api/v1/auth/signup", body("sora", inv2.Code)); r.ErrorCode() != "invite_invalid" {
			t.Fatalf("revoked invite accepted: %s", r.Body)
		}
		// closed
		setMode(settings.SignUpClosed, false)
		if r := ta.NewClient().PostJSON("/api/v1/auth/signup", body("sora", "")); r.ErrorCode() != "signup_closed" {
			t.Fatalf("closed: %s", r.Body)
		}
		// open with approval → pending, cannot log in
		setMode(settings.SignUpOpen, true)
		r = ta.NewClient().PostJSON("/api/v1/auth/signup", body("sora", ""))
		if r.StatusCode != 201 {
			t.Fatalf("open signup: %s", r.Body)
		}
		r.JSON(t, &out)
		if out.User.Status != auth.StatusPending || out.User.Role != auth.RoleCreator {
			t.Fatalf("pending: %+v", out.User)
		}
		if _, err := ta.Deps.Auth.Login(ctx, auth.LoginInput{Identifier: "sora", Password: "long enough password", IP: "9.9.9.9"}); !errors.Is(err, auth.ErrPending) {
			t.Fatalf("pending login: %v", err)
		}
		// validation errors come back per field
		setMode(settings.SignUpOpen, false)
		r = ta.NewClient().PostJSON("/api/v1/auth/signup", map[string]any{"username": "kaito", "email": "k2@example.org", "password": "long enough password"})
		if r.StatusCode != 422 || !strings.Contains(string(r.Body), "username") {
			t.Fatalf("duplicate username: %d %s", r.StatusCode, r.Body)
		}
		r = ta.NewClient().PostJSON("/api/v1/auth/signup", map[string]any{"username": "settings", "password": "long enough password"})
		if r.StatusCode != 422 {
			t.Fatalf("reserved username accepted: %s", r.Body)
		}
		r = ta.NewClient().PostJSON("/api/v1/auth/signup", map[string]any{"username": "valid_name", "password": "short"})
		if r.StatusCode != 422 || !strings.Contains(string(r.Body), "10") {
			t.Fatalf("short password: %s", r.Body)
		}
	})
}

func TestSignupNeedsSetupFirst(t *testing.T) {
	ta := testutil.NewTestApp(t, testutil.WithoutAdmin())
	_, err := ta.Deps.Auth.Signup(context.Background(), auth.SignupInput{Username: "early", Password: "long enough password"})
	if !errors.Is(err, auth.ErrSetupRequired) {
		t.Fatalf("got %v", err)
	}
}

func TestTOTPEnrolmentLoginAndRecovery(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		u, c := ta.UserClient(t, auth.RoleCreator)
		var enrol auth.TOTPEnrollment
		c.PostJSON("/api/v1/auth/totp/begin", nil).JSON(t, &enrol)
		if enrol.Secret == "" || !strings.HasPrefix(enrol.OTPAuthURL, "otpauth://totp/") {
			t.Fatalf("enrolment %+v", enrol)
		}
		if r := c.PostJSON("/api/v1/auth/totp/confirm", map[string]string{"code": "000000"}); r.StatusCode != 401 {
			t.Fatalf("bad confirm code accepted: %d", r.StatusCode)
		}
		clock := testutil.NewClock(time.Now())
		ta.Deps.Auth.Now = clock.Now
		code, _ := totp.GenerateCode(enrol.Secret, clock.Now())
		var conf struct {
			RecoveryCodes []string `json:"recovery_codes"`
		}
		c.PostJSON("/api/v1/auth/totp/confirm", map[string]string{"code": code}).JSON(t, &conf)
		if len(conf.RecoveryCodes) != 10 {
			t.Fatalf("recovery codes %v", conf.RecoveryCodes)
		}
		login := func(code string) error {
			_, err := ta.Deps.Auth.Login(ctx, auth.LoginInput{Identifier: u.Username, Password: testutil.TestPassword, TOTPCode: code, IP: "5.5.5." + code[:1]})
			return err
		}
		if err := login(" "); !errors.Is(err, auth.ErrTOTPRequired) {
			t.Fatalf("missing code: %v", err)
		}
		// the code used for confirmation cannot be replayed
		if err := login(code); !errors.Is(err, auth.ErrTOTPInvalid) {
			t.Fatalf("replayed code: %v", err)
		}
		clock.Advance(30 * time.Second)
		next, _ := totp.GenerateCode(enrol.Secret, clock.Now())
		if err := login(next); err != nil {
			t.Fatalf("fresh code: %v", err)
		}
		if err := login(next); !errors.Is(err, auth.ErrTOTPInvalid) {
			t.Fatalf("same code twice: %v", err)
		}
		// recovery codes work once, case/format-insensitively
		rc := strings.ToUpper(strings.ReplaceAll(conf.RecoveryCodes[0], "-", ""))
		if err := login(rc); err != nil {
			t.Fatalf("recovery code: %v", err)
		}
		if err := login(rc); !errors.Is(err, auth.ErrTOTPInvalid) {
			t.Fatalf("recovery code reused: %v", err)
		}
		// disable needs the password
		if r := c.PostJSON("/api/v1/auth/totp/disable", map[string]string{"password": "wrong password!"}); r.StatusCode != 403 {
			t.Fatalf("disable with wrong password: %d", r.StatusCode)
		}
		if r := c.PostJSON("/api/v1/auth/totp/disable", map[string]string{"password": testutil.TestPassword}); r.StatusCode != 200 {
			t.Fatalf("disable: %d %s", r.StatusCode, r.Body)
		}
		if err := login(" "); err != nil {
			t.Fatalf("login after disable: %v", err)
		}
	})
}

func TestPasswordResetFlows(t *testing.T) {
	ta := testutil.NewTestApp(t)
	ctx := context.Background()
	u, c := ta.UserClient(t, auth.RoleCreator)
	// no SMTP configured → explicit error, admin reset link instead
	if r := ta.NewClient().PostJSON("/api/v1/auth/password/forgot", map[string]string{"email": u.Email}); r.ErrorCode() != "email_not_configured" {
		t.Fatalf("forgot without smtp: %s", r.Body)
	}
	admin := ta.Login(t, "admin")
	var link struct{ Link string }
	admin.PostJSON("/api/v1/auth/users/"+u.ID+"/reset-link", nil).JSON(t, &link)
	i := strings.Index(link.Link, "/reset/")
	if i < 0 {
		t.Fatalf("link %q", link.Link)
	}
	token := link.Link[i+len("/reset/"):]
	if r := ta.NewClient().Get("/api/v1/auth/password/reset/" + token); r.StatusCode != 200 {
		t.Fatalf("check token %d", r.StatusCode)
	}
	if r := ta.NewClient().PostJSON("/api/v1/auth/password/reset", map[string]string{"token": token, "password": "brand new password"}); r.StatusCode != 200 {
		t.Fatalf("reset %d %s", r.StatusCode, r.Body)
	}
	if r := ta.NewClient().PostJSON("/api/v1/auth/password/reset", map[string]string{"token": token, "password": "another new password"}); r.ErrorCode() != "reset_invalid" {
		t.Fatalf("token reused: %s", r.Body)
	}
	if r := c.Get("/api/v1/auth/me"); r.StatusCode != 401 {
		t.Fatal("reset must sign out existing sessions")
	}
	if _, err := ta.Deps.Auth.Login(ctx, auth.LoginInput{Identifier: u.Username, Password: "brand new password", IP: "7.7.7.7"}); err != nil {
		t.Fatal(err)
	}
	// change password keeps only the current session
	c = ta.NewClient()
	c.PostJSON("/api/v1/auth/login", map[string]any{"identifier": u.Username, "password": "brand new password"})
	if r := c.PostJSON("/api/v1/auth/password/change", map[string]string{"current": "brand new password", "password": "third new password"}); r.StatusCode != 200 {
		t.Fatalf("change: %s", r.Body)
	}
	if r := c.Get("/api/v1/auth/me"); r.StatusCode != 200 {
		t.Fatal("current session must survive a password change")
	}
}

func TestBearerTokens(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		u, c := ta.UserClient(t, auth.RoleCreator)
		var created struct {
			Token string         `json:"token"`
			Info  auth.TokenInfo `json:"info"`
		}
		r := c.PostJSON("/api/v1/auth/tokens", map[string]any{"kind": "agent", "name": "Script helper", "scopes": []string{"projects:read"}, "approve_over_usd": 0.5})
		if r.StatusCode != 201 {
			t.Fatalf("create token: %s", r.Body)
		}
		r.JSON(t, &created)
		if !strings.HasPrefix(created.Token, "mva_") || created.Info.Prefix != created.Token[:10] {
			t.Fatalf("token %q prefix %q", created.Token, created.Info.Prefix)
		}
		bc := ta.BearerClient(created.Token)
		// Agent tokens work only through MCP (ADR 0013 §7): every other API refuses them.
		if r := bc.Get("/api/v1/auth/me"); r.StatusCode != 403 || !strings.Contains(string(r.Body), "agent_token_mcp_only") {
			t.Fatalf("agent token outside /mcp = %d %s", r.StatusCode, r.Body)
		}
		if r := bc.PostJSON("/api/v1/auth/tokens", map[string]any{"name": "x"}); r.StatusCode != 403 {
			t.Fatalf("agent created a token: %d", r.StatusCode)
		}
		// API tokens identify their owner on the REST API.
		apiRaw, _, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: u.ID, Kind: auth.TokenAPI, Name: "script", Scopes: []string{"projects:read"}})
		if err != nil {
			t.Fatal(err)
		}
		me := ta.BearerClient(apiRaw).Get("/api/v1/auth/me").Map(t)
		if me["user"].(map[string]any)["id"] != u.ID || me["token"].(map[string]any)["kind"] != "api" {
			t.Fatalf("bearer identity: %v", me)
		}
		// a bad bearer token is rejected outright
		if r := ta.BearerClient("mva_nottherightone").Get("/api/v1/auth/me"); r.StatusCode != 401 {
			t.Fatal(r.StatusCode)
		}
		// non-admins cannot mint worker tokens
		if r := c.PostJSON("/api/v1/auth/tokens", map[string]any{"kind": "worker", "name": "gpu"}); r.StatusCode != 403 {
			t.Fatalf("creator made worker token: %d", r.StatusCode)
		}
		// revoke
		if r := c.Do(http.MethodDelete, "/api/v1/auth/tokens/"+created.Info.ID, nil, ""); r.StatusCode != 200 {
			t.Fatal(r.StatusCode)
		}
		if r := bc.Get("/api/v1/auth/me"); r.StatusCode != 401 {
			t.Fatal("revoked token still works")
		}
		// expired tokens are rejected
		past := time.Now().Add(-time.Minute)
		raw, _, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: u.ID, Kind: auth.TokenAPI, Name: "old", ExpiresAt: &past})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := ta.Deps.Auth.VerifyToken(ctx, raw, ""); !errors.Is(err, auth.ErrTokenInvalid) {
			t.Fatalf("expired token: %v", err)
		}
		// worker tokens carry no user
		wraw, _, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: ta.Admin.ID, Kind: auth.TokenWorker, Name: "studio-pc"})
		if err != nil {
			t.Fatal(err)
		}
		tok, wu, err := ta.Deps.Auth.VerifyToken(ctx, wraw, "")
		if err != nil || wu != nil || tok.Kind != "worker" || !strings.HasPrefix(wraw, "mvw_") {
			t.Fatalf("worker token: %v %v %v", tok, wu, err)
		}
		if r := ta.BearerClient(wraw).Get("/api/v1/auth/me"); r.StatusCode != 401 {
			t.Fatalf("worker token acted as a user: %d", r.StatusCode)
		}
	})
}

func TestSharedWorkerToken(t *testing.T) {
	ta := testutil.NewTestApp(t, testutil.WithEnv("MV_WORKER_TOKEN", "compose-shared-secret-123"))
	tok, u, err := ta.Deps.Auth.VerifyToken(context.Background(), "compose-shared-secret-123", "")
	if err != nil || u != nil || tok.Kind != "worker" || tok.ID != auth.EnvWorkerTokenID {
		t.Fatalf("%+v %v %v", tok, u, err)
	}
}

func TestLoginRateLimit(t *testing.T) {
	ta := testutil.NewTestApp(t)
	ctx := context.Background()
	u := ta.CreateUser(t, "target", auth.RoleCreator)
	var last error
	for i := 0; i < 8; i++ {
		_, last = ta.Deps.Auth.Login(ctx, auth.LoginInput{Identifier: u.Username, Password: "wrong password " + string(rune('a'+i)), IP: "203.0.113.7"})
	}
	if last == nil || !strings.Contains(last.Error(), "rate_limited") {
		t.Fatalf("expected rate limit after repeated failures, got %v", last)
	}
	// even the right password is refused while limited
	if _, err := ta.Deps.Auth.Login(ctx, auth.LoginInput{Identifier: u.Username, Password: testutil.TestPassword, IP: "203.0.113.7"}); err == nil || !strings.Contains(err.Error(), "rate_limited") {
		t.Fatalf("limited login succeeded: %v", err)
	}
	// the HTTP layer returns 429 with Retry-After
	c := ta.NewClient()
	var resp *testutil.Response
	for i := 0; i < 25; i++ {
		resp = c.PostJSON("/api/v1/auth/login", map[string]any{"identifier": "nobody" + string(rune('a'+i)), "password": "whatever-whatever"})
		if resp.StatusCode == 429 {
			break
		}
	}
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("no 429: %d %v", resp.StatusCode, resp.Header)
	}
}

func TestAuditLog(t *testing.T) {
	ta := testutil.NewTestApp(t)
	u, _ := ta.UserClient(t, auth.RoleCreator)
	admin := ta.Login(t, "admin")
	if r := admin.PostJSON("/api/v1/auth/users/"+u.ID+"/role", map[string]string{"role": "reader"}); r.StatusCode != 200 {
		t.Fatalf("set role: %s", r.Body)
	}
	var page struct {
		Items []auth.AuditRecord `json:"items"`
	}
	admin.Get("/api/v1/auth/audit?action=user.set_role").JSON(t, &page)
	if len(page.Items) != 1 || page.Items[0].TargetID != u.ID || page.Items[0].ActorID == nil || *page.Items[0].ActorID != ta.Admin.ID {
		t.Fatalf("audit: %+v", page.Items)
	}
	if page.Items[0].IP == "" {
		t.Fatal("audit entry has no IP")
	}
	// non-admins cannot read it
	_, c := ta.UserClient(t, auth.RoleCreator)
	if r := c.Get("/api/v1/auth/audit"); r.StatusCode != 403 {
		t.Fatalf("creator read audit: %d", r.StatusCode)
	}
}

// TestAPITokenCannotManageAccount: a personal API token (mvk_, not an agent)
// must not mint tokens, change the password, touch two-factor or sessions, or
// use admin routes without the "admin" scope — a leaked or narrowly scoped
// token must not escalate into full account control.
func TestAPITokenCannotManageAccount(t *testing.T) {
	ta := testutil.NewTestApp(t)
	ctx := context.Background()
	u, _ := ta.UserClient(t, auth.RoleCreator)
	raw, _, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: u.ID, Kind: auth.TokenAPI, Name: "read only", Scopes: []string{"projects:read"}})
	if err != nil {
		t.Fatal(err)
	}
	bc := ta.BearerClient(raw)
	if r := bc.Get("/api/v1/auth/me"); r.StatusCode != 200 {
		t.Fatalf("api token must still identify its user: %d", r.StatusCode)
	}
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/v1/auth/tokens"}, {"GET", "/api/v1/auth/tokens"}, {"POST", "/api/v1/auth/password/change"},
		{"POST", "/api/v1/auth/totp/begin"}, {"POST", "/api/v1/auth/totp/disable"}, {"GET", "/api/v1/auth/sessions"},
		{"POST", "/api/v1/auth/sessions/revoke-others"},
	} {
		if r := bc.SendJSON(c.method, c.path, map[string]any{"name": "full", "scopes": []string{"*"}, "current": testutil.TestPassword, "password": "a-brand-new-password"}); r.StatusCode != 403 {
			t.Errorf("%s %s with an API token: %d %s", c.method, c.path, r.StatusCode, r.Body)
		}
	}
	// HTML settings routes are session-only as well.
	if r := bc.Do(http.MethodPost, "/settings/account/password", strings.NewReader("current="+testutil.TestPassword+"&password=a-brand-new-password&confirm=a-brand-new-password"),
		"application/x-www-form-urlencoded"); r.StatusCode != 403 {
		t.Errorf("settings password change with an API token: %d", r.StatusCode)
	}
	// Admin API: an admin's token needs the "admin" scope.
	araw, _, _ := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: ta.Admin.ID, Kind: auth.TokenAPI, Name: "stats", Scopes: []string{"projects:read"}})
	if r := ta.BearerClient(araw).PostJSON("/api/v1/auth/users/"+u.ID+"/role", map[string]string{"role": "admin"}); r.StatusCode != 403 {
		t.Fatalf("admin token without admin scope changed a role: %d", r.StatusCode)
	}
	sraw, _, _ := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: ta.Admin.ID, Kind: auth.TokenAPI, Name: "ops", Scopes: []string{"admin"}})
	if r := ta.BearerClient(sraw).Get("/api/v1/auth/users"); r.StatusCode != 200 {
		t.Fatalf("admin-scoped token: %d %s", r.StatusCode, r.Body)
	}
	if n, _ := ta.Deps.Auth.ListTokens(ctx, u.ID, ""); len(n) != 1 {
		t.Fatalf("tokens after the attempts: %d", len(n))
	}
}

// TestPasswordReentryIsRateLimited: a stolen session cannot brute-force the
// password that guards disabling two-factor, new recovery codes, password
// changes and account deletion.
func TestPasswordReentryIsRateLimited(t *testing.T) {
	ta := testutil.NewTestApp(t)
	ctx := context.Background()
	u := ta.CreateUser(t, "kaito", auth.RoleCreator)
	limited := false
	for i := 0; i < 20 && !limited; i++ {
		err := ta.Deps.Auth.CheckPassword(ctx, u.ID, "wrong-password-guess")
		if errors.Is(err, auth.ErrWrongPassword) {
			continue
		}
		var ae *httpx.APIError
		if !errors.As(err, &ae) || ae.Status != http.StatusTooManyRequests {
			t.Fatalf("attempt %d: %v", i, err)
		}
		limited = true
	}
	if !limited {
		t.Fatal("password re-entry never rate limited")
	}
	if err := ta.Deps.Auth.DisableTOTP(ctx, u.ID, testutil.TestPassword); err == nil {
		t.Fatal("correct password accepted while rate limited")
	}
}
