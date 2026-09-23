package ai

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/ai/catalog"
	"github.com/MaramHarsha/MangaVeyra/internal/ai/providers"
	"github.com/MaramHarsha/MangaVeyra/internal/ai/views"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/setup"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

// serverURL is the URL workers use to reach this server.
func (m *Module) serverURL(r *http.Request) string {
	inst := m.d.Settings.MustInstance(r.Context())
	for _, u := range []string{inst.PublicURL, m.d.Cfg.PublicURL} {
		if u != "" {
			return strings.TrimRight(u, "/")
		}
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// localSet returns the chosen local model ids (or the recommendation for the workers' GPUs).
func (m *Module) localSet(ctx context.Context) []string {
	if g := m.svc.Guards(ctx); len(g.LocalModels) > 0 {
		return g.LocalModels
	}
	var gpus []jobs.GPU
	for _, w := range m.svc.onlineWorkers(ctx) {
		gpus = append(gpus, w.GPUs.V...)
	}
	rec := m.svc.Recommend(ctx, gpus)
	if rec == nil {
		return nil
	}
	return rec.IDs
}

func workerCommands(server, token, name string, models []string) (docker, pip, compose string) {
	env := ""
	if len(models) > 0 {
		env = " -e MV_MODELS=" + strings.Join(models, ",")
	}
	docker = "docker build -f deploy/Dockerfile.worker -t mangaveyra-worker:gpu .\n" +
		"docker run -d --name mv-worker --gpus all --restart unless-stopped --stop-timeout 150 \\\n" +
		"  -e MV_SERVER=" + server + " -e MV_WORKER_TOKEN=" + token + " -e MV_WORKER_NAME=" + name + env + " \\\n" +
		"  -v mv-worker-models:/models mangaveyra-worker:gpu"
	modelsEnv := ""
	if len(models) > 0 {
		modelsEnv = " MV_MODELS=" + strings.Join(models, ",")
	}
	pip = "pip install './worker[diffusers,comfyui,vision]'\n" +
		"MV_SERVER=" + server + " MV_WORKER_TOKEN=" + token + " MV_WORKER_NAME=" + name + modelsEnv + " mangaveyra-worker"
	compose = "MV_WORKER_TOKEN=" + token + " MV_WORKER_NAME=" + name + " \\\n  docker compose -f deploy/docker-compose.yml --profile gpu up -d"
	return docker, pip, compose
}

func (m *Module) workersView(ctx context.Context, r *http.Request) (views.WorkersView, error) {
	v := views.WorkersView{IsAdmin: true, Server: m.serverURL(r)}
	toks, err := m.d.Auth.ListTokens(ctx, "", auth.TokenWorker)
	if err != nil {
		return v, err
	}
	for _, t := range toks {
		used := i18n.T(ctx, "ai.workers.never_used")
		if t.LastUsedAt != nil {
			used = i18n.T(ctx, "ai.workers.last_used", "when", whenText(ctx, *t.LastUsedAt, m.svc.Now()))
		}
		v.Tokens = append(v.Tokens, views.WorkerToken{ID: t.ID, Name: t.Name, Prefix: t.Prefix, Created: dateText(ctx, t.CreatedAt), LastUsed: used})
	}
	ws, err := m.d.Jobs.ListWorkers(ctx)
	if err != nil {
		return v, err
	}
	for _, w := range ws {
		detail := i18n.T(ctx, "ai.card.cpu_only")
		var gpus []string
		for _, g := range w.GPUs.V {
			gpus = append(gpus, fmt.Sprintf("%s · %s GB", shortGPU(g.Name), web.GB(g.VRAMTotalMB)))
		}
		if len(gpus) > 0 {
			detail = strings.Join(gpus, ", ")
		}
		detail += " · " + i18n.T(ctx, "ai.workers.running", "count", w.Running) + " · " + whenText(ctx, w.LastSeenAt, m.svc.Now())
		v.Workers = append(v.Workers, views.WorkerRow{Name: w.Name, Status: w.Status, Detail: detail, Models: strings.Join(w.Models.V, ", "), Version: w.Version})
	}
	if set := m.localSet(ctx); len(set) > 0 {
		v.Models = strings.Join(m.modelNames(ctx, set), ", ")
	}
	return v, nil
}

func (m *Module) modelNames(ctx context.Context, ids []string) []string {
	c := m.svc.Catalog(ctx)
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if man, ok := c.Get(id); ok {
			out = append(out, man.Name)
		} else {
			out = append(out, id)
		}
	}
	return out
}

func (m *Module) workersPage(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	v, err := m.workersView(ctx, r)
	if err != nil {
		return err
	}
	return m.page(w, r, http.StatusOK, i18n.T(ctx, "ai.workers.title"), i18n.T(ctx, "ai.workers.mobile"), "/models", views.WorkersPage(v))
}

func (m *Module) createWorkerToken(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	u := core.UserFrom(ctx)
	name := strings.TrimSpace(web.FormValue(r, "name"))
	v, err := m.workersView(ctx, r)
	if err != nil {
		return err
	}
	clean := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return -1
	}, name)
	if clean == "" || len(clean) > 80 || clean != name {
		v.Error = i18n.T(ctx, "ai.error.worker_name")
		return m.page(w, r, http.StatusUnprocessableEntity, i18n.T(ctx, "ai.workers.title"), i18n.T(ctx, "ai.workers.mobile"), "/models", views.WorkersPage(v))
	}
	raw, _, err := m.d.Auth.CreateToken(ctx, auth.TokenInput{UserID: u.ID, Kind: auth.TokenWorker, Name: name, Scopes: []string{"*"}})
	if err != nil {
		return err
	}
	v, err = m.workersView(ctx, r)
	if err != nil {
		return err
	}
	v.NewToken, v.NewName = raw, name
	v.Docker, v.Pip, v.Compose = workerCommands(v.Server, raw, name, m.localSet(ctx))
	w.Header().Set("Cache-Control", "no-store")
	return m.page(w, r, http.StatusOK, i18n.T(ctx, "ai.workers.title"), i18n.T(ctx, "ai.workers.mobile"), "/models", views.WorkersPage(v))
}

