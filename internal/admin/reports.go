package admin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/admin/adminapi"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/notify"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// Report actions (POST /admin/reports/{id}/resolve, field "action").
const (
	actDismiss         = "dismiss"
	actHideComment     = "hide_comment"
	actTakedownChapter = "takedown_chapter"
	actTakedownSeries  = "takedown_series"
	actSuspend         = "suspend"
	actReviewLabel     = "review_label"
)

// maxNote bounds the admin's resolution note.
const maxNote = 500

// aiRoles are the per-role AI labels, in the order E1 shows them.
var aiRoles = []string{"script", "art", "lettering"}

// reportItem is one row of a report queue.
type reportItem struct {
	ID       string
	Title    string
	Context  string
	Labels   []labelItem
	Reporter string
	Reason   string
	Href     string
	Current  bool
	Gone     bool
}

type labelItem struct{ Role, Value, Kind string }

// action is a button of the review panel.
type action struct {
	Value, Label, Icon string
	Variant            ui.Variant
}

// reviewView is the expanded review of one report.
type reviewView struct {
	ID        string
	Item      reportItem
	Target    target
	Details   string
	When      string
	Status    []string
	Comment   string
	Reader    string
	Actions   []action
	Note      string
	Error     string
	Tab       string
	Responder string
}

// takedownItem is one active takedown with its Restore button.
type takedownItem struct {
	Key, Title, Meta string
}

// stat is one G5 stat card.
type stat struct{ Label, Value string }

type reportsView struct {
	Disputes  bool
	Items     []reportItem
	Review    *reviewView
	Takedowns []takedownItem
}

func (m *Module) reportsPage(disputes bool) core.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		return m.renderReports(w, r, http.StatusOK, disputes, r.PathValue("id"), "", "")
	}
}

func (m *Module) renderReports(w http.ResponseWriter, r *http.Request, status int, disputes bool, reviewID, note, problem string) error {
	ctx := r.Context()
	tab := tabReports
	if disputes {
		tab = tabDisputes
	}
	f, err := m.frame(ctx, r, tab)
	if err != nil {
		return err
	}
	v := reportsView{Disputes: disputes}
	if !disputes {
		f.Stats = m.stats(ctx, f)
	}
	rows, err := openReports(ctx, m.d.DB, disputes)
	if err != nil {
		return err
	}
	base := "/admin/reports/"
	if disputes {
		base = "/admin/disputes/"
	}
	for _, row := range rows {
		t, err := m.loadTarget(ctx, row.TargetKind, row.TargetID)
		if err != nil {
			return err
		}
		it := m.item(ctx, row, t, disputes)
		it.Href, it.Current = base+url.PathEscape(row.ID), row.ID == reviewID
		v.Items = append(v.Items, it)
	}
	if reviewID != "" {
		row, err := reportByID(ctx, m.d.DB, reviewID)
		if errors.Is(err, errNotFound) || (err == nil && disputes && row.Reason != reasonAILabel) {
			return core.ErrNotFound()
		}
		if err != nil {
			return err
		}
		rv, err := m.review(ctx, row, disputes)
		if err != nil {
			return err
		}
		rv.Note, rv.Error = note, problem
		v.Review = &rv
	}
	if !disputes {
		list, err := listTakedowns(ctx, m.d.DB)
		if err != nil {
			return err
		}
		for _, t := range list {
			v.Takedowns = append(v.Takedowns, m.takedownItem(ctx, t))
		}
	}
	return m.render(w, r, status, f, ReportsTab(v))
}

