package stub

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/sdk/mvpack"
)

// Submission statuses.
const (
	statusInReview         = "in_review"
	statusChangesRequested = "changes_requested"
	statusApproved         = "approved"
	statusRejected         = "rejected"
	statusHeld             = "held"
)

var submissionStatuses = []string{statusInReview, statusChangesRequested, statusApproved, statusRejected, statusHeld}

// Check names in display order.
var checkNames = []string{"files", "malware", "safety", "similarity", "known_character", "signature", "licence"}

// eicar is the industry-standard antivirus test string; the stub's fake malware
// scanner flags any payload that contains it.
const eicar = `X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`

type submission struct {
	ID, AccountID    string
	Status           string
	Manifest         *mvpack.Manifest
	PackBytes        []byte
	PackSHA          string
	FileCount        int
	KeyID            string
	Checks           []check
	ChecksRevealedAt time.Time
	Review           *review
	Appeal           *appeal
	DecisionSeq      int // increments with every decision
	AppealedSeq      int // decision number that was appealed
	ReviewClock      time.Time
	WorldItemID      string
	Note             string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type check struct {
	Name, Status, Message string
}

type review struct {
	Decision, Message, Reviewer string
	Automated                   bool
	DecidedAt                   time.Time
}

type appeal struct {
	Message   string
	CreatedAt time.Time
	State     string // open | upheld | overturned
}

// ---- JSON ----

type submissionJSON struct {
	ID                  string         `json:"id"`
	Status              string         `json:"status"`
	Item                submissionItem `json:"item"`
	Pack                submissionPack `json:"pack"`
	Checks              []checkJSON    `json:"checks"`
	Review              *reviewJSON    `json:"review"`
	Appeal              *appealJSON    `json:"appeal"`
	CanAppeal           bool           `json:"can_appeal"`
	QueuePosition       *int           `json:"queue_position"`
	EstimatedDecisionAt *string        `json:"estimated_decision_at"`
	WorldItemID         *string        `json:"world_item_id"`
	WorldURL            *string        `json:"world_url"`
	Note                *string        `json:"note"`
	CreatedAt           string         `json:"created_at"`
	UpdatedAt           string         `json:"updated_at"`
}

type submissionItem struct {
	Kind    mvpack.Kind    `json:"kind"`
	PackID  string         `json:"pack_id"`
	Name    string         `json:"name"`
	Version string         `json:"version"`
	Licence string         `json:"licence"`
	AILabel mvpack.AILabel `json:"ai_label"`
}

type submissionPack struct {
	SHA256    string  `json:"sha256"`
	Size      int     `json:"size"`
	FileCount int     `json:"file_count"`
	KeyID     *string `json:"key_id"`
}

type checkJSON struct {
	Name        string  `json:"name"`
	Status      string  `json:"status"`
	Message     *string `json:"message"`
	CompletedAt *string `json:"completed_at"`
}

type reviewJSON struct {
	Decision  string  `json:"decision"`
	Message   string  `json:"message"`
	Reviewer  *string `json:"reviewer"`
	Automated bool    `json:"automated"`
	DecidedAt string  `json:"decided_at"`
}

type appealJSON struct {
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
	State     string `json:"state"`
}

func (sub *submission) canAppeal() bool {
	switch sub.Status {
	case statusChangesRequested, statusRejected, statusHeld:
		return sub.AppealedSeq != sub.DecisionSeq
	}
	return false
}

// submissionView renders a submission; s.mu must be held and sub advanced.
func (s *Server) submissionView(sub *submission) submissionJSON {
	m := sub.Manifest
	out := submissionJSON{
		ID: sub.ID, Status: sub.Status,
		Item:      submissionItem{Kind: m.Kind, PackID: m.ID, Name: m.Name, Version: m.Version, Licence: m.Licence, AILabel: m.AILabel},
		Pack:      submissionPack{SHA256: sub.PackSHA, Size: len(sub.PackBytes), FileCount: sub.FileCount},
		CanAppeal: sub.canAppeal(), CreatedAt: ts(sub.CreatedAt), UpdatedAt: ts(sub.UpdatedAt),
	}
	if sub.KeyID != "" {
		out.Pack.KeyID = strp(sub.KeyID)
	}
	for _, c := range sub.Checks {
		cj := checkJSON{Name: c.Name, Status: "pending"}
		if !sub.ChecksRevealedAt.IsZero() {
			cj.Status, cj.Message, cj.CompletedAt = c.Status, strp(c.Message), tsPtr(sub.ChecksRevealedAt)
		}
		out.Checks = append(out.Checks, cj)
	}
	if rv := sub.Review; rv != nil {
		rj := &reviewJSON{Decision: rv.Decision, Message: rv.Message, Automated: rv.Automated, DecidedAt: ts(rv.DecidedAt)}
		if rv.Reviewer != "" {
			rj.Reviewer = strp(rv.Reviewer)
		}
		out.Review = rj
	}
	if ap := sub.Appeal; ap != nil {
		out.Appeal = &appealJSON{Message: ap.Message, CreatedAt: ts(ap.CreatedAt), State: ap.State}
	}
	if sub.Status == statusInReview {
		pos := 1
		for _, other := range s.submissions {
			if other != sub && other.Status == statusInReview && other.ReviewClock.Before(sub.ReviewClock) {
				pos++
			}
		}
		out.QueuePosition = &pos
		if s.opts.ReviewDelay > 0 {
			out.EstimatedDecisionAt = tsPtr(sub.ReviewClock.Add(max(s.opts.ReviewDelay, s.opts.CheckDelay)))
		}
	}
	if sub.WorldItemID != "" {
		out.WorldItemID = strp(sub.WorldItemID)
		out.WorldURL = strp(s.publicURL + "/items/" + sub.WorldItemID)
	}
	if sub.Note != "" {
		out.Note = strp(sub.Note)
	}
	return out
}

// ---- state machine ----

// advance applies the passage of time: reveals automated checks after
// CheckDelay (auto-rejecting or holding on failures and warnings) and approves
// after ReviewDelay when enabled; s.mu must be held.
func (s *Server) advance(sub *submission) {
	now := s.opts.Now()
	if sub.Status != statusInReview {
		return
	}
	if sub.ChecksRevealedAt.IsZero() {
		if now.Before(sub.CreatedAt.Add(s.opts.CheckDelay)) {
			return
		}
		s.revealChecks(sub, sub.CreatedAt.Add(s.opts.CheckDelay))
		if sub.Status != statusInReview {
			return
		}
	}
	if s.opts.ReviewDelay > 0 && !now.Before(sub.ReviewClock.Add(s.opts.ReviewDelay)) {
		at := sub.ReviewClock.Add(s.opts.ReviewDelay)
		if at.Before(sub.ChecksRevealedAt) {
			at = sub.ChecksRevealedAt
		}
		if err := s.decide(sub, statusApproved, "Approved automatically by the development stub after "+s.opts.ReviewDelay.String()+".", "STUB", true, at); err != nil {
			s.opts.Logger.Error("auto-approve failed", "submission", sub.ID, "err", err)
		}
	}
}

// revealChecks completes the automated checks and applies their verdict; s.mu must be held.
func (s *Server) revealChecks(sub *submission, at time.Time) {
	sub.ChecksRevealedAt = at
	sub.UpdatedAt = at
	var failed, warned []string
	for _, c := range sub.Checks {
		switch c.Status {
		case "failed":
			failed = append(failed, c.Name+": "+c.Message)
		case "warning":
			warned = append(warned, c.Name+": "+c.Message)
		}
	}
	switch {
	case len(failed) > 0:
		_ = s.decide(sub, statusRejected, "Automated checks failed — "+strings.Join(failed, "; ")+". Fix the pack and submit again.", "", true, at)
	case len(warned) > 0:
		_ = s.decide(sub, statusHeld, "Held for a closer look by a reviewer — "+strings.Join(warned, "; ")+".", "", true, at)
	}
}

// decide records a decision; s.mu must be held.
func (s *Server) decide(sub *submission, decision, message, reviewer string, automated bool, at time.Time) error {
	if decision == statusApproved {
		if err := s.approve(sub, at); err != nil {
			return err
		}
	}
	sub.Status = decision
	sub.DecisionSeq++
	sub.Review = &review{Decision: decision, Message: message, Reviewer: reviewer, Automated: automated, DecidedAt: at}
	sub.UpdatedAt = at
	if sub.Appeal != nil && sub.Appeal.State == "open" {
		sub.Appeal.State = "upheld"
		if decision == statusApproved {
			sub.Appeal.State = "overturned"
		}
		// A decision made on an appeal is final: it cannot be appealed again.
		sub.AppealedSeq = sub.DecisionSeq
	}
	return nil
}

// ---- handlers ----

func (s *Server) handleListSubmissions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status := q.Get("status")
	if status != "" && !slices.Contains(submissionStatuses, status) {
		writeError(w, http.StatusBadRequest, "bad_request", "Unknown status filter.")
		return
	}
	offset, limit, ok := pageParams(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.authorize(w, r, "submit")
	if !ok {
		return
	}
	counts := map[string]int{"all": 0}
	for _, st := range submissionStatuses {
		counts[st] = 0
	}
	var mine []*submission
	for _, id := range s.subOrder {
		sub := s.submissions[id]
		if sub.AccountID != a.ID {
			continue
		}
		s.advance(sub)
		counts["all"]++
		counts[sub.Status]++
		if status == "" || sub.Status == status {
			mine = append(mine, sub)
		}
	}
	page, next := paginate(mine, offset, limit)
	items := make([]submissionJSON, 0, len(page))
	for _, sub := range page {
		items = append(items, s.submissionView(sub))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next, "counts": counts})
}

