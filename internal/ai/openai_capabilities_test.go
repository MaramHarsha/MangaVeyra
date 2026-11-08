package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// The OpenAI connection test reports text and image capability from one
// model list, and routing follows it. Every transport here is a fake.

func testOpenAIConnection(t *testing.T, m *Module, f *aiDBFixture, id string) providerView {
	t.Helper()
	view, err := m.providerAPI.TestConnection(context.Background(), providerActor{UserID: f.userID, Admin: true}, id)
	if err != nil && view.ID == "" {
		t.Fatal(err)
	}
	return view
}

func decisionSkipped(t *testing.T, p ImagePlan) []string {
	t.Helper()
	var d imageDecision
	if err := json.Unmarshal(p.Decision, &d); err != nil {
		t.Fatal(err)
	}
	return d.Skipped
}

func TestOpenAIImageRoutesFollowTheLastConnectionTest(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		fake := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){
			func(*http.Request) (*http.Response, error) { return openAIModelList("req_text", openAIModel), nil },
			func(*http.Request) (*http.Response, error) { return openAIModelList("req_flare", gptImageFlare), nil },
		}}
		m, f := imageTestModule(t, d, fake)
		provider := createOpenAIImageProvider(t, m, f, "sk-capabilities")
		needs := ImageNeeds{Width: 1024, Height: 1024, Count: 1}

		// Never tested: ADR 0009 routing is unchanged.
		before := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, Needs: needs})
		if before.ProviderID != provider.ID || before.ModelID != gptImageSunburst {
			t.Fatalf("untested plan = %s/%s", before.ProviderID, before.ModelID)
		}

		// The key lists the text model only: images fall back to the
		// placeholder and the decision records why.
		view := testOpenAIConnection(t, m, f, provider.ID)
		if view.LastTest.State != "ok" || view.LastTest.Text != capabilityReady || view.LastTest.Images != capabilityUnavailable {
			t.Fatalf("text-only check = %+v", view.LastTest)
		}
		for _, mode := range []string{ModeBalanced, ModeBest, ModeFastest} {
			p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, RoutingMode: mode, Needs: needs})
			if p.ProviderID != PlaceholderID {
				t.Fatalf("%s plan = %s/%s, want the placeholder", mode, p.ProviderID, p.ModelID)
			}
			skipped := decisionSkipped(t, p)
			for _, model := range []string{gptImageSunburst, gptImageFlare} {
				if !slices.Contains(skipped, model+": "+skipImagesUnavailable) {
					t.Fatalf("%s decision does not record why %s was skipped: %v", mode, model, skipped)
				}
			}
		}
		if _, err := m.image.PlanImage(ctx, ImagePlanRequest{Task: TaskPanel, ActorID: f.userID, ProjectID: f.projectID,
			PinProviderID: provider.ID, PinModelID: gptImageSunburst, Needs: needs}); err == nil {
			t.Fatal("a pin planned an image route the key cannot use")
		}

		// A plan approved before the test is refused at dispatch, before any
		// reservation or request.
		sent := fake.count()
		id := imageTestJob(t, m, f, before)
		_, err := m.image.GenerateImage(ctx, imageTestRequest(f, before, id))
		requireGenerateCode(t, err, CodeNoProvider)
		var reservations int
		if err := d.Get(ctx, &reservations, `SELECT COUNT(*) FROM spend_reservations WHERE job_id = ?`, id); err != nil || reservations != 0 {
			t.Fatalf("reservations = %d, %v", reservations, err)
		}
		if fake.count() != sent {
			t.Fatal("a refused route still sent a request")
		}

		// Only Flare listed: Auto keeps the direct route with the model the
		// key has, and drops the one it lacks.
		view = testOpenAIConnection(t, m, f, provider.ID)
		if view.LastTest.Text != capabilityUnavailable || view.LastTest.Images != capabilityReady || !slices.Equal(view.LastTest.ImageModels, []string{gptImageFlare}) {
			t.Fatalf("flare-only check = %+v", view.LastTest)
		}
		p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, Needs: needs})
		if p.ProviderID != provider.ID || p.ModelID != gptImageFlare || !slices.Contains(decisionSkipped(t, p), gptImageSunburst+": "+skipImagesUnavailable) {
			t.Fatalf("flare-only plan = %s/%s %v", p.ProviderID, p.ModelID, decisionSkipped(t, p))
		}
		// The text model is missing, so scripts use the placeholder.
		text, err := m.text.PlanText(ctx, TextPlanRequest{Task: TaskScript, Schema: SchemaScriptV1, ActorID: f.userID,
			ProjectID: f.projectID, Input: testScriptInputJSON(t)})
		if err != nil || text.ProviderID != PlaceholderID {
			t.Fatalf("text plan = %+v, %v", text, err)
		}
		if _, err := m.text.PlanText(ctx, TextPlanRequest{Task: TaskScript, Schema: SchemaScriptV1, ActorID: f.userID,
			ProjectID: f.projectID, PinProviderID: provider.ID, Input: testScriptInputJSON(t)}); !isCode(err, CodeNoProvider) {
			t.Fatalf("pinned text plan err = %v", err)
		}
		for _, r := range fake.requests() {
			if r.Method != http.MethodGet || r.URL != openAIModelsURL || len(r.Body) != 0 {
				t.Fatalf("a connection test sent %s %s", r.Method, r.URL)
			}
		}
	})
}

