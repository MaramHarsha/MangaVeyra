package ai

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

// ADR 0013 §9 text routes (Google, xAI, Together, Hugging Face). Request
// shapes follow the providers' public API documentation cited in each
// adapter; every response here comes from a fake transport.

func TestPhase4TextReservationsAreExactUpperBounds(t *testing.T) {
	for _, tc := range []struct {
		kind                  string
		in, out               int
		inNano, outNano       int64
		reserve, plan, wantUS float64
	}{
		// 131,072 × $1.50/M + 32,768 × $7.50/M (Gemini's 2027 tariff).
		{providerKindGoogle, googleInputTokens, googleOutputTokens, googleInputNano, googleOutputNano, googleReserveUSD, googlePlanUSD, 0.442368},
		// 131,072 × $1.25/M + 16,384 × $2.50/M (below xAI's 200k tier).
		{providerKindXAI, xaiInputTokens, xaiOutputTokens, xaiInputNano, xaiOutputNano, xaiReserveUSD, xaiPlanUSD, 0.2048},
		// 131,072 × $0.17/M + 16,384 × $0.25/M = $0.02637824, rounded up.
		{providerKindTogether, togetherInputTokens, togetherOutputTokens, togetherInputNano, togetherOutputNano, togetherReserveUSD, togetherPlanUSD, 0.026379},
	} {
		got := reserveFor(tc.in, tc.out, tc.inNano, tc.outNano)
		route, ok := routeForKind(tc.kind)
		if !ok || got != tc.wantUS || tc.reserve != tc.wantUS || route.ReserveUSD != tc.wantUS || math.Abs(route.PlanUSD-2*tc.wantUS) > 1e-12 || tc.plan != route.PlanUSD {
			t.Errorf("%s: reserveFor = %v, constant %v, route %+v", tc.kind, got, tc.reserve, route)
		}
	}
}

type textAdapterCase struct {
	kind   string
	client remoteTextClient
	url    string
	auth   func(http.Header) bool
	ok     func(content string, prompt, completion int64) *http.Response
	fail   func(status int, code string) *http.Response
}

func chatOK(content string, prompt, completion int64) *http.Response {
	return openAICompletion(json.RawMessage(content), prompt, completion, "stop")
}

func googleOK(content string, prompt, completion int64) *http.Response {
	return httpJSON(http.StatusOK, map[string]any{
		"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{
			map[string]any{"text": "thinking about pages", "thought": true}, map[string]any{"text": content}}}, "finishReason": "STOP"}},
		"usageMetadata": map[string]any{"promptTokenCount": prompt, "candidatesTokenCount": completion, "thoughtsTokenCount": 100},
	}, nil)
}

func googleFail(status int, code string) *http.Response {
	e := map[string]any{"code": status, "message": "provider detail that must stay hidden", "status": code}
	if code == "API_KEY_INVALID" {
		e["status"] = "INVALID_ARGUMENT"
		e["details"] = []any{map[string]any{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "API_KEY_INVALID"}}
	}
	return httpJSON(status, map[string]any{"error": e}, nil)
}

func phase4TextCases(doer httpDoer) []textAdapterCase {
	bearer := func(want string) func(http.Header) bool {
		return func(h http.Header) bool { return h.Get("Authorization") == want && h.Get("x-goog-api-key") == "" }
	}
	return []textAdapterCase{
		{kind: providerKindGoogle, client: newGoogleTextClient(doer), url: googleTextURL(),
			auth: func(h http.Header) bool { return h.Get("x-goog-api-key") == "test-key" && h.Get("Authorization") == "" },
			ok:   googleOK, fail: googleFail},
		{kind: providerKindXAI, client: newXAITextClient(doer), url: xaiChatURL, auth: bearer("Bearer test-key"), ok: chatOK, fail: openAIError},
		{kind: providerKindTogether, client: newTogetherTextClient(doer), url: togetherChatURL, auth: bearer("Bearer test-key"), ok: chatOK, fail: openAIError},
		{kind: providerKindHuggingFace, client: newHFTextClient(doer, hfRouterBase, testHFTextConfig()), url: hfRouterBase + "/chat/completions",
			auth: bearer("Bearer test-key"), ok: chatOK, fail: openAIError},
	}
}

