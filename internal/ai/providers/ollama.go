package providers

import (
	"context"
	"net/http"
	"strings"
)

// Ollama talks to a local Ollama server: chat through its OpenAI-compatible
// /v1/chat/completions endpoint and model discovery through /api/tags.
type Ollama struct{ Env Env }

// Kind implements Adapter.
func (Ollama) Kind() string { return KindOllama }

func (a Ollama) root(cfg Config) string {
	b := strings.TrimRight(baseOr(cfg, "http://localhost:11434"), "/")
	return strings.TrimSuffix(b, "/v1")
}

// Text runs a chat completion.
func (a Ollama) Text(ctx context.Context, cfg Config, req TextRequest) (*TextResult, error) {
	return a.Env.chatText(ctx, a.root(cfg)+"/v1/chat/completions", bearer(cfg.APIKey), buildChat(req, false))
}

// Models lists pulled models.
func (a Ollama) Models(ctx context.Context, cfg Config) ([]RemoteModel, error) {
	var out struct {
		Models []struct {
			Name    string `json:"name"`
			Model   string `json:"model"`
			Details struct {
				Family        string   `json:"family"`
				Families      []string `json:"families"`
				ParameterSize string   `json:"parameter_size"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := a.Env.call(ctx, http.MethodGet, a.root(cfg)+"/api/tags", bearer(cfg.APIKey), nil, &out); err != nil {
		return nil, err
	}
	res := make([]RemoteModel, 0, len(out.Models))
	for _, m := range out.Models {
		name := m.Name
		if name == "" {
			name = m.Model
		}
		desc := strings.TrimSpace(m.Details.Family + " " + m.Details.ParameterSize)
		vision := false
		for _, f := range m.Details.Families {
			if f == "clip" || f == "mllama" {
				vision = true
			}
		}
		res = append(res, RemoteModel{ID: name, Name: name, Kind: "text", InputImages: vision, Description: desc})
	}
	return res, nil
}
