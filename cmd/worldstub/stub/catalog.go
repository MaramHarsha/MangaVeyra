package stub

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/sdk/mvpack"
)

type item struct {
	ID            string
	CreatorID     string
	PackID        string
	Kind          mvpack.Kind
	Name          string
	Summary       string
	Description   string
	Licence       string
	AILabel       mvpack.AILabel
	ContentRating string
	Tags          []string
	WorksWith     []string
	// Paid marks items that are only obtainable on the World website. The stub
	// never grants them; seeded libraries already own some for the demo.
	Paid        bool
	Uses        int
	RatingAvg   float64
	RatingCount int
	FromSeries  string
	SeriesURL   string
	Versions    []*itemVersion // oldest first
	ApprovedAt  time.Time
	UpdatedAt   time.Time
}

type itemVersion struct {
	Version     string
	ReleasedAt  time.Time
	Pack        []byte // countersigned by World
	SHA256      string
	FileCount   int
	Contents    []string
	KeyID       string
	Fingerprint string
	Cover       []byte
}

func (it *item) latest() *itemVersion { return it.Versions[len(it.Versions)-1] }

func (it *item) version(v string) *itemVersion {
	if v == "" {
		return it.latest()
	}
	for _, iv := range it.Versions {
		if iv.Version == v {
			return iv
		}
	}
	return nil
}

type libEntry struct {
	ItemID          string
	Acquisition     string // free | paid | creator
	AcquiredAt      time.Time
	AcquiredVersion string
}

type download struct {
	Token     string
	ItemID    string
	Version   string
	FileName  string
	ExpiresAt time.Time
}

// catalogKind maps a pack kind to its catalogue tab.
func catalogKind(k mvpack.Kind) string {
	switch k {
	case mvpack.KindTemplate, mvpack.KindLayout:
		return "template"
	case mvpack.KindSeriesListing:
		return "series"
	}
	return string(k)
}

var catalogKinds = []string{"character", "template", "style", "series", "plugin"}

// ---- JSON (no price field, ever) ----

type creatorJSON struct {
	DisplayName string `json:"display_name"`
	Handle      string `json:"handle"`
	Verified    bool   `json:"verified"`
	ProfileURL  string `json:"profile_url"`
}

type ratingJSON struct {
	Average float64 `json:"average"`
	Count   int     `json:"count"`
}

type catalogItemJSON struct {
	ID            string         `json:"id"`
	Kind          string         `json:"kind"`
	PackKind      mvpack.Kind    `json:"pack_kind"`
	Name          string         `json:"name"`
	Summary       string         `json:"summary"`
	Creator       creatorJSON    `json:"creator"`
	Licence       string         `json:"licence"`
	LicenceName   string         `json:"licence_name"`
	CreditLine    string         `json:"credit_line"`
	AILabel       mvpack.AILabel `json:"ai_label"`
	HandDrawn     bool           `json:"hand_drawn"`
	ContentRating string         `json:"content_rating"`
	Tags          []string       `json:"tags"`
	WorksWith     []string       `json:"works_with"`
	Availability  string         `json:"availability"`
	Version       string         `json:"version"`
	WorldURL      string         `json:"world_url"`
	CoverURL      *string        `json:"cover_url"`
	Uses          int            `json:"uses"`
	Rating        *ratingJSON    `json:"rating"`
	UpdatedAt     string         `json:"updated_at"`
}

type catalogItemDetailJSON struct {
	catalogItemJSON
	Description string        `json:"description"`
	FromSeries  *string       `json:"from_series"`
	SeriesURL   *string       `json:"series_url"`
	Versions    []versionJSON `json:"versions"`
	Contents    contentsJSON  `json:"contents"`
	SignedBy    signedByJSON  `json:"signed_by"`
	ScannedAt   *string       `json:"scanned_at"`
	ApprovedAt  *string       `json:"approved_at"`
}

type versionJSON struct {
	Version    string `json:"version"`
	ReleasedAt string `json:"released_at"`
}

type contentsJSON struct {
	Size      int      `json:"size"`
	FileCount int      `json:"file_count"`
	Summary   []string `json:"summary"`
}

type signedByJSON struct {
	Handle      string `json:"handle"`
	KeyID       string `json:"key_id"`
	Fingerprint string `json:"fingerprint"`
}

var kindNoun = map[mvpack.Kind]string{
	mvpack.KindCharacter: "Character", mvpack.KindStyle: "Style", mvpack.KindTemplate: "Template",
	mvpack.KindLayout: "Layout", mvpack.KindSeriesListing: "Series", mvpack.KindPlugin: "Plugin",
}

