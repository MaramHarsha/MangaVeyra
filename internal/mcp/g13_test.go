package mcp_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

// TestG13AuthorizationAndTokenForm: G13 is session-only, decisions belong to
// the token owner or the project owner, and the Create token form shows the
// secret once.
func TestG13AuthorizationAndTokenForm(t *testing.T) {
	forEachDialect(t, func(t *testing.T, f *fixture) {
		anon := f.ta.NewClient()
		if r := anon.Get("/agents"); r.StatusCode != http.StatusSeeOther || !strings.HasPrefix(r.Header.Get("Location"), "/login") {
			t.Fatalf("anonymous G13 %d %q", r.StatusCode, r.Header.Get("Location"))
		}
		raw, _ := f.token("bearer", scopesGenerate, nil, nil)
		if r := f.ta.BearerClient(raw).Get("/agents"); r.StatusCode != http.StatusForbidden {
			t.Fatalf("bearer G13 %d", r.StatusCode)
		}
		if r := form(f.ta.BearerClient(raw), "/agents/tokens", url.Values{"name": {"escalate"}, "edit": {"on"}, "generate": {"on"}, "expires": {"never"}}); r.StatusCode != http.StatusForbidden {
			t.Fatalf("bearer token form %d", r.StatusCode)
		}

		// Create token: validation, then the secret shown exactly once.
		bad := form(f.browser, "/agents/tokens", url.Values{"name": {"Claude desktop · Jerry"}, "projects": {"some"}, "generate": {"on"}, "approve_over": {"lots"}, "expires": {"90"}})
		if bad.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("invalid form %d", bad.StatusCode)
		}
		for _, want := range []string{"Choose at least one project", "Generating changes pages, so it also needs Edit.", "Enter an amount from $0 to $1,000", `mv-modal--static`} {
			if !strings.Contains(string(bad.Body), want) {
				t.Errorf("invalid form lacks %q", want)
			}
		}
		good := form(f.browser, "/agents/tokens", url.Values{"name": {"Claude desktop · Jerry"}, "projects": {"some"}, "project_ids": {f.project.ID},
			"edit": {"on"}, "generate": {"on"}, "approve_over": {"0.50"}, "expires": {"90"}})
		if good.StatusCode != http.StatusCreated || good.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("create token %d %q", good.StatusCode, good.Header.Get("Cache-Control"))
		}
		secret := regexp.MustCompile(`mva_[A-Za-z0-9_-]{20,}`).FindString(string(good.Body))
		if secret == "" || !strings.Contains(string(good.Body), "mangaveyra mcp-stdio --url ") {
			t.Fatal("secret or stdio command not shown")
		}
		tokens, err := f.ta.Deps.Auth.ListTokens(f.ctx, f.owner.ID, auth.TokenAgent)
		if err != nil {
			t.Fatal(err)
		}
		var created *auth.TokenInfo
		for i := range tokens {
			if tokens[i].Name == "Claude desktop · Jerry" {
				created = &tokens[i]
			}
		}
		if created == nil || strings.Join(created.Scopes.V, ",") != "projects:read,projects:write,generate" ||
			len(created.ProjectIDs.V) != 1 || created.ProjectIDs.V[0] != f.project.ID || !created.ApproveOverUSD.Valid ||
			created.ApproveOverUSD.Float64 != 0.5 || created.ExpiresAt == nil || created.ExpiresAt.Sub(time.Now()) < 89*24*time.Hour {
			t.Fatalf("created token %+v", created)
		}
		later := string(f.browser.Get("/agents").Body)
		if strings.Contains(later, secret) {
			t.Fatal("the secret is shown again")
		}
		if !strings.Contains(later, "Tidebreak Courier · edit + generate") || !strings.Contains(later, "Approve over $0.50") {
			t.Fatal("token row not rendered as designed")
		}
		if out := mustCall(t, f.connect(secret), "list_projects", nil)["projects"].([]any); len(out) != 1 {
			t.Fatalf("form token works with %d projects", len(out))
		}

		// A request from the agent of a project member reaches the project owner.
		member := f.ta.CreateUser(t, "", auth.RoleCreator)
		if _, err := f.ta.Deps.DB.Exec(f.ctx, `INSERT INTO project_members (project_id, user_id, role) VALUES (?, ?, 'writer')`, f.project.ID, member.ID); err != nil {
			t.Fatal(err)
		}
		memberRaw, _, err := f.ta.Deps.Auth.CreateToken(f.ctx, auth.TokenInput{UserID: member.ID, Kind: auth.TokenAgent, Name: "Member agent", Scopes: scopesEdit})
		if err != nil {
			t.Fatal(err)
		}
		ask := mustCall(t, f.connect(memberRaw), "request_human_action", map[string]any{"project_id": f.project.ID, "action": "publish", "chapter_id": f.chapter.ID})
		id := str(ask["request_id"])
		if !strings.Contains(string(f.browser.Get("/agents").Body), "Asked to publish ch. 1") {
			t.Fatal("project owner does not see the member agent's request")
		}
		if f.count(`SELECT COUNT(*) FROM notifications WHERE user_id = ? AND kind = 'agent_request'`, f.owner.ID) == 0 ||
			f.count(`SELECT COUNT(*) FROM notifications WHERE user_id = ? AND kind = 'agent_request'`, member.ID) == 0 {
			t.Fatal("token owner and project owner must both be notified")
		}
		// Someone else cannot see or decide it.
		_, outsider := f.ta.UserClient(t, auth.RoleCreator)
		if strings.Contains(string(outsider.Get("/agents").Body), "Asked to publish ch. 1") {
			t.Fatal("an outsider sees the request")
		}
		for _, action := range []string{"approve", "decline", "undo"} {
			if r := form(outsider, "/agents/actions/"+id+"/"+action, nil); r.StatusCode != http.StatusNotFound {
				t.Fatalf("outsider %s %d", action, r.StatusCode)
			}
		}
		if f.actionStatus(id) != "pending" {
			t.Fatal("outsider changed the request")
		}
		if r := form(f.browser, "/agents/actions/"+id+"/decline", nil); r.StatusCode != http.StatusSeeOther || f.actionStatus(id) != "declined" {
			t.Fatalf("project owner decline %d", r.StatusCode)
		}

		// Revoke: only your own agent tokens.
		if r := form(outsider, "/agents/tokens/"+created.ID+"/revoke", nil); r.StatusCode != http.StatusNotFound {
			t.Fatalf("outsider revoke %d", r.StatusCode)
		}
		if r := form(f.browser, "/agents/tokens/"+created.ID+"/revoke", nil); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("revoke %d", r.StatusCode)
		}
		if strings.Contains(string(f.browser.Get("/agents").Body), `data-token-id="`+created.ID+`"`) {
			t.Fatal("revoked token still listed")
		}

		// Japanese.
		c := f.ta.Login(t, f.owner.Username)
		c.Header.Set("Accept-Language", "ja")
		jaBody := string(c.Get("/agents").Body)
		for _, want := range []string{"エージェントとMCP", "必ず人が行うこと", "最近のエージェント操作", "第1話の公開を依頼"} {
			if !strings.Contains(jaBody, want) {
				t.Errorf("Japanese G13 lacks %q", want)
			}
		}
	})
}