func testHFTextConfig() hfConfig {
	return hfConfig{Endpoint: hfServerless, Task: hfTaskText, Model: "Qwen/Qwen3-32B:together", ContextTokens: 32768,
		OutputTokens: 4096, Priced: true, InputNano: 900, OutputNano: 1800}
}

func TestPhase4TextRequestShapes(t *testing.T) {
	in := testScriptInput()
	content := string(testScriptDocument(t, in))
	doer := &routeDoer{}
	for _, tc := range phase4TextCases(doer) {
		t.Run(tc.kind, func(t *testing.T) {
			doer.routes = nil
			doer.seen = nil
			doer.on(http.MethodPost, tc.url, func(*http.Request, []byte) (*http.Response, error) { return tc.ok(content, 1000, 500), nil })
			call, err := tc.client.Complete(context.Background(), "test-key", in, scriptAttempt{Repair: true, Reason: "duplicate panel key"})
			if err != nil || string(call.Content) != content || !call.Sent {
				t.Fatalf("complete = %+v, %v", call, err)
			}
			seen := doer.requests()
			if len(seen) != 1 || seen[0].Method != http.MethodPost || seen[0].URL != tc.url || !tc.auth(seen[0].Header) ||
				seen[0].Header.Get("Content-Type") != "application/json" {
				t.Fatalf("request = %+v", seen)
			}
			body := seen[0].json(t)
			schema := string(strictScriptSchema(in))
			switch tc.kind {
			case providerKindGoogle:
				gc, _ := body["generationConfig"].(map[string]any)
				raw, _ := json.Marshal(gc["responseJsonSchema"])
				thinking, _ := gc["thinkingConfig"].(map[string]any)
				sys, _ := json.Marshal(body["systemInstruction"])
				if gc["responseMimeType"] != "application/json" || string(raw) != schema || gc["maxOutputTokens"] != float64(googleOutputTokens) ||
					thinking["thinkingLevel"] != "LOW" || !strings.Contains(string(sys), "duplicate panel key") || body["model"] != nil {
					t.Fatalf("gemini body = %v", body)
				}
				safety, _ := body["safetySettings"].([]any)
				if len(safety) != 4 || !strings.Contains(string(seen[0].Body), `"HARM_CATEGORY_SEXUALLY_EXPLICIT","threshold":"BLOCK_MEDIUM_AND_ABOVE"`) {
					t.Fatalf("safety settings = %v", safety)
				}
				contents, _ := json.Marshal(body["contents"])
				if !strings.Contains(string(contents), `"role":"user"`) || !strings.Contains(string(contents), "paper moon") {
					t.Fatalf("contents = %s", contents)
				}
			default:
				rf, _ := body["response_format"].(map[string]any)
				js, _ := rf["json_schema"].(map[string]any)
				raw, _ := json.Marshal(js["schema"])
				messages, _ := body["messages"].([]any)
				if rf["type"] != "json_schema" || js["strict"] != true || js["name"] != "mangaveyra_script_v1" || string(raw) != schema ||
					len(messages) != 2 || body["stream"] != false {
					t.Fatalf("%s body = %v", tc.kind, body)
				}
				switch tc.kind {
				case providerKindXAI:
					if body["model"] != xaiTextModel || body["max_completion_tokens"] != float64(xaiOutputTokens) || body["max_tokens"] != nil || body["reasoning_effort"] != nil {
						t.Fatalf("xai body = %v", body)
					}
				case providerKindTogether:
					reasoning, _ := body["reasoning"].(map[string]any)
					if body["model"] != togetherTextModel || body["max_tokens"] != float64(togetherOutputTokens) || reasoning["enabled"] != false {
						t.Fatalf("together body = %v", body)
					}
				case providerKindHuggingFace:
					if body["model"] != "Qwen/Qwen3-32B:together" || body["max_tokens"] != float64(4096) {
						t.Fatalf("hugging face body = %v", body)
					}
				}
			}
		})
	}
}

