package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/ai/catalog"
	"github.com/MaramHarsha/MangaVeyra/internal/ai/providers"
	"github.com/MaramHarsha/MangaVeyra/internal/ai/router"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

// Guards are the routing guards kept by the ai module (the others live in the instance settings).
type Guards struct {
	// Learn turns on keep/reroll learning (v2).
	Learn bool `json:"learn"`
	// LastLimitUSD remembers the monthly limit while the limit guard is off.
	LastLimitUSD float64 `json:"last_limit_usd"`
	// LocalModels is the local model set the admin chose to install on workers.
	LocalModels []string `json:"local_models,omitempty"`
}

const guardsKey = "ai.guards"

// IntentReader is the optional small text model that reads a prompt into a
// Need (characters, text in art, B/W…). Rules-only routing works without it.
type IntentReader interface {
	ReadIntent(ctx context.Context, prompt string, need *router.Need) error
}

// Service is the AI service other modules use through core.Use[*ai.Service].
type Service struct {
	d   *core.Deps
	log *slog.Logger

	// public reaches the internet only; private may also reach approved private addresses.
	public  *providers.Registry
	private *providers.Registry

	mu        sync.RWMutex
	base      *catalog.Catalog
	baseAt    time.Time
	custom    []*catalog.Manifest
	disabled  map[string]bool
	customAt  time.Time
	intent    IntentReader
	ownerOf   map[string]string // custom model id → owning provider id
	workersMu sync.Mutex

	// Now is the clock (tests).
	Now func() time.Time
	// Wait is the poll interval for worker re-dispatch (tests shorten it).
	Wait time.Duration
}

// NewService builds the service (no I/O).
func NewService(d *core.Deps) *Service {
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	s := &Service{d: d, log: log, disabled: map[string]bool{}, Now: time.Now, Wait: 400 * time.Millisecond}
	s.public = providers.NewRegistry(providers.Env{HTTP: s.policyFor("", false).Client(10 * time.Minute)})
	s.private = providers.NewRegistry(providers.Env{HTTP: s.policyFor(providers.KindComfyUI, true).Client(10 * time.Minute)})
	return s
}

// SetIntentReader installs the optional intent reader.
func (s *Service) SetIntentReader(r IntentReader) {
	s.mu.Lock()
	s.intent = r
	s.mu.Unlock()
}

// SetRegistries replaces the adapter registries (tests use plain clients against httptest servers).
func (s *Service) SetRegistries(public, private *providers.Registry) {
	s.public, s.private = public, private
}

// registryFor picks the registry whose HTTP client matches the provider's SSRF policy.
func (s *Service) registryFor(p *Provider) *providers.Registry {
	if s.policyFor(p.Kind, p.Config.V.PrivateApproved).AllowPrivate {
		return s.private
	}
	return s.public
}

func (s *Service) invalidate() {
	s.mu.Lock()
	s.customAt = time.Time{}
	s.mu.Unlock()
}

// ReloadCatalog re-reads the embedded catalogue and <data>/manifests and records changes.
func (s *Service) ReloadCatalog(ctx context.Context) error {
	c, err := catalog.Load(s.d.Cfg.ManifestDir())
	if err != nil {
		return err
	}
	for _, p := range c.Problems {
		s.log.Warn("ai: manifest skipped", "problem", p)
	}
	s.mu.Lock()
	s.base, s.baseAt = c, time.Now()
	s.customAt = time.Time{}
	s.mu.Unlock()
	return s.trackChanges(ctx, c)
}

