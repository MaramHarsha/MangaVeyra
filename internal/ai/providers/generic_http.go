package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HTTPAdapter is the generic HTTP adapter: any REST image API without code.
// The connection stores a URL, an auth header, a JSON request body template
// with {{placeholders}}, where the image is in the response (a dotted JSON
// path to a URL or base64 value) and whether the API answers at once (sync)
// or returns a job to poll.
type HTTPAdapter struct{ Env Env }

// Kind implements Adapter.
func (HTTPAdapter) Kind() string { return KindHTTP }

// HTTPOptions configure a generic HTTP connection.
type HTTPOptions struct {
	Method string `json:"method,omitempty"`
	// AuthHeader and AuthPrefix build the auth header ("Authorization", "Bearer ").
	AuthHeader string `json:"auth_header,omitempty"`
	AuthPrefix string `json:"auth_prefix,omitempty"`
	// BodyTemplate is JSON with placeholders: {{prompt}} {{negative}} {{model}} {{width}} {{height}}
	// {{seed}} {{n}} {{aspect}} {{image}} (data URI). String placeholders are JSON-escaped without quotes.
	BodyTemplate string `json:"body_template"`
	// ResultPath is the dotted path to the image ("data.0.b64_json", "output.images.0.url").
	ResultPath string `json:"result_path"`
	// ResultType is "url", "base64" or "auto".
	ResultType string `json:"result_type,omitempty"`
	// Mode is "sync" or "poll".
	Mode string `json:"mode,omitempty"`
	// Poll settings: the job id in the first answer, the status URL ({{id}}), status path and values.
	IDPath        string   `json:"id_path,omitempty"`
	PollURL       string   `json:"poll_url,omitempty"`
	StatusPath    string   `json:"status_path,omitempty"`
	DoneValues    []string `json:"done_values,omitempty"`
	FailedValues  []string `json:"failed_values,omitempty"`
	CostPath      string   `json:"cost_path,omitempty"`
	ErrorPath     string   `json:"error_path,omitempty"`
	ExtraHeaders  []string `json:"extra_headers,omitempty"`
	TimeoutSecond int      `json:"timeout_s,omitempty"`
}

// DefaultHTTPTemplate is the starting body template shown on the form.
const DefaultHTTPTemplate = `{"model": "{{model}}", "prompt": "{{prompt}}", "negative_prompt": "{{negative}}", "width": {{width}}, "height": {{height}}, "seed": {{seed}}, "num_images": {{n}}}`

// ParseHTTPOptions decodes and checks options.
func ParseHTTPOptions(raw json.RawMessage) (*HTTPOptions, error) {
	var o HTTPOptions
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &o); err != nil {
			return nil, Errf(CodeBadRequest, "invalid HTTP options: %v", err)
		}
	}
	if strings.TrimSpace(o.BodyTemplate) == "" {
		o.BodyTemplate = DefaultHTTPTemplate
	}
	if o.ResultPath == "" {
		return nil, Errf(CodeBadRequest, "say where the image is in the response (result path)")
	}
	if o.Mode == "poll" && (o.PollURL == "" || o.IDPath == "" || o.StatusPath == "") {
		return nil, Errf(CodeBadRequest, "poll mode needs a status URL, an id path and a status path")
	}
	if _, err := renderTemplate(o.BodyTemplate, map[string]any{"prompt": "x", "negative": "", "model": "m", "width": 1, "height": 1,
		"seed": 1, "n": 1, "aspect": "1:1", "image": ""}); err != nil {
		return nil, err
	}
	return &o, nil
}

// renderTemplate fills {{name}} placeholders and checks the result is JSON.
func renderTemplate(tpl string, vals map[string]any) ([]byte, error) {
	out := tpl
	for k, v := range vals {
		var s string
		switch t := v.(type) {
		case string:
			b, _ := json.Marshal(t)
			s = string(b[1 : len(b)-1])
		case int:
			s = strconv.Itoa(t)
		case int64:
			s = strconv.FormatInt(t, 10)
		case float64:
			s = strconv.FormatFloat(t, 'f', -1, 64)
		}
		out = strings.ReplaceAll(out, "{{"+k+"}}", s)
	}
	if !json.Valid([]byte(out)) {
		return nil, Errf(CodeBadRequest, "the body template is not valid JSON after filling placeholders")
	}
	return []byte(out), nil
}