// stats are the four G5 stat cards with real values; storage shows "—" when
// it cannot be read.
func (m *Module) stats(ctx context.Context, f frame) []stat {
	inst := m.d.Settings.MustInstance(ctx)
	storage := "—"
	if f.Counts.StorageKnown {
		storage = formatBytes(f.Counts.StorageBytes)
		if f.StorageCap > 0 {
			storage = i18n.T(ctx, "admin.stat.storage_of", "used", storage, "total", formatBytes(f.StorageCap))
		}
	}
	return []stat{
		{i18n.T(ctx, "admin.stat.reports"), itoa(f.Counts.OpenReports)},
		{i18n.T(ctx, "admin.stat.members"), itoa(f.Counts.Members)},
		{i18n.T(ctx, "admin.stat.storage"), storage},
		{i18n.T(ctx, "admin.stat.signup"), signupLabel(ctx, inst.SignUp)},
	}
}

func signupLabel(ctx context.Context, mode string) string {
	switch mode {
	case settings.SignUpClosed, settings.SignUpInvite, settings.SignUpOpen:
		return i18n.T(ctx, "admin.signup.stat."+mode)
	}
	return mode
}

// storageCapacity is the size of the disk holding local media (0 for S3 or
// when it cannot be read).
func (m *Module) storageCapacity() int64 {
	if m.d.Cfg.Storage != "" && m.d.Cfg.Storage != "local" {
		return 0
	}
	return diskSize(m.d.Cfg.MediaDir())
}

// item builds a queue row.
func (m *Module) item(ctx context.Context, row reportRow, t target, disputes bool) reportItem {
	it := reportItem{ID: row.ID, Reason: i18n.T(ctx, "admin.reason."+row.Reason), Gone: !t.Found}
	if d := strings.TrimSpace(row.Details); d != "" {
		it.Title = clip(strings.SplitN(d, "\n", 2)[0], 90)
	} else {
		it.Title = i18n.T(ctx, "admin.reason_title."+row.Reason)
	}
	it.Context = targetContext(ctx, t)
	switch {
	case !row.ReporterID.Valid:
		it.Reporter = i18n.T(ctx, "admin.reports.reporter_gone")
	default:
		name := row.ReporterName.String
		if name == "" {
			name = row.ReporterUsername.String
		}
		it.Reporter = i18n.T(ctx, "admin.reports.by", "role", roleLabel(ctx, row.ReporterRole.String), "name", name)
	}
	if disputes {
		it.Labels = labelItems(ctx, t.Labels)
	}
	return it
}

func targetContext(ctx context.Context, t target) string {
	switch {
	case !t.Found:
		return i18n.T(ctx, "admin.target.gone")
	case t.Kind == "user":
		return "@" + t.Account.Username
	case t.PublicationID != "":
		return i18n.T(ctx, "admin.target.chapter", "series", t.SeriesTitle, "number", number(t.Number))
	}
	return t.SeriesTitle
}

func labelItems(ctx context.Context, labels map[string]string) []labelItem {
	var out []labelItem
	for _, role := range aiRoles {
		v := labels[role]
		if v == "" {
			continue
		}
		kind := ui.ProvenanceHuman
		if v == ui.ProvenanceAI || v == ui.ProvenanceAIAssisted {
			kind = v
		}
		out = append(out, labelItem{Role: i18n.T(ctx, "admin.label.role."+role), Value: i18n.T(ctx, "admin.label."+v), Kind: kind})
	}
	return out
}

func roleLabel(ctx context.Context, role string) string {
	switch role {
	case auth.RoleAdmin, auth.RoleCreator, auth.RoleReader:
		return i18n.T(ctx, "admin.role."+role)
	}
	return role
}

