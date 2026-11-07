package studio

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

const (
	maxSourceBytes = 1 << 20
	maxShortText   = 160
)

type service struct {
	db     *db.DB
	auth   *auth.Service
	assets *assets.Service
}

var _ Service = (*service)(nil)

type actorState struct {
	user  *auth.User
	token *httpx.Token
}

func (s *service) ActorFromUser(ctx context.Context, userID string) (Actor, error) {
	u, err := s.auth.UserByID(ctx, strings.TrimSpace(userID))
	if err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			return Actor{}, ErrNotFound
		}
		return Actor{}, err
	}
	if u.Status != auth.StatusActive {
		return Actor{}, ErrForbidden
	}
	return Actor{UserID: u.ID, SiteRole: u.Role}, nil
}

func (s *service) Authorize(ctx context.Context, actor Actor, projectID string, action Action) error {
	return s.authorize(ctx, s.db, actor, strings.TrimSpace(projectID), action)
}

func (s *service) authorize(ctx context.Context, q db.Querier, actor Actor, projectID string, action Action) error {
	if projectID == "" || !validAction(action) {
		return ErrInvalid
	}
	state, err := s.resolveActor(ctx, q, actor)
	if err != nil {
		return err
	}

	var exists int
	if err := q.Get(ctx, &exists, `SELECT 1 FROM projects WHERE id = ?`+forUpdate(q), projectID); err != nil {
		if db.IsNotFound(err) {
			return ErrNotFound
		}
		return fmt.Errorf("studio: find project: %w", err)
	}
	if state.token != nil {
		if !state.token.AllowsProject(projectID) || !tokenAllows(state.token, action) {
			return ErrForbidden
		}
	}
	// A site admin may act on any project, but a narrow API token does not
	// carry that power unless it has the admin scope (as for job access).
	if state.user.IsAdmin() && (state.token == nil || state.token.HasScope("admin")) {
		return nil
	}

	var role string
	if err := q.Get(ctx, &role, `SELECT role FROM project_members WHERE project_id = ? AND user_id = ?`+forUpdate(q), projectID, state.user.ID); err != nil {
		if db.IsNotFound(err) {
			return ErrForbidden
		}
		return fmt.Errorf("studio: find project membership: %w", err)
	}
	if !roleAllows(role, action) {
		return ErrForbidden
	}
	return nil
}

func (s *service) resolveActor(ctx context.Context, q db.Querier, actor Actor) (actorState, error) {
	actor.UserID = strings.TrimSpace(actor.UserID)
	actor.TokenID = strings.TrimSpace(actor.TokenID)
	if actor.UserID == "" {
		return actorState{}, ErrForbidden
	}

	if actor.TokenID != "" {
		var tok *httpx.Token
		var u *auth.User
		var err error
		if tx, ok := q.(*db.Tx); ok {
			// Lock users before api_tokens, the same order auth uses when it
			// suspends a user and revokes their tokens, so the two cannot
			// deadlock on PostgreSQL.
			var lockedUserID string
			if err := tx.Get(ctx, &lockedUserID, `SELECT id FROM users WHERE id = ?`+forUpdate(tx), actor.UserID); err != nil {
				return actorState{}, ErrForbidden
			}
			var tokenUserID string
			if err := tx.Get(ctx, &tokenUserID, `SELECT user_id FROM api_tokens WHERE id = ?`+forUpdate(tx), actor.TokenID); err != nil || tokenUserID != actor.UserID {
				return actorState{}, ErrForbidden
			}
			tok, u, err = s.auth.ResolveStoredTokenTx(ctx, tx, actor.TokenID)
		} else {
			tok, u, err = s.auth.ResolveStoredToken(ctx, actor.TokenID)
		}
		if err != nil || tok == nil || u == nil || tok.UserID != actor.UserID || u.ID != actor.UserID || u.Status != auth.StatusActive {
			return actorState{}, ErrForbidden
		}
		return actorState{user: u, token: tok}, nil
	}

	var row struct {
		ID     string `db:"id"`
		Role   string `db:"role"`
		Status string `db:"status"`
	}
	if err := q.Get(ctx, &row, `SELECT id, role, status FROM users WHERE id = ?`+forUpdate(q), actor.UserID); err != nil {
		if db.IsNotFound(err) {
			return actorState{}, ErrForbidden
		}
		return actorState{}, fmt.Errorf("studio: resolve actor: %w", err)
	}
	if row.Status != auth.StatusActive {
		return actorState{}, ErrForbidden
	}
	return actorState{user: &auth.User{ID: row.ID, Role: row.Role, Status: row.Status}}, nil
}

func validAction(action Action) bool {
	switch action {
	case ActRead, ActEditScript, ActEditArt, ActLetter, ActGenerate, ActManage, ActLock:
		return true
	default:
		return false
	}
}

func roleAllows(role string, action Action) bool {
	switch action {
	case ActRead:
		return role == RoleOwner || role == RoleWriter || role == RoleArtist || role == RoleLetterer || role == RoleEditor || role == RoleViewer
	case ActEditScript:
		return role == RoleOwner || role == RoleWriter || role == RoleEditor
	case ActEditArt:
		return role == RoleOwner || role == RoleArtist || role == RoleEditor
	case ActLetter:
		return role == RoleOwner || role == RoleLetterer || role == RoleEditor
	case ActGenerate:
		return role == RoleOwner || role == RoleWriter || role == RoleArtist || role == RoleEditor
	case ActManage:
		return role == RoleOwner
	case ActLock:
		return role == RoleOwner || role == RoleEditor
	default:
		return false
	}
}

func tokenAllows(tok *httpx.Token, action Action) bool {
	switch action {
	case ActRead:
		return tok.HasScope("projects:read")
	case ActGenerate:
		return tok.HasScope("projects:write") && tok.HasScope("generate")
	default:
		return tok.HasScope("projects:write")
	}
}

func validateText(field, value string, max int, required bool) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s is not valid UTF-8", ErrInvalid, field)
	}
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalid, field)
	}
	if max > 0 && utf8.RuneCountInString(value) > max {
		return fmt.Errorf("%w: %s is longer than %d characters", ErrInvalid, field, max)
	}
	return nil
}

func forUpdate(q db.Querier) string {
	if q.Dialect() == db.Postgres {
		return " FOR UPDATE"
	}
	return ""
}
