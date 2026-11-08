package studioui

import (
	"context"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// The route chip names Character Lock scorers like the queue does and never
// shows their internal model IDs.
func TestRouteChipNamesCharacterLockScorers(t *testing.T) {
	m := &Module{d: &core.Deps{}}
	for _, tt := range []struct{ model, en, ja string }{
		{"character-lock-dinov2-v1", "DINOv2", "DINOv2"},
		{"character-lock-ccip-v1", "CCIP", "CCIP"},
		{"character-lock-ccip-outfit-v3", "CCIP + outfit", "CCIP + 衣装"},
		{"character-lock-ccip-outfit-multiview-v1", "CCIP + outfit views", "CCIP + 衣装ビュー"},
	} {
		for lang, want := range map[string]string{"en": tt.en, "ja": tt.ja} {
			got := m.routePick(i18n.WithLocale(context.Background(), lang), &jobs.Job{ModelID: tt.model, ProviderID: "local-worker"})
			if !strings.HasPrefix(got, want+" · ") || strings.Contains(got, "character-lock") {
				t.Errorf("%s route chip (%s) = %q, want %q first", tt.model, lang, got, want)
			}
		}
		if gen.LockModelLabelKey(tt.model) == "" {
			t.Errorf("%s has no display name", tt.model)
		}
	}
	if gen.LockModelLabelKey("gpt-image-2.5-sunburst") != "" {
		t.Error("an image model was named as a Character Lock scorer")
	}
}
