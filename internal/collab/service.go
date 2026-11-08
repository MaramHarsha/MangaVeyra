package collab

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// Comment statuses (comments.status).
const (
	StatusOpen         = "open"
	StatusNeedsChanges = "needs_changes"
	StatusDone         = "done"
	StatusApproved     = "approved"
)

// Limits.
const (
	maxBody              = 2000
	maxCommentsPerPage   = 500
	maxRepliesPerComment = 200
)

// Errors. Handlers map them to HTTP; fieldError carries a localized field message.
var (
	errNotFound  = errors.New("collab: not found")
	errForbidden = errors.New("collab: forbidden")
	errConflict  = errors.New("collab: conflict")
	errSession   = errors.New("collab: needs a browser session")
	// errAgent refuses agent tokens: review is for people, and agents act
	// only through /mcp (the owner's switch, per-token limits, agent log).
	errAgent = errors.New("collab: agents use MCP")
)

type fieldError struct{ field, key string }

func (e fieldError) Error() string { return "collab: invalid " + e.field }

// Person is a member as the drawer shows them.
type Person struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Role     string `json:"role,omitempty"`
}

// Reply is an answer in a comment's thread.
type Reply struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	Author    Person    `json:"author"`
	Agent     bool      `json:"agent,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Comment is a top-level review comment on a panel.
type Comment struct {
	ID         string     `json:"id"`
	PanelID    string     `json:"panel_id"`
	Label      string     `json:"label"`
	X          float64    `json:"x"`
	Y          float64    `json:"y"`
	Body       string     `json:"body"`
	Status     string     `json:"status"`
	Author     Person     `json:"author"`
	Assignee   *Person    `json:"assignee,omitempty"`
	Agent      bool       `json:"agent,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	ResolvedBy *Person    `json:"resolved_by,omitempty"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	Replies    []Reply    `json:"replies"`
	// Next lists the statuses the viewer may set now.
	Next []string `json:"next"`
}

// Approval is the latest approval of a page. Current is false once the page
// changed after it was approved.
type Approval struct {
	By      Person    `json:"by"`
	At      time.Time `json:"at"`
	Version int       `json:"version"`
	Current bool      `json:"current"`
}

// Counts summarise a page's review: open counts every comment not approved
// yet (open, needs changes, done), resolved the approved ones.
type Counts struct {
	Open         int `json:"open"`
	Resolved     int `json:"resolved"`
	NeedsChanges int `json:"needs_changes"`
	Approved     int `json:"approved"`
}

// Viewer is what the caller may do in the drawer.
type Viewer struct {
	ID         string `json:"id"`
	Role       string `json:"role,omitempty"`
	CanComment bool   `json:"can_comment"`
	CanReview  bool   `json:"can_review"`
	CanManage  bool   `json:"can_manage"`
}

// Member is a project member with role and scope.
type Member struct {
	UserID   string             `json:"user_id"`
	Name     string             `json:"name"`
	Username string             `json:"username"`
	Role     string             `json:"role"`
	Scope    studio.MemberScope `json:"scope"`
}

// Review is the D7 drawer for one page.
type Review struct {
	PageID      string    `json:"page_id"`
	ChapterID   string    `json:"chapter_id"`
	PageNumber  int       `json:"page_number"`
	PageVersion int       `json:"page_version"`
	Comments    []Comment `json:"comments"`
	Counts      Counts    `json:"counts"`
	Approval    *Approval `json:"approval,omitempty"`
	Members     []Member  `json:"members"`
	Viewer      Viewer    `json:"viewer"`
}

// caller is the requester: a person in a browser session or a bearer token.
type caller struct {
	actor   studio.Actor
	user    *httpx.User
	token   *httpx.Token
	session bool
}

func callerFrom(id httpx.Identity) (caller, error) {
	if id.User == nil {
		return caller{}, errForbidden
	}
	if id.IsAgent() {
		return caller{}, errAgent
	}
	c := caller{user: id.User, actor: studio.Actor{UserID: id.User.ID, SiteRole: id.User.Role}}
	if id.Token != nil {
		c.token, c.actor.TokenID = id.Token, id.Token.ID
	} else {
		c.session = id.SessionID != ""
	}
	return c, nil
}

