package ai

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// A no-key install routes every image job to the placeholder. Those jobs
// record their decision like any other route, once per job attempt, so G11
// "Recent decisions" is never empty on a fresh server.
func TestPlaceholderImageJobsRecordRoutingDecisions(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		m, f := imageTestModule(t, d, &queuedDoer{})
		p, err := m.image.PlanImage(ctx, ImagePlanRequest{Task: TaskPanel, ActorID: f.userID, ProjectID: f.projectID, Needs: ImageNeeds{Width: 64, Height: 64, Count: 1}})
		if err != nil || p.ProviderID != PlaceholderID {
			t.Fatalf("plan=%+v err=%v", p, err)
		}
		id := imageTestJob(t, m, f, p)
		req := imageTestRequest(f, p, id)
		for slot := 0; slot < 3; slot++ {
			req.CallSlot = slot
			if _, err := m.image.GenerateImage(ctx, req); err != nil {
				t.Fatalf("slot %d: %v", slot, err)
			}
		}
		var rows []struct {
			Provider    string `db:"chosen_provider_id"`
			Model       string `db:"chosen_model_id"`
			Task        string `db:"task"`
			Explanation string `db:"explanation"`
		}
		if err := d.Select(ctx, &rows, `SELECT chosen_provider_id, chosen_model_id, task, explanation FROM routing_decisions WHERE job_id = ?`, id); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Provider != PlaceholderID || rows[0].Model != PlaceholderArt || rows[0].Task != TaskPanel || rows[0].Explanation != `{"size":"64 × 64"}` {
			t.Fatalf("decisions=%+v", rows)
		}

		v, err := m.loadRoutingView(ctx, providerActor{UserID: f.userID})
		if err != nil || len(v.Decisions) != 1 {
			t.Fatalf("view decisions=%+v err=%v", v.Decisions, err)
		}
		var page bytes.Buffer
		if err := RoutingPage(v).Render(ctx, &page); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Panel art", "Placeholder art · built-in", "No connected model fits; placeholder art keeps the flow working"} {
			if !strings.Contains(page.String(), want) {
				t.Fatalf("G11 lacks %q", want)
			}
		}
		if strings.Contains(page.String(), "Image jobs will show their model choices here.") {
			t.Fatal("G11 still shows the empty state")
		}
		// The reason is given in words, as designed, without the image size.
		if strings.Contains(page.String(), "64 × 64") {
			t.Fatal("G11 decision reason still carries the image size")
		}
	})
}