func TestPhase4TextResponsesMapToSafeCodesAndAccounting(t *testing.T) {
	in := testScriptInput()
	content := string(testScriptDocument(t, in))
	doer := &routeDoer{}
	for _, tc := range phase4TextCases(doer) {
		wantActual := map[string]float64{
			// 1,000 × $1.50/M + (500 + 100 thinking) × $7.50/M.
			providerKindGoogle: 0.006, providerKindXAI: 0.0025, providerKindTogether: 0.000295, providerKindHuggingFace: 0.0018,
		}[tc.kind]
		statusCode := func(status int) string {
			if tc.kind != providerKindGoogle {
				return map[int]string{401: "invalid_api_key", 402: "", 403: "", 404: "model_not_found", 429: "rate_limit_exceeded"}[status]
			}
			return map[int]string{400: "API_KEY_INVALID", 402: "RESOURCE_EXHAUSTED", 403: "PERMISSION_DENIED", 404: "NOT_FOUND",
				429: "RESOURCE_EXHAUSTED", 500: "INTERNAL", 503: "UNAVAILABLE", 504: "DEADLINE_EXCEEDED"}[status]
		}
		cases := []struct {
			name       string
			respond    func(*http.Request, []byte) (*http.Response, error)
			code       string
			retry, rep bool
			actual     string // "nil" keeps the reservation; "0" is a definite rejection
		}{
			{"auth", func(*http.Request, []byte) (*http.Response, error) {
				s := http.StatusUnauthorized
				if tc.kind == providerKindGoogle {
					s = http.StatusBadRequest
				}
				return tc.fail(s, statusCode(s)), nil
			}, CodeProviderAuth, false, false, "0"},
			{"quota", func(*http.Request, []byte) (*http.Response, error) { return tc.fail(402, statusCode(402)), nil }, CodeProviderAuth, false, false, "0"},
			{"forbidden", func(*http.Request, []byte) (*http.Response, error) { return tc.fail(403, statusCode(403)), nil }, CodeProviderAuth, false, false, "0"},
			{"model", func(*http.Request, []byte) (*http.Response, error) { return tc.fail(404, statusCode(404)), nil }, CodeNoProvider, false, false, "0"},
			{"rate", func(*http.Request, []byte) (*http.Response, error) { return tc.fail(429, statusCode(429)), nil }, CodeRateLimited, true, false, "0"},
			{"server", func(*http.Request, []byte) (*http.Response, error) { return tc.fail(500, statusCode(500)), nil }, CodeUpstream, true, false, "0"},
			{"unavailable", func(*http.Request, []byte) (*http.Response, error) { return tc.fail(503, statusCode(503)), nil }, CodeUpstream, true, false, "0"},
			{"gateway timeout", func(*http.Request, []byte) (*http.Response, error) { return tc.fail(504, statusCode(504)), nil }, CodeTimeout, true, false, "0"},
			{"transport timeout", replyErr(context.DeadlineExceeded), CodeTimeout, true, false, "nil"},
		}
		if tc.kind == providerKindGoogle {
			cases = append(cases,
				struct {
					name       string
					respond    func(*http.Request, []byte) (*http.Response, error)
					code       string
					retry, rep bool
					actual     string
				}{"blocked prompt", reply(200, map[string]any{"promptFeedback": map[string]any{"blockReason": "PROHIBITED_CONTENT"},
					"usageMetadata": map[string]any{"promptTokenCount": 10, "candidatesTokenCount": 0}}), CodeProviderRefuse, false, false, "0.000015"},
				struct {
					name       string
					respond    func(*http.Request, []byte) (*http.Response, error)
					code       string
					retry, rep bool
					actual     string
				}{"truncated", reply(200, map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": "{"}}}, "finishReason": "MAX_TOKENS"}}}), CodeBadOutput, false, true, "nil"},
			)
		} else {
			cases = append(cases,
				struct {
					name       string
					respond    func(*http.Request, []byte) (*http.Response, error)
					code       string
					retry, rep bool
					actual     string
				}{"refusal", reply(200, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "", "refusal": "no"}, "finish_reason": "stop"}}}), CodeProviderRefuse, false, false, "nil"},
				struct {
					name       string
					respond    func(*http.Request, []byte) (*http.Response, error)
					code       string
					retry, rep bool
					actual     string
				}{"truncated", func(*http.Request, []byte) (*http.Response, error) {
					return openAICompletion(json.RawMessage(`{"schema"`), 10, 20, "length"), nil
				}, CodeBadOutput, false, true, ""},
			)
		}
		t.Run(tc.kind+"/success", func(t *testing.T) {
			doer.routes, doer.seen = nil, nil
			doer.on(http.MethodPost, tc.url, func(*http.Request, []byte) (*http.Response, error) { return tc.ok(content, 1000, 500), nil })
			call, err := tc.client.Complete(context.Background(), "test-key", in, scriptAttempt{})
			if err != nil || call.ActualUSD == nil || math.Abs(*call.ActualUSD-wantActual) > 1e-12 {
				t.Fatalf("actual = %s, want %v (%v)", usd(call.ActualUSD), wantActual, err)
			}
		})
		for _, c := range cases {
			t.Run(tc.kind+"/"+c.name, func(t *testing.T) {
				doer.routes, doer.seen = nil, nil
				doer.on(http.MethodPost, tc.url, c.respond)
				call, err := tc.client.Complete(context.Background(), "test-key", in, scriptAttempt{})
				if err == nil {
					t.Fatal("expected an error")
				}
				code, retry := p4Code(t, err)
				if code != c.code || retry != c.retry || isRepairable(err) != c.rep {
					t.Fatalf("code = %s retry=%v repair=%v, want %s %v %v", code, retry, isRepairable(err), c.code, c.retry, c.rep)
				}
				if strings.Contains(err.Error(), "hidden") || strings.Contains(err.Error(), "test-key") {
					t.Fatalf("unsafe error text %q", err)
				}
				if c.actual != "" && usd(call.ActualUSD) != c.actual {
					t.Fatalf("actual = %s, want %s", usd(call.ActualUSD), c.actual)
				}
			})
		}
	}
}

