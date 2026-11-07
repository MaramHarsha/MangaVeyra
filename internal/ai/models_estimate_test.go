package ai

import (
	"context"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// Paid models show the design's estimate from the manifest's published price
// before they are connected; local-worker models show GPU time.
func TestImageCatalogueShowsPublishedEstimates(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		m, _ := imageTestModule(t, d, &queuedDoer{})
		models, err := m.catalog.list(ctx)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{seedreamModel: "~$0.04 / img", falSchnellModel: "~$0.006 / img", falInpaintModel: "~$0.07 / img", "sdxl-base": "GPU time"}
		seen := 0
		for _, model := range models {
			if label, ok := want[model.ID]; ok {
				seen++
				if got := imageEstimate(ctx, model); got != label {
					t.Errorf("%s estimate = %q, want %q", model.ID, got, label)
				}
			}
		}
		if seen != len(want) {
			t.Fatalf("catalogue lacks expected models: %d", seen)
		}
		if got := imageCostLabel(ctx, 0.13); got != "~$0.13 / img" {
			t.Fatalf("design example = %q", got)
		}
	})
}
