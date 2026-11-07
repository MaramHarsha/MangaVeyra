// Package settings stores typed instance settings (admin-controlled, one set
// per server) and per-user preferences, both as JSON in the database with
// defaults applied on read.
//
// Instance settings live in instance_settings as one row per top-level field
// (key = JSON field name), so a partial update never clobbers unrelated
// fields written by another admin at the same time. Modules may also keep
// their own keys through GetKey/SetKey (use a "module." prefix, e.g. "ai.catalog").
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// Instance modes.
const (
	ModePersonal = "personal" // one creator, sign-up closed, Explore private
	ModePrivate  = "private"  // a group: sign-up by invite, Explore visible to members
	ModePublic   = "public"   // open community: Explore and series pages public
)

// Sign-up modes.
const (
	SignUpClosed = "closed"
	SignUpInvite = "invite"
	SignUpOpen   = "open"
)

// Routing modes.
const (
	RoutingBalanced  = "balanced"
	RoutingBest      = "best"
	RoutingFastest   = "fastest"
	RoutingLocalOnly = "local_only"
	RoutingFreeOnly  = "free_only"
)

// Instance holds the admin-controlled server settings.
type Instance struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// PublicURL overrides MV_PUBLIC_URL when set from the setup wizard.
	PublicURL string `json:"public_url"`
	Mode      string `json:"mode"`
	SignUp    string `json:"sign_up"`
	// DefaultRole is given to people who sign up without an invite role.
	DefaultRole string `json:"default_role"`
	// RequireApproval puts open sign-ups in "pending" until an admin approves them.
	RequireApproval bool   `json:"require_approval"`
	Rules           string `json:"rules"`
	DefaultLocale   string `json:"default_locale"`
	// Telemetry is always off by default; there is no telemetry endpoint in this build.
	Telemetry bool `json:"telemetry"`
	// LocalOnly guarantees no prompt or image leaves the server: remote providers, MCP client and World are blocked.
	LocalOnly   bool        `json:"local_only"`
	RoutingMode string      `json:"routing_mode"`
	Limits      Limits      `json:"limits"`
	Content     Content     `json:"content"`
	World       World       `json:"world"`
	Maintenance Maintenance `json:"maintenance"`
	NightMode   NightMode   `json:"night_mode"`
	// UsersMayAddProviders lets non-admins connect their own provider keys (user scope).
	UsersMayAddProviders bool `json:"users_may_add_providers"`
	// SetupCompletedAt is set by the first-run wizard.
	SetupCompletedAt *time.Time `json:"setup_completed_at,omitempty"`
}

// Limits are per-user fair-share limits.
type Limits struct {
	// RunningGPUJobs caps worker jobs running at once per user.
	RunningGPUJobs int `json:"running_gpu_jobs"`
	// RunningJobs caps Go-executor jobs (remote APIs, exports) running at once per user.
	RunningJobs int `json:"running_jobs"`
	// StorageQuotaMB caps media per user (0 = unlimited).
	StorageQuotaMB int `json:"storage_quota_mb"`
	// MonthlyRemoteSpendUSD caps estimated spend on server-scope remote keys per user and month (0 = unlimited).
	MonthlyRemoteSpendUSD float64 `json:"monthly_remote_spend_usd"`
	// ServerMonthlyRemoteSpendUSD caps the whole server's remote spend per month (0 = unlimited).
	ServerMonthlyRemoteSpendUSD float64 `json:"server_monthly_remote_spend_usd"`
	MaxUploadMB                 int     `json:"max_upload_mb"`
	MaxProjectsPerUser          int     `json:"max_projects_per_user"`
}

// Content holds content rules.
type Content struct {
	AllowMature      bool     `json:"allow_mature"`
	AllowAdult       bool     `json:"allow_adult"`
	NSFWFilter       bool     `json:"nsfw_filter"`
	DefaultAgeRating string   `json:"default_age_rating"`
	RequireAILabels  bool     `json:"require_ai_labels"`
	BlockedWords     []string `json:"blocked_words"`
	// LicenceGuard warns before non-commercial models are used on public series.
	LicenceGuard bool `json:"licence_guard"`
}

// World controls the optional World hub link. Off until an admin links the server.
type World struct {
	Enabled bool `json:"enabled"`
	ShowTab bool `json:"show_tab"`
}

// Maintenance puts the server in maintenance mode (admins still get in).
type Maintenance struct {
	Enabled bool   `json:"enabled"`
	Message string `json:"message"`
}

// NightMode defers batch jobs to quiet hours (server local time).
type NightMode struct {
	Enabled   bool `json:"enabled"`
	StartHour int  `json:"start_hour"`
	EndHour   int  `json:"end_hour"`
}

