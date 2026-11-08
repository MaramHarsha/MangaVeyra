package publish_test

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// chapterFix is a real Studio chapter (script, cast, pages with panels and
// bilingual lettering) owned by a signed-in creator.
type chapterFix struct {
	ta      *testutil.TestApp
	owner   *auth.User
	client  *testutil.Client
	actor   studio.Actor
	project studio.Project
	chapter studio.Chapter
	pages   []pagedoc.Page
	script  studio.ScriptDoc
	rin     studio.Character
	kaito   studio.Character
	base    string
}

const (
	pageW, pageH = 200, 300
)

// newChapter builds a two-page chapter. Page panels link to script panels;
// bubbles are lettered in English and Japanese unless jaMissing is set.
func newChapter(t *testing.T, ta *testutil.TestApp, jaMissing bool) *chapterFix {
	t.Helper()
	ctx := context.Background()
	ss := core.MustUse[studio.Service](ta.Deps)
	f := &chapterFix{ta: ta}
	f.owner = ta.CreateUser(t, "", auth.RoleCreator)
	f.client = ta.Login(t, f.owner.Username)
	var err error
	if f.actor, err = ss.ActorFromUser(ctx, f.owner.ID); err != nil {
		t.Fatal(err)
	}
	if f.project, err = ss.CreateProject(ctx, f.actor, studio.NewProject{Title: "Tidebreak Courier", Logline: "One last tide run.",
		Format: "manga_rtl", Language: "en", PageWidth: pageW, PageHeight: pageH, Genres: []string{"Adventure"}}); err != nil {
		t.Fatal(err)
	}
	if f.chapter, err = ss.CreateChapter(ctx, f.actor, f.project.ID, "The Lighthouse Debt"); err != nil {
		t.Fatal(err)
	}
	if f.rin, err = ss.CreateCharacter(ctx, f.actor, studio.NewCharacter{ProjectID: f.project.ID, Name: "Rin Asagiri", Category: "main"}); err != nil {
		t.Fatal(err)
	}
	if f.kaito, err = ss.CreateCharacter(ctx, f.actor, studio.NewCharacter{ProjectID: f.project.ID, Name: "Kaito Mbeki", Category: "main"}); err != nil {
		t.Fatal(err)
	}
	doc := studio.ScriptDoc{Schema: "mangaveyra.script.v1", ChapterID: f.chapter.ID, ChapterSummary: "Rin reaches the lighthouse.",
		Scenes: []studio.ScriptScene{{Key: "scene-1", Title: "Sea Gate", Summary: "A storm closes in.", TimeOfDay: "dawn",
			Panels: []studio.ScriptPanel{
				{Key: "p1", PageNumber: 1, Camera: "wide", Shot: "establishing", Emotion: "urgent", Action: "Rin sprints along the seawall",
					Dialogue: []studio.ScriptDialogue{{Speaker: "Rin", CharacterID: f.rin.ID, Kind: "speech", Text: "Keep moving!"}},
					SFX:      []studio.ScriptSFX{}, CharacterIDs: []string{f.rin.ID}},
				{Key: "p2", PageNumber: 1, Camera: "close", Shot: "close-up", Emotion: "calm", Action: "Kaito waits at the gate.",
					Dialogue: []studio.ScriptDialogue{}, SFX: []studio.ScriptSFX{}, CharacterIDs: []string{f.kaito.ID}},
			}}}}
	if err := ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
		_, err := ss.ReplaceGeneratedScriptTx(ctx, tx, studio.ReplaceScriptInput{Actor: f.actor, ProjectID: f.project.ID, ChapterID: f.chapter.ID,
			Doc: doc, Provenance: "ai", JobID: db.NewID(), ProviderID: ai.PlaceholderID, ModelID: ai.PlaceholderText})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if f.script, err = ss.GetScript(ctx, f.chapter.ID); err != nil {
		t.Fatal(err)
	}
	sp := f.script.Scenes[0].Panels
	var pages []pagedoc.Page
	for n := 1; n <= 2; n++ {
		p := pagedoc.New(db.NewID(), f.project.ID, f.chapter.ID, n, pageW, pageH, "rtl")
		top, bottom := db.NewID(), db.NewID()
		p.Panels = []pagedoc.Panel{
			{ID: top, Order: 0, PromptSource: "user", ScriptPanelID: sp[0].ID, Layers: []pagedoc.Layer{},
				Points: pagedoc.RectPoints(pagedoc.Rect{X: 10, Y: 10, W: 180, H: 130})},
			{ID: bottom, Order: 1, PromptSource: "user", ScriptPanelID: sp[1].ID, Layers: []pagedoc.Layer{},
				Points: []pagedoc.Point{{X: 10, Y: 150}, {X: 190, Y: 160}, {X: 190, Y: 290}, {X: 10, Y: 290}}},
		}
		ja := func(text string) map[string]pagedoc.BubbleText {
			out := map[string]pagedoc.BubbleText{"en": {Text: text, Status: "approved", Source: "human"}}
			return out
		}
		speech := ja("Keep moving!")
		sfx := ja("SPLASH")
		caption := ja("Dawn at the Sea Gate.")
		if !jaMissing || n == 2 {
			speech["ja"] = pagedoc.BubbleText{Text: "進め！", Status: "approved", Source: "human"}
		}
		if !jaMissing {
			sfx["ja"] = pagedoc.BubbleText{Text: "ザバーン", Status: "approved", Source: "human"}
			caption["ja"] = pagedoc.BubbleText{Text: "海門の夜明け。", Status: "approved", Source: "human"}
		}
		style := pagedoc.BubbleStyle{Size: 14, AutoFit: true}
		p.Bubbles = []pagedoc.Bubble{
			{ID: db.NewID(), PanelID: top, Kind: "speech", SpeakerCharacterID: f.rin.ID, Order: 0, Shape: "ellipse",
				Box: pagedoc.Rect{X: 20, Y: 20, W: 90, H: 50}, Style: style, Texts: speech},
			{ID: db.NewID(), PanelID: top, Kind: "sfx", Order: 1, Shape: "none", Box: pagedoc.Rect{X: 110, Y: 80, W: 70, H: 40}, Style: style, Texts: sfx},
			{ID: db.NewID(), PanelID: bottom, Kind: "caption", Order: 2, Shape: "rect", Box: pagedoc.Rect{X: 20, Y: 170, W: 120, H: 40}, Style: style, Texts: caption},
		}
		pages = append(pages, p)
	}
	ps := core.MustUse[pagestore.Service](ta.Deps)
	if err := ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
		_, err := ps.CreatePagesTx(ctx, tx, f.chapter.ID, pages, pagestore.Change{ActorKind: "user", ActorID: f.owner.ID, Action: "create"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if f.pages, err = ps.ChapterPages(ctx, f.chapter.ID); err != nil {
		t.Fatal(err)
	}
	if f.chapter, err = ss.GetChapter(ctx, f.chapter.ID); err != nil {
		t.Fatal(err)
	}
	f.base = "/studio/p/" + f.project.ID + "/chapters/" + strconv.Itoa(f.chapter.Number)
	return f
}

// publishForm is the E1 form as a browser posts it.
func (f *chapterFix) publishForm(lang string, extra ...string) url.Values {
	v := url.Values{"title": {"The Lighthouse Debt"}, "synopsis": {"Rin reaches the lighthouse at last."}, "tags": {"rooftops, salt city"},
		"lang": {lang}, "page_lang": {lang}, "age_rating": {publish.RatingTeen}, "licence": {"all_rights"},
		"visibility": {publish.VisibilityPublic}, "when": {"now"}, "notify": {"on"},
		"ai_script": {"ai"}, "ai_art": {"human"}, "ai_lettering": {"human"}}
	for i := 0; i+1 < len(extra); i += 2 {
		v.Set(extra[i], extra[i+1])
	}
	return v
}

func postForm(c *testutil.Client, path string, form url.Values) *testutil.Response {
	return c.Do(http.MethodPost, path, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
}

// publish posts E1 and runs the queued job; it returns the finished job.
func (f *chapterFix) publish(t *testing.T, form url.Values) *jobs.Job {
	t.Helper()
	resp := postForm(f.client, f.base+"/publish", form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("publish POST = %d %s", resp.StatusCode, resp.Body)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Query().Get("job") == "" {
		t.Fatalf("publish redirect %q", resp.Header.Get("Location"))
	}
	return runJob(t, f.ta, loc.Query().Get("job"))
}

func runJob(t *testing.T, ta *testutil.TestApp, id string) *jobs.Job {
	t.Helper()
	ctx := context.Background()
	if ran, err := ta.Deps.Executor.RunOnce(ctx); err != nil || !ran {
		t.Fatalf("RunOnce = %v, %v", ran, err)
	}
	j, err := ta.Deps.Jobs.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// fakeFollowers is a FollowerSource listing fixed users, recording calls.
type fakeFollowers struct {
	mu    sync.Mutex
	users map[string][]string
	calls int
}

func (f *fakeFollowers) Followers(_ context.Context, seriesID, cursor string, limit int) ([]string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	all := f.users[seriesID]
	start, _ := strconv.Atoi(cursor)
	end := min(start+limit, len(all))
	next := ""
	if end < len(all) {
		next = strconv.Itoa(end)
	}
	return append([]string(nil), all[start:end]...), next, nil
}

func notifications(t *testing.T, ta *testutil.TestApp, kind string) map[string]int {
	t.Helper()
	var rows []struct {
		UserID string `db:"user_id"`
	}
	if err := ta.Deps.DB.Select(context.Background(), &rows, `SELECT user_id FROM notifications WHERE kind = ?`, kind); err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, r := range rows {
		out[r.UserID]++
	}
	return out
}
