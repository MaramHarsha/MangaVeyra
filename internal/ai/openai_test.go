package ai

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

func openAICompletion(content json.RawMessage, prompt, completion int64, finish string) *http.Response {
	return httpJSON(http.StatusOK, map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": string(content), "refusal": nil},
			"finish_reason": finish}},
		"usage": map[string]any{"prompt_tokens": prompt, "completion_tokens": completion},
	}, nil)
}

func openAIError(status int, code string) *http.Response {
	return httpJSON(status, map[string]any{"error": map[string]any{"code": code, "type": "test", "message": "upstream detail"}}, nil)
}

func schemaKeywords(t *testing.T, raw json.RawMessage) map[string]int {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				seen[k]++
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(v)
	return seen
}

// OpenAI strict mode errors on minLength/maxLength and does not list const, so
// both remote routes send a schema without them; the server still validates.
func TestStrictScriptSchemaUsesOnlySupportedKeywords(t *testing.T) {
	strict := strictScriptSchema(testScriptInput())
	seen := schemaKeywords(t, strict)
	for _, banned := range []string{"minLength", "maxLength", "const", "allOf", "not", "if", "then", "else"} {
		if seen[banned] != 0 {
			t.Fatalf("strict schema still uses %q: %s", banned, strict)
		}
	}
	if seen["minItems"] == 0 || seen["maxItems"] == 0 || seen["additionalProperties"] == 0 {
		t.Fatalf("strict schema lost supported bounds: %v", seen)
	}
	if !strings.Contains(string(strict), `"enum":["mangaveyra.script.v1"]`) {
		t.Fatalf("schema constant not expressed as a one-value enum: %s", strict)
	}
	for _, attempt := range []scriptAttempt{{}, {Repair: true, Reason: "duplicate panel key"}} {
		or, err := buildOpenRouterRequest(testScriptInput(), attempt)
		if err != nil || string(or.ResponseFormat.JSONSchema.Schema) != string(strict) {
			t.Fatalf("OpenRouter request does not use the strict schema (%+v): %v", attempt, err)
		}
		oa, err := buildOpenAIRequest(testScriptInput(), attempt)
		if err != nil || string(oa.ResponseFormat.JSONSchema.Schema) != string(strict) {
			t.Fatalf("OpenAI request does not use the strict schema (%+v): %v", attempt, err)
		}
	}
	// The stricter server-side decoder still rejects what the schema no longer bounds.
	in := testScriptInput()
	var doc scriptDocument
	if err := json.Unmarshal(testScriptDocument(t, in), &doc); err != nil {
		t.Fatal(err)
	}
	doc.ChapterSummary = strings.Repeat("x", 4001)
	tooLong, _ := json.Marshal(doc)
	if _, _, err := decodeScriptDocument(tooLong, in); err == nil {
		t.Fatal("decoder accepted a summary over 4,000 characters")
	}
}

