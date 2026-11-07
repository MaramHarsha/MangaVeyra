package gen

import (
	"errors"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/compose"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/lettering"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// ErrDeferredFeature is retained for source compatibility. Implemented Phase 1
// operations never return it.
var ErrDeferredFeature = errors.New("gen: feature deferred until a later gate")

type service struct {
	queue     *jobs.Queue
	auth      *auth.Service
	text      ai.TextService
	studio    studio.Service
	db        *db.DB
	images    ai.ImageService
	pages     pagestore.Service
	assets    *assets.Service
	lettering lettering.Engine
	renderer  compose.Renderer
	fallback  ai.ImageFallback
}

var _ Service = (*service)(nil)
