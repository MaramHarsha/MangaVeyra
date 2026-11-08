// Package agegate is the reader's small, import-cycle-free contract for the
// once-per-series age confirmation of mature and adult series (ADR 0012 §1).
//
// The reader module provides Gate (core.Use[agegate.Gate]) and serves the
// confirmation form target; the E5 series page can show the same step:
//
//	if gate, ok := core.Use[agegate.Gate](d); ok {
//		need, err := gate.Required(ctx, viewer, series)
//		// render a form that POSTs to agegate.ConfirmPath(series.Slug) with next=<return path>
//	}
package agegate

import (
	"context"
	"net/url"

	"github.com/MaramHarsha/MangaVeyra/internal/publish"
)

// Gate answers whether a viewer still has to confirm their age for a series.
type Gate interface {
	// Required reports whether v must confirm before reading s: true only for
	// a signed-in viewer of a mature or adult series without a stored
	// confirmation. Anonymous viewers never see such series (publish hides them).
	Required(ctx context.Context, v publish.Viewer, s publish.Series) (bool, error)
}

// Applies reports whether an age rating asks for a confirmation.
func Applies(rating string) bool {
	return rating == publish.RatingMature || rating == publish.RatingAdult
}

// ConfirmPath is the form target (POST, optional field "next" with a local
// path to return to) that stores the confirmation for the signed-in reader.
func ConfirmPath(slug string) string { return "/s/" + url.PathEscape(slug) + "/confirm-age" }
