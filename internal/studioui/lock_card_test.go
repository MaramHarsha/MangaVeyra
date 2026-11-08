package studioui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

func renderLockCard(t *testing.T, lang string, v biblePage) string {
	t.Helper()
	var b bytes.Buffer
	if err := BibleLockCard(v).Render(i18n.WithLocale(context.Background(), lang), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// C3 follows the handoff: the amber drift line is scoped to the chapter its
// Fix link opens ("1 drifted panel in Ch. 8 · Fix"), the lock icon takes the
// card state, counts are pluralized and the worker helper appears only while
// no scoring worker is connected.
func TestCharacterLockCardMatchesTheC3Artboard(t *testing.T) {
	score := .96
	v := biblePage{Project: studio.Project{ID: "project"}, Character: studio.Character{ID: "rin", UpdatedAt: time.Unix(1, 0)}, CanGenerate: true, CanArt: true}
	v.Lock = gen.LockStatus{Status: "assessed", Score: &score, Method: "dinov2-small-v1:" + strings.Repeat("a", 64), Panels: 1, References: 1, Unscored: 1, OutfitPending: 1, RecentScores: []float64{.96},
		DriftTotal: 7, DriftChapterID: "chapter-8", DriftChapterNumber: 8, DriftChapterTotal: 1, ScoringWorker: true, CanScore: true,
		Drift: []gen.DriftPanel{{ChapterID: "chapter-8", PageNumber: 3, Score: .8}}}
	html := renderLockCard(t, "en", v)
	for _, want := range []string{
		"mv-bible__lock-card--ready",
		`<p class="mv-bible__lock-drift">1 drifted panel in Ch. 8 · <a href="/studio/p/project/chapters/chapter-8/edit?page=3&amp;tool=lockfix&amp;character=rin">Fix</a></p>`,
		">1 reference<", "average across 1 assessed panel.", "1 panel still needs assessment.", "1 assessed panel has no kept outfit image to compare.", "1 recent assessed panel measurement<",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("C3 card lacks %q", want)
		}
	}
	for _, unwanted := range []string{"7 drifted", "below the drift threshold", "1 panels", "1 references", "Assessment needs a local CCIP or DINOv2 worker"} {
		if strings.Contains(html, unwanted) {
			t.Errorf("C3 card shows %q", unwanted)
		}
	}

	// A failing average turns the card amber; without a scoring worker the
	// helper explains what is needed.
	low := .8
	v.Lock.Score, v.Lock.ScoringWorker, v.Lock.CanScore, v.Lock.DriftChapterTotal = &low, false, false, 2
	html = renderLockCard(t, "en", v)
	if !strings.Contains(html, "mv-bible__lock-card--drift") || !strings.Contains(html, "2 drifted panels in Ch. 8") || !strings.Contains(html, "Assessment needs a local CCIP or DINOv2 worker") {
		t.Fatalf("drift/no-worker card: %s", html)
	}

	ja := renderLockCard(t, "ja", v)
	for _, want := range []string{"参考画像 1枚", "第8話にずれのあるコマが2個", "キャラクターロックを学習"} {
		if !strings.Contains(ja, want) {
			t.Errorf("ja C3 card lacks %q", want)
		}
	}
	if strings.Contains(ja, "Character Lock") || strings.Contains(ja, "references") || strings.Contains(ja, "panels") {
		t.Errorf("ja C3 card mixes English: %s", ja)
	}
}
