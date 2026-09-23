package auth

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// Token kinds and their prefixes.
const (
	TokenAgent  = httpx.TokenAgent
	TokenWorker = httpx.TokenWorker
	TokenAPI    = httpx.TokenAPI
)

// TokenPrefixes maps kinds to the visible prefix of raw tokens.
var TokenPrefixes = map[string]string{TokenAgent: "mva_", TokenWorker: "mvw_", TokenAPI: "mvk_"}

// EnvWorkerTokenID identifies the MV_WORKER_TOKEN shared secret in worker records.
const EnvWorkerTokenID = "env"

// TokenInfo is a stored token (never with the secret).
type TokenInfo struct {
	ID             string            `db:"id" json:"id"`
	UserID         string            `db:"user_id" json:"user_id"`
	Kind           string            `db:"kind" json:"kind"`
	Name           string            `db:"name" json:"name"`
	Prefix         string            `db:"prefix" json:"prefix"`
	Scopes         db.JSON[[]string] `db:"scopes" json:"scopes"`
	ProjectIDs     db.JSON[[]string] `db:"project_ids" json:"project_ids"`
	ApproveOverUSD sql.NullFloat64   `db:"approve_over_usd" json:"-"`
	LastUsedAt     *time.Time        `db:"last_used_at" json:"last_used_at,omitempty"`
	LastUsedIP     string            `db:"last_used_ip" json:"last_used_ip,omitempty"`
	ExpiresAt      *time.Time        `db:"expires_at" json:"expires_at,omitempty"`
	RevokedAt      *time.Time        `db:"revoked_at" json:"revoked_at,omitempty"`
	CreatedAt      time.Time         `db:"created_at" json:"created_at"`
}

// Token converts to the request-identity form.
func (t *TokenInfo) Token() *httpx.Token {
	tok := &httpx.Token{ID: t.ID, Kind: t.Kind, Name: t.Name, UserID: t.UserID, Scopes: t.Scopes.V, ProjectIDs: t.ProjectIDs.V}
	if tok.Scopes == nil {
		tok.Scopes = []string{}
	}
	if tok.ProjectIDs == nil {
		tok.ProjectIDs = []string{}
	}
	if t.ApproveOverUSD.Valid {
		v := t.ApproveOverUSD.Float64
		tok.ApproveOverUSD = &v
	}
	return tok
}

// TokenInput describes a new bearer token.
type TokenInput struct {
	UserID         string
	Kind           string
	Name           string
	Scopes         []string
	ProjectIDs     []string
	ApproveOverUSD *float64
	ExpiresAt      *time.Time
}

const tokenCols = `id, user_id, kind, name, prefix, scopes, project_ids, approve_over_usd, last_used_at, last_used_ip, expires_at, revoked_at, created_at`

