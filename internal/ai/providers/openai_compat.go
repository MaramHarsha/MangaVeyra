package providers

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"sort"
	"strings"
)

// This file holds the OpenAI wire format shared by OpenAI, Together, xAI,
// Ollama, Hugging Face text endpoints and any OpenAI-compatible server
// (LiteLLM, vLLM, LM Studio, llama.cpp).

type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type chatRequest struct {
	Model               string         `json:"model"`
	Messages            []chatMessage  `json:"messages"`
	MaxTokens           int            `json:"max_tokens,omitempty"`
	MaxCompletionTokens int            `json:"max_completion_tokens,omitempty"`
	Temperature         *float64       `json:"temperature,omitempty"`
	ResponseFormat      map[string]any `json:"response_format,omitempty"`
	Stream              bool           `json:"stream"`
	// OpenRouter extensions.
	Modalities []string       `json:"modalities,omitempty"`
	Usage      map[string]any `json:"usage,omitempty"`
	ImageCfg   map[string]any `json:"image_config,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content any `json:"content"`
			Refusal any `json:"refusal"`
			Images  []struct {
				Type     string `json:"type"`
				ImageURL struct {
					URL string `json:"url"`
				} `json:"image_url"`
			} `json:"images"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int     `json:"prompt_tokens"`
		CompletionTokens int     `json:"completion_tokens"`
		Cost             float64 `json:"cost"`
	} `json:"usage"`
	Error any `json:"error"`
}

// contentText flattens string or [{type:text,text}] content.
func contentText(c any) string {
	switch t := c.(type) {
	case string:
		return t
	case []any:
		var b strings.Builder
		for _, p := range t {
			if m, ok := p.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					b.WriteString(s)
				}
			}
		}
		return b.String()
	}
	return ""
}

func buildChat(req TextRequest, useCompletionTokens bool) chatRequest {
	cr := chatRequest{Model: req.Model, Temperature: req.Temperature}
	if req.System != "" {
		cr.Messages = append(cr.Messages, chatMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		cr.Messages = append(cr.Messages, chatMessage{Role: m.Role, Content: m.Content})
	}
	if useCompletionTokens {
		cr.MaxCompletionTokens = req.MaxTokensOr(4096)
	} else {
		cr.MaxTokens = req.MaxTokensOr(4096)
	}
	if req.JSON {
		cr.ResponseFormat = map[string]any{"type": "json_object"}
	}
	return cr
}

// chatCall runs a chat completion against url.
func (e Env) chatCall(ctx context.Context, url string, headers map[string]string, cr chatRequest) (*chatResponse, error) {
	var out chatResponse
	if err := e.call(ctx, http.MethodPost, url, headers, cr, &out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		msg := errorMessage([]byte(fmt.Sprint(out.Error)))
		if looksLikeSafety(msg) {
			return nil, Errf(CodeSafety, "%s", msg)
		}
		return nil, Errf(CodeInternal, "%s", msg)
	}
	if len(out.Choices) == 0 {
		return nil, Errf(CodeInternal, "no choices in response")
	}
	if fr := out.Choices[0].FinishReason; fr == "content_filter" {
		return nil, Errf(CodeSafety, "the provider's content filter stopped the answer")
	}
	if r, ok := out.Choices[0].Message.Refusal.(string); ok && r != "" {
		return nil, Errf(CodeSafety, "%s", r)
	}
	return &out, nil
}

func (e Env) chatText(ctx context.Context, url string, headers map[string]string, cr chatRequest) (*TextResult, error) {
	out, err := e.chatCall(ctx, url, headers, cr)
	if err != nil {
		return nil, err
	}
	return &TextResult{Text: contentText(out.Choices[0].Message.Content), TokensIn: out.Usage.PromptTokens,
		TokensOut: out.Usage.CompletionTokens, CostUSD: out.Usage.Cost}, nil
}

type imagesResponse struct {
	Data []struct {
		B64JSON       string `json:"b64_json"`
		URL           string `json:"url"`
		RevisedPrompt string `json:"revised_prompt"`
	} `json:"data"`
	Usage map[string]any `json:"usage"`
}

// collectImages turns an images response into media (downloading URLs).
func (e Env) collectImages(ctx context.Context, out *imagesResponse) (*ImageResult, error) {
	res := &ImageResult{}
	for _, d := range out.Data {
		var img Image
		var err error
		switch {
		case d.B64JSON != "":
			img, err = decodeB64(d.B64JSON)
		case d.URL != "":
			img, err = e.fetch(ctx, d.URL, nil)
		default:
			continue
		}
		if err != nil {
			return nil, err
		}
		res.Images = append(res.Images, img)
		if d.RevisedPrompt != "" {
			res.Note = d.RevisedPrompt
		}
	}
	if len(res.Images) == 0 {
		return nil, Errf(CodeInternal, "the provider returned no images")
	}
	return res, nil
}

// openAISize maps a request size to the closest size the OpenAI image API accepts.
func openAISize(w, h int) string {
	switch {
	case w == h:
		return "1024x1024"
	case h > w:
		return "1024x1536"
	}
	return "1536x1024"
}

// multipartImages builds the multipart body of an OpenAI-style /images/edits call.
func multipartImages(fields map[string]string, files map[string][]Image) (*bytes.Buffer, string, error) {
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if fields[k] == "" {
			continue
		}
		if err := mw.WriteField(k, fields[k]); err != nil {
			return nil, "", err
		}
	}
	fkeys := make([]string, 0, len(files))
	for k := range files {
		fkeys = append(fkeys, k)
	}
	sort.Strings(fkeys)
	for _, k := range fkeys {
		for i, img := range files[k] {
			mime := img.MIME
			if mime == "" {
				mime = http.DetectContentType(img.Data)
			}
			ext := "png"
			switch mime {
			case "image/jpeg":
				ext = "jpg"
			case "image/webp":
				ext = "webp"
			}
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename="%s-%d.%s"`, k, strings.Trim(k, "[]"), i, ext))
			h.Set("Content-Type", mime)
			w, err := mw.CreatePart(h)
			if err != nil {
				return nil, "", err
			}
			if _, err := w.Write(img.Data); err != nil {
				return nil, "", err
			}
		}
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return buf, mw.FormDataContentType(), nil
}

type modelsResponse struct {
	Data []struct {
		ID      string `json:"id"`
		OwnedBy string `json:"owned_by"`
	} `json:"data"`
}

// listModels calls GET {base}/models.
func (e Env) listModels(ctx context.Context, url string, headers map[string]string) ([]RemoteModel, error) {
	var out modelsResponse
	if err := e.call(ctx, http.MethodGet, url, headers, nil, &out); err != nil {
		return nil, err
	}
	res := make([]RemoteModel, 0, len(out.Data))
	for _, d := range out.Data {
		kind := "text"
		id := strings.ToLower(d.ID)
		if strings.Contains(id, "image") || strings.Contains(id, "flux") || strings.Contains(id, "diffusion") || strings.Contains(id, "dall-e") {
			kind = "image"
		}
		res = append(res, RemoteModel{ID: d.ID, Name: d.ID, Kind: kind, OutputImages: kind == "image"})
	}
	return res, nil
}

func bearer(key string) map[string]string {
	if key == "" {
		return map[string]string{}
	}
	return map[string]string{"Authorization": "Bearer " + key}
}