// anchor is the stored JSON of comments.anchor.
type anchor struct {
	PageID      string   `json:"page_id,omitempty"`
	PanelID     string   `json:"panel_id,omitempty"`
	Label       string   `json:"label,omitempty"`
	X           *float64 `json:"x,omitempty"`
	Y           *float64 `json:"y,omitempty"`
	TokenID     string   `json:"token_id,omitempty"`
	PageVersion int      `json:"page_version,omitempty"`
}

type commentRow struct {
	ID         string         `db:"id"`
	ProjectID  string         `db:"project_id"`
	TargetKind string         `db:"target_kind"`
	TargetID   string         `db:"target_id"`
	Anchor     string         `db:"anchor"`
	AuthorID   sql.NullString `db:"author_id"`
	AssigneeID sql.NullString `db:"assignee_id"`
	ParentID   sql.NullString `db:"parent_id"`
	Body       string         `db:"body"`
	Status     string         `db:"status"`
	ResolvedBy sql.NullString `db:"resolved_by"`
	ResolvedAt sql.NullTime   `db:"resolved_at"`
	CreatedAt  time.Time      `db:"created_at"`
}

const commentCols = `id, project_id, target_kind, target_id, anchor, author_id, assignee_id, parent_id, body, status, resolved_by, resolved_at, created_at`

func (r commentRow) anchor() anchor {
	var a anchor
	_ = json.Unmarshal([]byte(r.Anchor), &a)
	return a
}

// ---- authorization -----------------------------------------------------------

// read checks project read access; anything else is reported as not found.
func (m *Module) read(ctx context.Context, c caller, projectID string) error {
	if !db.ValidID(projectID) {
		return errNotFound
	}
	if err := m.studio.Authorize(ctx, c.actor, projectID, studio.ActRead); err != nil {
		if errors.Is(err, studio.ErrNotFound) || errors.Is(err, studio.ErrForbidden) || errors.Is(err, studio.ErrInvalid) {
			return errNotFound
		}
		return err
	}
	return nil
}

// canWrite reports whether the caller may post comments: a session, or a
// token with projects:write.
func (c caller) canWrite() bool {
	return c.session || (c.token != nil && c.token.HasScope("projects:write"))
}

// reviewer reports whether the caller reviews: an owner or editor of the
// project (the member scope does not narrow reviewing) or a site admin, in a
// browser session.
func (c caller) reviewer(role string) bool {
	return c.session && (role == studio.RoleOwner || role == studio.RoleEditor || c.user.IsAdmin())
}

func (m *Module) roleOf(ctx context.Context, projectID, userID string) (string, error) {
	var role string
	err := m.d.DB.Get(ctx, &role, `SELECT role FROM project_members WHERE project_id = ? AND user_id = ?`, projectID, userID)
	if db.IsNotFound(err) {
		return "", nil
	}
	return role, err
}

// next lists the statuses c may set on a comment now.
func next(c caller, role string, row commentRow) []string {
	out := []string{}
	if !c.session {
		return out
	}
	mine := row.AuthorID.Valid && row.AuthorID.String == c.user.ID
	assigned := row.AssigneeID.Valid && row.AssigneeID.String == c.user.ID
	for _, s := range []string{StatusOpen, StatusNeedsChanges, StatusDone, StatusApproved} {
		if s == row.Status {
			continue
		}
		switch {
		case c.reviewer(role):
			out = append(out, s)
		case (mine || assigned) && s == StatusDone && row.Status != StatusApproved:
			out = append(out, s)
		case (mine || assigned) && s == StatusOpen && row.Status == StatusDone:
			out = append(out, s)
		}
	}
	return out
}

// ---- reading -------------------------------------------------------------------

func (m *Module) pageIn(ctx context.Context, projectID, pageID string) (pagedoc.Page, error) {
	if !db.ValidID(pageID) {
		return pagedoc.Page{}, errNotFound
	}
	p, err := m.pages.GetPage(ctx, pageID)
	if errors.Is(err, pagestore.ErrNotFound) || (err == nil && p.ProjectID != projectID) {
		return pagedoc.Page{}, errNotFound
	}
	return p, err
}

