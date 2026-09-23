package stub

import (
	"crypto/ed25519"
	"embed"
	"html/template"
	"mime"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/sdk/mvpack"
)

// Browser pages of the stub: the human side of the device flow (/device),
// public item and profile pages that world_url and profile_url point to, and
// the stub control panel (/_stub) with manual review. Every page carries a
// "development stub" banner; none of them resembles a store (no prices, ever).

//go:embed templates/*.html
var templateFS embed.FS

var pageTemplates = func() map[string]*template.Template {
	funcs := template.FuncMap{
		"date": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			return t.UTC().Format("2 Jan 2006 15:04 UTC")
		},
		"availabilityLabel": availabilityLabel,
		"join":              strings.Join,
		"deref":             deref,
	}
	layout := template.Must(template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html"))
	out := map[string]*template.Template{}
	for _, name := range []string{"device", "device_result", "item", "profile", "index", "message"} {
		t := template.Must(layout.Clone())
		out[name] = template.Must(t.ParseFS(templateFS, "templates/"+name+".html"))
	}
	return out
}()

// availabilityLabel is the only way availability is ever shown: a word, never a price.
func availabilityLabel(a string) string {
	switch a {
	case "owned":
		return "Owned"
	case "paid":
		return "Paid · on World"
	}
	return "Free"
}

// pageData is what every page template receives.
type pageData struct {
	Title string
	Body  any
}

func (s *Server) render(w http.ResponseWriter, status int, name, title string, body any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	if err := pageTemplates[name].ExecuteTemplate(w, "layout.html", pageData{Title: title, Body: body}); err != nil {
		s.opts.Logger.Error("render page", "page", name, "err", err)
	}
}

func (s *Server) renderMessage(w http.ResponseWriter, status int, title, sfx, text, linkHref, linkText string) {
	s.render(w, status, "message", title, map[string]string{"SFX": sfx, "Text": text, "Href": linkHref, "Link": linkText})
}

// ---- device flow (RFC 8628 verification URI) ----

// scopeLabels explains each OAuth scope on the approval page.
var scopeLabels = map[string]string{
	"profile": "See and edit your World name and profile",
	"servers": "Register this server's signing key",
	"submit":  "Submit items for review in your name",
	"library": "Browse the catalogue and download items in your library",
}

type accountOption struct {
	Ref, Label string
}

type devicePageData struct {
	UserCode string
	Found    bool
	Scopes   []string
	Expires  time.Time
	Accounts []accountOption
	Error    string
}

// accountOptions lists the accounts a developer can approve as; s.mu must be held.
func (s *Server) accountOptions() []accountOption {
	var out []accountOption
	for _, a := range s.accounts {
		if a.Handle != "" {
			out = append(out, accountOption{Ref: a.Handle, Label: a.DisplayName + " (@" + a.Handle + ")"})
		}
	}
	slices.SortFunc(out, func(a, b accountOption) int { return strings.Compare(a.Ref, b.Ref) })
	return append(out, accountOption{Ref: "new", Label: "A new account without a World name yet"})
}

func (s *Server) handleDevicePage(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.URL.Query().Get("user_code"))
	data := devicePageData{UserCode: normalizeUserCode(code)}
	now := s.opts.Now()
	s.mu.Lock()
	if code != "" {
		if dc, ok := s.userCodes[data.UserCode]; ok {
			if d := s.devices[dc]; d != nil && d.Status == "pending" && now.Before(d.ExpiresAt) {
				data.Found, data.Expires = true, d.ExpiresAt
				for _, sc := range d.Scope {
					data.Scopes = append(data.Scopes, scopeLabels[sc])
				}
			}
		}
		if !data.Found {
			data.Error = "That code is unknown, expired or already used. Start linking again on your server."
		}
	}
	data.Accounts = s.accountOptions()
	s.mu.Unlock()
	s.render(w, http.StatusOK, "device", "Link a server", data)
}

func (s *Server) handleDeviceDecision(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		s.renderMessage(w, http.StatusBadRequest, "Bad request", "HMMM…", "The form could not be read.", "/device", "Try again")
		return
	}
	code := r.PostForm.Get("user_code")
	approve := r.PostForm.Get("decision") == "approve"
	name, err := s.approveDevice(code, r.PostForm.Get("account"), approve)
	if err != nil {
		s.renderMessage(w, http.StatusNotFound, "Code not found", "SHIIN…",
			"That code is unknown, expired or already used. Start linking again on your server.", "/device", "Enter another code")
		return
	}
	s.render(w, http.StatusOK, "device_result", "Link a server", map[string]any{"Approved": approve, "Name": name})
}