// review builds the review panel of a report.
func (m *Module) review(ctx context.Context, row reportRow, disputes bool) (reviewView, error) {
	t, err := m.loadTarget(ctx, row.TargetKind, row.TargetID)
	if err != nil {
		return reviewView{}, err
	}
	rv := reviewView{ID: row.ID, Item: m.item(ctx, row, t, row.Reason == reasonAILabel), Target: t, Details: strings.TrimSpace(row.Details),
		When: ago(ctx, row.CreatedAt, m.now()), Tab: tabReports}
	if disputes {
		rv.Tab = tabDisputes
	}
	if t.SeriesTakenDown {
		rv.Status = append(rv.Status, i18n.T(ctx, "admin.review.series_down"))
	}
	if t.ChapterTakenDown {
		rv.Status = append(rv.Status, i18n.T(ctx, "admin.review.chapter_down"))
	}
	if t.Comment != nil {
		switch {
		case t.Comment.Deleted:
			rv.Status = append(rv.Status, i18n.T(ctx, "admin.review.comment_deleted"))
		case t.Comment.Hidden:
			rv.Status = append(rv.Status, i18n.T(ctx, "admin.review.comment_hidden"))
		}
		rv.Comment = t.Comment.Body
	}
	if t.Account != nil && t.Account.Status == auth.StatusSuspended {
		rv.Status = append(rv.Status, i18n.T(ctx, "admin.review.suspended", "name", t.Account.Name()))
	}
	if t.Account != nil {
		rv.Responder = t.Account.Name()
	}
	rv.Reader = readerHref(t)
	if row.Status != statusOpen {
		return rv, nil
	}
	if row.Reason == reasonAILabel && t.Found && t.OwnerID != "" && t.SeriesID != "" {
		rv.Actions = append(rv.Actions, action{actReviewLabel, i18n.T(ctx, "admin.action.review_label"), "tag", ui.Secondary})
	}
	if t.Comment != nil && !t.Comment.Hidden && !t.Comment.Deleted && m.mod != nil {
		rv.Actions = append(rv.Actions, action{actHideComment, i18n.T(ctx, "admin.action.hide_comment"), "eye-off", ui.Secondary})
	}
	if t.Kind == "chapter" && t.ChapterID != "" && !t.ChapterTakenDown {
		rv.Actions = append(rv.Actions, action{actTakedownChapter, i18n.T(ctx, "admin.action.takedown_chapter"), "eye-off", ui.DangerOutline})
	}
	if (t.Kind == "chapter" || t.Kind == "series") && t.SeriesID != "" && !t.SeriesTakenDown {
		rv.Actions = append(rv.Actions, action{actTakedownSeries, i18n.T(ctx, "admin.action.takedown_series"), "eye-off", ui.DangerOutline})
	}
	if a := t.Account; a != nil && a.Status != auth.StatusSuspended && a.ID != core.UserFrom(ctx).ID {
		rv.Actions = append(rv.Actions, action{actSuspend, i18n.T(ctx, "admin.action.suspend", "name", a.Name()), "user-off", ui.DangerOutline})
	}
	rv.Actions = append(rv.Actions, action{actDismiss, i18n.T(ctx, "admin.action.dismiss"), "", ui.Quiet})
	return rv, nil
}

// readerHref opens the reported thing in context ("" when it is gone).
func readerHref(t target) string {
	switch {
	case !t.Found || t.Kind == "user":
		if t.Account != nil {
			return "/u/" + url.PathEscape(t.Account.Username)
		}
		return ""
	case t.PublicationID != "":
		return "/s/" + url.PathEscape(t.SeriesSlug) + "/" + url.PathEscape(number(t.Number)) + "?lang=" + url.QueryEscape(t.Lang)
	}
	return "/s/" + url.PathEscape(t.SeriesSlug)
}

func (m *Module) takedownItem(ctx context.Context, t takedownRow) takedownItem {
	title := t.SeriesTitle
	if title == "" {
		title = i18n.T(ctx, "admin.target.gone")
	}
	if t.HasNumber {
		title = i18n.T(ctx, "admin.target.chapter", "series", title, "number", number(t.Number))
	}
	meta := i18n.T(ctx, "admin.takedowns.when", "when", ago(ctx, t.At, m.now()))
	if t.Data.Note != "" {
		meta += " · " + t.Data.Note
	}
	return takedownItem{Key: t.Key, Title: title, Meta: meta}
}

