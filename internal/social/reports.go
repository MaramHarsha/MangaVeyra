package social

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
)

// Report reasons (the reports table's CHECK list, ADR 0012 §9).
var ReportReasons = []string{"spam", "harassment", "copyright", "ai_label", "unmarked_mature", "illegal", "other"}

// Reportable target kinds through the social API.
var reportKinds = []string{"comment", "series", "chapter", "user"}

// MaxReportDetailRunes bounds a report's details.
const MaxReportDetailRunes = 1000

// NewReport is a report to file. SeriesID is required for chapter targets
// (the publication ID is the target).
type NewReport struct {
	TargetKind string `json:"target_kind"`
	TargetID   string `json:"target_id"`
	SeriesID   string `json:"series_id"`
	Reason     string `json:"reason"`
	Details    string `json:"details"`
}

// Report is a filed report (the admin panel reviews it in Phase 4).
type Report struct {
	ID         string    `json:"id"`
	TargetKind string    `json:"target_kind"`
	TargetID   string    `json:"target_id"`
	Reason     string    `json:"reason"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	// Existing is true when the reporter already had an open report on the target.
	Existing bool `json:"existing"`
}

// report files a report as v after checking the reporter may see the target.
// A second open report from the same person on the same target returns the
// first one instead of adding a duplicate.
func (s *service) report(ctx context.Context, v publish.Viewer, in NewReport) (Report, error) {
	if v.UserID == "" {
		return Report{}, ErrForbidden
	}
	kind, targetID := strings.TrimSpace(in.TargetKind), strings.TrimSpace(in.TargetID)
	if !slices.Contains(reportKinds, kind) {
		return Report{}, invalid("target_kind", "social.error.target_kind")
	}
	reason := strings.TrimSpace(in.Reason)
	if !slices.Contains(ReportReasons, reason) {
		return Report{}, invalid("reason", "social.error.reason")
	}
	details := strings.TrimSpace(strings.ToValidUTF8(in.Details, "�"))
	if utf8.RuneCountInString(details) > MaxReportDetailRunes {
		return Report{}, invalid("details", "social.error.details_long")
	}
	switch kind {
	case "comment":
		row, _, err := s.loadComment(ctx, v, targetID)
		if err != nil {
			return Report{}, err
		}
		if row.AuthorID.String == v.UserID {
			return Report{}, invalid("target_id", "social.error.report_self")
		}
		targetID = row.ID
	case "series":
		series, err := s.pub.SeriesByID(ctx, v, targetID)
		if err != nil {
			return Report{}, accessErr(err)
		}
		targetID = series.ID
	case "chapter":
		t, err := s.resolveTarget(ctx, v, in.SeriesID, TargetChapter, targetID)
		if err != nil {
			return Report{}, err
		}
		targetID = t.pub.ID
	case "user":
		u, err := s.auth.UserByID(ctx, targetID)
		if err != nil || u.Status != auth.StatusActive {
			if err != nil && !errors.Is(err, auth.ErrUserNotFound) {
				return Report{}, err
			}
			return Report{}, ErrNotFound
		}
		if u.ID == v.UserID {
			return Report{}, invalid("target_id", "social.error.report_self")
		}
		targetID = u.ID
	}
	out := Report{TargetKind: kind, TargetID: targetID, Reason: reason, Status: "open"}
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		var existing []struct {
			ID        string    `db:"id"`
			Reason    string    `db:"reason"`
			CreatedAt time.Time `db:"created_at"`
		}
		if err := tx.Select(ctx, &existing, `SELECT id, reason, created_at FROM reports WHERE reporter_id = ? AND target_kind = ?
			AND target_id = ? AND status = 'open' ORDER BY created_at LIMIT 1`, v.UserID, kind, targetID); err != nil {
			return fmt.Errorf("social: existing report: %w", err)
		}
		if len(existing) > 0 {
			out.ID, out.Reason, out.CreatedAt, out.Existing = existing[0].ID, existing[0].Reason, existing[0].CreatedAt.UTC(), true
			return nil
		}
		out.ID, out.CreatedAt = db.NewID(), s.now()
		if _, err := tx.Exec(ctx, `INSERT INTO reports (id, reporter_id, target_kind, target_id, reason, details, status, created_at)
			VALUES (?, ?, ?, ?, ?, ?, 'open', ?)`, out.ID, v.UserID, kind, targetID, reason, details, out.CreatedAt); err != nil {
			return fmt.Errorf("social: insert report: %w", err)
		}
		return nil
	})
	if err != nil {
		return Report{}, err
	}
	return out, nil
}
