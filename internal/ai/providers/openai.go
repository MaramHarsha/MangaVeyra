package providers

import (
	"context"
	"net/http"
	"strconv"
)

// OpenAI speaks the OpenAI API: /v1/images/generations, /v1/images/edits,
// /v1/chat/completions, /v1/audio/speech and /v1/models.
type OpenAI struct{ Env Env }

// Kind implements Adapter.
func (OpenAI) Kind() string { return KindOpenAI }

func (a OpenAI) base(cfg Config) string { return baseOr(cfg, "https://api.openai.com/v1") }

func (a OpenAI) headers(cfg Config) map[string]string {
	if cfg.APIKey == "" {
		return map[string]string{}
	}
	return map[string]string{"Authorization": "Bearer " + cfg.APIKey}
}

// Image generates (images/generations) or edits (images/edits with the source, references and mask).
func (a OpenAI) Image(ctx context.Context, cfg Config, req ImageRequest, progress Progress) (*ImageResult, error) {
	w, h := req.Size()
	prompt := stylePrompt(req.Prompt, req.Style)
	progress.report(0.1, "sending to OpenAI")
	var out imagesResponse
	if req.Image != nil || len(req.Refs) > 0 || req.Mask != nil {
		images := []Image{}
		if req.Image != nil {
			images = append(images, *req.Image)
		}
		images = append(images, req.Refs...)
		files := map[string][]Image{"image[]": images}
		if req.Mask != nil {
			files["mask"] = []Image{*req.Mask}
		}
		body, ct, err := multipartImages(map[string]string{"model": req.Model, "prompt": prompt, "n": strconv.Itoa(req.Count()),
			"size": openAISize(w, h)}, files)
		if err != nil {
			return nil, Errf(CodeInternal, "encode edit: %v", err)
		}
		hr, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL(a.base(cfg), "images/edits"), body)
		if err != nil {
			return nil, Errf(CodeBadRequest, "%v", err)
		}
		hr.Header.Set("Content-Type", ct)
		hr.Header.Set("User-Agent", userAgent)
		for k, v := range a.headers(cfg) {
			hr.Header.Set(k, v)
		}
		b, _, err := a.Env.send(hr)
		if err != nil {
			return nil, err
		}
		if err := jsonUnmarshal(b, &out); err != nil {
			return nil, err
		}
	} else {
		in := map[string]any{"model": req.Model, "prompt": prompt, "n": req.Count(), "size": openAISize(w, h)}
		if err := a.Env.call(ctx, http.MethodPost, joinURL(a.base(cfg), "images/generations"), a.headers(cfg), in, &out); err != nil {
			return nil, err
		}
	}
	progress.report(0.9, "downloading")
	return a.Env.collectImages(ctx, &out)
}

// Text runs a chat completion.
func (a OpenAI) Text(ctx context.Context, cfg Config, req TextRequest) (*TextResult, error) {
	return a.Env.chatText(ctx, joinURL(a.base(cfg), "chat/completions"), a.headers(cfg), buildChat(req, true))
}

// Media synthesises speech with /audio/speech (voice only).
func (a OpenAI) Media(ctx context.Context, cfg Config, req MediaRequest, progress Progress) (*MediaResult, error) {
	if req.Task != "voice" {
		return nil, unsupported("OpenAI", "video")
	}
	voice := req.Voice
	if voice == "" {
		voice = "alloy"
	}
	r, err := newRequest(ctx, http.MethodPost, joinURL(a.base(cfg), "audio/speech"),
		map[string]any{"model": req.Model, "input": req.Text, "voice": voice, "response_format": "mp3", "instructions": req.Prompt}, a.headers(cfg))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Accept", "audio/mpeg")
	progress.report(0.2, "synthesising")
	b, h, err := a.Env.send(r)
	if err != nil {
		return nil, err
	}
	mime := h.Get("Content-Type")
	if mime == "" {
		mime = "audio/mpeg"
	}
	return &MediaResult{Media: Image{Data: b, MIME: mime}}, nil
}

// Models lists the account's models.
func (a OpenAI) Models(ctx context.Context, cfg Config) ([]RemoteModel, error) {
	return a.Env.listModels(ctx, joinURL(a.base(cfg), "models"), a.headers(cfg))
}
