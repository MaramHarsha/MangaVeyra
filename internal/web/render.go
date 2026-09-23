package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/a-h/templ"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
)

// Render writes an HTML page. HTMX requests (except boosted navigations) get
// fragment when it is not nil; everything else gets full. Output is buffered
// so a render error still becomes a clean error page.
func Render(w http.ResponseWriter, r *http.Request, status int, full, fragment templ.Component) error {
	c := full
	if fragment != nil && core.IsHTMX(r) && r.Header.Get("HX-Boosted") != "true" {
		c = fragment
	}
	var buf bytes.Buffer
	if err := c.Render(r.Context(), &buf); err != nil {
		return fmt.Errorf("web: render: %w", err)
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	if h.Get("Cache-Control") == "" {
		h.Set("Cache-Control", "no-store")
	}
	h.Add("Vary", "HX-Request")
	w.WriteHeader(status)
	_, err := buf.WriteTo(w)
	return err
}

// Fragment renders a component without any layout (HTMX partials, tests).
func Fragment(w http.ResponseWriter, r *http.Request, status int, c templ.Component) error {
	return Render(w, r, status, c, nil)
}

// Toast asks the browser to show a toast after this response (HTMX requests
// only; use Flash before redirects of normal requests). kind is success,
// error, warning or info.
func Toast(w http.ResponseWriter, kind, title, body string) {
	Trigger(w, "mv:toast", map[string]string{"kind": kind, "title": title, "body": body})
}

// Trigger merges an event into the HX-Trigger response header.
func Trigger(w http.ResponseWriter, event string, detail any) {
	events := map[string]any{}
	if cur := w.Header().Get("HX-Trigger"); cur != "" {
		if err := json.Unmarshal([]byte(cur), &events); err != nil {
			for _, name := range strings.Split(cur, ",") {
				if name = strings.TrimSpace(name); name != "" {
					events[name] = nil
				}
			}
		}
	}
	events[event] = detail
	b, err := json.Marshal(events)
	if err != nil {
		return
	}
	w.Header().Set("HX-Trigger", string(b))
}

// flashCookie carries toasts across a redirect for non-HTMX form posts.
const flashCookie = "mv_flash"

// Flash queues a toast shown on the next full page render (after a redirect).
// For HTMX requests it sends the toast immediately instead.
func Flash(w http.ResponseWriter, r *http.Request, kind, title, body string) {
	if core.IsHTMX(r) {
		Toast(w, kind, title, body)
		return
	}
	list := readFlash(r)
	list = append(list, layout.Flash{Kind: kind, Title: title, Body: body})
	if len(list) > 3 {
		list = list[len(list)-3:]
	}
	b, _ := json.Marshal(list)
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: base64.RawURLEncoding.EncodeToString(b), Path: "/",
		MaxAge: 60, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil})
}

func readFlash(r *http.Request) []layout.Flash {
	c, err := r.Cookie(flashCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil
	}
	var list []layout.Flash
	if json.Unmarshal(b, &list) != nil {
		return nil
	}
	return list
}

// takeFlash returns queued toasts and clears the cookie.
func takeFlash(w http.ResponseWriter, r *http.Request) []layout.Flash {
	list := readFlash(r)
	if list != nil && w != nil {
		http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	}
	return list
}

// PrefsEvent is the payload of the "mv:prefs" HX-Trigger event main.ts
// applies to <html> so theme, motion and text size change instantly.
type PrefsEvent struct {
	Theme          string `json:"theme,omitempty"`
	Motion         string `json:"motion,omitempty"`
	TextSize       int    `json:"textSize,omitempty"`
	Textures       *bool  `json:"textures,omitempty"`
	StrongFocus    *bool  `json:"strongFocus,omitempty"`
	UnderlineLinks *bool  `json:"underlineLinks,omitempty"`
	DyslexiaFont   *bool  `json:"dyslexiaFont,omitempty"`
}

// TriggerPrefs tells the page to apply new interface preferences.
func TriggerPrefs(w http.ResponseWriter, p PrefsEvent) { Trigger(w, "mv:prefs", p) }

type uiKey struct{}

// withUI stores the web layer in the request context (used by the hooks).
func withUI(ctx context.Context, u *UI) context.Context { return context.WithValue(ctx, uiKey{}, u) }

func uiFrom(ctx context.Context) *UI {
	if u, ok := ctx.Value(uiKey{}).(*UI); ok {
		return u
	}
	return current.Load()
}