func TestPhase4XAICostTicksAreReportedCost(t *testing.T) {
	c := newXAITextClient(nil)
	ticks := int64(25_000_001) // $0.0025000001 → rounded up to $0.002501
	got := c.usageCost(chatCompatResponse{Usage: &struct {
		PromptTokens     *int64 `json:"prompt_tokens"`
		CompletionTokens *int64 `json:"completion_tokens"`
		CostTicks        *int64 `json:"cost_in_usd_ticks"`
	}{CostTicks: &ticks}})
	if got == nil || *got != 0.002501 {
		t.Fatalf("ticks cost = %s", usd(got))
	}
	huge := int64(1e12) // $100: clamped to the per-call reservation
	if got := c.usageCost(chatCompatResponse{Usage: &struct {
		PromptTokens     *int64 `json:"prompt_tokens"`
		CompletionTokens *int64 `json:"completion_tokens"`
		CostTicks        *int64 `json:"cost_in_usd_ticks"`
	}{CostTicks: &huge}}); got == nil || *got != xaiReserveUSD {
		t.Fatalf("clamped cost = %s", usd(got))
	}
}

func TestPhase4KeyTestsSendNoContentAndReportCapabilities(t *testing.T) {
	ctx := context.Background()
	t.Run("google", func(t *testing.T) {
		doer := (&routeDoer{}).on(http.MethodGet, googleAPIBase+"/models", reply(200, map[string]any{"models": []any{
			map[string]any{"name": "models/" + googleTextModel}, map[string]any{"name": "models/" + googleImageModel}}}))
		test, err := googleKeyTester{doer: doer}.TestCapabilities(ctx, "g-key")
		seen := doer.requests()
		if err != nil || test.Text != capabilityReady || test.Images != capabilityReady || len(test.ImageModels) != 1 ||
			len(seen) != 1 || seen[0].URL != googleAPIBase+"/models?pageSize=1000" || seen[0].Header.Get("x-goog-api-key") != "g-key" || len(seen[0].Body) != 0 {
			t.Fatalf("google test = %+v %v %+v", test, err, seen)
		}
		bad := (&routeDoer{}).on(http.MethodGet, googleAPIBase, func(*http.Request, []byte) (*http.Response, error) { return googleFail(400, "API_KEY_INVALID"), nil })
		if _, err := (googleKeyTester{doer: bad}).TestCapabilities(ctx, "g-key"); err == nil {
			t.Fatal("invalid key passed")
		} else if code, _ := p4Code(t, err); code != CodeProviderAuth {
			t.Fatalf("invalid key code = %s", code)
		}
	})
	t.Run("xai", func(t *testing.T) {
		doer := (&routeDoer{}).on(http.MethodGet, xaiKeyURL, reply(200, map[string]any{"api_key_blocked": false, "api_key_disabled": false, "team_blocked": false}))
		if _, err := (xaiKeyTester{doer: doer}).TestKey(ctx, "x-key"); err != nil || doer.requests()[0].Header.Get("Authorization") != "Bearer x-key" {
			t.Fatalf("xai test = %v", err)
		}
		blocked := (&routeDoer{}).on(http.MethodGet, xaiKeyURL, reply(200, map[string]any{"api_key_blocked": false, "api_key_disabled": true}))
		if _, err := (xaiKeyTester{doer: blocked}).TestKey(ctx, "x-key"); err == nil {
			t.Fatal("disabled key passed")
		}
	})
	t.Run("together", func(t *testing.T) {
		for _, list := range []any{
			[]any{map[string]any{"id": togetherTextModel, "type": "chat"}},
			map[string]any{"data": []any{map[string]any{"id": togetherTextModel}, map[string]any{"id": togetherImageModel}}},
		} {
			doer := (&routeDoer{}).on(http.MethodGet, togetherModelsURL, reply(200, list))
			test, err := togetherKeyTester{doer: doer}.TestCapabilities(ctx, "t-key")
			if err != nil || test.Text != capabilityReady || doer.requests()[0].Header.Get("Authorization") != "Bearer t-key" {
				t.Fatalf("together test = %+v %v", test, err)
			}
		}
		quota := (&routeDoer{}).on(http.MethodGet, togetherModelsURL, reply(402, map[string]any{"error": map[string]any{"message": "x", "type": "credit_limit"}}))
		if _, err := (togetherKeyTester{doer: quota}).TestCapabilities(ctx, "t-key"); err == nil {
			t.Fatal("exhausted account passed")
		}
	})
	t.Run("replicate", func(t *testing.T) {
		doer := (&routeDoer{}).on(http.MethodGet, replicateAPI+"/account", reply(200, map[string]any{"type": "user", "username": "harbour"}))
		if _, err := (replicateKeyTester{doer: doer}).TestKey(ctx, "r8_key"); err != nil || doer.requests()[0].Header.Get("Authorization") != "Bearer r8_key" {
			t.Fatalf("replicate test = %v", err)
		}
		denied := (&routeDoer{}).on(http.MethodGet, replicateAPI, reply(401, map[string]any{"detail": "Invalid token"}))
		if _, err := (replicateKeyTester{doer: denied}).TestKey(ctx, "r8_key"); err == nil {
			t.Fatal("bad token passed")
		}
	})
	t.Run("runware", func(t *testing.T) {
		doer := (&routeDoer{}).on(http.MethodPost, runwareURL, reply(200, map[string]any{"data": []any{map[string]any{"taskType": "authentication", "connectionSessionUUID": "s"}}}))
		if _, err := (runwareKeyTester{doer: doer}).TestKey(ctx, "rw-key"); err != nil {
			t.Fatalf("runware test = %v", err)
		}
		var tasks []map[string]any
		if err := json.Unmarshal(doer.requests()[0].Body, &tasks); err != nil || len(tasks) != 1 || tasks[0]["taskType"] != "authentication" || tasks[0]["apiKey"] != "rw-key" {
			t.Fatalf("runware auth task = %s", doer.requests()[0].Body)
		}
		bad := (&routeDoer{}).on(http.MethodPost, runwareURL, reply(401, map[string]any{"errors": []any{map[string]any{"code": "invalidApiKey", "message": "Invalid API key"}}}))
		if _, err := (runwareKeyTester{doer: bad}).TestKey(ctx, "rw-key"); err == nil {
			t.Fatal("invalid key passed")
		} else if code, _ := p4Code(t, err); code != CodeProviderAuth {
			t.Fatalf("code = %s", code)
		}
	})
}

