package ai

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/a-h/templ"

	"github.com/MaramHarsha/MangaVeyra/internal/ai/catalog"
	"github.com/MaramHarsha/MangaVeyra/internal/ai/providers"
	"github.com/MaramHarsha/MangaVeyra/internal/ai/router"
	"github.com/MaramHarsha/MangaVeyra/internal/ai/views"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// page renders body in the app shell with the Models & queue nav item.
func (m *Module) page(w http.ResponseWriter, r *http.Request, status int, title, mobileTitle, back string, body templ.Component) error {
	return m.ui.Page(w, r, status, web.PageOpts{Title: title, MobileTitle: mobileTitle, BackURL: back, Section: layout.SectionStudio,
		Nav: layout.NavModels, Tab: layout.TabMe}, body)
}

// done answers a form post: a toast for HTMX, otherwise a flash and a redirect.
func (m *Module) done(w http.ResponseWriter, r *http.Request, msg, target string) error {
	if core.IsHTMX(r) {
		web.Toast(w, "success", msg, "")
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	web.Flash(w, r, "success", msg, "")
	core.Redirect(w, r, target)
	return nil
}

func sentence(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

// usesText translates a manifest's "router uses it for" keys.
func usesText(ctx context.Context, m *catalog.Manifest) string {
	parts := make([]string, 0, len(m.Uses))
	for _, u := range m.Uses {
		t := i18n.T(ctx, "ai.use."+u)
		if t == "ai.use."+u {
			t = strings.ReplaceAll(u, "_", " ")
		}
		parts = append(parts, t)
	}
	if len(parts) == 0 {
		return m.Summary
	}
	return sentence(strings.Join(parts, i18n.T(ctx, "ai.list_sep")))
}

// costText is "GPU time", "~$0.13 / img", "~$0.40 / s" or "$5 / $25 per 1M tok".
func costText(ctx context.Context, m *catalog.Manifest, p router.Provider, o catalog.Offer, costPerImage float64) string {
	if p.Kind == providers.KindPlaceholder {
		return i18n.T(ctx, "ai.cost.free")
	}
	if p.Local {
		return i18n.T(ctx, "ai.cost.gpu")
	}
	switch o.Price.Unit {
	case catalog.PerImage, catalog.PerMegapixel:
		return i18n.T(ctx, "ai.cost.per_image", "usd", USD(o.Price.Estimate(1024, 1536, 0, 0, 0)))
	case catalog.PerSecond:
		return i18n.T(ctx, "ai.cost.per_second", "usd", USD(o.Price.USD))
	case catalog.Per1kTokens:
		out := o.Price.USDOut
		if out == 0 {
			out = o.Price.USD
		}
		return i18n.T(ctx, "ai.cost.per_mtok", "in", USD(o.Price.USD*1000), "out", USD(out*1000))
	}
	if costPerImage > 0 {
		return i18n.T(ctx, "ai.cost.per_image", "usd", USD(costPerImage))
	}
	return i18n.T(ctx, "ai.cost.unknown")
}

func kindTabs(ctx context.Context, counts map[string]int, current string, all bool) []ui.SegLink {
	var out []ui.SegLink
	for _, k := range catalog.Kinds {
		if counts[k] == 0 && k != catalog.KindImage && k != current {
			continue
		}
		href := "/models?kind=" + k
		if all {
			href += "&all=1"
		}
		out = append(out, ui.SegLink{Href: href, Label: i18n.T(ctx, "ai.kindtab."+k) + " " + fmt.Sprint(counts[k]), Current: k == current})
	}
	return out
}

func (m *Module) modelsPage(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := core.UserFrom(ctx)
	s := m.svc
	inst := s.d.Settings.MustInstance(ctx)
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = catalog.KindImage
	}
	showAll := r.URL.Query().Get("all") == "1"
	in, err := s.Inputs(ctx, u.ID)
	if err != nil {
		return err
	}
	provs, err := s.ListProviders(ctx, u.ID, false)
	if err != nil {
		return err
	}
	byID := map[string]*Provider{}
	for i := range provs {
		byID[provs[i].ID] = &provs[i]
	}
	v := views.ModelsView{Host: web.ServerHost(inst.PublicURL, s.d.Cfg.PublicURL, r.Host), IsAdmin: u.IsAdmin(),
		CanAdd: u.IsAdmin() || inst.UsersMayAddProviders, Kind: kind, ShowAll: showAll, LocalOnly: inst.LocalOnly}
	mode := in.UserMode
	if mode == "" {
		mode = inst.RoutingMode
	}
	if inst.LocalOnly {
		mode = router.ModeLocalOnly
	}
	v.ModeLabel = i18n.T(ctx, "ai.mode."+mode)
	if l := inst.Limits.ServerMonthlyRemoteSpendUSD; l > 0 {
		v.LimitLabel = USD(l)
	} else {
		v.LimitLabel = i18n.T(ctx, "ai.off")
	}
	v.Cards = m.providerCards(ctx, provs, in)
	// Models table.
	counts := map[string]int{}
	type rowSort struct {
		row  views.ModelRow
		rank int
		loc  bool
	}
	var rows []rowSort
	total := 0
	for _, man := range in.Catalog.All() {
		av := router.Available(in, man)
		row := views.ModelRow{ID: man.ID, Name: man.Name, Maker: man.Maker, Uses: usesText(ctx, man), Enabled: !in.Disabled[man.ID],
			CanToggle: u.IsAdmin() && man.Source != catalog.SourceWorker, Custom: man.Source == catalog.SourceCustom, Note: man.Summary}
		rank := 5
		switch {
		case av.Reason.Code == router.RDisabled:
			row.Status, rank = "disabled", 2
			row.Provider = av.Provider.Name
		case av.OK && av.Provider.Kind == providers.KindPlaceholder:
			row.Status, rank = "placeholder", 3
			row.Provider = i18n.T(ctx, "ai.kind.placeholder")
		case av.OK && !man.Licence.Commercial && inst.Content.LicenceGuard:
			row.Status, rank = "licence", 1
			row.Note = man.Licence.Note
		case av.OK:
			row.Status, rank = "ready", 0
		case av.Reason.Code == router.RVRAM:
			row.Status = "vram"
		case av.Reason.Code == router.RNotInstalled:
			row.Status = "not_installed"
		default:
			row.Status = "not_connected"
		}
		if av.OK || av.Reason.Code == router.RDisabled {
			if row.Provider == "" {
				row.Provider = av.Provider.Name
				if av.Provider.Kind == providers.KindLocalWorker {
					row.Provider = i18n.T(ctx, "ai.kind.local_worker")
				}
			}
			cpi := 0.0
			if p := byID[av.Provider.ID]; p != nil {
				cpi = p.Config.V.CostPerImage
			}
			row.Cost = costText(ctx, man, av.Provider, av.Offer, cpi)
		} else {
			row.Provider = "—"
			row.Cost = "—"
			if av.Reason.Code != "" {
				row.Note = ReasonText(ctx, av.Reason)
			}
		}
		row.StatusText = i18n.T(ctx, "ai.status."+row.Status)
		available := row.Status == "ready" || row.Status == "licence" || row.Status == "disabled" || row.Status == "placeholder"
		if available {
			counts[man.Kind]++
		}
		if man.Kind != kind {
			continue
		}
		total++
		if !showAll && !available {
			continue
		}
		rows = append(rows, rowSort{row: row, rank: rank, loc: av.Provider.Local})
	}
	if showAll {
		counts = map[string]int{}
		for _, man := range in.Catalog.All() {
			counts[man.Kind]++
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].rank != rows[j].rank {
			return rows[i].rank < rows[j].rank
		}
		return rows[i].loc && !rows[j].loc
	})
	for _, rs := range rows {
		v.Rows = append(v.Rows, rs.row)
	}
	v.Tabs = kindTabs(ctx, counts, kind, showAll)
	v.AllCount = total
	v.AllHref = "/models?kind=" + kind
	if !showAll {
		v.AllHref += "&all=1"
	}
	v.Empty = len(v.Rows) == 0
	real := 0
	for k, n := range counts {
		if k == catalog.KindImage || k == catalog.KindText || k == catalog.KindUpscale {
			real += n
		}
	}
	// Three placeholder models (image, text, resize) and nothing else.
	v.OnlyPlaceholder = !showAll && real <= 3 && len(in.Workers) == 0 && len(provs) <= 2
	if u.IsAdmin() {
		changes, err := s.ManifestChanges(ctx, 12)
		if err != nil {
			return err
		}
		for _, c := range changes {
			v.Changes = append(v.Changes, views.ChangeRow{Change: c.Change, Text: i18n.T(ctx, "ai.changes."+c.Change), Name: c.Name,
				When: dateText(ctx, c.CreatedAt), New: c.AckedAt == nil})
			if c.AckedAt == nil {
				v.NewCount++
			}
		}
		s.mu.RLock()
		if s.base != nil {
			v.Problems = s.base.Problems
		}
		s.mu.RUnlock()
	}
	return m.page(w, r, http.StatusOK, i18n.T(ctx, "ai.models.title"), i18n.T(ctx, "ai.models.mobile_title"), "/settings", views.ModelsPage(v))
}