// ---- public item and profile pages ----

type itemPageData struct {
	Item      catalogItemDetailJSON
	HasCover  bool
	ProfileAt string
}

func (s *Server) handleItemPage(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	it, ok := s.items[r.PathValue("id")]
	var data itemPageData
	if ok {
		data = itemPageData{Item: s.itemDetailView(it, nil), HasCover: it.latest().Cover != nil}
		data.ProfileAt = "/@" + data.Item.Creator.Handle
	}
	s.mu.Unlock()
	if !ok {
		s.renderMessage(w, http.StatusNotFound, "Not found", "SHIIN…", "No item with that id on this stub.", "/_stub", "Back to the stub")
		return
	}
	s.render(w, http.StatusOK, "item", data.Item.Name, data)
}

type profilePageData struct {
	Account accountJSON
	Items   []catalogItemJSON
}

func (s *Server) handleProfilePage(w http.ResponseWriter, r *http.Request) {
	at := r.PathValue("at")
	if !strings.HasPrefix(at, "@") {
		http.NotFound(w, r)
		return
	}
	h := strings.TrimPrefix(at, "@")
	s.mu.Lock()
	a := s.findAccountByHandle(h)
	if a == nil {
		// Old handles redirect to the new one for six months (screen H9).
		rec, ok := s.prevHandles[h]
		var target string
		if ok && rec.ReservedUntil.After(s.opts.Now()) {
			if owner := s.accounts[rec.AccountID]; owner != nil && owner.Handle != "" {
				target = "/@" + owner.Handle
			}
		}
		s.mu.Unlock()
		if target != "" {
			http.Redirect(w, r, target, http.StatusMovedPermanently)
			return
		}
		s.renderMessage(w, http.StatusNotFound, "Not found", "SHIIN…", "Nobody on this stub uses that handle.", "/_stub", "Back to the stub")
		return
	}
	data := profilePageData{Account: s.accountView(a)}
	for _, id := range s.itemOrder {
		if it := s.items[id]; it.CreatorID == a.ID {
			data.Items = append(data.Items, s.itemView(it, nil))
		}
	}
	s.mu.Unlock()
	s.render(w, http.StatusOK, "profile", a.DisplayName, data)
}

// ---- stub control panel ----

type indexData struct {
	Options     Options
	PublicURL   string
	PackKey     string
	LicenceKey  string
	Devices     []deviceRow
	Submissions []submissionRow
	Accounts    []accountRow
	Items       []catalogItemJSON
	Decisions   []string
}

type deviceRow struct {
	UserCode string
	Scope    string
	Expires  time.Time
}

type submissionRow struct {
	ID, Handle, Name, Kind, Version, Status, Checks, Message string
	Reviewable                                               bool
	WorldURL                                                 string
	Created                                                  time.Time
}

type accountRow struct {
	DisplayName, Handle string
	Keys, Library       int
	Developer           bool
}

func (s *Server) handleStubIndex(w http.ResponseWriter, r *http.Request) {
	now := s.opts.Now()
	s.mu.Lock()
	pk, lk := s.packKey.Public().(ed25519.PublicKey), s.licenceKey.Public().(ed25519.PublicKey)
	data := indexData{
		Options: s.opts, PublicURL: s.publicURL,
		PackKey:    s.packKeyID + " · " + mvpack.ShortFingerprint(pk),
		LicenceKey: s.licenceKeyID + " · " + mvpack.ShortFingerprint(lk),
		Decisions:  []string{statusApproved, statusChangesRequested, statusRejected, statusHeld},
	}
	for _, d := range s.devices {
		if d.Status == "pending" && now.Before(d.ExpiresAt) {
			data.Devices = append(data.Devices, deviceRow{UserCode: d.UserCode, Scope: strings.Join(d.Scope, " "), Expires: d.ExpiresAt})
		}
	}
	slices.SortFunc(data.Devices, func(a, b deviceRow) int { return a.Expires.Compare(b.Expires) })
	for _, id := range s.subOrder {
		sub := s.submissions[id]
		s.advance(sub)
		row := submissionRow{
			ID: sub.ID, Handle: s.accounts[sub.AccountID].Handle, Name: sub.Manifest.Name, Kind: string(sub.Manifest.Kind),
			Version: sub.Manifest.Version, Status: sub.Status, Created: sub.CreatedAt,
			Reviewable: sub.Status == statusInReview || sub.Status == statusHeld,
		}
		if sub.ChecksRevealedAt.IsZero() {
			row.Checks = "checks pending"
		} else {
			var parts []string
			for _, c := range sub.Checks {
				if c.Status != "passed" {
					parts = append(parts, c.Name+" "+c.Status)
				}
			}
			row.Checks = "all checks passed"
			if len(parts) > 0 {
				row.Checks = strings.Join(parts, ", ")
			}
		}
		if sub.Review != nil {
			row.Message = sub.Review.Message
		}
		if sub.WorldItemID != "" {
			row.WorldURL = "/items/" + sub.WorldItemID
		}
		data.Submissions = append(data.Submissions, row)
	}
	for _, a := range s.accounts {
		row := accountRow{DisplayName: a.DisplayName, Handle: a.Handle, Library: len(s.library[a.ID]), Developer: a.Developer}
		for _, k := range s.servers {
			if k.AccountID == a.ID && k.RevokedAt.IsZero() {
				row.Keys++
			}
		}
		data.Accounts = append(data.Accounts, row)
	}
	slices.SortFunc(data.Accounts, func(a, b accountRow) int {
		return strings.Compare(a.Handle+"\x00"+a.DisplayName, b.Handle+"\x00"+b.DisplayName)
	})
	for _, id := range s.itemOrder {
		data.Items = append(data.Items, s.itemView(s.items[id], nil))
	}
	s.mu.Unlock()
	s.render(w, http.StatusOK, "index", "Development stub", data)
}

