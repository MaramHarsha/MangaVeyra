package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core/netguard"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

const providerKindLocalText = "openai-compatible-local"
const providerKindFal = "fal"
const providerKindWorker = "local-worker"

type localTextConfig struct {
	Model         string `json:"model"`
	ContextTokens int    `json:"context_tokens"`
	OutputTokens  int    `json:"output_tokens"`
}

func (r providerRecord) localConfig() localTextConfig {
	var c localTextConfig
	if r.Kind == providerKindLocalText {
		dec := json.NewDecoder(strings.NewReader(r.Config))
		dec.DisallowUnknownFields()
		var extra any
		if dec.Decode(&c) != nil || dec.Decode(&extra) != io.EOF {
			return localTextConfig{}
		}
	}
	return c
}
func validateLocalTextConfig(base string, c localTextConfig) error {
	u, err := url.Parse(base)
	if err != nil || u.User != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || strings.TrimRight(u.Path, "/") != "/v1" || !validModelID(strings.ReplaceAll(c.Model, ":", "-")) || c.ContextTokens < 4096 || c.ContextTokens > 1_048_576 || c.OutputTokens < 256 || c.OutputTokens > 16384 || c.OutputTokens >= c.ContextTokens {
		return errProviderInvalid
	}
	return nil
}

func localTextFits(in ScriptInput, c localTextConfig) bool {
	for _, a := range []scriptAttempt{{}, repairFitAttempt} {
		r, err := buildOpenRouterRequest(in, a)
		if err != nil {
			return false
		}
		if len(r.Messages[0].Content)+len(r.Messages[1].Content)+len(r.ResponseFormat.JSONSchema.Schema)+4096+c.OutputTokens > c.ContextTokens {
			return false
		}
	}
	return true
}

// planLocalText only handles eligible explicitly configured local connections.
// Local-only intentionally permits no HTTP, including loopback, in this phase.
func (s *textService) planLocalText(ctx context.Context, req TextPlanRequest, in ScriptInput) (TextPlan, bool, error) {
	candidates, err := s.providers.Candidates(ctx, req.ActorID)
	if err != nil {
		return TextPlan{}, false, nil
	}
	mode := req.RoutingMode
	inst, err := s.localSettings(ctx)
	if err != nil {
		return TextPlan{}, false, nil
	}
	if mode == "" {
		mode = inst.RoutingMode
	}
	for _, candidate := range candidates {
		if candidate.Kind != providerKindLocalText || req.PinProviderID != "" && req.PinProviderID != candidate.ID || req.PinModelID != "" && req.PinModelID != candidate.Model {
			continue
		}
		if req.PinProviderID == "" && req.PinModelID == "" && mode == ModeBest {
			continue
		}
		if inst.LocalOnly || mode == ModeLocalOnly {
			if req.PinProviderID != "" || req.PinModelID != "" {
				return TextPlan{}, true, localOnlyError(nil)
			}
			continue
		}
		p, err := s.providers.ResolveDispatch(ctx, req.ActorID, candidate.ID)
		if err != nil {
			continue
		}
		if !localTextFits(in, p.Local) {
			if req.PinProviderID != "" || req.PinModelID != "" {
				return TextPlan{}, true, badInputError(nil)
			}
			continue
		}
		return TextPlan{Task: req.Task, Schema: req.Schema, RoutingMode: mode, ProviderID: p.ID, ModelID: p.Local.Model, Remote: false, Decision: marshalDecision(textDecision{Mode: mode, ProviderID: p.ID, ModelID: p.Local.Model, Reason: "connected local text endpoint"})}, true, nil
	}
	return TextPlan{}, false, nil
}

