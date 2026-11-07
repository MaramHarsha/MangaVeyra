package gen_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

func forEachGenDialect(t *testing.T, fn func(*testing.T, *testutil.TestApp)) {
	t.Helper()
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			fn(t, testutil.NewTestApp(t, testutil.WithDialect(dialect)))
		})
	}
}

type fakeTextService struct {
	mu          sync.Mutex
	planFn      func(context.Context, ai.TextPlanRequest) (ai.TextPlan, error)
	generateFn  func(context.Context, ai.TextGenerateRequest) (ai.TextResult, error)
	plans       []ai.TextPlanRequest
	generations []ai.TextGenerateRequest
}

func (f *fakeTextService) PlanText(ctx context.Context, request ai.TextPlanRequest) (ai.TextPlan, error) {
	f.mu.Lock()
	f.plans = append(f.plans, clonePlanRequest(request))
	fn := f.planFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, request)
	}
	mode := request.RoutingMode
	if mode == "" {
		mode = ai.ModeBalanced
	}
	return placeholderTextPlan(mode), nil
}

func (f *fakeTextService) GenerateText(ctx context.Context, request ai.TextGenerateRequest) (ai.TextResult, error) {
	f.mu.Lock()
	f.generations = append(f.generations, cloneGenerateRequest(request))
	fn := f.generateFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, request)
	}
	return ai.TextResult{Document: generatedScriptJSON("", "The courier crosses the gate."),
		ProviderID: request.Plan.ProviderID, ModelID: request.Plan.ModelID}, nil
}

func (f *fakeTextService) planRequests() []ai.TextPlanRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ai.TextPlanRequest(nil), f.plans...)
}

func (f *fakeTextService) generateRequests() []ai.TextGenerateRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ai.TextGenerateRequest(nil), f.generations...)
}

func clonePlanRequest(in ai.TextPlanRequest) ai.TextPlanRequest {
	in.Input = append(json.RawMessage(nil), in.Input...)
	return in
}

func cloneGenerateRequest(in ai.TextGenerateRequest) ai.TextGenerateRequest {
	in.Input = append(json.RawMessage(nil), in.Input...)
	in.Plan.Decision = append(json.RawMessage(nil), in.Plan.Decision...)
	return in
}

func placeholderTextPlan(mode string) ai.TextPlan {
	return ai.TextPlan{Task: ai.TaskScript, Schema: ai.SchemaScriptV1, RoutingMode: mode,
		ProviderID: ai.PlaceholderID, ModelID: ai.PlaceholderText,
		Decision: json.RawMessage(`{"mode":"` + mode + `","provider_id":"placeholder","model_id":"placeholder-text","remote":false}`)}
}

func remoteTextPlan(mode, providerID string, estimateUSD float64) ai.TextPlan {
	return ai.TextPlan{Task: ai.TaskScript, Schema: ai.SchemaScriptV1, RoutingMode: mode,
		ProviderID: providerID, ModelID: "openai/gpt-4.1-mini", Remote: true,
		Decision: json.RawMessage(`{"mode":"` + mode + `","provider_id":"` + providerID + `","model_id":"openai/gpt-4.1-mini","remote":true}`),
		Estimate: jobs.Cost{USD: estimateUSD}}
}

const gateAScriptMaxAttempts = 3
const gateAScriptPayloadVersion = 2

type storedScriptPayload struct {
	Version           int            `json:"version"`
	ExpectedUpdatedAt time.Time      `json:"expected_updated_at"`
	TokenID           string         `json:"token_id"`
	RoutingMode       string         `json:"routing_mode"`
	Remote            bool           `json:"remote"`
	Input             ai.ScriptInput `json:"input"`
}

type storedScriptOutput struct {
	ChapterID string `json:"chapter_id"`
	Scenes    int    `json:"scenes"`
	Panels    int    `json:"panels"`
	Pages     int    `json:"pages"`
	Provider  string `json:"provider_id"`
	Model     string `json:"model_id"`
}

type genFixture struct {
	ta         *testutil.TestApp
	owner      *auth.User
	actor      studio.Actor
	project    studio.Project
	chapter    studio.Chapter
	characters []studio.Character
	studio     studio.Service
	gen        gen.Service
}