// Each fixed text route plans, reserves its frozen bound and reconciles from
// reported usage on both databases.
func TestPhase4TextRoutesReserveAndReconcile(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		in := testScriptInput()
		for _, tc := range []struct {
			kind, model, url string
			respond          func() *http.Response
			reserve          int64
			actual           int64
		}{
			{providerKindGoogle, googleTextModel, googleTextURL(), func() *http.Response { return googleOK(string(testScriptDocument(t, in)), 2000, 1000) }, 442_368, 11_250},
			{providerKindXAI, xaiTextModel, xaiChatURL, func() *http.Response { return chatOK(string(testScriptDocument(t, in)), 2000, 1000) }, 204_800, 5_000},
			{providerKindTogether, togetherTextModel, togetherChatURL, func() *http.Response { return chatOK(string(testScriptDocument(t, in)), 2000, 1000) }, 26_379, 590},
		} {
			t.Run(tc.kind, func(t *testing.T) {
				fixture := newAIDBFixture(t, d)
				providers := fixture.providers(nil)
				p := fixture.createKindProvider(t, providers, tc.kind, providerScopeUser, tc.kind+" text")
				doer := (&routeDoer{}).on(http.MethodPost, tc.url, func(*http.Request, []byte) (*http.Response, error) { return tc.respond(), nil })
				service := newTextService(d, fixture.settings, providers, newOpenRouterClient(doer), NewSpendStore(d, fixture.settings), nil).
					withClient(providerKindGoogle, newGoogleTextClient(doer)).withClient(providerKindXAI, newXAITextClient(doer)).
					withClient(providerKindTogether, newTogetherTextClient(doer))
				plan, err := service.PlanText(ctx, TextPlanRequest{Task: TaskScript, Schema: SchemaScriptV1, ActorID: fixture.userID,
					ProjectID: fixture.projectID, Input: testScriptInputJSON(t)})
				route, _ := routeForKind(tc.kind)
				if err != nil || !plan.Remote || plan.ProviderID != p.ID || plan.ModelID != tc.model || plan.Estimate.USD != route.PlanUSD {
					t.Fatalf("plan = %+v, %v", plan, err)
				}
				jobID := fixture.seedRunningModelJob(t, p.ID, tc.model)
				result, err := service.GenerateText(ctx, TextGenerateRequest{Plan: plan, ActorID: fixture.userID, ProjectID: fixture.projectID,
					JobID: jobID, Attempt: 1, Input: testScriptInputJSON(t)})
				if err != nil || result.ModelID != tc.model {
					t.Fatalf("generate = %+v, %v", result, err)
				}
				var row struct {
					Estimate int64 `db:"estimate_microusd"`
					Actual   int64 `db:"actual_microusd"`
				}
				if err := d.Get(ctx, &row, `SELECT estimate_microusd, actual_microusd FROM spend_reservations WHERE job_id = ?`, jobID); err != nil {
					t.Fatal(err)
				}
				if row.Estimate != tc.reserve || row.Actual != tc.actual {
					t.Fatalf("reservation = %+v, want %d/%d", row, tc.reserve, tc.actual)
				}
			})
		}
	})
}

