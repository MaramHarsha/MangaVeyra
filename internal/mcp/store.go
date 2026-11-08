package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// Agent action kinds (agent_actions.kind).
const (
	kindScriptEdit      = "script_edit"
	kindCharacterCreate = "character_create"
	kindCharacterEdit   = "character_edit"
	kindGenerate        = "generate"
	kindRequest         = "request"
)

// Agent action statuses (agent_actions.status).
const (
	statusDone     = "done"
	statusUndone   = "undone"
	statusPending  = "pending"
	statusApproved = "approved"
	statusDeclined = "declined"
)

// Requested human-only actions (agent_actions.action for kind request), plus
// "generate" for generation estimated above the token's approval threshold.
const (
	reqPublish       = "publish"
	reqSchedule      = "schedule"
	reqDeletePage    = "delete_page"
	reqDeleteProject = "delete_project"
	reqShare         = "share"
	reqChangeRole    = "change_role"
	reqUnlock        = "unlock"
	reqGenerate      = "generate"
)

// humanActions are the actions only a person can take (product rule). They are
// never tools; an agent can only ask for them.
var humanActions = []string{reqPublish, reqSchedule, reqDeletePage, reqDeleteProject, reqShare, reqChangeRole, reqUnlock}

// maxPendingPerToken bounds how many requests one token can leave waiting.
const maxPendingPerToken = 20

// genRequest is a generation request exactly as an agent asked for it. A
// pending request stores it so a person approves precisely this work, on the
// same revision.
type genRequest struct {
	Kind              string    `json:"kind"` // script | page | region
	ProjectID         string    `json:"project_id"`
	ChapterID         string    `json:"chapter_id,omitempty"`
	PageID            string    `json:"page_id,omitempty"`
	LayerID           string    `json:"layer_id,omitempty"`
	MaskAssetID       string    `json:"mask_asset_id,omitempty"`
	Prompt            string    `json:"prompt,omitempty"`
	Mode              string    `json:"mode,omitempty"`
	Premise           string    `json:"premise,omitempty"`
	PagesTarget       int       `json:"pages_target,omitempty"`
	ExpectedVersion   int       `json:"expected_version,omitempty"`
	ExpectedUpdatedAt time.Time `json:"expected_updated_at,omitzero"`
	RoutingMode       string    `json:"routing_mode,omitempty"`
}

// actionDetail holds what G13 and list_recent_actions show about an action.
type actionDetail struct {
	ChapterNumber int    `json:"chapter_number,omitempty"`
	PageNumber    int    `json:"page_number,omitempty"`
	PageID        string `json:"page_id,omitempty"`
	PanelLabel    string `json:"panel_label,omitempty"`
	// Labels are the edited script panels ("7.4").
	Labels []string `json:"labels,omitempty"`
	// Caption/Dialogue mark a script edit that only rewrote captions or speech.
	Caption  bool   `json:"caption,omitempty"`
	Dialogue bool   `json:"dialogue,omitempty"`
	Name     string `json:"name,omitempty"`
	// Note is the agent's own short explanation of a request.
	Note       string      `json:"note,omitempty"`
	Generation *genRequest `json:"generation,omitempty"`
	// ThresholdUSD is the token's approval threshold when a generation waited.
	ThresholdUSD *float64 `json:"threshold_usd,omitempty"`
}

// actionRow is one agent_actions row.
type actionRow struct {
	ID         string                   `db:"id"`
	UserID     string                   `db:"user_id"`
	TokenID    string                   `db:"token_id"`
	TokenName  string                   `db:"token_name"`
	ProjectID  sql.NullString           `db:"project_id"`
	Kind       string                   `db:"kind"`
	Action     string                   `db:"action"`
	TargetKind string                   `db:"target_kind"`
	TargetID   string                   `db:"target_id"`
	Status     string                   `db:"status"`
	Detail     db.JSON[actionDetail]    `db:"detail"`
	Undo       db.JSON[json.RawMessage] `db:"undo"`
	JobID      sql.NullString           `db:"job_id"`
	Estimate   db.JSON[jobs.Cost]       `db:"estimate"`
	DecidedBy  sql.NullString           `db:"decided_by"`
	DecidedAt  *time.Time               `db:"decided_at"`
	CreatedAt  time.Time                `db:"created_at"`
	UpdatedAt  time.Time                `db:"updated_at"`
}

