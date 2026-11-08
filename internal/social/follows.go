package social

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
)

// ---- social.Follows -----------------------------------------------------------

// IsFollowing reports whether userID follows the series.
func (s *service) IsFollowing(ctx context.Context, userID, seriesID string) (bool, error) {
	if !validID(userID) || !validID(seriesID) {
		return false, nil
	}
	var n int
	if err := s.db.Get(ctx, &n, `SELECT COUNT(*) FROM follows WHERE user_id = ? AND series_id = ?`,
		strings.TrimSpace(userID), strings.TrimSpace(seriesID)); err != nil {
		return false, fmt.Errorf("social: is following: %w", err)
	}
	return n > 0, nil
}

type followRow struct {
	SeriesID  string    `db:"series_id"`
	CreatedAt time.Time `db:"created_at"`
}

// FollowedSeriesIDs lists the series a user follows, most recently followed
// first, in keyset pages of at most MaxFollowLimit. It does not filter by
// access: callers pass the IDs to publish.Service.ListSeries, which does.
func (s *service) FollowedSeriesIDs(ctx context.Context, userID, cursor string, limit int) ([]string, string, error) {
	if !validID(userID) {
		return []string{}, "", nil
	}
	limit = clampLimit(limit, DefaultFollowLimit, MaxFollowLimit)
	where, args := "user_id = ?", []any{strings.TrimSpace(userID)}
	if cursor != "" {
		k, err := decodeCursor(cursor, true)
		if err != nil {
			return nil, "", err
		}
		where += " AND (created_at < ? OR (created_at = ? AND series_id < ?))"
		args = append(args, k.T, k.T, k.ID)
	}
	var rows []followRow
	if err := s.db.Select(ctx, &rows, `SELECT series_id, created_at FROM follows WHERE `+where+
		` ORDER BY created_at DESC, series_id DESC LIMIT ?`, append(args, limit+1)...); err != nil {
		return nil, "", fmt.Errorf("social: followed series: %w", err)
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		next = encodeCursor(last.CreatedAt, last.SeriesID)
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.SeriesID
	}
	return ids, next, nil
}

// Followers lists the user IDs following a series with notifications on,
// ordered by user ID, in keyset pages of at most MaxFollowLimit. The publish
// module still checks each follower's access before notifying.
func (s *service) Followers(ctx context.Context, seriesID, cursor string, limit int) ([]string, string, error) {
	if !validID(seriesID) {
		return []string{}, "", nil
	}
	limit = clampLimit(limit, DefaultFollowLimit, MaxFollowLimit)
	where, args := "series_id = ? AND notify = ?", []any{strings.TrimSpace(seriesID), true}
	if cursor != "" {
		k, err := decodeCursor(cursor, false)
		if err != nil {
			return nil, "", err
		}
		where += " AND user_id > ?"
		args = append(args, k.ID)
	}
	var ids []string
	if err := s.db.Select(ctx, &ids, `SELECT user_id FROM follows WHERE `+where+` ORDER BY user_id LIMIT ?`,
		append(args, limit+1)...); err != nil {
		return nil, "", fmt.Errorf("social: followers: %w", err)
	}
	next := ""
	if len(ids) > limit {
		ids = ids[:limit]
		next = encodeCursor(time.Time{}, ids[limit-1])
	}
	if ids == nil {
		ids = []string{}
	}
	return ids, next, nil
}

// ---- series follows -------------------------------------------------------------

// FollowState is a viewer's follow of one series.
type FollowState struct {
	Following     bool `json:"following"`
	Notify        bool `json:"notify"`
	FollowerCount int  `json:"follower_count"`
}

// followState reads userID's follow of the series.
func (s *service) followState(ctx context.Context, userID, seriesID string) (bool, bool, error) {
	var notify []bool
	if err := s.db.Select(ctx, &notify, `SELECT notify FROM follows WHERE user_id = ? AND series_id = ?`, userID, seriesID); err != nil {
		return false, false, fmt.Errorf("social: follow state: %w", err)
	}
	if len(notify) == 0 {
		return false, false, nil
	}
	return true, notify[0], nil
}

