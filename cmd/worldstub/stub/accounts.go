package stub

import (
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/sdk/mvpack"
)

type account struct {
	ID              string
	DisplayName     string
	Handle          string // "" until chosen
	HandleChangedAt time.Time
	Previous        []previousHandle
	Bio             string
	Links           []string
	TrustLevel      int
	Verified        bool
	Developer       bool
	TwoFactor       bool
	ShowServerName  bool
	CreatedAt       time.Time
	ApprovedItems   int
}

type previousHandle struct {
	Handle        string
	UsedFrom      time.Time
	UsedUntil     time.Time
	ReservedUntil time.Time
}

type handleRecord struct {
	AccountID     string
	ReservedUntil time.Time
}

// reservedHandles are system and staff words nobody can take.
var reservedHandles = map[string]bool{
	"about": true, "abuse": true, "admin": true, "administrator": true, "anonymous": true, "api": true,
	"everyone": true, "help": true, "legal": true, "login": true, "mangaveyra": true, "mod": true,
	"moderator": true, "null": true, "official": true, "review": true, "reviewer": true, "root": true,
	"security": true, "settings": true, "signup": true, "staff": true, "support": true, "system": true,
	"team": true, "trust-and-safety": true, "undefined": true, "world": true, "www": true, "veyra": true,
}

// impersonationWords may not appear even as one hyphen-separated part of a
// handle ("adm1n-official" is blocked as "admin", screen WA9).
var impersonationWords = map[string]bool{
	"admin": true, "administrator": true, "mangaveyra": true, "moderator": true, "official": true, "staff": true,
	"veyra": true,
}

// lookalikes folds the character swaps people use to dodge name lists onto the
// letters they imitate (0→o, 1→i, 3→e, 4→a, 5→s, 7→t; screen WA9 "catch look-alikes").
var lookalikes = strings.NewReplacer("0", "o", "1", "i", "3", "e", "4", "a", "5", "s", "7", "t")

// nameSkeleton reduces a handle to the form name lists are compared in:
// look-alike digits folded and hyphens removed ("w0rld", "wor-ld" → "world").
func nameSkeleton(h string) string {
	return strings.ReplaceAll(lookalikes.Replace(h), "-", "")
}

// reservedSkeletons holds nameSkeleton of every reserved handle.
var reservedSkeletons = func() map[string]bool {
	out := map[string]bool{}
	for h := range reservedHandles {
		out[nameSkeleton(h)] = true
	}
	return out
}()

// isReserved reports whether h is a reserved name, a look-alike spelling of one,
// or contains a staff/brand word as one of its parts.
func isReserved(h string) bool {
	if reservedHandles[h] || reservedSkeletons[nameSkeleton(h)] {
		return true
	}
	for _, part := range strings.Split(h, "-") {
		if impersonationWords[nameSkeleton(part)] {
			return true
		}
	}
	return false
}

// ---- JSON ----

type accountJSON struct {
	ID                 string               `json:"id"`
	DisplayName        string               `json:"display_name"`
	Handle             *string              `json:"handle"`
	NeedsProfile       bool                 `json:"needs_profile"`
	HandleChangedAt    *string              `json:"handle_changed_at"`
	NextHandleChangeAt *string              `json:"next_handle_change_at"`
	PreviousHandles    []previousHandleJSON `json:"previous_handles"`
	Bio                string               `json:"bio"`
	Links              []string             `json:"links"`
	AvatarURL          *string              `json:"avatar_url"`
	ProfileURL         string               `json:"profile_url,omitempty"`
	TrustLevel         int                  `json:"trust_level"`
	Verified           bool                 `json:"verified"`
	Developer          bool                 `json:"developer"`
	TwoFactor          bool                 `json:"two_factor"`
	ShowServerName     bool                 `json:"show_server_name"`
	CreatedAt          string               `json:"created_at"`
	Stats              accountStats         `json:"stats"`
}

type previousHandleJSON struct {
	Handle        string `json:"handle"`
	UsedFrom      string `json:"used_from"`
	UsedUntil     string `json:"used_until"`
	ReservedUntil string `json:"reserved_until"`
}

type accountStats struct {
	ApprovedItems int `json:"approved_items"`
	TotalUses     int `json:"total_uses"`
}

