package auth

import (
	"context"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// AuditEntry is one audit log record. Actor, token and IP are taken from the
// request context when not set explicitly.
type AuditEntry struct {
	ActorID    string
	ActorKind  string // user | agent | worker | system (derived from the context when empty)
	TokenID    string
	Action     string // dotted verb, e.g. "user.set_role", "page.unlock", "agent.generate"
	TargetKind string
	TargetID   string
	ProjectID  string
	IP         string
	Data       map[string]any
}

// AuditRecord is a stored audit entry.
type AuditRecord struct {
	ID         string                  `db:"id" json:"id"`
	ActorID    *string                 `db:"actor_id" json:"actor_id,omitempty"`
	ActorKind  string                  `db:"actor_kind" json:"actor_kind"`
	TokenID    *string                 `db:"token_id" json:"token_id,omitempty"`
	Action     string                  `db:"action" json:"action"`
	TargetKind string                  `db:"target_kind" json:"target_kind"`
	TargetID   string                  `db:"target_id" json:"target_id"`
	ProjectID  *string                 `db:"project_id" json:"project_id,omitempty"`
	IP         string                  `db:"ip" json:"ip"`
	Data       db.JSON[map[string]any] `db:"data" json:"data"`
	CreatedAt  time.Time               `db:"created_at" json:"created_at"`
}

// Audit appends an entry to the audit log. Failures are logged, never returned:
// auditing must not break the action being audited.
func (s *Service) Audit(ctx context.Context, e AuditEntry) {
	id := httpx.IdentityFrom(ctx)
	if e.ActorID == "" && id.User != nil {
		e.ActorID = id.User.ID
	}
	if e.TokenID == "" && id.Token != nil {
		e.TokenID = id.Token.ID
	}
	if e.ActorKind == "" {
		e.ActorKind = id.ActorKind()
		if e.ActorKind == "" {
			e.ActorKind = "system"
			if e.ActorID != "" {
				e.ActorKind = "user"
			}
		}
	}
	if e.IP == "" {
		e.IP = httpx.ClientIPFrom(ctx)
	}
	if e.Data == nil {
		e.Data = map[string]any{}
	}
	var actor any
	if e.ActorID != "" && e.TokenID != EnvWorkerTokenID {
		actor = e.ActorID
	}
	_, err := s.db.Exec(context.WithoutCancel(ctx), `INSERT INTO audit_log (id, actor_id, actor_kind, token_id, action, target_kind, target_id, project_id, ip, data, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, db.NewID(), actor, e.ActorKind, db.NullString(e.TokenID), e.Action,
		e.TargetKind, e.TargetID, db.NullString(e.ProjectID), e.IP, db.MustJSON(e.Data), s.now())
	if err != nil {
		s.log.WarnContext(ctx, "auth: audit write failed", "action", e.Action, "err", err)
	}
}

// AuditFilter filters ListAudit.
type AuditFilter struct {
	ActorID    string
	Action     string // prefix match, e.g. "user."
	TargetKind string
	TargetID   string
	ProjectID  string
	Before     string // id cursor
	Limit      int
}

// ListAudit returns audit entries, newest first.
func (s *Service) ListAudit(ctx context.Context, f AuditFilter) ([]AuditRecord, error) {
	q, args := `SELECT id, actor_id, actor_kind, token_id, action, target_kind, target_id, project_id, ip, data, created_at FROM audit_log WHERE 1=1`, []any{}
	if f.ActorID != "" {
		q, args = q+` AND actor_id = ?`, append(args, f.ActorID)
	}
	if f.Action != "" {
		q, args = q+` AND action LIKE ?`, append(args, f.Action+"%")
	}
	if f.TargetKind != "" {
		q, args = q+` AND target_kind = ?`, append(args, f.TargetKind)
	}
	if f.TargetID != "" {
		q, args = q+` AND target_id = ?`, append(args, f.TargetID)
	}
	if f.ProjectID != "" {
		q, args = q+` AND project_id = ?`, append(args, f.ProjectID)
	}
	if f.Before != "" {
		q, args = q+` AND id < ?`, append(args, f.Before)
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	var out []AuditRecord
	err := s.db.Select(ctx, &out, q+` ORDER BY id DESC LIMIT ?`, append(args, f.Limit)...)
	return out, err
}
