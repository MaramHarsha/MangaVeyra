package publish

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// Credit roles of series_creators. The owner is always credited first.
const RoleOwner = "owner"

// CreditRoles are the roles a project member may be credited with.
var CreditRoles = []string{"writer", "artist", "letterer", "editor", "translator"}

const (
	maxCreditName = 60
	maxCreators   = 20
)

type creatorRow struct {
	SeriesID   string `db:"series_id"`
	UserID     string `db:"user_id"`
	Role       string `db:"role"`
	CreditName string `db:"credit_name"`
}

// attachCreators loads the credits of every series in one query (credit
// order: order_idx, then time added), always with the owner first, even when
// the owner has no series_creators row.
func attachCreators(ctx context.Context, q db.Querier, list []Series) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]string, len(list))
	for i, s := range list {
		ids[i] = s.ID
	}
	in := inStrings("series_id", ids)
	var rows []creatorRow
	if err := q.Select(ctx, &rows, `SELECT series_id, user_id, role, credit_name FROM series_creators WHERE `+in.sql+`
		ORDER BY series_id, order_idx, created_at, user_id, role`, in.args...); err != nil {
		return fmt.Errorf("publish: series creators: %w", err)
	}
	by := map[string][]creatorRow{}
	for _, r := range rows {
		by[r.SeriesID] = append(by[r.SeriesID], r)
	}
	for i := range list {
		list[i].Creators = creatorsFor(list[i].OwnerID, by[list[i].ID])
	}
	return nil
}

func creatorsFor(owner string, rows []creatorRow) []Creator {
	out := []Creator{{UserID: owner, Role: RoleOwner}}
	for _, r := range rows {
		c := Creator{UserID: r.UserID, Role: r.Role, CreditName: strings.TrimSpace(r.CreditName)}
		if c.UserID == owner && c.Role == RoleOwner {
			out[0].CreditName = c.CreditName
			continue
		}
		out = append(out, c)
	}
	return out
}

// member is a project member who can be credited on the series.
type member struct {
	UserID      string `db:"user_id"`
	ProjectRole string `db:"role"`
	Username    string `db:"username"`
	DisplayName string `db:"display_name"`
}

func (m member) name() string {
	if strings.TrimSpace(m.DisplayName) != "" {
		return m.DisplayName
	}
	return m.Username
}

// projectMembers lists the project's members, the owner first.
func projectMembers(ctx context.Context, q db.Querier, projectID, ownerID string) ([]member, error) {
	var rows []member
	if err := q.Select(ctx, &rows, `SELECT pm.user_id, pm.role, u.username, u.display_name FROM project_members pm
		JOIN users u ON u.id = pm.user_id WHERE pm.project_id = ? ORDER BY pm.created_at, pm.user_id`, projectID); err != nil {
		return nil, fmt.Errorf("publish: project members: %w", err)
	}
	slices.SortStableFunc(rows, func(a, b member) int {
		switch {
		case a.UserID == ownerID && b.UserID != ownerID:
			return -1
		case b.UserID == ownerID && a.UserID != ownerID:
			return 1
		}
		return 0
	})
	return rows, nil
}

// normalizeCredits trims credits, drops uncredited members and repeats.
func normalizeCredits(in []Creator) []Creator {
	out := []Creator{}
	seen := map[string]bool{}
	for _, c := range in {
		c.UserID, c.Role, c.CreditName = strings.TrimSpace(c.UserID), strings.TrimSpace(c.Role), strings.Join(strings.Fields(c.CreditName), " ")
		key := c.UserID + "\x00" + c.Role
		if c.UserID == "" || c.Role == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

func validCredit(c Creator) bool {
	return slices.Contains(CreditRoles, c.Role) && utf8.RuneCountInString(c.CreditName) <= maxCreditName && utf8.ValidString(c.CreditName)
}

// writeCreditsTx replaces the series credits: the owner (order 0) and each
// credited project member in form order. Non-members are refused.
func writeCreditsTx(ctx context.Context, tx *db.Tx, seriesID, projectID, ownerID, ownerCredit string, credits []Creator, now any) error {
	members, err := projectMembers(ctx, tx, projectID, ownerID)
	if err != nil {
		return err
	}
	isMember := map[string]bool{ownerID: true}
	for _, m := range members {
		isMember[m.UserID] = true
	}
	for _, c := range credits {
		if !isMember[c.UserID] || !validCredit(c) {
			return errBadCredit
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM series_creators WHERE series_id = ?`, seriesID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO series_creators (series_id, user_id, role, credit_name, order_idx, created_at)
		VALUES (?, ?, ?, ?, 0, ?)`, seriesID, ownerID, RoleOwner, ownerCredit, now); err != nil {
		return err
	}
	for i, c := range credits {
		if _, err := tx.Exec(ctx, `INSERT INTO series_creators (series_id, user_id, role, credit_name, order_idx, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, seriesID, c.UserID, c.Role, c.CreditName, i+1, now); err != nil {
			return err
		}
	}
	return nil
}

// errBadCredit marks a credit for a non-member, an unknown role or a long name.
var errBadCredit = fmt.Errorf("%w: credit", ErrInvalid)
