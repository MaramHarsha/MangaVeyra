package ai_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func post(c *testutil.Client, path string, v url.Values) *testutil.Response {
	return c.Do(http.MethodPost, path, strings.NewReader(v.Encode()), "application/x-www-form-urlencoded")
}

func htmx(c *testutil.Client, path string, v url.Values) *testutil.Response {
	c.Header.Set("HX-Request", "true")
	defer c.Header.Del("HX-Request")
	return post(c, path, v)
}

func contains(t *testing.T, r *testutil.Response, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(string(r.Body), p) {
			t.Fatalf("response %d lacks %q:\n%.3000s", r.StatusCode, p, r.Body)
		}
	}
}

func lacks(t *testing.T, r *testutil.Response, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if strings.Contains(string(r.Body), p) {
			t.Fatalf("response must not contain %q", p)
		}
	}
}

func location(t *testing.T, r *testutil.Response, prefix string) string {
	t.Helper()
	loc := r.Header.Get("Location")
	if r.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, prefix) {
		t.Fatalf("want 303 → %s…, got %d %q\n%.1500s", prefix, r.StatusCode, loc, r.Body)
	}
	return loc
}

// newApp starts an app where providers may use loopback addresses (httptest servers).
func newApp(t *testing.T, opts ...testutil.Option) (*testutil.TestApp, *ai.Service) {
	t.Helper()
	opts = append([]testutil.Option{testutil.WithEnv("MV_ALLOW_PRIVATE_PROVIDER_URLS", "true")}, opts...)
	ta := testutil.NewTestApp(t, opts...)
	svc := core.MustUse[*ai.Service](ta.Deps)
	svc.Wait = 10_000_000 // 10 ms
	return ta, svc
}

// dialects returns the dialects to run a test on (Postgres when MV_TEST_POSTGRES_URL is set).
func dialects() []string {
	if os.Getenv("MV_TEST_POSTGRES_URL") != "" {
		return []string{"sqlite", "postgres"}
	}
	return []string{"sqlite"}
}

func tinyPNG(t testing.TB) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 12))
	for y := 0; y < 12; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 20), uint8(y * 10), 180, 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

type settingsInstance = settings.Instance
