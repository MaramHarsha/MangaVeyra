package ai

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/secrets"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

const (
	providerKindOpenRouter = "openrouter"
	providerScopeServer    = "server"
	providerScopeUser      = "user"
	maxProviderNameRunes   = 120
	maxProviderKeyBytes    = 4096
)

var (
	errProviderNotFound    = errors.New("ai: provider not found")
	errProviderForbidden   = errors.New("ai: provider access denied")
	errProviderInvalid     = errors.New("ai: invalid provider")
	errProviderUnavailable = errors.New("ai: provider unavailable")
)

// instanceReader is deliberately private so tests can prove settings failures
// make every remote path fail closed.
type instanceReader interface {
	Instance(context.Context) (settings.Instance, error)
}

type providerActor struct {
	UserID string
	Admin  bool
}

type providerCreate struct {
	// Kind is a remote route kind (remoteRoutes); empty means OpenRouter.
	Kind    string
	BaseURL string
	Local   localTextConfig
	Draft   bool
	Name    string
	Key     string
	Scope   string
	Model   string
}

// providerUpdate is an ordinary metadata update. Key is accepted only so form
// callers can submit a blank password field; non-blank replacement must use
// ReplaceKey, which makes the secret-changing action explicit.
type providerUpdate struct {
	Name string
	Key  string
}

type providerTestState struct {
	State     string    `json:"state,omitempty"`
	Step      string    `json:"step,omitempty"`
	Time      time.Time `json:"time,omitempty"`
	RequestID string    `json:"request_id,omitempty"`
}

// providerView is the only representation suitable for a response or page.
// It intentionally has no ciphertext, plaintext key, base URL, or raw test
// response.
type providerView struct {
	ID         string            `json:"id"`
	Kind       string            `json:"kind"`
	Name       string            `json:"name"`
	KeyHint    string            `json:"key_hint"`
	Enabled    bool              `json:"enabled"`
	Scope      string            `json:"scope"`
	OwnerID    string            `json:"owner_id,omitempty"`
	LastTest   providerTestState `json:"last_test"`
	LocalModel string            `json:"local_model,omitempty"`
	Endpoint   string            `json:"endpoint,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

type providerRecord struct {
	ID        string         `db:"id"`
	Kind      string         `db:"kind"`
	Name      string         `db:"name"`
	BaseURL   string         `db:"base_url"`
	KeyEnc    string         `db:"key_enc"`
	KeyHint   string         `db:"key_hint"`
	Config    string         `db:"config"`
	Enabled   bool           `db:"enabled"`
	Scope     string         `db:"scope"`
	OwnerID   sql.NullString `db:"owner_id"`
	LastTest  string         `db:"last_test"`
	CreatedAt time.Time      `db:"created_at"`
	UpdatedAt time.Time      `db:"updated_at"`
}

type providerCandidate struct {
	ID    string
	Kind  string
	Scope string
	Model string
}

type dispatchProvider struct {
	ID      string
	Kind    string
	Scope   string
	OwnerID string
	Key     string
	KeyEnc  string
	KeyHint string
	Enabled bool
	BaseURL string
	Local   localTextConfig
}

type providerStore struct {
	db       *db.DB
	box      *secrets.Box
	settings instanceReader
	now      func() time.Time
}

func newProviderStore(d *db.DB, box *secrets.Box, st instanceReader, now func() time.Time) *providerStore {
	if now == nil {
		now = db.Now
	}
	return &providerStore{db: d, box: box, settings: st, now: now}
}

func providerKeyAAD(id string) string { return "provider:" + id + ":key" }

func (s *providerStore) Create(ctx context.Context, actor providerActor, in providerCreate) (providerView, error) {
	var zero providerView
	if s == nil || s.db == nil || s.box == nil {
		return zero, errProviderUnavailable
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Key = strings.TrimSpace(in.Key)
	in.Kind = strings.TrimSpace(in.Kind)
	if in.Kind == "" {
		in.Kind = providerKindOpenRouter
	}
	if !validProviderKind(in.Kind) || !validProviderName(in.Name) || (in.Kind != providerKindLocalText && !validProviderKey(in.Key)) || (in.Key != "" && !validProviderKey(in.Key)) {
		return zero, errProviderInvalid
	}
	config := "{}"
	if in.Kind == providerKindLocalText {
		if validateLocalTextConfig(in.BaseURL, in.Local) != nil {
			return zero, errProviderInvalid
		}
		raw, _ := json.Marshal(in.Local)
		config = string(raw)
	} else if in.BaseURL != "" {
		return zero, errProviderInvalid
	}
	owner, err := s.ownerForCreate(ctx, actor, in.Scope)
	if err != nil {
		return zero, err
	}
	id := db.NewID() // allocate before sealing: the row id is authenticated data
	now := s.timeNow()
	keyEnc, hint := "", ""
	if in.Key != "" {
		keyEnc = s.box.SealString(in.Key, providerKeyAAD(id))
		hint = secrets.Hint(in.Key)
	}
	_, err = s.db.Exec(ctx, `INSERT INTO providers (
		id, kind, name, base_url, key_enc, key_hint, config, enabled, scope, owner_id, last_test, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '{}', ?, ?)`,
		id, in.Kind, in.Name, in.BaseURL, keyEnc, hint, config, !in.Draft, in.Scope, db.NullString(owner), now, now)
	if err != nil {
		return zero, fmt.Errorf("ai: create provider: %w", err)
	}
	return s.GetManageable(ctx, actor, id)
}

func (s *providerStore) Update(ctx context.Context, actor providerActor, id string, in providerUpdate) (providerView, error) {
	var zero providerView
	in.Name = strings.TrimSpace(in.Name)
	if !validProviderName(in.Name) || strings.TrimSpace(in.Key) != "" {
		return zero, errProviderInvalid
	}
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		r, err := providerByID(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if err := authorizeProvider(actor, r); err != nil {
			return err
		}
		if err := validateProviderRecord(r); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE providers SET name = ?, updated_at = ? WHERE id = ?`, in.Name, s.timeNow(), id)
		return err
	})
	if err != nil {
		return zero, sanitizeProviderStoreError("update", err)
	}
	return s.GetManageable(ctx, actor, id)
}

