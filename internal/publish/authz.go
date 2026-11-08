package publish

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// errForbidden marks a signed-in project member who may not publish (403).
// Anyone who cannot read the project gets ErrNotFound instead (404).
var errForbidden = errors.New("publish: forbidden")

// publishRoles are the studio project roles that may create and edit the
// series, publish, schedule, update and unpublish chapters. Site admins get no
// override: publishing is the creators' decision (ADR 0012 §3).
var publishRoles = map[string]bool{studio.RoleOwner: true, studio.RoleEditor: true}

// authorizePublish checks that a human project owner or editor acts. Bearer
// and agent tokens are always refused: publishing is a human action.
func (s *service) authorizePublish(ctx context.Context, q db.Querier, actor studio.Actor, projectID string) error {
	actor.UserID, projectID = strings.TrimSpace(actor.UserID), strings.TrimSpace(projectID)
	if actor.UserID == "" || projectID == "" {
		return ErrNotFound
	}
	if actor.TokenID != "" {
		return errForbidden
	}
	var err error
	if tx, ok := q.(*db.Tx); ok {
		err = s.studio.AuthorizeTx(ctx, tx, actor, projectID, studio.ActRead)
	} else {
		err = s.studio.Authorize(ctx, actor, projectID, studio.ActRead)
	}
	switch {
	case err == nil:
	case errors.Is(err, studio.ErrNotFound), errors.Is(err, studio.ErrForbidden), errors.Is(err, studio.ErrInvalid):
		return ErrNotFound
	default:
		return fmt.Errorf("publish: authorize: %w", err)
	}
	var role string
	if err := q.Get(ctx, &role, `SELECT role FROM project_members WHERE project_id = ? AND user_id = ?`, projectID, actor.UserID); err != nil {
		if db.IsNotFound(err) {
			return errForbidden // e.g. a site admin outside the project
		}
		return fmt.Errorf("publish: project role: %w", err)
	}
	if !publishRoles[role] {
		return errForbidden
	}
	return nil
}

func forUpdate(q db.Querier) string {
	if q.Dialect() == db.Postgres {
		return " FOR UPDATE"
	}
	return ""
}
