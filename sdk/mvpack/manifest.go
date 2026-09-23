// SPDX-License-Identifier: MIT
// Copyright (c) 2026 MangaVeyra contributors

package mvpack

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// FormatVersion is the value of the "mvpack" member this package writes and reads.
const FormatVersion = 1

// Kind is what a pack contains.
type Kind string

// Pack kinds.
const (
	KindCharacter     Kind = "character"
	KindStyle         Kind = "style"
	KindTemplate      Kind = "template"
	KindLayout        Kind = "layout"
	KindSeriesListing Kind = "series-listing"
	KindPlugin        Kind = "plugin"
)

// Kinds lists every pack kind in display order.
var Kinds = []Kind{KindCharacter, KindStyle, KindTemplate, KindLayout, KindSeriesListing, KindPlugin}

// Role says what a payload file is for.
type Role string

// File roles. Each role accepts a fixed set of file types (see RoleAccepts).
const (
	RoleCover          Role = "cover"           // image shown on catalogue cards
	RolePreview        Role = "preview"         // extra preview image
	RoleReference      Role = "reference"       // character reference image (sheet, turnaround, expression)
	RoleLoRA           Role = "lora"            // LoRA / adapter weights (.safetensors)
	RoleProfile        Role = "profile"         // character profile, personality, voice notes (JSON)
	RoleStylePreset    Role = "style-preset"    // style settings (JSON)
	RoleTemplate       Role = "template"        // story/page template (JSON)
	RoleLayout         Role = "layout"          // panel layout geometry (JSON)
	RoleSeries         Role = "series"          // series listing details (JSON)
	RolePluginManifest Role = "plugin-manifest" // plugin manifest (JSON, see sdk/plugin)
	RoleModelManifest  Role = "model-manifest"  // model manifest contributed by a plugin (JSON)
	RoleTheme          Role = "theme"           // theme tokens contributed by a plugin (JSON)
	RoleFont           Role = "font"            // font file contributed by a plugin
	RoleData           Role = "data"            // any other JSON data
)

// Roles lists every file role.
var Roles = []Role{RoleCover, RolePreview, RoleReference, RoleLoRA, RoleProfile, RoleStylePreset, RoleTemplate,
	RoleLayout, RoleSeries, RolePluginManifest, RoleModelManifest, RoleTheme, RoleFont, RoleData}

// RoleAccepts reports whether a file of type t may carry role r.
func RoleAccepts(r Role, t FileType) bool {
	switch r {
	case RoleCover, RolePreview, RoleReference:
		return t.IsImage()
	case RoleLoRA:
		return t == TypeSafetensors
	case RoleFont:
		return t.IsFont()
	case RoleProfile, RoleStylePreset, RoleTemplate, RoleLayout, RoleSeries, RolePluginManifest,
		RoleModelManifest, RoleTheme, RoleData:
		return t == TypeJSON
	}
	return false
}

// AI label values for one role of a work (the same scale chapters use).
const (
	AIHuman      = "human"
	AIAssisted   = "ai-assisted"
	AIGenerated  = "ai"
	RatingAll    = "all-ages"
	RatingTeen   = "teen"
	RatingMature = "mature"
)

// AILabel is the honest-provenance label of a pack, per creative role. At
// least one role must be set; unset roles do not apply to the pack.
type AILabel struct {
	Art       string `json:"art,omitempty"`
	Design    string `json:"design,omitempty"`
	Script    string `json:"script,omitempty"`
	Lettering string `json:"lettering,omitempty"`
}

// HandDrawn reports whether every labelled role is "human".
func (a AILabel) HandDrawn() bool {
	n := 0
	for _, v := range []string{a.Art, a.Design, a.Script, a.Lettering} {
		if v == "" {
			continue
		}
		if v != AIHuman {
			return false
		}
		n++
	}
	return n > 0
}

// Credit identifies the creator by their World name. It is copied into every
// project that uses the pack ("Character by Jerry Ink (@jerry-ink), CC BY 4.0").
type Credit struct {
	DisplayName string `json:"display_name"`
	Handle      string `json:"handle"`
	// WorldItemID is set once World has approved the item; empty for local packs.
	WorldItemID string `json:"world_item_id,omitempty"`
}

