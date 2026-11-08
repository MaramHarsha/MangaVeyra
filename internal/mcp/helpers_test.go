package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/mcp"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/seed"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

var (
	scopesRead     = []string{"projects:read"}
	scopesEdit     = []string{"projects:read", "projects:write"}
	scopesGenerate = []string{"projects:read", "projects:write", "generate"}
)

// fixture is a running app with the Tidebreak Courier demo owned by a creator.
type fixture struct {
	t       *testing.T
	ta      *testutil.TestApp
	ctx     context.Context
	owner   *auth.User
	browser *testutil.Client
	project studio.Project
	chapter studio.Chapter
	studio  studio.Service
	pages   pagestore.Service
	mod     *mcp.Module
}

func forEachDialect(t *testing.T, fn func(t *testing.T, f *fixture)) {
	t.Helper()
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) { fn(t, newFixture(t, dialect)) })
	}
}

func newFixture(t *testing.T, dialect string) *fixture {
	t.Helper()
	ta := testutil.NewTestApp(t, testutil.WithDialect(dialect), testutil.WithBackground())
	ctx := context.Background()
	owner, browser := ta.UserClient(t, auth.RoleCreator)
	if err := seed.Run(ctx, ta.Deps, seed.Options{Demo: true, OwnerUsername: owner.Username}); err != nil {
		t.Fatalf("seed demo: %v", err)
	}
	f := &fixture{t: t, ta: ta, ctx: ctx, owner: owner, browser: browser,
		studio: core.MustUse[studio.Service](ta.Deps), pages: core.MustUse[pagestore.Service](ta.Deps)}
	list, err := f.studio.ListProjects(ctx, owner.ID, 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("demo project: %v %d", err, len(list))
	}
	f.project = list[0].Project
	chapters, err := f.studio.ListChapters(ctx, f.project.ID)
	if err != nil || len(chapters) == 0 {
		t.Fatalf("demo chapters: %v", err)
	}
	f.chapter = chapters[0]
	for _, m := range ta.App.Modules {
		if mm, ok := m.(*mcp.Module); ok {
			f.mod = mm
		}
	}
	if f.mod == nil {
		t.Fatal("mcp module not wired")
	}
	return f
}

// token creates an agent token for the owner.
func (f *fixture) token(name string, scopes, projects []string, threshold *float64) (string, *auth.TokenInfo) {
	f.t.Helper()
	raw, info, err := f.ta.Deps.Auth.CreateToken(f.ctx, auth.TokenInput{UserID: f.owner.ID, Kind: auth.TokenAgent, Name: name,
		Scopes: scopes, ProjectIDs: projects, ApproveOverUSD: threshold})
	if err != nil {
		f.t.Fatal(err)
	}
	return raw, info
}

type bearerRT struct{ token string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// connect opens a real MCP client session from the official SDK.
func (f *fixture) connect(raw string) *mcpsdk.ClientSession {
	f.t.Helper()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-agent", Version: "1.0"}, nil)
	s, err := client.Connect(f.ctx, &mcpsdk.StreamableClientTransport{Endpoint: f.ta.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerRT{raw}, Timeout: 30 * time.Second}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		f.t.Fatalf("connect: %v", err)
	}
	f.t.Cleanup(func() { _ = s.Close() })
	return s
}

// result is a decoded tool result.
type result struct {
	Err  bool
	Text string
	Data map[string]any
}

func call(t *testing.T, s *mcpsdk.ClientSession, name string, args map[string]any) result {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return result{Err: true, Text: err.Error()}
	}
	out := result{Err: res.IsError}
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			out.Text += tc.Text
		}
	}
	if res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(b, &out.Data)
	}
	return out
}

func mustCall(t *testing.T, s *mcpsdk.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	r := call(t, s, name, args)
	if r.Err {
		t.Fatalf("%s: %s", name, r.Text)
	}
	return r.Data
}

func form(c *testutil.Client, path string, values url.Values) *testutil.Response {
	return c.Do(http.MethodPost, path, strings.NewReader(values.Encode()), "application/x-www-form-urlencoded")
}

// firstPanel returns the first script panel of the fixture chapter.
func (f *fixture) script() studio.ScriptDoc {
	f.t.Helper()
	doc, err := f.studio.GetScript(f.ctx, f.chapter.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return doc
}

func (f *fixture) revision() string {
	f.t.Helper()
	c, err := f.studio.GetChapter(f.ctx, f.chapter.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return c.UpdatedAt.UTC().Format(time.RFC3339Nano)
}

// action reads one agent_actions row's status.
func (f *fixture) actionStatus(id string) string {
	f.t.Helper()
	var s string
	if err := f.ta.Deps.DB.Get(f.ctx, &s, `SELECT status FROM agent_actions WHERE id = ?`, id); err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *fixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.ta.Deps.DB.Get(f.ctx, &n, query, args...); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
