package providers

import (
	"context"
	"errors"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Anthropic calls the Claude Messages API through the official Go SDK (text
// only; Anthropic makes no image model). The SDK uses the guarded HTTP client
// from Env, and a refusal stop reason becomes a safety error so the router
// falls back to the next candidate.
type Anthropic struct{ Env Env }

// Kind implements Adapter.
func (Anthropic) Kind() string { return KindAnthropic }

func (a Anthropic) client(cfg Config) anthropic.Client {
	opts := []option.RequestOption{option.WithAPIKey(cfg.APIKey), option.WithHTTPClient(a.Env.client()), option.WithMaxRetries(1)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(strings.TrimRight(cfg.BaseURL, "/")+"/"))
	}
	return anthropic.NewClient(opts...)
}

// Text sends one Messages request. Current models reject sampling
// parameters, so Temperature is not forwarded.
func (a Anthropic) Text(ctx context.Context, cfg Config, req TextRequest) (*TextResult, error) {
	c := a.client(cfg)
	params := anthropic.MessageNewParams{Model: anthropic.Model(req.Model), MaxTokens: int64(req.MaxTokensOr(16000))}
	system := req.System
	if req.JSON {
		system = strings.TrimSpace(system + "\nAnswer with a single JSON object and nothing else.")
	}
	if system != "" {
		params.System = []anthropic.TextBlockParam{{Text: system}}
	}
	for _, m := range req.Messages {
		if m.Role == "assistant" {
			params.Messages = append(params.Messages, anthropic.NewAssistantMessage(anthropic.NewTextBlock(m.Content)))
		} else {
			params.Messages = append(params.Messages, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))
		}
	}
	resp, err := c.Messages.New(ctx, params)
	if err != nil {
		return nil, anthropicError(ctx, err)
	}
	if resp.StopReason == anthropic.StopReasonRefusal {
		return nil, Errf(CodeSafety, "Claude declined the request")
	}
	var b strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return &TextResult{Text: b.String(), TokensIn: int(resp.Usage.InputTokens), TokensOut: int(resp.Usage.OutputTokens)}, nil
}

// Models lists the models the key can use.
func (a Anthropic) Models(ctx context.Context, cfg Config) ([]RemoteModel, error) {
	c := a.client(cfg)
	it := c.Models.ListAutoPaging(ctx, anthropic.ModelListParams{})
	var out []RemoteModel
	for it.Next() {
		m := it.Current()
		out = append(out, RemoteModel{ID: m.ID, Name: m.DisplayName, Kind: "text", InputImages: true})
	}
	if err := it.Err(); err != nil {
		return nil, anthropicError(ctx, err)
	}
	return out, nil
}

func anthropicError(ctx context.Context, err error) error {
	var ae *anthropic.Error
	if errors.As(err, &ae) {
		msg := errorMessage([]byte(ae.RawJSON()))
		if msg == "" {
			msg = ae.Error()
		}
		e := statusError(ae.StatusCode, []byte(ae.RawJSON()))
		e.Message = msg
		if ae.StatusCode == 529 {
			e.Code = CodeUnavailable
		}
		return e
	}
	return transportError(ctx, err)
}
