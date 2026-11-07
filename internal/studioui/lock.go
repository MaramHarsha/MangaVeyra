package studioui

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/a-h/templ"
)

func (m *Module) lockFragment(w http.ResponseWriter, r *http.Request) error {
	v, _, err := m.bibleBase(r, "detail")
	if err != nil {
		return pageObjectError(err)
	}
	if err := m.loadBibleCharacter(r, &v, r.PathValue("character")); err != nil {
		return pageObjectError(err)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return BibleLockCard(v).Render(r.Context(), w)
}
func (m *Module) lockAction(w http.ResponseWriter, r *http.Request) error {
	v, actor, err := m.bibleBase(r, "detail")
	if err != nil {
		return pageObjectError(err)
	}
	if !v.CanGenerate || !v.CanArt {
		return core.ErrNotFound()
	}
	if err := m.loadBibleCharacter(r, &v, r.PathValue("character")); err != nil {
		return pageObjectError(err)
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := r.ParseForm(); err != nil {
		return err
	}
	rev, err := time.Parse(time.RFC3339Nano, r.FormValue("revision"))
	if err != nil {
		return pageObjectError(studio.ErrInvalid)
	}
	switch r.PathValue("action") {
	case "train":
		_, err = m.control.EnqueueTraining(r.Context(), actor, v.Project.ID, v.Character.ID, rev)
	case "score":
		_, err = m.control.EnqueueScoring(r.Context(), actor, v.Project.ID, v.Character.ID, rev)
	default:
		return core.ErrNotFound()
	}
	if err != nil {
		if errors.Is(err, gen.ErrDeferredFeature) {
			v.Error = i18n.T(r.Context(), "bible.lock.worker_needed")
		} else if errors.Is(err, studio.ErrLocked) {
			v.Error = i18n.T(r.Context(), "bible.lock.target_locked")
		} else if errors.Is(err, studio.ErrConflict) {
			v.Error = i18n.T(r.Context(), "bible.lock.changed")
		} else if errors.Is(err, studio.ErrInvalid) {
			v.Error = i18n.T(r.Context(), "bible.lock.not_ready")
		} else {
			return pageObjectError(err)
		}
		return m.renderBible(w, r, http.StatusConflict, v)
	}
	core.Redirect(w, r, characterPath(v.Project.ID, v.Character.ID))
	return nil
}
func lockCardAttrs(v biblePage) templ.Attributes {
	if v.Lock.JobID != "" {
		return templ.Attributes{"hx-get": characterPath(v.Project.ID, v.Character.ID) + "/lock", "hx-trigger": "every 3s", "hx-swap": "outerHTML"}
	}
	return nil
}
func lockFixHref(v biblePage) string {
	if len(v.Lock.Drift) == 0 {
		return ""
	}
	d := v.Lock.Drift[0]
	return fmt.Sprintf("/studio/p/%s/chapters/%s/edit?page=%d&tool=lockfix&character=%s", url.PathEscape(v.Project.ID), url.PathEscape(d.ChapterID), d.PageNumber, url.QueryEscape(v.Character.ID))
}
func lockPercent(n *float64) string {
	if n == nil {
		return ""
	}
	return strconv.Itoa(int(*n*100+.5)) + "%"
}