func (s *providerStore) ReplaceKey(ctx context.Context, actor providerActor, id, plaintext string) (providerView, error) {
	var zero providerView
	plaintext = strings.TrimSpace(plaintext)
	if !validProviderKey(plaintext) {
		return zero, errProviderInvalid
	}
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		r, err := providerByID(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if err := authorizeProvider(actor, r); err != nil {
			return err
		}
		if err := validateProviderRecord(r); err != nil {
			return err
		}
		sealed := s.box.SealString(plaintext, providerKeyAAD(r.ID))
		_, err = tx.Exec(ctx, `UPDATE providers
			SET key_enc = ?, key_hint = ?, last_test = '{}', updated_at = ? WHERE id = ?`,
			sealed, secrets.Hint(plaintext), s.timeNow(), r.ID)
		return err
	})
	if err != nil {
		return zero, sanitizeProviderStoreError("replace key", err)
	}
	return s.GetManageable(ctx, actor, id)
}

func (s *providerStore) SetEnabled(ctx context.Context, actor providerActor, id string, enabled bool) (providerView, error) {
	var zero providerView
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		r, err := providerByID(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if err := authorizeProvider(actor, r); err != nil {
			return err
		}
		if err := validateProviderRecord(r); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE providers SET enabled = ?, updated_at = ? WHERE id = ?`, enabled, s.timeNow(), id)
		return err
	})
	if err != nil {
		return zero, sanitizeProviderStoreError("set enabled", err)
	}
	return s.GetManageable(ctx, actor, id)
}

func (s *providerStore) Delete(ctx context.Context, actor providerActor, id string) error {
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		r, err := providerByID(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if err := authorizeProvider(actor, r); err != nil {
			return err
		}
		if err := validateProviderRecord(r); err != nil {
			return err
		}
		res, err := tx.Exec(ctx, `DELETE FROM providers WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return errProviderNotFound
		}
		return nil
	})
	return sanitizeProviderStoreError("delete", err)
}

