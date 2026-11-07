package gen

import (
	"context"
	"errors"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// ErrDeferredFeature is returned by generation operations that belong to a
// later product gate. It is stable so callers can disable those controls
// without accidentally enqueueing partial implementations.
var ErrDeferredFeature = errors.New("gen: feature deferred until a later gate")

type service struct {
	queue  *jobs.Queue
	auth   *auth.Service
	text   ai.TextService
	studio studio.Service
}

var _ Service = (*service)(nil)

func (s *service) EnqueueCharacterSheet(context.Context, studio.Actor, CharacterSheetRequest) (*jobs.Job, error) {
	return nil, ErrDeferredFeature
}

func (s *service) EnqueuePagesFromScript(context.Context, studio.Actor, PagesFromScriptRequest) (*jobs.Job, error) {
	return nil, ErrDeferredFeature
}

func (s *service) EnqueuePanel(context.Context, studio.Actor, PanelRequest) (*jobs.Job, error) {
	return nil, ErrDeferredFeature
}

func (s *service) EnqueueRegionEdit(context.Context, studio.Actor, RegionEditRequest) (*jobs.Job, error) {
	return nil, ErrDeferredFeature
}

func (s *service) Candidates(context.Context, studio.Actor, string) ([]Candidate, error) {
	return nil, ErrDeferredFeature
}

func (s *service) ApplyVariation(context.Context, studio.Actor, string, int) error {
	return ErrDeferredFeature
}
