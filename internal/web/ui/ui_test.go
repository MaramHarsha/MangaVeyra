package ui

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

func render(t *testing.T, c templ.Component) string {
	t.Helper()
	var buf bytes.Buffer
	ctx := i18n.WithLocale(context.Background(), "en")
	if err := c.Render(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func withText(c templ.Component, text string) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w templWriter) error {
		return c.Render(templ.WithChildren(ctx, templ.Raw(text)), w)
	})
}

func has(t *testing.T, html string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(html, p) {
			t.Errorf("missing %q in:\n%s", p, html)
		}
	}
	if strings.Contains(html, "style=") {
		t.Errorf("components must not emit inline styles: %s", html)
	}
}

func TestButtons(t *testing.T) {
	has(t, render(t, withText(Button(ButtonProps{Variant: Primary, Icon: "sparkles", Kbd: "⌘↵"}), "Generate panel")),
		`class="mv-btn mv-btn--primary"`, `type="button"`, `ti ti-sparkles`, `mv-btn__kbd`, "Generate panel")
	has(t, render(t, Button(ButtonProps{Variant: IconOnly, Size: SM, Icon: "lock", Label: "Locked", Tip: "No AI"})),
		`mv-btn--icon mv-btn--sm`, `aria-label="Locked"`, `data-tip="No AI"`, `mv-ic-16`)
	has(t, render(t, withText(Button(ButtonProps{Variant: Danger, Type: "submit", Disabled: true}), "Delete")), `type="submit"`, ` disabled`)
	has(t, render(t, withText(LinkButton("/studio/new", ButtonProps{Variant: Secondary, Full: true}), "New")),
		`href="/studio/new"`, `mv-btn--secondary mv-btn--full`)
	// Unsafe URLs are neutralised by templ.
	if html := render(t, withText(LinkButton("javascript:alert(1)", ButtonProps{}), "x")); strings.Contains(html, "javascript:") {
		t.Errorf("unsafe href rendered: %s", html)
	}
}

func TestFieldsAndControls(t *testing.T) {
	f := render(t, templ.ComponentFunc(func(ctx context.Context, w templWriter) error {
		return Field(FieldProps{ID: "url", Label: "Instance URL", Error: "Can’t reach this address."}).Render(
			templ.WithChildren(ctx, Input(InputProps{ID: "url", Name: "url", Invalid: true})), w)
	}))
	has(t, f, `for="url"`, `aria-invalid="true"`, `aria-describedby="url-error"`, `role="alert"`, "ti-alert-triangle")
	has(t, render(t, Toggle(ToggleProps{Name: "local_only", Checked: true, Label: "Local only mode"})), `role="switch"`, ` checked`, "mv-toggle__track")
	has(t, render(t, Slider(SliderProps{ID: "s", Name: "s", Label: "Sketch strength", Min: 0, Max: 1, Step: .01, Value: .62, Display: "0.62"})),
		`class="mv-range mv-p-62"`, `step="0.01"`, `data-output="s-out"`)
	seg := render(t, Segmented(SegmentedProps{Name: "view", Value: "webtoon", Label: "Mode", Options: []Option{{Value: "paged", Label: "Paged"}, {Value: "webtoon", Label: "Webtoon"}}}))
	has(t, seg, `role="radiogroup"`, `value="webtoon" checked`)
	has(t, render(t, Select(SelectProps{ID: "a", Name: "a", Value: "b", Options: []Option{{Value: "a", Label: "A"}, {Value: "b", Label: "B"}}})), `value="b" selected`)
	has(t, render(t, Textarea(TextareaProps{ID: "p", Name: "p", Value: "Rin vaults", MaxLength: 600})), "10 / 600")
}

