package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Generic HTTP image connections (G12 "HTTP", ADR 0013 §9). The admin
// describes one image API on this machine or network: a JSON request
// template with placeholders, where the image is in the response (base64 or
// a URL on the same server), and optionally a status URL to poll. Like custom
// text (ADR 0007) it needs the private-network setting, dials only loopback
// and private addresses, follows no redirects, is blocked by local_only and
// has no spend reservation. Only images and status URLs on the connection's
// own origin are fetched.
// Status: fake-transport tested, not live-verified.
const (
	httpModeSync = "sync"
	httpModePoll = "poll"
	httpBase64   = "base64"
	httpURL      = "url"
)

// httpPlaceholders are the values a template may use. A string that is
// exactly "{{width}}" becomes the JSON number; inside a longer string a
// placeholder is replaced by its text.
var httpPlaceholders = []string{"prompt", "negative", "width", "height", "seed", "count"}

// httpPlaceholderList is the placeholders as a template writes them.
func httpPlaceholderList() string {
	out := make([]string, len(httpPlaceholders))
	for i, p := range httpPlaceholders {
		out[i] = "{{" + p + "}}"
	}
	return strings.Join(out, ", ")
}

var httpPlaceholderRE = regexp.MustCompile(`\{\{\s*([a-z_]+)\s*\}\}`)

type httpConfig struct {
	Request   json.RawMessage `json:"request"`
	Mode      string          `json:"mode"`
	ImagePath string          `json:"image_path"`
	ImageKind string          `json:"image_kind"`
	// Poll mode: StatusURLPath finds the status URL in the submit response;
	// StatusPath finds the state in each status response, which is Done when
	// the image is ready and Failed when the job failed. ImagePath is then
	// read from the final status response.
	StatusURLPath string          `json:"status_url_path,omitempty"`
	StatusPath    string          `json:"status_path,omitempty"`
	Done          string          `json:"done,omitempty"`
	Failed        string          `json:"failed,omitempty"`
	CheckURL      string          `json:"check_url,omitempty"`
	Image         customImageSpec `json:"image"`
	Modes         []string        `json:"modes,omitempty"`
}

func (c httpConfig) routeModes() []string { return customModes(c.Modes, defaultLocalModes) }

func decodeHTTPConfig(raw string) (httpConfig, error) {
	var c httpConfig
	return c, decodeStrict(raw, &c)
}

// templatePlaceholders lists the placeholders a template uses, or false when
// it is not a JSON object or uses an unknown one.
func templatePlaceholders(raw []byte) (map[string]bool, bool) {
	var doc map[string]any
	if len(raw) == 0 || len(raw) > 64<<10 || json.Unmarshal(raw, &doc) != nil || doc == nil {
		return nil, false
	}
	used := map[string]bool{}
	for _, m := range httpPlaceholderRE.FindAllSubmatch(raw, -1) {
		name := string(m[1])
		if !kindIn(name, httpPlaceholders) {
			return nil, false
		}
		used[name] = true
	}
	return used, true
}

func validateHTTPConfig(base string, c httpConfig) error {
	if !validServerURL(base, true) || endpointNeverAllowed(base) || !c.Image.valid() || !validLocalModes(c.Modes) {
		return errProviderInvalid
	}
	used, ok := templatePlaceholders(c.Request)
	if !ok || !used["prompt"] || !validJSONPath(c.ImagePath) || (c.ImageKind != httpBase64 && c.ImageKind != httpURL) {
		return errProviderInvalid
	}
	// One image per call unless the template passes {{count}}; even then the
	// image path names one image, so the connection makes one per call.
	if c.Image.MaxCount != 1 {
		return errProviderInvalid
	}
	switch c.Mode {
	case httpModeSync:
		if c.StatusURLPath != "" || c.StatusPath != "" || c.Done != "" || c.Failed != "" {
			return errProviderInvalid
		}
	case httpModePoll:
		if !validJSONPath(c.StatusURLPath) || !validJSONPath(c.StatusPath) || c.Done == "" || len(c.Done) > 64 || len(c.Failed) > 64 {
			return errProviderInvalid
		}
	default:
		return errProviderInvalid
	}
	if c.CheckURL != "" {
		if _, same := sameOrigin(base, c.CheckURL); !same {
			return errProviderInvalid
		}
	}
	return nil
}

// renderTemplate substitutes the run's values into the template. Values
// are inserted into the decoded document, never spliced into JSON text, so a
// prompt cannot change the request's structure.
func renderTemplate(raw []byte, values map[string]any) (any, error) {
	doc, err := decodeJSONDoc(raw)
	if err != nil {
		return nil, badInputAdapterError()
	}
	var walk func(any) any
	walk = func(v any) any {
		switch t := v.(type) {
		case map[string]any:
			for k, child := range t {
				t[k] = walk(child)
			}
			return t
		case []any:
			for i, child := range t {
				t[i] = walk(child)
			}
			return t
		case string:
			if m := httpPlaceholderRE.FindStringSubmatch(t); m != nil && m[0] == strings.TrimSpace(t) {
				return values[m[1]]
			}
			return httpPlaceholderRE.ReplaceAllStringFunc(t, func(s string) string {
				name := httpPlaceholderRE.FindStringSubmatch(s)[1]
				switch v := values[name].(type) {
				case string:
					return v
				default:
					b, _ := json.Marshal(v)
					return string(b)
				}
			})
		}
		return v
	}
	return walk(doc), nil
}