// providerCards builds the G2 provider cards.
func (m *Module) providerCards(ctx context.Context, provs []Provider, in router.Inputs) []views.ProviderCard {
	s := m.svc
	c := in.Catalog
	var cards []views.ProviderCard
	for i := range provs {
		p := &provs[i]
		info, _ := KindInfoFor(p.Kind)
		card := views.ProviderCard{ID: p.ID, Icon: info.Icon, Name: p.Name, Href: "/models/providers/" + p.ID, State: "ok"}
		switch p.Kind {
		case providers.KindPlaceholder:
			card.Name = i18n.T(ctx, "ai.kind.placeholder")
			card.Sub = i18n.T(ctx, "ai.card.placeholder")
			card.Href = "/models?kind=image#model-placeholder-image"
		case providers.KindLocalWorker:
			card.Name = i18n.T(ctx, "ai.kind.local_worker")
			card.Href = "/models/workers/new"
			if len(in.Workers) == 0 {
				card.Sub, card.State = i18n.T(ctx, "ai.card.no_worker"), "off"
			} else {
				gpu := ""
				models := 0
				for _, man := range c.All() {
					for _, o := range man.Providers {
						if o.Provider == providers.KindLocalWorker && workerHas(in.Workers, man) {
							models++
							break
						}
					}
				}
				for _, w := range s.onlineWorkers(ctx) {
					for _, g := range w.GPUs.V {
						gpu = shortGPU(g.Name)
					}
				}
				if gpu == "" {
					gpu = i18n.T(ctx, "ai.card.cpu_only")
				}
				card.Sub = i18n.T(ctx, "ai.card.worker", "gpu", gpu, "count", models)
			}
		default:
			served := map[string]int{}
			for _, man := range c.All() {
				for _, o := range man.Providers {
					if o.ProviderID == p.ID || (o.ProviderID == "" && o.Provider == p.Kind) {
						served[man.Kind]++
						break
					}
				}
			}
			disc := map[string]int{}
			for _, rm := range p.Config.V.Models {
				disc[rm.Kind]++
			}
			switch {
			case p.Config.V.Draft:
				card.Sub, card.State = i18n.T(ctx, "ai.card.draft"), "warn"
				card.Href = "/models/providers/new/connect?id=" + p.ID
			case !p.Enabled:
				card.Sub, card.State = i18n.T(ctx, "ai.card.off"), "off"
			case !p.Ready():
				card.Sub, card.State = i18n.T(ctx, "ai.card.needs_key"), "warn"
			case !p.LastTest.V.At.IsZero() && !p.LastTest.V.OK:
				card.Sub, card.State = i18n.T(ctx, "ai.card.test_failed"), "warn"
			case p.Kind == providers.KindOllama && len(p.Config.V.Models) > 0:
				card.Sub = p.Config.V.Models[0].Name
			case disc["image"] > 0 || disc["text"] > 0:
				card.Sub = i18n.T(ctx, "ai.card.discovered", "images", disc["image"], "texts", disc["text"])
			case p.Config.V.ModelID != "":
				card.Sub = p.Config.V.ModelID
			default:
				n := 0
				for _, v := range served {
					n += v
				}
				if p.HasKey() {
					card.Sub = i18n.T(ctx, "ai.card.key_saved", "count", n)
				} else {
					card.Sub = i18n.T(ctx, "ai.card.models", "count", n)
				}
			}
		}
		card.StateText = i18n.T(ctx, "ai.card.state."+card.State)
		cards = append(cards, card)
	}
	// Built-ins go last so connected providers lead, like the design.
	sort.SliceStable(cards, func(i, j int) bool {
		return cardRank(cards[i].ID) < cardRank(cards[j].ID)
	})
	// The placeholder card fills the first row until four providers are connected;
	// its models stay listed in the table either way.
	if len(cards) > 4 {
		out := cards[:0]
		for _, c := range cards {
			if c.ID != PlaceholderID {
				out = append(out, c)
			}
		}
		cards = out
	}
	return cards
}

