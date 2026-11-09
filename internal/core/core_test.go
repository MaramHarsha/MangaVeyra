package core_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestRegistry(t *testing.T) {
	d := &core.Deps{}
	type greeter interface{ Hello() string }
	core.Provide[*strings.Builder](d, &strings.Builder{})
	if _, ok := core.Use[greeter](d); ok {
		t.Fatal("unprovided service found")
	}
	b := core.MustUse[*strings.Builder](d)
	b.WriteString("x")
	if core.MustUse[*strings.Builder](d).String() != "x" {
		t.Fatal("registry returned a different value")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("double Provide must panic")
		}
	}()
	core.Provide[*strings.Builder](d, &strings.Builder{})
}

func TestRouterGroupsAndErrors(t *testing.T) {
	r := core.NewRouter()
	calls := []string{}
	mw := func(name string) core.Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls = append(calls, name)
				next.ServeHTTP(w, req)
			})
		}
	}
	api := r.Group("/api/v1", mw("group"))
	api.API("GET /things/{id}", func(w http.ResponseWriter, req *http.Request) error {
		if req.PathValue("id") == "missing" {
			return core.ErrNotFound()
		}
		if req.PathValue("id") == "boom" {
			return errors.New("database exploded")
		}
		core.JSON(w, 200, map[string]string{"id": req.PathValue("id")})
		return nil
	}, mw("route"))
	r.Page("GET /page", func(w http.ResponseWriter, req *http.Request) error { return core.ErrForbidden("") })

	do := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec
	}
	if rec := do("GET", "/api/v1/things/42"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"42"`) || strings.Join(calls, ",") != "group,route" {
		t.Fatalf("route: %d %s %v", rec.Code, rec.Body, calls)
	}
	rec := do("GET", "/api/v1/things/missing")
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"code":"not_found"`) {
		t.Fatalf("404 envelope: %s", rec.Body)
	}
	rec = do("GET", "/api/v1/things/boom")
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "exploded") {
		t.Fatalf("500 must not leak internals: %s", rec.Body)
	}
	if rec := do("POST", "/api/v1/things/1"); rec.Code != 405 || !strings.Contains(rec.Body.String(), "method_not_allowed") || rec.Header().Get("Allow") == "" {
		t.Fatalf("405: %d %s %v", rec.Code, rec.Body, rec.Header())
	}
	if rec := do("GET", "/api/v1/nothing"); rec.Code != 404 || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("unmatched API path: %d %s", rec.Code, rec.Body)
	}
	if rec := do("GET", "/page"); rec.Code != 403 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("page error fallback: %d %v", rec.Code, rec.Header())
	}
	// The web layer's hook renders HTML errors.
	core.SetHooks(core.Hooks{ErrorPage: func(w http.ResponseWriter, r *http.Request, e *httpx.APIError) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(e.Status)
		w.Write([]byte("<h1>SHIIN… " + e.Code + "</h1>"))
	}})
	defer core.SetHooks(core.Hooks{})
	if rec := do("GET", "/nowhere"); rec.Code != 404 || !strings.Contains(rec.Body.String(), "SHIIN") {
		t.Fatalf("html 404 hook: %d %s", rec.Code, rec.Body)
	}
	routes := r.Routes()
	if len(routes) != 2 || routes[0].Pattern != "GET /api/v1/things/{id}" {
		t.Fatalf("routes %+v", routes)
	}
}

func TestSafeRedirectTarget(t *testing.T) {
	for in, want := range map[string]string{
		"/studio": "/studio", "//evil.com": "/", "https://evil.com": "/", "/\\evil": "/", "": "/", "studio": "/",
	} {
		if got := core.SafeRedirectTarget(in, "/"); got != want {
			t.Errorf("%q → %q", in, got)
		}
	}
}

func TestLimiter(t *testing.T) {
	l := core.NewLimiter(1, 2)
	now := testutil.NewClock(testutilNow())
	l.SetClock(now.Now)
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal()
	}
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal()
	}
	ok, wait := l.Allow("k")
	if ok || wait <= 0 {
		t.Fatal("burst exceeded but allowed")
	}
	now.Advance(wait)
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("token not refilled")
	}
	if ok, _ := l.Allow("other"); !ok {
		t.Fatal("keys must be independent")
	}
}