// accountView renders an account; s.mu must be held.
func (s *Server) accountView(a *account) accountJSON {
	now := s.opts.Now()
	out := accountJSON{
		ID: a.ID, DisplayName: a.DisplayName, NeedsProfile: a.Handle == "",
		Bio: a.Bio, Links: append([]string{}, a.Links...), TrustLevel: a.TrustLevel, Verified: a.Verified,
		Developer: a.Developer, TwoFactor: a.TwoFactor, ShowServerName: a.ShowServerName, CreatedAt: ts(a.CreatedAt),
		PreviousHandles: []previousHandleJSON{},
	}
	if a.Handle != "" {
		h := a.Handle
		out.Handle = &h
		out.ProfileURL = s.publicURL + "/@" + a.Handle
		out.HandleChangedAt = tsPtr(a.HandleChangedAt)
		if next := a.HandleChangedAt.Add(handleCooldown); next.After(now) {
			out.NextHandleChangeAt = tsPtr(next)
		}
	}
	for _, p := range a.Previous {
		if p.ReservedUntil.After(now) {
			out.PreviousHandles = append(out.PreviousHandles, previousHandleJSON{
				Handle: p.Handle, UsedFrom: ts(p.UsedFrom), UsedUntil: ts(p.UsedUntil), ReservedUntil: ts(p.ReservedUntil),
			})
		}
	}
	out.Stats.ApprovedItems = a.ApprovedItems
	for _, it := range s.items {
		if it.CreatorID == a.ID {
			out.Stats.TotalUses += it.Uses
		}
	}
	return out
}

// ---- handlers ----

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.authorize(w, r, "profile")
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.accountView(a))
}