func (s *Server) handleGetSubmission(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.authorize(w, r, "submit")
	if !ok {
		return
	}
	sub, ok := s.submissions[r.PathValue("id")]
	if !ok || sub.AccountID != a.ID {
		writeError(w, http.StatusNotFound, "not_found", "No such submission on this account.")
		return
	}
	s.advance(sub)
	writeJSON(w, http.StatusOK, s.submissionView(sub))
}

func (s *Server) handleAppeal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Message string `json:"message"`
	}
	if !decodeStrict(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.authorize(w, r, "submit")
	if !ok {
		return
	}
	sub, ok := s.submissions[r.PathValue("id")]
	if !ok || sub.AccountID != a.ID {
		writeError(w, http.StatusNotFound, "not_found", "No such submission on this account.")
		return
	}
	s.advance(sub)
	if n := utf8.RuneCountInString(strings.TrimSpace(req.Message)); n < 10 || n > 2000 {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", "Explain the appeal in 10–2000 characters.",
			errorDetail{Field: "message", Code: "message_invalid", Message: "Explain the appeal in 10–2000 characters."})
		return
	}
	if !sub.canAppeal() {
		writeError(w, http.StatusConflict, "appeal_not_allowed", "Only a changes-requested, rejected or held decision can be appealed, once per decision.")
		return
	}
	now := s.opts.Now()
	sub.AppealedSeq = sub.DecisionSeq
	sub.Appeal = &appeal{Message: strings.TrimSpace(req.Message), CreatedAt: now, State: "open"}
	sub.Status = statusInReview
	sub.Review = nil
	sub.ReviewClock = now
	sub.UpdatedAt = now
	writeJSON(w, http.StatusOK, s.submissionView(sub))
}