func cardRank(id string) int {
	switch id {
	case LocalWorkerID:
		return 0
	case PlaceholderID:
		return 2
	}
	return 1
}

func workerHas(ws []router.Worker, m *catalog.Manifest) bool {
	for _, w := range ws {
		for _, id := range w.Models {
			if id == m.ID {
				return true
			}
		}
	}
	return false
}

// shortGPU turns "NVIDIA GeForce RTX 4090" into "RTX 4090".
func shortGPU(name string) string {
	for _, p := range []string{"NVIDIA GeForce ", "NVIDIA ", "AMD Radeon ", "AMD "} {
		name = strings.TrimPrefix(name, p)
	}
	return name
}

func (m *Module) toggleModel(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	id := r.PathValue("id")
	on := web.FormBool(r, "enabled")
	if err := m.svc.SetModelEnabled(ctx, core.UserFrom(ctx), id, on); err != nil {
		return err
	}
	key := "ai.models.disabled"
	if on {
		key = "ai.models.enabled"
	}
	name := id
	if man, ok := m.svc.Catalog(ctx).Get(id); ok {
		name = man.Name
	}
	return m.done(w, r, i18n.T(ctx, key, "name", name), "/models")
}

func (m *Module) ackChanges(w http.ResponseWriter, r *http.Request) error {
	if err := m.svc.AcknowledgeChanges(r.Context()); err != nil {
		return err
	}
	return m.done(w, r, i18n.T(r.Context(), "ai.changes.acked"), "/models")
}