func (s *providerStore) GetManageable(ctx context.Context, actor providerActor, id string) (providerView, error) {
	var zero providerView
	r, err := providerByID(ctx, s.db, id, false)
	if err != nil {
		return zero, err
	}
	if err := authorizeProvider(actor, r); err != nil {
		return zero, err
	}
	if err := validateProviderRecord(r); err != nil {
		return zero, err
	}
	return r.view(), nil
}

// ListManageable returns only providers this browser actor may administer.
// Disabled rows remain visible so their keys and lifecycle can be managed.
// The returned view type is deliberately secret-free.
func (s *providerStore) ListManageable(ctx context.Context, actor providerActor) ([]providerView, error) {
	if s == nil || s.db == nil || strings.TrimSpace(actor.UserID) == "" {
		return nil, errProviderUnavailable
	}
	query := `SELECT ` + providerColumns + ` FROM providers WHERE kind IN (?, ?, ?, ?) AND `
	args := []any{providerKindOpenRouter, providerKindOpenAI, providerKindFal, providerKindLocalText}
	if actor.Admin {
		query += `((scope = 'server' AND owner_id IS NULL) OR (scope = 'user' AND owner_id = ?))`
		args = append(args, actor.UserID)
	} else {
		query += `(scope = 'user' AND owner_id = ?)`
		args = append(args, actor.UserID)
	}
	query += ` ORDER BY CASE WHEN scope = 'server' THEN 0 ELSE 1 END, created_at, id`
	var rows []providerRecord
	if err := s.db.Select(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("ai: list manageable providers: %w", err)
	}
	out := make([]providerView, 0, len(rows))
	for _, row := range rows {
		if err := validateProviderRecord(row); err != nil {
			return nil, err
		}
		if err := authorizeProvider(actor, row); err != nil {
			return nil, err
		}
		out = append(out, row.view())
	}
	return out, nil
}

func (s *providerStore) Candidates(ctx context.Context, actorID string) ([]providerCandidate, error) {
	if s == nil || s.db == nil || strings.TrimSpace(actorID) == "" {
		return nil, errProviderUnavailable
	}
	var rows []providerRecord
	err := s.db.Select(ctx, &rows, `SELECT `+providerColumns+` FROM providers
		WHERE kind IN (?, ?, ?, ?) AND enabled = TRUE AND (key_enc <> '' OR kind = 'openai-compatible-local')
			AND ((scope = 'user' AND owner_id = ?) OR (scope = 'server' AND owner_id IS NULL))
		ORDER BY CASE WHEN scope = 'user' THEN 0 ELSE 1 END, id`, providerKindOpenRouter, providerKindOpenAI, providerKindFal, providerKindLocalText, actorID)
	if err != nil {
		return nil, fmt.Errorf("ai: list providers: %w", err)
	}
	out := make([]providerCandidate, 0, len(rows))
	for _, r := range rows {
		if validateProviderRecord(r) == nil {
			out = append(out, providerCandidate{ID: r.ID, Kind: r.Kind, Scope: r.Scope, Model: r.localConfig().Model})
		}
	}
	return out, nil
}