func TestOpenAIRequestEnvelopeIsFrozen(t *testing.T) {
	req, err := buildOpenAIRequest(testScriptInput(), scriptAttempt{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"model": `"gpt-6-luna"`, "max_completion_tokens": "16384", "reasoning_effort": `"none"`,
		"store": "false"}
	for key, value := range want {
		if string(top[key]) != value {
			t.Fatalf("%s = %s, want %s", key, top[key], value)
		}
	}
	if _, ok := top["response_format"]; !ok || len(top) != 6 || top["messages"] == nil {
		t.Fatalf("unexpected request fields: %s", raw)
	}
	if req.ResponseFormat.Type != "json_schema" || !req.ResponseFormat.JSONSchema.Strict ||
		req.ResponseFormat.JSONSchema.Name != "mangaveyra_script_v1" {
		t.Fatalf("response format = %+v", req.ResponseFormat)
	}
	if _, present := top["max_tokens"]; present {
		t.Fatal("deprecated max_tokens sent")
	}
}

func TestOpenAIReservationIsTheFrozenUpperBound(t *testing.T) {
	bound := math.Ceil(openAIInputTokens*openAIPromptPrice+openAICompletionTokens*openAICompletionPrice) / costMicro
	if math.Abs(bound-openAIReserveUSD) > 1e-12 || math.Abs(openAIPlanUSD-2*openAIReserveUSD) > 1e-12 {
		t.Fatalf("reserve = %v, computed bound = %v", openAIReserveUSD, bound)
	}
	p, c := int64(1200), int64(800)
	if got := openAIUsageCost(&p, &c); got == nil || math.Abs(*got-0.000520) > 1e-12 {
		t.Fatalf("usage cost = %v, want 0.000520", got)
	}
	odd := int64(1)
	if got := openAIUsageCost(&odd, &odd); got == nil || math.Abs(*got-0.000001) > 1e-12 {
		t.Fatalf("fractional microdollars must round up: %v", got)
	}
	huge := int64(10_000_000)
	if got := openAIUsageCost(&huge, &huge); got == nil || *got != openAIReserveUSD {
		t.Fatalf("usage cost must clamp to the reservation: %v", got)
	}
	negative := int64(-1)
	if openAIUsageCost(nil, &c) != nil || openAIUsageCost(&negative, &c) != nil {
		t.Fatal("missing or negative usage must fall back to the reserved estimate")
	}
}

func TestOpenAIInputFitsBelowLongContextTier(t *testing.T) {
	in := testScriptInput()
	if !openAIInputFits(in) || !openRouterInputFits(in) {
		t.Fatal("ordinary script input rejected")
	}
	big := in
	big.Mode, big.SourceText = "adapt", strings.Repeat("x", 300_000)
	if openAIInputFits(big) {
		t.Fatal("input above the 272K long-context tier considered safe for OpenAI")
	}
	if !openRouterInputFits(big) {
		t.Fatal("OpenRouter's larger context should still hold a 300 KB adaptation")
	}
}

func TestOpenAIClientMapsResponses(t *testing.T) {
	in := testScriptInput()
	valid := testScriptDocument(t, in)
	cases := []struct {
		name       string
		resp       *http.Response
		code       string
		retryable  bool
		repairable bool
		billed     bool
	}{
		{name: "success", resp: openAICompletion(valid, 1200, 800, "stop"), billed: true},
		{name: "refusal", resp: httpJSON(http.StatusOK, map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"content": nil, "refusal": "I can't help with that."}, "finish_reason": "stop"}}}, nil),
			code: CodeProviderRefuse},
		{name: "truncated", resp: openAICompletion(json.RawMessage(`{"schema":`), 10, 16384, "length"), code: CodeBadOutput,
			repairable: true, billed: true},
		{name: "bad key", resp: openAIError(http.StatusUnauthorized, "invalid_api_key"), code: CodeProviderAuth},
		{name: "no quota", resp: openAIError(http.StatusTooManyRequests, "insufficient_quota"), code: CodeProviderAuth},
		{name: "rate limited", resp: openAIError(http.StatusTooManyRequests, "rate_limit_exceeded"), code: CodeRateLimited,
			retryable: true},
		{name: "model unavailable", resp: openAIError(http.StatusNotFound, "model_not_found"), code: CodeNoProvider},
		{name: "server error", resp: openAIError(http.StatusInternalServerError, "server_error"), code: CodeUpstream,
			retryable: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doer := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){
				func(*http.Request) (*http.Response, error) { return tc.resp, nil },
			}}
			call, err := newOpenAIClient(doer).Complete(context.Background(), "sk-test-openai", in, scriptAttempt{})
			seen := doer.requests()
			if len(seen) != 1 || seen[0].Method != http.MethodPost || seen[0].URL != openAIChatURL ||
				seen[0].Authorization != "Bearer sk-test-openai" || seen[0].ContentType != "application/json" {
				t.Fatalf("request = %+v", seen)
			}
			if !call.Sent {
				t.Fatal("sent request not marked as sent")
			}
			if tc.code == "" {
				if err != nil || string(call.Content) != string(valid) || call.ActualUSD == nil ||
					math.Abs(*call.ActualUSD-0.000520) > 1e-12 {
					t.Fatalf("success = %+v, %v", call, err)
				}
				return
			}
			var adapter *adapterError
			if !errors.As(err, &adapter) || adapter.code != tc.code || adapter.retryable != tc.retryable ||
				adapter.repairable != tc.repairable {
				t.Fatalf("error = %#v, want code %s retryable %v repairable %v", err, tc.code, tc.retryable, tc.repairable)
			}
			if strings.Contains(err.Error(), "upstream detail") || strings.Contains(err.Error(), "sk-test") {
				t.Fatalf("unsafe error text: %q", err.Error())
			}
			if !tc.billed && (call.ActualUSD != nil && *call.ActualUSD != 0) {
				t.Fatalf("rejected request billed: %v", *call.ActualUSD)
			}
		})
	}
}

