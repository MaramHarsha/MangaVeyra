package pagestore_test

import (
	"context"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func bubbleIDs(p pagedoc.Page) []string {
	out := make([]string, len(p.Bubbles))
	for i, b := range p.Bubbles {
		if b.Order != i {
			return nil
		}
		out[i] = b.ID
	}
	return out
}

func same(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The editor reorders reading order by swapping Order values without moving
// bubbles in the array; the stored page must keep that reading order, also
// through version history and revert.
func TestBubbleReadingOrderPersistsThroughSaveAndRevert(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx, f := context.Background(), newFixture(t, ta)
		page := f.create(t, 1)[0]
		first := page.Bubbles[0]
		for i := 1; i < 3; i++ {
			b := first
			b.ID = db.NewID()
			b.Order = i
			b.Box.Y += float64(i) * 110
			b.Texts = map[string]pagedoc.BubbleText{"ja": {Text: "吹き出し", Status: "draft", Source: "human"}}
			page.Bubbles = append(page.Bubbles, b)
		}
		saved, err := f.pages.SavePage(ctx, page, page.Version, f.change)
		if err != nil {
			t.Fatal(err)
		}
		original := bubbleIDs(saved)
		if len(original) != 3 {
			t.Fatalf("bubbles=%+v", saved.Bubbles)
		}

		// Swap the reading order of the first and last bubble in place.
		reordered := saved
		reordered.Bubbles = append([]pagedoc.Bubble(nil), saved.Bubbles...)
		reordered.Bubbles[0].Order, reordered.Bubbles[2].Order = 2, 0
		want := []string{original[2], original[1], original[0]}
		out, err := f.pages.SavePage(ctx, reordered, saved.Version, f.change)
		if err != nil {
			t.Fatal(err)
		}
		if got := bubbleIDs(out); !same(got, want) {
			t.Fatalf("saved order=%v want %v", got, want)
		}
		loaded, err := f.pages.GetPage(ctx, page.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got := bubbleIDs(loaded); !same(got, want) {
			t.Fatalf("reloaded order=%v want %v", got, want)
		}

		// Reverting to the version before the swap restores that order, and
		// reverting to the swapped version restores the swap.
		versions, err := f.pages.Versions(ctx, page.ID, 10)
		if err != nil || len(versions) < 3 {
			t.Fatalf("versions=%d %v", len(versions), err)
		}
		before, swapped := versions[1], versions[0]
		reverted, err := f.pages.Revert(ctx, page.ID, before.ID, f.change)
		if err != nil {
			t.Fatal(err)
		}
		if got := bubbleIDs(reverted); !same(got, original) {
			t.Fatalf("revert order=%v want %v", got, original)
		}
		again, err := f.pages.Revert(ctx, page.ID, swapped.ID, f.change)
		if err != nil {
			t.Fatal(err)
		}
		if got := bubbleIDs(again); !same(got, want) {
			t.Fatalf("revert to swap=%v want %v", got, want)
		}
	})
}