// A GPT Image refusal because the OpenAI organisation is not verified is a
// specific, terminal, unbilled error. It never falls back, and it is
// remembered for this key until the next connection test.
func TestOpenAIImageVerificationRefusalIsTerminalAndRemembered(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		fake := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){
			func(*http.Request) (*http.Response, error) {
				return httpJSON(http.StatusForbidden, map[string]any{"error": map[string]any{"message": "provider text that must never surface",
					"type": "invalid_request_error", "param": nil, "code": nil}}, nil), nil
			},
			func(*http.Request) (*http.Response, error) {
				return openAIModelList("req_again", openAIModel, gptImageSunburst, gptImageFlare), nil
			},
		}}
		m, f := imageTestModule(t, d, fake)
		provider := createOpenAIImageProvider(t, m, f, "sk-unverified-secret")
		needs := ImageNeeds{Width: 1024, Height: 1024, Count: 1}
		p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, Needs: needs})
		id := imageTestJob(t, m, f, p)
		_, err := m.image.GenerateImage(ctx, imageTestRequest(f, p, id))
		g := requireGenerateCode(t, err, CodeProviderVerification)
		if g.Retryable || g.Actual.USD != 0 {
			t.Fatalf("verification refusal retryable=%v actual=%v", g.Retryable, g.Actual)
		}
		if g.Error() != "OpenAI needs your organisation verified for image generation. Verify it in your OpenAI account, then test the connection again." {
			t.Fatalf("safe message = %q", g.Error())
		}
		var actual int64
		if err := d.Get(ctx, &actual, `SELECT actual_microusd FROM spend_reservations WHERE job_id = ?`, id); err != nil || actual != 0 {
			t.Fatalf("refusal reconciled at %d, %v", actual, err)
		}
		j, err := m.d.Jobs.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if rr, err := m.image.fallback(ctx, j, p, jobs.JobError{Code: CodeRateLimited}); err != nil || rr == nil {
			t.Fatalf("control: a transient failure should reroute, got %+v, %v", rr, err)
		}
		if rr, err := m.image.fallback(ctx, j, p, jobs.JobError{Code: CodeProviderVerification}); err != nil || rr != nil {
			t.Fatalf("verification refusal fell back: %+v, %v", rr, err)
		}

		// Remembered: G2 and routing see it, and a queued plan stops before
		// sending again.
		view, err := m.providers.GetManageable(ctx, providerActor{UserID: f.userID, Admin: true}, provider.ID)
		if err != nil || view.LastTest.Images != capabilityVerification || view.LastTest.State != "" {
			t.Fatalf("remembered = %+v, %v", view.LastTest, err)
		}
		if next := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, Needs: needs}); next.ProviderID != PlaceholderID {
			t.Fatalf("next plan = %s/%s, want the placeholder", next.ProviderID, next.ModelID)
		}
		again := imageTestJob(t, m, f, p)
		_, err = m.image.GenerateImage(ctx, imageTestRequest(f, p, again))
		requireGenerateCode(t, err, CodeProviderVerification)
		if fake.count() != 1 {
			t.Fatalf("requests = %d, want the one refused call", fake.count())
		}

		// Testing the connection again clears it.
		if view = testOpenAIConnection(t, m, f, provider.ID); view.LastTest.Images != capabilityReady {
			t.Fatalf("after re-test = %+v", view.LastTest)
		}
		if next := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, Needs: needs}); next.ProviderID != provider.ID {
			t.Fatalf("after re-test plan = %s/%s", next.ProviderID, next.ModelID)
		}
	})
}

// Capabilities from a stored row are sanitised: unknown values and models the
// route table does not have never widen routing.
func TestStoredOpenAICapabilitiesAreSanitised(t *testing.T) {
	r := providerRecord{Kind: providerKindOpenAI, LastTest: `{"state":"ok","text":"maybe","images":"ready","image_models":["dall-e-3","gpt-image-2.5-flare","gpt-image-2.5-flare"]}`}
	got := r.lastTest()
	if got.Text != "" || got.Images != capabilityReady || !slices.Equal(got.ImageModels, []string{gptImageFlare}) {
		t.Fatalf("sanitised = %+v", got)
	}
	r.LastTest = `{"state":"ok","images":"ready","image_models":["dall-e-3"]}`
	if got := r.lastTest(); got.Images != capabilityUnavailable || got.imageModelAvailable(gptImageSunburst) {
		t.Fatalf("unknown models only = %+v", got)
	}
	r.Kind = providerKindOpenRouter
	r.LastTest = `{"state":"ok","text":"unavailable","images":"unavailable"}`
	if got := r.lastTest(); got.Text != "" || got.Images != "" || !got.textAvailable() {
		t.Fatalf("other kinds carry no capabilities: %+v", got)
	}
}

