package studio

import (
	"context"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// AuthorizeTx rechecks current user/token/membership inside the caller's
// transaction, using the same lock order as ordinary Studio mutations.
func (s *service) AuthorizeTx(ctx context.Context, tx *db.Tx, actor Actor, projectID string, action Action) error {
	if tx == nil {
		return ErrInvalid
	}
	return s.authorize(ctx, tx, actor, strings.TrimSpace(projectID), action)
}

// CheckScopeWritableTx holds scope rows through the caller's atomic write.
func (s *service) CheckScopeWritableTx(ctx context.Context, tx *db.Tx, scope jobs.Scope) error {
	if tx == nil || scope.Validate() != nil {
		return ErrInvalid
	}
	return checkScopeWritable(ctx, tx, scope)
}

// GetCharacterTx locks a character before checking its current revision and
// loading its reference/outfit children in the same transaction.
func (s *service) GetCharacterTx(ctx context.Context, tx *db.Tx, id string) (Character, error) {
	if tx == nil {
		return Character{}, ErrInvalid
	}
	var lockedID string
	if err := tx.Get(ctx, &lockedID, `SELECT id FROM characters WHERE id = ?`+forUpdate(tx), strings.TrimSpace(id)); err != nil {
		return Character{}, scopeLookupError(err)
	}
	return getCharacter(ctx, tx, lockedID)
}