// reviewRequest is the body of POST /_stub/review (JSON or form).
type reviewRequest struct {
	SubmissionID string `json:"submission_id"`
	Decision     string `json:"decision"`
	Message      string `json:"message"`
}

// ReviewError is returned by Review when a decision cannot be applied.
type ReviewError struct {
	Status int
	Code   string
	Msg    string
}

func (e *ReviewError) Error() string { return e.Msg }

// defaultReviewMessages are used when the reviewer leaves the message empty.
var defaultReviewMessages = map[string]string{
	statusApproved:         "Approved. Thanks for sharing it on World.",
	statusChangesRequested: "Please make the requested changes and submit a new version.",
	statusRejected:         "Rejected by the reviewer.",
	statusHeld:             "Held for a closer look by another reviewer.",
}

// Review records a reviewer decision on a submission, the programmatic
// equivalent of POST /_stub/review. Automated checks still pending are
// completed first; a submission they rejected cannot be reviewed.
func (s *Server) Review(submissionID, decision, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.review(submissionID, decision, message)
	return err
}

// review applies a decision; s.mu must be held.
func (s *Server) review(submissionID, decision, message string) (*submission, error) {
	if _, ok := defaultReviewMessages[decision]; !ok {
		return nil, &ReviewError{http.StatusUnprocessableEntity, "validation_failed", "decision must be approved, changes_requested, rejected or held."}
	}
	sub, ok := s.submissions[submissionID]
	if !ok {
		return nil, &ReviewError{http.StatusNotFound, "not_found", "No such submission."}
	}
	now := s.opts.Now()
	s.advance(sub)
	if sub.Status == statusInReview && sub.ChecksRevealedAt.IsZero() {
		s.revealChecks(sub, now)
	}
	if sub.Status != statusInReview && sub.Status != statusHeld {
		return nil, &ReviewError{http.StatusConflict, "not_reviewable", "Only submissions in review or held can be decided; this one is " + sub.Status + "."}
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = defaultReviewMessages[decision]
	}
	if err := s.decide(sub, decision, message, "stub-reviewer", false, now); err != nil {
		return nil, &ReviewError{http.StatusConflict, "cannot_publish", err.Error()}
	}
	return sub, nil
}

func (s *Server) handleStubReview(w http.ResponseWriter, r *http.Request) {
	var req reviewRequest
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	isJSON := ct == "application/json"
	if isJSON {
		if !decodeStrict(w, r, &req) {
			return
		}
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := r.ParseForm(); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "Send JSON or a form.")
			return
		}
		req = reviewRequest{SubmissionID: r.PostForm.Get("submission_id"), Decision: r.PostForm.Get("decision"), Message: r.PostForm.Get("message")}
	}
	s.mu.Lock()
	sub, err := s.review(req.SubmissionID, req.Decision, req.Message)
	var view submissionJSON
	if err == nil {
		view = s.submissionView(sub)
	}
	s.mu.Unlock()
	if err != nil {
		re := err.(*ReviewError)
		if isJSON {
			writeError(w, re.Status, re.Code, re.Msg)
		} else {
			s.renderMessage(w, re.Status, "Review failed", "GASHAN!", re.Msg, "/_stub", "Back to the stub")
		}
		return
	}
	if isJSON {
		writeJSON(w, http.StatusOK, view)
		return
	}
	http.Redirect(w, r, "/_stub#submissions", http.StatusSeeOther)
}
