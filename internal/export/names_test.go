package export

import (
	"mime"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/lettering"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

func TestFilenameStemKeepsUnicodeLetters(t *testing.T) {
	for title, want := range map[string]string{
		"潮騒の配達人":                   "潮騒の配達人",
		"Tidebreak Courier":        "Tidebreak-Courier",
		"  Salt &   Static  ":      "Salt-&-Static",
		"등대 아래서":                   "등대-아래서",
		"灯塔下的信使":                   "灯塔下的信使",
		"नमस्ते दुनिया":            "नमस्ते-दुनिया",
		"Ça va? «Où» / ¿Qué?":      "Ça-va-«Où»-¿Qué",
		"a/b\\c:d*e?f\"g<h>i|j":    "abcdefghij",
		"tab\tnew\nline\u200bzero": "tab-new-linezero",
		"../../etc/passwd":         "etcpasswd",
		"???":                      "mangaveyra",
		"":                         "mangaveyra",
		"...":                      "mangaveyra",
	} {
		if got := filenameStem(title); got != want {
			t.Fatalf("filenameStem(%q) = %q, want %q", title, got, want)
		}
	}
	long := filenameStem(strings.Repeat("灯", 200))
	if n := len([]rune(long)); n != maxStemRunes {
		t.Fatalf("stem capped at %d runes, want %d", n, maxStemRunes)
	}
}

func TestContentDispositionHasASCIIFallback(t *testing.T) {
	for name, fallback := range map[string]string{
		"潮騒の配達人-ch-001.pdf":            "mangaveyra-ch-001.pdf",
		"Tidebreak-Courier-ch-002.cbz": "Tidebreak-Courier-ch-002.cbz",
		"Ça-va-ch-003.zip":             "a-va-ch-003.zip",
		"नमस्ते-ch-001.pdf":            "mangaveyra-ch-001.pdf",
	} {
		if got := asciiFilename(name); got != fallback {
			t.Fatalf("asciiFilename(%q) = %q, want %q", name, got, fallback)
		}
		header := contentDisposition(name, "")
		if !strings.Contains(header, `filename="`+fallback+`"`) || !strings.Contains(header, "filename*=UTF-8''") {
			t.Fatalf("header %q", header)
		}
		for _, r := range header {
			if r > 0x7e || r < 0x20 {
				t.Fatalf("header is not ASCII: %q", header)
			}
		}
		disposition, params, err := mime.ParseMediaType(header)
		if err != nil || disposition != "attachment" || params["filename"] != name {
			t.Fatalf("parsed %q: %v %v", header, params, err)
		}
	}
	if got := contentDisposition("x\".pdf", "evil\"\r\n.pdf"); strings.ContainsAny(got, "\r\n") || strings.Count(got, `"`) != 2 {
		t.Fatalf("fallback not sanitised: %q", got)
	}
}

func TestComicInfoLanguageAndManga(t *testing.T) {
	project := studio.Project{Language: "ja"}
	for req, want := range map[string]string{"": "ja", "ko": "ko", "zh-TW": "zh-Hant", "zh-Hans": "zh-Hans", "pt-BR": "pt", "hi": "hi", "x-old": "x-old"} {
		if got := exportLanguage(Request{Lang: req}, project); got != want {
			t.Fatalf("exportLanguage(%q) = %q, want %q", req, got, want)
		}
	}
	for _, lang := range pagedoc.ContentLanguages {
		if got := exportLanguage(Request{}, studio.Project{Language: lang}); got != lang {
			t.Fatalf("project language %s exported as %s", lang, got)
		}
	}
	if exportLanguage(Request{}, studio.Project{}) != "en" {
		t.Fatal("missing language defaults to en")
	}
	for format, want := range map[string]string{"manga_rtl": "YesAndRightToLeft", "webtoon": "Yes", "comic_ltr": "No", "": "No"} {
		if got := comicInfoManga(format); got != want {
			t.Fatalf("comicInfoManga(%q) = %q", format, got)
		}
	}
}

func TestCreditsPageInEveryContentLanguage(t *testing.T) {
	engine, err := lettering.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	s := &service{}
	seen := map[string]bool{}
	for _, lang := range pagedoc.ContentLanguages {
		p := prepared{project: studio.Project{ID: "p", Title: "潮騒の配達人 नाव 등대", Language: lang, PageWidth: 1600, PageHeight: 2400},
			chapter: studio.Chapter{ID: "c", Title: "Chapter One", AILabel: map[string]string{"script": "ai", "art": "ai_assisted", "lettering": "human"}}}
		page := s.credits(p)
		if err := page.Validate(); err != nil {
			t.Fatal(err)
		}
		b := page.Bubbles[0]
		text, ok := b.Texts[lang]
		if !ok || !strings.Contains(text.Text, "潮騒の配達人") || !strings.Contains(text.Text, "MangaVeyra") {
			t.Fatalf("%s credits: %+v", lang, b.Texts)
		}
		if seen[strings.SplitN(text.Text, "\n", 2)[0]+lang] {
			t.Fatal("duplicate copy")
		}
		seen[strings.SplitN(text.Text, "\n", 2)[0]+lang] = true
		l, err := engine.Layout(b, lang)
		if err != nil || l.Overflow || l.TextPath == "" {
			t.Fatalf("%s credits layout: overflow=%v %v", lang, l.Overflow, err)
		}
	}
	for _, c := range creditsCopies {
		for _, s := range append([]string{c.heading, c.footer}, c.roles[:]...) {
			if strings.Contains(strings.ToLower(s), "credit") || strings.Contains(strings.ToLower(s), "crédit") {
				t.Fatalf("credits page copy must not use the word credit: %q", s)
			}
		}
	}
}

func TestRestoreRequestShowsTheJobSettings(t *testing.T) {
	v := exportView{Page: pagedoc.Page{ID: "p2"}, Pages: []pagestore.PageSummary{{ID: "p1", Number: 1}, {ID: "p2", Number: 2}, {ID: "p3", Number: 3}, {ID: "p5", Number: 5}},
		Languages: []ui.Option{{Value: "ja"}, {Value: "ko"}}, Request: Request{Format: FormatPDF, TrimID: "b5", Lang: "ja"}}
	restoreRequest(&v, Request{Format: FormatCBZ, TrimID: "a5", DPI: 150, Lang: "ko", PageIDs: []string{"p5", "p1", "p2", "p3"}})
	if v.Request.Format != FormatCBZ || v.Request.TrimID != "a5" || v.Request.DPI != 150 || v.Request.Lang != "ko" || v.Request.CreditsPage || v.PageMode != "range" || v.Range != "1-3,5" {
		t.Fatalf("restored %+v %q %q", v.Request, v.PageMode, v.Range)
	}
	restoreRequest(&v, Request{Format: FormatPNG, Lang: "fr", PageIDs: []string{"p2"}, CreditsPage: true})
	if v.PageMode != "current" || v.Request.Lang != "ko" || !v.Request.CreditsPage {
		t.Fatalf("current page / unknown language: %+v %q", v.Request, v.PageMode)
	}
	restoreRequest(&v, Request{Format: FormatPDF})
	if v.PageMode != "all" || v.Range != "" {
		t.Fatalf("whole chapter: %q %q", v.PageMode, v.Range)
	}
}
