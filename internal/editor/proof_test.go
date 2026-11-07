package editor_test

import (
	"encoding/json"
	"html"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestEditorNoJSProofIsCompleteAndLocalized(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		_, c, p, ch, base := fixture(t, ta)
		page := doc(t, c.PostJSON(base+"/pages", map[string]string{"layout_id": "reveal-5"}), 201).Page
		// Server-created layers carry English default names.
		for i := range page.Panels {
			for j := range page.Panels[i].Layers {
				switch page.Panels[i].Layers[j].Kind {
				case "background":
					page.Panels[i].Layers[j].Name = "Background"
				case "lettering":
					page.Panels[i].Layers[j].Name = "Lettering"
				}
			}
		}
		page.Panels[0].Layers[0].Name = "Background · rooftop"
		doc(t, save(c, base+"/pages/"+page.ID, page, page.Version), 200)
		doc(t, c.PostJSON(base+"/pages", map[string]string{"layout_id": "splash"}), 201)
		editor := "/studio/p/" + p.ID + "/chapters/" + ch.ID + "/edit"

		r := c.Get(editor)
		body := html.UnescapeString(string(r.Body))
		if r.StatusCode != 200 {
			t.Fatalf("editor=%d", r.StatusCode)
		}
		for _, want := range []string{
			`class="mv-ed__proof"`, `preserveAspectRatio="xMidYMid meet"`,
			"Ch. 1 · The Lighthouse Debt", "Page 1 of 2", "Manga · right to left", "B5 · 182 × 257 mm",
			`class="mv-ed__help mv-ed__loading"`, `<noscript><p class="mv-ed__help mv-ed__nojs">`,
			`aria-label="Page 1"`, `aria-label="Page 2"`, `class="mv-ed__thumb-number" aria-hidden="true"`,
			"Panel 1.1", "Background · rooftop", "Lettering",
			`<span class="mv-ed__footer-end">Self-host · 127.0.0.1</span>`, "/pages/" + page.ID + "/thumb?v=",
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("no-JS editor is missing %q", want)
			}
		}
		thumb := c.Get(base + "/pages/" + page.ID + "/thumb?v=2")
		if thumb.StatusCode != 200 || thumb.Header.Get("Content-Type") != "image/png" || len(thumb.Body) < 100 {
			t.Fatalf("thumbnail=%d %s", thumb.StatusCode, thumb.Header.Get("Content-Type"))
		}
		for _, raw := range []string{"<span>manga_rtl</span>", "<span>b5</span>", "<strong>1 · The Lighthouse Debt</strong>", `<span class="mv-ed__footer-end">Lettering`} {
			if strings.Contains(body, raw) {
				t.Fatalf("no-JS editor shows raw value %q", raw)
			}
		}

		c.Header.Set("Accept-Language", "ja")
		ja := html.UnescapeString(string(c.Get(editor).Body))
		for _, want := range []string{"第1話 · The Lighthouse Debt", "マンガ・右から左", "B5判 · 182 × 257 mm", "コマ 1.1", "背景", "文字", "Background · rooftop"} {
			if !strings.Contains(ja, want) {
				t.Fatalf("Japanese no-JS editor is missing %q", want)
			}
		}
		for _, english := range []string{">Lettering<", ">Background<"} {
			if strings.Contains(ja, english) {
				t.Fatalf("Japanese no-JS editor shows the stored English layer name %q", english)
			}
		}
	})
}