// InWindow reports whether t falls inside the night window.
func (n NightMode) InWindow(t time.Time) bool {
	h := t.Hour()
	if n.StartHour == n.EndHour {
		return true
	}
	if n.StartHour < n.EndHour {
		return h >= n.StartHour && h < n.EndHour
	}
	return h >= n.StartHour || h < n.EndHour
}

// DefaultInstance returns the defaults for a fresh server.
func DefaultInstance() Instance {
	return Instance{
		Name:          "MangaVeyra",
		Mode:          ModePrivate,
		SignUp:        SignUpInvite,
		DefaultRole:   "creator",
		DefaultLocale: "en",
		RoutingMode:   RoutingBalanced,
		Limits: Limits{
			RunningGPUJobs:        2,
			RunningJobs:           4,
			StorageQuotaMB:        20480,
			MonthlyRemoteSpendUSD: 20,
			MaxUploadMB:           50,
			MaxProjectsPerUser:    0,
		},
		Content: Content{
			DefaultAgeRating: "all",
			RequireAILabels:  true,
			NSFWFilter:       true,
			LicenceGuard:     true,
		},
		World:     World{Enabled: false, ShowTab: true},
		NightMode: NightMode{StartHour: 1, EndHour: 7},
	}
}

// Validate checks enumerations and ranges.
func (i Instance) Validate() error {
	var errs []error
	if strings.TrimSpace(i.Name) == "" || len(i.Name) > 80 {
		errs = append(errs, errors.New("name must be 1–80 characters"))
	}
	if !oneOf(i.Mode, ModePersonal, ModePrivate, ModePublic) {
		errs = append(errs, fmt.Errorf("mode %q must be personal, private or public", i.Mode))
	}
	if !oneOf(i.SignUp, SignUpClosed, SignUpInvite, SignUpOpen) {
		errs = append(errs, fmt.Errorf("sign_up %q must be closed, invite or open", i.SignUp))
	}
	if !oneOf(i.DefaultRole, "creator", "reader") {
		errs = append(errs, fmt.Errorf("default_role %q must be creator or reader", i.DefaultRole))
	}
	if !oneOf(i.RoutingMode, RoutingBalanced, RoutingBest, RoutingFastest, RoutingLocalOnly, RoutingFreeOnly) {
		errs = append(errs, fmt.Errorf("routing_mode %q is not a routing mode", i.RoutingMode))
	}
	if !oneOf(i.Content.DefaultAgeRating, "all", "teen", "mature", "adult") {
		errs = append(errs, fmt.Errorf("content.default_age_rating %q is invalid", i.Content.DefaultAgeRating))
	}
	if i.Limits.RunningGPUJobs < 1 || i.Limits.RunningJobs < 1 {
		errs = append(errs, errors.New("running job limits must be at least 1"))
	}
	if !validRemoteSpendLimit(i.Limits.MonthlyRemoteSpendUSD) || !validRemoteSpendLimit(i.Limits.ServerMonthlyRemoteSpendUSD) {
		errs = append(errs, errors.New("remote spend limits must be finite, non-negative and fit in microdollars"))
	}
	if i.Limits.StorageQuotaMB < 0 || i.Limits.MaxUploadMB < 0 {
		errs = append(errs, errors.New("limits cannot be negative"))
	}
	if i.NightMode.StartHour < 0 || i.NightMode.StartHour > 23 || i.NightMode.EndHour < 0 || i.NightMode.EndHour > 23 {
		errs = append(errs, errors.New("night mode hours must be 0–23"))
	}
	return errors.Join(errs...)
}

func validRemoteSpendLimit(v float64) bool {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return false
	}
	scaled := v * 1_000_000
	return !math.IsInf(scaled, 0) && scaled < float64(math.MaxInt64)
}

// ExplorePublic reports whether Explore and public series are visible to anonymous visitors.
func (i Instance) ExplorePublic() bool { return i.Mode == ModePublic }

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// Service reads and writes settings with a short-lived cache.
type Service struct {
	db  *db.DB
	ttl time.Duration

	mu       sync.Mutex
	cached   *Instance
	cachedAt time.Time
	watchers []func(Instance)
}

// New creates the settings service.
func New(d *db.DB) *Service { return &Service{db: d, ttl: 3 * time.Second} }

// Instance returns the current instance settings with defaults for unset fields.
func (s *Service) Instance(ctx context.Context) (Instance, error) {
	s.mu.Lock()
	if s.cached != nil && time.Since(s.cachedAt) < s.ttl {
		v := *s.cached
		s.mu.Unlock()
		return v, nil
	}
	s.mu.Unlock()

	inst, err := loadInstance(ctx, s.db)
	if err != nil {
		return DefaultInstance(), err
	}
	s.mu.Lock()
	s.cached, s.cachedAt = &inst, time.Now()
	s.mu.Unlock()
	return inst, nil
}

