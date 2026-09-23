// Package providers holds the provider adapters: one Go file per vendor or
// connection type, each speaking that vendor's documented HTTP API. Adapters
// are stateless; the configured provider (URL, decrypted key, options) is
// passed with every call, and all network access goes through the HTTP
// client in Env (the SSRF-guarded client from internal/core/netguard in
// production, a plain client against httptest servers in tests).
//
// Capabilities are optional interfaces: ImageGenerator, TextGenerator,
// MediaGenerator (video and voice) and ModelLister (capability discovery).
package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Provider kinds.
const (
	KindPlaceholder      = "placeholder"
	KindLocalWorker      = "local_worker"
	KindOpenAI           = "openai"
	KindOpenRouter       = "openrouter"
	KindFal              = "fal"
	KindReplicate        = "replicate"
	KindTogether         = "together"
	KindRunware          = "runware"
	KindHuggingFace      = "huggingface"
	KindGoogle           = "google"
	KindXAI              = "xai"
	KindAnthropic        = "anthropic"
	KindOllama           = "ollama"
	KindOpenAICompatible = "openai_compatible"
	KindComfyUI          = "comfyui"
	KindHTTP             = "http"
)

// Config is one configured provider as the adapter needs it.
type Config struct {
	ID      string
	Kind    string
	Name    string
	BaseURL string
	// APIKey is the decrypted key ("" when the provider needs none).
	APIKey string
	// Options holds kind-specific settings (ComfyUI workflow and mapping, HTTP templates…).
	Options json.RawMessage
}

// Image is image (or other media) bytes with their content type.
type Image struct {
	Data []byte `json:"-"`
	MIME string `json:"mime,omitempty"`
	// URL is set when the provider returned a URL that has not been downloaded.
	URL  string `json:"url,omitempty"`
	Seed int64  `json:"seed,omitempty"`
}

// LoRA is a LoRA weight reference.
type LoRA struct {
	Key    string  `json:"key"`
	URL    string  `json:"url,omitempty"`
	Weight float64 `json:"weight"`
}

// ImageRequest asks for images.
type ImageRequest struct {
	// Task is generate, edit, inpaint, sheet or upscale.
	Task string
	// Model is the provider-side model id.
	Model    string
	Prompt   string
	Negative string
	Width    int
	Height   int
	N        int
	Seed     *int64
	Steps    int
	Guidance float64
	// Style is bw_screentone, flat_grey or colour.
	Style string
	// Image is the source for edit / inpaint / upscale; Mask marks the region to change (white).
	Image *Image
	Mask  *Image
	Refs  []Image
	// PoseImage is an OpenPose skeleton image.
	PoseImage *Image
	LoRAs     []LoRA
	// Scale for upscale (2 or 4).
	Scale int
	// Label is written on placeholder art.
	Label string
}

// Count returns N clamped to 1–4.
func (r ImageRequest) Count() int {
	switch {
	case r.N < 1:
		return 1
	case r.N > 4:
		return 4
	}
	return r.N
}

// Size returns width and height with defaults (1024×1024) and a 4096 cap.
func (r ImageRequest) Size() (int, int) {
	w, h := r.Width, r.Height
	if w <= 0 {
		w = 1024
	}
	if h <= 0 {
		h = 1024
	}
	return min(w, 4096), min(h, 4096)
}

// ImageResult is what an adapter produced.
type ImageResult struct {
	Images []Image
	// CostUSD is the cost the provider reported (0 = not reported).
	CostUSD float64
	// GPUSeconds is GPU time the provider reported.
	GPUSeconds float64
	// Note is a provider message worth keeping (revised prompt, warnings).
	Note string
}

// Message is one chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// TextRequest asks for text.
type TextRequest struct {
	Model       string
	System      string
	Messages    []Message
	MaxTokens   int
	Temperature *float64
	// JSON asks for a JSON object answer where the API supports it.
	JSON bool
	// Task is text or translate (the placeholder uses it); Lang is the target language for translate.
	Task string
	Lang string
}

// MaxTokensOr returns MaxTokens or def.
func (r TextRequest) MaxTokensOr(def int) int {
	if r.MaxTokens > 0 {
		return r.MaxTokens
	}
	return def
}

// TextResult is generated text.
type TextResult struct {
	Text      string
	TokensIn  int
	TokensOut int
	CostUSD   float64
}