func TestStackHeadersGatesAndLocale(t *testing.T) {
	ta := testutil.NewTestApp(t, testutil.WithoutAdmin())
	c := ta.NewClient()
	// Setup gate: HTML requests go to /setup, API requests get setup_required.
	r := c.Do("GET", "/studio", nil, "")
	if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/setup" {
		t.Fatalf("setup redirect: %d %v", r.StatusCode, r.Header)
	}
	if r := c.Get("/api/v1/jobs"); r.StatusCode != 503 || r.ErrorCode() != "setup_required" {
		t.Fatalf("api during setup: %d %s", r.StatusCode, r.Body)
	}
	if r := c.Get("/healthz"); r.StatusCode != 200 {
		t.Fatalf("healthz during setup: %d", r.StatusCode)
	}
	// Security headers on every response.
	h := r.Header
	csp := h.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "img-src 'self' data: blob:", "connect-src 'self' ws://", "object-src 'none'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP lacks %q: %s", want, csp)
		}
	}
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != "DENY" || h.Get("X-Request-Id") == "" || !strings.Contains(h.Get("Permissions-Policy"), "payment=()") {
		t.Fatalf("headers: %v", h)
	}

	admin := ta.CreateUser(t, "admin", auth.RoleAdmin)
	_ = admin
	// Locale negotiation: Accept-Language → Japanese error messages; cookie wins over header.
	jc := ta.NewClient()
	jc.Header.Set("Accept-Language", "ja-JP,ja;q=0.9,en;q=0.5")
	r = jc.Get("/api/v1/auth/me")
	if r.StatusCode != 401 || !strings.Contains(string(r.Body), "ログイン") || r.Header.Get("Content-Language") != "ja" {
		t.Fatalf("ja negotiation: %s %v", r.Body, r.Header.Get("Content-Language"))
	}
	jc.Header.Set("Cookie", core.LangCookie+"=en")
	if r := jc.Get("/api/v1/auth/me"); r.Header.Get("Content-Language") != "en" {
		t.Fatalf("cookie override: %v", r.Header.Get("Content-Language"))
	}

	// CSRF: a cross-site browser POST is refused; same-origin and non-browser requests pass.
	cs := ta.NewClient()
	cs.Header.Set("Sec-Fetch-Site", "cross-site")
	if r := cs.PostJSON("/api/v1/auth/login", map[string]string{"identifier": "admin", "password": testutil.TestPassword}); r.StatusCode != 403 || r.ErrorCode() != "csrf" {
		t.Fatalf("cross-site POST: %d %s", r.StatusCode, r.Body)
	}
	cs.Header.Set("Sec-Fetch-Site", "same-origin")
	if r := cs.PostJSON("/api/v1/auth/login", map[string]string{"identifier": "admin", "password": testutil.TestPassword}); r.StatusCode != 200 {
		t.Fatalf("same-origin POST: %d %s", r.StatusCode, r.Body)
	}

	// Maintenance: members get 503, admins get through.
	if _, err := ta.Deps.Settings.UpdateInstance(context.Background(), "", func(i *settings.Instance) error {
		i.Maintenance = settings.Maintenance{Enabled: true, Message: "Updating to 1.1 — GARA GARA"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, member := ta.UserClient(t, auth.RoleCreator)
	if r := member.Get("/api/v1/jobs"); r.StatusCode != 503 || r.ErrorCode() != "maintenance" || !strings.Contains(string(r.Body), "GARA") || r.Header.Get("Retry-After") == "" {
		t.Fatalf("maintenance for members: %d %s", r.StatusCode, r.Body)
	}
	// The holding page follows the visitor's language although the gate runs before locale resolution.
	ja := ta.NewClient()
	ja.Header.Set("Accept-Language", "ja")
	if r := ja.Get("/explore"); r.StatusCode != 503 || !strings.Contains(string(r.Body), `lang="ja"`) || !strings.Contains(string(r.Body), "を更新中") {
		t.Fatalf("maintenance page for a Japanese visitor: %d %.300s", r.StatusCode, r.Body)
	}
	if r := cs.Get("/api/v1/jobs"); r.StatusCode != 200 {
		t.Fatalf("admin during maintenance: %d %s", r.StatusCode, r.Body)
	}
}

func TestRequireUserRedirectsPages(t *testing.T) {
	r := core.NewRouter()
	r.Page("GET /studio", func(w http.ResponseWriter, req *http.Request) error { return nil }, core.RequireUser)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/studio?tab=1", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?next=%2Fstudio%3Ftab%3D1" {
		t.Fatalf("redirect: %d %v", rec.Code, rec.Header())
	}
	req := httptest.NewRequest("GET", "/studio", nil)
	req.Header.Set("HX-Request", "true")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Header().Get("HX-Redirect") == "" {
		t.Fatalf("htmx redirect: %v", rec.Header())
	}
	// agents are not humans
	h := core.RequireHuman(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req = httptest.NewRequest("POST", "/api/v1/publish", nil)
	req = req.WithContext(httpx.WithIdentity(req.Context(), httpx.Identity{User: &httpx.User{ID: "u", Status: "active"}, Token: &httpx.Token{Kind: "agent"}}))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("agent passed RequireHuman: %d", rec.Code)
	}
}

func testutilNow() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) }
