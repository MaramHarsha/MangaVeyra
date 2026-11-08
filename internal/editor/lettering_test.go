package editor_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// heavyDraft fills page with n maximum-size bubbles: 2000 runes of Japanese
// at size 1000 with auto-fit, the most expensive input validation allows.
func heavyDraft(page pagedoc.Page, n int) pagedoc.Page {
	text := []rune(strings.Repeat("灯台の光が、海を照らした。「急げ！」", 120))[:pagedoc.MaxTextRunes]
	page.Bubbles = nil
	for i := 0; i < n; i++ {
		page.Bubbles = append(page.Bubbles, pagedoc.Bubble{ID: fmt.Sprintf("heavy%02d", i), PanelID: page.Panels[0].ID, Kind: pagedoc.BubbleSpeech,
			Shape: pagedoc.ShapeEllipse, Order: i, Box: pagedoc.Rect{X: 0, Y: 0, W: float64(page.Width), H: float64(page.Height)},
			Style: pagedoc.BubbleStyle{Size: 1000, AutoFit: true}, Texts: map[string]pagedoc.BubbleText{"ja": {Text: string(text)}}})
	}
	return page
}

func previewDraft(page pagedoc.Page) pagedoc.Page {
	page.Bubbles = []pagedoc.Bubble{{ID: "draft", PanelID: page.Panels[0].ID, Kind: pagedoc.BubbleSpeech, Shape: pagedoc.ShapeEllipse,
		Box: pagedoc.Rect{X: 300, Y: 60, W: 200, H: 120}, Style: pagedoc.BubbleStyle{AutoFit: true},
		Texts: map[string]pagedoc.BubbleText{"ja": {Text: "海へ行こう"}}}}
	return page
}

// Previewing a draft needs art or lettering rights (the editor previews
// after an edit); reflowing needs lettering rights. Read-only members get
// the not-found answer every other forbidden action gets.
func TestLetteringPreviewNeedsEditRights(t *testing.T) {
	each(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		_, owner, p, _, base := fixture(t, ta)
		page := doc(t, owner.PostJSON(base+"/pages", map[string]string{"layout_id": "splash"}), 201).Page
		path := base + "/pages/" + page.ID + "/lettering"
		draft := previewDraft(page)
		member := func(role string) *testutil.Client {
			u, c := ta.UserClient(t, auth.RoleCreator)
			if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO project_members(project_id,user_id,role) VALUES(?,?,?)`, p.ID, u.ID, role); err != nil {
				t.Fatal(err)
			}
			return c
		}
		for _, tc := range []struct {
			name           string
			c              *testutil.Client
			preview, reflo int
		}{
			{"owner", owner, 200, 200},
			{studio.RoleEditor, member(studio.RoleEditor), 200, 200},
			{studio.RoleLetterer, member(studio.RoleLetterer), 200, 200},
			{studio.RoleArtist, member(studio.RoleArtist), 200, 404},
			{studio.RoleWriter, member(studio.RoleWriter), 404, 404},
			{studio.RoleViewer, member(studio.RoleViewer), 404, 404},
		} {
			for _, reflow := range []bool{false, true} {
				want := map[bool]int{false: tc.preview, true: tc.reflo}[reflow]
				r := tc.c.PostJSON(path, map[string]any{"page": draft, "reflow": reflow})
				if r.StatusCode != want {
					t.Fatalf("%s reflow=%v: HTTP %d, want %d: %.300s", tc.name, reflow, r.StatusCode, want, r.Body)
				}
				if want != 200 {
					continue
				}
				var out struct {
					Lettering []struct {
						TextPath string `json:"text_path"`
					} `json:"lettering"`
				}
				if err := json.Unmarshal(r.Body, &out); err != nil || len(out.Lettering) != 1 || out.Lettering[0].TextPath == "" {
					t.Fatalf("%s: preview %v %.300s", tc.name, err, r.Body)
				}
			}
		}
	})
}

// The heaviest drafts validation allows used to take seconds per bubble
// behind the engine's single mutex; now a preview is bounded.
func TestLetteringPreviewOfMaximumDraftIsBounded(t *testing.T) {
	ta := testutil.NewTestApp(t)
	_, c, _, _, base := fixture(t, ta)
	page := doc(t, c.PostJSON(base+"/pages", map[string]string{"layout_id": "splash"}), 201).Page
	start := time.Now()
	r := c.PostJSON(base+"/pages/"+page.ID+"/lettering", map[string]any{"page": heavyDraft(page, 8), "reflow": false})
	took := time.Since(start)
	if r.StatusCode != 200 {
		t.Fatalf("HTTP %d: %.300s", r.StatusCode, r.Body)
	}
	t.Logf("8 maximum bubbles previewed in %v (%d bytes)", took, len(r.Body))
	if took > 10*time.Second {
		t.Fatalf("preview took %v", took)
	}
}

// An abandoned preview stops laying out: the editor aborts a preview when
// the next keystroke supersedes it.
func TestLetteringPreviewStopsWhenAbandoned(t *testing.T) {
	ta := testutil.NewTestApp(t)
	_, c, _, _, base := fixture(t, ta)
	page := doc(t, c.PostJSON(base+"/pages", map[string]string{"layout_id": "splash"}), 201).Page
	body, err := json.Marshal(map[string]any{"page": heavyDraft(page, pagedoc.MaxBubbles), "reflow": false})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(ta.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, base+"/pages/"+page.ID+"/lettering", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, cookie := range c.Client.Jar.Cookies(u) {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	start := time.Now()
	ta.Handler.ServeHTTP(w, req)
	took := time.Since(start)
	// 64 such bubbles take over a second to lay out and encode; the handler
	// returns soon after the context ends, without a body.
	t.Logf("abandoned after %v: HTTP %d, %d bytes", took, w.Code, w.Body.Len())
	if took > 750*time.Millisecond || w.Body.Len() != 0 {
		t.Fatalf("abandoned preview ran for %v and wrote %d bytes", took, w.Body.Len())
	}
	// The editor keeps working.
	if r := c.PostJSON(base+"/pages/"+page.ID+"/lettering", map[string]any{"page": previewDraft(page), "reflow": false}); r.StatusCode != 200 {
		t.Fatalf("preview after abandon: HTTP %d", r.StatusCode)
	}
}
