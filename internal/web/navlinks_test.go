package web_test

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

var (
	linkTarget = regexp.MustCompile(`\s(?:href|hx-get|hx-post)="(/[^"]*)"`)
	formTag    = regexp.MustCompile(`<form\b[^>]*>`)
	formAction = regexp.MustCompile(`\saction="(/[^"]*)"`)
	formMethod = regexp.MustCompile(`\smethod="([a-zA-Z]+)"`)
	hxPost     = regexp.MustCompile(`\shx-post="(/[^"]*)"`)
)

// Design fidelity (AGENTS.md, ADR 0004 §7): navigation keeps every designed
// item, but no link, HTMX target or form on a built page may lead to a path the
// server does not serve. Unbuilt destinations must render in the Soon state.
func TestBuiltPagesHaveNoDeadLinks(t *testing.T) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) { checkBuiltPages(t, dialect) })
	}
}

func checkBuiltPages(t *testing.T, dialect string) {
	ta := testutil.NewTestApp(t, testutil.WithDialect(dialect))
	ctx := context.Background()
	svc := core.MustUse[studio.Service](ta.Deps)
	actor := studio.Actor{UserID: ta.Admin.ID}
	project, err := svc.CreateProject(ctx, actor, studio.NewProject{Title: "Lantern Ferry", Premise: "A ferry that only sails at dusk.",
		Style: "bw_screentone", Format: "manga_rtl", Language: "en"})
	if err != nil {
		t.Fatal(err)
	}
	chapter, err := svc.CreateChapter(ctx, actor, project.ID, "Dusk Crossing")
	if err != nil {
		t.Fatal(err)
	}
	character, err := svc.CreateCharacter(ctx, actor, studio.NewCharacter{ProjectID: project.ID, Name: "Rin Asagiri", Category: "main"})
	if err != nil {
		t.Fatal(err)
	}
	var page pagedoc.Page
	if err := ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
		doc := pagedoc.New(db.NewID(), project.ID, chapter.ID, 1, 400, 600, pagedoc.DirRTL)
		doc.Panels = []pagedoc.Panel{{ID: db.NewID(), Points: pagedoc.RectPoints(pagedoc.Rect{X: 20, Y: 20, W: 360, H: 560}), PromptSource: "user", Layers: []pagedoc.Layer{{ID: db.NewID(), Kind: pagedoc.LayerBackground, Visible: true, Opacity: 1, Provenance: pagedoc.ProvenanceHuman}}}}
		pages, err := core.MustUse[pagestore.Service](ta.Deps).CreatePagesTx(ctx, tx, chapter.ID, []pagedoc.Page{doc}, pagestore.Change{ActorKind: "user", ActorID: actor.UserID, Action: "save"})
		if err == nil {
			page = pages[0]
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// A disabled local draft permits every G12 screen to render without
	// contacting a provider. The fixture is at the final wizard stage.
	providerID := db.NewID()
	if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO providers (id, kind, name, base_url, config, scope, enabled, last_test)
		VALUES (?, 'openai-compatible-local', 'Lantern local test', 'http://127.0.0.1:11434/v1', ?, 'server', FALSE, ?)`,
		providerID, `{"model":"lantern-test","context_tokens":32768,"output_tokens":4096}`, `{"step":"route","state":"ok"}`); err != nil {
		t.Fatal(err)
	}
	signedIn := map[string]string{
		"dashboard":                "/studio",
		"new project":              "/studio/new/premise",
		"story":                    "/studio/p/" + project.ID + "/story/" + chapter.ID,
		"models":                   "/models",
		"job queue":                "/queue",
		"notification center":      "/notifications",
		"connect OR":               "/models/providers/new?kind=openrouter",
		"connect OAI":              "/models/providers/new?kind=openai",
		"connect fal":              "/models/providers/new/fal",
		"model routing":            "/models/routing",
		"custom provider":          "/models/providers/new/connect",
		"custom provider test":     "/models/providers/new/test?id=" + providerID,
		"custom provider describe": "/models/providers/new/describe?id=" + providerID,
		"custom provider route":    "/models/providers/new/route?id=" + providerID,
		"project library":          "/studio/projects",
		"global characters":        "/studio/characters",
		"global setting":           "/studio/setting",
		"character bible":          "/studio/p/" + project.ID + "/characters",
		"create character":         "/studio/p/" + project.ID + "/characters/new",
		"character detail":         "/studio/p/" + project.ID + "/characters/" + character.ID,
		"character profile":        "/studio/p/" + project.ID + "/characters/" + character.ID + "?edit=1",
		"setting bible":            "/studio/p/" + project.ID + "/setting",
		"chapter export":           "/studio/p/" + project.ID + "/chapters/" + chapter.ID + "/export",
		"onboarding":               "/onboarding/1",
		"offline":                  "/offline",
		"404":                      "/this-page-is-not-built",
		"settings":                 "/settings",
		"account":                  "/settings/account",
		"appearance":               "/settings/appearance",
		"language":                 "/settings/language",
		"reading":                  "/settings/reading",
		"notifications":            "/settings/notifications",
		"accessibility":            "/settings/accessibility",
		"world":                    "/settings/world",
		"data":                     "/settings/data",
		"publish flow":             "/studio/p/" + project.ID + "/chapters/" + chapter.ID + "/publish",
		"series settings":          "/studio/p/" + project.ID + "/series",
		"explore":                  "/explore",
		"search":                   "/search?q=ferry",
		"analytics":                "/studio/analytics",
	}
	editorURL := "/studio/p/" + project.ID + "/chapters/" + chapter.ID + "/edit?page=" + page.ID
	signedIn["page editor"] = editorURL
	signedIn["layout picker"] = editorURL + "&panel=layouts"
	signedIn["lettering editor"] = editorURL + "&mode=lettering"
	signedIn["variations tray"] = editorURL + "&panel=variations"
	signedIn["region editor"] = editorURL + "&tool=region"
	signedOut := map[string]string{"login": "/login", "signup": "/signup", "forgot": "/forgot", "404 (signed out)": "/nothing-here"}

	check := func(t *testing.T, client *testutil.Client, name, page string) {
		t.Helper()
		res := client.Get(page)
		mustLoad := strings.HasPrefix(name, "custom provider ") ||
			name == "publish flow" || name == "series settings" || name == "explore" || name == "search" || name == "analytics"
		if mustLoad && res.StatusCode != http.StatusOK {
			t.Fatalf("%s (%s) = %d", name, page, res.StatusCode)
		}
		if res.StatusCode >= 500 {
			t.Fatalf("%s (%s) = %d", name, page, res.StatusCode)
		}
		body := string(res.Body)
		for _, m := range linkTarget.FindAllStringSubmatch(body, -1) {
			method := http.MethodGet
			if strings.HasPrefix(strings.TrimSpace(m[0]), "hx-post") {
				method = http.MethodPost
			}
			assertServed(t, ta, name, method, m[1])
		}
		for _, tag := range formTag.FindAllString(body, -1) {
			action := formAction.FindStringSubmatch(tag)
			if action == nil {
				continue
			}
			method := http.MethodGet
			if mm := formMethod.FindStringSubmatch(tag); mm != nil {
				method = strings.ToUpper(mm[1])
			}
			if hxPost.MatchString(tag) {
				method = http.MethodPost
			}
			assertServed(t, ta, name, method, action[1])
		}
	}

	admin := ta.Login(t, ta.Admin.Username)
	for name, page := range signedIn {
		t.Run(name, func(t *testing.T) { check(t, admin, name, page) })
	}
	story := admin.Get(signedIn["story"])
	for _, target := range []string{"/studio/p/" + project.ID + "/characters", "/studio/p/" + project.ID + "/setting"} {
		if !strings.Contains(string(story.Body), `href="`+target+`"`) {
			t.Errorf("project context missing from sidebar: %s", target)
		}
	}
	anon := ta.NewClient()
	for name, page := range signedOut {
		t.Run(name, func(t *testing.T) { check(t, anon, name, page) })
	}
}

func assertServed(t *testing.T, ta *testutil.TestApp, page, method, target string) {
	t.Helper()
	path := target
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	if strings.HasPrefix(path, "/static/") || path == "/manifest.webmanifest" || path == "/sw.js" || path == "" {
		return
	}
	if !ta.Router.Serves(method, path) {
		t.Errorf("%s links to %s %s, which this server does not serve; render it in the Soon state", page, method, target)
	}
}
