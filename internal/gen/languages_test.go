package gen_test

import (
	"context"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/lettering"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// With no model connected, the placeholder script is written in the project's
// language with dialogue, and "Make pages" letters it into real bubbles that
// lay out without overflow; vertical lettering follows VerticalByDefault.
func TestOfflinePlaceholderLettersEveryContentLanguage(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		engine := core.MustUse[lettering.Engine](ta.Deps)
		pages := core.MustUse[pagestore.Service](ta.Deps)
		for _, lang := range pagedoc.ContentLanguages {
			t.Run(lang, func(t *testing.T) {
				f := newGenFixture(t, ta, "", 0)
				project, err := f.studio.CreateProject(ctx, f.actor, studio.NewProject{Title: "Lantern Quay " + lang, Premise: "",
					Style: "bw_screentone", Format: "manga_rtl", PageSize: "b5", Language: lang, PageWidth: 1600, PageHeight: 2400})
				if err != nil {
					t.Fatal(err)
				}
				chapter, err := f.studio.CreateChapter(ctx, f.actor, project.ID, "Storm Bell")
				if err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"Rin Asagiri", "Pip the Gull"} {
					if _, err := f.studio.CreateCharacter(ctx, f.actor, studio.NewCharacter{ProjectID: project.ID, Name: name, Role: "courier", Category: "main"}); err != nil {
						t.Fatal(err)
					}
				}
				j, err := f.gen.EnqueueScript(ctx, f.actor, gen.ScriptRequest{ProjectID: project.ID, ChapterID: chapter.ID, Mode: "premise", Premise: "A courier crosses the harbour before the storm.", PagesTarget: 3})
				if err != nil {
					t.Fatal(err)
				}
				if done := runArtJob(t, ta, j.ID); done.State != jobs.StateDone || done.ProviderID != "placeholder" {
					t.Fatalf("offline script: %+v %v", done.State, done.Error)
				}
				j, err = f.gen.EnqueuePagesFromScript(ctx, f.actor, gen.PagesFromScriptRequest{ProjectID: project.ID, ChapterID: chapter.ID, Letter: true})
				if err != nil {
					t.Fatal(err)
				}
				if done := runArtJob(t, ta, j.ID); done.State != jobs.StateDone {
					t.Fatalf("pages job: %v", done.Error)
				}
				docs, err := pages.ChapterPages(ctx, chapter.ID)
				if err != nil || len(docs) != 3 {
					t.Fatalf("pages: %d %v", len(docs), err)
				}
				bubbles := 0
				for _, p := range docs {
					for _, b := range p.Bubbles {
						bubbles++
						text, ok := b.Texts[lang]
						if !ok || text.Text == "" || text.Source != pagedoc.ProvenanceAI {
							t.Fatalf("bubble texts %v", b.Texts)
						}
						if b.Vertical != pagedoc.VerticalByDefault(lang) {
							t.Fatalf("vertical = %v", b.Vertical)
						}
						l, err := engine.Layout(b, lang)
						if err != nil || l.Overflow || l.TextPath == "" {
							t.Fatalf("layout %q: overflow=%v %v", text.Text, l.Overflow, err)
						}
					}
				}
				if bubbles < 3 {
					t.Fatalf("only %d bubbles were lettered", bubbles)
				}
			})
		}
	})
}