const actionCols = `id, user_id, token_id, token_name, project_id, kind, action, target_kind, target_id, status, detail, undo, job_id, estimate, decided_by, decided_at, created_at, updated_at`

// newAction is the input for recording an action.
type newAction struct {
	UserID, TokenID, TokenName, ProjectID string
	Kind, Action, TargetKind, TargetID    string
	Status                                string
	Detail                                actionDetail
	Undo                                  any
	JobID                                 string
	Estimate                              jobs.Cost
}

func (m *Module) insertAction(ctx context.Context, q db.Querier, in newAction) (actionRow, error) {
	undo := json.RawMessage("{}")
	if in.Undo != nil {
		b, err := json.Marshal(in.Undo)
		if err != nil {
			return actionRow{}, err
		}
		undo = b
	}
	now := db.Now()
	row := actionRow{ID: db.NewID(), UserID: in.UserID, TokenID: in.TokenID, TokenName: in.TokenName,
		ProjectID: sql.NullString{String: in.ProjectID, Valid: in.ProjectID != ""}, Kind: in.Kind, Action: in.Action,
		TargetKind: in.TargetKind, TargetID: in.TargetID, Status: in.Status, Detail: db.NewJSON(in.Detail),
		Undo: db.NewJSON(undo), JobID: sql.NullString{String: in.JobID, Valid: in.JobID != ""},
		Estimate: db.NewJSON(in.Estimate), CreatedAt: now, UpdatedAt: now}
	_, err := q.Exec(ctx, `INSERT INTO agent_actions (`+actionCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.UserID, row.TokenID, row.TokenName, db.NullString(in.ProjectID), row.Kind, row.Action, row.TargetKind,
		row.TargetID, row.Status, db.MustJSON(in.Detail), string(undo), db.NullString(in.JobID), db.MustJSON(in.Estimate),
		nil, nil, now, now)
	return row, err
}

func (m *Module) getAction(ctx context.Context, q db.Querier, id string) (actionRow, error) {
	var row actionRow
	if !db.ValidID(id) {
		return row, sql.ErrNoRows
	}
	lock := ""
	if _, ok := q.(*db.Tx); ok && q.Dialect() == db.Postgres {
		lock = " FOR UPDATE"
	}
	err := q.Get(ctx, &row, `SELECT `+actionCols+` FROM agent_actions WHERE id = ?`+lock, id)
	return row, err
}

// tokenActions lists one token's actions, newest first.
func (m *Module) tokenActions(ctx context.Context, tokenID string, limit int) ([]actionRow, error) {
	var rows []actionRow
	err := m.d.DB.Select(ctx, &rows, `SELECT `+actionCols+` FROM agent_actions WHERE token_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, tokenID, limit)
	return rows, err
}

// visibleActions lists what G13 shows a person: actions of their own tokens
// and every agent action in projects they own, newest first.
func (m *Module) visibleActions(ctx context.Context, userID string, limit int) ([]actionRow, error) {
	var rows []actionRow
	err := m.d.DB.Select(ctx, &rows, `SELECT `+actionCols+` FROM agent_actions WHERE user_id = ?
		OR project_id IN (SELECT project_id FROM project_members WHERE user_id = ? AND role = 'owner')
		ORDER BY created_at DESC, id DESC LIMIT ?`, userID, userID, limit)
	return rows, err
}

// pendingCount counts a token's requests that still wait for a person.
func (m *Module) pendingCount(ctx context.Context, tokenID string) (int, error) {
	var n int
	err := m.d.DB.Get(ctx, &n, `SELECT COUNT(*) FROM agent_actions WHERE token_id = ? AND status = ?`, tokenID, statusPending)
	return n, err
}

// transition moves an action from one status to another, recording who
// decided. It reports false when the action was no longer in from (another
// person or tab acted first).
func (m *Module) transition(ctx context.Context, q db.Querier, id, from, to, by, jobID string) (bool, error) {
	now := db.Now()
	query := `UPDATE agent_actions SET status = ?, decided_by = ?, decided_at = ?, updated_at = ?`
	args := []any{to, db.NullString(by), now, now}
	if jobID != "" {
		query += `, job_id = ?`
		args = append(args, jobID)
	}
	res, err := q.Exec(ctx, query+` WHERE id = ? AND status = ?`, append(args, id, from)...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// isHumanAction reports whether action is one an agent may only ask for.
func isHumanAction(action string) bool {
	for _, a := range humanActions {
		if a == strings.TrimSpace(action) {
			return true
		}
	}
	return false
}
