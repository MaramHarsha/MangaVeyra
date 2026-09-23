package seed

import (
	"context"
	"errors"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
)

// ErrNotAvailable is returned until the seed module ships demo content.
var ErrNotAvailable = errors.New("seed: demo content is not available in this build yet")

// Options control seeding.
type Options struct {
	// Demo creates the Tidebreak Courier demo project, series and readers.
	Demo bool
	// OwnerUsername owns the demo project ("" = first admin).
	OwnerUsername string
}

// Run seeds the database (called by `mangaveyra seed`).
func Run(ctx context.Context, d *core.Deps, opts Options) error {
	return ErrNotAvailable
}