func (s *providerStore) Candidate(ctx context.Context, actorID, id string) (providerCandidate, error) {
	var zero providerCandidate
	if strings.TrimSpace(actorID) == "" || strings.TrimSpace(id) == "" {
		return zero, errProviderUnavailable
	}
	r, err := providerByID(ctx, s.db, id, false)
	if err != nil {
		return zero, errProviderUnavailable
	}
	if err := validateProviderRecord(r); err != nil || !r.Enabled || (r.Kind != providerKindLocalText && strings.TrimSpace(r.KeyEnc) == "") {
		return zero, errProviderUnavailable
	}
	if r.Scope == providerScopeUser && (!r.OwnerID.Valid || r.OwnerID.String != actorID) {
		return zero, errProviderUnavailable
	}
	return providerCandidate{ID: r.ID, Kind: r.Kind, Scope: r.Scope, Model: r.localConfig().Model}, nil
}

func (s *providerStore) ResolveDispatch(ctx context.Context, actorID, id string) (dispatchProvider, error) {
	var zero dispatchProvider
	if s == nil || s.box == nil || strings.TrimSpace(actorID) == "" {
		return zero, errProviderUnavailable
	}
	r, err := providerByID(ctx, s.db, id, false)
	if err != nil || validateProviderRecord(r) != nil || !r.Enabled || (r.Kind != providerKindLocalText && strings.TrimSpace(r.KeyEnc) == "") {
		return zero, errProviderUnavailable
	}
	if r.Scope == providerScopeUser && r.OwnerID.String != actorID {
		return zero, errProviderUnavailable
	}
	key := ""
	if r.KeyEnc != "" {
		key, err = s.box.OpenString(r.KeyEnc, providerKeyAAD(r.ID))
	}
	if err != nil || (r.Kind != providerKindLocalText && strings.TrimSpace(key) == "") {
		return zero, errProviderUnavailable
	}
	owner := ""
	if r.OwnerID.Valid {
		owner = r.OwnerID.String
	}
	return dispatchProvider{ID: r.ID, Kind: r.Kind, Scope: r.Scope, OwnerID: owner, Key: key,
		KeyEnc: r.KeyEnc, KeyHint: r.KeyHint, Enabled: r.Enabled, BaseURL: r.BaseURL, Local: r.localConfig()}, nil
}