// availability of it for viewer (may be nil); s.mu must be held.
func (s *Server) availability(it *item, viewer *account) string {
	if viewer != nil {
		if _, owned := s.library[viewer.ID][it.ID]; owned {
			return "owned"
		}
	}
	if it.Paid {
		return "paid"
	}
	return "free"
}

// itemView renders a catalogue item; s.mu must be held.
func (s *Server) itemView(it *item, viewer *account) catalogItemJSON {
	c := s.accounts[it.CreatorID]
	lic, _ := mvpack.LookupLicence(it.Licence)
	out := catalogItemJSON{
		ID: it.ID, Kind: catalogKind(it.Kind), PackKind: it.Kind, Name: it.Name, Summary: it.Summary,
		Creator:     creatorJSON{DisplayName: c.DisplayName, Handle: c.Handle, Verified: c.Verified, ProfileURL: s.publicURL + "/@" + c.Handle},
		Licence:     it.Licence,
		LicenceName: lic.Name,
		CreditLine:  fmt.Sprintf("%s by %s (@%s), %s", kindNoun[it.Kind], c.DisplayName, c.Handle, lic.Name),
		AILabel:     it.AILabel, HandDrawn: it.AILabel.HandDrawn(), ContentRating: orDefault(it.ContentRating, mvpack.RatingAll),
		Tags: append([]string{}, it.Tags...), WorksWith: append([]string{}, it.WorksWith...),
		Availability: s.availability(it, viewer), Version: it.latest().Version,
		WorldURL: s.publicURL + "/items/" + it.ID, Uses: it.Uses, UpdatedAt: ts(it.UpdatedAt),
	}
	if it.latest().Cover != nil {
		out.CoverURL = strp(s.publicURL + "/_stub/covers/" + it.ID + ".png")
	}
	if it.RatingCount > 0 {
		out.Rating = &ratingJSON{Average: it.RatingAvg, Count: it.RatingCount}
	}
	return out
}

func (s *Server) itemDetailView(it *item, viewer *account) catalogItemDetailJSON {
	v := it.latest()
	out := catalogItemDetailJSON{
		catalogItemJSON: s.itemView(it, viewer),
		Description:     it.Description,
		Contents:        contentsJSON{Size: len(v.Pack), FileCount: v.FileCount, Summary: v.Contents},
		SignedBy:        signedByJSON{Handle: s.accounts[it.CreatorID].Handle, KeyID: v.KeyID, Fingerprint: v.Fingerprint},
		ScannedAt:       tsPtr(v.ReleasedAt),
		ApprovedAt:      tsPtr(it.ApprovedAt),
	}
	if it.FromSeries != "" {
		out.FromSeries = strp(it.FromSeries)
	}
	if it.SeriesURL != "" {
		out.SeriesURL = strp(it.SeriesURL)
	}
	for _, iv := range it.Versions {
		out.Versions = append(out.Versions, versionJSON{Version: iv.Version, ReleasedAt: ts(iv.ReleasedAt)})
	}
	return out
}

// optionalViewer resolves the bearer token when present; s.mu must be held.
// ok is false when a token was sent but is invalid (a 401 was written).
func (s *Server) optionalViewer(w http.ResponseWriter, r *http.Request) (*account, bool) {
	if bearer(r) == "" {
		return nil, true
	}
	return s.authorize(w, r, "library")
}