type signedManifest struct {
	Manifest  json.RawMessage `json:"manifest"`
	Signature json.RawMessage `json:"signature"`
}

type declarations struct {
	RightsHeld     bool `json:"rights_held"`
	Original       bool `json:"original"`
	AILabelCorrect bool `json:"ai_label_correct"`
}

func (s *Server) handleCreateSubmission(w http.ResponseWriter, r *http.Request) {
	// Authorise before reading a potentially large body.
	s.mu.Lock()
	a, ok := s.authorize(w, r, "submit")
	var needsProfile bool
	var accID string
	if ok {
		needsProfile, accID = a.Handle == "", a.ID
		if key := r.Header.Get("Idempotency-Key"); key != "" {
			if id, seen := s.idempotency[accID+"\x00"+key]; seen {
				sub := s.submissions[id]
				s.advance(sub)
				writeJSON(w, http.StatusAccepted, s.submissionView(sub))
				s.mu.Unlock()
				return
			}
		}
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	if needsProfile {
		writeError(w, http.StatusConflict, "profile_incomplete", "Choose your World name before submitting.")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.opts.MaxUpload+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Send multipart/form-data with signed_manifest, declarations and pack.")
		return
	}
	var sm *signedManifest
	var decl *declarations
	var note string
	var pack []byte
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			s.uploadError(w, err)
			return
		}
		switch part.FormName() {
		case "signed_manifest":
			sm = &signedManifest{}
			if err := json.NewDecoder(io.LimitReader(part, 2<<20)).Decode(sm); err != nil {
				writeError(w, http.StatusBadRequest, "bad_request", "signed_manifest must be JSON {manifest, signature}.")
				return
			}
		case "declarations":
			decl = &declarations{}
			if err := json.NewDecoder(io.LimitReader(part, 4<<10)).Decode(decl); err != nil {
				writeError(w, http.StatusBadRequest, "bad_request", "declarations must be JSON.")
				return
			}
		case "note":
			b, _ := io.ReadAll(io.LimitReader(part, 8<<10))
			note = strings.TrimSpace(string(b))
		case "pack":
			var buf bytes.Buffer
			n, err := io.Copy(&buf, io.LimitReader(part, s.opts.MaxUpload+1))
			if err != nil {
				s.uploadError(w, err)
				return
			}
			if n > s.opts.MaxUpload {
				writeError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("Packs are limited to %d bytes.", s.opts.MaxUpload))
				return
			}
			pack = buf.Bytes()
		default:
			writeError(w, http.StatusBadRequest, "bad_request", "Unknown form field "+strconv.Quote(part.FormName())+".")
			return
		}
	}

	var details []errorDetail
	if pack == nil {
		details = append(details, errorDetail{Field: "pack", Code: "pack_required", Message: "Attach the signed .mvpack file."})
	}
	if decl == nil || !decl.RightsHeld || !decl.Original || !decl.AILabelCorrect {
		details = append(details, errorDetail{Field: "declarations", Code: "declarations_required",
			Message: "Confirm that you have the rights, that the item is original and that the AI label is correct."})
	}
	if utf8.RuneCountInString(note) > 1000 {
		details = append(details, errorDetail{Field: "note", Code: "note_invalid", Message: "At most 1000 characters."})
	}
	var manifest *mvpack.Manifest
	if sm == nil || len(sm.Manifest) == 0 {
		details = append(details, errorDetail{Field: "signed_manifest", Code: "manifest_invalid", Message: "Include the pack's manifest and signature."})
	} else if manifest, err = mvpack.ParseManifest(sm.Manifest); err != nil {
		details = append(details, errorDetail{Field: "signed_manifest.manifest", Code: "manifest_invalid", Message: err.Error()})
	}
	if len(details) > 0 {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", details[0].Message, details...)
		return
	}

	p, openErr := mvpack.Open(bytes.NewReader(pack), int64(len(pack)), nil)
	if openErr == nil && !sameJSON(sm.Manifest, p.CanonicalManifest()) {
		details = append(details, errorDetail{Field: "signed_manifest.manifest", Code: "manifest_mismatch", Message: "The signed manifest differs from the manifest inside the pack."})
	}
	if openErr == nil && p.Signature() != nil {
		sig, _ := json.Marshal(p.Signature())
		if !sameJSON(sm.Signature, sig) {
			details = append(details, errorDetail{Field: "signed_manifest.signature", Code: "manifest_mismatch", Message: "The signature differs from signature.json inside the pack."})
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	a = s.accounts[accID]
	if manifest.Credit.Handle != a.Handle || manifest.Credit.DisplayName != a.DisplayName {
		details = append(details, errorDetail{Field: "signed_manifest.manifest.credit", Code: "credit_mismatch",
			Message: fmt.Sprintf("Credit must use your World name: %s (@%s).", a.DisplayName, a.Handle)})
	}
	if id := manifest.Credit.WorldItemID; id != "" {
		if it, ok := s.items[id]; !ok || it.CreatorID != a.ID {
			details = append(details, errorDetail{Field: "signed_manifest.manifest.credit.world_item_id", Code: "item_not_yours",
				Message: "world_item_id must name one of your approved items.", Value: id})
		}
	}
	if manifest.Kind == mvpack.KindPlugin && !a.Developer {
		details = append(details, errorDetail{Field: "signed_manifest.manifest.kind", Code: "developer_required",
			Message: "Plugins can only be submitted from a developer account."})
	}
	if len(details) > 0 {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", details[0].Message, details...)
		return
	}
	for _, other := range s.submissions {
		if other.AccountID == a.ID && other.Manifest.ID == manifest.ID && other.Manifest.Version == manifest.Version && other.Status != statusRejected {
			writeError(w, http.StatusConflict, "duplicate_version", "This version was already submitted; bump the version.")
			return
		}
	}

	now := s.opts.Now()
	sum := sha256.Sum256(pack)
	sub := &submission{
		ID: randomID("sub_", 10), AccountID: a.ID, Status: statusInReview, Manifest: manifest,
		PackBytes: pack, PackSHA: hex.EncodeToString(sum[:]), Note: note,
		CreatedAt: now, UpdatedAt: now, ReviewClock: now,
	}
	sub.Checks, sub.KeyID, sub.FileCount = s.runChecks(a, manifest, p, openErr)
	s.submissions[sub.ID] = sub
	s.subOrder = append([]string{sub.ID}, s.subOrder...)
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		s.idempotency[a.ID+"\x00"+key] = sub.ID
	}
	s.advance(sub) // a zero CheckDelay reveals immediately
	w.Header().Set("Location", "/v1/submissions/"+sub.ID)
	writeJSON(w, http.StatusAccepted, s.submissionView(sub))
}

