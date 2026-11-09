package ai

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

type imageModelView struct {
	Name, ID, Provider, Use, Status, Cost string
	Ready                                 bool
	Connection                            *providerView
	// Warning marks a connected route the key cannot use (a warning badge,
	// as the design's Licence guard); Detail says why in full.
	Warning bool
	Detail  string
	// Verification is how the row's route has been verified (G2 meta line).
	Verification string
}

func (m *Module) catalogueRows(ctx context.Context, actor providerActor, manageable []providerView, inst settings.Instance) ([]imageModelView, error) {
	models, err := m.catalog.list(ctx)
	if err != nil {
		return nil, err
	}
	candidates, err := m.providers.Candidates(ctx, actor.UserID)
	if err != nil {
		return nil, err
	}
	workers, err := m.d.Jobs.ListWorkers(ctx)
	if err != nil {
		return nil, err
	}
	used := map[string]bool{}
	out := []imageModelView{}
	for _, model := range models {
		row := imageModelView{Name: model.Name, ID: model.ID, Status: i18n.T(ctx, "models.tile.not_connected"), Use: i18n.T(ctx, "aiimage.use.panels"), Cost: imageEstimate(ctx, model)}
		if len(model.Providers) > 0 {
			row.Verification = providerVerification(model.Providers[0])
		}
		if model.Edit {
			row.Use = i18n.T(ctx, "aiimage.use.edits")
		}
		if model.ID == PlaceholderArt {
			// The placeholder is every image task's fallback, not only edits;
			// its name is translated like the text placeholder's.
			row.Name = m.modelDisplayName(ctx, model.ID)
			row.Provider = i18n.T(ctx, "aiimage.provider.placeholder")
			row.Use = i18n.T(ctx, "aiimage.use.placeholder")
			row.Cost = i18n.T(ctx, "models.cost.none")
			row.Ready = true
			row.Status = i18n.T(ctx, "models.status.ready")
		}
		var routed *providerCandidate
		for _, p := range candidates {
			if !hasProvider(model, p.Kind) {
				continue
			}
			routed = &p
			row.Provider = providerKindName(p.Kind)
			row.Ready = !inst.LocalOnly
			row.Status = i18n.T(ctx, "models.status.ready")
			if inst.LocalOnly {
				row.Status = i18n.T(ctx, "models.tile.blocked")
			}
			if quote, ok := estimateQuote(p.Kind, model); ok {
				row.Cost = imageCostLabel(ctx, quote.USD)
			}
			if !used[p.ID] {
				for _, v := range manageable {
					if v.ID == p.ID {
						copy := v
						row.Connection = &copy
						used[p.ID] = true
						break
					}
				}
			}
			break
		}

		// Disabled connections remain manageable from the image tab; otherwise
		// disabling the only fal connection would also hide its enable action.
		if row.Provider == "" {
			for _, v := range manageable {
				if !hasProvider(model, v.Kind) {
					continue
				}
				row.Provider = providerKindName(v.Kind)
				row.Status = providerStatusLabel(ctx, v)
				if !used[v.ID] {
					copy := v
					row.Connection = &copy
					used[v.ID] = true
				}
				if quote, ok := estimateQuote(v.Kind, model); ok {
					row.Cost = imageCostLabel(ctx, quote.USD)
				}
				break
			}
		}
		if row.Connection != nil && !inst.LocalOnly {
			row.Status = providerStatusLabel(ctx, *row.Connection)
			row.Ready = row.Connection.Enabled && row.Connection.LastTest.State == "ok"
		}
		// The connection's last test (or a GPT Image refusal) showed the key
		// cannot use this model, so routing skips it: say so in the row.
		if routed != nil && capabilityKind(routed.Kind) && !inst.LocalOnly && !routed.Test.imageModelAvailable(model.ID) {
			row.Ready, row.Warning = false, true
			row.Status, row.Detail = i18n.T(ctx, "models.status.no_access"), i18n.T(ctx, "models.test.unavailable")
			if routed.Test.Images == capabilityVerification {
				row.Status, row.Detail = i18n.T(ctx, "models.status.verify"), i18n.T(ctx, "models.status.verify_detail")
			}
		}
		if row.Provider == "" && hasProvider(model, providerKindWorker) {
			row.Provider = i18n.T(ctx, "models.tile.local")
			for _, w := range workers {
				if w.Status != jobs.WorkerOnline || time.Since(w.LastSeenAt) > jobs.MissedHeartbeats*jobs.HeartbeatInterval {
					continue
				}
				for _, id := range w.Models.V {
					if id == model.ID {
						row.Ready = !inst.LocalOnly
						row.Status = i18n.T(ctx, "models.status.ready")
					}
				}
			}
			row.Cost = i18n.T(ctx, "aiimage.cost.gpu_time")
			if inst.LocalOnly {
				row.Status = i18n.T(ctx, "models.tile.blocked")
			}
		}
		if row.Provider == "" && len(model.Providers) > 0 {
			row.Provider = providerKindName(model.Providers[0])
		}
		out = append(out, row)
	}
	return append(out, customImageRows(ctx, manageable, inst)...), nil
}