func (m *Module) review(ctx context.Context, c caller, projectID, pageID string) (Review, error) {
	if err := m.read(ctx, c, projectID); err != nil {
		return Review{}, err
	}
	p, err := m.pageIn(ctx, projectID, pageID)
	if err != nil {
		return Review{}, err
	}
	members, err := m.members(ctx, projectID)
	if err != nil {
		return Review{}, err
	}
	roles := map[string]string{}
	for _, mb := range members {
		roles[mb.UserID] = mb.Role
	}
	role := roles[c.user.ID]
	out := Review{PageID: p.ID, ChapterID: p.ChapterID, PageNumber: p.Number, PageVersion: p.Version, Comments: []Comment{}, Members: members,
		Viewer: Viewer{ID: c.user.ID, Role: role, CanComment: c.canWrite(), CanReview: c.reviewer(role)}}
	if c.session {
		out.Viewer.CanManage = m.studio.Authorize(ctx, c.actor, projectID, studio.ActManage) == nil
	}
	rows, err := m.pageComments(ctx, projectID, p)
	if err != nil {
		return Review{}, err
	}
	approval, err := m.latestApproval(ctx, projectID, p.ID)
	if err != nil {
		return Review{}, err
	}
	people, err := m.people(ctx, rows, approval, roles)
	if err != nil {
		return Review{}, err
	}
	labels, index := map[string]string{}, map[string]int{}
	for i, pn := range p.Panels {
		labels[pn.ID] = strconv.Itoa(p.Number) + "." + strconv.Itoa(i+1)
	}
	for _, r := range rows {
		if r.ParentID.Valid {
			continue
		}
		a := r.anchor()
		cm := Comment{ID: r.ID, PanelID: r.TargetID, Label: labels[r.TargetID], Body: r.Body, Status: r.Status,
			Author: person(people, r.AuthorID), Agent: a.TokenID != "", CreatedAt: r.CreatedAt, Replies: []Reply{}, Next: next(c, role, r)}
		if a.X != nil && a.Y != nil {
			cm.X, cm.Y = *a.X, *a.Y
		}
		if r.AssigneeID.Valid {
			pp := person(people, r.AssigneeID)
			cm.Assignee = &pp
		}
		if r.ResolvedBy.Valid {
			pp := person(people, r.ResolvedBy)
			cm.ResolvedBy = &pp
		}
		if r.ResolvedAt.Valid {
			t := r.ResolvedAt.Time.UTC()
			cm.ResolvedAt = &t
		}
		index[r.ID] = len(out.Comments)
		out.Comments = append(out.Comments, cm)
		switch r.Status {
		case StatusApproved:
			out.Counts.Resolved++
			out.Counts.Approved++
		case StatusNeedsChanges:
			out.Counts.Open++
			out.Counts.NeedsChanges++
		default:
			out.Counts.Open++
		}
	}
	for _, r := range rows {
		if !r.ParentID.Valid {
			continue
		}
		if i, ok := index[r.ParentID.String]; ok {
			out.Comments[i].Replies = append(out.Comments[i].Replies, Reply{ID: r.ID, Body: r.Body, Author: person(people, r.AuthorID), Agent: r.anchor().TokenID != "", CreatedAt: r.CreatedAt})
		}
	}
	if approval != nil {
		a := approval.anchor()
		out.Approval = &Approval{By: person(people, approval.AuthorID), At: approval.CreatedAt, Version: a.PageVersion, Current: a.PageVersion == p.Version}
	}
	return out, nil
}

// pageComments returns the internal comments and replies on the page's panels, oldest first.
func (m *Module) pageComments(ctx context.Context, projectID string, p pagedoc.Page) ([]commentRow, error) {
	if len(p.Panels) == 0 {
		return nil, nil
	}
	ids := make([]any, 0, len(p.Panels))
	for _, pn := range p.Panels {
		ids = append(ids, pn.ID)
	}
	q, args, err := db.In(`SELECT `+commentCols+` FROM comments WHERE project_id = ? AND visibility = 'internal' AND deleted_at IS NULL
		AND target_kind = 'panel' AND target_id IN (?) ORDER BY created_at, id`, projectID, ids)
	if err != nil {
		return nil, err
	}
	var rows []commentRow
	if err := m.d.DB.Select(ctx, &rows, q, args...); err != nil {
		return nil, fmt.Errorf("collab: list comments: %w", err)
	}
	return rows, nil
}

func (m *Module) latestApproval(ctx context.Context, projectID, pageID string) (*commentRow, error) {
	var r commentRow
	err := m.d.DB.Get(ctx, &r, `SELECT `+commentCols+` FROM comments WHERE project_id = ? AND visibility = 'internal' AND deleted_at IS NULL
		AND target_kind = 'page' AND target_id = ? AND status = 'approved' AND parent_id IS NULL ORDER BY created_at DESC, id DESC LIMIT 1`, projectID, pageID)
	if db.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("collab: page approval: %w", err)
	}
	return &r, nil
}

