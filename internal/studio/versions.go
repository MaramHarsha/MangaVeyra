package studio

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

// Versions lists a page's versions, newest first. branchID "" lists the main
// line; "*" lists every branch.
func (s *Service) Versions(ctx context.Context, pageID, branchID string, limit int) ([]Version, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	cond, args := " AND v.branch_id IS NULL", []any{pageID}
	switch branchID {
	case "*":
		cond = ""
	case "":
	default:
		cond, args = " AND v.branch_id = ?", append(args, branchID)
	}
	var out []Version
	err := s.db.Select(ctx, &out, `SELECT `+versionCols+` FROM versions v LEFT JOIN users u ON u.id = v.actor_id
		WHERE v.page_id = ?`+cond+` ORDER BY v.created_at DESC, v.id DESC LIMIT `+itoa(limit), args...)
	return out, err
}

// Version returns a version (without its snapshot).
func (s *Service) Version(ctx context.Context, id string) (*Version, error) {
	var v Version
	if err := s.db.Get(ctx, &v, `SELECT `+versionCols+` FROM versions v LEFT JOIN users u ON u.id = v.actor_id WHERE v.id = ?`, id); err != nil {
		return nil, notFound(err)
	}
	return &v, nil
}

// VersionDoc returns the page document stored in a version.
func (s *Service) VersionDoc(ctx context.Context, versionID string) (*PageDoc, error) {
	var raw string
	if err := s.db.Get(ctx, &raw, `SELECT snapshot FROM versions WHERE id = ?`, versionID); err != nil {
		return nil, notFound(err)
	}
	var d PageDoc
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// RestoreVersion makes a version the current state of its page (recorded as a new version).
func (s *Service) RestoreVersion(ctx context.Context, actor Actor, versionID string) (*PageDoc, *Version, error) {
	v, err := s.Version(ctx, versionID)
	if err != nil {
		return nil, nil, err
	}
	doc, err := s.VersionDoc(ctx, versionID)
	if err != nil {
		return nil, nil, err
	}
	return s.saveSnapshot(ctx, actor, v.PageID, doc, "restore", i18n.T(ctx, "studio.version.restored", "n", v.Number))
}

// saveSnapshot writes a stored snapshot back onto the live page (keeping the page's identity and lock state).
func (s *Service) saveSnapshot(ctx context.Context, actor Actor, pageID string, doc *PageDoc, action, summary string) (*PageDoc, *Version, error) {
	cur, err := s.Page(ctx, pageID)
	if err != nil {
		return nil, nil, err
	}
	doc.Page.ID = pageID
	doc.Page.Locked, doc.Page.LockedBy, doc.Page.LockedAt = cur.Locked, cur.LockedBy, cur.LockedAt
	// Lock state is not history: panels and layers locked now stay locked.
	panels, err := s.Panels(ctx, pageID)
	if err != nil {
		return nil, nil, err
	}
	locked := map[string]Panel{}
	for _, p := range panels {
		if p.Locked {
			locked[p.ID] = p
		}
	}
	for i := range doc.Panels {
		if p, ok := locked[doc.Panels[i].ID]; ok {
			doc.Panels[i].Locked, doc.Panels[i].LockedBy, doc.Panels[i].LockedAt = true, p.LockedBy, p.LockedAt
		}
	}
	return s.SavePageDoc(ctx, actor, doc, SaveOptions{Action: action, Summary: summary})
}

// UndoLast reverts the most recent change on the page's current branch (it
// restores the previous version and records the undo as a new version).
func (s *Service) UndoLast(ctx context.Context, actor Actor, pageID string) (*PageDoc, *Version, error) {
	p, err := s.Page(ctx, pageID)
	if err != nil {
		return nil, nil, err
	}
	list, err := s.Versions(ctx, pageID, p.BranchID, 2)
	if err != nil {
		return nil, nil, err
	}
	if len(list) == 0 || list[0].ParentID == "" {
		return nil, nil, core.ErrConflict(i18n.T(ctx, "studio.version.nothing_to_undo"))
	}
	doc, err := s.VersionDoc(ctx, list[0].ParentID)
	if err != nil {
		return nil, nil, err
	}
	return s.saveSnapshot(ctx, actor, pageID, doc, "undo", i18n.T(ctx, "studio.version.undone", "summary", list[0].Summary))
}

// Branches lists a page's named branches.
func (s *Service) Branches(ctx context.Context, pageID string) ([]Branch, error) {
	var out []Branch
	err := s.db.Select(ctx, &out, `SELECT `+branchCols+` FROM branches WHERE page_id = ? ORDER BY created_at`, pageID)
	return out, err
}

// Branch returns a branch by id.
func (s *Service) Branch(ctx context.Context, id string) (*Branch, error) {
	var b Branch
	if err := s.db.Get(ctx, &b, `SELECT `+branchCols+` FROM branches WHERE id = ?`, id); err != nil {
		return nil, notFound(err)
	}
	return &b, nil
}

// CreateBranch starts a named branch from the page's current state and
// switches the page onto it.
func (s *Service) CreateBranch(ctx context.Context, actor Actor, pageID, name string) (*Branch, error) {
	p, err := s.Page(ctx, pageID)
	if err != nil {
		return nil, err
	}
	a, err := s.Access(ctx, actor, p.ProjectID)
	if err != nil {
		return nil, err
	}
	if !a.Can(EditPages) && !a.Can(EditArt) {
		return nil, core.ErrForbidden(i18n.T(ctx, "studio.error.role"))
	}
	if name = clip(name, 60); name == "" || strings.EqualFold(name, "main") {
		return nil, core.ErrValidation(map[string]string{"name": i18n.T(ctx, "studio.form.invalid")})
	}
	b := &Branch{ID: db.NewID(), PageID: pageID, Name: name, ParentBranchID: p.BranchID, CreatedBy: actor.UserID, CreatedAt: s.now()}
	err = s.db.WithTx(ctx, func(tx *db.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO branches (id, page_id, name, parent_branch_id, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			b.ID, pageID, b.Name, db.NullString(b.ParentBranchID), db.NullString(actor.UserID), b.CreatedAt); err != nil {
			if db.IsUniqueViolation(err) {
				return core.ErrValidation(map[string]string{"name": i18n.T(ctx, "studio.version.branch_exists")})
			}
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE pages SET branch_id = ? WHERE id = ?`, b.ID, pageID); err != nil {
			return err
		}
		v, err := s.recordVersion(ctx, tx, pageID, actor, SaveOptions{Action: "branch", Summary: i18n.T(ctx, "studio.version.branched", "name", b.Name)})
		if err != nil {
			return err
		}
		b.HeadVersionID = v.ID
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.Publish(ctx, p.ProjectID, "studio.page.updated", Event{Kind: "branch", ID: b.ID, PageID: pageID})
	return b, nil
}

// branchHead returns the latest version of a branch ("" = main line).
func (s *Service) branchHead(ctx context.Context, pageID, branchID string) (string, error) {
	var id string
	var err error
	if branchID == "" {
		err = s.db.Get(ctx, &id, `SELECT id FROM versions WHERE page_id = ? AND branch_id IS NULL ORDER BY created_at DESC, id DESC LIMIT 1`, pageID)
	} else {
		err = s.db.Get(ctx, &id, `SELECT COALESCE(head_version_id, '') FROM branches WHERE id = ? AND page_id = ?`, branchID, pageID)
	}
	return id, notFound(err)
}

// SwitchBranch loads the head of another branch ("" = main) onto the page.
func (s *Service) SwitchBranch(ctx context.Context, actor Actor, pageID, branchID string) (*PageDoc, error) {
	p, err := s.Page(ctx, pageID)
	if err != nil {
		return nil, err
	}
	if _, err := s.Access(ctx, actor, p.ProjectID); err != nil {
		return nil, err
	}
	if p.BranchID == branchID {
		return s.LoadPageDoc(ctx, pageID)
	}
	head, err := s.branchHead(ctx, pageID, branchID)
	if err != nil {
		return nil, err
	}
	var doc *PageDoc
	if head != "" {
		if doc, err = s.VersionDoc(ctx, head); err != nil {
			return nil, err
		}
	}
	if _, err := s.db.Exec(ctx, `UPDATE pages SET branch_id = ? WHERE id = ?`, db.NullString(branchID), pageID); err != nil {
		return nil, err
	}
	if doc == nil {
		return s.LoadPageDoc(ctx, pageID)
	}
	name := "main"
	if branchID != "" {
		if b, err := s.Branch(ctx, branchID); err == nil {
			name = b.Name
		}
	}
	saved, _, err := s.saveSnapshot(ctx, actor, pageID, doc, "switch", i18n.T(ctx, "studio.version.switched", "name", name))
	return saved, err
}

// MergeBranch copies the head of branchID onto the target branch (into; "" =
// main), switches the page to the target and marks the source merged.
func (s *Service) MergeBranch(ctx context.Context, actor Actor, pageID, branchID, into string) (*PageDoc, error) {
	b, err := s.Branch(ctx, branchID)
	if err != nil || b.PageID != pageID {
		return nil, core.ErrNotFound()
	}
	if b.HeadVersionID == "" {
		return nil, core.ErrConflict(i18n.T(ctx, "studio.version.empty_branch"))
	}
	doc, err := s.VersionDoc(ctx, b.HeadVersionID)
	if err != nil {
		return nil, err
	}
	if _, err := s.SwitchBranch(ctx, actor, pageID, into); err != nil {
		return nil, err
	}
	saved, _, err := s.saveSnapshot(ctx, actor, pageID, doc, "merge", i18n.T(ctx, "studio.version.merged", "name", b.Name))
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(ctx, `UPDATE branches SET merged_into_id = ?, merged_at = ? WHERE id = ?`, db.NullString(into), s.now(), branchID); err != nil {
		return nil, err
	}
	return saved, nil
}

// DocDiff summarises what changed between two page documents.
type DocDiff struct {
	PanelsAdded    []string `json:"panels_added"`
	PanelsRemoved  []string `json:"panels_removed"`
	PanelsChanged  []string `json:"panels_changed"`
	BubblesAdded   []string `json:"bubbles_added"`
	BubblesRemoved []string `json:"bubbles_removed"`
	BubblesChanged []string `json:"bubbles_changed"`
	PageChanged    bool     `json:"page_changed"`
}

// Empty reports whether nothing changed.
func (d DocDiff) Empty() bool {
	return !d.PageChanged && len(d.PanelsAdded)+len(d.PanelsRemoved)+len(d.PanelsChanged)+len(d.BubblesAdded)+len(d.BubblesRemoved)+len(d.BubblesChanged) == 0
}

// DiffDocs compares two page documents panel by panel and bubble by bubble.
func DiffDocs(a, b *PageDoc) DocDiff {
	d := DocDiff{PanelsAdded: []string{}, PanelsRemoved: []string{}, PanelsChanged: []string{}, BubblesAdded: []string{},
		BubblesRemoved: []string{}, BubblesChanged: []string{}}
	pa, pb := a.Page, b.Page
	pa.Version, pb.Version, pa.BranchID, pb.BranchID = 0, 0, "", ""
	d.PageChanged = fingerprint(pa) != fingerprint(pb)
	ap := map[string]string{}
	for _, p := range a.Panels {
		ap[p.ID] = fingerprint(p)
	}
	for _, p := range b.Panels {
		if fp, ok := ap[p.ID]; !ok {
			d.PanelsAdded = append(d.PanelsAdded, p.ID)
		} else if fp != fingerprint(p) {
			d.PanelsChanged = append(d.PanelsChanged, p.ID)
		}
		delete(ap, p.ID)
	}
	for _, p := range a.Panels {
		if _, ok := ap[p.ID]; ok {
			d.PanelsRemoved = append(d.PanelsRemoved, p.ID)
		}
	}
	ab := map[string]string{}
	for _, x := range a.Bubbles {
		ab[x.ID] = fingerprint(x)
	}
	for _, x := range b.Bubbles {
		if fp, ok := ab[x.ID]; !ok {
			d.BubblesAdded = append(d.BubblesAdded, x.ID)
		} else if fp != fingerprint(x) {
			d.BubblesChanged = append(d.BubblesChanged, x.ID)
		}
		delete(ab, x.ID)
	}
	for _, x := range a.Bubbles {
		if _, ok := ab[x.ID]; ok {
			d.BubblesRemoved = append(d.BubblesRemoved, x.ID)
		}
	}
	return d
}

// CompareVersions loads two versions and their diff (a → b).
func (s *Service) CompareVersions(ctx context.Context, aID, bID string) (*PageDoc, *PageDoc, DocDiff, error) {
	a, err := s.VersionDoc(ctx, aID)
	if err != nil {
		return nil, nil, DocDiff{}, err
	}
	b, err := s.VersionDoc(ctx, bID)
	if err != nil {
		return nil, nil, DocDiff{}, err
	}
	if a.Page.ID != b.Page.ID {
		return nil, nil, DocDiff{}, core.ErrBadRequest("versions belong to different pages")
	}
	return a, b, DiffDocs(a, b), nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