func TestBadgesAvatarsAndCards(t *testing.T) {
	has(t, render(t, StatusBadge(StatusFailed, "Failed")), "mv-badge--error", "ti-alert-triangle", "Failed")
	has(t, render(t, ProvenanceBadge(ProvenanceHuman, "Hand-drawn")), "ti-brush")
	has(t, render(t, Avatar(AvatarProps{Name: "Jun Sato", Online: true})), ">JS<", "mv-avatar--online")
	has(t, render(t, CharacterCard(CharacterCardProps{Kanji: "凛", FirstName: "Rin", LastName: "Asagiri", Lock: .82, LockLabel: "Character Lock"})),
		"mv-ccard__kanji", "mv-lock--drift", "82%", "mv-bar--warning")
	has(t, render(t, ChapterRow(ChapterRowProps{Href: "/s/x/8", Number: 8, Title: "The Lighthouse Debt", New: true, NewLabel: "New"})), ">08<", "mv-chrow--new")
	has(t, render(t, ProjectCard(ProjectCardProps{Href: "/p", Title: "Tidebreak Courier", ThumbLabel: "PAGE THUMBNAIL"})), "mv-pcard__thumb")
	// The designed "…" has no project actions behind it yet: disabled with the Soon hint, never a dead button (ADR 0004 §7).
	has(t, render(t, ProjectCard(ProjectCardProps{Href: "/p", Title: "Tidebreak Courier", MenuLabel: "Project actions"})),
		`aria-label="Project actions · Soon"`, `title="Coming in a later release"`, "disabled")
	// Card names are level-two headings under the page title (heading order).
	has(t, render(t, CharacterCard(CharacterCardProps{FirstName: "Rin", LastName: "Asagiri", Lock: -1})), `<h2 class="mv-ccard__name">`)
	has(t, render(t, SeriesCard(SeriesCardProps{Href: "/s", Title: "Lanterns of Oru", Rank: 1})), "mv-scard__rank", "mv-tone--3x4")
}

func TestAIAndGateComponents(t *testing.T) {
	has(t, render(t, GenState(GenProps{State: GenGenerating, Progress: .62, ProgressText: "62% · step 18/28", Title: "Generating"})),
		`role="progressbar"`, `aria-valuenow="62"`, "mv-gen__fill mv-p-62")
	has(t, render(t, GenState(GenProps{State: GenFailed, SFX: "FZZT", RetryLabel: "Retry", Title: "Failed"})), "FZZT", "ti-refresh")
	has(t, render(t, VariationPicker("Pick a variation", "Keys 1–4", "v", "2", []Variation{{Value: "1", Label: "VAR 1", Lock: -1}, {Value: "2", Label: "VAR 2", Lock: .97}})),
		`aria-keyshortcuts="2"`, "LOCK 97%", `value="2" checked`)
	has(t, render(t, ComputeMeter(MeterProps{Href: "/queue", Load: .68, Text: "2 queued", Title: "RTX 4090"})), "mv-p-68", "2 queued", `aria-label="2 queued, RTX 4090"`)
	if n := (MeterProps{Text: "GPU idle", Title: "GPU idle · RTX 4090"}).Name(); n != "GPU idle · RTX 4090" {
		t.Errorf("meter name repeats its text: %q", n)
	}
	has(t, render(t, RouteChip("Auto", "Qwen-Image Edit · local · ~40 s", "/why", "Why this model?")), "→ Qwen-Image Edit")
	has(t, render(t, RankBadge("S", false)), "mv-rank--s", `aria-label="Rank S"`)
	has(t, render(t, XPBar("XP to LV. 24", "7,240 / 10,000", .724)), "mv-p-72", `aria-label="XP to LV. 24"`, `aria-valuetext="7,240 / 10,000"`)
	// The dashboard draws the bar without a visible label; the progressbar is still named (WCAG 4.1.2).
	has(t, render(t, XPBar("", "", .5)), `role="progressbar"`, `aria-label="XP"`)
	has(t, render(t, LevelUpToast("Level 24 · Storyteller", "Unlocked")), "LV UP")
	has(t, render(t, withText(StatusWindow("Status · Creator", ""), "<b>x</b>")), "mv-status__title")
}

func TestStudioCardsRenderStoredArtAndHonestReferenceStatus(t *testing.T) {
	has(t, render(t, ProjectCard(ProjectCardProps{Href: "/studio/projects", Title: "Tidebreak Courier", Variant: "studio",
		ThumbURL: "/media/page.png", ThumbLabel: "Page thumbnail", Class: "project-library-item"})),
		"mv-pcard--studio", "project-library-item", `src="/media/page.png"`, `alt="Page thumbnail"`)
	card := render(t, CharacterCard(CharacterCardProps{FirstName: "Rin", Variant: "bible", ArtURL: "/media/ref.png",
		ArtLabel: "Rin's reference", Lock: -1, StatusLabel: "Reference images", StatusTone: ToneInfo}))
	has(t, card, "mv-ccard--bible", `src="/media/ref.png"`, "Reference images")
	if strings.Contains(card, "mv-lock__score") {
		t.Fatal("reference-only character claimed a trained score")
	}
}

