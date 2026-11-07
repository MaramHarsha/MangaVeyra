package app

import (
	"github.com/MaramHarsha/MangaVeyra/internal/account"
	"github.com/MaramHarsha/MangaVeyra/internal/activityui"
	"github.com/MaramHarsha/MangaVeyra/internal/admin"
	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/analytics"
	"github.com/MaramHarsha/MangaVeyra/internal/animate"
	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/authui"
	"github.com/MaramHarsha/MangaVeyra/internal/backup"
	"github.com/MaramHarsha/MangaVeyra/internal/compose"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/discover"
	"github.com/MaramHarsha/MangaVeyra/internal/editor"
	"github.com/MaramHarsha/MangaVeyra/internal/export"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/importer"
	"github.com/MaramHarsha/MangaVeyra/internal/lettering"
	"github.com/MaramHarsha/MangaVeyra/internal/mcp"
	"github.com/MaramHarsha/MangaVeyra/internal/packs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/plugins"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/reader"
	"github.com/MaramHarsha/MangaVeyra/internal/seed"
	"github.com/MaramHarsha/MangaVeyra/internal/setup"
	"github.com/MaramHarsha/MangaVeyra/internal/social"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/studioui"
	"github.com/MaramHarsha/MangaVeyra/internal/translate"
	"github.com/MaramHarsha/MangaVeyra/internal/update"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/world"
	"github.com/MaramHarsha/MangaVeyra/internal/xp"
)

// foundationModules are the core services that expose HTTP routes.
func foundationModules(a *App) []core.Module {
	d := a.Deps
	return []core.Module{
		&healthModule{d: d},
		&mediaModule{d: d},
		auth.NewModule(d.Auth),
		a.JobAPI,
		d.Hub,
		d.Notify,
	}
}

// FeatureModules lists every feature module once, in dependency order:
// services that others Use are constructed first. Each module's New may
// core.Provide its services; cross-module lookups belong in Init.
//
// The web layer (internal/web: layouts, UI kit, static assets, error pages)
// registers itself here as well, first in the list, so its hooks are
// installed before other modules render pages.
func FeatureModules(d *core.Deps) []core.Module {
	return []core.Module{
		// Web layer first: it provides *web.UI and installs the render hooks.
		web.New(d),
		// Shared file storage (provides *assets.Service).
		assets.NewModule(d),
		// Setup and accounts.
		setup.New(d),
		account.New(d),
		authui.New(d),
		// Studio core and AI.
		studio.New(d),
		pagestore.New(d),
		ai.New(d),
		compose.New(d),
		lettering.New(d),
		editor.New(d),
		gen.New(d),
		studioui.New(d),
		activityui.New(d),
		translate.New(d),
		animate.New(d),
		// Publishing, reading and community.
		publish.New(d),
		reader.New(d),
		social.New(d),
		discover.New(d),
		analytics.New(d),
		xp.New(d),
		// Files in and out.
		export.New(d),
		importer.New(d),
		packs.New(d),
		backup.New(d),
		// Operations.
		admin.New(d),
		update.New(d),
		plugins.New(d),
		mcp.New(d),
		world.New(d),
		seed.New(d),
		// Placeholder pages (e.g. the /studio dashboard) whose routes no module claimed. Keep last.
		web.NewFallbacks(d),
	}
}
