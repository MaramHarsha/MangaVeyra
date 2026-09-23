package setup

import (
	"context"
	"fmt"

	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// ModelStep is the hook the AI module fills to drive step 3 ("Connect
// models"): live provider status and a recommended local model set sized to
// the detected GPUs. Provide it with core.Provide[setup.ModelStep](d, impl);
// until then the wizard shows the built-in provider cards (not connected) and
// a VRAM-based recommendation.
type ModelStep interface {
	// Providers returns the provider cards with their connection status.
	Providers(ctx context.Context) ([]ProviderCard, error)
	// Recommend returns the local model set that fits the GPUs (nil when none fits).
	Recommend(ctx context.Context, gpus []jobs.GPU) (*Recommendation, error)
}

// Provider card statuses.
const (
	ProviderNotConnected = ""
	ProviderConnected    = "connected"
	ProviderTesting      = "testing"
	ProviderError        = "error"
)

// ProviderCard is one connectable provider on step 3.
type ProviderCard struct {
	// Kind is the provider kind passed to /models/providers/new?kind=…
	Kind string
	// NameKey / DescKey are i18n keys (Name / Desc override them when set).
	NameKey, DescKey string
	Name, Desc       string
	Icon             string
	Status           string
	// Href is where "Connect" goes (defaults to the add-provider flow for Kind).
	Href string
}

// ConnectHref returns the card's connect link.
func (p ProviderCard) ConnectHref() string {
	if p.Href != "" {
		return p.Href
	}
	return "/models/providers/new?kind=" + p.Kind
}

// Recommendation is the recommended local model set for the detected hardware.
type Recommendation struct {
	// Models are display names ("Qwen-Image Edit").
	Models []string
	// DownloadGB is the total download size.
	DownloadGB float64
	// InstallHref starts the install (a job in the models module).
	InstallHref string
}

// DefaultProviders are the cards shown on day one (design G8): local worker,
// OpenRouter, fal, direct vendor keys, Ollama and custom connections.
func DefaultProviders() []ProviderCard {
	return []ProviderCard{
		{Kind: "local_worker", NameKey: "setup.provider.local.name", DescKey: "setup.provider.local.desc", Icon: "cpu", Href: "/models/workers/new"},
		{Kind: "openrouter", NameKey: "setup.provider.openrouter.name", DescKey: "setup.provider.openrouter.desc", Icon: "route"},
		{Kind: "fal", NameKey: "setup.provider.fal.name", DescKey: "setup.provider.fal.desc", Icon: "bolt"},
		{Kind: "vendor", NameKey: "setup.provider.vendor.name", DescKey: "setup.provider.vendor.desc", Icon: "key"},
		{Kind: "ollama", NameKey: "setup.provider.ollama.name", DescKey: "setup.provider.ollama.desc", Icon: "server-2"},
		{Kind: "custom", NameKey: "setup.provider.custom.name", DescKey: "setup.provider.custom.desc", Icon: "adjustments"},
	}
}

// RecommendFor is the built-in recommendation by the largest GPU's VRAM.
func RecommendFor(gpus []jobs.GPU) *Recommendation {
	best := 0
	for _, g := range gpus {
		if g.VRAMTotalMB > best {
			best = g.VRAMTotalMB
		}
	}
	gb := best / 1024
	switch {
	case gb >= 20:
		return &Recommendation{Models: []string{"Qwen-Image Edit", "FLUX.2 [klein]", "Illustrious XL"}, DownloadGB: 41, InstallHref: "/models/local?recommended=1"}
	case gb >= 12:
		return &Recommendation{Models: []string{"FLUX.2 [klein]", "Illustrious XL"}, DownloadGB: 24, InstallHref: "/models/local?recommended=1"}
	case gb >= 8:
		return &Recommendation{Models: []string{"Illustrious XL"}, DownloadGB: 7, InstallHref: "/models/local?recommended=1"}
	}
	return nil
}

// defaultModelStep is used until the AI module provides a ModelStep.
type defaultModelStep struct{}

func (defaultModelStep) Providers(context.Context) ([]ProviderCard, error) {
	return DefaultProviders(), nil
}

func (defaultModelStep) Recommend(_ context.Context, gpus []jobs.GPU) (*Recommendation, error) {
	return RecommendFor(gpus), nil
}

func gbString(f float64) string {
	if f == float64(int(f)) {
		return fmt.Sprintf("%d", int(f))
	}
	return fmt.Sprintf("%.1f", f)
}