func (s *providerStore) recordTest(ctx context.Context, providerID, expectedKeyEnc, state, requestID string) error {
	if state != "ok" && state != "error" {
		return errProviderInvalid
	}
	test := providerTestState{State: state, Time: s.timeNow(), RequestID: sanitizeRequestID(requestID)}
	raw, err := json.Marshal(test)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(ctx, `UPDATE providers SET last_test = ?, updated_at = ?
		WHERE id = ? AND key_enc = ?`, string(raw), s.timeNow(), providerID, expectedKeyEnc)
	if err != nil {
		return fmt.Errorf("ai: record provider test: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errProviderUnavailable
	}
	return nil
}

func (s *providerStore) ownerForCreate(ctx context.Context, actor providerActor, scope string) (string, error) {
	switch scope {
	case providerScopeServer:
		if !actor.Admin {
			return "", errProviderForbidden
		}
		return "", nil
	case providerScopeUser:
		if strings.TrimSpace(actor.UserID) == "" {
			return "", errProviderForbidden
		}
		if !actor.Admin {
			inst, err := loadRemoteSettings(ctx, s.settings)
			if err != nil || !inst.UsersMayAddProviders {
				return "", errProviderForbidden
			}
		}
		return actor.UserID, nil
	default:
		return "", errProviderInvalid
	}
}

func loadRemoteSettings(ctx context.Context, reader instanceReader) (settings.Instance, error) {
	if reader == nil {
		return settings.Instance{}, errors.New("ai: settings unavailable")
	}
	inst, err := reader.Instance(ctx)
	if err != nil {
		return settings.Instance{}, errors.New("ai: settings unavailable")
	}
	if err := inst.Validate(); err != nil {
		return settings.Instance{}, errors.New("ai: settings invalid")
	}
	return inst, nil
}

func (s *providerStore) timeNow() time.Time {
	if s == nil || s.now == nil {
		return db.Now()
	}
	return s.now().UTC().Truncate(time.Microsecond)
}

const providerColumns = `id, kind, name, base_url, key_enc, key_hint, config, enabled, scope, owner_id,
	last_test, created_at, updated_at`

func providerByID(ctx context.Context, q db.Querier, id string, lock bool) (providerRecord, error) {
	var r providerRecord
	query := `SELECT ` + providerColumns + ` FROM providers WHERE id = ?`
	if lock && q.Dialect() == db.Postgres {
		query += ` FOR UPDATE`
	}
	if err := q.Get(ctx, &r, query, id); err != nil {
		if db.IsNotFound(err) {
			return r, errProviderNotFound
		}
		return r, fmt.Errorf("ai: read provider: %w", err)
	}
	r.CreatedAt = r.CreatedAt.UTC()
	r.UpdatedAt = r.UpdatedAt.UTC()
	return r, nil
}

func validateProviderRecord(r providerRecord) error {
	if !db.ValidID(r.ID) || !validProviderKind(r.Kind) || !validProviderName(r.Name) || (r.Kind != providerKindLocalText && (r.BaseURL != "" || r.Config != "{}")) {
		return errProviderInvalid
	}
	if r.Kind == providerKindLocalText && validateLocalTextConfig(r.BaseURL, r.localConfig()) != nil {
		return errProviderInvalid
	}
	switch r.Scope {
	case providerScopeServer:
		if r.OwnerID.Valid {
			return errProviderInvalid
		}
	case providerScopeUser:
		if !r.OwnerID.Valid || strings.TrimSpace(r.OwnerID.String) == "" {
			return errProviderInvalid
		}
	default:
		return errProviderInvalid
	}
	return nil
}

func authorizeProvider(actor providerActor, r providerRecord) error {
	switch r.Scope {
	case providerScopeServer:
		if !actor.Admin || r.OwnerID.Valid {
			return errProviderForbidden
		}
	case providerScopeUser:
		if !r.OwnerID.Valid || actor.UserID == "" || r.OwnerID.String != actor.UserID {
			return errProviderForbidden
		}
	default:
		return errProviderForbidden
	}
	return nil
}

func (r providerRecord) view() providerView {
	last := providerTestState{}
	if strings.TrimSpace(r.LastTest) != "" && r.LastTest != "{}" {
		if err := json.Unmarshal([]byte(r.LastTest), &last); err != nil || (last.State != "ok" && last.State != "error" && !(last.State == "" && customStepNumber(last.Step) > 0)) {
			last = providerTestState{State: "error"}
		}
		last.RequestID = sanitizeRequestID(last.RequestID)
		last.Time = last.Time.UTC()
	}
	owner := ""
	if r.OwnerID.Valid {
		owner = r.OwnerID.String
	}
	return providerView{ID: r.ID, Kind: r.Kind, Name: r.Name, KeyHint: r.KeyHint, Enabled: r.Enabled,
		Scope: r.Scope, OwnerID: owner, LastTest: last, LocalModel: r.localConfig().Model, Endpoint: r.BaseURL, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

// validProviderKey accepts a non-empty key of visible ASCII only. A key with
// spaces or control characters can never be sent as an Authorization header,
// so it would make every call fail (and be charged as an unknown outcome).
func validProviderKey(key string) bool {
	if key == "" || len(key) > maxProviderKeyBytes {
		return false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x21 || key[i] > 0x7e {
			return false
		}
	}
	return true
}

func validProviderName(name string) bool {
	name = strings.TrimSpace(name)
	return name != "" && len([]rune(name)) <= maxProviderNameRunes
}

func sanitizeProviderStoreError(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errProviderNotFound) || errors.Is(err, errProviderForbidden) ||
		errors.Is(err, errProviderInvalid) || errors.Is(err, errProviderUnavailable) {
		return err
	}
	return fmt.Errorf("ai: provider %s failed: %w", op, err)
}

func sanitizeRequestID(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > 128 {
		v = v[:128]
	}
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return ""
	}
	return v
}