// follow makes userID follow a series the caller already resolved through
// publish.Service for that viewer. It is idempotent: an existing follow only
// has its notify flag updated, and follower_count changes only when a row is
// inserted (one narrow UPDATE of the publish-owned series row, same transaction).
func (s *service) follow(ctx context.Context, userID string, series publish.Series, notify bool) error {
	if !validID(userID) || !validID(series.ID) {
		return ErrNotFound
	}
	return s.db.WithTx(ctx, func(tx *db.Tx) error {
		res, err := tx.Exec(ctx, `INSERT INTO follows (user_id, series_id, notify, created_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (user_id, series_id) DO NOTHING`, userID, series.ID, notify, s.now())
		if err != nil {
			if db.IsForeignKeyViolation(err) {
				return ErrNotFound
			}
			return fmt.Errorf("social: follow: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			if _, err := tx.Exec(ctx, `UPDATE follows SET notify = ? WHERE user_id = ? AND series_id = ?`, notify, userID, series.ID); err != nil {
				return fmt.Errorf("social: follow notify: %w", err)
			}
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE series SET follower_count = follower_count + 1 WHERE id = ?`, series.ID); err != nil {
			return fmt.Errorf("social: follower count: %w", err)
		}
		return nil
	})
}

// unfollow removes userID's follow. It needs no access check (it only deletes
// the viewer's own row) and is idempotent.
func (s *service) unfollow(ctx context.Context, userID, seriesID string) error {
	if !validID(userID) || !validID(seriesID) {
		return nil
	}
	return s.db.WithTx(ctx, func(tx *db.Tx) error {
		res, err := tx.Exec(ctx, `DELETE FROM follows WHERE user_id = ? AND series_id = ?`, userID, seriesID)
		if err != nil {
			return fmt.Errorf("social: unfollow: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE series SET follower_count = follower_count - 1 WHERE id = ? AND follower_count > 0`,
			seriesID); err != nil {
			return fmt.Errorf("social: follower count: %w", err)
		}
		return nil
	})
}

// ---- user follows ------------------------------------------------------------------

// followUser makes follower follow followee (an active account other than
// themselves). It is idempotent.
func (s *service) followUser(ctx context.Context, followerID string, followee *auth.User) error {
	if followee == nil || !validID(followerID) {
		return ErrNotFound
	}
	if followee.ID == followerID {
		return invalid("followee", "social.error.self_follow")
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO user_follows (follower_id, followee_id, created_at) VALUES (?, ?, ?)
		ON CONFLICT (follower_id, followee_id) DO NOTHING`, followerID, followee.ID, s.now()); err != nil {
		if db.IsForeignKeyViolation(err) {
			return ErrNotFound
		}
		return fmt.Errorf("social: follow user: %w", err)
	}
	return nil
}

// unfollowUser is idempotent.
func (s *service) unfollowUser(ctx context.Context, followerID, followeeID string) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM user_follows WHERE follower_id = ? AND followee_id = ?`, followerID, followeeID); err != nil {
		return fmt.Errorf("social: unfollow user: %w", err)
	}
	return nil
}

// isFollowingUser reports whether follower follows followee.
func (s *service) isFollowingUser(ctx context.Context, followerID, followeeID string) (bool, error) {
	if !validID(followerID) || !validID(followeeID) {
		return false, nil
	}
	var n int
	if err := s.db.Get(ctx, &n, `SELECT COUNT(*) FROM user_follows WHERE follower_id = ? AND followee_id = ?`,
		followerID, followeeID); err != nil {
		return false, fmt.Errorf("social: is following user: %w", err)
	}
	return n > 0, nil
}

// userFollowerCount counts the accounts following a user.
func (s *service) userFollowerCount(ctx context.Context, userID string) (int, error) {
	var n int
	if err := s.db.Get(ctx, &n, `SELECT COUNT(*) FROM user_follows WHERE followee_id = ?`, userID); err != nil {
		return 0, fmt.Errorf("social: user followers: %w", err)
	}
	return n, nil
}

// ageConfirmed reports whether userID confirmed their age for the series
// (age_confirmations, migration 0400; written by the reader's
// POST /s/{series}/confirm-age).
func (s *service) ageConfirmed(ctx context.Context, userID, seriesID string) (bool, error) {
	if !validID(userID) || !validID(seriesID) {
		return false, nil
	}
	var n int
	if err := s.db.Get(ctx, &n, `SELECT COUNT(*) FROM age_confirmations WHERE user_id = ? AND series_id = ?`,
		userID, seriesID); err != nil {
		return false, fmt.Errorf("social: age confirmation: %w", err)
	}
	return n > 0, nil
}