func (s *Server) uploadError(w http.ResponseWriter, err error) {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("Uploads are limited to %d bytes.", s.opts.MaxUpload))
		return
	}
	writeError(w, http.StatusBadRequest, "bad_request", "Could not read the upload: "+err.Error())
}

// sameJSON compares two JSON documents by their canonical form.
func sameJSON(a, b []byte) bool {
	ca, err1 := mvpack.Canonicalize(a)
	cb, err2 := mvpack.Canonicalize(b)
	return err1 == nil && err2 == nil && bytes.Equal(ca, cb)
}

// runChecks runs the stub's fake automated checks; s.mu must be held.
func (s *Server) runChecks(a *account, m *mvpack.Manifest, p *mvpack.Pack, openErr error) (checks []check, keyID string, files int) {
	result := map[string]check{}
	set := func(name, status, msg string) { result[name] = check{Name: name, Status: status, Message: msg} }
	if openErr != nil {
		set("files", "failed", openErr.Error())
		for _, n := range checkNames[1:] {
			set(n, "skipped", "Skipped because the pack could not be read.")
		}
	} else {
		files = len(p.Manifest().Files)
		if _, err := p.Verify(mvpack.VerifyOptions{}); err != nil {
			set("files", "failed", err.Error())
		} else {
			set("files", "passed", fmt.Sprintf("%d files; types, paths, sizes and hashes OK.", files))
		}
		set("malware", "passed", "Clean (development stub scanner).")
		for _, f := range p.Manifest().Files {
			if rc, err := p.Open(f.Path); err == nil {
				b, _ := io.ReadAll(rc)
				rc.Close()
				if bytes.Contains(b, []byte(eicar)) {
					set("malware", "failed", "Test signature (EICAR) found in "+f.Path+".")
					break
				}
			}
		}
		switch {
		case slices.Contains(m.Tags, "stub-unsafe"):
			set("safety", "failed", "The stub safety classifier flagged this item (tag stub-unsafe).")
		case m.ContentRating == mvpack.RatingMature:
			set("safety", "warning", "Mature rating: a reviewer checks the rating and previews.")
		default:
			set("safety", "passed", "Rating "+orDefault(m.ContentRating, mvpack.RatingAll)+" looks right.")
		}
		set("similarity", "passed", "No close match among World items.")
		for _, it := range s.items {
			if it.CreatorID != a.ID && slugify(it.Name) == slugify(m.Name) {
				set("similarity", "warning", fmt.Sprintf("Looks similar to %q by @%s.", it.Name, s.accounts[it.CreatorID].Handle))
				break
			}
		}
		set("known_character", "passed", "No match with known characters.")
		for _, n := range s.knownCharName {
			if slugify(n) == slugify(m.Name) {
				set("known_character", "warning", "Close to a known character ("+n+", stub test list).")
			}
		}
		keys := s.trustedKeys(a.ID)
		switch res, err := p.Verify(mvpack.VerifyOptions{Keys: keys, RequireTrusted: true}); {
		case p.Signature() == nil:
			set("signature", "failed", "The pack is not signed.")
		case err != nil:
			set("signature", "failed", "Not signed by a key registered to this account: "+err.Error())
		default:
			for _, sg := range res.Signatures {
				if sg.Trusted {
					keyID = sg.KeyID
					if k := s.servers[sg.KeyID]; k != nil {
						k.LastUsedAt = s.opts.Now()
					}
					set("signature", "passed", fmt.Sprintf("Signed by key %s (%s…%s).", sg.KeyID, sg.Fingerprint[:4], sg.Fingerprint[len(sg.Fingerprint)-4:]))
					break
				}
			}
		}
		if lic, ok := mvpack.LookupLicence(m.Licence); ok {
			set("licence", "passed", "Licence "+lic.Name+".")
		} else {
			set("licence", "failed", "Unknown licence.")
		}
	}
	for _, n := range checkNames {
		checks = append(checks, result[n])
	}
	return checks, keyID, files
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// ---- pagination ----

const defaultLimit = 24

// pageParams parses cursor and limit; cursors are opaque offsets.
func pageParams(w http.ResponseWriter, r *http.Request) (offset, limit int, ok bool) {
	q := r.URL.Query()
	limit = defaultLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit must be 1–100.")
			return 0, 0, false
		}
		limit = n
	}
	if c := q.Get("cursor"); c != "" {
		n, err := strconv.Atoi(strings.TrimPrefix(c, "o"))
		if err != nil || n < 0 || !strings.HasPrefix(c, "o") {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid cursor.")
			return 0, 0, false
		}
		offset = n
	}
	return offset, limit, true
}

func paginate[T any](all []T, offset, limit int) ([]T, *string) {
	if offset >= len(all) {
		return nil, nil
	}
	end := min(offset+limit, len(all))
	var next *string
	if end < len(all) {
		next = strp("o" + strconv.Itoa(end))
	}
	return all[offset:end], next
}
