package collab

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

func (m *Module) caller(r *http.Request) (caller, error) {
	return callerFrom(core.IdentityFrom(r.Context()))
}

// decode reads one JSON object (at most 64 KiB, no unknown fields).
func decode(w http.ResponseWriter, r *http.Request, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		var max *http.MaxBytesError
		if errors.As(err, &max) {
			return core.NewError(http.StatusRequestEntityTooLarge, "too_large", i18n.T(r.Context(), "collab.error.invalid"))
		}
		return core.ErrBadRequest(i18n.T(r.Context(), "collab.error.invalid"))
	}
	if d.Decode(new(any)) != io.EOF {
		return core.ErrBadRequest(i18n.T(r.Context(), "collab.error.invalid"))
	}
	return nil
}

// apiError maps service errors. A project the caller cannot read is a 404,
// never a 403, so its existence does not leak.
func apiError(r *http.Request, err error) error {
	var fe fieldError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &fe):
		return core.ErrValidation(map[string]string{fe.field: i18n.T(r.Context(), fe.key)})
	case errors.Is(err, errNotFound), errors.Is(err, studio.ErrNotFound):
		return core.ErrNotFound()
	case errors.Is(err, errSession):
		return core.ErrForbidden(i18n.T(r.Context(), "collab.error.session"))
	case errors.Is(err, errForbidden), errors.Is(err, studio.ErrForbidden):
		return core.ErrForbidden(i18n.T(r.Context(), "collab.error.forbidden"))
	case errors.Is(err, errConflict), errors.Is(err, studio.ErrConflict):
		return core.ErrConflict(i18n.T(r.Context(), "collab.error.changed"))
	case errors.Is(err, studio.ErrInvalid):
		return core.ErrValidation(map[string]string{"request": i18n.T(r.Context(), "collab.error.invalid")})
	}
	return err
}

func (m *Module) reviewJSON(w http.ResponseWriter, r *http.Request, c caller, status int, projectID, pageID string) error {
	v, err := m.review(r.Context(), c, projectID, pageID)
	if err != nil {
		return apiError(r, err)
	}
	w.Header().Set("Cache-Control", "no-store")
	core.JSON(w, status, v)
	return nil
}

func (m *Module) getReview(w http.ResponseWriter, r *http.Request) error {
	c, err := m.caller(r)
	if err != nil {
		return apiError(r, err)
	}
	return m.reviewJSON(w, r, c, http.StatusOK, r.PathValue("project"), r.PathValue("page"))
}

func (m *Module) postComment(w http.ResponseWriter, r *http.Request) error {
	c, err := m.caller(r)
	if err != nil {
		return apiError(r, err)
	}
	var in NewComment
	if err := decode(w, r, &in); err != nil {
		return err
	}
	projectID, pageID := r.PathValue("project"), r.PathValue("page")
	id, err := m.createComment(r.Context(), c, projectID, pageID, in)
	if err != nil {
		return apiError(r, err)
	}
	w.Header().Set("X-Comment-ID", id)
	return m.reviewJSON(w, r, c, http.StatusCreated, projectID, pageID)
}

// pageOf returns the page id that holds a comment's panel, for the response.
func (m *Module) pageOf(r *http.Request, projectID, commentID string) (string, error) {
	root, err := m.root(r.Context(), projectID, commentID)
	if err != nil {
		return "", err
	}
	p, _, err := m.panelPage(r.Context(), projectID, root)
	return p.ID, err
}

func (m *Module) postReply(w http.ResponseWriter, r *http.Request) error {
	c, err := m.caller(r)
	if err != nil {
		return apiError(r, err)
	}
	var in struct {
		Body string `json:"body"`
	}
	if err := decode(w, r, &in); err != nil {
		return err
	}
	projectID, commentID := r.PathValue("project"), r.PathValue("comment")
	id, err := m.reply(r.Context(), c, projectID, commentID, in.Body)
	if err != nil {
		return apiError(r, err)
	}
	pageID, err := m.pageOf(r, projectID, commentID)
	if err != nil {
		return apiError(r, err)
	}
	w.Header().Set("X-Comment-ID", id)
	return m.reviewJSON(w, r, c, http.StatusCreated, projectID, pageID)
}

func (m *Module) postStatus(w http.ResponseWriter, r *http.Request) error {
	c, err := m.caller(r)
	if err != nil {
		return apiError(r, err)
	}
	var in struct {
		Status string `json:"status"`
	}
	if err := decode(w, r, &in); err != nil {
		return err
	}
	projectID, commentID := r.PathValue("project"), r.PathValue("comment")
	if err := m.setStatus(r.Context(), c, projectID, commentID, in.Status); err != nil {
		return apiError(r, err)
	}
	pageID, err := m.pageOf(r, projectID, commentID)
	if err != nil {
		return apiError(r, err)
	}
	return m.reviewJSON(w, r, c, http.StatusOK, projectID, pageID)
}

func (m *Module) postApproval(w http.ResponseWriter, r *http.Request) error {
	c, err := m.caller(r)
	if err != nil {
		return apiError(r, err)
	}
	var in struct {
		Version int `json:"version"`
	}
	if err := decode(w, r, &in); err != nil {
		return err
	}
	projectID, pageID := r.PathValue("project"), r.PathValue("page")
	if err := m.approvePage(r.Context(), c, projectID, pageID, in.Version); err != nil {
		return apiError(r, err)
	}
	return m.reviewJSON(w, r, c, http.StatusOK, projectID, pageID)
}

func (m *Module) getMembers(w http.ResponseWriter, r *http.Request) error {
	c, err := m.caller(r)
	if err != nil {
		return apiError(r, err)
	}
	projectID := r.PathValue("project")
	if err := m.read(r.Context(), c, projectID); err != nil {
		return apiError(r, err)
	}
	list, err := m.members(r.Context(), projectID)
	if err != nil {
		return err
	}
	core.JSON(w, http.StatusOK, map[string]any{"members": list})
	return nil
}

func (m *Module) postMember(w http.ResponseWriter, r *http.Request) error {
	c, err := m.caller(r)
	if err != nil {
		return apiError(r, err)
	}
	var in MemberInput
	if err := decode(w, r, &in); err != nil {
		return err
	}
	mb, err := m.addMember(r.Context(), c, r.PathValue("project"), in)
	if err != nil {
		return apiError(r, err)
	}
	core.JSON(w, http.StatusCreated, map[string]any{"member": mb})
	return nil
}

func (m *Module) patchMember(w http.ResponseWriter, r *http.Request) error {
	c, err := m.caller(r)
	if err != nil {
		return apiError(r, err)
	}
	var in MemberInput
	if err := decode(w, r, &in); err != nil {
		return err
	}
	if in.Username != "" {
		return apiError(r, fieldError{"username", "collab.error.invalid"})
	}
	userID := r.PathValue("user")
	if !db.ValidID(userID) {
		return core.ErrNotFound()
	}
	mb, err := m.updateMember(r.Context(), c, r.PathValue("project"), userID, in)
	if err != nil {
		return apiError(r, err)
	}
	core.JSON(w, http.StatusOK, map[string]any{"member": mb})
	return nil
}

func (m *Module) deleteMember(w http.ResponseWriter, r *http.Request) error {
	c, err := m.caller(r)
	if err != nil {
		return apiError(r, err)
	}
	userID := r.PathValue("user")
	if !db.ValidID(userID) {
		return core.ErrNotFound()
	}
	if err := m.removeMember(r.Context(), c, r.PathValue("project"), userID); err != nil {
		return apiError(r, err)
	}
	core.JSON(w, http.StatusOK, map[string]bool{"removed": true})
	return nil
}
