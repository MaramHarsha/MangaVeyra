package authui_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/authui"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func post(c *testutil.Client, path string, v url.Values) *testutil.Response {
	return c.Do(http.MethodPost, path, strings.NewReader(v.Encode()), "application/x-www-form-urlencoded")
}

func contains(t *testing.T, r *testutil.Response, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(string(r.Body), p) {
			t.Fatalf("response %d lacks %q:\n%.2500s", r.StatusCode, p, r.Body)
		}
	}
}

func setInstance(t *testing.T, ta *testutil.TestApp, fn func(i *settings.Instance)) {
	t.Helper()
	if _, err := ta.Deps.Settings.UpdateInstance(context.Background(), "", func(i *settings.Instance) error { fn(i); return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestLoginPage(t *testing.T) {
	ta := testutil.NewTestApp(t)
	r := ta.NewClient().Get("/login?next=/settings/reading")
	if r.StatusCode != http.StatusOK {
		t.Fatalf("GET /login: %d", r.StatusCode)
	}
	contains(t, r, "Welcome back", `name="identifier"`, `autocomplete="current-password"`, "Stay signed in", "/forgot",
		`value="/settings/reading"`, "Ask your admin for an invite", `lang="en"`)
	// The designed "Use a passkey" action is shown, but passkeys are not
	// implemented, so it must be the disabled Soon control, never a link.
	contains(t, r, "Use a passkey", `class="mv-soon"`)
	if strings.Contains(string(r.Body), `href="/login/passkey"`) {
		t.Fatal("passkeys are not implemented and must not be offered as a working link")
	}
	if !strings.Contains(r.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatal("CSP header missing")
	}
	// Japanese via Accept-Language.
	c := ta.NewClient()
	c.Header.Set("Accept-Language", "ja")
	contains(t, c.Get("/login"), "おかえりなさい", `lang="ja"`)
}

func TestLoginFlow(t *testing.T) {
	ta := testutil.NewTestApp(t)
	u := ta.CreateUser(t, "rin", auth.RoleCreator)
	c := ta.NewClient()

	bad := post(c, "/login", url.Values{"identifier": {"rin"}, "password": {"wrong-password-1"}})
	if bad.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("wrong password: %d", bad.StatusCode)
	}
	contains(t, bad, "Wrong username, email or password.", `value="rin"`)

	empty := post(c, "/login", url.Values{})
	if empty.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("empty form: %d", empty.StatusCode)
	}

	ok := post(c, "/login", url.Values{"identifier": {"rin@example.org"}, "password": {testutil.TestPassword}, "remember": {"on"}})
	if ok.StatusCode != http.StatusSeeOther || ok.Header.Get("Location") != "/onboarding/1" {
		t.Fatalf("first login goes to onboarding: %d %q", ok.StatusCode, ok.Header.Get("Location"))
	}
	var session *http.Cookie
	for _, ck := range (&http.Response{Header: ok.Header}).Cookies() {
		if ck.Name == auth.SessionCookie {
			session = ck
		}
	}
	if session == nil || !session.HttpOnly || session.MaxAge <= 0 {
		t.Fatalf("stay-signed-in session cookie: %+v", session)
	}
	if r := c.Get("/login"); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("signed-in visitors skip the login page: %d", r.StatusCode)
	}

	// Once onboarded, "next" is honoured and open redirects are refused.
	c2 := ta.NewClient()
	if r := post(c, "/onboarding/skip", nil); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("skip onboarding: %d", r.StatusCode)
	}
	r := post(c2, "/login", url.Values{"identifier": {u.Username}, "password": {testutil.TestPassword}, "next": {"//evil.example/x"}})
	if r.Header.Get("Location") != "/studio" {
		t.Fatalf("open redirect not blocked: %q", r.Header.Get("Location"))
	}
	c3 := ta.NewClient()
	r = post(c3, "/login", url.Values{"identifier": {u.Username}, "password": {testutil.TestPassword}, "next": {"/settings/reading"}})
	if r.Header.Get("Location") != "/settings/reading" {
		t.Fatalf("next: %q", r.Header.Get("Location"))
	}

	// Log out.
	out := post(c3, "/logout", nil)
	if out.StatusCode != http.StatusSeeOther || c3.Get("/studio").StatusCode != http.StatusSeeOther {
		t.Fatalf("logout: %d", out.StatusCode)
	}
}

