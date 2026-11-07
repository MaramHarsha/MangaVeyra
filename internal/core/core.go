// Package core is the contract between the application wiring and the
// feature modules: the Deps container (shared services plus a type-keyed
// service registry), the Module / Starter / JobHandlers interfaces, the
// Router and middleware, request-context helpers, the JSON error envelope and
// pagination helpers.
//
// A feature module looks like this:
//
//	package studio
//
//	type Module struct{ d *core.Deps }
//
//	func New(d *core.Deps) *Module { return &Module{d: d} }
//	func (m *Module) Name() string  { return "studio" }
//	func (m *Module) Routes(r *core.Router) {
//	    r.Page("GET /studio", m.dashboard, core.RequireUser)
//	    api := r.Group("/api/v1/projects", core.RequireUser)
//	    api.API("GET /{id}", m.getProject)
//	}
//
// Modules find each other's services through the registry instead of app
// wiring edits: studio calls core.Provide[studio.Service](d, service) in New and
// the editor calls core.MustUse[studio.Service](d) in Init.
package core

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"sync"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/config"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/notify"
	"github.com/MaramHarsha/MangaVeyra/internal/secrets"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
	"github.com/MaramHarsha/MangaVeyra/internal/version"
	"github.com/MaramHarsha/MangaVeyra/internal/ws"
)

// Deps holds the services every module may use. Fields are set once during
// wiring and never replaced afterwards.
type Deps struct {
	Cfg      *config.Config
	Log      *slog.Logger
	DB       *db.DB
	Store    storage.Store
	Media    *storage.Signer
	Secrets  *secrets.Box
	Settings *settings.Service
	Auth     *auth.Service
	I18n     *i18n.Bundle
	Jobs     *jobs.Queue
	// Executor runs Go-executor jobs; modules usually register handlers through the JobHandlers interface instead.
	Executor *jobs.Pool
	Hub      *ws.Hub
	Notify   *notify.Service
	Version  version.Info

	regMu    sync.RWMutex
	registry map[reflect.Type]any
}

// Provide registers v as the service of type T (usually a pointer or an
// interface type). Registering the same type twice panics: every service has one owner.
func Provide[T any](d *Deps, v T) {
	t := reflect.TypeFor[T]()
	d.regMu.Lock()
	defer d.regMu.Unlock()
	if d.registry == nil {
		d.registry = map[reflect.Type]any{}
	}
	if _, dup := d.registry[t]; dup {
		panic(fmt.Sprintf("core: service %v provided twice", t))
	}
	d.registry[t] = v
}

// Use returns the service of type T, if one was provided.
func Use[T any](d *Deps) (T, bool) {
	d.regMu.RLock()
	defer d.regMu.RUnlock()
	v, ok := d.registry[reflect.TypeFor[T]()]
	if !ok {
		var zero T
		return zero, false
	}
	return v.(T), true
}

// MustUse returns the service of type T or panics with a clear message
// (call it from Init or handlers, not from New, unless the provider is known to be constructed earlier).
func MustUse[T any](d *Deps) T {
	v, ok := Use[T](d)
	if !ok {
		panic(fmt.Sprintf("core: no service of type %v was provided (is its module listed in internal/app/modules.go?)", reflect.TypeFor[T]()))
	}
	return v
}

// Services lists the registered service types (diagnostics).
func (d *Deps) Services() []string {
	d.regMu.RLock()
	defer d.regMu.RUnlock()
	out := make([]string, 0, len(d.registry))
	for t := range d.registry {
		out = append(out, t.String())
	}
	sort.Strings(out)
	return out
}

// Module is a feature package mounted into the server.
type Module interface {
	// Name is a short identifier ("studio", "reader"...).
	Name() string
	// Routes registers HTTP routes.
	Routes(r *Router)
}

// Initer is implemented by modules that need other modules' services: Init
// runs after every module was constructed and before routes are registered.
type Initer interface {
	Init(ctx context.Context) error
}

// Starter is implemented by modules with background loops. Start runs in its
// own goroutine and must return when ctx is canceled.
type Starter interface {
	Start(ctx context.Context) error
}

// JobHandlers is implemented by modules that run Go-executor jobs.
type JobHandlers interface {
	JobHandlers() map[string]jobs.Handler
}
