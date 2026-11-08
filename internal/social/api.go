package social

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
)

// commentPage is the list response: newest first, next_cursor while more exist.
type commentPage struct {
	Items      []Comment `json:"items"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

func queryInt(r *http.Request, name string) (int, error) {
	v := strings.TrimSpace(r.URL.Query().Get(name))
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > 100000 {
		return 0, invalid(name, "social.error.anchor")
	}
	return n, nil
}

// GET /api/v1/social/comments?target_kind=&target_id=&series=[&cursor=&limit=&page=&panel=]
func (m *Module) apiListComments(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	q := r.URL.Query()
	page, err := queryInt(r, "page")
	if err != nil {
		return apiError(ctx, err)
	}
	panel, err := queryInt(r, "panel")
	if err != nil {
		return apiError(ctx, err)
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	items, next, err := m.svc.listComments(ctx, viewer(r), CommentQuery{SeriesID: q.Get("series"),
		TargetKind: q.Get("target_kind"), TargetID: q.Get("target_id"), Page: page, Panel: panel,
		Cursor: q.Get("cursor"), Limit: limit})
	if err != nil {
		return apiError(ctx, err)
	}
	core.JSON(w, http.StatusOK, commentPage{Items: items, NextCursor: next})
	return nil
}

// POST /api/v1/social/comments {series_id, target_kind, target_id, anchor, body, parent_id?}
func (m *Module) apiCreateComment(w http.ResponseWriter, r *http.Request) error {
	var in NewComment
	if err := core.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	if err := allow(m.commentL, r); err != nil {
		return err
	}
	c, err := m.svc.createComment(r.Context(), viewer(r), in)
	if err != nil {
		return apiError(r.Context(), err)
	}
	core.JSON(w, http.StatusCreated, c)
	return nil
}

// PATCH /api/v1/social/comments/{id} {body}
func (m *Module) apiEditComment(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Body string `json:"body"`
	}
	if err := core.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	if err := allow(m.editL, r); err != nil {
		return err
	}
	c, err := m.svc.editComment(r.Context(), viewer(r), r.PathValue("id"), in.Body)
	if err != nil {
		return apiError(r.Context(), err)
	}
	core.JSON(w, http.StatusOK, c)
	return nil
}

// DELETE /api/v1/social/comments/{id}
func (m *Module) apiDeleteComment(w http.ResponseWriter, r *http.Request) error {
	if err := allow(m.editL, r); err != nil {
		return err
	}
	if _, err := m.svc.deleteComment(r.Context(), viewer(r), r.PathValue("id")); err != nil {
		return apiError(r.Context(), err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (m *Module) apiSetHidden(w http.ResponseWriter, r *http.Request, hidden bool) error {
	if err := allow(m.editL, r); err != nil {
		return err
	}
	c, err := m.svc.setHidden(r.Context(), viewer(r), r.PathValue("id"), hidden)
	if err != nil {
		return apiError(r.Context(), err)
	}
	core.JSON(w, http.StatusOK, c)
	return nil
}

// POST /api/v1/social/comments/{id}/hide
func (m *Module) apiHideComment(w http.ResponseWriter, r *http.Request) error {
	return m.apiSetHidden(w, r, true)
}

// DELETE /api/v1/social/comments/{id}/hide
func (m *Module) apiUnhideComment(w http.ResponseWriter, r *http.Request) error {
	return m.apiSetHidden(w, r, false)
}

// POST /api/v1/social/reports {target_kind, target_id, series_id?, reason, details?}
func (m *Module) apiReport(w http.ResponseWriter, r *http.Request) error {
	var in NewReport
	if err := core.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	if err := allow(m.reportL, r); err != nil {
		return err
	}
	rep, err := m.svc.report(r.Context(), viewer(r), in)
	if err != nil {
		return apiError(r.Context(), err)
	}
	status := http.StatusCreated
	if rep.Existing {
		status = http.StatusOK
	}
	core.JSON(w, status, rep)
	return nil
}

// seriesFollowState answers for a readable series.
func (m *Module) seriesFollowState(r *http.Request, seriesID string) (FollowState, error) {
	ctx := r.Context()
	u := core.UserFrom(ctx)
	series, err := m.svc.pub.SeriesByID(ctx, viewer(r), seriesID)
	if err != nil {
		return FollowState{}, accessErr(err)
	}
	following, notify, err := m.svc.followState(ctx, u.ID, series.ID)
	if err != nil {
		return FollowState{}, err
	}
	return FollowState{Following: following, Notify: notify, FollowerCount: series.FollowerCount}, nil
}

// GET /api/v1/social/series/{id}/follow
func (m *Module) apiSeriesFollowState(w http.ResponseWriter, r *http.Request) error {
	st, err := m.seriesFollowState(r, r.PathValue("id"))
	if err != nil {
		return apiError(r.Context(), err)
	}
	core.JSON(w, http.StatusOK, st)
	return nil
}

// PUT /api/v1/social/series/{id}/follow {notify?: bool (default true)}
func (m *Module) apiFollowSeries(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	in := struct {
		Notify *bool `json:"notify"`
	}{}
	if r.ContentLength != 0 {
		if err := core.DecodeJSON(w, r, &in); err != nil && !isEmptyBody(err) {
			return err
		}
	}
	if err := allow(m.followL, r); err != nil {
		return err
	}
	series, err := m.svc.pub.SeriesByID(ctx, viewer(r), r.PathValue("id"))
	if err != nil {
		return apiError(ctx, accessErr(err))
	}
	notify := in.Notify == nil || *in.Notify
	if err := m.svc.follow(ctx, core.UserFrom(ctx).ID, series, notify); err != nil {
		return apiError(ctx, err)
	}
	st, err := m.seriesFollowState(r, series.ID)
	if err != nil {
		return apiError(ctx, err)
	}
	core.JSON(w, http.StatusOK, st)
	return nil
}

// DELETE /api/v1/social/series/{id}/follow (idempotent; no access check is
// needed to remove one's own follow, so a series that became unreadable can
// still be unfollowed from the library).
func (m *Module) apiUnfollowSeries(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := allow(m.followL, r); err != nil {
		return err
	}
	if err := m.svc.unfollow(ctx, core.UserFrom(ctx).ID, r.PathValue("id")); err != nil {
		return apiError(ctx, err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// UserFollowState is a viewer's follow of a creator.
type UserFollowState struct {
	Following     bool `json:"following"`
	FollowerCount int  `json:"follower_count"`
}

// profileUser returns an active account by username (ErrNotFound otherwise).
func (m *Module) profileUser(r *http.Request, username string) (*auth.User, error) {
	u, err := m.d.Auth.UserByUsername(r.Context(), username)
	if err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if u.Status != auth.StatusActive {
		return nil, ErrNotFound
	}
	return u, nil
}

func (m *Module) userFollowState(r *http.Request, followee *auth.User) (UserFollowState, error) {
	ctx := r.Context()
	following, err := m.svc.isFollowingUser(ctx, core.UserFrom(ctx).ID, followee.ID)
	if err != nil {
		return UserFollowState{}, err
	}
	n, err := m.svc.userFollowerCount(ctx, followee.ID)
	if err != nil {
		return UserFollowState{}, err
	}
	return UserFollowState{Following: following, FollowerCount: n}, nil
}

// GET /api/v1/social/users/{username}/follow
func (m *Module) apiUserFollowState(w http.ResponseWriter, r *http.Request) error {
	u, err := m.profileUser(r, r.PathValue("username"))
	if err != nil {
		return apiError(r.Context(), err)
	}
	st, err := m.userFollowState(r, u)
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, st)
	return nil
}

// PUT /api/v1/social/users/{username}/follow
func (m *Module) apiFollowUser(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := allow(m.followL, r); err != nil {
		return err
	}
	u, err := m.profileUser(r, r.PathValue("username"))
	if err != nil {
		return apiError(ctx, err)
	}
	if err := m.svc.followUser(ctx, core.UserFrom(ctx).ID, u); err != nil {
		return apiError(ctx, err)
	}
	st, err := m.userFollowState(r, u)
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, st)
	return nil
}

// DELETE /api/v1/social/users/{username}/follow
func (m *Module) apiUnfollowUser(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := allow(m.followL, r); err != nil {
		return err
	}
	u, err := m.d.Auth.UserByUsername(ctx, r.PathValue("username"))
	if err == nil {
		err = m.svc.unfollowUser(ctx, core.UserFrom(ctx).ID, u.ID)
	} else if errors.Is(err, auth.ErrUserNotFound) {
		err = nil
	}
	if err != nil {
		return apiError(ctx, err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func isEmptyBody(err error) bool {
	var ae *core.APIError
	return errors.As(err, &ae) && ae.Status == http.StatusBadRequest && strings.Contains(ae.Message, "empty")
}
