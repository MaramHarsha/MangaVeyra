package ai

import (
	"context"
	"math"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/ai/catalog"
	"github.com/MaramHarsha/MangaVeyra/internal/ai/providers"
	"github.com/MaramHarsha/MangaVeyra/internal/ai/router"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// Test prompts (original sample content).
const (
	TestImagePrompt = "courier on a rooftop, screentone"
	TestTextPrompt  = "In one short sentence, describe a courier crossing a rainy rooftop at night."
)

// testMode picks "image" or "text" for a provider test.
func (s *Service) testMode(p *Provider, want string) string {
	canImage := s.public.Can(p.Kind, "image") || p.Kind == providers.KindLocalWorker
	canText := s.public.Can(p.Kind, "text")
	switch {
	case want == "image" && canImage:
		return "image"
	case want == "text" && canText:
		return "text"
	}
	// Custom connections follow what they were described as.
	for _, m := range s.CustomModelsOf(context.Background(), p.ID) {
		if m.Kind == catalog.KindText && canText {
			return "text"
		}
		if m.Kind == catalog.KindImage && canImage {
			return "image"
		}
	}
	switch p.Kind {
	case providers.KindOllama, providers.KindAnthropic, providers.KindOpenAICompatible:
		return "text"
	case providers.KindOpenAI, providers.KindGoogle, providers.KindXAI, providers.KindOpenRouter:
		// Both work; text is the cheap check of the key.
		return "text"
	}
	if canImage {
		return "image"
	}
	return "text"
}

// testModel picks the provider-side model for a test: the custom connection's
// model, else the cheapest catalogue model of the right kind served by this provider kind.
func (s *Service) testModel(ctx context.Context, p *Provider, mode string) (string, *catalog.Manifest) {
	if p.Config.V.ModelID != "" {
		return p.Config.V.ModelID, nil
	}
	for _, m := range s.CustomModelsOf(ctx, p.ID) {
		if (mode == "text") == (m.Kind == catalog.KindText) {
			if o, ok := m.Offer(p.ID); ok {
				return o.ModelID, m
			}
		}
	}
	wantKind := catalog.KindImage
	if mode == "text" {
		wantKind = catalog.KindText
	}
	best, bestCost := "", math.Inf(1)
	var bestM *catalog.Manifest
	for _, m := range s.Catalog(ctx).ByKind(wantKind) {
		if mode == "image" && !m.Supports(catalog.TaskGenerate) {
			continue
		}
		if o, ok := m.Offer(p.Kind); ok {
			c := o.Price.Estimate(512, 768, 0, 50, 50)
			if c < bestCost {
				best, bestCost, bestM = o.ModelID, c, m
			}
		}
	}
	if best == "" && len(p.Config.V.Models) > 0 {
		for _, rm := range p.Config.V.Models {
			if rm.Kind == wantKind {
				return rm.ID, nil
			}
		}
	}
	return best, bestM
}

// TestProvider sends a sample prompt and stores the result (image or text,
// time and the cost the provider reported) as the provider's last test.
func (s *Service) TestProvider(ctx context.Context, u *core.User, id, want string) (*TestResult, error) {
	p, err := s.GetProvider(ctx, id)
	if err != nil {
		return nil, err
	}
	if !p.Builtin && !CanManage(u, p) {
		return nil, core.ErrForbidden("")
	}
	mode := s.testMode(p, want)
	res := &TestResult{At: s.Now().UTC(), Local: p.Local()}
	finish := func() (*TestResult, error) {
		if !p.Builtin {
			if _, err := s.d.DB.Exec(ctx, `UPDATE providers SET last_test = ?, updated_at = ? WHERE id = ?`, db.MustJSON(res), db.Now(), p.ID); err != nil {
				return nil, err
			}
		}
		return res, nil
	}
	if !p.Local() && s.d.Settings.MustInstance(ctx).LocalOnly {
		res.Code, res.Message = "local_only", "local only mode is on: remote providers cannot be called"
		return finish()
	}
	start := time.Now()
	if p.Kind == providers.KindLocalWorker {
		res.Prompt = TestImagePrompt
		c := router.Candidate{ModelID: "", ProviderID: LocalWorkerID, ProviderKind: providers.KindLocalWorker}
		m := &catalog.Manifest{ID: "", Kind: catalog.KindImage, Hardware: catalog.Hardware{AnyWorker: true}}
		if len(s.onlineWorkers(ctx)) == 0 {
			res.Code, res.Message = "no_worker", "no worker is online"
			return finish()
		}
		tctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		out := &Result{}
		need := router.Need{Task: catalog.TaskGenerate, UserID: u.ID}
		err := s.viaWorker(tctx, nil, c, m, need, Request{Prompt: TestImagePrompt, Width: 512, Height: 768, N: 1, Style: "bw_screentone"}, out)
		res.Seconds = time.Since(start).Seconds()
		if err != nil {
			res.Code, res.Message = providers.CodeOf(err), err.Error()
			return finish()
		}
		res.OK = true
		if len(out.Images) > 0 {
			res.ImageKey = out.Images[0].Key
		}
		return finish()
	}
	cfg, err := s.adapterConfig(p)
	if err != nil {
		res.Code, res.Message = providers.CodeAuth, err.Error()
		return finish()
	}
	adapter, ok := s.registryFor(p).Get(p.Kind)
	if !ok {
		res.Code, res.Message = providers.CodeUnsupported, "no adapter"
		return finish()
	}
	model, man := s.testModel(ctx, p, mode)
	res.Model = model
	tctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	switch mode {
	case "text":
		res.Prompt = TestTextPrompt
		tg := adapter.(providers.TextGenerator)
		out, err := tg.Text(tctx, cfg, providers.TextRequest{Model: model, MaxTokens: 120, Messages: []providers.Message{{Role: "user", Content: TestTextPrompt}}})
		res.Seconds = time.Since(start).Seconds()
		if err != nil {
			res.Code, res.Message = providers.CodeOf(err), err.Error()
			return finish()
		}
		res.OK, res.Text = true, truncate(out.Text, 600)
		res.CostUSD = out.CostUSD
		if res.CostUSD == 0 && man != nil {
			if o, ok := man.Offer(p.Kind); ok {
				res.CostUSD = o.Price.Estimate(0, 0, 0, out.TokensIn, out.TokensOut)
			}
		}
	default:
		res.Prompt = TestImagePrompt
		ig, ok := adapter.(providers.ImageGenerator)
		if !ok {
			res.Code, res.Message = providers.CodeUnsupported, "this provider does not generate images"
			return finish()
		}
		out, err := ig.Image(tctx, cfg, providers.ImageRequest{Task: catalog.TaskGenerate, Model: model, Prompt: TestImagePrompt, Width: 512, Height: 768, N: 1,
			Style: "bw_screentone", Label: "TEST IMAGE"}, nil)
		res.Seconds = time.Since(start).Seconds()
		if err != nil {
			res.Code, res.Message = providers.CodeOf(err), err.Error()
			return finish()
		}
		o, err := s.store(ctx, nil, "test", out.Images[0], out.Images[0].Seed)
		if err != nil {
			return nil, err
		}
		res.OK, res.ImageKey = true, o.Key
		res.CostUSD = out.CostUSD
		if res.CostUSD == 0 && man != nil && !p.Local() {
			if off, ok := man.Offer(p.Kind); ok {
				res.CostUSD = off.Price.Estimate(512, 768, 0, 0, 0)
			}
		} else if res.CostUSD == 0 && !p.Local() {
			res.CostUSD = p.Config.V.CostPerImage
		}
	}
	res.Seconds = math.Round(res.Seconds*10) / 10
	if !p.Local() && res.CostUSD > 0 {
		s.ledger(ctx, nil, router.Need{UserID: u.ID}, "test:"+model, p.ID, "remote", math.Round(res.CostUSD*10000)/10000, 0)
	}
	return finish()
}