// G2 shows both capabilities after Test connection, keeps them on the rows,
// and gives the image-tab (no-JavaScript) flash the same result in one line.
func TestModelsPageShowsOpenAICapabilities(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		fake := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){
			func(*http.Request) (*http.Response, error) { return openAIModelList("req_g2", openAIModel), nil },
			func(*http.Request) (*http.Response, error) {
				return openAIModelList("req_g2b", gptImageSunburst, gptImageFlare), nil
			},
		}}
		m, f := imageTestModule(t, d, fake)
		provider := createOpenAIImageProvider(t, m, f, "sk-g2-secret")
		actor := providerActor{UserID: f.userID, Admin: true}
		render := func(c templ.Component) string {
			var b bytes.Buffer
			if err := c.Render(ctx, &b); err != nil {
				t.Fatal(err)
			}
			return b.String()
		}
		imageRow := func(model string) imageModelView {
			inst, err := m.image.readSettings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			manageable, err := m.providers.ListManageable(ctx, actor)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := m.catalogueRows(ctx, actor, manageable, inst)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range rows {
				if r.ID == model {
					return r
				}
			}
			t.Fatalf("no image row for %s", model)
			return imageModelView{}
		}

		view := testOpenAIConnection(t, m, f, provider.ID)
		fragment := render(ProviderRows(providerRowView{Provider: view, Tested: true, Tone: ui.ToneSuccess, Notice: "Connection verified"}))
		for _, want := range []string{"Connection verified", `aria-label="Connection test results"`, "<dt>Text</dt>", "Ready", "<dt>Images</dt>", "Not available for this key"} {
			if !strings.Contains(fragment, want) {
				t.Fatalf("test result lacks %q:\n%s", want, fragment)
			}
		}
		if strings.Contains(fragment, "sk-g2-secret") {
			t.Fatal("fragment leaked the key")
		}
		if got := providerCapabilitySummary(ctx, view); got != "Text · Ready · Images · Not available for this key" {
			t.Fatalf("flash summary = %q", got)
		}
		row := imageRow(gptImageSunburst)
		if row.Ready || !row.Warning || row.Status != "No access" || row.Detail != "Not available for this key" {
			t.Fatalf("image row = %+v", row)
		}
		table := render(imageModelsTable([]imageModelView{row}))
		if !strings.Contains(table, "mv-badge--warning") || !strings.Contains(table, `title="Not available for this key"`) {
			t.Fatalf("image row badge:\n%s", table)
		}
		// Rows outside a test response carry no result list.
		if plain := render(ProviderRows(providerRowView{Provider: view})); strings.Contains(plain, "Connection test results") {
			t.Fatal("result list shown without a test")
		}

		// Images listed, text missing: the text row says so in its status.
		view = testOpenAIConnection(t, m, f, provider.ID)
		fragment = render(ProviderRows(providerRowView{Provider: view}))
		if !strings.Contains(fragment, "No access") || !strings.Contains(fragment, `title="Not available for this key"`) {
			t.Fatalf("text row status:\n%s", fragment)
		}
		if row := imageRow(gptImageFlare); !row.Ready || row.Warning || row.Status != "Ready" {
			t.Fatalf("image row with access = %+v", row)
		}

		// A verification refusal shows on the image rows until the next test.
		if err := m.providers.markImagesNeedVerification(ctx, provider.ID, providerKeyEnc(t, d, provider.ID)); err != nil {
			t.Fatal(err)
		}
		if row := imageRow(gptImageSunburst); row.Ready || row.Status != "Verify org" || !strings.Contains(row.Detail, "Verify it in your OpenAI account, then test the connection again.") {
			t.Fatalf("verification row = %+v", row)
		}
		view, _ = m.providers.GetManageable(ctx, actor, provider.ID)
		if got := providerCapabilitySummary(ctx, view); got != "Text · Not available for this key · Images · Needs organisation verification" {
			t.Fatalf("verification summary = %q", got)
		}
	})
}

func providerKeyEnc(t *testing.T, d *db.DB, id string) string {
	t.Helper()
	var keyEnc string
	if err := d.Get(context.Background(), &keyEnc, `SELECT key_enc FROM providers WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	return keyEnc
}
