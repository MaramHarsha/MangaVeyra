package publish

import (
	"context"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// Test hooks for the external publish_test package.

func impl(s Service) *service { return s.(*service) }

// SetFollowersForTest replaces the follower source used for notifications.
func SetFollowersForTest(s Service, f FollowerSource) { impl(s).followsOverride = f }

// SetNowForTest replaces the service clock.
func SetNowForTest(s Service, now func() time.Time) { impl(s).now = now }

// PublishDueForTest runs one schedule pass through d (a second connection in tests).
func PublishDueForTest(ctx context.Context, s Service, d *db.DB) (int, error) {
	return impl(s).publishDue(ctx, d)
}

// NotifyFollowersForTest runs the new-chapter fan-out again for a publication.
func NotifyFollowersForTest(ctx context.Context, s Service, publicationID string) {
	impl(s).notifyFollowers(ctx, publicationID)
}

// DraftAltTextForTest exposes the deterministic alt-text draft.
func DraftAltTextForTest(ctx context.Context, lang, action, setting, timeOfDay string, characters []string, bubbles []GuidedBubble) string {
	return draftAltText(ctx, lang, altSource{Action: action, Setting: setting, TimeOfDay: timeOfDay, Characters: characters, Bubbles: bubbles})
}