func TestEditorBootstrapCarriesDesignContext(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		_, c, _, ch, base := fixture(t, ta)
		page := doc(t, c.PostJSON(base+"/pages", map[string]string{"layout_id": "reveal-5"}), 201).Page
		var boot struct {
			Chapters []struct {
				ID   string `json:"id"`
				Href string `json:"href"`
			} `json:"chapters"`
			Styles []struct {
				ID    string `json:"id"`
				Label string `json:"label"`
			} `json:"styles"`
			Print struct {
				FormatLabel string  `json:"format_label"`
				SizeLabel   string  `json:"size_label"`
				WidthMM     float64 `json:"width_mm"`
			} `json:"print"`
			Compute struct {
				Host string `json:"host"`
			} `json:"compute"`
			Layouts []struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				Suggested bool   `json:"suggested"`
			} `json:"layouts"`
		}
		r := c.Get(base)
		if err := json.Unmarshal(r.Body, &boot); err != nil {
			t.Fatal(err)
		}
		if len(boot.Chapters) != 1 || boot.Chapters[0].ID != ch.ID || !strings.HasSuffix(boot.Chapters[0].Href, "/chapters/"+ch.ID+"/edit") {
			t.Fatalf("chapters=%#v", boot.Chapters)
		}
		styled := false
		for _, s := range boot.Styles {
			styled = styled || s.ID == "seinen_bw" && s.Label == "Seinen · B/W screentone"
		}
		if !styled {
			t.Fatalf("styles=%#v", boot.Styles)
		}
		if boot.Print.FormatLabel != "Manga · right to left" || boot.Print.SizeLabel != "B5 · 182 × 257 mm" || boot.Print.WidthMM != 182 || boot.Compute.Host == "" {
			t.Fatalf("print=%#v compute=%#v", boot.Print, boot.Compute)
		}
		suggested := 0
		for _, l := range boot.Layouts {
			if l.Suggested {
				suggested++
			}
			if l.ID == "grid-9" && (l.Suggested || l.Name == "Custom grid") {
				t.Fatalf("the 3 × 3 preset is presented as the custom grid: %#v", l)
			}
		}
		if suggested != 7 {
			t.Fatalf("suggested=%d want 7", suggested)
		}
		var picker struct {
			Layouts []struct {
				ID        string `json:"id"`
				Suggested bool   `json:"suggested"`
			} `json:"layouts"`
		}
		if err := json.Unmarshal(c.Get(base+"/pages/"+page.ID+"/layouts").Body, &picker); err != nil || len(picker.Layouts) == 0 {
			t.Fatalf("layouts endpoint: %v", err)
		}
		// Suggestions follow the page: a one-panel page is offered layouts
		// close to one panel, best fit first, and a blank page says so.
		splash := doc(t, c.PostJSON(base+"/pages", map[string]string{"layout_id": "splash"}), 201).Page
		var fit struct {
			Layouts []struct {
				ID        string `json:"id"`
				Panels    int    `json:"panels"`
				Suggested bool   `json:"suggested"`
				Rank      int    `json:"rank"`
			} `json:"layouts"`
			Blank bool `json:"blank"`
		}
		if err := json.Unmarshal(c.Get(base+"/pages/"+splash.ID+"/layouts").Body, &fit); err != nil {
			t.Fatal(err)
		}
		best := ""
		for _, l := range fit.Layouts {
			if l.Suggested && (l.Panels > 3 || l.Rank == 0) {
				t.Fatalf("a %d-panel layout was suggested for a one-panel page: %+v", l.Panels, l)
			}
			if l.Rank == 1 {
				best = l.ID
			}
		}
		if best != "splash" || fit.Blank {
			t.Fatalf("best=%q blank=%v", best, fit.Blank)
		}
		blank := doc(t, c.PostJSON(base+"/pages", map[string]string{"layout_id": ""}), 201).Page
		if err := json.Unmarshal(c.Get(base+"/pages/"+blank.ID+"/layouts").Body, &fit); err != nil || !fit.Blank {
			t.Fatalf("blank page not reported: %v", err)
		}
		var route struct {
			Available   bool   `json:"available"`
			ModelName   string `json:"model_name"`
			Placeholder bool   `json:"placeholder"`
		}
		if err := json.Unmarshal(c.Get(base+"/pages/"+page.ID+"/route?kind=panel&panel_id="+page.Panels[0].ID).Body, &route); err != nil || !route.Available || route.ModelName != "Placeholder art" || !route.Placeholder {
			t.Fatalf("route=%+v %v", route, err)
		}
		var queue struct {
			Compute struct {
				Host string `json:"host"`
			} `json:"compute"`
			Jobs []any `json:"jobs"`
		}
		if err := json.Unmarshal(c.Get(base+"/queue").Body, &queue); err != nil || queue.Compute.Host == "" || queue.Jobs == nil {
			t.Fatalf("queue=%#v %v", queue, err)
		}
	})
}
