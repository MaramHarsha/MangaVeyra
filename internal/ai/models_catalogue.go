package ai

import (
	"context"
	"fmt"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

type imageModelView struct {
	Name, ID, Provider, Use, Status, Cost string
	Ready                                 bool
	Connection                            *providerView
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
		row := imageModelView{Name: model.Name, ID: model.ID, Status: i18n.T(ctx, "models.tile.not_connected"), Use: i18n.T(ctx, "aiimage.use.panels"), Cost: i18n.T(ctx, "models.cost.none")}
		if model.Edit {
			row.Use = i18n.T(ctx, "aiimage.use.edits")
		}
		if model.ID == PlaceholderArt {
			row.Provider = i18n.T(ctx, "aiimage.provider.placeholder")
			row.Ready = true
			row.Status = i18n.T(ctx, "models.status.ready")
		}
		for _, p := range candidates {
			if !hasProvider(model, p.Kind) {
				continue
			}
			row.Provider = providerKindName(p.Kind)
			row.Ready = !inst.LocalOnly
			row.Status = i18n.T(ctx, "models.status.ready")
			if inst.LocalOnly {
				row.Status = i18n.T(ctx, "models.tile.blocked")
			}
			quote, ok := quoteImage(p.Kind, model.ID, ImageNeeds{Width: 1024, Height: 1024, Count: 1, Edit: model.Edit, Mask: model.Mask})
			if ok {
				row.Cost = fmt.Sprintf("≤ $%.3f", quote.USD)
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
				if quote, ok := quoteImage(v.Kind, model.ID, ImageNeeds{Width: 1024, Height: 1024, Count: 1, Edit: model.Edit, Mask: model.Mask}); ok {
					row.Cost = fmt.Sprintf("≤ $%.3f", quote.USD)
				}
				break
			}
		}
		if row.Connection != nil && !inst.LocalOnly {
			row.Status = providerStatusLabel(ctx, *row.Connection)
			row.Ready = row.Connection.Enabled && row.Connection.LastTest.State == "ok"
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
			row.Cost = fmt.Sprintf("~ %.0f s", model.GPUSeconds)
			if inst.LocalOnly {
				row.Status = i18n.T(ctx, "models.tile.blocked")
			}
		}
		if row.Provider == "" && len(model.Providers) > 0 {
			row.Provider = providerKindName(model.Providers[0])
		}
		out = append(out, row)
	}
	return out, nil
}

func providerRowModel(v providerView) string {
	if v.Kind == providerKindLocalText {
		return v.LocalModel
	}
	return providerKindModel(v.Kind)
}
