package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/notify"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// kindAgentRequest is the notification kind of a request waiting for a person.
const kindAgentRequest = "agent_request"

type requestIn struct {
	ProjectID string `json:"project_id" jsonschema:"the project id"`
	Action    string `json:"action" jsonschema:"publish, schedule, delete_page, delete_project, share, change_role or unlock"`
	ChapterID string `json:"chapter_id,omitempty" jsonschema:"publish, schedule and unlock: the chapter"`
	PageID    string `json:"page_id,omitempty" jsonschema:"delete_page and unlock: the page"`
	PanelID   string `json:"panel_id,omitempty" jsonschema:"unlock: one panel of that page"`
	Note      string `json:"note,omitempty" jsonschema:"one short sentence for the person: why you ask"`
}

func (m *Module) requestHuman(ctx context.Context, req *mcpsdk.CallToolRequest, in requestIn) (*mcpsdk.CallToolResult, any, error) {
	a, err := m.agentFor(ctx, req, scopeRead)
	if err != nil {
		return nil, nil, err
	}
	if !m.ready(ctx) {
		return nil, nil, errUnavailable
	}
	action := strings.TrimSpace(in.Action)
	if !isHumanAction(action) {
		return nil, nil, errors.New("action must be publish, schedule, delete_page, delete_project, share, change_role or unlock; anything else you can do with your own tools")
	}
	p, err := m.project(ctx, a, in.ProjectID, studio.ActRead)
	if err != nil {
		return nil, nil, err
	}
	if err := m.writeAllowed(a); err != nil {
		return nil, nil, err
	}
	detail := actionDetail{Note: clip(strings.TrimSpace(in.Note), 280)}
	targetKind, targetID := "project", p.ID
	switch action {
	case reqPublish, reqSchedule:
		c, err := m.chapterOf(ctx, p.ID, in.ChapterID)
		if err != nil {
			return nil, nil, err
		}
		targetKind, targetID, detail.ChapterNumber = "chapter", c.ID, c.Number
	case reqDeletePage, reqUnlock:
		if action == reqUnlock && strings.TrimSpace(in.PageID) == "" {
			c, err := m.chapterOf(ctx, p.ID, in.ChapterID)
			if err != nil {
				return nil, nil, errors.New("unlock needs the chapter_id or the page_id (and optionally panel_id)")
			}
			targetKind, targetID, detail.ChapterNumber = "chapter", c.ID, c.Number
			break
		}
		page, err := m.pageOf(ctx, p.ID, in.PageID)
		if err != nil {
			return nil, nil, err
		}
		c, err := m.studio.GetChapter(ctx, page.ChapterID)
		if err != nil {
			return nil, nil, toolError(err)
		}
		targetKind, targetID = "page", page.ID
		detail.ChapterNumber, detail.PageNumber, detail.PageID = c.Number, page.Number, page.ID
		if action == reqUnlock && strings.TrimSpace(in.PanelID) != "" {
			found := false
			for i, pn := range page.Panels {
				if pn.ID == strings.TrimSpace(in.PanelID) {
					targetKind, targetID, found = "panel", pn.ID, true
					detail.PanelLabel = strconv.Itoa(page.Number) + "." + strconv.Itoa(i+1)
				}
			}
			if !found {
				return nil, nil, errors.New("panel_id is not a panel of that page")
			}
		}
	}
	row, err := m.createRequest(ctx, a, p, action, targetKind, targetID, detail, jobs.Cost{})
	if err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{"request_id": row.ID, "status": row.Status,
		"message": "Nothing was done. A person decides in Agents & MCP; check list_recent_actions for the answer."}, nil
}

func (m *Module) pageOf(ctx context.Context, projectID, pageID string) (pagedoc.Page, error) {
	if !db.ValidID(strings.TrimSpace(pageID)) {
		return pagedoc.Page{}, toolError(studio.ErrNotFound)
	}
	page, err := m.pages.GetPage(ctx, strings.TrimSpace(pageID))
	if err != nil || page.ProjectID != projectID {
		return pagedoc.Page{}, toolError(studio.ErrNotFound)
	}
	return page, nil
}