// POST /admin/reports/{id}/resolve
func (m *Module) resolveReport(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("form")
	}
	ctx := r.Context()
	disputes := web.FormValue(r, "tab") == tabDisputes
	id := r.PathValue("id")
	row, err := reportByID(ctx, m.d.DB, id)
	if errors.Is(err, errNotFound) {
		return core.ErrNotFound()
	}
	if err != nil {
		return err
	}
	note := strings.TrimSpace(web.FormValue(r, "note"))
	if utf8.RuneCountInString(note) > maxNote {
		return m.renderReports(w, r, http.StatusUnprocessableEntity, disputes, id, note, i18n.T(ctx, "admin.error.note_long"))
	}
	if row.Status != statusOpen {
		return m.renderReports(w, r, http.StatusConflict, disputes, id, note, i18n.T(ctx, "admin.error.resolved"))
	}
	rv, err := m.review(ctx, row, disputes)
	if err != nil {
		return err
	}
	act := web.FormValue(r, "action")
	allowed := false
	for _, a := range rv.Actions {
		allowed = allowed || a.Value == act
	}
	if !allowed {
		return m.renderReports(w, r, http.StatusConflict, disputes, id, note, i18n.T(ctx, "admin.error.action"))
	}
	if problem, err := m.apply(ctx, row, rv.Target, act, note); err != nil {
		return err
	} else if problem != "" {
		return m.renderReports(w, r, http.StatusConflict, disputes, id, note, problem)
	}
	back := "/admin"
	if disputes {
		back = "/admin/disputes"
	}
	return done(w, r, back)
}

// apply performs one report action and resolves the report (every open report
// on the same target for actions, only this one for a dismissal). problem is a
// message for the admin when the action was refused.
func (m *Module) apply(ctx context.Context, row reportRow, t target, act, note string) (problem string, err error) {
	admin := core.UserFrom(ctx)
	resolution := note
	if resolution == "" {
		resolution = i18n.T(ctx, "admin.resolution."+act)
	}
	status, all := statusActioned, true
	data := map[string]any{"report_id": row.ID, "target_kind": row.TargetKind, "target_id": row.TargetID, "reason": row.Reason}
	if note != "" {
		data["note"] = note
	}
	var auditKind, auditID string
	switch act {
	case actDismiss:
		status, all = statusDismissed, false
		auditKind, auditID = "report", row.ID
	case actHideComment:
		if err := m.mod.HideComment(ctx, t.Comment.ID, admin.ID); err != nil {
			return i18n.T(ctx, "admin.error.comment_gone"), nil
		}
		auditKind, auditID = "comment", t.Comment.ID
	case actTakedownChapter, actTakedownSeries:
		kind, tid := adminapi.TakedownSeries, t.SeriesID
		if act == actTakedownChapter {
			kind, tid = adminapi.TakedownChapter, t.ChapterID
		}
		td := adminapi.Takedown{Kind: kind, TargetID: tid, SeriesID: t.SeriesID, ReportID: row.ID, Reason: row.Reason, Note: note,
			By: admin.ID, At: m.now().Format(time.RFC3339Nano)}
		auditKind, auditID = kind, tid
		data["project_id"] = t.ProjectID
		err := m.d.DB.WithTx(ctx, func(tx *db.Tx) error {
			if _, err := tx.Exec(ctx, `INSERT INTO instance_settings (key, value, updated_by, updated_at) VALUES (?, ?, ?, ?)
				ON CONFLICT (key) DO NOTHING`, adminapi.TakedownKey(kind, tid), db.MustJSON(td), admin.ID, m.now()); err != nil {
				return err
			}
			return resolveReports(ctx, tx, row, all, status, admin.ID, resolution, m.now())
		})
		if err != nil {
			return "", err
		}
		m.audit(ctx, "admin.takedown", auditKind, auditID, data)
		return "", nil
	case actSuspend:
		if err := m.d.Auth.SetStatus(ctx, t.Account.ID, auth.StatusSuspended, resolution); err != nil {
			if errors.Is(err, auth.ErrLastAdmin) {
				return i18n.T(ctx, "admin.error.last_admin"), nil
			}
			return "", err
		}
		auditKind, auditID = "user", t.Account.ID
	case actReviewLabel:
		if err := m.askLabelReview(ctx, t); err != nil {
			return "", err
		}
		auditKind, auditID = "series", t.SeriesID
		if t.ChapterID != "" {
			auditKind, auditID = "chapter", t.ChapterID
		}
	}
	if err := m.d.DB.WithTx(ctx, func(tx *db.Tx) error {
		return resolveReports(ctx, tx, row, all, status, admin.ID, resolution, m.now())
	}); err != nil {
		return "", err
	}
	m.audit(ctx, "admin.report."+act, auditKind, auditID, data)
	return "", nil
}