// The router picks the new text routes in balanced, best and fastest modes,
// never in free_only or local_only, honours pins and skips a connection whose
// model-list check did not list the text model.
func TestPhase4TextRouterModesPinsAndCapabilities(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		fixture := newAIDBFixture(t, d)
		providers := fixture.providers(nil)
		google := fixture.createKindProvider(t, providers, providerKindGoogle, providerScopeServer, "Gemini")
		xai := fixture.createKindProvider(t, providers, providerKindXAI, providerScopeServer, "Grok")
		service := newTextService(d, fixture.settings, providers, newOpenRouterClient(&routeDoer{}), NewSpendStore(d, fixture.settings), nil)
		base := TextPlanRequest{Task: TaskScript, Schema: SchemaScriptV1, ActorID: fixture.userID, ProjectID: fixture.projectID, Input: testScriptInputJSON(t)}
		first := google.ID
		if xai.ID < google.ID {
			first = xai.ID
		}
		for _, mode := range []string{ModeBalanced, ModeBest, ModeFastest} {
			req := base
			req.RoutingMode = mode
			if plan, err := service.PlanText(ctx, req); err != nil || !plan.Remote || plan.ProviderID != first {
				t.Fatalf("%s plan = %+v, %v", mode, plan, err)
			}
		}
		for _, mode := range []string{ModeFreeOnly, ModeLocalOnly} {
			req := base
			req.RoutingMode = mode
			if plan, err := service.PlanText(ctx, req); err != nil || plan.Remote || plan.ProviderID != PlaceholderID {
				t.Fatalf("%s plan = %+v, %v", mode, plan, err)
			}
			req.PinProviderID = xai.ID
			if _, err := service.PlanText(ctx, req); err == nil {
				t.Fatalf("%s accepted a remote pin", mode)
			}
		}
		pinned := base
		pinned.PinProviderID, pinned.PinModelID = xai.ID, xaiTextModel
		if plan, err := service.PlanText(ctx, pinned); err != nil || plan.ProviderID != xai.ID || plan.ModelID != xaiTextModel {
			t.Fatalf("pinned plan = %+v, %v", plan, err)
		}
		pinned.PinModelID = googleTextModel
		if _, err := service.PlanText(ctx, pinned); !isCode(err, CodeNoProvider) {
			t.Fatalf("mismatched pin = %v", err)
		}
		// A Google key whose model list lacked the text model is skipped.
		if err := providers.recordTestResult(ctx, google.ID, providerKindGoogle, keyEncOf(t, d, google.ID),
			providerTestState{State: "ok", Text: capabilityUnavailable, Images: capabilityReady, ImageModels: []string{googleImageModel}}); err != nil {
			t.Fatal(err)
		}
		if plan, err := service.PlanText(ctx, base); err != nil || plan.ProviderID != xai.ID {
			t.Fatalf("plan after the capability check = %+v, %v", plan, err)
		}
		// Instance local-only blocks every remote route before any request.
		updateInstance(t, fixture.settings, func(inst *settings.Instance) { inst.LocalOnly = true })
		if plan, err := service.PlanText(ctx, base); err != nil || plan.Remote {
			t.Fatalf("local-only plan = %+v, %v", plan, err)
		}
	})
}

func keyEncOf(t *testing.T, d *db.DB, id string) string {
	t.Helper()
	var enc string
	if err := d.Get(context.Background(), &enc, `SELECT key_enc FROM providers WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	return enc
}