// people loads the names of everyone the rows mention.
func (m *Module) people(ctx context.Context, rows []commentRow, approval *commentRow, roles map[string]string) (map[string]Person, error) {
	seen := map[string]bool{}
	ids := []any{}
	add := func(v sql.NullString) {
		if v.Valid && !seen[v.String] {
			seen[v.String] = true
			ids = append(ids, v.String)
		}
	}
	for _, r := range rows {
		add(r.AuthorID)
		add(r.AssigneeID)
		add(r.ResolvedBy)
	}
	if approval != nil {
		add(approval.AuthorID)
	}
	out := map[string]Person{}
	if len(ids) == 0 {
		return out, nil
	}
	q, args, err := db.In(`SELECT id, username, display_name FROM users WHERE id IN (?)`, ids)
	if err != nil {
		return nil, err
	}
	var us []struct {
		ID          string `db:"id"`
		Username    string `db:"username"`
		DisplayName string `db:"display_name"`
	}
	if err := m.d.DB.Select(ctx, &us, q, args...); err != nil {
		return nil, fmt.Errorf("collab: people: %w", err)
	}
	for _, u := range us {
		out[u.ID] = Person{ID: u.ID, Name: displayName(u.DisplayName, u.Username), Username: u.Username, Role: roles[u.ID]}
	}
	return out, nil
}

func person(people map[string]Person, id sql.NullString) Person {
	if !id.Valid {
		return Person{}
	}
	if p, ok := people[id.String]; ok {
		return p
	}
	return Person{ID: id.String}
}

func displayName(name, username string) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	return username
}

// members lists the project's members, the owner first.
func (m *Module) members(ctx context.Context, projectID string) ([]Member, error) {
	var rows []struct {
		UserID      string    `db:"user_id"`
		Role        string    `db:"role"`
		Scope       string    `db:"scope"`
		Username    string    `db:"username"`
		DisplayName string    `db:"display_name"`
		CreatedAt   time.Time `db:"created_at"`
	}
	if err := m.d.DB.Select(ctx, &rows, `SELECT pm.user_id, pm.role, pm.scope, u.username, u.display_name, pm.created_at
		FROM project_members pm JOIN users u ON u.id = pm.user_id WHERE pm.project_id = ? ORDER BY pm.created_at, pm.user_id`, projectID); err != nil {
		return nil, fmt.Errorf("collab: members: %w", err)
	}
	out := make([]Member, 0, len(rows))
	for _, r := range rows {
		scope, _ := studio.ParseMemberScope(r.Scope)
		out = append(out, Member{UserID: r.UserID, Name: displayName(r.DisplayName, r.Username), Username: r.Username, Role: r.Role, Scope: scope})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Role == studio.RoleOwner && out[j].Role != studio.RoleOwner })
	return out, nil
}

// ---- writing -------------------------------------------------------------------

func cleanBody(body string) (string, error) {
	body = strings.TrimSpace(body)
	if !utf8.ValidString(body) || body == "" {
		return "", fieldError{"body", "collab.error.body_required"}
	}
	if utf8.RuneCountInString(body) > maxBody {
		return "", fieldError{"body", "collab.error.body_long"}
	}
	return body, nil
}

// NewComment is the input of a panel comment. X/Y place the pin in page
// units; without them the pin sits at the panel's top-left corner.
type NewComment struct {
	PanelID    string   `json:"panel_id"`
	Body       string   `json:"body"`
	X          *float64 `json:"x,omitempty"`
	Y          *float64 `json:"y,omitempty"`
	AssigneeID string   `json:"assignee_id,omitempty"`
}

