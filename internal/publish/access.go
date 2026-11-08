package publish

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// Access rules (ADR 0012 §1). Every reader-facing read builds its WHERE clause
// from one access value so single reads, lists, CanRead and signed page URLs
// can never disagree.
//
// A viewer is signed in only when Viewer.UserID names an active account
// (studio.ActorFromUser); unknown, suspended and pending accounts read as
// anonymous visitors. Viewer.Admin grants nothing beyond a signed-in member:
// admins read members-only series like any member, but private series stay
// limited to project members and unpublished, scheduled or future chapters
// stay hidden from everyone.
//
//	series visibility | anonymous (public mode) | anonymous (personal, private) | signed-in member | project member
//	public            | yes, rating all/teen    | no                            | yes              | yes
//	unlisted          | yes, rating all/teen    | no                            | yes              | yes
//	members           | no                      | no                            | yes              | yes
//	private           | no                      | no                            | no               | yes
//
// A series is readable once published: status is not draft and published_at
// has passed. Project members may preview their own series in any state by ID
// or slug; lists only ever contain published series. A chapter publication is
// readable when its series is, its status is published, its published_at has
// passed and its own visibility passes the same table (so the effective
// visibility is the stricter of the two). Listed lists exclude unlisted and
// private series.
type access struct {
	// userID is the active signed-in viewer; "" reads as an anonymous visitor.
	userID string
	// anonOK is true when the instance mode lets anonymous visitors read.
	anonOK bool
	// now is the moment schedules are compared against.
	now time.Time
}

func (s *service) resolve(ctx context.Context, v Viewer) (access, error) {
	inst, err := s.settings.Instance(ctx)
	if err != nil {
		return access{}, fmt.Errorf("publish: instance settings: %w", err)
	}
	a := access{anonOK: inst.ExplorePublic(), now: s.now().UTC()}
	id := strings.TrimSpace(v.UserID)
	if id == "" {
		return a, nil
	}
	actor, err := s.studio.ActorFromUser(ctx, id)
	switch {
	case err == nil:
		a.userID = actor.UserID
	case errors.Is(err, studio.ErrNotFound), errors.Is(err, studio.ErrForbidden):
		// Unknown, suspended or pending accounts read as anonymous visitors.
	default:
		return access{}, fmt.Errorf("publish: resolve viewer: %w", err)
	}
	return a, nil
}

// clause is a SQL fragment with its arguments, in order.
type clause struct {
	sql  string
	args []any
}

// and joins clauses with AND (true when empty); or joins them with OR (false
// when empty). Each part is parenthesised so precedence never leaks between
// fragments. Empty fragments are skipped.
func and(parts ...clause) clause { return join(" AND ", "1 = 1", parts) }

func or(parts ...clause) clause { return join(" OR ", "1 = 0", parts) }

func join(op, empty string, parts []clause) clause {
	var out clause
	sqls := make([]string, 0, len(parts))
	for _, p := range parts {
		if p.sql == "" {
			continue
		}
		sqls = append(sqls, "("+p.sql+")")
		out.args = append(out.args, p.args...)
	}
	if len(sqls) == 0 {
		return clause{sql: empty}
	}
	out.sql = "(" + strings.Join(sqls, op) + ")"
	return out
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

func inStrings(col string, values []string) clause {
	if len(values) == 0 {
		return clause{sql: "1 = 0"}
	}
	args := make([]any, len(values))
	for i, v := range values {
		args[i] = v
	}
	return clause{sql: col + " IN (" + placeholders(len(values)) + ")", args: args}
}

// signedIn reports whether the viewer is an active account.
func (a access) signedIn() bool { return a.userID != "" }

// member is true when the viewer belongs to the series' project.
func (a access) member() clause {
	if !a.signedIn() {
		return clause{sql: "1 = 0"}
	}
	return clause{sql: `EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = s.project_id AND pm.user_id = ?)`,
		args: []any{a.userID}}
}

// visibilities are the visibilities the viewer may read without project
// membership; listed drops unlisted.
func (a access) visibilities(listed bool) []string {
	var out []string
	switch {
	case a.signedIn():
		out = []string{VisibilityPublic, VisibilityUnlisted, VisibilityMembers}
	case a.anonOK:
		out = []string{VisibilityPublic, VisibilityUnlisted}
	}
	if listed {
		kept := out[:0:0]
		for _, v := range out {
			if v != VisibilityUnlisted {
				kept = append(kept, v)
			}
		}
		out = kept
	}
	return out
}

// visibility restricts col (s.visibility or p.visibility) to what the viewer
// may read. Private content needs project membership and is never listed.
func (a access) visibility(col string, listed bool) clause {
	parts := []clause{inStrings(col, a.visibilities(listed))}
	if a.signedIn() && !listed {
		parts = append(parts, and(clause{sql: col + " = ?", args: []any{VisibilityPrivate}}, a.member()))
	}
	return or(parts...)
}

// rating keeps mature and adult series away from anonymous visitors.
func (a access) rating() clause {
	if a.signedIn() {
		return clause{}
	}
	return inStrings("s.age_rating", []string{RatingAll, RatingTeen})
}

// seriesPublished is true once the series left draft and published_at passed.
func (a access) seriesPublished() clause {
	return clause{sql: "s.status <> ? AND s.published_at IS NOT NULL AND s.published_at <= ?",
		args: []any{SeriesDraft, a.now}}
}

// seriesListed is the predicate for lists: published series the viewer may read.
func (a access) seriesListed(listed bool) clause {
	return and(a.seriesPublished(), a.visibility("s.visibility", listed), a.rating())
}

// seriesReadable is the predicate for one series (alias s): a published series
// the viewer may read, or any series of a project the viewer belongs to.
func (a access) seriesReadable() clause {
	return or(a.member(), a.seriesListed(false))
}

// publicationReadable is the predicate for one publication (alias p) of a
// readable series (alias s).
func (a access) publicationReadable() clause {
	return and(
		clause{sql: "p.status = ? AND p.published_at IS NOT NULL AND p.published_at <= ?",
			args: []any{PublicationPublished, a.now}},
		a.visibility("p.visibility", false),
	)
}