func (m *Module) revokeWorkerToken(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := core.UserFrom(ctx)
	id := r.PathValue("id")
	t, err := m.d.Auth.GetToken(ctx, id)
	if err != nil {
		return err
	}
	if t.Kind != auth.TokenWorker {
		return core.ErrNotFound()
	}
	if err := m.d.Auth.RevokeToken(ctx, u.ID, id, true); err != nil {
		return err
	}
	return m.done(w, r, i18n.T(ctx, "ai.workers.revoked", "name", t.Name), "/models/workers/new")
}

// Recommendation is the local model set that fits the workers' GPUs.
type Recommendation struct {
	IDs        []string
	Names      []string
	DownloadGB float64
}

// Recommend picks local models that fit the largest GPU: the best edit
// model, the fastest draft model and the Character Lock base (SDXL family).
func (s *Service) Recommend(ctx context.Context, gpus []jobs.GPU) *Recommendation {
	best := 0
	for _, g := range gpus {
		best = max(best, g.VRAMTotalMB)
	}
	vram := float64(best) / 1024
	if vram < 5 {
		return nil
	}
	c := s.Catalog(ctx)
	fits := func(m *catalog.Manifest) bool {
		if !m.Hardware.Local || m.Hardware.AnyWorker || m.Source == catalog.SourceWorker {
			return false
		}
		if _, ok := m.Offer(providers.KindLocalWorker); !ok {
			return false
		}
		need := m.Hardware.VRAMGB
		if need == 0 {
			need = m.Hardware.MinVRAMGB
		}
		return need > 0 && need <= vram+0.5 && m.Licence.Commercial
	}
	pick := func(task, cat string) *catalog.Manifest {
		var out *catalog.Manifest
		for _, m := range c.ByKind(catalog.KindImage) {
			if !fits(m) || !m.Supports(task) {
				continue
			}
			if out == nil || m.Score(cat) > out.Score(cat) {
				out = m
			}
		}
		return out
	}
	rec := &Recommendation{}
	add := func(m *catalog.Manifest) {
		if m == nil || slices.Contains(rec.IDs, m.ID) {
			return
		}
		rec.IDs = append(rec.IDs, m.ID)
		rec.Names = append(rec.Names, m.Name)
		rec.DownloadGB += m.Hardware.DownloadGB
	}
	add(pick(catalog.TaskEdit, catalog.ScoreEdit))
	add(pick(catalog.TaskGenerate, catalog.ScoreSpeed))
	add(pick(catalog.TaskTrain, catalog.ScoreConsistency))
	if len(rec.IDs) == 0 {
		return nil
	}
	return rec
}