func TestLoginWithTwoStepCode(t *testing.T) {
	ta := testutil.NewTestApp(t)
	u := ta.CreateUser(t, "kaito", auth.RoleCreator)
	ctx := context.Background()
	enr, err := ta.Deps.Auth.BeginTOTP(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := auth.GenerateTOTPCode(enr.Secret, time.Now())
	if _, err := ta.Deps.Auth.ConfirmTOTP(ctx, u.ID, code); err != nil {
		t.Fatal(err)
	}
	c := ta.NewClient()
	step := post(c, "/login", url.Values{"identifier": {"kaito"}, "password": {testutil.TestPassword}})
	if step.StatusCode != http.StatusOK {
		t.Fatalf("password step: %d", step.StatusCode)
	}
	contains(t, step, "Two-step check", `action="/login/code"`, `autocomplete="one-time-code"`)
	if strings.Contains(string(step.Body), testutil.TestPassword) {
		t.Fatal("the password must never be echoed into the page")
	}
	wrong := post(c, "/login/code", url.Values{"code": {"000000"}})
	if wrong.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("wrong code: %d", wrong.StatusCode)
	}
	// A fresh code (next step: the previous one may already be used).
	code, _ = auth.GenerateTOTPCode(enr.Secret, time.Now().Add(30*time.Second))
	done := post(c, "/login/code", url.Values{"code": {code}})
	if done.StatusCode != http.StatusSeeOther {
		t.Fatalf("code step: %d %s", done.StatusCode, done.Body)
	}
	// Without the sealed pending cookie the code step starts over.
	if r := post(ta.NewClient(), "/login/code", url.Values{"code": {code}}); r.Header.Get("Location") != "/login" {
		t.Fatalf("no pending login: %d %q", r.StatusCode, r.Header.Get("Location"))
	}
}

func TestSignupWithInvite(t *testing.T) {
	ta := testutil.NewTestApp(t)
	ctx := context.Background()
	setInstance(t, ta, func(i *settings.Instance) { i.SignUp = settings.SignUpInvite; i.Rules = "Be kind. Original work only." })

	c := ta.NewClient()
	closed := c.Get("/signup")
	contains(t, closed, "Join ", "You need an invite to join this server.")
	if strings.Contains(string(closed.Body), `action="/signup"`) {
		t.Fatal("closed sign-up must not render the sign-up form")
	}
	if r := post(c, "/signup", url.Values{"username": {"sora"}}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("sign-up without invite: %d", r.StatusCode)
	}

	inv, err := ta.Deps.Auth.CreateInvite(ctx, ta.Admin.ID, auth.InviteInput{Role: auth.RoleCreator, MaxUses: 2})
	if err != nil {
		t.Fatal(err)
	}
	page := c.Get("/signup?invite=" + inv.Code)
	contains(t, page, "Invited by admin", `name="invite" value="`+inv.Code+`"`, "Be kind. Original work only.", `data-strength="pw-strength"`)

	form := url.Values{"invite": {inv.Code}, "display_name": {"Sora Vell"}, "username": {"sora"}, "email": {"sora@example.org"},
		"password": {"paper-crows-ninth-bell"}}
	noAgree := post(c, "/signup", form)
	if noAgree.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("rules not accepted: %d", noAgree.StatusCode)
	}
	contains(t, noAgree, "Please accept the server rules")

	form.Set("agree", "on")
	short := url.Values{}
	for k, v := range form {
		short[k] = v
	}
	short.Set("password", "short")
	if r := post(c, "/signup", short); r.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(r.Body), "at least 10") {
		t.Fatalf("short password: %d", r.StatusCode)
	}

	ok := post(c, "/signup", form)
	if ok.StatusCode != http.StatusSeeOther || ok.Header.Get("Location") != "/onboarding/1" {
		t.Fatalf("sign-up: %d %q %s", ok.StatusCode, ok.Header.Get("Location"), ok.Body)
	}
	u, err := ta.Deps.Auth.UserByUsername(ctx, "sora")
	if err != nil || u.DisplayName != "Sora Vell" || u.Role != auth.RoleCreator {
		t.Fatalf("created user: %+v %v", u, err)
	}
	if r := c.Get("/onboarding/1"); r.StatusCode != http.StatusOK {
		t.Fatalf("signed in after sign-up: %d", r.StatusCode)
	}

	// Taken username on the second use.
	c2 := ta.NewClient()
	form.Set("email", "sora2@example.org")
	dup := post(c2, "/signup", form)
	if dup.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate: %d", dup.StatusCode)
	}
	contains(t, dup, "That username is taken.")

	// Invalid invites say so.
	contains(t, ta.NewClient().Get("/signup?invite=nope"), "This invite is invalid")
}