// CreateToken issues a bearer token. The raw token is returned once and only its hash is stored.
func (s *Service) CreateToken(ctx context.Context, in TokenInput) (string, *TokenInfo, error) {
	prefix, ok := TokenPrefixes[in.Kind]
	if !ok {
		return "", nil, fieldError("kind", "error.validation")
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 80 {
		return "", nil, fieldError("name", "error.validation")
	}
	if in.Scopes == nil {
		in.Scopes = []string{}
	}
	if in.ProjectIDs == nil {
		in.ProjectIDs = []string{}
	}
	u, err := s.UserByID(ctx, in.UserID)
	if err != nil {
		return "", nil, err
	}
	if in.Kind == TokenWorker && !u.IsAdmin() {
		return "", nil, httpx.ErrForbidden("only admins can create worker tokens")
	}
	raw := prefix + randomToken(30)
	info := &TokenInfo{ID: db.NewID(), UserID: in.UserID, Kind: in.Kind, Name: in.Name, Prefix: raw[:len(prefix)+6],
		Scopes: db.NewJSON(in.Scopes), ProjectIDs: db.NewJSON(in.ProjectIDs), ExpiresAt: in.ExpiresAt, CreatedAt: s.now()}
	var approve any
	if in.ApproveOverUSD != nil {
		approve = *in.ApproveOverUSD
		info.ApproveOverUSD = sql.NullFloat64{Float64: *in.ApproveOverUSD, Valid: true}
	}
	var exp any
	if in.ExpiresAt != nil {
		exp = in.ExpiresAt.UTC()
	}
	_, err = s.db.Exec(ctx, `INSERT INTO api_tokens (id, user_id, kind, name, prefix, token_hash, scopes, project_ids, approve_over_usd, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, info.ID, info.UserID, info.Kind, info.Name, info.Prefix, hashToken(raw),
		db.MustJSON(in.Scopes), db.MustJSON(in.ProjectIDs), approve, exp, info.CreatedAt)
	if err != nil {
		return "", nil, err
	}
	s.Audit(ctx, AuditEntry{Action: "token.create", TargetKind: "token", TargetID: info.ID, Data: map[string]any{"kind": in.Kind, "name": in.Name, "scopes": in.Scopes}})
	return raw, info, nil
}

// VerifyToken resolves a raw bearer token. Agent and API tokens act as their
// user (returned); worker tokens return no user. The MV_WORKER_TOKEN shared
// secret is accepted as a worker token with ID "env".
func (s *Service) VerifyToken(ctx context.Context, raw, ip string) (*httpx.Token, *User, error) {
	if raw == "" || len(raw) > 200 {
		return nil, nil, ErrTokenInvalid
	}
	if wt := s.opts.WorkerToken; wt != "" && subtle.ConstantTimeCompare([]byte(raw), []byte(wt)) == 1 {
		return &httpx.Token{ID: EnvWorkerTokenID, Kind: TokenWorker, Name: "MV_WORKER_TOKEN", Scopes: []string{"*"}, ProjectIDs: []string{}}, nil, nil
	}
	known := false
	for _, p := range TokenPrefixes {
		if strings.HasPrefix(raw, p) {
			known = true
		}
	}
	if !known {
		return nil, nil, ErrTokenInvalid
	}
	var t TokenInfo
	err := s.db.Get(ctx, &t, `SELECT `+tokenCols+` FROM api_tokens WHERE token_hash = ?`, hashToken(raw))
	if err != nil {
		if db.IsNotFound(err) {
			return nil, nil, ErrTokenInvalid
		}
		return nil, nil, err
	}
	now := s.now()
	if t.RevokedAt != nil || (t.ExpiresAt != nil && !t.ExpiresAt.After(now)) {
		return nil, nil, ErrTokenInvalid
	}
	u, err := s.UserByID(ctx, t.UserID)
	if err != nil || u.Status != StatusActive {
		return nil, nil, ErrTokenInvalid
	}
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) > time.Minute {
		_, _ = s.db.Exec(ctx, `UPDATE api_tokens SET last_used_at = ?, last_used_ip = ? WHERE id = ?`, now, ip, t.ID)
	}
	if t.Kind == TokenWorker {
		return t.Token(), nil, nil
	}
	return t.Token(), u, nil
}

// ListTokens lists a user's tokens of kind ("" = all kinds), newest first. Admins pass userID "" to list every worker token.
func (s *Service) ListTokens(ctx context.Context, userID, kind string) ([]TokenInfo, error) {
	q, args := `SELECT `+tokenCols+` FROM api_tokens WHERE revoked_at IS NULL`, []any{}
	if userID != "" {
		q += ` AND user_id = ?`
		args = append(args, userID)
	}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	var out []TokenInfo
	err := s.db.Select(ctx, &out, q+` ORDER BY created_at DESC`, args...)
	return out, err
}

// GetToken returns one token.
func (s *Service) GetToken(ctx context.Context, id string) (*TokenInfo, error) {
	var t TokenInfo
	if err := s.db.Get(ctx, &t, `SELECT `+tokenCols+` FROM api_tokens WHERE id = ?`, id); err != nil {
		if db.IsNotFound(err) {
			return nil, httpx.ErrNotFound()
		}
		return nil, err
	}
	return &t, nil
}

// RevokeToken revokes a token. userID must own it unless asAdmin is true.
func (s *Service) RevokeToken(ctx context.Context, userID, tokenID string, asAdmin bool) error {
	q, args := `UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, []any{s.now(), tokenID}
	if !asAdmin {
		q += ` AND user_id = ?`
		args = append(args, userID)
	}
	res, err := s.db.Exec(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return httpx.ErrNotFound()
	}
	s.Audit(ctx, AuditEntry{Action: "token.revoke", TargetKind: "token", TargetID: tokenID})
	return nil
}
