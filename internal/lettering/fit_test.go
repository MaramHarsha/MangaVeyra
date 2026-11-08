package lettering

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
)

func prepared(t *testing.T, e *engine, c fitCase) *bubbleFit {
	t.Helper()
	p, err := e.prepare(context.Background(), c.b, c.lang)
	if err != nil {
		t.Fatalf("%s: %v", c.name, err)
	}
	return p
}

// referenceArrange is the line-count loop of commit 3612a17 without any of
// arrange's shortcuts: every block size is wrapped in full.
func referenceArrange(f *fitter, size float64) arrangement {
	a := arrangement{size: size, pitch: size * f.gap, rubySize: size * .45}
	if f.b.Style.Stroke {
		a.stroke = textStrokeWidth(size) / 2
	}
	if len(f.ruby) > 0 {
		a.rubyBand = a.rubySize * 1.1
	}
	maxLines := int(math.Floor(f.area.across(f.vertical)/a.pitch + 1e-9))
	for n := 1; n <= maxLines && !a.found; n++ {
		_, ls, ok := f.limits(&a, n, false)
		if !ok {
			break
		}
		lines := f.st.wrapLines(func(i int) float64 {
			if i < len(ls) {
				return ls[i] / size
			}
			return 0
		})
		if len(lines) > n {
			continue
		}
		starts, lengths, ok := f.limits(&a, len(lines), false)
		if !ok {
			continue
		}
		found := true
		for i, line := range lines {
			found = found && line.advance*size <= lengths[i]+.01
		}
		if found {
			a.lines, a.starts, a.lengths, a.found = lines, starts, lengths, true
		}
	}
	return a
}

// At every size the scan can try, the measured decision equals the
// unoptimised one: the same lines from the full line-count loop, and the
// same inside verdict as drawing every glyph outline.
func TestMeasuredFitMatchesDrawnFitAtEverySize(t *testing.T) {
	e := testEngine(t).(*engine)
	stride := 4 * raceSlowdown
	if testing.Short() {
		stride *= 5
	}
	sizes, insides, cases := 0, 0, 0
	for i, c := range fitCorpus() {
		if i%stride != 0 || strings.HasPrefix(c.name, "stress/") {
			continue
		}
		cases++
		p := prepared(t, e, c)
		for size := p.size; ; size = math.Max(p.minSize, size-1) {
			p.limit = math.MaxInt
			got := p.try(size)
			want := referenceArrange(p.fitter, size)
			if got.found != want.found || got.found && (!reflect.DeepEqual(got.lines, want.lines) || !reflect.DeepEqual(got.starts, want.starts) || !reflect.DeepEqual(got.lengths, want.lengths)) {
				t.Fatalf("%s at %g: arrangement differs: found %v/%v\n got  %+v\n want %+v", c.name, size, got.found, want.found, got.lines, want.lines)
			}
			drawn := false
			if want.found {
				e.mu.Lock()
				drawn = p.draw(&want).inside
				e.mu.Unlock()
			}
			if got.fits != drawn {
				t.Fatalf("%s at %g: measured inside=%v, drawn inside=%v", c.name, size, got.fits, drawn)
			}
			sizes++
			if drawn {
				insides++
			}
			if size <= p.minSize {
				break
			}
		}
	}
	t.Logf("%d cases, %d sizes compared (%d inside)", cases, sizes, insides)
	if sizes < 1000/raceSlowdown || insides == 0 {
		t.Fatal("too few sizes compared")
	}
}

// The corpus never needs a drawn decision to be overruled, nor the budget.
func TestCorpusFitsWithinBudget(t *testing.T) {
	e := testEngine(t).(*engine)
	worst, exact := 0, 0
	for i, c := range fitCorpus() {
		if raceSlowdown > 1 && i%10 != 0 && !strings.HasPrefix(c.name, "stress/") {
			continue
		}
		_, stats, err := e.layout(context.Background(), c.b, c.lang)
		if err != nil {
			t.Fatal(err)
		}
		if stats.fit.mismatch != 0 || stats.fit.exhausted {
			t.Fatalf("%s: %+v", c.name, stats.fit)
		}
		worst, exact = max(worst, stats.fit.work), exact+stats.fit.exact
	}
	t.Logf("most work for one bubble %d of %d; %d ink boxes drawn to decide", worst, fitBudget, exact)
	if worst > fitBudget/4 {
		t.Fatalf("a corpus bubble needed %d of the %d budget", worst, fitBudget)
	}
}

