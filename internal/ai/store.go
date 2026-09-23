package ai

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/ai/providers"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/core/netguard"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/secrets"
)

// Provider scopes.
const (
	ScopeServer = "server"
	ScopeUser   = "user"
)

// The built-in providers always exist and are not stored.
const (
	PlaceholderID = "builtin-placeholder"
	LocalWorkerID = "builtin-local-worker"
)

// KindInfo describes a provider kind for forms and cards.
type KindInfo struct {
	Kind string
	// Icon is a Tabler icon name.
	Icon string
	// NeedsKey / NeedsURL say which fields the connect form shows.
	NeedsKey bool
	NeedsURL bool
	// DefaultURL is prefilled (and used when the URL is left empty).
	DefaultURL string
	// KeyURL is where the user gets a key.
	KeyURL string
	// Custom kinds are the four G12 connection types.
	Custom bool
	// LocalByDefault kinds usually run on the operator's network.
	LocalByDefault bool
}

// Kinds lists every provider kind users can add, in display order.
var Kinds = []KindInfo{
	{Kind: providers.KindOpenRouter, Icon: "route", NeedsKey: true, DefaultURL: "https://openrouter.ai/api/v1", KeyURL: "https://openrouter.ai/settings/keys"},
	{Kind: providers.KindFal, Icon: "bolt", NeedsKey: true, DefaultURL: "https://queue.fal.run", KeyURL: "https://fal.ai/dashboard/keys"},
	{Kind: providers.KindOpenAI, Icon: "brand-openai", NeedsKey: true, DefaultURL: "https://api.openai.com/v1", KeyURL: "https://platform.openai.com/api-keys"},
	{Kind: providers.KindGoogle, Icon: "brand-google", NeedsKey: true, DefaultURL: "https://generativelanguage.googleapis.com/v1beta", KeyURL: "https://aistudio.google.com/apikey"},
	{Kind: providers.KindXAI, Icon: "letter-x", NeedsKey: true, DefaultURL: "https://api.x.ai/v1", KeyURL: "https://console.x.ai"},
	{Kind: providers.KindAnthropic, Icon: "message-chatbot", NeedsKey: true, DefaultURL: "https://api.anthropic.com", KeyURL: "https://console.anthropic.com/settings/keys"},
	{Kind: providers.KindReplicate, Icon: "copy", NeedsKey: true, DefaultURL: "https://api.replicate.com/v1", KeyURL: "https://replicate.com/account/api-tokens"},
	{Kind: providers.KindTogether, Icon: "atom", NeedsKey: true, DefaultURL: "https://api.together.xyz/v1", KeyURL: "https://api.together.ai/settings/api-keys"},
	{Kind: providers.KindRunware, Icon: "rocket", NeedsKey: true, DefaultURL: "https://api.runware.ai/v1", KeyURL: "https://my.runware.ai/keys"},
	{Kind: providers.KindHuggingFace, Icon: "mood-smile", NeedsKey: true, Custom: true, KeyURL: "https://huggingface.co/settings/tokens"},
	{Kind: providers.KindOllama, Icon: "server-2", NeedsURL: true, DefaultURL: "http://localhost:11434", LocalByDefault: true},
	{Kind: providers.KindOpenAICompatible, Icon: "plug-connected", NeedsURL: true, NeedsKey: true, Custom: true},
	{Kind: providers.KindComfyUI, Icon: "topology-star-3", NeedsURL: true, DefaultURL: "http://localhost:8188", Custom: true, LocalByDefault: true},
	{Kind: providers.KindHTTP, Icon: "world-code", NeedsURL: true, NeedsKey: true, Custom: true},
}

// KindInfoFor returns the info of kind.
func KindInfoFor(kind string) (KindInfo, bool) {
	for _, k := range Kinds {
		if k.Kind == kind {
			return k, true
		}
	}
	switch kind {
	case providers.KindPlaceholder:
		return KindInfo{Kind: kind, Icon: "square-dot"}, true
	case providers.KindLocalWorker:
		return KindInfo{Kind: kind, Icon: "cpu"}, true
	}
	return KindInfo{}, false
}