func httpManifest(id, name string, c httpConfig) ModelManifest {
	return ModelManifest{ID: customModelPrefix + id, Name: name, Maker: "HTTP", Kind: "image", Licence: "endpoint",
		Commercial: c.Image.Commercial, Providers: []string{providerKindHTTP}, BW: true,
		MaxWidth: c.Image.MaxWidth, MaxHeight: c.Image.MaxHeight, MaxCount: 1, Quality: 60, Speed: 50}
}

type httpClient struct {
	doer      httpDoer
	base, key string
	poll      time.Duration
}

func (c httpClient) send(ctx context.Context, method, target string, body []byte) ([]byte, int, error) {
	r, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, 0, badInputAdapterError()
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Accept", "application/json, image/*")
	if c.key != "" {
		r.Header.Set("Authorization", "Bearer "+c.key)
	}
	if c.doer == nil {
		return nil, 0, upstreamAdapterError(false)
	}
	resp, err := c.doer.Do(r)
	if err != nil {
		return nil, 0, localTransportError(err)
	}
	if resp == nil || resp.Body == nil {
		return nil, 0, upstreamAdapterError(true)
	}
	defer resp.Body.Close()
	data, large, err := readBounded(resp.Body, 64<<20)
	if err != nil || large {
		return nil, resp.StatusCode, badOutputAdapterError(false)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return data, resp.StatusCode, classifyHTTPStatus(resp.StatusCode)
	}
	return data, resp.StatusCode, nil
}

// testHTTP checks the server answers its check URL (or its origin) without
// sending a generation request.
func testHTTP(ctx context.Context, c httpClient, cfg httpConfig) error {
	target := cfg.CheckURL
	if target == "" {
		target = "/"
	}
	abs, ok := sameOrigin(c.base, target)
	if !ok {
		return badInputAdapterError()
	}
	_, _, err := c.send(ctx, http.MethodGet, abs, nil)
	return err
}

func httpImage(ctx context.Context, c httpClient, cfg httpConfig, req ImageGenerateRequest) (imageCall, error) {
	var out imageCall
	if req.Init != nil || req.Mask != nil || len(req.References) > 0 || req.Count != 1 {
		return out, badInputAdapterError()
	}
	doc, err := renderTemplate(cfg.Request, map[string]any{"prompt": plainImagePromptNoAvoid(req), "negative": req.Negative,
		"width": comfySide(req.Width, cfg.Image.MaxWidth), "height": comfySide(req.Height, cfg.Image.MaxHeight),
		"seed": req.Seed & math.MaxInt32, "count": req.Count})
	if err != nil {
		return out, err
	}
	body, err := json.Marshal(doc)
	if err != nil {
		return out, badInputAdapterError()
	}
	callCtx, cancel := context.WithTimeout(ctx, imageCallTimeout)
	defer cancel()
	out.Sent = true
	data, _, err := c.send(callCtx, http.MethodPost, c.base, body)
	if err != nil {
		return out, err
	}
	final, err := decodeJSONDoc(data)
	if err != nil {
		return out, badOutputAdapterError(false)
	}
	if cfg.Mode == httpModePoll {
		raw, err := lookupJSONPath(final, cfg.StatusURLPath)
		status, _ := raw.(string)
		statusURL, same := sameOrigin(c.base, status)
		if err != nil || status == "" || !same {
			return out, badOutputAdapterError(false)
		}
		for {
			if err := sleepCtx(callCtx, c.poll); err != nil {
				return out, classifyTransportError(err)
			}
			data, _, err := c.send(callCtx, http.MethodGet, statusURL, nil)
			if err != nil {
				return out, err
			}
			if final, err = decodeJSONDoc(data); err != nil {
				return out, badOutputAdapterError(false)
			}
			state, err := lookupJSONPath(final, cfg.StatusPath)
			if err != nil {
				continue
			}
			text := strings.TrimSpace(strings.Trim(jsonText(state), `"`))
			if cfg.Failed != "" && text == cfg.Failed {
				return out, &adapterError{code: CodeUpstream, safe: "The image endpoint reported a failed job.", cause: errInvalidScriptInput}
			}
			if text == cfg.Done {
				break
			}
		}
	}
	raw, err := lookupJSONPath(final, cfg.ImagePath)
	value, _ := raw.(string)
	if err != nil || value == "" {
		return out, badOutputAdapterError(false)
	}
	var image []byte
	if cfg.ImageKind == httpBase64 {
		if i := strings.Index(value, ";base64,"); strings.HasPrefix(value, "data:image/") && i > 0 {
			value = value[i+len(";base64,"):]
		}
		if len(value) > (maxImageBytes*4/3)+4 {
			return out, badOutputAdapterError(false)
		}
		if image, err = base64.StdEncoding.DecodeString(value); err != nil {
			return out, badOutputAdapterError(false)
		}
	} else {
		target, same := sameOrigin(c.base, value)
		if !same {
			return out, badOutputAdapterError(false)
		}
		if image, _, err = c.send(callCtx, http.MethodGet, target, nil); err != nil {
			return out, badOutputAdapterError(false)
		}
	}
	g, err := fitGeneratedImage(image, req.Width, req.Height, req.Seed, true)
	if err != nil {
		return out, err
	}
	out.Images = []GeneratedImage{g}
	return out, nil
}

func jsonText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}
