package backup

import (
	"context"
	"errors"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
)

// ErrNotAvailable is returned until the backup module is implemented.
var ErrNotAvailable = errors.New("backup: backups are not available in this build yet")

// RunCLI implements `mangaveyra backup <create|list|restore> [flags]`.
func RunCLI(ctx context.Context, d *core.Deps, args []string) error {
	return ErrNotAvailable
}
