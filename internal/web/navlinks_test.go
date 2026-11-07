package web_test

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
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
	ta := testutil.NewTestApp(t)
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

	signedIn := map[string]string{
		"dashboard":     "/studio",
		"new project":   "/studio/new/premise",
		"story":         "/studio/p/" + project.ID + "/story/" + chapter.ID,
		"models":        "/models",
		"connect OR":    "/models/providers/new?kind=openrouter",
		"connect OAI":   "/models/providers/new?kind=openai",
		"onboarding":    "/onboarding/1",
		"offline":       "/offline",
		"404":           "/this-page-is-not-built",
		"settings":      "/settings",
		"account":       "/settings/account",
		"appearance":    "/settings/appearance",
		"language":      "/settings/language",
		"reading":       "/settings/reading",
		"notifications": "/settings/notifications",
		"accessibility": "/settings/accessibility",
		"world":         "/settings/world",
		"data":          "/settings/data",
	}
	signedOut := map[string]string{"login": "/login", "signup": "/signup", "forgot": "/forgot", "404 (signed out)": "/nothing-here"}

	check := func(t *testing.T, client *testutil.Client, name, page string) {
		t.Helper()
		res := client.Get(page)
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