func (s *textService) generateLocalText(ctx context.Context, req TextGenerateRequest, in ScriptInput) (TextResult, error) {
	var zero TextResult
	if req.Plan.Remote || req.Plan.RoutingMode == ModeLocalOnly || req.JobID == "" || req.Attempt < 1 {
		return zero, localOnlyError(nil)
	}
	p, err := s.providers.ResolveDispatch(ctx, req.ActorID, req.Plan.ProviderID)
	if err != nil || p.Kind != providerKindLocalText || p.Local.Model != req.Plan.ModelID || !localTextFits(in, p.Local) {
		return zero, badInputError(nil)
	}
	var valid int
	if err := s.db.Get(ctx, &valid, `SELECT COUNT(*) FROM jobs WHERE id = ? AND user_id = ? AND project_id = ? AND state = 'running' AND attempts = ? AND cancel_requested = FALSE AND provider_id = ? AND model_id = ?`, req.JobID, req.ActorID, req.ProjectID, req.Attempt, p.ID, p.Local.Model); err != nil || valid != 1 {
		return zero, badInputError(nil)
	}
	reason := ""
	for attempt := 0; attempt < 2; attempt++ {
		inst, err := s.localSettings(ctx)
		if err != nil || inst.LocalOnly {
			return zero, localOnlyError(nil)
		}
		p, err = s.providers.ResolveDispatch(ctx, req.ActorID, req.Plan.ProviderID)
		if err != nil || p.Kind != providerKindLocalText || p.Local.Model != req.Plan.ModelID || !localTextFits(in, p.Local) {
			return zero, noProviderError(nil)
		}
		if err := s.db.Get(ctx, &valid, `SELECT COUNT(*) FROM jobs WHERE id = ? AND user_id = ? AND project_id = ? AND state = 'running' AND attempts = ? AND cancel_requested = FALSE AND provider_id = ? AND model_id = ?`, req.JobID, req.ActorID, req.ProjectID, req.Attempt, p.ID, p.Local.Model); err != nil || valid != 1 {
			return zero, badInputError(nil)
		}
		request, _ := buildOpenRouterRequest(in, scriptAttempt{Repair: attempt > 0, Reason: reason})
		payload := struct {
			Model          string                   `json:"model"`
			Messages       []openRouterMessage      `json:"messages"`
			MaxTokens      int                      `json:"max_tokens"`
			ResponseFormat openRouterResponseFormat `json:"response_format"`
			Stream         bool                     `json:"stream"`
		}{p.Local.Model, request.Messages, p.Local.OutputTokens, request.ResponseFormat, false}
		raw, _ := json.Marshal(payload)
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.BaseURL, "/")+"/chat/completions", bytes.NewReader(raw))
		if err != nil {
			return zero, badInputError(nil)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if p.Key != "" {
			httpReq.Header.Set("Authorization", "Bearer "+p.Key)
		}
		doer := s.localDoer
		if doer == nil {
			return zero, noProviderError(nil)
		}
		resp, err := doer.Do(httpReq)
		if err != nil {
			return zero, generateErrorFrom(classifyTransportError(err), zero.Actual)
		}
		if resp == nil || resp.Body == nil {
			return zero, upstreamError(nil, false, zero.Actual)
		}
		data, large, readErr := readBounded(resp.Body, openRouterResponseLimit)
		resp.Body.Close()
		if readErr != nil || large {
			return zero, generateErrorFrom(badOutputAdapterError(false), zero.Actual)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return zero, generateErrorFrom(classifyHTTPStatus(resp.StatusCode), zero.Actual)
		}
		var result openRouterResponse
		if json.Unmarshal(data, &result) != nil || len(result.Choices) != 1 {
			return zero, generateErrorFrom(badOutputAdapterError(false), zero.Actual)
		}
		if result.Choices[0].Message.Refusal != "" {
			return zero, generateErrorFrom(providerAdapterError(CodeProviderRefuse, false, false), zero.Actual)
		}
		_, doc, decodeErr := decodeScriptDocument([]byte(result.Choices[0].Message.Content), in)
		if decodeErr == nil {
			return TextResult{Document: doc, ProviderID: p.ID, ModelID: p.Local.Model}, nil
		}
		reason = scriptOutputReason(decodeErr)
	}
	return zero, &GenerateError{Code: CodeBadOutput, SafeMessage: invalidScriptMessage(reason), Err: errInvalidScriptOutput}
}

// newLocalTextHTTPClient dials only loopback and private-network addresses
// (custom connections are local servers) and refuses every request while the
// live private-endpoint setting is off. Link-local and metadata addresses stay
// blocked by netguard in every case.
func newLocalTextHTTPClient(private *privateEndpointPolicy) *http.Client {
	policy := netguard.Policy{AllowPrivate: true, AllowHTTP: true}
	transport := policy.Transport()
	dialer := policy.Dialer()
	dialer.Control = func(network, address string, conn syscall.RawConn) error {
		if err := policy.Control(network, address, conn); err != nil {
			return err
		}
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return err
		}
		ip := ap.Addr().Unmap()
		if !ip.IsLoopback() && !ip.IsPrivate() {
			return netguard.ErrBlocked
		}
		return nil
	}
	transport.DialContext = dialer.DialContext
	return &http.Client{Timeout: 2 * time.Minute, Transport: privateGate{policy: private, next: transport}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("ai: provider redirects are disabled") }}
}

func testLocalText(ctx context.Context, p dispatchProvider, doer httpDoer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.BaseURL, "/")+"/models", nil)
	if err != nil {
		return errProviderInvalid
	}
	if p.Key != "" {
		req.Header.Set("Authorization", "Bearer "+p.Key)
	}
	resp, err := doer.Do(req)
	if err != nil {
		return errProviderUnavailable
	}
	if resp == nil || resp.Body == nil {
		return errProviderUnavailable
	}
	defer resp.Body.Close()
	data, large, err := readBounded(resp.Body, 1<<20)
	if err != nil || large || resp.StatusCode != 200 {
		return errProviderUnavailable
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &body) != nil {
		return errProviderUnavailable
	}
	for _, m := range body.Data {
		if m.ID == p.Local.Model {
			return nil
		}
	}
	return errProviderUnavailable
}

// Custom local text has no remote reservation transaction, so its privacy
// admission must independently bypass the settings cache before every call.
func (s *textService) localSettings(ctx context.Context) (settings.Instance, error) {
	if reader, ok := s.settings.(interface {
		InstanceTx(context.Context, *db.Tx) (settings.Instance, error)
	}); ok && s.db != nil {
		var inst settings.Instance
		err := s.db.WithTx(ctx, func(tx *db.Tx) error {
			var err error
			inst, err = reader.InstanceTx(ctx, tx)
			if err != nil {
				return err
			}
			return inst.Validate()
		})
		return inst, err
	}
	return loadRemoteSettings(ctx, s.settings)
}
