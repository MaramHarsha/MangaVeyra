package ui

import (
	"context"
	"net/http"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
)

// Ready reports whether href leads to a page this server serves. Components
// render links to destinations that are not built yet as disabled controls
// with a "Soon" tag, so navigation can keep the design's items without ever
// leading to a 404 (ADR 0004 §7). External links, fragments and components
// rendered without a router count as ready.
func Ready(ctx context.Context, href string) bool {
	return ReadyMethod(ctx, http.MethodGet, href)
}

// ReadyMethod is Ready for a form target with an explicit method.
func ReadyMethod(ctx context.Context, method, href string) bool {
	if !strings.HasPrefix(href, "/") || strings.HasPrefix(href, "//") {
		return true
	}
	path := href
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	return httpx.Serves(ctx, method, path)
}