// createRequest records a request that waits for a person (an identical one
// that is still waiting is reused) and notifies the token owner and the
// project owner.
func (m *Module) createRequest(ctx context.Context, a *agent, p studio.Project, action, targetKind, targetID string, detail actionDetail, estimate jobs.Cost) (actionRow, error) {
	var existing actionRow
	err := m.d.DB.Get(ctx, &existing, `SELECT `+actionCols+` FROM agent_actions WHERE token_id = ? AND kind = ? AND action = ?
		AND target_id = ? AND status = ? ORDER BY created_at DESC LIMIT 1`, a.tok.ID, kindRequest, action, targetID, statusPending)
	if err == nil && action != reqGenerate {
		return existing, nil
	}
	n, err := m.pendingCount(ctx, a.tok.ID)
	if err != nil {
		return actionRow{}, toolError(err)
	}
	if n >= maxPendingPerToken {
		return actionRow{}, fmt.Errorf("%d requests from this token already wait for a person; wait for answers before asking again", n)
	}
	row, err := m.insertAction(ctx, m.d.DB, newAction{UserID: a.user.ID, TokenID: a.tok.ID, TokenName: a.tok.Name, ProjectID: p.ID,
		Kind: kindRequest, Action: action, TargetKind: targetKind, TargetID: targetID, Status: statusPending, Detail: detail, Estimate: estimate})
	if err != nil {
		return actionRow{}, toolError(err)
	}
	m.audit(ctx, a, "agent.request", targetKind, targetID, p.ID, map[string]any{"action_id": row.ID, "action": action})
	recipients := []string{a.user.ID}
	if p.OwnerID != "" && p.OwnerID != a.user.ID {
		recipients = append(recipients, p.OwnerID)
	}
	for _, uid := range recipients {
		m.notifyRequest(ctx, uid, row, p)
	}
	return row, nil
}

// notifyRequest tells a person a request waits for them, in their language.
func (m *Module) notifyRequest(ctx context.Context, userID string, row actionRow, p studio.Project) {
	if m.d.Notify == nil {
		return
	}
	lang := "en"
	if inst, err := m.d.Settings.Instance(ctx); err == nil && inst.DefaultLocale != "" {
		lang = inst.DefaultLocale
	}
	if prefs, err := m.d.Settings.UserPrefs(ctx, userID); err == nil && prefs.Languages.UI != "" {
		lang = prefs.Languages.UI
	} else if u, err := m.d.Auth.UserByID(ctx, userID); err == nil && u.Locale != "" {
		lang = u.Locale
	}
	lctx := i18n.WithLocale(context.WithoutCancel(ctx), lang)
	_, err := m.d.Notify.Notify(lctx, notify.Input{UserID: userID, Kind: kindAgentRequest,
		Title: i18n.T(lctx, "agents.notify.title", "agent", row.TokenName),
		Body:  i18n.T(lctx, "agents.notify.body", "action", describe(lctx, row), "project", p.Title),
		Link:  "/agents", Data: map[string]any{"action_id": row.ID}, GroupKey: "agents:" + row.TokenID})
	if err != nil {
		m.d.Log.WarnContext(ctx, "mcp: notify agent request", "err", err)
	}
}

// canDecide reports whether a person may decide on or undo an action: the
// token owner, or the owner of the project it touched.
func (m *Module) canDecide(ctx context.Context, u *httpx.User, row actionRow) bool {
	if u == nil {
		return false
	}
	if row.UserID == u.ID {
		return true
	}
	if !row.ProjectID.Valid {
		return false
	}
	p, err := m.studio.GetProject(ctx, row.ProjectID.String)
	return err == nil && p.OwnerID == u.ID
}

