package gen

import "github.com/MaramHarsha/MangaVeyra/internal/studio"

// Outfit views belong to a variant, not every costume the character has worn.
// An unassigned legacy reference is used only when no default variant exists.
func outfitReferenceIDs(c studio.Character, limit int) []string {
	limit = max(1, min(5, limit))
	variant, image := "", ""
	for _, outfit := range c.Outfits {
		if outfit.IsDefault {
			variant, image = outfit.ID, outfit.AssetID
			break
		}
	}
	out := []string{}
	seen := map[string]bool{}
	add := func(id string) {
		if id != "" && !seen[id] && len(out) < limit {
			out, seen[id] = append(out, id), true
		}
	}
	add(image)
	for _, ref := range orderedRefs(c.Refs) {
		if ref.Kind != "outfit" || ref.OutfitID != variant {
			continue
		}
		add(ref.AssetID)
		if len(out) == limit || variant == "" {
			break
		}
	}
	return out
}

// Preserve sheet-first generation while excluding another variant's pose
// views. The default outfit's main image also participates when kept separately.
func generationRefs(c studio.Character) []studio.CharacterRef {
	variant, image := "", ""
	for _, outfit := range c.Outfits {
		if outfit.IsDefault {
			variant, image = outfit.ID, outfit.AssetID
			break
		}
	}
	refs := []studio.CharacterRef{}
	seen := map[string]bool{}
	for _, ref := range c.Refs {
		if ref.Kind == "outfit" && ref.OutfitID != variant {
			continue
		}
		if !seen[ref.AssetID] {
			refs, seen[ref.AssetID] = append(refs, ref), true
		}
	}
	if image != "" && !seen[image] {
		refs = append(refs, studio.CharacterRef{AssetID: image, Kind: "outfit", OutfitID: variant, Order: -1})
	}
	return orderedRefs(refs)
}