func (m *Module) createComment(ctx context.Context, c caller, projectID, pageID string, in NewComment) (string, error) {
	if err := m.read(ctx, c, projectID); err != nil {
		return "", err
	}
	if !c.canWrite() {
		return "", errForbidden
	}
	body, err := cleanBody(in.Body)
	if err != nil {
		return "", err
	}
	p, err := m.pageIn(ctx, projectID, pageID)
	if err != nil {
		return "", err
	}
	idx := -1
	for i, pn := range p.Panels {
		if pn.ID == in.PanelID {
			idx = i
		}
	}
	if idx < 0 {
		return "", fieldError{"panel_id", "collab.error.panel"}
	}
	x, y, err := pinAt(p, p.Panels[idx], in.X, in.Y)
	if err != nil {
		return "", err
	}
	assignee := strings.TrimSpace(in.AssigneeID)
	if assignee != "" {
		role, err := m.roleOf(ctx, projectID, assignee)
		if err != nil {
			return "", err
		}
		if role == "" {
			return "", fieldError{"assignee_id", "collab.error.assignee"}
		}
	}
	a := anchor{PageID: p.ID, PanelID: in.PanelID, Label: strconv.Itoa(p.Number) + "." + strconv.Itoa(idx+1), X: &x, Y: &y}
	if c.token != nil {
		a.TokenID = c.token.ID
	}
	id := db.NewID()
	err = m.d.DB.WithTx(ctx, func(tx *db.Tx) error {
		if err := m.studio.AuthorizeTx(ctx, tx, c.actor, projectID, studio.ActRead); err != nil {
			return errNotFound
		}
		var n int
		if err := tx.Get(ctx, &n, `SELECT COUNT(*) FROM comments c JOIN panels pa ON pa.id = c.target_id
			WHERE c.visibility = 'internal' AND c.target_kind = 'panel' AND c.parent_id IS NULL AND pa.page_id = ?`, p.ID); err != nil {
			return err
		}
		if n >= maxCommentsPerPage {
			return fieldError{"body", "collab.error.too_many"}
		}
		now := db.Now()
		_, err := tx.Exec(ctx, `INSERT INTO comments (id, project_id, target_kind, target_id, anchor, visibility, author_id, assignee_id, body, status, created_at, updated_at)
			VALUES (?, ?, 'panel', ?, ?, 'internal', ?, ?, ?, 'open', ?, ?)`, id, projectID, in.PanelID, db.MustJSON(a), c.user.ID, db.NullString(assignee), body, now, now)
		return err
	})
	if err != nil {
		return "", err
	}
	m.changed(p.ID, id, "comment")
	if assignee != "" && assignee != c.user.ID {
		m.notifyMembers(ctx, c, projectID, p, a.Label, []string{assignee}, "collab.notify.assigned", "")
	}
	return id, nil
}

// pinAt returns the pin position, clamped to the page.
func pinAt(p pagedoc.Page, pn pagedoc.Panel, x, y *float64) (float64, float64, error) {
	if (x == nil) != (y == nil) {
		return 0, 0, fieldError{"x", "collab.error.pin"}
	}
	if x != nil {
		if math.IsNaN(*x) || math.IsNaN(*y) || math.IsInf(*x, 0) || math.IsInf(*y, 0) {
			return 0, 0, fieldError{"x", "collab.error.pin"}
		}
		return clamp(*x, float64(p.Width)), clamp(*y, float64(p.Height)), nil
	}
	if len(pn.Points) == 0 {
		return 0, 0, nil
	}
	minX, minY := math.Inf(1), math.Inf(1)
	for _, pt := range pn.Points {
		minX, minY = math.Min(minX, pt.X), math.Min(minY, pt.Y)
	}
	return clamp(minX, float64(p.Width)), clamp(minY, float64(p.Height)), nil
}

func clamp(v, max float64) float64 { return math.Round(math.Max(0, math.Min(max, v))*10) / 10 }

// root loads a top-level internal panel comment of the project.
func (m *Module) root(ctx context.Context, projectID, id string) (commentRow, error) {
	var r commentRow
	if !db.ValidID(id) {
		return r, errNotFound
	}
	err := m.d.DB.Get(ctx, &r, `SELECT `+commentCols+` FROM comments WHERE id = ? AND project_id = ? AND visibility = 'internal'
		AND deleted_at IS NULL AND target_kind = 'panel' AND parent_id IS NULL`, id, projectID)
	if db.IsNotFound(err) {
		return r, errNotFound
	}
	return r, err
}

// panelPage returns the page that holds the comment's panel now.
func (m *Module) panelPage(ctx context.Context, projectID string, r commentRow) (pagedoc.Page, string, error) {
	var pageID string
	if err := m.d.DB.Get(ctx, &pageID, `SELECT page_id FROM panels WHERE id = ? AND project_id = ?`, r.TargetID, projectID); err != nil {
		if db.IsNotFound(err) {
			return pagedoc.Page{}, "", errNotFound
		}
		return pagedoc.Page{}, "", err
	}
	p, err := m.pageIn(ctx, projectID, pageID)
	if err != nil {
		return p, "", err
	}
	label := r.anchor().Label
	for i, pn := range p.Panels {
		if pn.ID == r.TargetID {
			label = strconv.Itoa(p.Number) + "." + strconv.Itoa(i+1)
		}
	}
	return p, label, nil
}