// humanURL is the real screen where a person takes a requested action
// ("" when the request carries too little to find it).
func (m *Module) humanURL(ctx context.Context, row actionRow) string {
	if !row.ProjectID.Valid {
		return ""
	}
	base := "/studio/p/" + url.PathEscape(row.ProjectID.String)
	d := row.Detail.V
	chapter := base + "/chapters/" + strconv.Itoa(d.ChapterNumber)
	switch row.Action {
	case reqPublish, reqSchedule:
		if d.ChapterNumber > 0 {
			return chapter + "/publish"
		}
	case reqDeletePage, reqUnlock:
		if d.ChapterNumber > 0 && d.PageNumber > 0 {
			return chapter + "/edit?page=" + strconv.Itoa(d.PageNumber)
		}
		if d.ChapterNumber > 0 {
			return chapter + "/edit"
		}
	case reqDeleteProject:
		return base + "/settings"
	case reqShare, reqChangeRole:
		return base + "/members"
	}
	return ""
}

var (
	errDecided  = errors.New("mcp: already decided")
	errNotYours = errors.New("mcp: not yours to decide")
	errChanged  = errors.New("mcp: changed since the agent's change")
	errNoUndo   = errors.New("mcp: nothing to undo")
)

// decision is the result of approving a request.
type decision struct {
	JobID string
	// Redirect is the human screen to open for a human-only action.
	Redirect string
}

// approve decides a pending request. A generation runs as the approving
// person (their permissions and spend, exactly the stored request); a
// human-only action is never performed here: the person is sent to its
// screen to do it themselves.
func (m *Module) approve(ctx context.Context, u *httpx.User, id string) (decision, error) {
	row, err := m.getAction(ctx, m.d.DB, id)
	if err != nil {
		return decision{}, errNotYours
	}
	if !m.canDecide(ctx, u, row) {
		return decision{}, errNotYours
	}
	if row.Kind != kindRequest || row.Status != statusPending {
		return decision{}, errDecided
	}
	if row.Action != reqGenerate {
		target := m.humanURL(ctx, row)
		if target == "" || !httpx.Serves(ctx, "GET", strings.SplitN(target, "?", 2)[0]) {
			return decision{}, errNoScreen
		}
		ok, err := m.transition(ctx, m.d.DB, row.ID, statusPending, statusApproved, u.ID, "")
		if err != nil {
			return decision{}, err
		}
		if !ok {
			return decision{}, errDecided
		}
		m.d.Auth.Audit(ctx, auth.AuditEntry{ActorID: u.ID, ActorKind: "user", Action: "agent.request.approve",
			TargetKind: row.TargetKind, TargetID: row.TargetID, ProjectID: row.ProjectID.String,
			Data: map[string]any{"action_id": row.ID, "action": row.Action}})
		return decision{Redirect: target}, nil
	}
	g := row.Detail.V.Generation
	if g == nil {
		return decision{}, errDecided
	}
	// Claim first so two people (or tabs) cannot both start the work.
	ok, err := m.transition(ctx, m.d.DB, row.ID, statusPending, statusApproved, u.ID, "")
	if err != nil {
		return decision{}, err
	}
	if !ok {
		return decision{}, errDecided
	}
	actor, err := m.studio.ActorFromUser(ctx, u.ID)
	var j *jobs.Job
	if err == nil {
		j, err = m.enqueue(ctx, actor, *g)
	}
	if err != nil {
		if _, rerr := m.d.DB.Exec(context.WithoutCancel(ctx), `UPDATE agent_actions SET status = ?, decided_by = NULL, decided_at = NULL, updated_at = ? WHERE id = ? AND status = ?`,
			statusPending, db.Now(), row.ID, statusApproved); rerr != nil {
			m.d.Log.ErrorContext(ctx, "mcp: reopen request", "action", row.ID, "err", rerr)
		}
		return decision{}, err
	}
	if _, err := m.d.DB.Exec(ctx, `UPDATE agent_actions SET job_id = ?, estimate = ?, updated_at = ? WHERE id = ?`,
		j.ID, db.MustJSON(j.Estimate), db.Now(), row.ID); err != nil {
		m.d.Log.ErrorContext(ctx, "mcp: link approved job", "action", row.ID, "err", err)
	}
	m.d.Auth.Audit(ctx, auth.AuditEntry{ActorID: u.ID, ActorKind: "user", Action: "agent.request.approve",
		TargetKind: row.TargetKind, TargetID: row.TargetID, ProjectID: row.ProjectID.String,
		Data: map[string]any{"action_id": row.ID, "action": row.Action, "job_id": j.ID}})
	return decision{JobID: j.ID}, nil
}

