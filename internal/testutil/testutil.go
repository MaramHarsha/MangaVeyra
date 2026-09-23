// Package testutil starts a complete MangaVeyra app for integration tests:
// a temporary data directory, SQLite (or Postgres when MV_TEST_POSTGRES_URL
// is set and the test asks for it), every module wired, and an httptest
// server. Helpers create users and return signed-in HTTP clients.
package testutil

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/app"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/config"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

// TestPassword is the password of users created by CreateUser.
const TestPassword = "correct-horse-battery"

// Option customises NewTestApp.
type Option func(*setup)

type setup struct {
	dialect string
	env     map[string]string
	start   bool
	noAdmin bool
}

// WithPostgres runs the app on a fresh Postgres database (skips the test when MV_TEST_POSTGRES_URL is unset).
func WithPostgres() Option { return func(s *setup) { s.dialect = "postgres" } }

// WithDialect selects "sqlite" or "postgres".
func WithDialect(d string) Option { return func(s *setup) { s.dialect = d } }

// WithEnv sets MV_* configuration variables.
func WithEnv(kv ...string) Option {
	return func(s *setup) {
		for i := 0; i+1 < len(kv); i += 2 {
			s.env[kv[i]] = kv[i+1]
		}
	}
}

// WithBackground starts the background loops (executor, job maintenance...).
func WithBackground() Option { return func(s *setup) { s.start = true } }

// WithoutAdmin skips creating the first admin, so the setup gate stays active.
func WithoutAdmin() Option { return func(s *setup) { s.noAdmin = true } }

// TestApp is a running app.
type TestApp struct {
	*app.App
	Deps   *core.Deps
	Server *httptest.Server
	URL    string
	Clock  *Clock
	// Admin is the first admin (nil with WithoutAdmin).
	Admin *auth.User
	t     testing.TB
	seq   atomic.Int64
}

// NewTestApp starts an app for the test and stops it on cleanup.
func NewTestApp(t testing.TB, opts ...Option) *TestApp {
	t.Helper()
	s := &setup{dialect: "sqlite", env: map[string]string{}}
	for _, o := range opts {
		o(s)
	}
	dir := t.TempDir()
	// Clients get distinct X-Forwarded-For addresses (trusted from loopback) so
	// per-IP rate limits behave as they would for separate people.
	env := map[string]string{"MV_DATA_DIR": dir, "MV_LOG_LEVEL": "error", "MV_MASTER_KEY": strings.Repeat("ab", 32),
		"MV_TRUSTED_PROXIES": "127.0.0.1,::1"}
	if s.dialect == "postgres" {
		env["MV_DB"] = testdb.NewTarget(t, "postgres").URL
	}
	for k, v := range s.env {
		env[k] = v
	}
	cfg, err := config.Load("", func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("testutil: config: %v", err)
	}
	ctx := context.Background()
	params := auth.FastHashParams
	a, err := app.New(ctx, cfg, app.Options{HashParams: &params, LogOutput: io.Discard, ExecutorSize: 2})
	if err != nil {
		t.Fatalf("testutil: app: %v", err)
	}
	clock := NewClock(time.Now())
	srv := httptest.NewServer(a.Handler)
	ta := &TestApp{App: a, Deps: a.Deps, Server: srv, URL: srv.URL, Clock: clock, t: t}
	if s.start {
		a.Start(ctx)
	}
	t.Cleanup(func() {
		srv.CloseClientConnections()
		srv.Close()
		_ = a.Close()
	})
	if !s.noAdmin {
		ta.Admin = ta.CreateUser(t, "admin", auth.RoleAdmin)
	}
	return ta
}

// CreateUser creates an active user with TestPassword. username "" picks a unique one.
func (ta *TestApp) CreateUser(t testing.TB, username, role string) *auth.User {
	t.Helper()
	if username == "" {
		username = "user" + itoa(ta.seq.Add(1))
	}
	u, err := ta.Deps.Auth.CreateUser(context.Background(), auth.NewUser{Username: username, Email: username + "@example.org",
		Password: TestPassword, Role: role})
	if err != nil {
		t.Fatalf("testutil: create user %s: %v", username, err)
	}
	return u
}

// Client is an HTTP client with a cookie jar bound to the test server.
type Client struct {
	*http.Client
	Base string
	t    testing.TB
	// Header is sent with every request (e.g. Authorization).
	Header http.Header
}

// NewClient returns an anonymous client with a cookie jar and its own client IP.
func (ta *TestApp) NewClient() *Client {
	jar, _ := cookiejar.New(nil)
	n := ta.seq.Add(1)
	h := http.Header{}
	h.Set("X-Forwarded-For", "10.9."+itoa(n/250%250)+"."+itoa(n%250+1))
	return &Client{Client: &http.Client{Jar: jar, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}, Base: ta.URL, t: ta.t, Header: h}
}

// Login signs username in through the JSON API and returns the client.
func (ta *TestApp) Login(t testing.TB, username string) *Client {
	t.Helper()
	c := ta.NewClient()
	c.t = t
	resp := c.PostJSON("/api/v1/auth/login", map[string]any{"identifier": username, "password": TestPassword, "remember": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("testutil: login %s: %d %s", username, resp.StatusCode, resp.Body)
	}
	return c
}

// UserClient creates a user with role and returns it with a signed-in client.
func (ta *TestApp) UserClient(t testing.TB, role string) (*auth.User, *Client) {
	t.Helper()
	u := ta.CreateUser(t, "", role)
	return u, ta.Login(t, u.Username)
}

// BearerClient returns a client that authenticates with a bearer token.
func (ta *TestApp) BearerClient(token string) *Client {
	c := ta.NewClient()
	c.Header.Set("Authorization", "Bearer "+token)
	return c
}

// Response is a fully read HTTP response.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// JSON decodes the body into v (failing the test on error).
func (r *Response) JSON(t testing.TB, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("testutil: decode %q: %v", r.Body, err)
	}
}

// Map decodes the body into a map.
func (r *Response) Map(t testing.TB) map[string]any {
	t.Helper()
	m := map[string]any{}
	r.JSON(t, &m)
	return m
}

// ErrorCode returns the "error.code" of a JSON error envelope.
func (r *Response) ErrorCode() string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &e)
	return e.Error.Code
}

// Do sends a request with an optional body.
func (c *Client) Do(method, path string, body io.Reader, contentType string) *Response {
	c.t.Helper()
	req, err := http.NewRequest(method, c.Base+path, body)
	if err != nil {
		c.t.Fatalf("testutil: request: %v", err)
	}
	for k, v := range c.Header {
		req.Header[k] = v
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		c.t.Fatalf("testutil: %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return &Response{StatusCode: resp.StatusCode, Header: resp.Header, Body: b}
}

// Get sends GET.
func (c *Client) Get(path string) *Response { return c.Do(http.MethodGet, path, nil, "") }

// PostJSON sends POST with a JSON body.
func (c *Client) PostJSON(path string, v any) *Response { return c.SendJSON(http.MethodPost, path, v) }

// SendJSON sends any method with a JSON body.
func (c *Client) SendJSON(method, path string, v any) *Response {
	var body io.Reader
	if v != nil {
		b, err := json.Marshal(v)
		if err != nil {
			c.t.Fatalf("testutil: marshal: %v", err)
		}
		body = bytes.NewReader(b)
	}
	return c.Do(method, path, body, "application/json")
}

// Clock is a manually advanced clock for services with a Now field.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock starts at t.
func NewClock(t time.Time) *Clock { return &Clock{now: t.UTC()} }

// Now returns the current fake time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// Set jumps to t.
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t.UTC()
	c.mu.Unlock()
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