func TestOpenSignupWithoutInvite(t *testing.T) {
	ta := testutil.NewTestApp(t)
	setInstance(t, ta, func(i *settings.Instance) { i.SignUp = settings.SignUpOpen })
	c := ta.NewClient()
	contains(t, c.Get("/login"), "Create an account")
	r := post(c, "/signup", url.Values{"display_name": {"Pip"}, "username": {"pip"}, "email": {"pip@example.org"},
		"password": {"salt-and-static-9"}, "agree": {"on"}})
	if r.StatusCode != http.StatusSeeOther {
		t.Fatalf("open sign-up: %d %s", r.StatusCode, r.Body)
	}
}

func TestForgotAndReset(t *testing.T) {
	ta := testutil.NewTestApp(t)
	ctx := context.Background()
	u := ta.CreateUser(t, "wren", auth.RoleCreator)
	c := ta.NewClient()

	// No SMTP on this server: the designed form and lead still show (the lead
	// names the admin route); submitting confirms that email is unavailable.
	f := c.Get("/forgot")
	contains(t, f, "HMMM…", `action="/forgot"`, "your admin can reset it from Admin → Users.")
	r := post(c, "/forgot", url.Values{"email": {"wren@example.org"}})
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("forgot without email: %d", r.StatusCode)
	}
	contains(t, r, "Ask an admin to reset your password.")

	link, err := ta.Deps.Auth.CreateResetLink(ctx, ta.Admin.ID, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := link[strings.Index(link, "/reset/"):]
	page := c.Get(path)
	contains(t, page, "Choose a new password", "@wren")
	if page.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("reset pages must not leak the token in Referer")
	}
	if r := post(c, path, url.Values{"password": {"lanterns-of-oru-1"}, "confirm": {"lanterns-of-oru-2"}}); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("mismatch: %d", r.StatusCode)
	}
	done := post(c, path, url.Values{"password": {"lanterns-of-oru-1"}, "confirm": {"lanterns-of-oru-1"}})
	if done.StatusCode != http.StatusSeeOther {
		t.Fatalf("reset: %d %s", done.StatusCode, done.Body)
	}
	if _, err := ta.Deps.Auth.Login(ctx, auth.LoginInput{Identifier: "wren", Password: "lanterns-of-oru-1", IP: "10.1.1.1"}); err != nil {
		t.Fatalf("login with the new password: %v", err)
	}
	if r := c.Get(path); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("used link: %d", r.StatusCode)
	}
	contains(t, c.Get("/reset/not-a-token"), "This link has expired")
}

func TestOnboarding(t *testing.T) { testOnboarding(t) }

// TestOnboardingPostgres runs the same flow on Postgres (skipped unless MV_TEST_POSTGRES_URL is set).
func TestOnboardingPostgres(t *testing.T) { testOnboarding(t, testutil.WithPostgres()) }