// customImageRows are the G12 image connections (ComfyUI workflows, HTTP
// endpoints, Hugging Face image endpoints): one row each, managed in place.
func customImageRows(ctx context.Context, manageable []providerView, inst settings.Instance) []imageModelView {
	out := []imageModelView{}
	for _, v := range manageable {
		if !v.ImageOnly || !customKind(v.Kind) {
			continue
		}
		view := v
		row := imageModelView{Name: v.Name, ID: customModelPrefix + v.ID, Provider: providerKindName(v.Kind), Use: i18n.T(ctx, "aiimage.use.panels"),
			Status: providerStatusLabel(ctx, v), Ready: v.Enabled && v.LastTest.State == "ok" && !inst.LocalOnly, Connection: &view,
			Cost: i18n.T(ctx, "aiimage.cost.gpu_time"), Verification: providerVerification(v.Kind)}
		if v.Kind == providerKindHuggingFace {
			row.Cost = i18n.T(ctx, "models.cost.endpoint_hours")
		}
		if inst.LocalOnly {
			row.Status = i18n.T(ctx, "models.tile.blocked")
		}
		out = append(out, row)
	}
	return out
}

// estimateQuote is the one 1024 × 1024 image a COST cell estimates: a new
// image when the route can make one, otherwise a masked edit (fal
// inpainting). Token-priced routes would otherwise show the init and mask
// uploads of an edit as the price of every panel (ADR 0009).
func estimateQuote(kind string, model ModelManifest) (jobs.Cost, bool) {
	n := ImageNeeds{Width: 1024, Height: 1024, Count: 1}
	if quote, ok := quoteImage(kind, model.ID, n); ok {
		return quote, true
	}
	n.Edit, n.Mask = model.Edit, model.Mask
	return quoteImage(kind, model.ID, n)
}

// imageEstimate is the COST cell of a model before it is connected: the
// published per-image price of its first remote provider, shown as the
// compute meter's estimate of spend on the user's own key ("~$0.04 / img").
// Local-worker models cost GPU time; anything without a published price
// shows none.
func imageEstimate(ctx context.Context, model ModelManifest) string {
	for _, kind := range model.Providers {
		if quote, ok := estimateQuote(kind, model); ok && quote.USD > 0 {
			return imageCostLabel(ctx, quote.USD)
		}
	}
	if hasProvider(model, providerKindWorker) {
		return i18n.T(ctx, "aiimage.cost.gpu_time")
	}
	return i18n.T(ctx, "models.cost.none")
}

// imageCostLabel formats a per-image estimate as designed ("~$0.13 / img"),
// keeping a third decimal only when it carries information ("~$0.003 / img").
func imageCostLabel(ctx context.Context, usd float64) string {
	amount := strconv.FormatFloat(usd, 'f', 3, 64)
	if strings.HasSuffix(amount, "0") {
		amount = amount[:len(amount)-1]
	}
	return i18n.T(ctx, "aiimage.cost.per_image", "amount", "$"+amount)
}

func providerRowModel(v providerView) string {
	if v.Kind == providerKindLocalText {
		return v.LocalModel
	}
	if v.TextModel != "" {
		return v.TextModel
	}
	return providerKindModel(v.Kind)
}