func newGenFixture(t *testing.T, ta *testutil.TestApp, source string, characterCount int) *genFixture {
	t.Helper()
	ctx := context.Background()
	owner := ta.CreateUser(t, "", auth.RoleCreator)
	studioService := core.MustUse[studio.Service](ta.Deps)
	actor, err := studioService.ActorFromUser(ctx, owner.ID)
	if err != nil {
		t.Fatalf("ActorFromUser: %v", err)
	}
	project, err := studioService.CreateProject(ctx, actor, studio.NewProject{
		Title: "Tideglass " + db.NewID()[:6], Premise: "A courier races the closing tide gate.",
		Logline: "One last delivery.", Style: "bw_screentone", Format: "manga_rtl", PageSize: "b5",
		Language: "en", Genres: []string{"adventure", "mystery"}, Tags: []string{"original"},
		PageWidth: 1600, PageHeight: 2400, SourceText: source,
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	chapter, err := studioService.CreateChapter(ctx, actor, project.ID, "The Closing Gate")
	if err != nil {
		t.Fatalf("CreateChapter: %v", err)
	}
	characters := make([]studio.Character, 0, characterCount)
	for i := 0; i < characterCount; i++ {
		character, err := studioService.CreateCharacter(ctx, actor, studio.NewCharacter{
			ProjectID: project.ID, Name: []string{"Rin", "Kaito", "Sora"}[i%3], Role: "courier",
			Look: "Rain cape and brass compass", Personality: "Steadfast", Category: "main",
		})
		if err != nil {
			t.Fatalf("CreateCharacter: %v", err)
		}
		characters = append(characters, character)
	}
	genService := core.MustUse[gen.Service](ta.Deps)
	// A fixture may wrap Gen's private Studio dependency to force a transaction
	// rollback; every new fixture restores the app-owned implementation.
	gen.SetStudioServiceForTest(genService, studioService)
	return &genFixture{ta: ta, owner: owner, actor: actor, project: project, chapter: chapter,
		characters: characters, studio: studioService, gen: genService}
}

func (f *genFixture) useText(fake *fakeTextService) { gen.SetTextServiceForTest(f.gen, fake) }

func generatedScriptJSON(characterID, action string) json.RawMessage {
	dialogue := []map[string]any{}
	characterIDs := []string{}
	if characterID != "" {
		dialogue = append(dialogue, map[string]any{"speaker": "Rin", "character_id": characterID,
			"kind": "speech", "text": "The tide is turning."})
		characterIDs = append(characterIDs, characterID)
	}
	doc := map[string]any{
		"schema": "mangaveyra.script.v1", "chapter_summary": "The delivery begins.",
		"scenes": []any{map[string]any{
			"key": "scene-1", "title": "Sea Gate", "summary": "A storm closes in.", "time_of_day": "dawn",
			"panels": []any{map[string]any{
				"key": "panel-1", "page_number": 1, "camera": "wide", "shot": "establishing",
				"emotion": "urgent", "action": action, "dialogue": dialogue,
				"sfx":           []any{map[string]any{"text": "SPLASH", "note": "distant"}},
				"character_ids": characterIDs, "notes": "",
			}},
		}},
	}
	raw, _ := json.Marshal(doc)
	return raw
}

func studioScript(characterID, action string) studio.ScriptDoc {
	panel := studio.ScriptPanel{Key: "panel-old", PageNumber: 1, Camera: "wide", Shot: "establishing",
		Emotion: "calm", Action: action, Dialogue: []studio.ScriptDialogue{}, SFX: []studio.ScriptSFX{}, CharacterIDs: []string{}}
	if characterID != "" {
		panel.Dialogue = []studio.ScriptDialogue{{Speaker: "Rin", CharacterID: characterID, Kind: "speech", Text: "Wait."}}
		panel.CharacterIDs = []string{characterID}
	}
	return studio.ScriptDoc{Schema: ai.SchemaScriptV1, ChapterSummary: "Before generation.", Scenes: []studio.ScriptScene{{
		Key: "scene-old", Title: "Old scene", Summary: "Existing work.", TimeOfDay: "night", Panels: []studio.ScriptPanel{panel},
	}}}
}
