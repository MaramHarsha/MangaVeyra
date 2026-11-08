package activityui

import (
	"context"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/notify"
)

// JobSummaryBody marks (in Notification.Data["body"]) a job notification
// whose body was written by JobNotificationBody, so G4 shows it as stored.
const JobSummaryBody = "job_summary"

// JobNotificationBody is the body of a job notification in the locale of
// ctx, as G4 draws it: project · what was made · how long it took
// ("Tidebreak Courier · Panel 7.3 · Regenerate · 38 s"). It never contains a
// job type, id, prompt or worker/provider text.
func JobNotificationBody(ctx context.Context, d *db.DB, j *jobs.Job) string {
	var parts []string
	if j.Scope.ProjectID != "" {
		var title string
		if err := d.Get(ctx, &title, `SELECT title FROM projects WHERE id = ?`, j.Scope.ProjectID); err == nil && strings.TrimSpace(title) != "" {
			parts = append(parts, title)
		}
	}
	what, err := jobTitle(ctx, d, j)
	if err != nil {
		what = i18n.T(ctx, jobLabelKey(j.Type, j.Executor))
	}
	parts = append(parts, what)
	if j.StartedAt != nil && j.FinishedAt != nil && j.FinishedAt.After(*j.StartedAt) {
		parts = append(parts, shortDuration(ctx, j.FinishedAt.Sub(*j.StartedAt).Seconds()))
	}
	return strings.Join(parts, " · ")
}

// notificationBody is the body G4 shows. Job notifications written before
// JobNotificationBody stored the raw job type ("page.generate"), and older
// failures could store raw worker text: known job types read as their
// localized label and any other unmarked failure body as safe recovery copy.
func notificationBody(ctx context.Context, n notify.Notification) string {
	if n.Kind != notify.KindJobDone && n.Kind != notify.KindJobFailed {
		return n.Body
	}
	if body, _ := n.Data["body"].(string); body == JobSummaryBody {
		return n.Body
	}
	if looksLikeJobType(n.Body) {
		return i18n.T(ctx, jobLabelKey(n.Body, ""))
	}
	if n.Kind == notify.KindJobFailed {
		return i18n.T(ctx, "activity.error.job")
	}
	return n.Body
}
