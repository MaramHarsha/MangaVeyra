package providers

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"strings"
)

// Runware speaks the Runware task API: POST https://api.runware.ai/v1 with a
// JSON array of tasks ({"taskType":"imageInference", "taskUUID", …}); the
// response carries {"data":[…]} with one entry per image (base64 or URL and
// the cost when includeCost is set) or {"errors":[…]}.
type Runware struct{ Env Env }

// Kind implements Adapter.
func (Runware) Kind() string { return KindRunware }

func (a Runware) base(cfg Config) string { return baseOr(cfg, "https://api.runware.ai/v1") }

func uuid4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// round64 rounds to a multiple of 64 within Runware's 128–2048 range.
func round64(v int) int {
	v = (v + 32) / 64 * 64
	return max(128, min(v, 2048))
}

// Image runs imageInference (or imageUpscale for upscale).
func (a Runware) Image(ctx context.Context, cfg Config, req ImageRequest, progress Progress) (*ImageResult, error) {
	w, h := req.Size()
	id := uuid4()
	task := map[string]any{"taskType": "imageInference", "taskUUID": id, "model": req.Model,
		"positivePrompt": stylePrompt(req.Prompt, req.Style), "width": round64(w), "height": round64(h),
		"numberResults": req.Count(), "outputType": "base64Data", "outputFormat": "PNG", "includeCost": true}
	if req.Negative != "" {
		task["negativePrompt"] = req.Negative
	}
	if req.Seed != nil {
		task["seed"] = *req.Seed
	}
	if req.Steps > 0 {
		task["steps"] = req.Steps
	}
	if req.Guidance > 0 {
		task["CFGScale"] = req.Guidance
	}
	if req.Image != nil {
		task["seedImage"] = DataURI(*req.Image)
		task["strength"] = 0.7
	}
	if req.Mask != nil {
		task["maskImage"] = DataURI(*req.Mask)
	}
	if len(req.Refs) > 0 {
		var refs []string
		for _, r := range req.Refs {
			refs = append(refs, DataURI(r))
		}
		task["referenceImages"] = refs
	}
	if len(req.LoRAs) > 0 {
		var ls []map[string]any
		for _, l := range req.LoRAs {
			ls = append(ls, map[string]any{"model": l.Key, "weight": l.Weight})
		}
		task["lora"] = ls
	}
	if req.Task == "upscale" && req.Image != nil {
		task = map[string]any{"taskType": "imageUpscale", "taskUUID": id, "inputImage": DataURI(*req.Image),
			"upscaleFactor": max(req.Scale, 2), "outputType": "base64Data", "outputFormat": "PNG", "includeCost": true}
	}
	progress.report(0.1, "sending to Runware")
	var out struct {
		Data []struct {
			TaskType        string  `json:"taskType"`
			TaskUUID        string  `json:"taskUUID"`
			ImageBase64Data string  `json:"imageBase64Data"`
			ImageURL        string  `json:"imageURL"`
			Seed            int64   `json:"seed"`
			Cost            float64 `json:"cost"`
			NSFWContent     bool    `json:"NSFWContent"`
		} `json:"data"`
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := a.Env.call(ctx, http.MethodPost, a.base(cfg), bearer(cfg.APIKey), []any{task}, &out); err != nil {
		return nil, err
	}
	if len(out.Errors) > 0 {
		e := out.Errors[0]
		code := CodeInternal
		switch {
		case strings.Contains(strings.ToLower(e.Code), "apikey") || strings.Contains(strings.ToLower(e.Code), "auth"):
			code = CodeAuth
		case looksLikeSafety(e.Code + " " + e.Message):
			code = CodeSafety
		case strings.HasPrefix(strings.ToLower(e.Code), "invalid"):
			code = CodeBadRequest
		}
		return nil, &Error{Code: code, Message: e.Message}
	}
	res := &ImageResult{}
	for _, d := range out.Data {
		if d.TaskUUID != "" && d.TaskUUID != id {
			continue
		}
		var img Image
		var err error
		if d.ImageBase64Data != "" {
			img, err = decodeB64(d.ImageBase64Data)
		} else if d.ImageURL != "" {
			img, err = a.Env.fetch(ctx, d.ImageURL, nil)
		} else {
			continue
		}
		if err != nil {
			return nil, err
		}
		img.Seed = d.Seed
		res.Images = append(res.Images, img)
		res.CostUSD += d.Cost
	}
	if len(res.Images) == 0 {
		return nil, Errf(CodeInternal, "Runware returned no images")
	}
	return res, nil
}