// TestLocaleKeysExistInEnglishAndJapanese checks every agents.* key the
// package uses, including the dynamically built ones.
func TestLocaleKeysExistInEnglishAndJapanese(t *testing.T) {
	keyRe := regexp.MustCompile(`"(agents\.[a-z0-9_.]+)"`)
	used := map[string]bool{}
	files, _ := filepath.Glob("*.go")
	templs, _ := filepath.Glob("*.templ")
	for _, p := range append(files, templs...) {
		if strings.HasSuffix(p, "_templ.go") || strings.HasSuffix(p, "_test.go") {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range keyRe.FindAllStringSubmatch(string(b), -1) {
			if !strings.HasSuffix(m[1], ".") {
				used[m[1]] = true
			}
		}
	}
	delete(used, "agents.mcp") // the Allow AI agents preference flag, not a message
	for _, prefix := range []string{"agents.action.generate.", "agents.ask.generate."} {
		for _, k := range []string{"script", "page", "region", "other"} {
			used[prefix+k] = true
		}
	}
	for _, lang := range []string{"en", "ja"} {
		b, err := os.ReadFile(filepath.Join("..", "i18n", "locales", lang, "mcp.json"))
		if err != nil {
			t.Fatal(err)
		}
		cat := map[string]string{}
		if err := json.Unmarshal(b, &cat); err != nil {
			t.Fatal(err)
		}
		for k := range used {
			if _, ok := cat[k]; !ok {
				if _, ok := cat[k+".other"]; !ok {
					t.Errorf("%s lacks %s", lang, k)
				}
			}
		}
		if lang == "ja" {
			if got := i18n.Default().Translate("ja", "agents.title"); got == "agents.title" {
				t.Error("ja catalogue not embedded")
			}
		}
	}
}
