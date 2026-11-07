package ai

import (
	"context"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

// RouteNamer names the model and provider a job was routed to, for the route
// chips of other modules ("Placeholder art" · "built-in", "Nano Banana Pro" ·
// "OpenRouter"). Unknown IDs come back as they are.
type RouteNamer interface {
	RouteNames(ctx context.Context, modelID, providerID string) (model, provider string)
}

// modelDisplayName is a model's catalogue name; the built-in placeholder
// models have fixed names.
func (m *Module) modelDisplayName(ctx context.Context, id string) string {
	switch id {
	case PlaceholderArt:
		return i18n.T(ctx, "aiimage.placeholder.art")
	case PlaceholderText:
		return i18n.T(ctx, "aiimage.placeholder.text")
	}
	if manifest, err := m.catalog.lookup(ctx, id); err == nil && manifest.Name != "" {
		return manifest.Name
	}
	return id
}

// RouteNames implements RouteNamer.
func (m *Module) RouteNames(ctx context.Context, modelID, providerID string) (string, string) {
	model := m.modelDisplayName(ctx, modelID)
	switch providerID {
	case "":
		return model, ""
	case PlaceholderID:
		return model, i18n.T(ctx, "aiimage.decision.builtin")
	case providerKindWorker:
		return model, i18n.T(ctx, "aiimage.decision.local")
	}
	var kind string
	err := m.d.DB.Get(ctx, &kind, `SELECT kind FROM providers WHERE id = ?`, providerID)
	switch {
	case db.IsNotFound(err):
		return model, i18n.T(ctx, "aiimage.decision.removed")
	case err != nil:
		return model, ""
	case kind == providerKindLocalText || kind == providerKindWorker:
		return model, i18n.T(ctx, "aiimage.decision.local")
	}
	return model, providerKindName(kind)
}
