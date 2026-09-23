package studio_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// env is a test app with the studio service and one creator.
type env struct {
	ta    *testutil.TestApp
	svc   *studio.Service
	ctx   context.Context
	owner *auth.User
	c     *testutil.Client
}

func newEnv(t *testing.T, opts ...testutil.Option) *env {
	t.Helper()
	ta := testutil.NewTestApp(t, opts...)
	owner, c := ta.UserClient(t, auth.RoleCreator)
	return &env{ta: ta, svc: core.MustUse[*studio.Service](ta.Deps), ctx: context.Background(), owner: owner, c: c}
}

// userCtx returns a context acting as user u (as a request would).
func userCtx(u *auth.User) context.Context {
	return httpx.WithIdentity(context.Background(), httpx.Identity{User: &httpx.User{ID: u.ID, Username: u.Username, Role: u.Role}})
}

// agentCtx returns a context acting as an agent token of u.
func agentCtx(u *auth.User) context.Context {
	return httpx.WithIdentity(context.Background(), httpx.Identity{User: &httpx.User{ID: u.ID, Username: u.Username, Role: u.Role},
		Token: &httpx.Token{ID: "tok", Kind: httpx.TokenAgent, Scopes: []string{"*"}}})
}

func (e *env) project(t *testing.T, title string) *studio.Project {
	t.Helper()
	p, err := e.svc.CreateProject(e.ctx, e.owner.ID, studio.NewProject{Title: title, Premise: "A courier who cannot read maps delivers letters."})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return p
}

func (e *env) access(t *testing.T, ctx context.Context, projectID string) *studio.Access {
	t.Helper()
	a, err := e.svc.Access(ctx, studio.ActorFrom(ctx), projectID)
	if err != nil {
		t.Fatalf("access: %v", err)
	}
	return a
}

func post(c *testutil.Client, path string, v url.Values) *testutil.Response {
	return c.Do(http.MethodPost, path, strings.NewReader(v.Encode()), "application/x-www-form-urlencoded")
}

func htmxPost(c *testutil.Client, path string, v url.Values) *testutil.Response {
	c.Header.Set("HX-Request", "true")
	defer c.Header.Del("HX-Request")
	return post(c, path, v)
}

func htmxGet(c *testutil.Client, path string) *testutil.Response {
	c.Header.Set("HX-Request", "true")
	defer c.Header.Del("HX-Request")
	return c.Get(path)
}

func contains(t *testing.T, r *testutil.Response, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(string(r.Body), p) {
			t.Fatalf("response %d lacks %q:\n%.3000s", r.StatusCode, p, r.Body)
		}
	}
}

func status(t *testing.T, r *testutil.Response, want int) {
	t.Helper()
	if r.StatusCode != want {
		t.Fatalf("status %d, want %d: %.1500s", r.StatusCode, want, r.Body)
	}
}

func redirect(t *testing.T, r *testutil.Response) string {
	t.Helper()
	if r.StatusCode != http.StatusSeeOther {
		t.Fatalf("want 303, got %d: %.1500s", r.StatusCode, r.Body)
	}
	return r.Header.Get("Location")
}

// pngBytes returns a small valid PNG.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < 8; i++ {
		img.Set(i, i, color.Black)
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// multipartPost posts fields and files (field → content) as multipart/form-data.
func multipartPost(t *testing.T, c *testutil.Client, path string, fields map[string]string, fileField string, files ...[]byte) *testutil.Response {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	for i, f := range files {
		fw, err := w.CreateFormFile(fileField, "ref"+string(rune('a'+i))+".png")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(f)
	}
	_ = w.Close()
	return c.Do(http.MethodPost, path, &body, w.FormDataContentType())
}

// runJobs runs queued Go-executor jobs with the studio's placeholder handlers until none is left.
func (e *env) runJobs(t *testing.T) {
	t.Helper()
	e.svc.RegisterFallbackHandlers(e.ta.Deps.Executor)
	for i := 0; i < 20; i++ {
		ran, err := e.ta.Deps.Executor.RunOnce(context.Background())
		if err != nil {
			t.Fatalf("run job: %v", err)
		}
		if !ran {
			return
		}
	}
}
