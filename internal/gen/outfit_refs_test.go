package gen

import (
	"slices"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

func TestOutfitViewsNeverCrossVariantsOrReplaceIdentityViews(t *testing.T) {
	c := studio.Character{Outfits: []studio.Outfit{{ID: "courier", AssetID: "front", IsDefault: true}}, Refs: []studio.CharacterRef{
		{AssetID: "sheet", Kind: "sheet"}, {AssetID: "front", Kind: "front"},
		{AssetID: "legacy-costume", Kind: "outfit"},
		{AssetID: "storm-pose", Kind: "outfit", OutfitID: "storm"},
		{AssetID: "courier-pose", Kind: "outfit", OutfitID: "courier"},
	}}
	if got := outfitReferenceIDs(c, 5); !slices.Equal(got, []string{"front", "courier-pose"}) {
		t.Fatalf("wrong costume in outfit assessment: %v", got)
	}
	if got := generationRefs(c); len(got) != 3 || got[0].AssetID != "sheet" || got[1].AssetID != "front" || got[2].AssetID != "courier-pose" {
		t.Fatalf("wrong costume in generation references: %+v", got)
	}
	c.Outfits = []studio.Outfit{{ID: "storm", IsDefault: true}}
	if got := outfitReferenceIDs(c, 5); !slices.Equal(got, []string{"storm-pose"}) {
		t.Fatalf("default switch reused prior wardrobe: %v", got)
	}
	c.Outfits = nil
	if got := outfitReferenceIDs(c, 5); !slices.Equal(got, []string{"legacy-costume"}) {
		t.Fatalf("unassigned fallback crossed a bound variant: %v", got)
	}
}
