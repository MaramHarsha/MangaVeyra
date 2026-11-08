package ai

import (
	"math"

	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// ADR 0009: frozen hosted image routes added beside ADR 0007's Seedream and
// fal routes. Each row pairs one provider kind with one model and one pricing
// rule. Manifests cannot add rows, endpoints or tariffs; quoteImage consults
// this table before ADR 0007's routes.
type imageRoute struct {
	Kind, Model string
	// Preferred routes rank ahead of every other candidate when Auto routes
	// a paid-capable mode (balanced, best, fastest). Free-only and local-only
	// never see them because they are paid remote routes.
	Preferred bool
	// PinOnly routes are vetted backups a creator selects with a model pin;
	// Auto never chooses them, so they never widen an unpinned estimate.
	PinOnly bool
	// MaxRefs is the number of reference images one call sends.
	MaxRefs int
	quote   func(ImageNeeds) (jobs.Cost, bool)
	// validate checks the route's per-call ceilings before anything is
	// reserved; nil means ADR 0009's GPT Image prompt-bytes check.
	validate func(ImageGenerateRequest) error
}

// Model identifiers of the ADR 0009 routes.
const (
	gptImageSunburst           = "gpt-image-2.5-sunburst"
	gptImageFlare              = "gpt-image-2.5-flare"
	openRouterGPTImageSunburst = "openai/gpt-image-2.5-sunburst"
	openRouterNanoBanana21     = "google/gemini-nano-banana-2.1"
	openRouterNanoBananaPro    = "google/gemini-3-pro-image"
)

// Published tariffs, in nano-USD per token (USD per 1M tokens × 1,000), so
// every bound is exact integer arithmetic. Checked 2026-11-07.
const (
	gptImageTextInNano   = 5_000  // $5.00 / 1M text input tokens
	gptImageImageInNano  = 8_000  // $8.00 / 1M image input tokens
	gptImageImageOutNano = 30_000 // $30.00 / 1M image output tokens

	nanoBanana21InNano     = 1_500  // $1.50 / 1M text or image input tokens
	nanoBanana21TextOut    = 7_500  // $7.50 / 1M text and reasoning output tokens
	nanoBanana21ImageOut   = 30_000 // $30.00 / 1M image output tokens
	nanoBananaProInNano    = 2_000  // $2.00 / 1M text or image input tokens
	nanoBananaProTextOut   = 12_000 // $12.00 / 1M text and reasoning output tokens
	nanoBananaProImageOut  = 120_000
	nanoBananaImageTokens  = 1_120 // output tokens of one 1K image (both models)
	nanoBananaProInputImg  = 560   // published input tokens per image (Pro)
	nanoBanana21InputImg   = 1_120 // allowance per input image (not published for 2.1)
	nanoBananaReasoningCap = 4_096 // application allowance for text/reasoning output
	nanoBananaMaxRefs      = 6
)

// Request ceilings shared by the token-priced routes.
const (
	// imagePromptBytes bounds the composed prompt (description, avoid list and
	// route instructions). UTF-8 bytes are an upper bound on text tokens.
	imagePromptBytes = 6_144
	// gptImageInputTokens is the allowance per input image (reference, init or
	// mask). OpenAI has not published the GPT Image 2.5 input rule; this is the
	// largest published GPT Image high-fidelity figure for an input of at most
	// 3:1 scaled to a 512 px short side (65 + 3 × 129 + 6,240 = 6,692), rounded
	// up. Inputs are downscaled before sending (gptImageRefEdge).
	gptImageInputTokens = 6_800
	gptImageMaxRefs     = 2
	gptImageQuality     = "high"
)

var imageRoutes = []imageRoute{
	{Kind: providerKindOpenAI, Model: gptImageSunburst, Preferred: true, MaxRefs: gptImageMaxRefs, quote: quoteGPTImageDirect},
	{Kind: providerKindOpenAI, Model: gptImageFlare, Preferred: true, MaxRefs: gptImageMaxRefs, quote: quoteGPTImageDirect},
	{Kind: providerKindOpenRouter, Model: openRouterGPTImageSunburst, PinOnly: true, MaxRefs: gptImageMaxRefs, quote: quoteGPTImageOpenRouter},
	{Kind: providerKindOpenRouter, Model: openRouterNanoBanana21, PinOnly: true, MaxRefs: nanoBananaMaxRefs, quote: quoteNanoBanana21},
	{Kind: providerKindOpenRouter, Model: openRouterNanoBananaPro, PinOnly: true, MaxRefs: nanoBananaMaxRefs, quote: quoteNanoBananaPro},
	// ADR 0013 §9 (fake-transport tested, not live-verified). Neither
	// preferred nor pin-only, except the time-billed SDXL route whose quote is
	// an application bound.
	{Kind: providerKindGoogle, Model: googleImageModel, MaxRefs: nanoBananaMaxRefs, quote: quoteGoogleImage, validate: validateGoogleImageRequest},
	{Kind: providerKindXAI, Model: xaiImageModel, quote: quoteXAIImage, validate: func(r ImageGenerateRequest) error { return validatePlainPrompt(r, imagePromptBytes) }},
	{Kind: providerKindTogether, Model: togetherImageModel, quote: quoteTogetherImage, validate: func(r ImageGenerateRequest) error { return validatePlainPrompt(r, imagePromptBytes) }},
	{Kind: providerKindReplicate, Model: replicateFluxModel, quote: quoteReplicateFlux, validate: func(r ImageGenerateRequest) error { return validatePlainPrompt(r, imagePromptBytes) }},
	{Kind: providerKindReplicate, Model: replicateSDXLModel, PinOnly: true, quote: quoteReplicateSDXL, validate: func(r ImageGenerateRequest) error { return validatePlainPrompt(r, imagePromptBytes) }},
	{Kind: providerKindRunware, Model: runwareModel, quote: quoteRunware, validate: validateRunwareRequest},
}

func imageRouteFor(kind, model string) (imageRoute, bool) {
	for _, r := range imageRoutes {
		if r.Kind == kind && r.Model == model {
			return r, true
		}
	}
	return imageRoute{}, false
}

// nanoToCost rounds a nano-USD total up to whole microdollars.
func nanoToCost(nano int64) jobs.Cost {
	return jobs.Cost{USD: float64((nano+999)/1000) / costMicro}
}

func routeInputImages(n ImageNeeds, maxRefs int) int64 {
	inputs := int64(min(n.References, maxRefs))
	if n.Edit {
		inputs++
	}
	if n.Mask {
		inputs++
	}
	return inputs
}

// gptImageOutputTokens is OpenAI's published GPT Image 2.5 output-token rule
// (the image generation guide's cost calculator) for one image.
func gptImageOutputTokens(w, h int, quality string) int64 {
	base := map[string]float64{"low": 16, "medium": 24, "high": 48, "xhigh": 64, "max": 96}[quality]
	if base == 0 || w <= 0 || h <= 0 {
		return 0
	}
	long, short := float64(max(w, h)), float64(min(w, h))
	s := base / (long / short)
	u := math.Floor(s)
	d := math.Round(s)
	if s-u == .5 {
		d = u + math.Mod(u, 2)
	}
	return int64(math.Ceil(base * d * (2e6 + float64(w)*float64(h)) / 4e6))
}

func quoteGPTImage(n ImageNeeds, outputTokens int64) (jobs.Cost, bool) {
	if n.Count < 1 || n.Count > 4 || outputTokens <= 0 {
		return jobs.Cost{}, false
	}
	nano := outputTokens*int64(n.Count)*gptImageImageOutNano + imagePromptBytes*gptImageTextInNano + routeInputImages(n, gptImageMaxRefs)*gptImageInputTokens*gptImageImageInNano
	return nanoToCost(nano), true
}

// quoteGPTImageDirect bounds one direct OpenAI call at the size the adapter
// will request (gptImageSizeFor) and the frozen quality.
func quoteGPTImageDirect(n ImageNeeds) (jobs.Cost, bool) {
	if n.Width < 64 || n.Height < 64 {
		return jobs.Cost{}, false
	}
	size := gptImageSizeFor(n.Width, n.Height)
	return quoteGPTImage(n, gptImageOutputTokens(size.W, size.H, gptImageQuality))
}

// quoteGPTImageOpenRouter cannot know the pixel size OpenRouter derives from
// an aspect ratio, so it bounds the largest documented pixel budget
// (8,294,400 px as a 2,880² square, the most expensive shape).
func quoteGPTImageOpenRouter(n ImageNeeds) (jobs.Cost, bool) {
	return quoteGPTImage(n, gptImageOutputTokens(2880, 2880, gptImageQuality))
}

func quoteNanoBanana(n ImageNeeds, inNano, textOutNano, imageOutNano, inputImageTokens int64) (jobs.Cost, bool) {
	if n.Count != 1 {
		return jobs.Cost{}, false
	}
	nano := nanoBananaImageTokens*imageOutNano + nanoBananaReasoningCap*textOutNano + imagePromptBytes*inNano + routeInputImages(n, nanoBananaMaxRefs)*inputImageTokens*inNano
	return nanoToCost(nano), true
}

func quoteNanoBanana21(n ImageNeeds) (jobs.Cost, bool) {
	return quoteNanoBanana(n, nanoBanana21InNano, nanoBanana21TextOut, nanoBanana21ImageOut, nanoBanana21InputImg)
}

func quoteNanoBananaPro(n ImageNeeds) (jobs.Cost, bool) {
	return quoteNanoBanana(n, nanoBananaProInNano, nanoBananaProTextOut, nanoBananaProImageOut, nanoBananaProInputImg)
}

// gptImageSize is one frozen output size: multiples of 16, an aspect ratio
// within 1:3–3:1 and 655,360–8,294,400 pixels, as the Image API requires.
type gptImageSize struct{ W, H int }

var gptImageSizes = []gptImageSize{
	{1024, 1024}, {1120, 896}, {896, 1120}, {1152, 864}, {864, 1152}, {1536, 1024}, {1024, 1536},
	{1536, 864}, {864, 1536}, {1536, 768}, {768, 1536}, {1536, 640}, {640, 1536}, {1536, 512}, {512, 1536},
}

// gptImageSizeFor maps a layer's aspect ratio to the nearest frozen size by
// log-ratio distance; the result is later fitted back into the layer.
func gptImageSizeFor(w, h int) gptImageSize {
	if w <= 0 || h <= 0 {
		return gptImageSizes[0]
	}
	target := math.Log(float64(w) / float64(h))
	best, bestD := gptImageSizes[0], math.Inf(1)
	for _, s := range gptImageSizes {
		d := math.Abs(math.Log(float64(s.W)/float64(s.H)) - target)
		if d < bestD-1e-12 {
			best, bestD = s, d
		}
	}
	return best
}