// MediaRequest asks for video or voice.
type MediaRequest struct {
	// Task is video or voice.
	Task   string
	Model  string
	Prompt string
	// Text is spoken for voice; Voice picks the voice.
	Text     string
	Voice    string
	Image    *Image
	Seconds  float64
	Aspect   string
	Width    int
	Height   int
	Language string
}

// MediaResult is a clip.
type MediaResult struct {
	Media    Image
	Seconds  float64
	CostUSD  float64
	Provider string
}

// RemoteModel is a model a provider lists (capability discovery).
type RemoteModel struct {
	ID            string   `json:"id"`
	Name          string   `json:"name,omitempty"`
	Kind          string   `json:"kind"`
	InputImages   bool     `json:"input_images,omitempty"`
	OutputImages  bool     `json:"output_images,omitempty"`
	Context       int      `json:"context,omitempty"`
	PriceUnit     string   `json:"price_unit,omitempty"`
	PriceUSD      float64  `json:"price_usd,omitempty"`
	PriceUSDOut   float64  `json:"price_usd_out,omitempty"`
	AspectRatios  []string `json:"aspect_ratios,omitempty"`
	Description   string   `json:"description,omitempty"`
	SupportsTools bool     `json:"supports_tools,omitempty"`
	// License is the licence id the provider reports (Hugging Face model cards).
	License string `json:"license,omitempty"`
}

// Progress reports adapter progress (0–1) with a short message.
type Progress func(p float64, msg string)

func (p Progress) report(v float64, msg string) {
	if p != nil {
		p(v, msg)
	}
}

// Adapter is implemented by every provider adapter.
type Adapter interface {
	Kind() string
}

// ImageGenerator produces images.
type ImageGenerator interface {
	Image(ctx context.Context, cfg Config, req ImageRequest, progress Progress) (*ImageResult, error)
}

// TextGenerator produces text.
type TextGenerator interface {
	Text(ctx context.Context, cfg Config, req TextRequest) (*TextResult, error)
}

// MediaGenerator produces video or audio.
type MediaGenerator interface {
	Media(ctx context.Context, cfg Config, req MediaRequest, progress Progress) (*MediaResult, error)
}

// ModelLister lists the provider's models (used to auto-fill capabilities).
type ModelLister interface {
	Models(ctx context.Context, cfg Config) ([]RemoteModel, error)
}

// Env is what adapters need from the outside world.
type Env struct {
	// HTTP is the client for every request (the SSRF-guarded client in production).
	HTTP *http.Client
	// PollInterval is the wait between status polls (default 1.5 s).
	PollInterval time.Duration
	// MaxWait caps polling (default 10 minutes).
	MaxWait time.Duration
	// MaxBytes caps one downloaded file (default 64 MiB).
	MaxBytes int64
}

func (e Env) poll() time.Duration {
	if e.PollInterval > 0 {
		return e.PollInterval
	}
	return 1500 * time.Millisecond
}

func (e Env) maxWait() time.Duration {
	if e.MaxWait > 0 {
		return e.MaxWait
	}
	return 10 * time.Minute
}

func (e Env) maxBytes() int64 {
	if e.MaxBytes > 0 {
		return e.MaxBytes
	}
	return 64 << 20
}

func (e Env) client() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

// Error codes.
const (
	CodeAuth        = "auth"
	CodeRateLimit   = "rate_limit"
	CodeSafety      = "safety"
	CodeTimeout     = "timeout"
	CodeBadRequest  = "bad_request"
	CodeUnavailable = "unavailable"
	CodeUnsupported = "unsupported"
	CodeInternal    = "internal"
)

// Error is an adapter failure with a stable code.
type Error struct {
	Code    string
	Status  int
	Message string
}

func (e *Error) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("%s (HTTP %d): %s", e.Code, e.Status, e.Message)
	}
	return e.Code + ": " + e.Message
}

// Retryable reports whether trying again (or elsewhere) may help.
func (e *Error) Retryable() bool {
	switch e.Code {
	case CodeRateLimit, CodeTimeout, CodeUnavailable, CodeInternal:
		return true
	}
	return false
}

// Errf builds an Error.
func Errf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// CodeOf returns the code of err (timeouts and cancellations included).
func CodeOf(err error) string {
	var e *Error
	switch {
	case err == nil:
		return ""
	case errors.As(err, &e):
		return e.Code
	case errors.Is(err, context.DeadlineExceeded):
		return CodeTimeout
	}
	return CodeInternal
}

// ErrUnsupported is returned for capabilities an adapter lacks.
func unsupported(kind, what string) error {
	return Errf(CodeUnsupported, "%s does not support %s", kind, what)
}