// JSONPath reads a dotted path ("data.0.url") from decoded JSON.
func JSONPath(v any, path string) (any, bool) {
	path = strings.TrimPrefix(strings.TrimPrefix(path, "$"), ".")
	if path == "" {
		return v, true
	}
	cur := v
	for _, part := range strings.Split(path, ".") {
		part = strings.TrimSuffix(part, "]")
		if name, idx, ok := strings.Cut(part, "["); ok {
			if name != "" {
				m, ok := cur.(map[string]any)
				if !ok {
					return nil, false
				}
				cur = m[name]
			}
			part = idx
		}
		switch t := cur.(type) {
		case map[string]any:
			x, ok := t[part]
			if !ok {
				return nil, false
			}
			cur = x
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(t) {
				return nil, false
			}
			cur = t[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

func (a HTTPAdapter) headers(cfg Config, o *HTTPOptions) map[string]string {
	h := map[string]string{}
	if cfg.APIKey != "" {
		name := o.AuthHeader
		if name == "" {
			name = "Authorization"
		}
		prefix := o.AuthPrefix
		if o.AuthHeader == "" && prefix == "" {
			prefix = "Bearer "
		}
		h[name] = prefix + cfg.APIKey
	}
	for _, kv := range o.ExtraHeaders {
		if k, v, ok := strings.Cut(kv, ":"); ok {
			h[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return h
}

// Image sends the templated request and extracts the image(s).
func (a HTTPAdapter) Image(ctx context.Context, cfg Config, req ImageRequest, progress Progress) (*ImageResult, error) {
	if cfg.BaseURL == "" {
		return nil, Errf(CodeBadRequest, "URL is required")
	}
	o, err := ParseHTTPOptions(cfg.Options)
	if err != nil {
		return nil, err
	}
	w, h := req.Size()
	seed := randomSeed() % 2147483647
	if req.Seed != nil {
		seed = *req.Seed
	}
	img := ""
	if req.Image != nil {
		img = DataURI(*req.Image)
	} else if len(req.Refs) > 0 {
		img = DataURI(req.Refs[0])
	}
	body, err := renderTemplate(o.BodyTemplate, map[string]any{"prompt": stylePrompt(req.Prompt, req.Style), "negative": req.Negative,
		"model": req.Model, "width": w, "height": h, "seed": seed, "n": req.Count(), "aspect": aspect(w, h), "image": img})
	if err != nil {
		return nil, err
	}
	method := o.Method
	if method == "" {
		method = http.MethodPost
	}
	progress.report(0.1, "sending")
	var first any
	if err := a.Env.call(ctx, method, cfg.BaseURL, a.headers(cfg, o), json.RawMessage(body), &first); err != nil {
		return nil, err
	}
	result := first
	if o.Mode == "poll" {
		idv, ok := JSONPath(first, o.IDPath)
		if !ok {
			return nil, Errf(CodeInternal, "no job id at %q in the response", o.IDPath)
		}
		id := strings.Trim(jsonScalar(idv), `"`)
		pollURL := strings.ReplaceAll(o.PollURL, "{{id}}", id)
		done := o.DoneValues
		if len(done) == 0 {
			done = []string{"succeeded", "completed", "success", "done", "COMPLETED", "SUCCEEDED"}
		}
		failed := o.FailedValues
		if len(failed) == 0 {
			failed = []string{"failed", "error", "canceled", "cancelled", "FAILED", "ERROR"}
		}
		wait := a.Env.maxWait()
		if o.TimeoutSecond > 0 {
			wait = time.Duration(o.TimeoutSecond) * time.Second
		}
		deadline := time.Now().Add(wait)
		for {
			var st any
			if err := a.Env.call(ctx, http.MethodGet, pollURL, a.headers(cfg, o), nil, &st); err != nil {
				return nil, err
			}
			sv, _ := JSONPath(st, o.StatusPath)
			status := strings.Trim(jsonScalar(sv), `"`)
			if contains(done, status) {
				result = st
				break
			}
			if contains(failed, status) {
				msg := "the job failed"
				if o.ErrorPath != "" {
					if ev, ok := JSONPath(st, o.ErrorPath); ok {
						msg = strings.Trim(jsonScalar(ev), `"`)
					}
				}
				if looksLikeSafety(msg) {
					return nil, Errf(CodeSafety, "%s", msg)
				}
				return nil, Errf(CodeInternal, "%s", msg)
			}
			progress.report(0.5, "waiting for "+status)
			if time.Now().After(deadline) {
				return nil, Errf(CodeTimeout, "the job did not finish in %s", wait)
			}
			if err := sleep(ctx, a.Env.poll()); err != nil {
				return nil, err
			}
		}
	}
	v, ok := JSONPath(result, o.ResultPath)
	if !ok {
		return nil, Errf(CodeInternal, "nothing at %q in the response", o.ResultPath)
	}
	var values []string
	switch t := v.(type) {
	case string:
		values = []string{t}
	case []any:
		for _, x := range t {
			switch y := x.(type) {
			case string:
				values = append(values, y)
			case map[string]any:
				for _, k := range []string{"url", "b64_json", "base64", "image"} {
					if s, ok := y[k].(string); ok {
						values = append(values, s)
						break
					}
				}
			}
		}
	}
	res := &ImageResult{}
	if o.CostPath != "" {
		if cv, ok := JSONPath(result, o.CostPath); ok {
			res.CostUSD, _ = strconv.ParseFloat(strings.Trim(jsonScalar(cv), `"`), 64)
		}
	}
	for _, s := range values {
		var im Image
		var err error
		isURL := strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
		switch {
		case o.ResultType == "url" || (o.ResultType != "base64" && isURL):
			im, err = a.Env.fetch(ctx, s, nil)
		default:
			im, err = decodeB64(s)
		}
		if err != nil {
			return nil, err
		}
		res.Images = append(res.Images, im)
	}
	if len(res.Images) == 0 {
		return nil, Errf(CodeInternal, "no image at %q in the response", o.ResultPath)
	}
	return res, nil
}

func jsonScalar(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
