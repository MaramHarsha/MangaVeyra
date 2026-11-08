package social

// Frozen Phase 3 contract (docs/plans/0010-phase-3-publish-and-read.md, ADR 0012).
//
// Social owns follows, user follows, public comments and reports. Other modules
// use Follows through core.Use[social.Follows] instead of reading its tables.

import "context"

// Follows answers follow questions for the reader library, the series page and
// new-chapter notifications.
type Follows interface {
	// IsFollowing reports whether userID follows the series.
	IsFollowing(ctx context.Context, userID, seriesID string) (bool, error)
	// FollowedSeriesIDs lists the series a user follows, most recent first;
	// next is the cursor for the following page ("" when done).
	FollowedSeriesIDs(ctx context.Context, userID, cursor string, limit int) (ids []string, next string, err error)
	// Followers lists the user IDs following a series that asked to be
	// notified about new chapters, for the publish module's notifications.
	Followers(ctx context.Context, seriesID, cursor string, limit int) (userIDs []string, next string, err error)
}