// ProviderConfig is the non-secret configuration stored with a provider.
type ProviderConfig struct {
	// Options holds kind-specific settings (ComfyUI workflow + mapping, HTTP templates).
	Options json.RawMessage `json:"options,omitempty"`
	// Local is set when the URL points at the operator's own network (local only mode keeps it).
	Local bool `json:"local,omitempty"`
	// PrivateApproved lets a ComfyUI / Ollama / OpenAI-compatible provider use a private address (admin approval).
	PrivateApproved bool `json:"private_approved,omitempty"`
	// Draft is true until the add-provider flow finishes.
	Draft bool `json:"draft,omitempty"`
	// Models are the provider models discovered or typed in (custom connections, aggregators).
	Models []providers.RemoteModel `json:"models,omitempty"`
	// ModelID is the model a custom connection serves (the provider-side id).
	ModelID string `json:"model_id,omitempty"`
	// CostPerImage is the user-entered cost on their own key when the provider publishes none.
	CostPerImage float64 `json:"cost_per_image,omitempty"`
	// Note is a free description.
	Note string `json:"note,omitempty"`
}

// TestResult is the outcome of the last "Test".
type TestResult struct {
	At      time.Time `json:"at"`
	OK      bool      `json:"ok"`
	Code    string    `json:"code,omitempty"`
	Message string    `json:"message,omitempty"`
	Seconds float64   `json:"seconds"`
	// CostUSD is the cost the provider reported (or the published estimate); Local marks local GPU.
	CostUSD float64 `json:"cost_usd"`
	Local   bool    `json:"local,omitempty"`
	// ImageKey is the stored test image; Text the test answer.
	ImageKey string `json:"image_key,omitempty"`
	Text     string `json:"text,omitempty"`
	Model    string `json:"model,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
}

// Provider is a configured provider (the key itself is never included).
type Provider struct {
	ID        string                  `db:"id" json:"id"`
	Kind      string                  `db:"kind" json:"kind"`
	Name      string                  `db:"name" json:"name"`
	BaseURL   string                  `db:"base_url" json:"base_url"`
	KeyEnc    string                  `db:"key_enc" json:"-"`
	KeyHint   string                  `db:"key_hint" json:"key_hint,omitempty"`
	Config    db.JSON[ProviderConfig] `db:"config" json:"config"`
	Enabled   bool                    `db:"enabled" json:"enabled"`
	Scope     string                  `db:"scope" json:"scope"`
	OwnerID   sql.NullString          `db:"owner_id" json:"-"`
	LastTest  db.JSON[TestResult]     `db:"last_test" json:"last_test"`
	CreatedAt time.Time               `db:"created_at" json:"created_at"`
	UpdatedAt time.Time               `db:"updated_at" json:"updated_at"`
	// Builtin marks the always-present providers (placeholder, local worker).
	Builtin bool `db:"-" json:"builtin,omitempty"`
}

// HasKey reports whether a key is saved.
func (p *Provider) HasKey() bool { return p.KeyEnc != "" }

// Owner returns the owner user id ("" for server scope).
func (p *Provider) Owner() string { return p.OwnerID.String }

// Local reports whether calls stay on the operator's network.
func (p *Provider) Local() bool {
	switch p.Kind {
	case providers.KindPlaceholder, providers.KindLocalWorker:
		return true
	}
	return p.Config.V.Local
}

// Ready reports whether the provider can be called (key saved when one is needed, not a draft).
func (p *Provider) Ready() bool {
	if p.Builtin {
		return true
	}
	if p.Config.V.Draft {
		return false
	}
	info, _ := KindInfoFor(p.Kind)
	if info.NeedsKey && !info.NeedsURL && !p.HasKey() {
		return false
	}
	if info.NeedsURL && p.BaseURL == "" {
		return false
	}
	return true
}

const providerCols = `id, kind, name, base_url, key_enc, key_hint, config, enabled, scope, owner_id, last_test, created_at, updated_at`

func keyAAD(id string) string { return "provider:" + id + ":key" }

// ErrProvidersNotAllowed is returned when members may not add providers.
var ErrProvidersNotAllowed = core.NewError(403, "providers_not_allowed", "the admin has not allowed members to add their own providers")

// builtins returns the placeholder and local worker providers.
func builtins() []Provider {
	return []Provider{
		{ID: PlaceholderID, Kind: providers.KindPlaceholder, Name: providers.PlaceholderLabel, Enabled: true, Scope: ScopeServer, Builtin: true},
		{ID: LocalWorkerID, Kind: providers.KindLocalWorker, Name: "Local worker", Enabled: true, Scope: ScopeServer, Builtin: true},
	}
}

// ListProviders returns the providers userID may use: server providers, the
// user's own providers and the built-ins. userID "" returns server providers only;
// all=true returns every provider (admin view).
func (s *Service) ListProviders(ctx context.Context, userID string, all bool) ([]Provider, error) {
	q, args := `SELECT `+providerCols+` FROM providers WHERE scope = 'server'`, []any{}
	switch {
	case all:
		q = `SELECT ` + providerCols + ` FROM providers WHERE 1=1`
	case userID != "":
		q += ` OR (scope = 'user' AND owner_id = ?)`
		args = append(args, userID)
	}
	var list []Provider
	if err := s.d.DB.Select(ctx, &list, q+` ORDER BY created_at`, args...); err != nil {
		return nil, err
	}
	return append(builtins(), list...), nil
}

// GetProvider loads a provider (built-ins included).
func (s *Service) GetProvider(ctx context.Context, id string) (*Provider, error) {
	for _, b := range builtins() {
		if b.ID == id {
			return &b, nil
		}
	}
	var p Provider
	if err := s.d.DB.Get(ctx, &p, `SELECT `+providerCols+` FROM providers WHERE id = ?`, id); err != nil {
		if db.IsNotFound(err) {
			return nil, core.ErrNotFound()
		}
		return nil, err
	}
	return &p, nil
}

// CanManage reports whether u may change p.
func CanManage(u *core.User, p *Provider) bool {
	if u == nil || p.Builtin {
		return false
	}
	if u.IsAdmin() {
		return true
	}
	return p.Scope == ScopeUser && p.Owner() == u.ID
}

// CanUse reports whether u may route jobs to p.
func CanUse(u *core.User, p *Provider) bool {
	if p.Builtin || p.Scope == ScopeServer {
		return true
	}
	return u != nil && p.Owner() == u.ID
}

// ProviderInput creates or updates a provider.
type ProviderInput struct {
	Kind    string
	Name    string
	BaseURL string
	// Key is set to replace the key; KeepKey keeps the stored one when Key is empty.
	Key     string
	KeepKey bool
	Scope   string
	Options json.RawMessage
	ModelID string
	Note    string
	// CostPerImage is the user-entered cost on their own key (nil keeps the stored value).
	CostPerImage *float64
	Draft        bool
}

// policyFor returns the SSRF policy for a provider kind and approval.
func (s *Service) policyFor(kind string, approved bool) netguard.Policy {
	private := s.d.Cfg.AllowPrivateProviderURLs
	if approved && (kind == providers.KindComfyUI || kind == providers.KindOllama || kind == providers.KindOpenAICompatible || kind == providers.KindLocalWorker) {
		private = true
	}
	return netguard.Policy{AllowPrivate: private, AllowHTTP: private}
}

// FieldError is a form field problem with an i18n key.
type FieldError struct {
	Field string
	Key   string
	Args  []any
	Err   error
}

func (e *FieldError) Error() string {
	if e.Err != nil {
		return e.Field + ": " + e.Err.Error()
	}
	return e.Field + ": " + e.Key
}

// localCapable reports kinds that can run on the operator's own network.
// Cloud vendors stay remote even behind a private proxy: prompts still leave.
func localCapable(kind string) bool {
	switch kind {
	case providers.KindComfyUI, providers.KindOllama, providers.KindOpenAICompatible, providers.KindHTTP, providers.KindHuggingFace:
		return true
	}
	return false
}

// isPrivateURL reports whether raw points at loopback or a private network (literal or resolved).
func isPrivateURL(ctx context.Context, raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".home.arpa") {
		return true
	}
	addrs := []netip.Addr{}
	if ip, err := netip.ParseAddr(host); err == nil {
		addrs = append(addrs, ip)
	} else {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		res, err := net.DefaultResolver.LookupNetIP(cctx, "ip", host)
		if err != nil {
			return false
		}
		addrs = res
	}
	for _, a := range addrs {
		a = a.Unmap()
		if !(a.IsLoopback() || a.IsPrivate()) {
			return false
		}
	}
	return len(addrs) > 0
}

// SaveProvider creates (id "") or updates a provider after validating its URL
// against the SSRF guard and sealing the key.
func (s *Service) SaveProvider(ctx context.Context, u *core.User, id string, in ProviderInput) (*Provider, error) {
	if u == nil {
		return nil, core.ErrUnauthorized()
	}
	inst := s.d.Settings.MustInstance(ctx)
	var cur *Provider
	if id != "" {
		p, err := s.GetProvider(ctx, id)
		if err != nil {
			return nil, err
		}
		if !CanManage(u, p) {
			return nil, core.ErrForbidden("")
		}
		cur = p
		in.Kind = p.Kind
		in.Scope = p.Scope
	}
	info, ok := KindInfoFor(in.Kind)
	if !ok || in.Kind == providers.KindPlaceholder || in.Kind == providers.KindLocalWorker {
		return nil, &FieldError{Field: "kind", Key: "ai.error.kind"}
	}
	if in.Scope == "" {
		in.Scope = ScopeUser
		if u.IsAdmin() {
			in.Scope = ScopeServer
		}
	}
	if in.Scope == ScopeServer && !u.IsAdmin() {
		return nil, core.ErrForbidden("")
	}
	if in.Scope == ScopeUser && !u.IsAdmin() && !inst.UsersMayAddProviders {
		return nil, ErrProvidersNotAllowed
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		in.Name = s.KindName(ctx, in.Kind)
	}
	if len(in.Name) > 80 {
		return nil, &FieldError{Field: "name", Key: "ai.error.name"}
	}
	in.BaseURL = strings.TrimSpace(in.BaseURL)
	if in.BaseURL == "" && !info.Custom && info.DefaultURL != "" && !info.NeedsURL {
		in.BaseURL = info.DefaultURL
	}
	if info.NeedsURL && in.BaseURL == "" && !in.Draft {
		return nil, &FieldError{Field: "base_url", Key: "ai.error.url_required"}
	}
	cfg := ProviderConfig{}
	if cur != nil {
		cfg = cur.Config.V
	}
	approved := cfg.PrivateApproved
	if u.IsAdmin() && (info.LocalByDefault || in.Kind == providers.KindOpenAICompatible) {
		// An admin connecting a local runtime approves its private address.
		approved = true
	}
	if in.BaseURL != "" {
		pol := s.policyFor(in.Kind, approved)
		if _, err := pol.CheckURL(ctx, in.BaseURL, nil); err != nil {
			key := "ai.error.url_blocked"
			if errors.Is(err, netguard.ErrBlocked) && !pol.AllowPrivate && isPrivateURL(ctx, in.BaseURL) {
				key = "ai.error.url_private"
			}
			return nil, &FieldError{Field: "base_url", Key: key, Err: err}
		}
		cfg.Local = localCapable(in.Kind) && isPrivateURL(ctx, in.BaseURL)
	} else {
		cfg.Local = false
	}
	cfg.PrivateApproved = approved
	if in.Options != nil {
		cfg.Options = in.Options
	}
	if in.ModelID != "" || cur == nil {
		cfg.ModelID = strings.TrimSpace(in.ModelID)
	}
	if in.Note != "" || cur == nil {
		cfg.Note = strings.TrimSpace(in.Note)
	}
	if in.CostPerImage != nil && *in.CostPerImage >= 0 {
		cfg.CostPerImage = *in.CostPerImage
	}
	cfg.Draft = in.Draft
	key := strings.TrimSpace(in.Key)
	if info.NeedsKey && !info.NeedsURL && key == "" && (cur == nil || !cur.HasKey()) && !in.Draft {
		return nil, &FieldError{Field: "key", Key: "ai.error.key_required"}
	}
	if len(key) > 4096 {
		return nil, &FieldError{Field: "key", Key: "ai.error.key_long"}
	}
	now := db.Now()
	if cur == nil {
		id = db.NewID()
		var owner any
		if in.Scope == ScopeUser {
			owner = u.ID
		}
		enc, hint := "", ""
		if key != "" {
			enc, hint = s.d.Secrets.SealString(key, keyAAD(id)), secrets.Hint(key)
		}
		if _, err := s.d.DB.Exec(ctx, `INSERT INTO providers (id, kind, name, base_url, key_enc, key_hint, config, enabled, scope, owner_id, last_test, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '{}', ?, ?)`, id, in.Kind, in.Name, in.BaseURL, enc, hint, db.MustJSON(cfg), true, in.Scope, owner, now, now); err != nil {
			return nil, err
		}
		s.d.Auth.Audit(ctx, auth.AuditEntry{Action: "ai.provider.create", TargetKind: "provider", TargetID: id, Data: map[string]any{"kind": in.Kind, "scope": in.Scope}})
	} else {
		q := `UPDATE providers SET name = ?, base_url = ?, config = ?, updated_at = ?`
		args := []any{in.Name, in.BaseURL, db.MustJSON(cfg), now}
		if key != "" {
			q += `, key_enc = ?, key_hint = ?`
			args = append(args, s.d.Secrets.SealString(key, keyAAD(id)), secrets.Hint(key))
		} else if !in.KeepKey && cur.HasKey() && info.NeedsKey && info.NeedsURL {
			// Custom connections may clear the key (an open local server needs none).
			q += `, key_enc = '', key_hint = ''`
		}
		if _, err := s.d.DB.Exec(ctx, q+` WHERE id = ?`, append(args, id)...); err != nil {
			return nil, err
		}
		s.d.Auth.Audit(ctx, auth.AuditEntry{Action: "ai.provider.update", TargetKind: "provider", TargetID: id, Data: map[string]any{"key_changed": key != ""}})
	}
	s.invalidate()
	return s.GetProvider(ctx, id)
}

// UpdateProviderConfig changes a provider's stored configuration.
func (s *Service) UpdateProviderConfig(ctx context.Context, id string, fn func(*ProviderConfig)) error {
	p, err := s.GetProvider(ctx, id)
	if err != nil {
		return err
	}
	if p.Builtin {
		return core.ErrBadRequest("built-in providers cannot be changed")
	}
	cfg := p.Config.V
	fn(&cfg)
	_, err = s.d.DB.Exec(ctx, `UPDATE providers SET config = ?, updated_at = ? WHERE id = ?`, db.MustJSON(cfg), db.Now(), id)
	s.invalidate()
	return err
}

// SetProviderEnabled turns a provider on or off.
func (s *Service) SetProviderEnabled(ctx context.Context, u *core.User, id string, on bool) error {
	p, err := s.GetProvider(ctx, id)
	if err != nil {
		return err
	}
	if !CanManage(u, p) {
		return core.ErrForbidden("")
	}
	if _, err := s.d.DB.Exec(ctx, `UPDATE providers SET enabled = ?, updated_at = ? WHERE id = ?`, on, db.Now(), id); err != nil {
		return err
	}
	s.d.Auth.Audit(ctx, auth.AuditEntry{Action: "ai.provider.enable", TargetKind: "provider", TargetID: id, Data: map[string]any{"enabled": on}})
	s.invalidate()
	return nil
}

// ApprovePrivate lets an admin approve a private address for a local runtime provider.
func (s *Service) ApprovePrivate(ctx context.Context, u *core.User, id string) error {
	if u == nil || !u.IsAdmin() {
		return core.ErrForbidden("")
	}
	p, err := s.GetProvider(ctx, id)
	if err != nil {
		return err
	}
	switch p.Kind {
	case providers.KindComfyUI, providers.KindOllama, providers.KindOpenAICompatible:
	default:
		return core.ErrBadRequest("only ComfyUI, Ollama and OpenAI-compatible providers can use private addresses")
	}
	s.d.Auth.Audit(ctx, auth.AuditEntry{Action: "ai.provider.approve_private", TargetKind: "provider", TargetID: id})
	return s.UpdateProviderConfig(ctx, id, func(c *ProviderConfig) {
		c.PrivateApproved = true
		c.Local = localCapable(p.Kind) && isPrivateURL(ctx, p.BaseURL)
	})
}

// DeleteProvider removes a provider (its custom models go with it).
func (s *Service) DeleteProvider(ctx context.Context, u *core.User, id string) error {
	p, err := s.GetProvider(ctx, id)
	if err != nil {
		return err
	}
	if !CanManage(u, p) {
		return core.ErrForbidden("")
	}
	if _, err := s.d.DB.Exec(ctx, `DELETE FROM providers WHERE id = ?`, id); err != nil {
		return err
	}
	_, _ = s.d.DB.Exec(ctx, `DELETE FROM routing_pins WHERE provider_id = ?`, id)
	s.d.Auth.Audit(ctx, auth.AuditEntry{Action: "ai.provider.delete", TargetKind: "provider", TargetID: id, Data: map[string]any{"kind": p.Kind}})
	s.invalidate()
	return nil
}

// adapterConfig builds the adapter configuration with the decrypted key.
func (s *Service) adapterConfig(p *Provider) (providers.Config, error) {
	key := ""
	if p.KeyEnc != "" {
		k, err := s.d.Secrets.OpenString(p.KeyEnc, keyAAD(p.ID))
		if err != nil {
			return providers.Config{}, fmt.Errorf("ai: provider %s key cannot be decrypted (was the master key changed?): %w", p.ID, err)
		}
		key = k
	}
	return providers.Config{ID: p.ID, Kind: p.Kind, Name: p.Name, BaseURL: p.BaseURL, APIKey: key, Options: p.Config.V.Options}, nil
}