// The denial-of-service report: maximum-size bubbles used to take 4–14 s
// each; the 64-bubble page minutes.
func TestWorstCaseLayoutsAreFast(t *testing.T) {
	e := testEngine(t).(*engine)
	for _, w := range worstTexts() {
		for _, box := range []pagedoc.Rect{{W: 4000, H: 4000}, {W: 200, H: 200}} {
			b := worstBubble(w, box)
			start := time.Now()
			l, stats, err := e.layout(context.Background(), b, w.lang)
			took := time.Since(start)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("%s %gx%g: %v, size %g, %d lines, overflow %v, %+v", w.name, box.W, box.H, took, l.FontSize, len(l.Lines), l.Overflow, stats.fit)
			if took > raceSlowdown*time.Second {
				t.Errorf("%s %gx%g took %v", w.name, box.W, box.H, took)
			}
			if stats.fit.exhausted || stats.fit.mismatch != 0 {
				t.Errorf("%s %gx%g: %+v", w.name, box.W, box.H, stats.fit)
			}
		}
	}
	p, lang := worstPage()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	ls, err := e.LayoutPage(p, lang)
	took := time.Since(start)
	if err != nil || len(ls) != pagedoc.MaxBubbles {
		t.Fatal(len(ls), err)
	}
	t.Logf("64-bubble page: %v", took)
	if took > 3*raceSlowdown*time.Second {
		t.Errorf("64-bubble page took %v", took)
	}
}

// Text that no size can fit and every size nearly fits (a line of closing
// brackets no break may split) spends the budget instead of minutes: it is
// set at the minimum size, overflowing, as the full scan would set it.
func TestWorkBudgetBoundsPathologicalText(t *testing.T) {
	e := testEngine(t).(*engine)
	b := worstBubble(worstText{name: "kinsoku", lang: "ja", text: strings.Repeat("」", pagedoc.MaxTextRunes)}, pagedoc.Rect{W: 4000, H: 4000})
	start := time.Now()
	l, stats, err := e.layout(context.Background(), b, "ja")
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%v %+v", took, stats.fit)
	if !stats.fit.exhausted || !l.Overflow || l.FontSize != 12 {
		t.Fatalf("budget: size %g overflow %v %+v", l.FontSize, l.Overflow, stats.fit)
	}
	if stats.fit.work > 3*fitBudget || took > raceSlowdown*time.Second {
		t.Fatalf("work %d, %v", stats.fit.work, took)
	}
	// Without auto-fit the requested size overflows.
	b.Style.AutoFit = false
	if l, _, err = e.layout(context.Background(), b, "ja"); err != nil || !l.Overflow || l.FontSize != 1000 {
		t.Fatalf("no auto-fit: %v %g %v", l.Overflow, l.FontSize, err)
	}
}

func TestCancelledContextStopsPageLayout(t *testing.T) {
	e := testEngine(t)
	p, lang := worstPage()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if _, err := LayoutPageContext(ctx, e, p, lang); !errors.Is(err, context.Canceled) || time.Since(start) > 50*raceSlowdown*time.Millisecond {
		t.Fatalf("cancelled before start: %v after %v", err, time.Since(start))
	}
	ctx, cancel = context.WithCancel(context.Background())
	var cancelled time.Time
	go func() {
		time.Sleep(40 * time.Millisecond)
		cancelled = time.Now()
		cancel()
	}()
	_, err := LayoutPageContext(ctx, e, p, lang)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("page layout ran to the end: %v", err)
	}
	// The error above proves the layout stopped early; this bounds how promptly.
	// 250 ms leaves room for scheduling delay when the whole suite runs in
	// parallel (100 ms flaked under full PostgreSQL runs).
	if late := time.Since(cancelled); late > 250*raceSlowdown*time.Millisecond {
		t.Fatalf("stopped %v after cancellation", late)
	}
	// A single bubble's size scan stops too.
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if _, _, err := e.(*engine).layout(ctx, worstBubble(worstTexts()[2], pagedoc.Rect{W: 4000, H: 4000}), "ja"); !errors.Is(err, context.Canceled) {
		t.Fatalf("bubble: %v", err)
	}
	// The engine still works afterwards.
	if l, err := e.Layout(testBubble(), "en"); err != nil || l.TextPath == "" {
		t.Fatal(err)
	}
}

// Large pages hold the engine mutex only while a bubble is shaped and
// drawn, so small layouts from other requests keep going.
func TestSmallLayoutsAreNotStarvedByLargePages(t *testing.T) {
	e := testEngine(t)
	p, lang := worstPage()
	p.Bubbles = p.Bubbles[:16]
	done := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 2 {
				if _, err := e.LayoutPage(p, lang); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	go func() { wg.Wait(); close(done) }()
	small := []struct {
		b    pagedoc.Bubble
		lang string
	}{{testBubble(), "en"}, {testBubble(), "ja"}, {languageBubble("ko", languageSamples["ko"]), "ko"}}
	var slowest time.Duration
	n := 0
	for running := true; running; n++ {
		select {
		case <-done:
			running = false
		default:
		}
		s := small[n%len(small)]
		start := time.Now()
		if _, err := e.Layout(s.b, s.lang); err != nil {
			t.Fatal(err)
		}
		slowest = max(slowest, time.Since(start))
	}
	t.Logf("%d small layouts alongside the large pages, slowest %v", n, slowest)
	if n < 10 || slowest > 250*raceSlowdown*time.Millisecond {
		t.Fatalf("%d small layouts, slowest %v", n, slowest)
	}
}