func testOnboarding(t *testing.T, opts ...testutil.Option) {
	ta := testutil.NewTestApp(t, opts...)
	u, c := ta.UserClient(t, auth.RoleCreator)
	p1 := c.Get("/onboarding/1")
	contains(t, p1, "What brings you here?", `name="creator_type"`, "Skip for now", `data-transition="turn"`)
	if r := post(c, "/onboarding/1", url.Values{"creator_type": {"artist"}}); r.Header.Get("Location") != "/onboarding/2" {
		t.Fatalf("step 1: %d %q", r.StatusCode, r.Header.Get("Location"))
	}
	contains(t, c.Get("/onboarding/2"), "Which stories pull you in?", "Preview of your Explore shelf", "Pick genres to see matches")
	few := post(c, "/onboarding/2", url.Values{"genres": {"action", "mystery"}})
	if few.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("two genres: %d", few.StatusCode)
	}
	contains(t, few, "Pick at least 3 genres")
	if r := post(c, "/onboarding/2", url.Values{"genres": {"action", "mystery", "fantasy", "bogus"}}); r.Header.Get("Location") != "/onboarding/3" {
		t.Fatalf("step 2: %d", r.StatusCode)
	}
	prev := c.Get("/onboarding/preview?genres=mystery&genres=horror")
	contains(t, prev, "Ninth Bell", "Matches 2 picks", `id="mv-genre-count"`, `hx-swap-oob="true"`)
	if r := post(c, "/onboarding/3", url.Values{"art_style": {"shojo_soft"}}); r.Header.Get("Location") != "/studio" {
		t.Fatalf("finish: %d", r.StatusCode)
	}
	var raw string
	if err := ta.Deps.DB.Get(context.Background(), &raw, `SELECT onboarding FROM users WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"creator_type":"artist"`, `"genres":["action","mystery","fantasy"]`, `"art_style":"shojo_soft"`, `"completed_at"`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("onboarding %s lacks %s", raw, want)
		}
	}
	if r := c.Get("/onboarding/9"); r.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown step: %d", r.StatusCode)
	}
}

func TestShelfFor(t *testing.T) {
	items := authui.ShelfFor([]string{"romance", "slice_of_life", "comedy"})
	if len(items) != 6 || items[0].Matches < items[5].Matches || items[0].Title != "Low Tide Radio" {
		t.Fatalf("ShelfFor: %+v", items)
	}
}

func TestSignupDerivesUsernameFromEmail(t *testing.T) {
	ta := testutil.NewTestApp(t)
	ctx := context.Background()
	setInstance(t, ta, func(i *settings.Instance) { i.SignUp = settings.SignUpInvite })
	inv, err := ta.Deps.Auth.CreateInvite(ctx, ta.Admin.ID, auth.InviteInput{Role: auth.RoleCreator, MaxUses: 3})
	if err != nil {
		t.Fatal(err)
	}
	ta.CreateUser(t, "pip.vell", auth.RoleCreator)
	c := ta.NewClient()
	page := c.Get("/signup?invite=" + inv.Code)
	if strings.Contains(string(page.Body), `name="username"`) {
		t.Fatal("the designed sign-up form has no username field")
	}
	r := post(c, "/signup", url.Values{"invite": {inv.Code}, "display_name": {"Pip Vell"}, "email": {"Pip.Vell@studio.example"},
		"password": {"paper-crows-ninth-bell"}, "agree": {"on"}})
	if r.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign-up without username = %d: %.1500s", r.StatusCode, r.Body)
	}
	var username string
	if err := ta.Deps.DB.Get(ctx, &username, `SELECT username FROM users WHERE email = ?`, "pip.vell@studio.example"); err != nil {
		t.Fatal(err)
	}
	if username != "pip.vell2" {
		t.Fatalf("derived username = %q, want pip.vell2 (pip.vell was taken)", username)
	}
}