// trackChanges compares manifests with the last seen fingerprints and logs added / updated / removed.
func (s *Service) trackChanges(ctx context.Context, c *catalog.Catalog) error {
	var rows []struct {
		ModelID string `db:"model_id"`
		Name    string `db:"name"`
		Source  string `db:"source"`
		Hash    string `db:"hash"`
	}
	if err := s.d.DB.Select(ctx, &rows, `SELECT model_id, name, source, hash FROM ai_manifest_state`); err != nil {
		return err
	}
	first := len(rows) == 0
	seen := map[string]bool{}
	now := db.Now()
	return s.d.DB.WithTx(ctx, func(tx *db.Tx) error {
		prev := map[string]int{}
		for i, r := range rows {
			prev[r.ModelID] = i
		}
		for _, m := range c.All() {
			seen[m.ID] = true
			h := catalog.Hash(m)
			i, ok := prev[m.ID]
			switch {
			case !ok:
				if _, err := tx.Exec(ctx, `INSERT INTO ai_manifest_state (model_id, name, source, hash, updated_at) VALUES (?, ?, ?, ?, ?)`, m.ID, m.Name, m.Source, h, now); err != nil {
					return err
				}
				if !first {
					if err := addChange(ctx, tx, m.ID, m.Name, "added", m.Source, m.Summary, now); err != nil {
						return err
					}
				}
			case rows[i].Hash != h:
				if _, err := tx.Exec(ctx, `UPDATE ai_manifest_state SET name = ?, source = ?, hash = ?, updated_at = ? WHERE model_id = ?`, m.Name, m.Source, h, now, m.ID); err != nil {
					return err
				}
				if err := addChange(ctx, tx, m.ID, m.Name, "updated", m.Source, m.Verify, now); err != nil {
					return err
				}
			}
		}
		for _, r := range rows {
			if !seen[r.ModelID] {
				if _, err := tx.Exec(ctx, `DELETE FROM ai_manifest_state WHERE model_id = ?`, r.ModelID); err != nil {
					return err
				}
				if err := addChange(ctx, tx, r.ModelID, r.Name, "removed", r.Source, "", now); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func addChange(ctx context.Context, tx *db.Tx, id, name, change, source, summary string, now time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO ai_manifest_changes (id, model_id, name, change, source, summary, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		db.NewID(), id, name, change, source, summary, now)
	return err
}

// ManifestChange is one catalogue change for admins.
type ManifestChange struct {
	ID        string     `db:"id" json:"id"`
	ModelID   string     `db:"model_id" json:"model_id"`
	Name      string     `db:"name" json:"name"`
	Change    string     `db:"change" json:"change"`
	Source    string     `db:"source" json:"source"`
	Summary   string     `db:"summary" json:"summary"`
	CreatedAt time.Time  `db:"created_at" json:"created_at"`
	AckedAt   *time.Time `db:"acknowledged_at" json:"acknowledged_at,omitempty"`
}

// ManifestChanges lists recent catalogue changes (unacknowledged first).
func (s *Service) ManifestChanges(ctx context.Context, limit int) ([]ManifestChange, error) {
	var out []ManifestChange
	err := s.d.DB.Select(ctx, &out, `SELECT id, model_id, name, change, source, summary, created_at, acknowledged_at FROM ai_manifest_changes
		ORDER BY CASE WHEN acknowledged_at IS NULL THEN 0 ELSE 1 END, created_at DESC LIMIT ?`, limit)
	return out, err
}

// AcknowledgeChanges marks every change as seen.
func (s *Service) AcknowledgeChanges(ctx context.Context) error {
	_, err := s.d.DB.Exec(ctx, `UPDATE ai_manifest_changes SET acknowledged_at = ? WHERE acknowledged_at IS NULL`, db.Now())
	return err
}

// Catalog returns the full catalogue: built-in and file manifests, custom
// models from the database and models advertised by workers without a manifest.
func (s *Service) Catalog(ctx context.Context) *catalog.Catalog {
	s.mu.RLock()
	base, fresh := s.base, time.Since(s.customAt) < 5*time.Second
	custom, disabled := s.custom, s.disabled
	s.mu.RUnlock()
	if base == nil {
		if err := s.ReloadCatalog(ctx); err != nil {
			s.log.Error("ai: catalogue", "err", err)
			c, _ := catalog.Load("")
			s.mu.Lock()
			s.base = c
			s.mu.Unlock()
		}
		s.mu.RLock()
		base = s.base
		s.mu.RUnlock()
	}
	if !fresh {
		custom, disabled = s.loadCustom(ctx)
	}
	_ = disabled
	extra := append([]*catalog.Manifest(nil), custom...)
	extra = append(extra, s.workerManifests(ctx, base.With(custom...))...)
	return base.With(extra...)
}

// modelRow is a row of the models table.
type modelRow struct {
	ID         string  `db:"id"`
	Source     string  `db:"source"`
	Manifest   string  `db:"manifest"`
	Enabled    bool    `db:"enabled"`
	ProviderID *string `db:"provider_id"`
}

func (s *Service) loadCustom(ctx context.Context) ([]*catalog.Manifest, map[string]bool) {
	var rows []modelRow
	if err := s.d.DB.Select(ctx, &rows, `SELECT id, source, manifest, enabled, provider_id FROM models`); err != nil {
		s.log.Warn("ai: load custom models", "err", err)
	}
	var custom []*catalog.Manifest
	disabled := map[string]bool{}
	owner := map[string]string{}
	for _, r := range rows {
		if !r.Enabled {
			disabled[r.ID] = true
		}
		if r.Source != catalog.SourceCustom && r.Source != catalog.SourcePlugin {
			continue
		}
		var m catalog.Manifest
		if err := json.Unmarshal([]byte(r.Manifest), &m); err != nil {
			s.log.Warn("ai: bad custom manifest", "id", r.ID, "err", err)
			continue
		}
		m.ID, m.Source = r.ID, r.Source
		if err := m.Validate(); err != nil {
			s.log.Warn("ai: invalid custom manifest", "id", r.ID, "err", err)
			continue
		}
		if r.ProviderID != nil {
			owner[r.ID] = *r.ProviderID
		}
		custom = append(custom, &m)
	}
	s.mu.Lock()
	s.custom, s.disabled, s.ownerOf, s.customAt = custom, disabled, owner, time.Now()
	s.mu.Unlock()
	return custom, disabled
}

// Disabled returns the models disabled server-wide.
func (s *Service) Disabled(ctx context.Context) map[string]bool {
	s.Catalog(ctx)
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]bool, len(s.disabled))
	for k, v := range s.disabled {
		out[k] = v
	}
	return out
}

// SetModelEnabled enables or disables a model server-wide (admins).
func (s *Service) SetModelEnabled(ctx context.Context, u *core.User, id string, on bool) error {
	if u == nil || !u.IsAdmin() {
		return core.ErrForbidden("")
	}
	c := s.Catalog(ctx)
	m, ok := c.Get(id)
	if !ok {
		return core.ErrNotFound()
	}
	src := m.Source
	if src == catalog.SourceWorker || src == "" {
		src = catalog.SourceBuiltin
	}
	if src == catalog.SourceCustom || src == catalog.SourcePlugin {
		_, err := s.d.DB.Exec(ctx, `UPDATE models SET enabled = ?, updated_at = ? WHERE id = ?`, on, db.Now(), id)
		s.invalidate()
		return err
	}
	if src != catalog.SourceBuiltin && src != catalog.SourceFile {
		src = catalog.SourceBuiltin
	}
	_, err := s.d.DB.Exec(ctx, `INSERT INTO models (id, source, manifest, enabled, created_at, updated_at) VALUES (?, ?, '{}', ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET enabled = excluded.enabled, updated_at = excluded.updated_at`, id, src, on, db.Now(), db.Now())
	s.invalidate()
	return err
}

// SaveCustomModel stores a custom manifest served by a provider (G12 describe step).
func (s *Service) SaveCustomModel(ctx context.Context, u *core.User, providerID string, m *catalog.Manifest) error {
	if err := m.Validate(); err != nil {
		return err
	}
	var existing modelRow
	err := s.d.DB.Get(ctx, &existing, `SELECT id, source, manifest, enabled, provider_id FROM models WHERE id = ?`, m.ID)
	switch {
	case err == nil && (existing.ProviderID == nil || *existing.ProviderID != providerID):
		return &FieldError{Field: "model_id", Key: "ai.error.model_taken"}
	case err != nil && !db.IsNotFound(err):
		return err
	}
	if b, ok := s.Catalog(ctx).Get(m.ID); ok && b.Source != catalog.SourceCustom && b.Source != catalog.SourceWorker {
		return &FieldError{Field: "model_id", Key: "ai.error.model_taken"}
	}
	m.Source = catalog.SourceCustom
	now := db.Now()
	var owner any
	if u != nil {
		owner = u.ID
	}
	_, err = s.d.DB.Exec(ctx, `INSERT INTO models (id, source, manifest, enabled, provider_id, owner_id, created_at, updated_at) VALUES (?, 'custom', ?, TRUE, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET manifest = excluded.manifest, updated_at = excluded.updated_at`, m.ID, db.MustJSON(m), providerID, owner, now, now)
	s.invalidate()
	return err
}

// CustomModelsOf returns the custom models served by a provider.
func (s *Service) CustomModelsOf(ctx context.Context, providerID string) []*catalog.Manifest {
	s.Catalog(ctx)
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*catalog.Manifest
	for _, m := range s.custom {
		if s.ownerOf[m.ID] == providerID {
			out = append(out, m)
		}
	}
	return out
}

// onlineWorkers returns online workers with their GPU sizes.
func (s *Service) onlineWorkers(ctx context.Context) []jobs.Worker {
	ws, err := s.d.Jobs.ListWorkers(ctx)
	if err != nil {
		s.log.Warn("ai: list workers", "err", err)
		return nil
	}
	var out []jobs.Worker
	for _, w := range ws {
		if w.Status == jobs.WorkerOnline {
			out = append(out, w)
		}
	}
	return out
}

// workerManifests synthesises manifests for models workers advertise but the catalogue does not know.
func (s *Service) workerManifests(ctx context.Context, c *catalog.Catalog) []*catalog.Manifest {
	var out []*catalog.Manifest
	seen := map[string]bool{}
	for _, w := range s.onlineWorkers(ctx) {
		for _, id := range w.Models.V {
			if _, ok := c.Get(id); ok || seen[id] || !catalog.ValidID(id) {
				continue
			}
			seen[id] = true
			var tasks []string
			for _, t := range []string{catalog.TaskGenerate, catalog.TaskEdit, catalog.TaskInpaint, catalog.TaskSheet, catalog.TaskTrain, catalog.TaskScore} {
				if slices.Contains(w.JobTypes.V, catalog.JobTypeForTask(t)) {
					tasks = append(tasks, t)
				}
			}
			if len(tasks) == 0 {
				continue
			}
			name := id
			summary := "Advertised by worker " + w.Name + "; add a manifest to describe it."
			if id == "mock" {
				name = "Worker mock model"
				summary = "The worker's built-in mock backend: placeholder art from the worker, no AI."
			}
			out = append(out, &catalog.Manifest{ID: id, Name: name, Maker: w.Name, Kind: catalog.KindImage, Summary: summary,
				Capabilities: catalog.Capabilities{Tasks: tasks, Refs: true, MaxRefs: 4, Edit: slices.Contains(tasks, catalog.TaskEdit),
					Inpaint: slices.Contains(tasks, catalog.TaskInpaint), BWScreentone: true, Colour: true},
				Licence:   catalog.Licence{ID: "unknown", Commercial: true, Note: "Licence not described by a manifest."},
				Hardware:  catalog.Hardware{Local: true},
				Providers: []catalog.Offer{{Provider: providers.KindLocalWorker, ModelID: id, Seconds: 8}},
				Scores:    map[string]float64{catalog.ScorePanel: 20, catalog.ScoreEdit: 20, catalog.ScoreConsistency: 20, catalog.ScoreSpeed: 80},
				Source:    catalog.SourceWorker})
		}
	}
	return out
}

// Guards returns the module-owned guards.
func (s *Service) Guards(ctx context.Context) Guards {
	var g Guards
	if _, err := s.d.Settings.GetKey(ctx, guardsKey, &g); err != nil {
		s.log.Warn("ai: guards", "err", err)
	}
	return g
}

// SaveGuards stores the module-owned guards.
func (s *Service) SaveGuards(ctx context.Context, by string, g Guards) error {
	return s.d.Settings.SetKey(ctx, guardsKey, g, by)
}

// UserMode returns a member's routing override ("" = instance default).
func (s *Service) UserMode(ctx context.Context, userID string) string {
	if userID == "" {
		return ""
	}
	var m string
	err := s.d.DB.Get(ctx, &m, `SELECT routing_mode FROM ai_user_prefs WHERE user_id = ?`, userID)
	if err != nil && !db.IsNotFound(err) {
		s.log.Warn("ai: user mode", "err", err)
	}
	return m
}

// SetUserMode stores a member's routing override ("" clears it).
func (s *Service) SetUserMode(ctx context.Context, userID, mode string) error {
	if mode != "" && !router.ValidMode(mode) {
		return core.ErrValidation(map[string]string{"mode": "unknown routing mode"})
	}
	_, err := s.d.DB.Exec(ctx, `INSERT INTO ai_user_prefs (user_id, routing_mode, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET routing_mode = excluded.routing_mode, updated_at = excluded.updated_at`, userID, mode, db.Now())
	return err
}

// SetInstanceMode stores the instance default routing mode (admins).
func (s *Service) SetInstanceMode(ctx context.Context, by, mode string) error {
	if !router.ValidMode(mode) {
		return core.ErrValidation(map[string]string{"mode": "unknown routing mode"})
	}
	_, err := s.d.Settings.UpdateInstance(ctx, by, func(i *settings.Instance) error { i.RoutingMode = mode; return nil })
	return err
}

// KindName returns the display name of a provider kind.
func (s *Service) KindName(ctx context.Context, kind string) string {
	return i18n.T(ctx, "ai.kind."+kind)
}

// monthKey is "YYYY-MM" (UTC).
func (s *Service) monthKey() string { return s.Now().UTC().Format("2006-01") }

// MonthSpend returns this month's remote spend on own keys: for userID ("" = the whole server).
func (s *Service) MonthSpend(ctx context.Context, userID string) (float64, error) {
	q, args := `SELECT COALESCE(SUM(usd), 0) FROM spend_ledger WHERE month = ? AND kind = 'remote'`, []any{s.monthKey()}
	if userID != "" {
		q += ` AND user_id = ?`
		args = append(args, userID)
	}
	var v float64
	err := s.d.DB.Get(ctx, &v, q, args...)
	return v, err
}

// remaining returns what is left of the monthly limits for userID (-1 = unlimited).
func (s *Service) remaining(ctx context.Context, inst settings.Instance, userID string) float64 {
	rem := -1.0
	if l := inst.Limits.ServerMonthlyRemoteSpendUSD; l > 0 {
		spent, _ := s.MonthSpend(ctx, "")
		rem = max(0, l-spent)
	}
	if l := inst.Limits.MonthlyRemoteSpendUSD; l > 0 && userID != "" {
		spent, _ := s.MonthSpend(ctx, userID)
		r := max(0, l-spent)
		if rem < 0 || r < rem {
			rem = r
		}
	}
	return rem
}

// Inputs builds the router snapshot for a user.
func (s *Service) Inputs(ctx context.Context, userID string) (router.Inputs, error) {
	inst := s.d.Settings.MustInstance(ctx)
	c := s.Catalog(ctx)
	provs, err := s.ListProviders(ctx, userID, false)
	if err != nil {
		return router.Inputs{}, err
	}
	visible := map[string]bool{}
	in := router.Inputs{DefaultMode: inst.RoutingMode, UserMode: s.UserMode(ctx, userID), LocalOnly: inst.LocalOnly,
		LicenceGuard: inst.Content.LicenceGuard, RemainingUSD: s.remaining(ctx, inst, userID), Disabled: s.Disabled(ctx)}
	for i := range provs {
		p := &provs[i]
		visible[p.ID] = true
		in.Providers = append(in.Providers, router.Provider{ID: p.ID, Kind: p.Kind, Name: p.Name, Local: p.Local(), Enabled: p.Enabled, Ready: p.Ready()})
	}
	// Hide custom models of providers this user cannot see.
	s.mu.RLock()
	owner := s.ownerOf
	s.mu.RUnlock()
	var keep []*catalog.Manifest
	for _, m := range c.All() {
		if pid, ok := owner[m.ID]; ok && !visible[pid] {
			continue
		}
		keep = append(keep, m)
	}
	in.Catalog = catalog.New(keep...)
	queued := s.queuedWorkerJobs(ctx)
	online := s.onlineWorkers(ctx)
	for _, w := range online {
		vram := 0
		for _, g := range w.GPUs.V {
			vram = max(vram, g.VRAMTotalMB)
		}
		slots := max(1, w.MaxConcurrent)
		in.Workers = append(in.Workers, router.Worker{ID: w.ID, Name: w.Name, Models: w.Models.V, JobTypes: w.JobTypes.V,
			VRAMGB: float64(vram) / 1024, QueueSeconds: float64(queued) * 20 / float64(slots*max(1, len(online)))})
	}
	in.Pins, err = s.Pins(ctx)
	if err != nil {
		return in, err
	}
	g := s.Guards(ctx)
	in.Learn = g.Learn
	if g.Learn {
		in.Stats = s.stats(ctx)
	} else {
		in.Stats = s.runCounts(ctx)
	}
	return in, nil
}

func (s *Service) queuedWorkerJobs(ctx context.Context) int {
	var n int
	_ = s.d.DB.Get(ctx, &n, `SELECT COUNT(*) FROM jobs WHERE state IN ('queued', 'running') AND executor = 'worker'`)
	return n
}

// stats returns keep/reroll statistics per model.
func (s *Service) stats(ctx context.Context) map[string]router.Stat {
	var rows []struct {
		ModelID  string `db:"model_id"`
		Runs     int64  `db:"runs"`
		Failures int64  `db:"failures"`
		Keeps    int64  `db:"keeps"`
		Rerolls  int64  `db:"rerolls"`
	}
	if err := s.d.DB.Select(ctx, &rows, `SELECT model_id, SUM(runs) AS runs, SUM(failures) AS failures, SUM(keeps) AS keeps, SUM(rerolls) AS rerolls
		FROM model_stats GROUP BY model_id`); err != nil {
		s.log.Warn("ai: stats", "err", err)
	}
	out := map[string]router.Stat{}
	for _, r := range rows {
		out[r.ModelID] = router.Stat{Runs: r.Runs, Failures: r.Failures, Keeps: r.Keeps, Rerolls: r.Rerolls}
	}
	return out
}

// runCounts returns only run counts (custom models join routing once they have results).
func (s *Service) runCounts(ctx context.Context) map[string]router.Stat {
	full := s.stats(ctx)
	for k, v := range full {
		full[k] = router.Stat{Runs: v.Runs}
	}
	return full
}

// Pins returns every routing pin.
func (s *Service) Pins(ctx context.Context) ([]router.Pin, error) {
	var out []router.Pin
	err := s.d.DB.Select(ctx, &out, `SELECT id, scope_kind, scope_id, task, model_id, provider_id, mode, label FROM routing_pins ORDER BY created_at`)
	return out, err
}

// ScopeAuthorizer lets the studio decide who may pin models for a project,
// series or character (provide it with core.Provide[ai.ScopeAuthorizer]).
// Without it only admins pin those scopes.
type ScopeAuthorizer interface {
	CanPin(ctx context.Context, u *core.User, scopeKind, scopeID string) bool
}

// AddPin creates or replaces a pin.
func (s *Service) AddPin(ctx context.Context, u *core.User, p router.Pin) (*router.Pin, error) {
	if u == nil {
		return nil, core.ErrUnauthorized()
	}
	if !slices.Contains(router.PinScopes, p.ScopeKind) {
		return nil, &FieldError{Field: "scope_kind", Key: "ai.error.pin_scope"}
	}
	if p.ScopeKind == router.PinServer {
		p.ScopeID = ""
	} else if strings.TrimSpace(p.ScopeID) == "" {
		return nil, &FieldError{Field: "scope_id", Key: "ai.error.pin_scope_id"}
	}
	if p.Task != "" && !slices.Contains(catalog.Tasks, p.Task) {
		return nil, &FieldError{Field: "task", Key: "ai.error.pin_task"}
	}
	allowed := u.IsAdmin()
	if !allowed {
		if a, ok := core.Use[ScopeAuthorizer](s.d); ok {
			allowed = (p.ScopeKind == router.PinProject || p.ScopeKind == router.PinSeries || p.ScopeKind == router.PinCharacter) && a.CanPin(ctx, u, p.ScopeKind, p.ScopeID)
		}
	}
	if !allowed {
		return nil, core.ErrForbidden("")
	}
	pinType := "model"
	switch {
	case p.Mode != "":
		if !router.ValidMode(p.Mode) {
			return nil, &FieldError{Field: "mode", Key: "ai.error.pin_mode"}
		}
		pinType, p.ModelID, p.ProviderID = "mode", "", ""
	case p.ModelID == "":
		return nil, &FieldError{Field: "model_id", Key: "ai.error.pin_model"}
	default:
		if _, ok := s.Catalog(ctx).Get(p.ModelID); !ok {
			return nil, &FieldError{Field: "model_id", Key: "ai.error.pin_model"}
		}
	}
	p.Label = strings.TrimSpace(p.Label)
	if len(p.Label) > 120 {
		p.Label = p.Label[:120]
	}
	p.ID = db.NewID()
	err := s.d.DB.WithTx(ctx, func(tx *db.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM routing_pins WHERE scope_kind = ? AND scope_id = ? AND task = ? AND pin_type = ?`, p.ScopeKind, p.ScopeID, p.Task, pinType); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO routing_pins (id, scope_kind, scope_id, task, pin_type, model_id, provider_id, mode, label, created_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, p.ID, p.ScopeKind, p.ScopeID, p.Task, pinType, p.ModelID, p.ProviderID, p.Mode, p.Label, u.ID, db.Now())
		return err
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// DeletePin removes a pin.
func (s *Service) DeletePin(ctx context.Context, u *core.User, id string) error {
	if u == nil {
		return core.ErrUnauthorized()
	}
	var p router.Pin
	if err := s.d.DB.Get(ctx, &p, `SELECT id, scope_kind, scope_id, task, model_id, provider_id, mode, label FROM routing_pins WHERE id = ?`, id); err != nil {
		if db.IsNotFound(err) {
			return core.ErrNotFound()
		}
		return err
	}
	allowed := u.IsAdmin()
	if !allowed {
		if a, ok := core.Use[ScopeAuthorizer](s.d); ok {
			allowed = p.ScopeKind != router.PinServer && a.CanPin(ctx, u, p.ScopeKind, p.ScopeID)
		}
	}
	if !allowed {
		return core.ErrForbidden("")
	}
	_, err := s.d.DB.Exec(ctx, `DELETE FROM routing_pins WHERE id = ?`, id)
	return err
}

// Route decides for need (reading the prompt with the intent reader or rules).
func (s *Service) Route(ctx context.Context, need router.Need, prompt string) (*router.Decision, error) {
	if prompt != "" {
		router.ReadPrompt(&need, prompt)
		s.mu.RLock()
		ir := s.intent
		s.mu.RUnlock()
		if ir != nil {
			if err := ir.ReadIntent(ctx, prompt, &need); err != nil {
				s.log.Debug("ai: intent reader", "err", err)
			}
		}
	}
	in, err := s.Inputs(ctx, need.UserID)
	if err != nil {
		return nil, err
	}
	return router.Route(in, need), nil
}

// sortedKeys is a helper for stable output.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// errorf wraps with the ai prefix.
func errorf(format string, args ...any) error { return fmt.Errorf("ai: "+format, args...) }
