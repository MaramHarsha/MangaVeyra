package storage

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLocalStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := PutBytes(ctx, s, "projects/p1/pages/a.png", []byte("hello"), "image/png"); err != nil {
		t.Fatal(err)
	}
	obj, info, err := s.Get(ctx, "projects/p1/pages/a.png")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(obj)
	obj.Close()
	if string(b) != "hello" || info.Size != 5 || info.ContentType != "image/png" {
		t.Fatalf("got %q %+v", b, info)
	}
	if err := PutBytes(ctx, s, "projects/p1/b.json", []byte("{}"), ""); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx, "projects/p1/")
	if err != nil || len(list) != 2 {
		t.Fatalf("list %v %v", list, err)
	}
	if err := s.Delete(ctx, "projects/p1/pages/a.png"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stat(ctx, "projects/p1/pages/a.png"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stat after delete: %v", err)
	}
	if err := s.Delete(ctx, "missing/key.png"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	for _, bad := range []string{"../x", "/abs", "a/../b", "a//b", ".hidden", "a/.git/x", "", "dir/"} {
		if err := PutBytes(ctx, s, bad, []byte("x"), ""); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("key %q accepted: %v", bad, err)
		}
	}
	// size mismatch is rejected
	if err := s.Put(ctx, "x.txt", strings.NewReader("abc"), 10, ""); err == nil {
		t.Fatal("size mismatch accepted")
	}
}

func TestSignedMediaURLs(t *testing.T) {
	ctx := context.Background()
	s, _ := NewLocal(t.TempDir())
	_ = PutBytes(ctx, s, "covers/c.png", pngBytes(t, 10, 10), "image/png")
	_ = PutBytes(ctx, s, "evil/x.svg", []byte(`<svg onload="alert(1)"/>`), "image/svg+xml")
	signer := NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	mux := http.NewServeMux()
	mux.Handle("GET /media/{key...}", MediaHandler(s, signer))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	u := signer.URL("covers/c.png", time.Minute)
	if u != signer.URL("covers/c.png", time.Minute) {
		t.Fatal("URLs should be stable within an expiry bucket")
	}
	resp := get(t, srv.URL+u)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("signed get: %d %v", resp.StatusCode, resp.Header)
	}
	// tampered key / sig / missing params
	for _, bad := range []string{
		strings.Replace(u, "covers/c.png", "covers/d.png", 1),
		u[:len(u)-3] + "AAA",
		"/media/covers/c.png",
	} {
		if r := get(t, srv.URL+bad); r.StatusCode != 403 {
			t.Errorf("%s → %d", bad, r.StatusCode)
		}
	}
	// expired
	past := NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	past.SetClock(func() time.Time { return time.Now().Add(-2 * time.Hour) })
	if r := get(t, srv.URL+past.URL("covers/c.png", time.Minute)); r.StatusCode != 403 {
		t.Fatalf("expired → %d", r.StatusCode)
	}
	// SVG is never served inline
	r := get(t, srv.URL+signer.URL("evil/x.svg", time.Minute))
	if r.Header.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(r.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("svg served inline: %v", r.Header)
	}
	// missing object
	if r := get(t, srv.URL+signer.URL("nope.png", time.Minute)); r.StatusCode != 404 {
		t.Fatalf("missing → %d", r.StatusCode)
	}
}

func get(t *testing.T, u string) *http.Response {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, x%h, color.NRGBA{R: 200, A: 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImageHelpers(t *testing.T) {
	data := pngBytes(t, 400, 200)
	if ct := DetectContentType(data); ct != "image/png" {
		t.Fatalf("sniff %s", ct)
	}
	ct, r, err := Sniff(bytes.NewReader(data))
	if err != nil || ct != "image/png" {
		t.Fatal(ct, err)
	}
	all, _ := io.ReadAll(r)
	if !bytes.Equal(all, data) {
		t.Fatal("sniff consumed data")
	}
	if err := CheckType("image/png", ImageTypes...); err != nil {
		t.Fatal(err)
	}
	if err := CheckType("text/html; charset=utf-8", ImageTypes...); !errors.Is(err, ErrUnsupportedType) {
		t.Fatal("html accepted as image")
	}
	if _, _, err := DecodeImage(data, ImageLimits{MaxWidth: 100, MaxHeight: 100, MaxPixels: 10000}); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("limits not enforced: %v", err)
	}
	th, thCT, err := ThumbnailBytes(data, 100, 100, DefaultImageLimits)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(th))
	if err != nil || cfg.Width != 100 || cfg.Height != 50 {
		t.Fatalf("thumb %v %+v (%s)", err, cfg, thCT)
	}
	if w, h := Fit(100, 100, 400, 400); w != 100 || h != 100 {
		t.Fatal("Fit upscaled")
	}
	if k := NewKey("jobs/x", "image/png"); !strings.HasPrefix(k, "jobs/x/") || !strings.HasSuffix(k, ".png") || ValidateKey(k) != nil {
		t.Fatal(k)
	}
}
