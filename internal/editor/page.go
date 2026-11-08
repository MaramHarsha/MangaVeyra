package editor

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
)

func editorMessages(ctx context.Context) map[string]string {
	out := map[string]string{}
	for _, file := range []string{"en/editor.json", "en/collab.json"} {
		raw, _ := fs.ReadFile(i18n.Embedded(), file)
		catalog := map[string]string{}
		_ = json.Unmarshal(raw, &catalog)
		// Editor keys drop their prefix; collaboration keys keep "collab.".
		for key := range catalog {
			out[strings.TrimPrefix(key, "editor.")] = i18n.T(ctx, key)
		}
	}
	return out
}
func (m *Module) editorPage(w http.ResponseWriter, r *http.Request) error {
	res, err := m.resource(r, false)
	if err != nil {
		return mapped(r, err)
	}
	data, err := m.data(r, res)
	if err != nil {
		return mapped(r, err)
	}
	props := map[string]any{"initial": data, "mode": r.URL.Query().Get("mode"), "tool": r.URL.Query().Get("tool"), "panel": r.URL.Query().Get("panel"), "job": r.URL.Query().Get("job")}
	raw, err := json.Marshal(props)
	if err != nil {
		return err
	}
	c := m.ui.Chrome(w, r, web.PageOpts{Title: res.Chapter.Title, Section: layout.SectionStudio, Nav: layout.NavProjects})
	w.Header().Set("Cache-Control", "no-store")
	return web.Render(w, r, 200, EditorDocument(c, data, string(raw)), nil)
}
func polygonString(points []pagedoc.Point) string {
	parts := make([]string, 0, len(points))
	for _, p := range points {
		parts = append(parts, fmt.Sprintf("%g,%g", p.X, p.Y))
	}
	return strings.Join(parts, " ")
}
func rotation(angle, x, y float64) string { return fmt.Sprintf("rotate(%g %g %g)", angle, x, y) }
func rectView(p pagedoc.Page) string      { return fmt.Sprintf("0 0 %d %d", p.Width, p.Height) }
func pageHref(data bootstrapData, number int) string {
	return fmt.Sprintf("/studio/p/%s/chapters/%s/edit?page=%d", data.Project.ID, data.Chapter.ID, number)
}
func exportHref(data bootstrapData) string {
	return fmt.Sprintf("/studio/p/%s/chapters/%s/export", data.Project.ID, data.Chapter.ID)
}
func label(ctx context.Context, key string, args ...any) string {
	return i18n.T(ctx, "editor."+key, args...)
}

var _ core.Initer = (*Module)(nil)