func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind, text := q.Get("kind"), strings.ToLower(strings.TrimSpace(q.Get("q")))
	if kind != "" && !slices.Contains(catalogKinds, kind) {
		writeError(w, http.StatusBadRequest, "bad_request", "kind must be one of "+strings.Join(catalogKinds, ", ")+".")
		return
	}
	var licences []string
	if v := q.Get("licence"); v != "" {
		licences = strings.Split(v, ",")
		for _, l := range licences {
			if _, ok := mvpack.LookupLicence(l); !ok {
				writeError(w, http.StatusBadRequest, "bad_request", "Unknown licence "+l+".")
				return
			}
		}
	}
	handDrawn := false
	switch q.Get("hand_drawn_only") {
	case "", "false", "0":
	case "true", "1":
		handDrawn = true
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "hand_drawn_only must be true or false.")
		return
	}
	avail, sortBy, worksWith := q.Get("availability"), q.Get("sort"), q.Get("works_with")
	if avail != "" && !slices.Contains([]string{"free", "paid", "owned"}, avail) {
		writeError(w, http.StatusBadRequest, "bad_request", "availability must be free, paid or owned.")
		return
	}
	if sortBy != "" && !slices.Contains([]string{"relevance", "newest", "most_used"}, sortBy) {
		writeError(w, http.StatusBadRequest, "bad_request", "sort must be relevance, newest or most_used.")
		return
	}
	offset, limit, ok := pageParams(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	viewer, ok := s.optionalViewer(w, r)
	if !ok {
		return
	}
	var hits []*item
	for _, id := range s.itemOrder {
		it := s.items[id]
		c := s.accounts[it.CreatorID]
		switch {
		case kind != "" && catalogKind(it.Kind) != kind,
			len(licences) > 0 && !slices.Contains(licences, it.Licence),
			worksWith != "" && !slices.Contains(it.WorksWith, worksWith),
			handDrawn && !it.AILabel.HandDrawn(),
			avail != "" && s.availability(it, viewer) != avail:
			continue
		}
		if text != "" {
			hay := strings.ToLower(strings.Join(append([]string{it.Name, it.Summary, c.DisplayName, c.Handle, it.FromSeries}, it.Tags...), " "))
			if !strings.Contains(hay, text) {
				continue
			}
		}
		hits = append(hits, it)
	}
	switch sortBy {
	case "newest":
		slices.SortStableFunc(hits, func(a, b *item) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	case "most_used":
		slices.SortStableFunc(hits, func(a, b *item) int { return b.Uses - a.Uses })
	}
	page, next := paginate(hits, offset, limit)
	items := make([]catalogItemJSON, 0, len(page))
	for _, it := range page {
		items = append(items, s.itemView(it, viewer))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next, "total": len(hits)})
}

func (s *Server) handleCatalogItem(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	viewer, ok := s.optionalViewer(w, r)
	if !ok {
		return
	}
	it, found := s.items[r.PathValue("id")]
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "No such item on World.")
		return
	}
	writeJSON(w, http.StatusOK, s.itemDetailView(it, viewer))
}

// ---- library ----

type libraryItemJSON struct {
	Item            catalogItemJSON `json:"item"`
	Acquisition     string          `json:"acquisition"`
	AcquiredAt      string          `json:"acquired_at"`
	AcquiredVersion string          `json:"acquired_version"`
	LatestVersion   string          `json:"latest_version"`
	LicenceToken    string          `json:"licence_token"`
}

// libraryView renders one library entry; s.mu must be held.
func (s *Server) libraryView(a *account, e *libEntry) (libraryItemJSON, error) {
	it := s.items[e.ItemID]
	tok, err := s.licenceToken(a, it, e)
	if err != nil {
		return libraryItemJSON{}, err
	}
	return libraryItemJSON{
		Item: s.itemView(it, a), Acquisition: e.Acquisition, AcquiredAt: ts(e.AcquiredAt),
		AcquiredVersion: e.AcquiredVersion, LatestVersion: it.latest().Version, LicenceToken: tok,
	}, nil
}

func (s *Server) licenceToken(a *account, it *item, e *libEntry) (string, error) {
	return mvpack.SignLicenceToken(mvpack.LicenceClaims{
		ItemID: it.ID, AccountID: a.ID, Handle: a.Handle, ItemVersion: e.AcquiredVersion,
		Licence: it.Licence, Acquisition: e.Acquisition, IssuedAt: e.AcquiredAt.Unix(),
	}, s.licenceKeyID, s.licenceKey)
}

func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	if kind != "" && !slices.Contains(catalogKinds, kind) {
		writeError(w, http.StatusBadRequest, "bad_request", "kind must be one of "+strings.Join(catalogKinds, ", ")+".")
		return
	}
	offset, limit, ok := pageParams(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.authorize(w, r, "library")
	if !ok {
		return
	}
	counts := map[string]int{"all": 0}
	for _, k := range catalogKinds {
		counts[k] = 0
	}
	var entries []*libEntry
	for _, e := range s.library[a.ID] {
		k := catalogKind(s.items[e.ItemID].Kind)
		counts["all"]++
		counts[k]++
		if kind == "" || k == kind {
			entries = append(entries, e)
		}
	}
	slices.SortFunc(entries, func(x, y *libEntry) int {
		if c := y.AcquiredAt.Compare(x.AcquiredAt); c != 0 {
			return c
		}
		return strings.Compare(x.ItemID, y.ItemID)
	})
	page, next := paginate(entries, offset, limit)
	items := make([]libraryItemJSON, 0, len(page))
	for _, e := range page {
		v, err := s.libraryView(a, e)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		items = append(items, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next, "counts": counts})
}