func TestOverlaysAndStates(t *testing.T) {
	has(t, render(t, withText(Modal(ModalProps{ID: "m", Title: "Regenerate page 7?"}), "body")), `<dialog`, `aria-labelledby="m-title"`, `method="dialog"`, `aria-label="Close"`)
	has(t, render(t, Toast(ToastProps{Tone: ToneSuccess, Title: "Chapter 8 published", Href: "/s/x", Action: "View"})), "mv-toast--success", "ti-circle-check")
	has(t, render(t, Menu([]MenuItem{{Href: "/x", Label: "PDF", Current: true}, {Separator: true}, {Href: "/logout", Label: "Log out", Post: true}})),
		`role="menu"`, `aria-current="true"`, `method="post"`, "mv-menu__sep")
	has(t, render(t, withText(State(StateProps{SFX: "シーン…", JP: true, Title: "No projects yet", Art: "splash"}), "")), "mv-sfx--jp", "mv-tex-splash", `<h2 class="mv-state__title">`)
	// A state that is the whole page (the series age gate) carries the page's level-one heading.
	has(t, render(t, withText(State(StateProps{Title: "Harbour Ghosts is rated Mature 17+", H1: true, Boxed: true}), "")), `<h1 class="mv-state__title">`)
	has(t, render(t, Skeleton("Loading")), "mv-skeleton__block")
	has(t, render(t, Steps("Setup", []Step{{Label: "Admin", Done: true}, {Label: "Instance", Current: true}})), "is-done", `aria-current="step"`)
}

func TestHelpers(t *testing.T) {
	if Pct(0.624) != "mv-p-62" || Pct(2) != "mv-p-100" || Pct(-1) != "mv-p-0" || PctOf(1, 0) != "mv-p-0" {
		t.Fatal("Pct")
	}
	if Initials("Rin Asagiri") != "RA" || Initials("@jerry") != "JE" || Initials("") != "?" {
		t.Fatalf("Initials: %s %s", Initials("Rin Asagiri"), Initials("@jerry"))
	}
	if AvatarColor("a") != AvatarColor("a") {
		t.Fatal("AvatarColor must be stable")
	}
	if FormatInt(1204) != "1,204" || FormatInt(-10000) != "-10,000" {
		t.Fatal("FormatInt")
	}
	if Cls("a", "", " b ") != "a b" {
		t.Fatal("Cls")
	}
}

type templWriter = io.Writer

func TestFileDropAndVariationCaption(t *testing.T) {
	drop := render(t, FileDrop(FileDropProps{ID: "ref", Name: "file", Accept: "image/png", Prompt: "Choose an image", DescribedBy: "ref-hint"}))
	for _, want := range []string{`<label class="mv-filedrop" for="ref">`, `type="file"`, `name="file"`, `accept="image/png"`,
		`aria-describedby="ref-hint"`, "Choose an image", "ti-upload"} {
		if !strings.Contains(drop, want) {
			t.Errorf("file drop lacks %q: %s", want, drop)
		}
	}
	picker := render(t, VariationPicker("Pick a look", "hint", "index", "0", []Variation{
		{Value: "0", Label: "Sheet 1", Caption: "SHEET 1 · FRONT / SIDE / FACE", ImageURL: "/media/a.png", Lock: -1},
		{Value: "1", Label: "Sheet 2", ImageURL: "/media/b.png", Lock: -1},
	}))
	if strings.Count(picker, `class="mv-var__caption"`) != 1 || !strings.Contains(picker, "SHEET 1 · FRONT / SIDE / FACE") {
		t.Errorf("variation captions: %s", picker)
	}
}

func TestCharacterCardLockPercentNeverRoundsFailIntoPass(t *testing.T) {
	for v, want := range map[float64]string{0.895: "89%", 0.8999: "89%", 0.9: "90%", 0.9049: "90%", 0.956: "95%", 1: "100%", 0: "0%"} {
		if got := lockPercent(v); got != want {
			t.Errorf("lockPercent(%v) = %q, want %q", v, got, want)
		}
	}
}
