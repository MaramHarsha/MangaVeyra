package export

import (
	"context"
	"math"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
)

// The E2 layer note uses the floor-and-cap rule: a failing measurement never
// reads 90% or more (0.895 was shown as "LOCK 90%").
func TestExportLockScoreNeverRoundsDriftIntoAPass(t *testing.T) {
	ctx := i18n.WithLocale(context.Background(), "en")
	page := pagedoc.New("page", "project", "chapter", 1, 1600, 2400, "rtl")
	panel := pagedoc.Panel{ID: "panel"}
	for _, tt := range []struct {
		score float64
		want  string
	}{{.895, "LOCK 89%"}, {math.Nextafter(.9, 0), "LOCK 89%"}, {.9, "LOCK 90%"}, {.958, "LOCK 95%"}, {.42, "LOCK 42%"}, {1, "LOCK 100%"}} {
		score := tt.score
		l := pagedoc.Layer{ID: "rin", Kind: pagedoc.LayerCharacter, Provenance: pagedoc.ProvenanceAI, LockScore: &score}
		if got := layerMeta(ctx, page, panel, l); got != tt.want {
			t.Errorf("score %v rendered %q, want %q", tt.score, got, tt.want)
		}
	}
	l := pagedoc.Layer{ID: "rin", Kind: pagedoc.LayerCharacter, Provenance: pagedoc.ProvenanceAI}
	if got := layerMeta(ctx, page, panel, l); got != i18n.T(ctx, "exportui.provenance.ai") {
		t.Errorf("unassessed character layer = %q", got)
	}
}