func (s *Server) handleClaim(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ItemID string `json:"item_id"`
	}
	if !decodeStrict(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.authorize(w, r, "library")
	if !ok {
		return
	}
	it, found := s.items[req.ItemID]
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "No such item on World.")
		return
	}
	if e, owned := s.library[a.ID][it.ID]; owned {
		v, _ := s.libraryView(a, e)
		writeJSON(w, http.StatusOK, v)
		return
	}
	if it.Paid {
		url := s.publicURL + "/items/" + it.ID
		writeError(w, http.StatusForbidden, "get_on_world", "This item is not free. It can only be obtained on the World website.",
			errorDetail{Field: "item_id", Code: "get_on_world", Message: "Open the item on World.", Value: url})
		return
	}
	e := s.addToLibrary(a.ID, it, "free", s.opts.Now(), "")
	v, err := s.libraryView(a, e)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// addToLibrary records ownership; s.mu must be held.
func (s *Server) addToLibrary(accountID string, it *item, acquisition string, at time.Time, version string) *libEntry {
	if s.library[accountID] == nil {
		s.library[accountID] = map[string]*libEntry{}
	}
	if version == "" {
		version = it.latest().Version
	}
	e := &libEntry{ItemID: it.ID, Acquisition: acquisition, AcquiredAt: at, AcquiredVersion: version}
	s.library[accountID][it.ID] = e
	return e
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.authorize(w, r, "library")
	if !ok {
		return
	}
	it, found := s.items[r.PathValue("id")]
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "No such item on World.")
		return
	}
	e, owned := s.library[a.ID][it.ID]
	if !owned {
		writeError(w, http.StatusForbidden, "not_owned", "This item is not in your World library.")
		return
	}
	v := it.version(r.URL.Query().Get("version"))
	if v == nil {
		writeError(w, http.StatusNotFound, "not_found", "No such version of this item.")
		return
	}
	tok, err := s.licenceToken(a, it, e)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	now := s.opts.Now()
	d := &download{Token: randomID("dl_", 16), ItemID: it.ID, Version: v.Version,
		FileName: it.PackID + "-" + v.Version + ".mvpack", ExpiresAt: now.Add(downloadTTL)}
	s.downloads[d.Token] = d
	for tok, old := range s.downloads {
		if now.After(old.ExpiresAt) {
			delete(s.downloads, tok)
		}
	}
	it.Uses++
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"url":        s.publicURL + "/_stub/downloads/" + d.Token + "/" + d.FileName,
		"expires_at": ts(d.ExpiresAt), "file_name": d.FileName, "size": len(v.Pack),
		"sha256": v.SHA256, "version": v.Version, "licence_token": tok,
	})
}

