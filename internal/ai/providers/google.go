package providers

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"time"
)

// Google speaks the Gemini API (generativelanguage.googleapis.com/v1beta):
// generateContent with responseModalities ["TEXT","IMAGE"] for images
// (inline_data parts carry references and sources), generateContent for
// text and predictLongRunning + operation polling for Veo video.
type Google struct{ Env Env }

// Kind implements Adapter.
func (Google) Kind() string { return KindGoogle }

func (a Google) base(cfg Config) string {
	return baseOr(cfg, "https://generativelanguage.googleapis.com/v1beta")
}

func (a Google) headers(cfg Config) map[string]string {
	return map[string]string{"x-goog-api-key": cfg.APIKey}
}

type geminiPart struct {
	Text       string        `json:"text,omitempty"`
	InlineData *geminiInline `json:"inline_data,omitempty"`
}

type geminiInline struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text       string `json:"text"`
				InlineData *struct {
					MimeType string `json:"mimeType"`
					Data     string `json:"data"`
				} `json:"inlineData"`
				Thought bool `json:"thought"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
}

func inline(img Image) geminiPart {
	mime := img.MIME
	if mime == "" {
		mime = http.DetectContentType(img.Data)
	}
	return geminiPart{InlineData: &geminiInline{MimeType: mime, Data: base64.StdEncoding.EncodeToString(img.Data)}}
}

func (a Google) generate(ctx context.Context, cfg Config, model string, body map[string]any) (*geminiResponse, error) {
	var out geminiResponse
	if err := a.Env.call(ctx, http.MethodPost, joinURL(a.base(cfg), "models/"+model+":generateContent"), a.headers(cfg), body, &out); err != nil {
		return nil, err
	}
	if out.PromptFeedback.BlockReason != "" {
		return nil, Errf(CodeSafety, "Gemini blocked the prompt (%s)", out.PromptFeedback.BlockReason)
	}
	if len(out.Candidates) == 0 {
		return nil, Errf(CodeInternal, "Gemini returned no candidates")
	}
	switch fr := out.Candidates[0].FinishReason; fr {
	case "SAFETY", "IMAGE_SAFETY", "PROHIBITED_CONTENT", "BLOCKLIST", "SPII", "RECITATION", "IMAGE_PROHIBITED_CONTENT":
		return nil, Errf(CodeSafety, "Gemini stopped for safety (%s)", fr)
	}
	return &out, nil
}

// Image generates or edits images (Nano Banana models).
func (a Google) Image(ctx context.Context, cfg Config, req ImageRequest, progress Progress) (*ImageResult, error) {
	w, h := req.Size()
	parts := []geminiPart{{Text: stylePrompt(req.Prompt, req.Style)}}
	if req.Negative != "" {
		parts[0].Text += "\nAvoid: " + req.Negative
	}
	if req.Image != nil {
		parts = append(parts, inline(*req.Image))
	}
	for _, r := range req.Refs {
		parts = append(parts, inline(r))
	}
	size := "1K"
	switch long := max(w, h); {
	case long > 2048:
		size = "4K"
	case long > 1024:
		size = "2K"
	}
	body := map[string]any{
		"contents": []map[string]any{{"role": "user", "parts": parts}},
		"generationConfig": map[string]any{"responseModalities": []string{"TEXT", "IMAGE"},
			"imageConfig": map[string]any{"aspectRatio": aspect(w, h), "imageSize": size}},
	}
	res := &ImageResult{}
	for i := 0; i < req.Count(); i++ {
		progress.report(float64(i)/float64(req.Count()), "generating at Google")
		out, err := a.generate(ctx, cfg, req.Model, body)
		if err != nil {
			return nil, err
		}
		for _, p := range out.Candidates[0].Content.Parts {
			if p.InlineData != nil && !p.Thought && strings.HasPrefix(p.InlineData.MimeType, "image/") {
				b, err := base64.StdEncoding.DecodeString(p.InlineData.Data)
				if err != nil {
					return nil, Errf(CodeInternal, "invalid image data")
				}
				res.Images = append(res.Images, Image{Data: b, MIME: p.InlineData.MimeType})
			} else if p.Text != "" && !p.Thought {
				res.Note = p.Text
			}
		}
	}
	if len(res.Images) == 0 {
		if looksLikeSafety(res.Note) {
			return nil, Errf(CodeSafety, "%s", res.Note)
		}
		return nil, Errf(CodeInternal, "Gemini returned no image")
	}
	return res, nil
}

// Text runs generateContent with a system instruction.
func (a Google) Text(ctx context.Context, cfg Config, req TextRequest) (*TextResult, error) {
	var contents []map[string]any
	for _, m := range req.Messages {
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		}
		contents = append(contents, map[string]any{"role": role, "parts": []geminiPart{{Text: m.Content}}})
	}
	gc := map[string]any{"maxOutputTokens": req.MaxTokensOr(4096)}
	if req.Temperature != nil {
		gc["temperature"] = *req.Temperature
	}
	if req.JSON {
		gc["responseMimeType"] = "application/json"
	}
	body := map[string]any{"contents": contents, "generationConfig": gc}
	if req.System != "" {
		body["system_instruction"] = map[string]any{"parts": []geminiPart{{Text: req.System}}}
	}
	out, err := a.generate(ctx, cfg, req.Model, body)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	for _, p := range out.Candidates[0].Content.Parts {
		if !p.Thought {
			b.WriteString(p.Text)
		}
	}
	return &TextResult{Text: b.String(), TokensIn: out.UsageMetadata.PromptTokenCount, TokensOut: out.UsageMetadata.CandidatesTokenCount}, nil
}

// Media generates Veo video through a long-running operation.
func (a Google) Media(ctx context.Context, cfg Config, req MediaRequest, progress Progress) (*MediaResult, error) {
	if req.Task != "video" {
		return nil, unsupported("Google", req.Task)
	}
	inst := map[string]any{"prompt": req.Prompt}
	if req.Image != nil {
		mime := req.Image.MIME
		if mime == "" {
			mime = http.DetectContentType(req.Image.Data)
		}
		inst["image"] = map[string]any{"bytesBase64Encoded": base64.StdEncoding.EncodeToString(req.Image.Data), "mimeType": mime}
	}
	params := map[string]any{}
	if req.Aspect != "" {
		params["aspectRatio"] = req.Aspect
	}
	if req.Seconds > 0 {
		params["durationSeconds"] = int(req.Seconds + .5)
	}
	var op struct {
		Name  string `json:"name"`
		Done  bool   `json:"done"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Response struct {
			GenerateVideoResponse struct {
				GeneratedSamples []struct {
					Video struct {
						URI string `json:"uri"`
					} `json:"video"`
				} `json:"generatedSamples"`
				RaiMediaFilteredReasons []string `json:"raiMediaFilteredReasons"`
			} `json:"generateVideoResponse"`
		} `json:"response"`
	}
	if err := a.Env.call(ctx, http.MethodPost, joinURL(a.base(cfg), "models/"+req.Model+":predictLongRunning"), a.headers(cfg),
		map[string]any{"instances": []any{inst}, "parameters": params}, &op); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(a.Env.maxWait())
	for !op.Done {
		progress.report(0.4, "rendering at Google")
		if time.Now().After(deadline) {
			return nil, Errf(CodeTimeout, "Veo did not finish in %s", a.Env.maxWait())
		}
		if err := sleep(ctx, a.Env.poll()); err != nil {
			return nil, err
		}
		if err := a.Env.call(ctx, http.MethodGet, joinURL(a.base(cfg), op.Name), a.headers(cfg), nil, &op); err != nil {
			return nil, err
		}
	}
	if op.Error != nil {
		if looksLikeSafety(op.Error.Message) {
			return nil, Errf(CodeSafety, "%s", op.Error.Message)
		}
		return nil, Errf(CodeInternal, "%s", op.Error.Message)
	}
	gv := op.Response.GenerateVideoResponse
	if len(gv.GeneratedSamples) == 0 {
		if len(gv.RaiMediaFilteredReasons) > 0 {
			return nil, Errf(CodeSafety, "%s", strings.Join(gv.RaiMediaFilteredReasons, "; "))
		}
		return nil, Errf(CodeInternal, "Veo returned no video")
	}
	m, err := a.Env.fetch(ctx, gv.GeneratedSamples[0].Video.URI, a.headers(cfg))
	if err != nil {
		return nil, err
	}
	return &MediaResult{Media: m, Seconds: req.Seconds}, nil
}

// Models lists Gemini models (image output is recognised by name).
func (a Google) Models(ctx context.Context, cfg Config) ([]RemoteModel, error) {
	var out struct {
		Models []struct {
			Name                       string   `json:"name"`
			DisplayName                string   `json:"displayName"`
			InputTokenLimit            int      `json:"inputTokenLimit"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := a.Env.call(ctx, http.MethodGet, joinURL(a.base(cfg), "models?pageSize=1000"), a.headers(cfg), nil, &out); err != nil {
		return nil, err
	}
	var res []RemoteModel
	for _, m := range out.Models {
		id := strings.TrimPrefix(m.Name, "models/")
		rm := RemoteModel{ID: id, Name: m.DisplayName, Context: m.InputTokenLimit, Kind: "text", InputImages: true}
		switch {
		case strings.Contains(id, "image"):
			rm.Kind, rm.OutputImages = "image", true
		case strings.HasPrefix(id, "veo"):
			rm.Kind = "video"
		case strings.Contains(id, "tts"):
			rm.Kind = "voice"
		case strings.Contains(id, "embedding"):
			continue
		}
		res = append(res, rm)
	}
	return res, nil
}
