package app

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
)

func parseURL(raw string) (*url.URL, error) { return url.Parse(raw) }

// healthModule serves /healthz (liveness), /readyz (readiness) and /api/v1/version.
type healthModule struct{ d *core.Deps }

func (m *healthModule) Name() string { return "health" }

func (m *healthModule) Routes(r *core.Router) {
	r.HandleFunc("GET /healthz", m.healthz)
	r.HandleFunc("GET /readyz", m.readyz)
	r.API("GET /api/v1/version", func(w http.ResponseWriter, r *http.Request) error {
		core.JSON(w, http.StatusOK, m.d.Version)
		return nil
	})
}

func (m *healthModule) healthz(w http.ResponseWriter, r *http.Request) {
	core.JSON(w, http.StatusOK, map[string]any{"status": "ok", "version": m.d.Version.Version})
}

type check struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Detail any    `json:"detail,omitempty"`
}

func (m *healthModule) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	checks := map[string]check{}
	ready := true

	if err := m.d.DB.Ping(ctx); err != nil {
		checks["database"], ready = check{Error: err.Error()}, false
	} else {
		checks["database"] = check{OK: true, Detail: string(m.d.DB.Dialect())}
	}

	if err := pingStore(ctx, m.d.Store); err != nil {
		checks["storage"], ready = check{Error: err.Error()}, false
	} else {
		checks["storage"] = check{OK: true, Detail: m.d.Store.Kind()}
	}

	if stats, err := m.d.Jobs.Stats(ctx, ""); err != nil {
		checks["jobs"] = check{Error: err.Error()}
	} else {
		checks["jobs"] = check{OK: true, Detail: map[string]int{"workers_online": stats.WorkersOnline, "queued": stats.Queued, "running": stats.Running}}
	}
	checks["email"] = check{OK: true, Detail: map[string]bool{"configured": m.d.Notify.EmailEnabled()}}

	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	core.JSON(w, status, map[string]any{"ready": ready, "version": m.d.Version, "checks": checks})
}

func pingStore(ctx context.Context, s storage.Store) error {
	switch st := s.(type) {
	case *storage.S3:
		return st.Ping(ctx)
	case *storage.Local:
		fi, err := os.Stat(st.Root())
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return os.ErrInvalid
		}
		f, err := os.CreateTemp(st.Root(), ".readyz-*")
		if err != nil {
			return err
		}
		name := f.Name()
		_ = f.Close()
		return os.Remove(name)
	}
	_, err := s.List(ctx, "readyz-probe/")
	return err
}

// mediaModule serves signed media URLs at GET /media/{key...}.
type mediaModule struct{ d *core.Deps }

func (m *mediaModule) Name() string { return "media" }

func (m *mediaModule) Routes(r *core.Router) {
	r.Handle("GET /media/{key...}", storage.MediaHandler(m.d.Store, m.d.Media))
}