// G11 shows what actually ran: the route's model · provider with its
// per-call estimate, and after an automatic fallback the switch with its
// cause in words. Stored rows are data: legacy text and tampered values
// render without an estimate or a switch.
func TestRecentDecisionsShowTheRouteEstimateAndSwitch(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		m, f := imageTestModule(t, d, &queuedDoer{})
		openai := createOpenAIImageProvider(t, m, f, "sk-g11")
		or := f.createProvider(t, m.providers, providerScopeServer, "or-g11")
		p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, Needs: ImageNeeds{Width: 1024, Height: 1024, Count: 1}})
		if p.ProviderID != openai.ID || p.ModelID != gptImageSunburst {
			t.Fatalf("plan = %s/%s", p.ProviderID, p.ModelID)
		}
		id := imageTestJob(t, m, f, p)
		if err := m.image.recordDecision(ctx, imageTestRequest(f, p, id)); err != nil {
			t.Fatal(err)
		}
		j, err := m.d.Jobs.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		rr, err := m.image.fallback(ctx, j, p, jobs.JobError{Code: CodeRateLimited, Retryable: true})
		if err != nil || rr == nil || rr.ProviderID != or.ID {
			t.Fatalf("fallback = %+v, %v", rr, err)
		}
		next := p
		next.ProviderID, next.ModelID, next.Decision = rr.ProviderID, rr.ModelID, rr.Decision
		req := imageTestRequest(f, next, id)
		req.Attempt = 2
		if err := m.image.recordDecision(ctx, req); err != nil {
			t.Fatal(err)
		}
		// A row from before the structured record, and a tampered one.
		for _, explanation := range []string{"1024 × 1024", `{"size":"x","remote":true,"estimate_usd":-1,"switched_from":"<b>:x","switch_code":"<script>","skip":"other"}`} {
			if _, err := d.Exec(ctx, `INSERT INTO routing_decisions (id, job_id, user_id, project_id, task, mode, chosen_model_id, chosen_provider_id, explanation, created_at) VALUES (?, NULL, ?, ?, ?, ?, ?, ?, ?, ?)`,
				db.NewID(), f.userID, f.projectID, TaskRegionEdit, ModeBalanced, seedreamModel, or.ID, explanation, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
				t.Fatal(err)
			}
		}

		v, err := m.loadRoutingView(ctx, providerActor{UserID: f.userID})
		if err != nil || len(v.Decisions) != 4 {
			t.Fatalf("decisions = %+v, %v", v.Decisions, err)
		}
		switched, first := v.Decisions[0], v.Decisions[1]
		if first.Record.EstimateUSD != 0.0834 || first.Record.SwitchedFrom != "" || switched.Record.SwitchedFrom != "openai:"+gptImageSunburst || switched.Record.SwitchCode != CodeRateLimited || switched.Record.EstimateUSD != 0.04 {
			t.Fatalf("records = %+v / %+v", first.Record, switched.Record)
		}
		for _, legacy := range v.Decisions[2:] {
			if legacy.Record.EstimateUSD != 0 || legacy.Record.SwitchedFrom != "" || legacy.Record.SwitchCode != "" || legacy.Record.Skip != "" {
				t.Fatalf("legacy or tampered row kept values: %+v", legacy.Record)
			}
		}
		var page bytes.Buffer
		if err := RoutingPage(v).Render(ctx, &page); err != nil {
			t.Fatal(err)
		}
		body := page.String()
		mustAppear := []string{
			"Seedream 4.5 · OpenRouter", "~$0.04", "Switched from GPT Image 2.5 Sunburst · OpenAI (rate limited)",
			"GPT Image 2.5 Sunburst · OpenAI", "~$0.08",
		}
		at := 0
		for _, want := range mustAppear {
			i := strings.Index(body[at:], want)
			if i < 0 {
				t.Fatalf("G11 lacks %q after offset %d", want, at)
			}
			at += i + len(want)
		}
		if strings.Count(body, "Switched from") != 1 || strings.Contains(body, "&lt;script") || strings.Contains(body, "<script>") {
			t.Fatal("G11 shows a switch it should not")
		}
		if strings.Count(body, `class="mv-airouting__estimate"`) != 2 {
			t.Fatalf("estimates = %d, want only the two recorded remote routes", strings.Count(body, `class="mv-airouting__estimate"`))
		}
	})
}

// When the key cannot use OpenAI images, G11 says so instead of the generic
// placeholder reason.
func TestRecentDecisionsExplainUnavailableOpenAIImages(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		fake := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){
			func(*http.Request) (*http.Response, error) { return openAIModelList("req_text", openAIModel), nil },
		}}
		m, f := imageTestModule(t, d, fake)
		provider := createOpenAIImageProvider(t, m, f, "sk-g11-text")
		testOpenAIConnection(t, m, f, provider.ID)
		p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, Needs: ImageNeeds{Width: 64, Height: 64, Count: 1}})
		if p.ProviderID != PlaceholderID {
			t.Fatalf("plan = %s", p.ProviderID)
		}
		id := imageTestJob(t, m, f, p)
		if _, err := m.image.GenerateImage(ctx, imageTestRequest(f, p, id)); err != nil {
			t.Fatal(err)
		}
		v, err := m.loadRoutingView(ctx, providerActor{UserID: f.userID})
		if err != nil || len(v.Decisions) != 1 {
			t.Fatalf("decisions = %+v, %v", v.Decisions, err)
		}
		var page bytes.Buffer
		if err := RoutingPage(v).Render(ctx, &page); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(page.String(), "OpenAI images are not available for this key; placeholder art keeps the flow working") ||
			strings.Contains(page.String(), "mv-airouting__estimate") {
			t.Fatal("G11 does not explain the skipped OpenAI image route")
		}
	})
}

// Other modules' route chips name the routed model and provider in words.
func TestRouteNamesForChips(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		m, _ := imageTestModule(t, d, &queuedDoer{})
		for _, tc := range []struct{ model, provider, wantModel, wantProvider string }{
			{PlaceholderArt, PlaceholderID, "Placeholder art", "built-in"},
			{PlaceholderText, PlaceholderID, "Placeholder text", "built-in"},
			{"unknown-model", "01m6v0ahn00zr9aetqbppvtgnn", "unknown-model", "removed connection"},
		} {
			model, provider := m.RouteNames(ctx, tc.model, tc.provider)
			if model != tc.wantModel || provider != tc.wantProvider {
				t.Errorf("RouteNames(%q, %q) = %q, %q", tc.model, tc.provider, model, provider)
			}
		}
	})
}