func TestOpenAITestKeyReadsTheModelWithoutGenerating(t *testing.T) {
	doer := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){
		func(*http.Request) (*http.Response, error) {
			return httpJSON(http.StatusOK, map[string]any{"id": openAIModel, "object": "model"},
				map[string]string{"X-Request-Id": "req_abc123"}), nil
		},
		func(*http.Request) (*http.Response, error) {
			return openAIError(http.StatusUnauthorized, "invalid_api_key"), nil
		},
	}}
	client := newOpenAIClient(doer)
	requestID, err := client.TestKey(context.Background(), "sk-test-openai")
	if err != nil || requestID != "req_abc123" {
		t.Fatalf("test key = %q, %v", requestID, err)
	}
	var adapter *adapterError
	if _, err := client.TestKey(context.Background(), "sk-bad"); !errors.As(err, &adapter) || adapter.code != CodeProviderAuth {
		t.Fatalf("rejected key = %v", err)
	}
	for _, seen := range doer.requests() {
		if seen.Method != http.MethodGet || seen.URL != openAIModelURL || len(seen.Body) != 0 {
			t.Fatalf("connection check sent %+v", seen)
		}
	}
}

func (f *aiDBFixture) createKindProvider(t *testing.T, store *providerStore, kind, scope, name string) providerView {
	t.Helper()
	view, err := store.Create(context.Background(), providerActor{UserID: f.userID, Admin: true}, providerCreate{
		Kind: kind, Name: name, Key: "sk-" + kind + "-test", Scope: scope,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Kind != kind {
		t.Fatalf("created kind = %q, want %q", view.Kind, kind)
	}
	return view
}

func (f *aiDBFixture) seedRunningModelJob(t *testing.T, providerID, model string) string {
	t.Helper()
	id := db.NewID()
	if _, err := f.d.Exec(context.Background(), `INSERT INTO jobs (
		id, type, scope, scope_kind, project_id, user_id, state, executor, model_id, provider_id, attempts
	) VALUES (?, ?, '{}', 'chapter', ?, ?, 'running', 'go', ?, ?, 1)`,
		id, TaskScript, f.projectID, f.userID, model, providerID); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestOpenAIConnectionPlansReservesAndReconcilesFromUsage(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		fixture := newAIDBFixture(t, d)
		providers := fixture.providers(nil)
		openai := fixture.createKindProvider(t, providers, providerKindOpenAI, providerScopeServer, "OpenAI direct")
		in := testScriptInput()
		doer := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){
			func(*http.Request) (*http.Response, error) {
				return openAICompletion(testScriptDocument(t, in), 1200, 800, "stop"), nil
			},
		}}
		service := newTextService(d, fixture.settings, providers, newOpenRouterClient(doer),
			NewSpendStore(d, fixture.settings), nil).withClient(providerKindOpenAI, newOpenAIClient(doer))

		plan, err := service.PlanText(ctx, TextPlanRequest{Task: TaskScript, Schema: SchemaScriptV1, ActorID: fixture.userID,
			ProjectID: fixture.projectID, Input: testScriptInputJSON(t)})
		if err != nil || !plan.Remote || plan.ProviderID != openai.ID || plan.ModelID != openAIModel ||
			math.Abs(plan.Estimate.USD-openAIPlanUSD) > 1e-12 || !strings.Contains(string(plan.Decision), "OpenAI") {
			t.Fatalf("plan = %+v, %v", plan, err)
		}
		jobID := fixture.seedRunningModelJob(t, openai.ID, openAIModel)
		result, err := service.GenerateText(ctx, TextGenerateRequest{Plan: plan, ActorID: fixture.userID,
			ProjectID: fixture.projectID, JobID: jobID, Attempt: 1, Input: testScriptInputJSON(t)})
		if err != nil || result.ProviderID != openai.ID || result.ModelID != openAIModel ||
			math.Abs(result.Actual.USD-0.000520) > 1e-12 {
			t.Fatalf("generate = %+v, %v", result, err)
		}
		if seen := doer.requests(); len(seen) != 1 || seen[0].URL != openAIChatURL ||
			seen[0].Authorization != "Bearer sk-openai-test" {
			t.Fatalf("requests = %+v", seen)
		}
		var row struct {
			Model    string `db:"model_id"`
			Estimate int64  `db:"estimate_microusd"`
			Actual   int64  `db:"actual_microusd"`
		}
		if err := d.Get(ctx, &row, `SELECT model_id, estimate_microusd, actual_microusd FROM spend_reservations WHERE job_id = ?`,
			jobID); err != nil {
			t.Fatal(err)
		}
		if row.Model != openAIModel || row.Estimate != 35_392 || row.Actual != 520 {
			t.Fatalf("reservation = %+v", row)
		}
	})
}