var errNoScreen = errors.New("mcp: that screen is not available yet")

// decline refuses a pending request; nothing runs.
func (m *Module) decline(ctx context.Context, u *httpx.User, id string) error {
	row, err := m.getAction(ctx, m.d.DB, id)
	if err != nil || !m.canDecide(ctx, u, row) {
		return errNotYours
	}
	if row.Kind != kindRequest {
		return errDecided
	}
	ok, err := m.transition(ctx, m.d.DB, row.ID, statusPending, statusDeclined, u.ID, "")
	if err != nil {
		return err
	}
	if !ok {
		return errDecided
	}
	m.d.Auth.Audit(ctx, auth.AuditEntry{ActorID: u.ID, ActorKind: "user", Action: "agent.request.decline",
		TargetKind: row.TargetKind, TargetID: row.TargetID, ProjectID: row.ProjectID.String,
		Data: map[string]any{"action_id": row.ID, "action": row.Action}})
	return nil
}

// undoable reports whether the history behind an action supports a revert.
func undoable(row actionRow, job *jobs.Job) bool {
	if row.Status != statusDone {
		return false
	}
	switch row.Kind {
	case kindScriptEdit, kindCharacterEdit:
		return true
	case kindGenerate:
		return (row.Action == "page" || row.Action == "region") && job != nil && job.State == jobs.StateDone
	}
	return false
}

// undo reverts an agent change as the person, through the same services and
// revision fences as the UI. It refuses when someone changed the same thing
// after the agent did.
func (m *Module) undo(ctx context.Context, u *httpx.User, id string) error {
	row, err := m.getAction(ctx, m.d.DB, id)
	if err != nil || !m.canDecide(ctx, u, row) {
		return errNotYours
	}
	if row.Status != statusDone {
		return errDecided
	}
	actor, err := m.studio.ActorFromUser(ctx, u.ID)
	if err != nil {
		return err
	}
	// Claim the row so a double click cannot undo twice.
	ok, err := m.transition(ctx, m.d.DB, row.ID, statusDone, statusUndone, u.ID, "")
	if err != nil {
		return err
	}
	if !ok {
		return errDecided
	}
	switch row.Kind {
	case kindScriptEdit:
		err = m.undoScript(ctx, actor, row)
	case kindCharacterEdit:
		err = m.undoCharacter(ctx, actor, row)
	case kindGenerate:
		err = m.undoGenerate(ctx, actor, row)
	default:
		err = errNoUndo
	}
	if err != nil {
		if _, rerr := m.d.DB.Exec(context.WithoutCancel(ctx), `UPDATE agent_actions SET status = ?, decided_by = NULL, decided_at = NULL, updated_at = ? WHERE id = ? AND status = ?`,
			statusDone, db.Now(), row.ID, statusUndone); rerr != nil {
			m.d.Log.ErrorContext(ctx, "mcp: restore undo claim", "action", row.ID, "err", rerr)
		}
		return err
	}
	m.d.Auth.Audit(ctx, auth.AuditEntry{ActorID: u.ID, ActorKind: "user", Action: "agent.undo", TargetKind: row.TargetKind,
		TargetID: row.TargetID, ProjectID: row.ProjectID.String, Data: map[string]any{"action_id": row.ID, "kind": row.Kind, "token_id": row.TokenID}})
	return nil
}