// String renders the credit line shown wherever the pack is used.
func (c Credit) String() string {
	if c.Handle == "" {
		return c.DisplayName
	}
	return fmt.Sprintf("%s (@%s)", c.DisplayName, c.Handle)
}

// CharacterInfo carries optional character details.
type CharacterInfo struct {
	// FromSeries is the series the character comes from, if any.
	FromSeries string `json:"from_series,omitempty"`
	// TriggerWord is the token that activates the LoRA.
	TriggerWord string `json:"trigger_word,omitempty"`
	// BaseModel is the model id the LoRA was trained for (e.g. "illustrious-xl").
	BaseModel string `json:"base_model,omitempty"`
}

// SeriesListing points to a series hosted on the creator's own server.
type SeriesListing struct {
	URL       string   `json:"url"`
	Languages []string `json:"languages,omitempty"`
	Status    string   `json:"status,omitempty"` // ongoing | complete | hiatus
	Chapters  int      `json:"chapters,omitempty"`
}

// PluginRef names the plugin manifest inside a plugin pack.
type PluginRef struct {
	Manifest string `json:"manifest"`
}

// File describes one payload file.
type File struct {
	Path      string `json:"path"`
	Role      Role   `json:"role"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
}

// Manifest is the content of manifest.json. Members whose names start with
// "x-" are kept in Extensions and survive a read/write round trip; any other
// unknown member is an error.
type Manifest struct {
	Format        int            `json:"mvpack"`
	Kind          Kind           `json:"kind"`
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Version       string         `json:"version"`
	Summary       string         `json:"summary,omitempty"`
	Description   string         `json:"description,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	Licence       string         `json:"licence"`
	LicenceNotes  string         `json:"licence_notes,omitempty"`
	AILabel       AILabel        `json:"ai_label"`
	ContentRating string         `json:"content_rating,omitempty"`
	Credit        Credit         `json:"credit"`
	Tags          []string       `json:"tags,omitempty"`
	WorksWith     []string       `json:"works_with,omitempty"`
	Character     *CharacterInfo `json:"character,omitempty"`
	SeriesListing *SeriesListing `json:"series_listing,omitempty"`
	Plugin        *PluginRef     `json:"plugin,omitempty"`
	Files         []File         `json:"files"`

	// Extensions holds "x-" members verbatim.
	Extensions map[string]json.RawMessage `json:"-"`
}

type manifestAlias Manifest

// knownMembers are the top-level member names of the manifest.
var knownMembers = map[string]bool{
	"mvpack": true, "kind": true, "id": true, "name": true, "version": true, "summary": true, "description": true,
	"created_at": true, "licence": true, "licence_notes": true, "ai_label": true, "content_rating": true,
	"credit": true, "tags": true, "works_with": true, "character": true, "series_listing": true, "plugin": true,
	"files": true,
}

// MarshalJSON implements json.Marshaler, merging Extensions into the object.
func (m Manifest) MarshalJSON() ([]byte, error) {
	a := manifestAlias(m)
	a.CreatedAt = a.CreatedAt.UTC()
	if a.Files == nil {
		a.Files = []File{}
	}
	raw, err := json.Marshal(a)
	if err != nil || len(m.Extensions) == 0 {
		return raw, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	for k, v := range m.Extensions {
		if !strings.HasPrefix(k, "x-") {
			return nil, fmt.Errorf("mvpack: extension member %q must start with \"x-\"", k)
		}
		obj[k] = v
	}
	return json.Marshal(obj)
}

// UnmarshalJSON implements json.Unmarshaler. It rejects unknown members that do
// not start with "x-", and unknown members inside nested objects.
func (m *Manifest) UnmarshalJSON(data []byte) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	ext := map[string]json.RawMessage{}
	known := map[string]json.RawMessage{}
	for k, v := range obj {
		switch {
		case knownMembers[k]:
			known[k] = v
		case strings.HasPrefix(k, "x-"):
			ext[k] = v
		default:
			return fmt.Errorf("unknown member %q", k)
		}
	}
	strict, err := json.Marshal(known)
	if err != nil {
		return err
	}
	var a manifestAlias
	dec := json.NewDecoder(bytes.NewReader(strict))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return err
	}
	*m = Manifest(a)
	if len(ext) > 0 {
		m.Extensions = ext
	}
	return nil
}