func TestRouterChoosesAFittingProviderAndPinsStayExact(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		fixture := newAIDBFixture(t, d)
		providers := fixture.providers(nil)
		openai := fixture.createKindProvider(t, providers, providerKindOpenAI, providerScopeServer, "OpenAI direct")
		service := newTextService(d, fixture.settings, providers, newOpenRouterClient(&queuedDoer{}),
			NewSpendStore(d, fixture.settings), nil).withClient(providerKindOpenAI, newOpenAIClient(&queuedDoer{}))
		big := testScriptInput()
		big.Mode, big.SourceText = "adapt", strings.Repeat("x", 300_000)
		bigJSON, _ := json.Marshal(big)
		base := TextPlanRequest{Task: TaskScript, Schema: SchemaScriptV1, ActorID: fixture.userID, ProjectID: fixture.projectID}

		large := base
		large.Input = bigJSON
		if plan, err := service.PlanText(ctx, large); err != nil || plan.Remote {
			t.Fatalf("oversized input with only OpenAI = %+v, %v (want placeholder)", plan, err)
		}
		pinnedLarge := large
		pinnedLarge.PinProviderID = openai.ID
		if _, err := service.PlanText(ctx, pinnedLarge); !isCode(err, CodeBadInput) {
			t.Fatalf("pinned OpenAI with oversized input = %v, want bad_input", err)
		}

		openrouter := fixture.createKindProvider(t, providers, providerKindOpenRouter, providerScopeServer, "OpenRouter")
		normal := base
		normal.Input = testScriptInputJSON(t)
		if plan, err := service.PlanText(ctx, normal); err != nil || plan.ProviderID != openai.ID || plan.ModelID != openAIModel {
			t.Fatalf("first eligible connection = %+v, %v", plan, err)
		}
		if plan, err := service.PlanText(ctx, large); err != nil || plan.ProviderID != openrouter.ID ||
			plan.ModelID != openRouterModel {
			t.Fatalf("oversized input should skip OpenAI for OpenRouter: %+v, %v", plan, err)
		}
		modelPin := normal
		modelPin.PinModelID = openRouterModel
		if plan, err := service.PlanText(ctx, modelPin); err != nil || plan.ProviderID != openrouter.ID {
			t.Fatalf("model pin = %+v, %v", plan, err)
		}
		mismatch := normal
		mismatch.PinProviderID, mismatch.PinModelID = openai.ID, openRouterModel
		if _, err := service.PlanText(ctx, mismatch); !isCode(err, CodeNoProvider) {
			t.Fatalf("provider/model pin mismatch = %v, want no_provider", err)
		}
	})
}

