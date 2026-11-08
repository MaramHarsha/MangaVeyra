package ai_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

// Review fixes for ADR 0013 §9 on the screens: members never create, edit or
// test a ComfyUI or HTTP connection, even when the admin allows personal
// providers and private-network endpoints; and the G2 ON switch turns a
// custom connection on only after a passed test.

func TestReviewMembersCannotConnectComfyUIOrHTTPServers(t *testing.T) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.WriteString(w, `{"ok":true}`)
			}))
			defer server.Close()
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect), testutil.WithEnv("MV_ALLOW_PRIVATE_PROVIDER_URLS", "true"))
			updateModelsInstance(t, ta, func(inst *settings.Instance) { inst.UsersMayAddProviders = true })
			_, member := ta.UserClient(t, auth.RoleCreator)

			for _, typ := range []string{"http", "comfyui"} {
				r := member.Get("/models/providers/new/connect?type=" + typ)
				if r.StatusCode != http.StatusForbidden {
					t.Fatalf("%s form for a member = %d", typ, r.StatusCode)
				}
				r.StatusCode = http.StatusOK // the designed page, served with 403
				page := mainOf(t, r)
				mustContain(t, page, "An admin connects this server", `href="/models"`)
				mustNotContain(t, page, `name="endpoint"`, "<form")
				assertEveryTargetIsServed(t, ta, page)
			}
			// Posting the form anyway (a Docker API on loopback, any path and
			// body) saves and dials nothing.
			post := postModelsForm(member, "/models/providers/new/connect", url.Values{"type": {"http"}, "name": {"Docker"},
				"endpoint": {server.URL + "/containers/create"}, "template": {`{"Image":"{{prompt}}"}`}, "mode": {"sync"},
				"image_path": {"images.0"}, "image_kind": {"base64"}, "check_url": {"/version"}, "scope": {"user"}, "allow_private": {"true"}})
			if post.StatusCode != http.StatusNotFound {
				t.Fatalf("member http connect = %d %.800s", post.StatusCode, post.Body)
			}
			body, ct := multipartForm(t, map[string]string{"type": "comfyui", "name": "Nodes", "endpoint": server.URL, "scope": "user"},
				"workflow", "w.json", []byte(uiComfyWorkflow))
			if r := member.Do(http.MethodPost, "/models/providers/new/connect", body, ct); r.StatusCode != http.StatusNotFound {
				t.Fatalf("member comfyui connect = %d", r.StatusCode)
			}
			var n int
			if err := ta.Deps.DB.Get(t.Context(), &n, `SELECT COUNT(*) FROM providers`); err != nil || n != 0 || calls.Load() != 0 {
				t.Fatalf("member connect stored %d rows, %d calls (%v)", n, calls.Load(), err)
			}

			// An admin connects one for everyone (no scope choice); members
			// can neither reopen nor test it.
			admin := ta.Login(t, ta.Admin.Username)
			form := mainOf(t, admin.Get("/models/providers/new/connect?type=http"))
			mustContain(t, form, `name="scope" value="server"`)
			mustNotContain(t, form, `id="custom-scope"`)
			ok := postModelsForm(admin, "/models/providers/new/connect", url.Values{"type": {"http"}, "name": {"Shared images"},
				"endpoint": {server.URL + "/generate"}, "template": {`{"prompt":"{{prompt}}"}`}, "mode": {"sync"}, "image_path": {"images.0"},
				"image_kind": {"base64"}, "check_url": {"/health"}, "scope": {"user"}})
			if ok.StatusCode != http.StatusSeeOther {
				t.Fatalf("admin connect = %d %.1500s", ok.StatusCode, ok.Body)
			}
			id := providerIDByName(t, ta, "Shared images")
			var scope string
			if err := ta.Deps.DB.Get(t.Context(), &scope, `SELECT scope FROM providers WHERE id = ?`, id); err != nil || scope != "server" {
				t.Fatalf("admin http scope = %q %v", scope, err)
			}
			before := calls.Load()
			for _, r := range []*testutil.Response{
				member.Get("/models/providers/new/connect?id=" + id),
				member.Get("/models/providers/new/test?id=" + id),
				postModelsForm(member, "/models/providers/new/test", url.Values{"id": {id}}),
				postModelsForm(member, "/models/providers/"+id+"/test", url.Values{}),
				postModelsForm(member, "/models/providers/"+id+"/enabled", url.Values{"enabled": {"true"}}),
			} {
				if r.StatusCode != http.StatusNotFound && r.StatusCode != http.StatusForbidden {
					t.Fatalf("member reached the admin's server: %d %.400s", r.StatusCode, r.Body)
				}
			}
			if calls.Load() != before {
				t.Fatal("a member's request dialled the admin's server")
			}
		})
	}
}

func TestReviewTheOnSwitchNeedsAPassedTest(t *testing.T) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			var healthy atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/health" && healthy.Load() {
					_, _ = io.WriteString(w, `{"ok":true}`)
					return
				}
				http.Error(w, "down", http.StatusServiceUnavailable)
			}))
			defer server.Close()
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect), testutil.WithEnv("MV_ALLOW_PRIVATE_PROVIDER_URLS", "true"))
			admin := ta.Login(t, ta.Admin.Username)
			// Step 1 saves the draft and its test fails.
			if r := postModelsForm(admin, "/models/providers/new/connect", url.Values{"type": {"http"}, "name": {"Flaky images"},
				"endpoint": {server.URL + "/generate"}, "template": {`{"prompt":"{{prompt}}"}`}, "mode": {"sync"}, "image_path": {"images.0"},
				"image_kind": {"base64"}, "check_url": {"/health"}}); r.StatusCode != http.StatusSeeOther {
				t.Fatalf("connect = %d %.1500s", r.StatusCode, r.Body)
			}
			id := providerIDByName(t, ta, "Flaky images")
			enabled := func() bool {
				var on bool
				if err := ta.Deps.DB.Get(t.Context(), &on, `SELECT enabled FROM providers WHERE id = ?`, id); err != nil {
					t.Fatal(err)
				}
				return on
			}
			// The G2 switch refuses the failed draft, with and without HTMX.
			if r := postModelsForm(admin, "/models/providers/"+id+"/enabled", url.Values{"enabled": {"true"}}); r.StatusCode != http.StatusSeeOther || enabled() {
				t.Fatalf("a draft whose test failed was turned on: %d", r.StatusCode)
			}
			admin.Header.Set("HX-Request", "true")
			r := postModelsForm(admin, "/models/providers/"+id+"/enabled", url.Values{"enabled": {"true"}})
			admin.Header.Del("HX-Request")
			if r.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(r.Body), "Test this connection before turning it on") || enabled() {
				t.Fatalf("htmx enable of a failed draft = %d %.600s", r.StatusCode, r.Body)
			}
			// Test connection works on the connection while it is off; once it
			// passes, the switch turns it on.
			healthy.Store(true)
			if r := postModelsForm(admin, "/models/providers/"+id+"/test?cap=image", url.Values{}); r.StatusCode != http.StatusSeeOther {
				t.Fatalf("test = %d", r.StatusCode)
			}
			if r := postModelsForm(admin, "/models/providers/"+id+"/enabled?cap=image", url.Values{"enabled": {"true"}}); r.StatusCode != http.StatusSeeOther || !enabled() {
				t.Fatalf("enable after a passed test = %d", r.StatusCode)
			}
		})
	}
}
