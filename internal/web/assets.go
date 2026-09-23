package web

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// static holds the committed assets (brand logos, PWA icons, sw.js) and, after
// `make web`, the Vite build in static/dist. The dist directory is git-ignored,
// so the embed pattern names the parent directory: the binary still builds
// (unstyled) when the frontend was not built.
//
//go:embed all:static
var static embed.FS

// StaticFS returns the embedded static directory (rooted at static/).
func StaticFS() fs.FS {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	return sub
}

// MainEntry is the Vite entry every page loads.
const MainEntry = "src/main.ts"

// manifestChunk is one entry of the Vite manifest (build.manifest).
type manifestChunk struct {
	File           string   `json:"file"`
	Name           string   `json:"name"`
	Src            string   `json:"src"`
	IsEntry        bool     `json:"isEntry"`
	IsDynamicEntry bool     `json:"isDynamicEntry"`
	Imports        []string `json:"imports"`
	DynamicImports []string `json:"dynamicImports"`
	CSS            []string `json:"css"`
	Assets         []string `json:"assets"`
}

// EntryAssets are the files a page needs for one Vite entry, as URLs.
type EntryAssets struct {
	// CSS lists stylesheets (the entry's and its static imports').
	CSS []string
	// Script is the entry module.
	Script string
	// Preload lists statically imported chunks (rel=modulepreload).
	Preload []string
	// Fonts lists font files worth preloading (the UI font's latin subset).
	Fonts []string
}

// Assets resolves Vite entries to hashed URLs under /static/dist/ by reading
// the Vite manifest. In dev mode it re-reads the manifest from disk when it
// changes so `npm run dev` (vite build --watch) works without restarting.
type Assets struct {
	fsys   fs.FS
	prefix string
	dev    bool
	log    *slog.Logger

	mu       sync.RWMutex
	manifest map[string]manifestChunk
	modTime  time.Time
	loadErr  error
}

// NewAssets reads the Vite manifest from fsys (rooted at the static directory).
// Missing manifests are not fatal: pages render without the bundle and a
// warning is logged once.
func NewAssets(fsys fs.FS, dev bool, log *slog.Logger) *Assets {
	if log == nil {
		log = slog.Default()
	}
	a := &Assets{fsys: fsys, prefix: "/static/", dev: dev, log: log}
	a.load()
	if a.loadErr != nil {
		log.Warn("web: frontend bundle missing; run `make web` to build it", "err", a.loadErr)
	}
	return a
}

func (a *Assets) load() {
	a.mu.Lock()
	defer a.mu.Unlock()
	st, statErr := fs.Stat(a.fsys, "dist/manifest.json")
	if statErr == nil && a.manifest != nil && !st.ModTime().After(a.modTime) {
		return
	}
	b, err := fs.ReadFile(a.fsys, "dist/manifest.json")
	if err != nil {
		a.loadErr = err
		if a.manifest == nil {
			a.manifest = map[string]manifestChunk{}
		}
		return
	}
	m := map[string]manifestChunk{}
	if err := json.Unmarshal(b, &m); err != nil {
		a.loadErr = fmt.Errorf("web: parse vite manifest: %w", err)
		return
	}
	a.manifest, a.loadErr = m, nil
	if statErr == nil {
		a.modTime = st.ModTime()
	}
}

// Ready reports whether the frontend bundle was found.
func (a *Assets) Ready() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.loadErr == nil && len(a.manifest) > 0
}

// Entry returns the CSS, script and preloads for a Vite entry such as "src/main.ts".
func (a *Assets) Entry(name string) EntryAssets {
	if a.dev {
		a.load()
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	var out EntryAssets
	c, ok := a.manifest[name]
	if !ok {
		return out
	}
	out.Script = a.url(c.File)
	seen := map[string]bool{}
	var walk func(key string, top bool)
	walk = func(key string, top bool) {
		if seen[key] {
			return
		}
		seen[key] = true
		ch, ok := a.manifest[key]
		if !ok {
			return
		}
		for _, css := range ch.CSS {
			out.CSS = append(out.CSS, a.url(css))
		}
		if !top {
			out.Preload = append(out.Preload, a.url(ch.File))
		}
		for _, imp := range ch.Imports {
			walk(imp, false)
		}
	}
	walk(name, true)
	for _, f := range c.Assets {
		base := path.Base(f)
		if strings.HasPrefix(base, "hanken-grotesk-latin-400-normal") || strings.HasPrefix(base, "hanken-grotesk-latin-600-normal") {
			out.Fonts = append(out.Fonts, a.url(f))
		}
	}
	sort.Strings(out.Fonts)
	return out
}

// URL returns the public URL of a built file by its source path (e.g. a font), or "".
func (a *Assets) URL(src string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if c, ok := a.manifest[src]; ok {
		return a.url(c.File)
	}
	return ""
}

func (a *Assets) url(file string) string { return a.prefix + "dist/" + strings.TrimPrefix(file, "/") }

// StaticHandler serves the static directory at /static/. Hashed files under
// dist/assets/ are cached for a year; everything else for an hour (no-cache in dev).
func StaticHandler(fsys fs.FS, dev bool) http.Handler {
	files := http.FileServer(http.FS(fsys))
	return http.StripPrefix("/static", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/" || strings.HasSuffix(p, "/") || strings.Contains(p, "..") {
			http.NotFound(w, r)
			return
		}
		if _, err := fs.Stat(fsys, strings.TrimPrefix(p, "/")); errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		switch {
		case dev:
			w.Header().Set("Cache-Control", "no-cache")
		case strings.HasPrefix(p, "/dist/assets/"):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		default:
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		if strings.HasSuffix(p, ".woff2") {
			w.Header().Set("Content-Type", "font/woff2")
		}
		files.ServeHTTP(w, r)
	}))
}

// devStaticFS prefers the on-disk static directory in dev mode (so a watching
// Vite build is picked up without rebuilding the binary).
func devStaticFS(dev bool) fs.FS {
	if dev {
		for _, dir := range []string{"internal/web/static", "../internal/web/static", "../../internal/web/static"} {
			if st, err := os.Stat(path.Join(dir, "dist", "manifest.json")); err == nil && !st.IsDir() {
				return os.DirFS(dir)
			}
		}
	}
	return StaticFS()
}