func TestLocalOnlyAndKindMismatchBlockOpenAIBeforeAnyRequest(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		fixture := newAIDBFixture(t, d)
		providers := fixture.providers(nil)
		openai := fixture.createKindProvider(t, providers, providerKindOpenAI, providerScopeServer, "OpenAI direct")
		doer := &queuedDoer{}
		service := newTextService(d, fixture.settings, providers, newOpenRouterClient(doer),
			NewSpendStore(d, fixture.settings), nil).withClient(providerKindOpenAI, newOpenAIClient(doer))
		plan, err := service.PlanText(ctx, TextPlanRequest{Task: TaskScript, Schema: SchemaScriptV1, ActorID: fixture.userID,
			ProjectID: fixture.projectID, Input: testScriptInputJSON(t)})
		if err != nil || plan.ModelID != openAIModel {
			t.Fatalf("plan = %+v, %v", plan, err)
		}

		// A plan that pairs the OpenAI connection with another route's model
		// is refused before reserving or sending anything.
		forged := plan
		forged.ModelID = openRouterModel
		jobID := fixture.seedRunningModelJob(t, openai.ID, openRouterModel)
		if _, err := service.GenerateText(ctx, TextGenerateRequest{Plan: forged, ActorID: fixture.userID,
			ProjectID: fixture.projectID, JobID: jobID, Attempt: 1, Input: testScriptInputJSON(t)}); !isCode(err, CodeNoProvider) {
			t.Fatalf("kind mismatch = %v, want no_provider", err)
		}

		updateInstance(t, fixture.settings, func(inst *settings.Instance) { inst.LocalOnly = true })
		jobID = fixture.seedRunningModelJob(t, openai.ID, openAIModel)
		if _, err := service.GenerateText(ctx, TextGenerateRequest{Plan: plan, ActorID: fixture.userID,
			ProjectID: fixture.projectID, JobID: jobID, Attempt: 1, Input: testScriptInputJSON(t)}); !isCode(err, CodeLocalOnly) {
			t.Fatalf("local-only generate = %v, want local_only", err)
		}
		if replanned, err := service.PlanText(ctx, TextPlanRequest{Task: TaskScript, Schema: SchemaScriptV1,
			ActorID: fixture.userID, ProjectID: fixture.projectID, Input: testScriptInputJSON(t)}); err != nil || replanned.Remote {
			t.Fatalf("local-only plan = %+v, %v", replanned, err)
		}
		tester := &providerService{store: providers, settings: fixture.settings, client: newOpenRouterClient(doer),
			testers: map[string]remoteKeyTester{providerKindOpenAI: newOpenAIClient(doer)}}
		if _, err := tester.TestConnection(ctx, providerActor{UserID: fixture.userID, Admin: true}, openai.ID); err == nil {
			t.Fatal("local-only connection check succeeded")
		}
		if doer.count() != 0 {
			t.Fatalf("blocked OpenAI paths sent %d requests", doer.count())
		}
		var reservations int
		if err := d.Get(ctx, &reservations, `SELECT COUNT(*) FROM spend_reservations`); err != nil || reservations != 0 {
			t.Fatalf("reservations = %d, %v", reservations, err)
		}
	})
}

func TestOpenAIConnectionCheckUsesTheOpenAITester(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		fixture := newAIDBFixture(t, d)
		providers := fixture.providers(func() time.Time { return time.Date(2026, 11, 7, 1, 0, 0, 0, time.UTC) })
		openai := fixture.createKindProvider(t, providers, providerKindOpenAI, providerScopeServer, "OpenAI direct")
		openRouterDoer := &queuedDoer{}
		openAIDoer := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){
			func(*http.Request) (*http.Response, error) {
				return httpJSON(http.StatusOK, map[string]any{"id": openAIModel}, map[string]string{"X-Request-Id": "req_ok"}), nil
			},
		}}
		service := &providerService{store: providers, settings: fixture.settings, client: newOpenRouterClient(openRouterDoer),
			testers: map[string]remoteKeyTester{providerKindOpenAI: newOpenAIClient(openAIDoer)}}
		view, err := service.TestConnection(ctx, providerActor{UserID: fixture.userID, Admin: true}, openai.ID)
		if err != nil || view.LastTest.State != "ok" || view.LastTest.RequestID != "req_ok" {
			t.Fatalf("connection check = %+v, %v", view, err)
		}
		if openRouterDoer.count() != 0 || openAIDoer.count() != 1 || openAIDoer.requests()[0].URL != openAIModelURL {
			t.Fatalf("wrong tester: openrouter=%d openai=%+v", openRouterDoer.count(), openAIDoer.requests())
		}
	})
}

func TestProviderStoreRejectsUnknownKinds(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		fixture := newAIDBFixture(t, d)
		_, err := fixture.providers(nil).Create(context.Background(), providerActor{UserID: fixture.userID, Admin: true},
			providerCreate{Kind: "custom", Name: "Elsewhere", Key: "sk-x", Scope: providerScopeServer})
		if !errors.Is(err, errProviderInvalid) {
			t.Fatalf("unknown kind create = %v", err)
		}
	})
}

func isCode(err error, code string) bool {
	var generated *GenerateError
	return errors.As(err, &generated) && generated.Code == code
}

func characterSchemas(t *testing.T, raw json.RawMessage) (map[string]any, map[string]any) {
	t.Helper()
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	panel := schemaPath(schema, "properties", "scenes", "items", "properties", "panels", "items", "properties")
	return schemaPath(panel, "dialogue", "items", "properties", "character_id"), panel["character_ids"].(map[string]any)
}

