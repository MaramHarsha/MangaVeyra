package gen

import (
	"context"
	"errors"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// A deadlock or serialization failure rolls the save back, so the attempt is
// retried instead of failing the job for good after the provider was paid.
func TestTransientSaveErrorsAreRetryable(t *testing.T) {
	ctx := context.Background()
	for _, msg := range []string{
		"db: commit: ERROR: deadlock detected (SQLSTATE 40P01)",
		"ERROR: could not serialize access due to concurrent update (SQLSTATE 40001)",
		"database is locked (5) (SQLITE_BUSY)",
	} {
		var jerr *jobs.JobError
		if err := mapStudioJobError(ctx, errors.New(msg)); !errors.As(err, &jerr) || !jerr.Retryable {
			t.Errorf("%q -> %#v, want a retryable job error", msg, err)
		}
	}
	var jerr *jobs.JobError
	if err := mapStudioJobError(ctx, errors.New("disk full")); !errors.As(err, &jerr) || jerr.Retryable {
		t.Fatalf("an unknown error must stay non-retryable: %#v", err)
	}
}