func (s *Server) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	d, ok := s.downloads[r.PathValue("token")]
	var data []byte
	if ok && !s.opts.Now().After(d.ExpiresAt) && r.PathValue("name") == d.FileName {
		data = s.items[d.ItemID].version(d.Version).Pack
	}
	s.mu.Unlock()
	if data == nil {
		http.Error(w, "download link expired or unknown", http.StatusGone)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.mangaveyra.mvpack")
	w.Header().Set("Content-Disposition", `attachment; filename="`+d.FileName+`"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, d.FileName, time.Time{}, bytes.NewReader(data))
}

func (s *Server) handleCover(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.PathValue("file"), ".png")
	s.mu.Lock()
	var cover []byte
	if it, ok := s.items[id]; ok {
		cover = it.latest().Cover
	}
	s.mu.Unlock()
	if cover == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(cover)
}

// ---- publishing ----

// approve publishes a submission as a new item or a new version of an existing
// one, countersigned by the World key; s.mu must be held.
func (s *Server) approve(sub *submission, at time.Time) error {
	a := s.accounts[sub.AccountID]
	m := sub.Manifest
	var it *item
	if id := m.Credit.WorldItemID; id != "" {
		it = s.items[id]
		if it == nil || it.CreatorID != a.ID {
			return fmt.Errorf("world_item_id %q is not an item of this account", id)
		}
		if it.version(m.Version) != nil {
			return fmt.Errorf("version %s already published", m.Version)
		}
	} else {
		it = &item{ID: randomID("wi_", 8), CreatorID: a.ID, ApprovedAt: at}
		s.items[it.ID] = it
		s.itemOrder = append([]string{it.ID}, s.itemOrder...)
		a.ApprovedItems++
	}
	if _, err := s.publishVersion(it, m, sub.PackBytes, at); err != nil {
		if len(it.Versions) == 0 {
			delete(s.items, it.ID)
			s.itemOrder = slices.DeleteFunc(s.itemOrder, func(id string) bool { return id == it.ID })
			a.ApprovedItems--
		}
		return err
	}
	if a.TrustLevel < 3 && a.ApprovedItems >= 3*(a.TrustLevel+1) {
		a.TrustLevel++
	}
	s.addToLibrary(a.ID, it, "creator", at, m.Version)
	sub.WorldItemID = it.ID
	return nil
}

// publishVersion countersigns pack and appends it to it as a new version,
// refreshing the item's metadata from the manifest; s.mu must be held.
func (s *Server) publishVersion(it *item, m *mvpack.Manifest, pack []byte, at time.Time) (*itemVersion, error) {
	p, err := mvpack.Open(bytes.NewReader(pack), int64(len(pack)), nil)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	world := &mvpack.Signer{KeyID: s.packKeyID, Key: s.packKey, Role: mvpack.SignerWorld, Now: func() time.Time { return at }}
	if err := mvpack.AddSignature(p, &out, world); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(out.Bytes())
	v := &itemVersion{
		Version: m.Version, ReleasedAt: at, Pack: out.Bytes(), SHA256: hex.EncodeToString(sum[:]),
		FileCount: len(m.Files), Contents: contentsSummary(m),
	}
	if sig := p.Signature(); sig != nil && len(sig.Signatures) > 0 {
		v.KeyID = sig.Signatures[0].KeyID
		if pub, err := mvpack.ParsePublicKey(sig.Signatures[0].PublicKey); err == nil {
			v.Fingerprint = mvpack.Fingerprint(pub)
		}
	}
	for _, f := range m.Files {
		if f.Role == mvpack.RoleCover && f.MediaType == "image/png" {
			if rc, err := p.Open(f.Path); err == nil {
				var b bytes.Buffer
				_, err = b.ReadFrom(rc)
				rc.Close()
				if err == nil {
					v.Cover = b.Bytes()
				}
			}
		}
	}
	it.PackID, it.Kind, it.Name, it.Summary, it.Description = m.ID, m.Kind, m.Name, m.Summary, m.Description
	it.Licence, it.AILabel, it.ContentRating = m.Licence, m.AILabel, m.ContentRating
	it.Tags, it.WorksWith = slices.Clone(m.Tags), slices.Clone(m.WorksWith)
	if m.Character != nil {
		it.FromSeries = m.Character.FromSeries
	}
	if m.SeriesListing != nil {
		it.SeriesURL = m.SeriesListing.URL
	}
	it.Versions = append(it.Versions, v)
	it.UpdatedAt = at
	return v, nil
}

// contentsSummary describes a pack the way the item page does ("18 reference images").
func contentsSummary(m *mvpack.Manifest) []string {
	count := map[mvpack.Role]int{}
	for _, f := range m.Files {
		count[f.Role]++
	}
	var out []string
	plural := func(n int, one, many string) string {
		if n == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %s", n, many)
	}
	if n := count[mvpack.RoleReference]; n > 0 {
		out = append(out, plural(n, "reference image", "reference images"))
	}
	if n := count[mvpack.RoleLoRA]; n > 0 {
		base := ""
		if m.Character != nil && m.Character.BaseModel != "" {
			base = " (" + m.Character.BaseModel + ")"
		}
		out = append(out, "LoRA"+base)
	}
	if count[mvpack.RoleProfile] > 0 {
		out = append(out, "Profile, personality, voice notes")
	}
	for _, rl := range []struct {
		role  mvpack.Role
		label string
	}{
		{mvpack.RoleLayout, "Panel layout"}, {mvpack.RoleTemplate, "Page template"}, {mvpack.RoleStylePreset, "Style preset"},
		{mvpack.RoleTheme, "Theme"}, {mvpack.RoleModelManifest, "Model manifests"}, {mvpack.RoleFont, "Fonts"},
		{mvpack.RoleSeries, "Series details"},
	} {
		if count[rl.role] > 0 {
			out = append(out, rl.label)
		}
	}
	if n := count[mvpack.RolePreview]; n > 0 {
		out = append(out, plural(n, "preview", "previews"))
	}
	return out
}