// InstanceTx reads the current instance settings uncached through tx. Policy
// decisions that linearize under a database lock use this method so a cached
// snapshot cannot outlive a concurrent committed settings update.
func (s *Service) InstanceTx(ctx context.Context, tx *db.Tx) (Instance, error) {
	if s == nil || tx == nil {
		return DefaultInstance(), errors.New("settings: transaction is not configured")
	}
	return loadInstance(ctx, tx)
}

func loadInstance(ctx context.Context, q db.Querier) (Instance, error) {
	rows := []struct {
		Key   string `db:"key"`
		Value string `db:"value"`
	}{}
	if err := q.Select(ctx, &rows, `SELECT key, value FROM instance_settings WHERE key NOT LIKE '%.%'`); err != nil {
		return DefaultInstance(), fmt.Errorf("settings: load: %w", err)
	}
	merged := map[string]json.RawMessage{}
	def, _ := json.Marshal(DefaultInstance())
	_ = json.Unmarshal(def, &merged)
	for _, r := range rows {
		if _, known := merged[r.Key]; known || r.Key == "setup_completed_at" {
			merged[r.Key] = json.RawMessage(r.Value)
		}
	}
	// Nested objects: overlay stored fields on the defaults so new fields keep defaults.
	inst := DefaultInstance()
	b, _ := json.Marshal(merged)
	if err := json.Unmarshal(b, &inst); err != nil {
		return DefaultInstance(), fmt.Errorf("settings: decode: %w", err)
	}
	return inst, nil
}

// MustInstance returns the instance settings, or the defaults if the database cannot be read.
func (s *Service) MustInstance(ctx context.Context) Instance {
	i, _ := s.Instance(ctx)
	return i
}

// UpdateInstance applies fn to the current settings, validates and stores the
// top-level fields that changed. by is the acting admin's user id (may be "").
func (s *Service) UpdateInstance(ctx context.Context, by string, fn func(*Instance) error) (Instance, error) {
	cur, err := s.Instance(ctx)
	if err != nil {
		return cur, err
	}
	next := cur
	// deep copy slices
	next.Content.BlockedWords = append([]string(nil), cur.Content.BlockedWords...)
	if err := fn(&next); err != nil {
		return cur, err
	}
	if err := next.Validate(); err != nil {
		return cur, fmt.Errorf("settings: %w", err)
	}
	oldFields, newFields := fields(cur), fields(next)
	now := db.Now()
	err = s.db.WithTx(ctx, func(tx *db.Tx) error {
		for k, v := range newFields {
			if string(oldFields[k]) == string(v) {
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO instance_settings (key, value, updated_by, updated_at) VALUES (?, ?, ?, ?)
				ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
				k, string(v), db.NullString(by), now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return cur, fmt.Errorf("settings: save: %w", err)
	}
	s.Invalidate()
	for _, w := range s.watcherList() {
		w(next)
	}
	return next, nil
}

func fields(i Instance) map[string]json.RawMessage {
	b, _ := json.Marshal(i)
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(b, &m)
	return m
}

// Invalidate drops the cache (call after writing instance_settings directly).
func (s *Service) Invalidate() {
	s.mu.Lock()
	s.cached = nil
	s.mu.Unlock()
}

// OnChange registers fn to run after every successful UpdateInstance in this process.
func (s *Service) OnChange(fn func(Instance)) {
	s.mu.Lock()
	s.watchers = append(s.watchers, fn)
	s.mu.Unlock()
}

func (s *Service) watcherList() []func(Instance) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]func(Instance){}, s.watchers...)
}

// GetKey loads a module-owned setting stored under key (must contain a dot,
// e.g. "ai.routing") into dest. It returns false when the key is unset.
func (s *Service) GetKey(ctx context.Context, key string, dest any) (bool, error) {
	if !strings.Contains(key, ".") {
		return false, fmt.Errorf("settings: module keys need a namespace prefix, got %q", key)
	}
	var raw string
	err := s.db.Get(ctx, &raw, `SELECT value FROM instance_settings WHERE key = ?`, key)
	if db.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), dest)
}

// SetKey stores a module-owned setting (see GetKey).
func (s *Service) SetKey(ctx context.Context, key string, v any, by string) error {
	if !strings.Contains(key, ".") {
		return fmt.Errorf("settings: module keys need a namespace prefix, got %q", key)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO instance_settings (key, value, updated_by, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		key, string(b), db.NullString(by), db.Now())
	return err
}

// DeleteKey removes a module-owned setting.
func (s *Service) DeleteKey(ctx context.Context, key string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM instance_settings WHERE key = ?`, key)
	return err
}