func (m *Module) reply(ctx context.Context, c caller, projectID, commentID, body string) (string, error) {
	if err := m.read(ctx, c, projectID); err != nil {
		return "", err
	}
	root, err := m.root(ctx, projectID, commentID)
	if err != nil {
		return "", err
	}
	if !c.canWrite() {
		return "", errForbidden
	}
	body, err = cleanBody(body)
	if err != nil {
		return "", err
	}
	p, label, err := m.panelPage(ctx, projectID, root)
	if err != nil {
		return "", err
	}
	a := anchor{PageID: p.ID, PanelID: root.TargetID, Label: label}
	if c.token != nil {
		a.TokenID = c.token.ID
	}
	id := db.NewID()
	err = m.d.DB.WithTx(ctx, func(tx *db.Tx) error {
		if err := m.studio.AuthorizeTx(ctx, tx, c.actor, projectID, studio.ActRead); err != nil {
			return errNotFound
		}
		var n int
		if err := tx.Get(ctx, &n, `SELECT COUNT(*) FROM comments WHERE parent_id = ?`, root.ID); err != nil {
			return err
		}
		if n >= maxRepliesPerComment {
			return fieldError{"body", "collab.error.too_many"}
		}
		now := db.Now()
		_, err := tx.Exec(ctx, `INSERT INTO comments (id, project_id, target_kind, target_id, anchor, visibility, author_id, parent_id, body, status, created_at, updated_at)
			VALUES (?, ?, 'panel', ?, ?, 'internal', ?, ?, ?, 'open', ?, ?)`, id, projectID, root.TargetID, db.MustJSON(a), c.user.ID, root.ID, body, now, now)
		return err
	})
	if err != nil {
		return "", err
	}
	m.changed(p.ID, root.ID, "reply")
	m.notifyMembers(ctx, c, projectID, p, label, recipients(root), "collab.notify.reply", "")
	return id, nil
}

func recipients(r commentRow) []string {
	var out []string
	if r.AuthorID.Valid {
		out = append(out, r.AuthorID.String)
	}
	if r.AssigneeID.Valid && (!r.AuthorID.Valid || r.AssigneeID.String != r.AuthorID.String) {
		out = append(out, r.AssigneeID.String)
	}
	return out
}

func validStatus(s string) bool {
	switch s {
	case StatusOpen, StatusNeedsChanges, StatusDone, StatusApproved:
		return true
	}
	return false
}

func (m *Module) setStatus(ctx context.Context, c caller, projectID, commentID, status string) error {
	if err := m.read(ctx, c, projectID); err != nil {
		return err
	}
	root, err := m.root(ctx, projectID, commentID)
	if err != nil {
		return err
	}
	if !c.session {
		return errSession
	}
	if !validStatus(status) {
		return fieldError{"status", "collab.error.status"}
	}
	var changedTo string
	var page pagedoc.Page
	var label string
	err = m.d.DB.WithTx(ctx, func(tx *db.Tx) error {
		if err := m.studio.AuthorizeTx(ctx, tx, c.actor, projectID, studio.ActRead); err != nil {
			return errNotFound
		}
		var cur commentRow
		if err := tx.Get(ctx, &cur, `SELECT `+commentCols+` FROM comments WHERE id = ?`+forUpdate(tx), root.ID); err != nil {
			return err
		}
		if cur.Status == status {
			return nil
		}
		var role string
		if err := tx.Get(ctx, &role, `SELECT role FROM project_members WHERE project_id = ? AND user_id = ?`, projectID, c.user.ID); err != nil && !db.IsNotFound(err) {
			return err
		}
		allowed := false
		for _, s := range next(c, role, cur) {
			allowed = allowed || s == status
		}
		if !allowed {
			return errForbidden
		}
		now := db.Now()
		var err error
		if status == StatusApproved {
			_, err = tx.Exec(ctx, `UPDATE comments SET status = ?, resolved_by = ?, resolved_at = ?, updated_at = ? WHERE id = ?`, status, c.user.ID, now, now, cur.ID)
		} else {
			_, err = tx.Exec(ctx, `UPDATE comments SET status = ?, resolved_by = NULL, resolved_at = NULL, updated_at = ? WHERE id = ?`, status, now, cur.ID)
		}
		changedTo = status
		return err
	})
	if err != nil || changedTo == "" {
		return err
	}
	page, label, err = m.panelPage(ctx, projectID, root)
	if err != nil {
		return nil // the panel was removed meanwhile; nothing to refresh
	}
	m.changed(page.ID, root.ID, "status")
	m.notifyMembers(ctx, c, projectID, page, label, recipients(root), "collab.notify.status", changedTo)
	return nil
}