// resolveReports closes row (and, when all is set, every other open report on
// the same target) with status and the resolution text.
func resolveReports(ctx context.Context, tx *db.Tx, row reportRow, all bool, status, by, resolution string, now time.Time) error {
	if !all {
		_, err := tx.Exec(ctx, `UPDATE reports SET status = ?, resolved_by = ?, resolution = ?, resolved_at = ? WHERE id = ? AND status = ?`,
			status, by, resolution, now, row.ID, statusOpen)
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE reports SET status = ?, resolved_by = ?, resolution = ?, resolved_at = ?
		WHERE target_kind = ? AND target_id = ? AND status = ?`, status, by, resolution, now, row.TargetKind, row.TargetID, statusOpen)
	return err
}

// askLabelReview notifies the series owner, in their language, to check the
// chapter's (or series') AI labels in E1.
func (m *Module) askLabelReview(ctx context.Context, t target) error {
	owner := m.userOrNil(ctx, t.OwnerID)
	if owner == nil {
		return nil
	}
	inst := m.d.Settings.MustInstance(ctx)
	lctx := i18n.WithLocale(ctx, firstNonEmpty(owner.Locale, inst.DefaultLocale, "en"))
	link := "/studio/p/" + url.PathEscape(t.ProjectID) + "/series"
	title := i18n.T(lctx, "admin.notify.label_series", "series", t.SeriesTitle)
	if t.PublicationID != "" {
		link = "/studio/p/" + url.PathEscape(t.ProjectID) + "/chapters/" + url.PathEscape(number(t.Number)) + "/publish?lang=" + url.QueryEscape(t.Lang)
		title = i18n.T(lctx, "admin.notify.label_chapter", "series", t.SeriesTitle, "number", number(t.Number))
	}
	_, err := m.d.Notify.Notify(ctx, notify.Input{UserID: owner.ID, Kind: notify.KindSystem, Title: title,
		Body: i18n.T(lctx, "admin.notify.label_body"), Link: link, ActorID: core.UserFrom(ctx).ID,
		Data: map[string]any{"kind": "ai_label_review", "series_id": t.SeriesID, "chapter_id": t.ChapterID}})
	return err
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// POST /admin/takedowns/restore (key) lifts a takedown: the content is
// readable again exactly as before.
func (m *Module) restoreTakedown(w http.ResponseWriter, r *http.Request) error {
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("form")
	}
	ctx := r.Context()
	key := web.FormValue(r, "key")
	if !strings.HasPrefix(key, adminapi.SeriesTakedownPrefix) && !strings.HasPrefix(key, adminapi.ChapterTakedownPrefix) {
		return core.ErrNotFound()
	}
	res, err := m.d.DB.Exec(ctx, `DELETE FROM instance_settings WHERE key = ?`, key)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.ErrNotFound()
	}
	kind, id := adminapi.TakedownSeries, strings.TrimPrefix(key, adminapi.SeriesTakedownPrefix)
	if strings.HasPrefix(key, adminapi.ChapterTakedownPrefix) {
		kind, id = adminapi.TakedownChapter, strings.TrimPrefix(key, adminapi.ChapterTakedownPrefix)
	}
	m.audit(ctx, "admin.takedown.restore", kind, id, nil)
	return done(w, r, "/admin")
}
