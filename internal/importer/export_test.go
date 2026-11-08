package importer

import (
	"context"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
)

// Extract reads a source with limits and reports the accepted image names
// in page order, the skipped count and the refusal code.
func Extract(ctx context.Context, kind, src, dir string, lim Limits) ([]string, int, string, error) {
	x, err := extract(ctx, kind, src, dir, lim)
	if err != nil {
		return nil, 0, codeOf(err), err
	}
	names := make([]string, len(x.Images))
	for i, im := range x.Images {
		names[i] = im.Name
	}
	return names, x.Skipped, "", nil
}

// SafeName exposes the entry-name check.
func SafeName(name string) (string, error) { return safeName(name) }

// NaturalLess exposes the page-order comparison.
func NaturalLess(a, b string) bool { return naturalLess(a, b) }

// SetLimits replaces the module's limits.
func SetLimits(d *core.Deps, lim Limits) { core.MustUse[Service](d).(*service).limits = lim }

// SetDetectTimeout shortens the per-page worker wait.
func SetDetectTimeout(t time.Duration) func() {
	old := detectTimeout
	detectTimeout = t
	return func() { detectTimeout = old }
}

// FailAfter makes the next imports stop with an internal error once n pages are saved.
func FailAfter(n int) func() {
	failAfter = n
	return func() { failAfter = -1 }
}