func forUpdate(q db.Querier) string {
	if q.Dialect() == db.Postgres {
		return " FOR UPDATE"
	}
	return ""
}

// approvePage records the reviewer's approval of the page at version.
func (m *Module) approvePage(ctx context.Context, c caller, projectID, pageID string, version int) error {
	if err := m.read(ctx, c, projectID); err != nil {
		return err
	}
	p, err := m.pageIn(ctx, projectID, pageID)
	if err != nil {
		return err
	}
	if !c.session {
		return errSession
	}
	role, err := m.roleOf(ctx, projectID, c.user.ID)
	if err != nil {
		return err
	}
	if !c.reviewer(role) {
		return errForbidden
	}
	if version < 1 {
		return fieldError{"version", "collab.error.version"}
	}
	approved := false
	err = m.d.DB.WithTx(ctx, func(tx *db.Tx) error {
		if err := m.studio.AuthorizeTx(ctx, tx, c.actor, projectID, studio.ActRead); err != nil {
			return errNotFound
		}
		var current int
		if err := tx.Get(ctx, &current, `SELECT version FROM pages WHERE id = ?`+forUpdate(tx), p.ID); err != nil {
			return err
		}
		if current != version {
			return errConflict
		}
		var latest []commentRow
		if err := tx.Select(ctx, &latest, `SELECT `+commentCols+` FROM comments WHERE project_id = ? AND visibility = 'internal' AND deleted_at IS NULL
			AND target_kind = 'page' AND target_id = ? AND status = 'approved' AND parent_id IS NULL ORDER BY created_at DESC, id DESC LIMIT 1`, projectID, p.ID); err != nil {
			return err
		}
		if len(latest) == 1 && latest[0].anchor().PageVersion == version {
			return nil
		}
		now := db.Now()
		a := anchor{PageID: p.ID, PageVersion: version}
		if _, err := tx.Exec(ctx, `INSERT INTO comments (id, project_id, target_kind, target_id, anchor, visibility, author_id, body, status, resolved_by, resolved_at, created_at, updated_at)
			VALUES (?, ?, 'page', ?, ?, 'internal', ?, '', 'approved', ?, ?, ?, ?)`, db.NewID(), projectID, p.ID, db.MustJSON(a), c.user.ID, c.user.ID, now, now, now); err != nil {
			return err
		}
		approved = true
		return nil
	})
	if err != nil {
		return err
	}
	if approved {
		m.d.Auth.Audit(ctx, auth.AuditEntry{Action: "page.approve", TargetKind: "page", TargetID: p.ID, ProjectID: projectID, Data: map[string]any{"version": version}})
		m.changed(p.ID, "", "approval")
	}
	return nil
}

// ---- members -------------------------------------------------------------------

// assignable roles: ownership is never given or taken through this API.
func assignable(role string) bool {
	switch role {
	case studio.RoleWriter, studio.RoleArtist, studio.RoleLetterer, studio.RoleEditor, studio.RoleViewer:
		return true
	}
	return false
}

// manage checks that a person in a browser session may manage the project's members.
func (m *Module) manage(ctx context.Context, c caller, projectID string) error {
	if err := m.read(ctx, c, projectID); err != nil {
		return err
	}
	if !c.session {
		return errSession
	}
	if err := m.studio.Authorize(ctx, c.actor, projectID, studio.ActManage); err != nil {
		if errors.Is(err, studio.ErrForbidden) {
			return errForbidden
		}
		return err
	}
	return nil
}

// MemberInput adds a member (Username) or changes one (role and scope).
type MemberInput struct {
	Username string             `json:"username,omitempty"`
	Role     string             `json:"role"`
	Scope    studio.MemberScope `json:"scope"`
}

func checkMember(in MemberInput) error {
	if !assignable(in.Role) {
		return fieldError{"role", "collab.error.role"}
	}
	if err := in.Scope.Validate(in.Role); err != nil {
		return fieldError{"scope", "collab.error.scope"}
	}
	return nil
}