func (m *Module) setLocalOnly(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	on := web.FormBool(r, "local_only")
	if _, err := m.d.Settings.UpdateInstance(ctx, core.UserFrom(ctx).ID, func(i *settings.Instance) error { i.LocalOnly = on; return nil }); err != nil {
		return err
	}
	key := "ai.local_only.off"
	if on {
		key = "ai.local_only.on"
	}
	return m.done(w, r, i18n.T(ctx, key), "/models")
}

// providerPage sends a provider card to the right place.
func (m *Module) providerPage(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p, err := m.svc.GetProvider(ctx, r.PathValue("id"))
	if err != nil {
		return err
	}
	u := core.UserFrom(ctx)
	switch {
	case p.Kind == providers.KindLocalWorker:
		core.Redirect(w, r, "/models/workers/new")
	case p.Kind == providers.KindPlaceholder:
		core.Redirect(w, r, "/models?kind=image")
	case !CanUse(u, p):
		return core.ErrNotFound()
	case !CanManage(u, p):
		core.Redirect(w, r, "/models")
	default:
		core.Redirect(w, r, "/models/providers/new/test?id="+p.ID)
	}
	return nil
}

func (m *Module) toggleProvider(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	id := r.PathValue("id")
	if err := m.svc.SetProviderEnabled(ctx, core.UserFrom(ctx), id, web.FormBool(r, "enabled")); err != nil {
		return err
	}
	return m.done(w, r, i18n.T(ctx, "ai.saved"), "/models/providers/new/test?id="+id)
}

func (m *Module) deleteProviderPage(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := m.svc.DeleteProvider(ctx, core.UserFrom(ctx), r.PathValue("id")); err != nil {
		return err
	}
	return m.done(w, r, i18n.T(ctx, "ai.provider.deleted"), "/models")
}

func (m *Module) approveProvider(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id := r.PathValue("id")
	if err := m.svc.ApprovePrivate(ctx, core.UserFrom(ctx), id); err != nil {
		return err
	}
	return m.done(w, r, i18n.T(ctx, "ai.provider.approved"), "/models/providers/new/test?id="+id)
}

// ComputeExtra implements web.ComputeExtender: remote spend, queue ETA and weekly GPU time.
func (m *Module) ComputeExtra(ctx context.Context, userID string) web.ComputeExtra {
	mt, err := m.svc.Meter(ctx, userID)
	if err != nil {
		m.d.Log.Warn("ai: meter", "err", err)
		return web.ComputeExtra{}
	}
	var ex web.ComputeExtra
	switch {
	case mt.LocalOnly:
	case mt.LimitUSD > 0:
		ex.Remote = i18n.T(ctx, "ai.meter.remote_limit", "spent", USD(mt.MonthUSD), "limit", USD(mt.LimitUSD))
	case mt.MonthUSD > 0:
		ex.Remote = i18n.T(ctx, "ai.meter.remote", "spent", USD(mt.MonthUSD))
	}
	if mt.ETASeconds > 0 {
		ex.ETA = i18n.T(ctx, "ai.meter.eta", "eta", DurationText(ctx, mt.ETASeconds))
	}
	if mt.WeekGPUSeconds > 0 {
		ex.WeekGPU = i18n.T(ctx, "ai.meter.week", "time", DurationText(ctx, mt.WeekGPUSeconds))
	}
	return ex
}

// whenText formats a time as a short relative or dated text.
func whenText(ctx context.Context, t time.Time, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return i18n.T(ctx, "ai.when.now")
	case d < time.Hour:
		return i18n.T(ctx, "ai.when.minutes", "n", int(d.Minutes()))
	case d < 24*time.Hour:
		return i18n.T(ctx, "ai.when.hours", "n", int(d.Hours()))
	}
	return dateText(ctx, t)
}

// dateText formats a date for lists.
func dateText(ctx context.Context, t time.Time) string {
	if i18n.LocaleFrom(ctx) == "ja" {
		return t.UTC().Format("2006年1月2日")
	}
	return t.UTC().Format("2 Jan 2006")
}
