package httpx

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

// templ components are used directly as httpx.Component by the web layer.
var _ Component = templ.Component(nil)

func TestRenderFragmentAndLayout(t *testing.T) {
	body := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		_, err := io.WriteString(w, "<p>panel</p>")
		return err
	})
	SetHooks(Hooks{Layout: func(r *http.Request, c Component) Component {
		return ComponentFunc(func(ctx context.Context, w io.Writer) error {
			io.WriteString(w, "<main>")
			if err := c.Render(ctx, w); err != nil {
				return err
			}
			_, err := io.WriteString(w, "</main>")
			return err
		})
	}})
	defer SetHooks(Hooks{})
	rec := httptest.NewRecorder()
	if err := Render(rec, httptest.NewRequest("GET", "/", nil), 200, body); err != nil {
		t.Fatal(err)
	}
	if rec.Body.String() != "<main><p>panel</p></main>" {
		t.Fatalf("full page: %q", rec.Body)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("HX-Request", "true")
	rec = httptest.NewRecorder()
	Render(rec, req, 200, body)
	if rec.Body.String() != "<p>panel</p>" || !strings.Contains(rec.Header().Get("Vary"), "HX-Request") {
		t.Fatalf("fragment: %q", rec.Body)
	}
}