func (m *Module) localView(ctx context.Context, u *core.User) views.LocalView {
	s := m.svc
	v := views.LocalView{IsAdmin: u.IsAdmin()}
	var gpus []jobs.GPU
	installed := map[string]bool{}
	online := s.onlineWorkers(ctx)
	v.HasWorker = len(online) > 0
	for _, w := range online {
		gpus = append(gpus, w.GPUs.V...)
		for _, id := range w.Models.V {
			installed[id] = true
		}
	}
	best := jobs.GPU{}
	for _, g := range gpus {
		if g.VRAMTotalMB > best.VRAMTotalMB {
			best = g
		}
	}
	if best.VRAMTotalMB > 0 {
		v.GPU = i18n.T(ctx, "ai.local.gpu", "gpu", best.Name, "vram", web.GB(best.VRAMTotalMB))
	} else {
		v.GPU = i18n.T(ctx, "ai.local.no_gpu")
	}
	rec := s.Recommend(ctx, gpus)
	chosen := s.Guards(ctx).LocalModels
	if len(chosen) == 0 && rec != nil {
		chosen = rec.IDs
	}
	vram := float64(best.VRAMTotalMB) / 1024
	total := 0.0
	for _, man := range s.Catalog(ctx).All() {
		if _, ok := man.Offer(providers.KindLocalWorker); !ok || man.Hardware.AnyWorker || man.Source == catalog.SourceWorker {
			continue
		}
		need := man.Hardware.VRAMGB
		if need == 0 {
			need = man.Hardware.MinVRAMGB
		}
		fits := best.VRAMTotalMB == 0 || man.Hardware.MinVRAMGB <= vram+0.5
		lm := views.LocalModel{ID: man.ID, Name: man.Name, Fits: fits, Installed: installed[man.ID], Chosen: slices.Contains(chosen, man.ID),
			Recommended: rec != nil && slices.Contains(rec.IDs, man.ID),
			Detail:      i18n.T(ctx, "ai.local.detail", "vram", strconv.FormatFloat(need, 'f', -1, 64), "size", strconv.FormatFloat(man.Hardware.DownloadGB, 'f', -1, 64)),
			Licence:     licenceText(ctx, man)}
		if lm.Chosen {
			total += man.Hardware.DownloadGB
		}
		v.Models = append(v.Models, lm)
	}
	slices.SortStableFunc(v.Models, func(a, b views.LocalModel) int {
		rank := func(x views.LocalModel) int {
			switch {
			case x.Installed:
				return 0
			case x.Recommended:
				return 1
			case x.Fits:
				return 2
			}
			return 3
		}
		return rank(a) - rank(b)
	})
	v.DownloadGB = strconv.FormatFloat(total, 'f', -1, 64)
	v.Recommended = rec != nil
	if len(chosen) == 0 {
		v.Command = "mangaveyra-worker check\nmangaveyra-worker"
	} else {
		env := "MV_MODELS=" + strings.Join(chosen, ",")
		v.Command = env + " mangaveyra-worker check\n" + env + " mangaveyra-worker"
	}
	return v
}

func licenceText(ctx context.Context, m *catalog.Manifest) string {
	if m.Licence.Commercial {
		return m.Licence.ID
	}
	return i18n.T(ctx, "ai.local.noncommercial", "licence", m.Licence.ID)
}

func (m *Module) localPage(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	v := m.localView(ctx, core.UserFrom(ctx))
	v.Saved = r.URL.Query().Get("saved") == "1"
	return m.page(w, r, http.StatusOK, i18n.T(ctx, "ai.local.title"), i18n.T(ctx, "ai.local.title"), "/models", views.LocalPage(v))
}

func (m *Module) saveLocal(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	c := m.svc.Catalog(ctx)
	var ids []string
	for _, id := range r.PostForm["model"] {
		if man, ok := c.Get(id); ok {
			if _, ok := man.Offer(providers.KindLocalWorker); ok {
				ids = append(ids, id)
			}
		}
	}
	g := m.svc.Guards(ctx)
	g.LocalModels = ids
	if err := m.svc.SaveGuards(ctx, core.UserFrom(ctx).ID, g); err != nil {
		return err
	}
	core.Redirect(w, r, "/models/local?saved=1")
	return nil
}

// modelStep fills the first-run wizard's step 3 (setup.ModelStep).
type modelStep struct{ m *Module }

// Providers returns the step 3 cards with live connection status.
func (ms *modelStep) Providers(ctx context.Context) ([]setup.ProviderCard, error) {
	s := ms.m.svc
	provs, err := s.ListProviders(ctx, "", false)
	if err != nil {
		return nil, err
	}
	status := func(kinds ...string) string {
		st := setup.ProviderNotConnected
		for _, p := range provs {
			if p.Builtin || !slices.Contains(kinds, p.Kind) || p.Config.V.Draft {
				continue
			}
			switch {
			case !p.LastTest.V.At.IsZero() && !p.LastTest.V.OK:
				if st == setup.ProviderNotConnected {
					st = setup.ProviderError
				}
			case p.Enabled && p.Ready():
				return setup.ProviderConnected
			}
		}
		return st
	}
	next := "&next=/setup/models"
	cards := setup.DefaultProviders()
	for i := range cards {
		c := &cards[i]
		switch c.Kind {
		case "local_worker":
			if len(s.onlineWorkers(ctx)) > 0 {
				c.Status = setup.ProviderConnected
			}
		case "vendor":
			c.Status = status(providers.KindOpenAI, providers.KindGoogle, providers.KindXAI, providers.KindAnthropic)
			c.Href = "/models/providers/new?kind=vendor" + next
		case "custom":
			c.Status = status(customKinds...)
			c.Href = "/models/providers/new?kind=custom" + next
		default:
			c.Status = status(c.Kind)
			c.Href = "/models/providers/new?kind=" + c.Kind + next
		}
	}
	return cards, nil
}

// Recommend sizes the local model set to the detected GPUs.
func (ms *modelStep) Recommend(ctx context.Context, gpus []jobs.GPU) (*setup.Recommendation, error) {
	rec := ms.m.svc.Recommend(ctx, gpus)
	if rec == nil {
		return nil, nil
	}
	return &setup.Recommendation{Models: rec.Names, DownloadGB: rec.DownloadGB, InstallHref: "/models/local?recommended=1"}, nil
}