// ParseManifest decodes and validates manifest.json bytes. It rejects duplicate
// members and invalid UTF-8 (by canonicalizing first) and returns a
// *ValidationError listing every broken rule.
func ParseManifest(data []byte) (*Manifest, error) {
	if _, err := Canonicalize(data); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if err := CheckMemberNames(data, "x-"); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, &ValidationError{Problems: []Problem{{Field: "", Message: err.Error()}}}
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Licence describes a licence id accepted in manifests.
type Licence struct {
	ID string
	// Name is the human-readable name, e.g. "CC BY 4.0".
	Name string
	// Credit is true when users must credit the creator.
	Credit bool
	// NonCommercial is true when commercial use is not allowed.
	NonCommercial bool
	// ShareAlike is true when derivatives must use the same licence.
	ShareAlike bool
	// NoDerivatives is true when adapted material may not be shared.
	NoDerivatives bool
	// Redistributable is true when the pack may be passed on to others.
	Redistributable bool
}

// Licences lists every accepted licence id. "personal" means the owner may use
// the item in their own works but not redistribute the pack; it is the usual
// licence of paid items. "all-rights-reserved" is used for series listings and
// items the creator shares for viewing only.
var Licences = []Licence{
	{ID: "CC-BY-4.0", Name: "CC BY 4.0", Credit: true, Redistributable: true},
	{ID: "CC-BY-SA-4.0", Name: "CC BY-SA 4.0", Credit: true, ShareAlike: true, Redistributable: true},
	{ID: "CC-BY-NC-4.0", Name: "CC BY-NC 4.0", Credit: true, NonCommercial: true, Redistributable: true},
	{ID: "CC-BY-NC-SA-4.0", Name: "CC BY-NC-SA 4.0", Credit: true, NonCommercial: true, ShareAlike: true, Redistributable: true},
	{ID: "CC-BY-ND-4.0", Name: "CC BY-ND 4.0", Credit: true, NoDerivatives: true, Redistributable: true},
	{ID: "CC0-1.0", Name: "CC0 1.0", Redistributable: true},
	{ID: "MIT", Name: "MIT", Credit: true, Redistributable: true},
	{ID: "Apache-2.0", Name: "Apache 2.0", Credit: true, Redistributable: true},
	{ID: "OFL-1.1", Name: "SIL Open Font License 1.1", Credit: true, Redistributable: true},
	{ID: "personal", Name: "Personal licence", Credit: true},
	{ID: "all-rights-reserved", Name: "All rights reserved", Credit: true, NoDerivatives: true},
}

// LookupLicence returns the licence with the given id.
func LookupLicence(id string) (Licence, bool) {
	for _, l := range Licences {
		if l.ID == id {
			return l, true
		}
	}
	return Licence{}, false
}

var (
	reID      = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)
	reSemver  = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
	reHandle  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,28}[a-z0-9]$`)
	reTag     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	reModelID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	reSHA256  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	reLang    = regexp.MustCompile(`^[a-z]{2,3}(?:-[A-Za-z0-9]{2,8})*$`)
	reItemID  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// ValidHandle reports whether h follows the World handle rules: 3–30 characters
// of lowercase a–z, 0–9 and '-', starting and ending with a letter or digit and
// without "--". Uniqueness and reserved names are checked by World, not here.
func ValidHandle(h string) bool {
	return reHandle.MatchString(h) && !strings.Contains(h, "--")
}

// ValidDisplayName reports whether s is an acceptable World display name:
// 1–40 characters after trimming, no control or format characters.
func ValidDisplayName(s string) bool {
	if s != strings.TrimSpace(s) || !utf8.ValidString(s) {
		return false
	}
	n := utf8.RuneCountInString(s)
	if n < 1 || n > 40 {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

func textOK(s string, maxRunes int) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxRunes {
		return false
	}
	for _, r := range s {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// Validate checks every rule of SPEC.md that can be checked from the manifest
// alone. It returns a *ValidationError or nil.
func (m *Manifest) Validate() error {
	v := &ValidationError{}
	if m.Format != FormatVersion {
		v.add("mvpack", "must be %d", FormatVersion)
	}
	if !slices.Contains(Kinds, m.Kind) {
		v.add("kind", "must be one of %v", Kinds)
	}
	if !reID.MatchString(m.ID) {
		v.add("id", "must be 1–64 lowercase letters, digits and '-'")
	}
	if strings.TrimSpace(m.Name) == "" || !textOK(m.Name, 80) || strings.ContainsAny(m.Name, "\n\t") {
		v.add("name", "must be 1–80 characters on one line")
	}
	if !reSemver.MatchString(m.Version) || len(m.Version) > 64 {
		v.add("version", "must be a semantic version like 1.2.0")
	}
	if !textOK(m.Summary, 280) {
		v.add("summary", "must be at most 280 characters")
	}
	if !textOK(m.Description, 4000) {
		v.add("description", "must be at most 4000 characters")
	}
	if m.CreatedAt.IsZero() {
		v.add("created_at", "is required (RFC 3339, UTC)")
	}
	lic, ok := LookupLicence(m.Licence)
	if !ok {
		v.add("licence", "unknown licence id %q", m.Licence)
	}
	if !textOK(m.LicenceNotes, 500) {
		v.add("licence_notes", "must be at most 500 characters")
	}
	validateAILabel(v, m.AILabel)
	switch m.ContentRating {
	case "", RatingAll, RatingTeen, RatingMature:
	default:
		v.add("content_rating", "must be all-ages, teen or mature")
	}
	if !ValidDisplayName(m.Credit.DisplayName) {
		v.add("credit.display_name", "must be 1–40 characters without leading or trailing spaces")
	}
	if !ValidHandle(m.Credit.Handle) {
		v.add("credit.handle", "must be 3–30 characters of a–z, 0–9 and '-'")
	}
	if m.Credit.WorldItemID != "" && !reItemID.MatchString(m.Credit.WorldItemID) {
		v.add("credit.world_item_id", "must be 1–64 characters of A–Z, a–z, 0–9, '_' and '-'")
	}
	if len(m.Tags) > 20 {
		v.add("tags", "at most 20 tags")
	}
	for i, t := range m.Tags {
		if !reTag.MatchString(t) {
			v.add(fmt.Sprintf("tags[%d]", i), "must be 1–32 lowercase letters, digits and '-'")
		}
	}
	if hasDup(m.Tags) {
		v.add("tags", "must be unique")
	}
	if len(m.WorksWith) > 20 {
		v.add("works_with", "at most 20 model ids")
	}
	for i, id := range m.WorksWith {
		if !reModelID.MatchString(id) {
			v.add(fmt.Sprintf("works_with[%d]", i), "must be a model id (lowercase, digits, '.', '_' and '-')")
		}
	}
	if hasDup(m.WorksWith) {
		v.add("works_with", "must be unique")
	}
	m.validateKindBlocks(v)
	m.validateFiles(v, lic)
	return v.orNil()
}

func validateAILabel(v *ValidationError, a AILabel) {
	set := 0
	for field, val := range map[string]string{"art": a.Art, "design": a.Design, "script": a.Script, "lettering": a.Lettering} {
		switch val {
		case "":
		case AIHuman, AIAssisted, AIGenerated:
			set++
		default:
			v.add("ai_label."+field, "must be human, ai-assisted or ai")
		}
	}
	if set == 0 {
		v.add("ai_label", "at least one role (art, design, script, lettering) must be labelled")
	}
}

func (m *Manifest) validateKindBlocks(v *ValidationError) {
	if m.Character != nil {
		if m.Kind != KindCharacter {
			v.add("character", "only allowed when kind is character")
		}
		if !textOK(m.Character.FromSeries, 80) || !textOK(m.Character.TriggerWord, 64) {
			v.add("character", "from_series ≤ 80 and trigger_word ≤ 64 characters")
		}
		if m.Character.BaseModel != "" && !reModelID.MatchString(m.Character.BaseModel) {
			v.add("character.base_model", "must be a model id")
		}
	}
	if m.Kind == KindSeriesListing {
		if m.SeriesListing == nil {
			v.add("series_listing", "is required when kind is series-listing")
		} else {
			u, err := url.Parse(m.SeriesListing.URL)
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || len(m.SeriesListing.URL) > 500 {
				v.add("series_listing.url", "must be an http(s) URL of at most 500 characters")
			}
			for i, l := range m.SeriesListing.Languages {
				if !reLang.MatchString(l) {
					v.add(fmt.Sprintf("series_listing.languages[%d]", i), "must be a BCP 47 language tag")
				}
			}
			switch m.SeriesListing.Status {
			case "", "ongoing", "complete", "hiatus":
			default:
				v.add("series_listing.status", "must be ongoing, complete or hiatus")
			}
			if m.SeriesListing.Chapters < 0 {
				v.add("series_listing.chapters", "must not be negative")
			}
		}
	} else if m.SeriesListing != nil {
		v.add("series_listing", "only allowed when kind is series-listing")
	}
	if m.Kind == KindPlugin {
		if m.Plugin == nil || m.Plugin.Manifest == "" {
			v.add("plugin.manifest", "is required when kind is plugin")
		}
	} else if m.Plugin != nil {
		v.add("plugin", "only allowed when kind is plugin")
	}
}

func (m *Manifest) validateFiles(v *ValidationError, lic Licence) {
	if len(m.Files) > DefaultLimits().MaxFiles {
		v.add("files", "at most %d files", DefaultLimits().MaxFiles)
	}
	seen := map[string]bool{}
	roles := map[Role]int{}
	for i, f := range m.Files {
		field := fmt.Sprintf("files[%d]", i)
		if err := CheckPath(f.Path); err != nil {
			v.add(field+".path", "%v", err)
			continue
		}
		low := strings.ToLower(f.Path)
		if seen[low] {
			v.add(field+".path", "duplicate path (paths are compared case-insensitively)")
		}
		seen[low] = true
		t, ok := FileTypeOf(f.Path)
		if !ok {
			v.add(field+".path", "file type not allowed (png, jpg, jpeg, webp, safetensors, json; fonts in plugin packs)")
			continue
		}
		if t.IsFont() && m.Kind != KindPlugin {
			v.add(field+".path", "fonts are only allowed in plugin packs")
		}
		if t == TypeSafetensors && m.Kind == KindSeriesListing {
			v.add(field+".path", "series listings cannot carry model weights")
		}
		if !slices.Contains(Roles, f.Role) {
			v.add(field+".role", "unknown role %q", f.Role)
		} else if !RoleAccepts(f.Role, t) {
			v.add(field+".role", "role %s does not accept %s files", f.Role, t)
		}
		roles[f.Role]++
		if f.MediaType != t.MediaType() {
			v.add(field+".media_type", "must be %s", t.MediaType())
		}
		if f.Size < 0 {
			v.add(field+".size", "must not be negative")
		}
		if !reSHA256.MatchString(f.SHA256) {
			v.add(field+".sha256", "must be 64 lowercase hex digits")
		}
	}
	if roles[RoleCover] > 1 {
		v.add("files", "at most one cover")
	}
	switch m.Kind {
	case KindCharacter:
		if roles[RoleReference] == 0 {
			v.add("files", "a character pack needs at least one reference image")
		}
	case KindPlugin:
		if m.Plugin != nil && m.Plugin.Manifest != "" {
			if f, ok := m.File(m.Plugin.Manifest); !ok || f.Role != RolePluginManifest {
				v.add("plugin.manifest", "must name a listed file with role plugin-manifest")
			}
		}
		if lic.ID == "personal" || lic.ID == "all-rights-reserved" {
			v.add("licence", "plugins must use an open licence")
		}
	}
}

// File returns the file entry with the given path.
func (m *Manifest) File(p string) (File, bool) {
	for _, f := range m.Files {
		if f.Path == p {
			return f, true
		}
	}
	return File{}, false
}

// TotalSize is the sum of the declared payload sizes.
func (m *Manifest) TotalSize() int64 {
	var n int64
	for _, f := range m.Files {
		n += f.Size
	}
	return n
}

// CanonicalBytes returns the RFC 8785 canonical form of the manifest.
func (m *Manifest) CanonicalBytes() ([]byte, error) { return CanonicalMarshal(m) }

func hasDup(s []string) bool {
	seen := map[string]bool{}
	for _, x := range s {
		if seen[x] {
			return true
		}
		seen[x] = true
	}
	return false
}

func hexSum(sum []byte) string { return hex.EncodeToString(sum) }