func (m *Module) addMember(ctx context.Context, c caller, projectID string, in MemberInput) (Member, error) {
	if err := m.manage(ctx, c, projectID); err != nil {
		return Member{}, err
	}
	if err := checkMember(in); err != nil {
		return Member{}, err
	}
	u, err := m.d.Auth.UserByUsername(ctx, strings.TrimSpace(in.Username))
	if err != nil || u == nil || u.Status != auth.StatusActive {
		return Member{}, fieldError{"username", "collab.error.user"}
	}
	err = m.d.DB.WithTx(ctx, func(tx *db.Tx) error {
		if err := m.studio.AuthorizeTx(ctx, tx, c.actor, projectID, studio.ActManage); err != nil {
			return errForbidden
		}
		var n int
		if err := tx.Get(ctx, &n, `SELECT COUNT(*) FROM project_members WHERE project_id = ? AND user_id = ?`, projectID, u.ID); err != nil {
			return err
		}
		if n > 0 {
			return fieldError{"username", "collab.error.member_exists"}
		}
		_, err := tx.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role, scope, invited_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			projectID, u.ID, in.Role, in.Scope.Encode(), c.user.ID, db.Now())
		return err
	})
	if err != nil {
		return Member{}, err
	}
	m.d.Auth.Audit(ctx, auth.AuditEntry{Action: "project.member_add", TargetKind: "user", TargetID: u.ID, ProjectID: projectID, Data: map[string]any{"role": in.Role, "scope": in.Scope}})
	return Member{UserID: u.ID, Name: u.Name(), Username: u.Username, Role: in.Role, Scope: in.Scope}, nil
}

func (m *Module) updateMember(ctx context.Context, c caller, projectID, userID string, in MemberInput) (Member, error) {
	if err := m.manage(ctx, c, projectID); err != nil {
		return Member{}, err
	}
	if err := checkMember(in); err != nil {
		return Member{}, err
	}
	err := m.d.DB.WithTx(ctx, func(tx *db.Tx) error {
		if err := m.studio.AuthorizeTx(ctx, tx, c.actor, projectID, studio.ActManage); err != nil {
			return errForbidden
		}
		var role string
		if err := tx.Get(ctx, &role, `SELECT role FROM project_members WHERE project_id = ? AND user_id = ?`+forUpdate(tx), projectID, userID); err != nil {
			if db.IsNotFound(err) {
				return errNotFound
			}
			return err
		}
		if role == studio.RoleOwner {
			return errForbidden
		}
		_, err := tx.Exec(ctx, `UPDATE project_members SET role = ?, scope = ? WHERE project_id = ? AND user_id = ?`, in.Role, in.Scope.Encode(), projectID, userID)
		return err
	})
	if err != nil {
		return Member{}, err
	}
	m.d.Auth.Audit(ctx, auth.AuditEntry{Action: "project.member_role", TargetKind: "user", TargetID: userID, ProjectID: projectID, Data: map[string]any{"role": in.Role, "scope": in.Scope}})
	list, err := m.members(ctx, projectID)
	if err != nil {
		return Member{}, err
	}
	for _, mb := range list {
		if mb.UserID == userID {
			return mb, nil
		}
	}
	return Member{}, errNotFound
}

func (m *Module) removeMember(ctx context.Context, c caller, projectID, userID string) error {
	if err := m.manage(ctx, c, projectID); err != nil {
		return err
	}
	err := m.d.DB.WithTx(ctx, func(tx *db.Tx) error {
		if err := m.studio.AuthorizeTx(ctx, tx, c.actor, projectID, studio.ActManage); err != nil {
			return errForbidden
		}
		var role string
		if err := tx.Get(ctx, &role, `SELECT role FROM project_members WHERE project_id = ? AND user_id = ?`+forUpdate(tx), projectID, userID); err != nil {
			if db.IsNotFound(err) {
				return errNotFound
			}
			return err
		}
		if role == studio.RoleOwner {
			return errForbidden
		}
		_, err := tx.Exec(ctx, `DELETE FROM project_members WHERE project_id = ? AND user_id = ?`, projectID, userID)
		return err
	})
	if err != nil {
		return err
	}
	m.d.Auth.Audit(ctx, auth.AuditEntry{Action: "project.member_remove", TargetKind: "user", TargetID: userID, ProjectID: projectID})
	return nil
}
