package studio_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestCreateProjectOwnerMembershipAndPrivateSource(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		svc := studioService(ta)
		owner := ta.CreateUser(t, "", auth.RoleCreator)
		actor, err := svc.ActorFromUser(ctx, owner.ID)
		if err != nil {
			t.Fatal(err)
		}
		const source = "Private draft: the gate opens at low tide."
		project, err := svc.CreateProject(ctx, actor, newProjectInput("Tide Gate", source))
		if err != nil {
			t.Fatalf("CreateProject: %v", err)
		}
		var role string
		if err := ta.Deps.DB.Get(ctx, &role, `SELECT role FROM project_members WHERE project_id = ? AND user_id = ?`, project.ID, owner.ID); err != nil {
			t.Fatalf("owner membership was not committed with project: %v", err)
		}
		if role != studio.RoleOwner {
			t.Fatalf("owner role = %q", role)
		}
		gotSource, err := svc.SourceText(ctx, project.ID)
		if err != nil || gotSource != source {
			t.Fatalf("SourceText = %q, %v", gotSource, err)
		}
		encoded, err := json.Marshal(project)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(encoded, []byte(source)) || bytes.Contains(encoded, []byte("script_source_text")) {
			t.Fatalf("project DTO leaked source: %s", encoded)
		}

		projects, err := svc.ListProjects(ctx, owner.ID, 10)
		if err != nil || len(projects) != 1 || projects[0].Role != studio.RoleOwner {
			t.Fatalf("ListProjects = %#v, %v", projects, err)
		}
		second, err := svc.CreateProject(ctx, actor, newProjectInput("Tide Gate", ""))
		if err != nil {
			t.Fatal(err)
		}
		if second.Slug == project.Slug || !strings.HasPrefix(second.Slug, project.Slug+"-") {
			t.Fatalf("colliding slugs = %q, %q", project.Slug, second.Slug)
		}
	})
}

func TestCreateProjectAuthorizationAndValidationAreAtomic(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		svc := studioService(ta)
		reader := ta.CreateUser(t, "", auth.RoleReader)
		before := 0
		if err := ta.Deps.DB.Get(ctx, &before, `SELECT COUNT(*) FROM projects`); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.CreateProject(ctx, studio.Actor{UserID: reader.ID, SiteRole: auth.RoleAdmin}, newProjectInput("Spoofed", "")); !errors.Is(err, studio.ErrForbidden) {
			t.Fatalf("reader create error = %v, want ErrForbidden", err)
		}
		tooLarge := newProjectInput("Too Large", strings.Repeat("x", (1<<20)+1))
		creator := ta.CreateUser(t, "", auth.RoleCreator)
		if _, err := svc.CreateProject(ctx, studio.Actor{UserID: creator.ID}, tooLarge); !errors.Is(err, studio.ErrInvalid) {
			t.Fatalf("large source error = %v, want ErrInvalid", err)
		}
		after := 0
		if err := ta.Deps.DB.Get(ctx, &after, `SELECT COUNT(*) FROM projects`); err != nil {
			t.Fatal(err)
		}
		if after != before {
			t.Fatalf("failed creates changed project count: before=%d after=%d", before, after)
		}

		_, restricted, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: creator.ID, Kind: auth.TokenAgent,
			Name: "restricted creator", Scopes: []string{"projects:write"}, ProjectIDs: []string{"existing-only"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.CreateProject(ctx, studio.Actor{UserID: creator.ID, TokenID: restricted.ID}, newProjectInput("Restricted", "")); !errors.Is(err, studio.ErrForbidden) {
			t.Fatalf("restricted token create error = %v, want ErrForbidden", err)
		}
	})
}

func TestCreateProjectAcceptsContentLanguagesOnly(t *testing.T) {
	forEachDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		svc := studioService(ta)
		owner := ta.CreateUser(t, "", auth.RoleCreator)
		actor, err := svc.ActorFromUser(ctx, owner.ID)
		if err != nil {
			t.Fatal(err)
		}
		create := func(lang string) (studio.Project, error) {
			in := newProjectInput("Harbour Lights "+lang, "")
			in.Language = lang
			return svc.CreateProject(ctx, actor, in)
		}
		for input, want := range map[string]string{
			"en": "en", "ja": "ja", "ko": "ko", "zh-Hans": "zh-Hans", "zh-Hant": "zh-Hant", "es": "es", "fr": "fr",
			"de": "de", "pt": "pt", "hi": "hi", "": "en", " ko-KR ": "ko", "zh-TW": "zh-Hant", "zh_CN": "zh-Hans",
			"zh": "zh-Hans", "pt-BR": "pt", "JA": "ja", "hi-IN": "hi", "en-US-x-private": "en",
		} {
			project, err := create(input)
			if err != nil {
				t.Fatalf("language %q rejected: %v", input, err)
			}
			var stored string
			if err := ta.Deps.DB.Get(ctx, &stored, `SELECT language FROM projects WHERE id = ?`, project.ID); err != nil {
				t.Fatal(err)
			}
			if project.Language != want || stored != want {
				t.Fatalf("language %q stored as %q/%q, want %q", input, project.Language, stored, want)
			}
		}
		for _, input := range []string{"ru", "klingon", "x-private", "ar", "zh-Latn-x", "<script>"} {
			if _, err := create(input); !errors.Is(err, studio.ErrInvalid) || !strings.Contains(err.Error(), "language") {
				t.Fatalf("language %q error = %v, want a language validation error", input, err)
			}
		}
	})
}