// The live run showed the model inventing character IDs for a project without
// characters. The strict schema now carries the request's allow-list, so the
// provider itself can only emit "" or an allowed ID.
func TestStrictSchemaLimitsCharacterReferencesToTheAllowList(t *testing.T) {
	none := testScriptInput()
	none.Characters = nil
	speaker, ids := characterSchemas(t, strictScriptSchema(none))
	if fmtJSON(speaker["enum"]) != `[""]` || fmtJSON(ids["maxItems"]) != "0" {
		t.Fatalf("no-character schema = %v / %v", speaker, ids)
	}

	speaker, ids = characterSchemas(t, strictScriptSchema(testScriptInput()))
	items := ids["items"].(map[string]any)
	if fmtJSON(speaker["enum"]) != `["","char-rin"]` || fmtJSON(items["enum"]) != `["char-rin"]` ||
		fmtJSON(ids["maxItems"]) != "1" {
		t.Fatalf("allow-list schema = %v / %v", speaker, ids)
	}

	// OpenAI strict limits: at most 1,000 enum values in total, and at most
	// 15,000 characters for a single enum with more than 250 values.
	big := testScriptInput()
	big.Characters = nil
	for range maxPromptCharactersForTest {
		big.Characters = append(big.Characters, ScriptCharacter{ID: db.NewID(), Name: "Extra"})
	}
	raw := strictScriptSchema(big)
	speaker, ids = characterSchemas(t, raw)
	total, longest := 0, 0
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if values, ok := x["enum"].([]any); ok {
				total += len(values)
				length := 0
				for _, value := range values {
					length += len(value.(string))
				}
				longest = max(longest, length)
			}
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	var tree any
	_ = json.Unmarshal(raw, &tree)
	walk(tree)
	if total > 1000 || longest > 15000 {
		t.Fatalf("worst-case schema exceeds strict-mode enum limits: %d values, longest enum %d chars", total, longest)
	}
}

const maxPromptCharactersForTest = 256

func fmtJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestRepairRequestNamesTheFailedCheck(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		fixture := newAIDBFixture(t, d)
		providers := fixture.providers(nil)
		openai := fixture.createKindProvider(t, providers, providerKindOpenAI, providerScopeServer, "OpenAI direct")
		in := testScriptInput()
		valid := testScriptDocument(t, in)
		var doc scriptDocument
		if err := json.Unmarshal(valid, &doc); err != nil {
			t.Fatal(err)
		}
		doc.Scenes[0].Panels[0].CharacterIDs = []string{"char-ghost"}
		invented, _ := json.Marshal(doc)
		respond := func(content json.RawMessage) func(*http.Request) (*http.Response, error) {
			return func(*http.Request) (*http.Response, error) { return openAICompletion(content, 100, 100, "stop"), nil }
		}
		run := func(responses ...json.RawMessage) (*queuedDoer, TextResult, error) {
			doer := &queuedDoer{}
			for _, content := range responses {
				doer.responders = append(doer.responders, respond(content))
			}
			service := newTextService(d, fixture.settings, providers, newOpenRouterClient(&queuedDoer{}),
				NewSpendStore(d, fixture.settings), nil).withClient(providerKindOpenAI, newOpenAIClient(doer))
			plan, err := service.PlanText(ctx, TextPlanRequest{Task: TaskScript, Schema: SchemaScriptV1, ActorID: fixture.userID,
				ProjectID: fixture.projectID, Input: testScriptInputJSON(t)})
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.GenerateText(ctx, TextGenerateRequest{Plan: plan, ActorID: fixture.userID,
				ProjectID: fixture.projectID, JobID: fixture.seedRunningModelJob(t, openai.ID, openAIModel), Attempt: 1,
				Input: testScriptInputJSON(t)})
			return doer, result, err
		}

		doer, result, err := run(invented, valid)
		if err != nil || len(result.Document) == 0 {
			t.Fatalf("repair after an invented character = %+v, %v", result, err)
		}
		seen := doer.requests()
		var repair openAIRequest
		if len(seen) != 2 || json.Unmarshal(seen[1].Body, &repair) != nil ||
			!strings.Contains(repair.Messages[0].Content, "failed this check: character id.") {
			t.Fatalf("repair instruction did not name the failed check: %d requests", len(seen))
		}
		var initial openAIRequest
		if json.Unmarshal(seen[0].Body, &initial) != nil || strings.Contains(initial.Messages[0].Content, "failed this check") {
			t.Fatal("initial request carried a repair reason")
		}

		_, _, err = run(invented, invented)
		var generated *GenerateError
		if !errors.As(err, &generated) || generated.Code != CodeBadOutput ||
			generated.SafeMessage != "The text provider returned an invalid script (character id)." {
			t.Fatalf("double failure = %#v", err)
		}
	})
}
