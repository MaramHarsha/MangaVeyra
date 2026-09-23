package core_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// TestRealIPTrustsOnlyConfiguredProxies: X-Forwarded-For and -Proto count
// only from MV_TRUSTED_PROXIES, and the internal HTTPS marker header can
// never be injected by a client (directly or through a trusted proxy).
func TestRealIPTrustsOnlyConfiguredProxies(t *testing.T) {
	_, proxy, _ := net.ParseCIDR("10.0.0.0/8")
	var gotIP string
	var gotHTTPS bool
	h := httpx.RealIP([]*net.IPNet{proxy})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIP, gotHTTPS = httpx.ClientIP(r), httpx.IsHTTPS(r)
	}))
	serve := func(remote string, hdr map[string]string) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = remote
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	serve("203.0.113.7:5555", map[string]string{"X-Forwarded-For": "1.2.3.4", "X-Forwarded-Proto": "https", "X-Mv-Https": "1"})
	if gotIP != "203.0.113.7" || gotHTTPS {
		t.Fatalf("untrusted peer: ip=%s https=%v", gotIP, gotHTTPS)
	}
	serve("10.1.1.1:5555", map[string]string{"X-Forwarded-For": "6.6.6.6, 198.51.100.9", "X-Forwarded-Proto": "http", "X-Mv-Https": "1"})
	if gotIP != "198.51.100.9" || gotHTTPS {
		t.Fatalf("trusted proxy, spoofed marker: ip=%s https=%v", gotIP, gotHTTPS)
	}
	serve("10.1.1.1:5555", map[string]string{"X-Forwarded-For": "198.51.100.9", "X-Forwarded-Proto": "https"})
	if gotIP != "198.51.100.9" || !gotHTTPS {
		t.Fatalf("trusted proxy: ip=%s https=%v", gotIP, gotHTTPS)
	}
}

func TestRedactPath(t *testing.T) {
	for in, want := range map[string]string{
		"/reset/abcDEF123":                           "/reset/[redacted]",
		"/api/v1/auth/password/reset/abcDEF":         "/api/v1/auth/password/reset/[redacted]",
		"/api/v1/auth/invites/k3m9-ab2c-xyz9":        "/api/v1/auth/invites/[redacted]",
		"/api/v1/auth/invites":                       "/api/v1/auth/invites",
		"/studio/projects/01j0000000000000000000000": "/studio/projects/01j0000000000000000000000",
	} {
		if got := httpx.RedactPath(in); got != want {
			t.Errorf("%s → %s, want %s", in, got, want)
		}
	}
}

// TestLoginCodeIsRateLimited: the two-step code endpoint shares the strict
// per-IP limit of the other authentication forms.
func TestLoginCodeIsRateLimited(t *testing.T) {
	ta := testutil.NewTestApp(t)
	c := ta.NewClient()
	c.Header.Set("X-Forwarded-For", "198.51.100.77")
	limited := false
	for i := 0; i < 40 && !limited; i++ {
		r := c.Do("POST", "/login/code", strings.NewReader("code=123456"), "application/x-www-form-urlencoded")
		limited = r.StatusCode == http.StatusTooManyRequests
	}
	if !limited {
		t.Fatal("POST /login/code was never rate limited")
	}
}