func (m *Module) undoScript(ctx context.Context, actor studio.Actor, row actionRow) error {
	var u scriptUndo
	if err := json.Unmarshal(row.Undo.V, &u); err != nil || u.ChapterID == "" || len(u.Panels) == 0 {
		return errNoUndo
	}
	c, err := m.studio.GetChapter(ctx, u.ChapterID)
	if err != nil || !row.ProjectID.Valid || c.ProjectID != row.ProjectID.String {
		return errNoUndo
	}
	doc, err := m.studio.GetScript(ctx, c.ID)
	if err != nil {
		return err
	}
	byID := map[string]*studio.ScriptPanel{}
	for si := range doc.Scenes {
		for pi := range doc.Scenes[si].Panels {
			byID[doc.Scenes[si].Panels[pi].ID] = &doc.Scenes[si].Panels[pi]
		}
	}
	for _, ch := range u.Panels {
		sp := byID[ch.ID]
		if sp == nil {
			return errChanged
		}
		for f, after := range ch.After {
			cur, _ := json.Marshal(panelField(sp, f))
			if string(cur) != string(after) {
				return errChanged
			}
		}
		for f, before := range ch.Before {
			if err := setPanelField(sp, f, before); err != nil {
				return errNoUndo
			}
		}
	}
	_, err = m.studio.EditScript(ctx, studio.EditScriptInput{Actor: actor, ProjectID: c.ProjectID, ChapterID: c.ID, Doc: doc, ExpectedUpdatedAt: c.UpdatedAt})
	if errors.Is(err, studio.ErrConflict) {
		return errChanged
	}
	return err
}

func (m *Module) undoCharacter(ctx context.Context, actor studio.Actor, row actionRow) error {
	var u characterUndo
	if err := json.Unmarshal(row.Undo.V, &u); err != nil || u.CharacterID == "" || len(u.Fields) == 0 {
		return errNoUndo
	}
	c, err := m.studio.GetCharacter(ctx, u.CharacterID)
	if err != nil || !row.ProjectID.Valid || c.ProjectID != row.ProjectID.String {
		return errNoUndo
	}
	next := newCharacterOf(c)
	current := characterValues(next)
	for _, f := range u.Fields {
		if current[f] != u.After[f] {
			return errChanged
		}
		setCharacterValue(&next, f, u.Before[f])
	}
	_, err = m.bible.UpdateCharacter(ctx, actor, studio.UpdateCharacterInput{ProjectID: c.ProjectID, CharacterID: c.ID, Character: next, ExpectedUpdatedAt: c.UpdatedAt})
	if errors.Is(err, studio.ErrConflict) {
		return errChanged
	}
	return err
}

// undoGenerate reverts the page to the version before the job's change, if
// the job's version is still the latest.
func (m *Module) undoGenerate(ctx context.Context, actor studio.Actor, row actionRow) error {
	var u struct {
		PageID string `json:"page_id"`
	}
	if err := json.Unmarshal(row.Undo.V, &u); err != nil || u.PageID == "" || !row.JobID.Valid {
		return errNoUndo
	}
	page, err := m.pages.GetPage(ctx, u.PageID)
	if err != nil || !row.ProjectID.Valid || page.ProjectID != row.ProjectID.String {
		return errNoUndo
	}
	versions, err := m.pages.Versions(ctx, page.ID, 500)
	if err != nil {
		return err
	}
	first, last := 0, 0
	for _, v := range versions {
		if v.JobID == row.JobID.String {
			if first == 0 || v.Number < first {
				first = v.Number
			}
			last = max(last, v.Number)
		}
	}
	if first == 0 {
		return errNoUndo
	}
	if page.Version != last {
		return errChanged
	}
	var base *pagestore.Version
	for i := range versions {
		v := &versions[i]
		if v.Number < first && (base == nil || v.Number > base.Number) {
			base = v
		}
	}
	if base == nil {
		return errNoUndo
	}
	_, err = m.pages.Revert(ctx, page.ID, base.ID, pagestore.Change{ActorKind: "user", ActorID: actor.UserID, Action: "revert",
		Summary: "Undo agent change · " + row.TokenName, ExpectedVersion: page.Version})
	if errors.Is(err, pagestore.ErrVersion) {
		return errChanged
	}
	return err
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