type profileUpdate struct {
	DisplayName         *string   `json:"display_name"`
	Handle              *string   `json:"handle"`
	Bio                 *string   `json:"bio"`
	Links               *[]string `json:"links"`
	ShowServerName      *bool     `json:"show_server_name"`
	UpdateCreditOnItems *bool     `json:"update_credit_on_items"`
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	var req profileUpdate
	if !decodeStrict(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.authorize(w, r, "profile")
	if !ok {
		return
	}
	if req == (profileUpdate{}) {
		writeError(w, http.StatusBadRequest, "bad_request", "Send at least one field to change.")
		return
	}
	now := s.opts.Now()
	var details []errorDetail
	if req.DisplayName != nil && !mvpack.ValidDisplayName(*req.DisplayName) {
		details = append(details, errorDetail{Field: "display_name", Code: "display_name_invalid",
			Message: "Use 1–40 characters without leading or trailing spaces."})
	}
	changeHandle := req.Handle != nil && *req.Handle != a.Handle
	if changeHandle {
		h := *req.Handle
		st := s.handleStatus(h, a)
		switch {
		case !st.Available:
			details = append(details, errorDetail{Field: "handle", Code: "handle_" + map[string]string{
				"invalid": "invalid", "taken": "taken", "reserved": "reserved", "blocked": "blocked", "previous_handle": "previous",
			}[deref(st.Reason)], Message: deref(st.Message), Value: h})
		case a.Handle != "" && a.HandleChangedAt.Add(handleCooldown).After(now):
			next := a.HandleChangedAt.Add(handleCooldown)
			details = append(details, errorDetail{Field: "handle", Code: "handle_change_too_soon",
				Message: "Next change after " + next.UTC().Format("2006-01-02") + ".", Value: h})
		}
	}
	if req.Bio != nil && (!utf8.ValidString(*req.Bio) || utf8.RuneCountInString(*req.Bio) > 280) {
		details = append(details, errorDetail{Field: "bio", Code: "bio_invalid", Message: "At most 280 characters."})
	}
	if req.Links != nil {
		if len(*req.Links) > 4 {
			details = append(details, errorDetail{Field: "links", Code: "links_invalid", Message: "At most 4 links."})
		}
		for _, l := range *req.Links {
			if l == "" || len(l) > 200 || strings.ContainsAny(l, " \t\n<>\"") {
				details = append(details, errorDetail{Field: "links", Code: "links_invalid", Message: "Links are plain web addresses up to 200 characters.", Value: l})
				break
			}
		}
	}
	if len(details) > 0 {
		msg := "Some fields are not valid."
		if len(details) == 1 {
			msg = details[0].Message
		}
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", msg, details...)
		return
	}
	if req.DisplayName != nil {
		a.DisplayName = *req.DisplayName
	}
	if changeHandle {
		s.setHandle(a, *req.Handle, now)
	}
	if req.Bio != nil {
		a.Bio = *req.Bio
	}
	if req.Links != nil {
		a.Links = append([]string{}, *req.Links...)
	}
	if req.ShowServerName != nil {
		a.ShowServerName = *req.ShowServerName
	}
	// Credit on items is rendered from the live account, so update_credit_on_items
	// needs no extra work in the stub.
	writeJSON(w, http.StatusOK, s.accountView(a))
}

// setHandle moves a to handle h, reserving the old one for six months; s.mu must be held.
func (s *Server) setHandle(a *account, h string, now time.Time) {
	if a.Handle != "" {
		used := a.HandleChangedAt
		if used.IsZero() {
			used = a.CreatedAt
		}
		a.Previous = append(a.Previous, previousHandle{Handle: a.Handle, UsedFrom: used, UsedUntil: now, ReservedUntil: now.Add(handleReserve)})
		s.prevHandles[a.Handle] = &handleRecord{AccountID: a.ID, ReservedUntil: now.Add(handleReserve)}
		delete(s.handles, a.Handle)
	}
	a.Previous = slices.DeleteFunc(a.Previous, func(p previousHandle) bool { return p.Handle == h })
	delete(s.prevHandles, h)
	a.Handle = h
	a.HandleChangedAt = now
	s.handles[h] = a.ID
}

type handleAvailability struct {
	Handle      string   `json:"handle"`
	Available   bool     `json:"available"`
	Reason      *string  `json:"reason"`
	Message     *string  `json:"message"`
	RedirectTo  *string  `json:"redirect_to"`
	Suggestions []string `json:"suggestions,omitempty"`
}

func strp(s string) *string { return &s }

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// handleStatus decides whether viewer (may be nil) can take handle h; s.mu must be held.
func (s *Server) handleStatus(h string, viewer *account) handleAvailability {
	out := handleAvailability{Handle: h}
	now := s.opts.Now()
	switch {
	case !mvpack.ValidHandle(h):
		out.Reason, out.Message = strp("invalid"), strp("Use 3–30 lowercase letters, digits and single hyphens; start and end with a letter or digit.")
		return out
	case viewer != nil && viewer.Handle == h:
		out.Available, out.Reason, out.Message = true, strp("current"), strp("This is your current handle.")
		return out
	}
	if _, taken := s.handles[h]; taken {
		out.Reason, out.Message = strp("taken"), strp("Someone on World already uses this handle.")
		out.Suggestions = s.suggest(h, viewer)
		return out
	}
	if isReserved(h) {
		out.Reason, out.Message = strp("reserved"), strp("Reserved for World staff and system pages.")
		return out
	}
	if s.isBlocked(h, viewer) {
		out.Reason, out.Message = strp("blocked"), strp("Blocked: matches the name of another creator, a brand or an existing character.")
		return out
	}
	if rec, ok := s.prevHandles[h]; ok && rec.ReservedUntil.After(now) {
		if viewer != nil && rec.AccountID == viewer.ID {
			out.Available, out.Reason, out.Message = true, strp("own_previous"), strp("One of your previous handles; you can take it back.")
			return out
		}
		out.Reason, out.Message = strp("previous_handle"), strp("Recently used by another creator; reserved for six months so their links keep working.")
		if owner := s.accounts[rec.AccountID]; owner != nil && owner.Handle != "" {
			out.RedirectTo = strp(owner.Handle)
		}
		return out
	}
	out.Available = true
	return out
}

// isBlocked applies the stub's blocked-name list: placeholder known characters
// and names of existing items by other creators.
func (s *Server) isBlocked(h string, viewer *account) bool {
	if s.blockedNames[h] || s.blockedNames[nameSkeleton(h)] {
		return true
	}
	for _, it := range s.items {
		if slugify(it.Name) == h && (viewer == nil || it.CreatorID != viewer.ID) {
			return true
		}
	}
	return false
}

func (s *Server) suggest(h string, viewer *account) []string {
	var out []string
	for _, suffix := range []string{"-studio", "-ink", "-art", "2"} {
		c := h + suffix
		if len(c) <= 30 {
			if st := s.handleStatusNoSuggest(c, viewer); st {
				out = append(out, c)
			}
		}
	}
	return out
}

func (s *Server) handleStatusNoSuggest(h string, viewer *account) bool {
	if _, taken := s.handles[h]; taken || !mvpack.ValidHandle(h) || isReserved(h) || s.isBlocked(h, viewer) {
		return false
	}
	rec, ok := s.prevHandles[h]
	return !ok || !rec.ReservedUntil.After(s.opts.Now())
}

func (s *Server) handleHandle(w http.ResponseWriter, r *http.Request) {
	h := r.PathValue("handle")
	s.mu.Lock()
	defer s.mu.Unlock()
	var viewer *account
	if bearer(r) != "" {
		a, ok := s.authorize(w, r, "profile")
		if !ok {
			return
		}
		viewer = a
	}
	writeJSON(w, http.StatusOK, s.handleStatus(h, viewer))
}

// findAccountByHandle resolves current handles; s.mu must be held.
func (s *Server) findAccountByHandle(h string) *account {
	if id, ok := s.handles[h]; ok {
		return s.accounts[id]
	}
	return nil
}
