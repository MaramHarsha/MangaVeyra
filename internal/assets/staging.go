package assets

import (
	"context"
	"errors"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// StagedAsset is a storage object whose database row has not yet been
// committed. Its metadata is private so callers cannot substitute a key or
// ownership when a job reaches its persistence fence.
type StagedAsset struct {
	asset Asset
	owner *Service
}

// Asset returns an independent metadata copy. Its URL must not be exposed
// until CommitStagedTx has committed successfully.
func (a *StagedAsset) Asset() Asset {
	if a == nil {
		return Asset{}
	}
	out := a.asset
	out.Meta = append(out.Meta[:0:0], out.Meta...)
	if out.OwnerID != nil {
		id := *out.OwnerID
		out.OwnerID = &id
	}
	if out.ProjectID != nil {
		id := *out.ProjectID
		out.ProjectID = &id
	}
	return out
}

// Stage performs the storage write before entering a database/job transaction.
// The caller commits the row with CommitStagedTx or discards the object after
// its transaction rolls back. A crash can leave only an unreferenced object.
func (s *Service) Stage(ctx context.Context, p SaveParams) (*StagedAsset, error) {
	a, err := s.put(ctx, p)
	if err != nil {
		return nil, err
	}
	return &StagedAsset{asset: a, owner: s}, nil
}

// CommitStagedTx inserts metadata only; it performs no storage/network I/O.
func (s *Service) CommitStagedTx(ctx context.Context, tx *db.Tx, a *StagedAsset) (Asset, error) {
	if tx == nil || a == nil || a.owner != s {
		return Asset{}, errors.New("assets: invalid staged asset")
	}
	if err := insert(ctx, tx, &a.asset); err != nil {
		return Asset{}, err
	}
	return a.Asset(), nil
}

// DiscardStaged removes an uncommitted object after rollback. It refuses to
// remove an object with a committed row, so a deferred cleanup cannot erase
// art already attached by a successful job.
func (s *Service) DiscardStaged(ctx context.Context, a *StagedAsset) error {
	if a == nil || a.owner != s {
		return errors.New("assets: invalid staged asset")
	}
	if _, err := s.Get(ctx, a.asset.ID); err == nil {
		return errors.New("assets: staged asset is already committed")
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	return s.store.Delete(ctx, a.asset.Key)
}
