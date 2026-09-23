// SPDX-License-Identifier: MIT
// Copyright (c) 2026 MangaVeyra contributors

// Package plugin defines the MangaVeyra plugin manifest (plugin.json): identity,
// kind, entry point, declared permissions with their risk level, the minimum
// app version and the public source release a build comes from.
//
// Version 1 of the app runs manifest-only plugins, which contribute model
// manifests, fonts, layout templates and themes without running any code.
// Plugins with a WebAssembly entry are specified by PLUGINS.md (ABI 2) and are
// accepted by this package so developers can prepare them, but the v1 app only
// lists them. The package depends only on the Go standard library and is MIT
// licensed (see sdk/LICENSE); import it with an alias if you also use the
// standard library's plugin package.
package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/sdk/mvpack"
)

// FormatVersion is the value of "mvplugin" this package reads and writes.
const FormatVersion = 1

// WasmABI is the WebAssembly ABI version described in PLUGINS.md.
const WasmABI = 2

// Kind is the plugin directory category.
type Kind string

// Plugin kinds (the filter chips of screen G7).
const (
	KindModelAdapter Kind = "model-adapter"
	KindExporter     Kind = "exporter"
	KindReader       Kind = "reader"
	KindLettering    Kind = "lettering"
	KindTheme        Kind = "theme"
)

// Kinds lists every plugin kind.
var Kinds = []Kind{KindModelAdapter, KindExporter, KindReader, KindLettering, KindTheme}

// Risk is how much harm a permission could do if a plugin misused it.
type Risk string

// Risk levels, shown next to every permission on install and review screens.
const (
	RiskLow    Risk = "low"
	RiskMedium Risk = "medium"
	RiskHigh   Risk = "high"
)

func (r Risk) rank() int {
	switch r {
	case RiskLow:
		return 1
	case RiskMedium:
		return 2
	case RiskHigh:
		return 3
	}
	return 0
}

// PermissionInfo describes one permission of the registry.
type PermissionInfo struct {
	ID   string
	Risk Risk
	// Label is the sentence shown to admins ("Play audio in the reader").
	Label string
	// Code is true when the permission only makes sense for plugins that run
	// code (WebAssembly entry); manifest-only plugins may not request it.
	Code bool
	// NeedsHosts is true when the permission must list the hosts it applies to.
	NeedsHosts bool
}

// Permissions is the registry of every permission a plugin can request. The
// risk a manifest declares must equal the registry's risk, so reviewers and
// admins always see the same level.
var Permissions = []PermissionInfo{
	{ID: "models.provide", Risk: RiskLow, Label: "Add model manifests to the model catalogue"},
	{ID: "lettering.fonts", Risk: RiskLow, Label: "Add fonts to the lettering tools"},
	{ID: "layout.templates", Risk: RiskLow, Label: "Add page layout templates"},
	{ID: "theme.tokens", Risk: RiskLow, Label: "Change theme colours and fonts"},
	{ID: "reader.audio", Risk: RiskLow, Label: "Play audio in the reader", Code: true},
	{ID: "reader.settings.read", Risk: RiskLow, Label: "Read reader settings", Code: true},
	{ID: "reader.ui", Risk: RiskLow, Label: "Add controls to the reader toolbar", Code: true},
	{ID: "export.formats", Risk: RiskMedium, Label: "Add an export format (receives the pages being exported)", Code: true},
	{ID: "import.formats", Risk: RiskMedium, Label: "Read files a person chooses to import", Code: true},
	{ID: "models.invoke", Risk: RiskMedium, Label: "Run models through the router (counts toward the compute meter)", Code: true},
	{ID: "project.read", Risk: RiskHigh, Label: "Read project content: scripts, pages and characters", Code: true},
	{ID: "project.write", Risk: RiskHigh, Label: "Change project content (scope and lock rules still apply)", Code: true},
	{ID: "files.read", Risk: RiskHigh, Label: "Read media files stored on this server", Code: true},
	{ID: "network.fetch", Risk: RiskHigh, Label: "Connect to the listed internet hosts (blocked in local-only mode)", Code: true, NeedsHosts: true},
	{ID: "secrets.use", Risk: RiskHigh, Label: "Use an API key the admin assigns to this plugin", Code: true},
}

// LookupPermission returns the registry entry for id.
func LookupPermission(id string) (PermissionInfo, bool) {
	for _, p := range Permissions {
		if p.ID == id {
			return p, true
		}
	}
	return PermissionInfo{}, false
}

// Licences accepted for plugins: open source licences for code, OFL for fonts,
// and CC0 / CC BY for pure data.
var Licences = []string{"MIT", "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "ISC", "MPL-2.0", "OFL-1.1", "CC0-1.0", "CC-BY-4.0"}

// Author credits a person who made the plugin.
type Author struct {
	Name   string `json:"name"`
	Handle string `json:"handle,omitempty"` // World handle
}

// Permission is one permission a plugin requests.
type Permission struct {
	ID     string   `json:"id"`
	Risk   Risk     `json:"risk"`
	Reason string   `json:"reason"`
	Hosts  []string `json:"hosts,omitempty"`
}

// Contributes lists the files a manifest-only plugin adds. Paths are relative
// to the plugin root (inside a .mvpack, relative to the pack root).
type Contributes struct {
	Models          []string `json:"models,omitempty"`
	Fonts           []string `json:"fonts,omitempty"`
	LayoutTemplates []string `json:"layout_templates,omitempty"`
	Themes          []string `json:"themes,omitempty"`
}

func (c *Contributes) count() int {
	return len(c.Models) + len(c.Fonts) + len(c.LayoutTemplates) + len(c.Themes)
}

// Entry type values.
const (
	EntryManifest = "manifest"
	EntryWasm     = "wasm"
)

// Entry says how the app loads the plugin.
type Entry struct {
	// Type is "manifest" (v1, no code) or "wasm" (ABI 2, planned).
	Type string `json:"type"`
	// Contributes is required for manifest entries.
	Contributes *Contributes `json:"contributes,omitempty"`
	// Module is the .wasm path for wasm entries.
	Module string `json:"module,omitempty"`
	// ABI is the WebAssembly ABI version for wasm entries (must be WasmABI).
	ABI int `json:"abi,omitempty"`
	// Hooks are the extension points a wasm plugin implements (see PLUGINS.md).
	Hooks []string `json:"hooks,omitempty"`
}

// Source identifies the public source release a build was made from.
type Source struct {
	Repo      string `json:"repo"`
	Tag       string `json:"tag"`
	Commit    string `json:"commit,omitempty"`
	SignedTag bool   `json:"signed_tag"`
}

// Manifest is the content of plugin.json.
type Manifest struct {
	Format        int          `json:"mvplugin"`
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Version       string       `json:"version"`
	Kind          Kind         `json:"kind"`
	Description   string       `json:"description,omitempty"`
	Licence       string       `json:"licence"`
	Authors       []Author     `json:"authors,omitempty"`
	Homepage      string       `json:"homepage,omitempty"`
	MinAppVersion string       `json:"min_app_version"`
	Entry         Entry        `json:"entry"`
	Permissions   []Permission `json:"permissions"`
	Source        Source       `json:"source"`
}

// Problem is one reason a plugin manifest failed validation.
type Problem struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ErrInvalidManifest is wrapped by every *ValidationError.
var ErrInvalidManifest = errors.New("plugin: invalid manifest")

// ValidationError lists every problem found in a plugin manifest.
type ValidationError struct{ Problems []Problem }

// Error implements error.
func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		parts[i] = p.Field + ": " + p.Message
	}
	return fmt.Sprintf("%v: %s", ErrInvalidManifest, strings.Join(parts, "; "))
}

// Unwrap lets errors.Is(err, ErrInvalidManifest) succeed.
func (e *ValidationError) Unwrap() error { return ErrInvalidManifest }

func (e *ValidationError) add(field, format string, args ...any) {
	e.Problems = append(e.Problems, Problem{Field: field, Message: fmt.Sprintf(format, args...)})
}

var (
	reID     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,28}[a-z0-9]\.[a-z0-9][a-z0-9-]{0,62}[a-z0-9]$`)
	reSemver = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)
	reHandle = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,28}[a-z0-9]$`)
	reHost   = regexp.MustCompile(`^(?:\*\.)?(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
	reHook   = regexp.MustCompile(`^[a-z]+(?:\.[a-z_]+)+$`)
	reTag    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)
	reCommit = regexp.MustCompile(`^[0-9a-f]{40}(?:[0-9a-f]{24})?$`)
	rePath   = regexp.MustCompile(`^(?:[A-Za-z0-9_-][A-Za-z0-9._-]{0,99}/){0,5}[A-Za-z0-9_-][A-Za-z0-9._-]{0,99}$`)
)

// Parse decodes plugin.json strictly (unknown members are errors) and validates it.
// Duplicate members, members that differ only in case (encoding/json would merge
// them) and invalid UTF-8 are rejected first, so every reader — the app, World's
// review tools, a plugin author's editor — sees the same permissions.
func Parse(data []byte) (*Manifest, error) {
	if _, err := mvpack.Canonicalize(data); err != nil {
		return nil, &ValidationError{Problems: []Problem{{Field: "", Message: err.Error()}}}
	}
	if err := mvpack.CheckMemberNames(data, ""); err != nil {
		return nil, &ValidationError{Problems: []Problem{{Field: "", Message: err.Error()}}}
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, &ValidationError{Problems: []Problem{{Field: "", Message: err.Error()}}}
	}
	if dec.More() {
		return nil, &ValidationError{Problems: []Problem{{Field: "", Message: "trailing data after the manifest"}}}
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks the manifest against the rules of PLUGINS.md.
func (m *Manifest) Validate() error {
	v := &ValidationError{}
	if m.Format != FormatVersion {
		v.add("mvplugin", "must be %d", FormatVersion)
	}
	if !reID.MatchString(m.ID) || strings.Contains(m.ID, "--") {
		v.add("id", "must be <world handle>.<name>, e.g. kestrel.page-flip-sounds")
	}
	if strings.TrimSpace(m.Name) == "" || utf8.RuneCountInString(m.Name) > 60 {
		v.add("name", "must be 1–60 characters")
	}
	if !reSemver.MatchString(m.Version) {
		v.add("version", "must be a semantic version")
	}
	if !slices.Contains(Kinds, m.Kind) {
		v.add("kind", "must be one of %v", Kinds)
	}
	if utf8.RuneCountInString(m.Description) > 500 {
		v.add("description", "at most 500 characters")
	}
	if !slices.Contains(Licences, m.Licence) {
		v.add("licence", "must be one of %v", Licences)
	}
	for i, a := range m.Authors {
		if strings.TrimSpace(a.Name) == "" || utf8.RuneCountInString(a.Name) > 40 {
			v.add(fmt.Sprintf("authors[%d].name", i), "must be 1–40 characters")
		}
		if a.Handle != "" && (!reHandle.MatchString(a.Handle) || strings.Contains(a.Handle, "--")) {
			v.add(fmt.Sprintf("authors[%d].handle", i), "must be a World handle")
		}
	}
	if m.Homepage != "" && !httpsURL(m.Homepage) {
		v.add("homepage", "must be an https URL")
	}
	if !reSemver.MatchString(m.MinAppVersion) {
		v.add("min_app_version", "must be a semantic version")
	}
	m.validateEntry(v)
	m.validatePermissions(v)
	if !httpsURL(m.Source.Repo) {
		v.add("source.repo", "must be the https URL of a public repository")
	}
	if !reTag.MatchString(m.Source.Tag) {
		v.add("source.tag", "must name the release tag")
	}
	if m.Source.Commit != "" && !reCommit.MatchString(m.Source.Commit) {
		v.add("source.commit", "must be a full lowercase commit hash")
	}
	if len(v.Problems) > 0 {
		return v
	}
	return nil
}

func (m *Manifest) validateEntry(v *ValidationError) {
	e := m.Entry
	switch e.Type {
	case EntryManifest:
		if e.Module != "" || e.ABI != 0 || len(e.Hooks) > 0 {
			v.add("entry", "manifest entries cannot declare module, abi or hooks")
		}
		if e.Contributes == nil || e.Contributes.count() == 0 {
			v.add("entry.contributes", "must list at least one model, font, layout template or theme")
			return
		}
		check := func(field string, paths []string, exts ...string) {
			for i, p := range paths {
				if !rePath.MatchString(p) || !slices.Contains(exts, path.Ext(p)) {
					v.add(fmt.Sprintf("entry.contributes.%s[%d]", field, i), "must be a relative path ending in %s", strings.Join(exts, ", "))
				}
			}
		}
		check("models", e.Contributes.Models, ".json")
		check("fonts", e.Contributes.Fonts, ".ttf", ".otf", ".woff2")
		check("layout_templates", e.Contributes.LayoutTemplates, ".json")
		check("themes", e.Contributes.Themes, ".json")
	case EntryWasm:
		if e.Contributes != nil {
			v.add("entry.contributes", "only allowed for manifest entries")
		}
		if !rePath.MatchString(e.Module) || path.Ext(e.Module) != ".wasm" {
			v.add("entry.module", "must be a relative path ending in .wasm")
		}
		if e.ABI != WasmABI {
			v.add("entry.abi", "must be %d", WasmABI)
		}
		if len(e.Hooks) == 0 {
			v.add("entry.hooks", "must list at least one hook")
		}
		for i, h := range e.Hooks {
			if !reHook.MatchString(h) {
				v.add(fmt.Sprintf("entry.hooks[%d]", i), "must look like reader.on_page_turn")
			}
		}
	default:
		v.add("entry.type", "must be manifest or wasm")
	}
}

func (m *Manifest) validatePermissions(v *ValidationError) {
	if m.Permissions == nil {
		v.add("permissions", "is required (use [] for none)")
	}
	seen := map[string]bool{}
	for i, p := range m.Permissions {
		field := fmt.Sprintf("permissions[%d]", i)
		info, ok := LookupPermission(p.ID)
		if !ok {
			v.add(field+".id", "unknown permission %q", p.ID)
			continue
		}
		if seen[p.ID] {
			v.add(field+".id", "requested twice")
		}
		seen[p.ID] = true
		if p.Risk != info.Risk {
			v.add(field+".risk", "%s is %s risk", p.ID, info.Risk)
		}
		if n := utf8.RuneCountInString(strings.TrimSpace(p.Reason)); n < 3 || n > 200 {
			v.add(field+".reason", "explain in 3–200 characters why the plugin needs it")
		}
		if info.Code && m.Entry.Type != EntryWasm {
			v.add(field+".id", "%s needs code; manifest-only plugins cannot request it", p.ID)
		}
		if info.NeedsHosts {
			if len(p.Hosts) == 0 || len(p.Hosts) > 10 {
				v.add(field+".hosts", "list 1–10 hosts")
			}
			for j, h := range p.Hosts {
				if !reHost.MatchString(h) {
					v.add(fmt.Sprintf("%s.hosts[%d]", field, j), "must be a lowercase host name")
				}
			}
		} else if len(p.Hosts) > 0 {
			v.add(field+".hosts", "only allowed for %s", "network.fetch")
		}
	}
}

// MaxRisk returns the highest risk among the requested permissions, or "" when
// the plugin requests none.
func (m *Manifest) MaxRisk() Risk {
	var max Risk
	for _, p := range m.Permissions {
		if info, ok := LookupPermission(p.ID); ok && info.Risk.rank() > max.rank() {
			max = info.Risk
		}
	}
	return max
}

// RunsCode reports whether the plugin has a WebAssembly entry.
func (m *Manifest) RunsCode() bool { return m.Entry.Type == EntryWasm }

// CompatibleWith reports whether the plugin can run on the given app version.
func (m *Manifest) CompatibleWith(appVersion string) bool {
	c, err := CompareVersions(appVersion, m.MinAppVersion)
	return err == nil && c >= 0
}

// PermissionDiff compares the permissions of two versions of a plugin, as
// shown on the review screen: added and removed permission ids.
func PermissionDiff(old, new *Manifest) (added, removed []string) {
	has := func(m *Manifest, id string) bool {
		return slices.ContainsFunc(m.Permissions, func(p Permission) bool { return p.ID == id })
	}
	for _, p := range new.Permissions {
		if !has(old, p.ID) {
			added = append(added, p.ID)
		}
	}
	for _, p := range old.Permissions {
		if !has(new, p.ID) {
			removed = append(removed, p.ID)
		}
	}
	return added, removed
}

// CompareVersions compares two semantic versions by precedence (SemVer 2.0 §11):
// -1 when a < b, 0 when equal, +1 when a > b. Build metadata is ignored.
func CompareVersions(a, b string) (int, error) {
	pa, err := parseSemver(a)
	if err != nil {
		return 0, err
	}
	pb, err := parseSemver(b)
	if err != nil {
		return 0, err
	}
	for i := range 3 {
		if c := cmpInt(pa.nums[i], pb.nums[i]); c != 0 {
			return c, nil
		}
	}
	switch {
	case pa.pre == "" && pb.pre == "":
		return 0, nil
	case pa.pre == "":
		return 1, nil
	case pb.pre == "":
		return -1, nil
	}
	ai, bi := strings.Split(pa.pre, "."), strings.Split(pb.pre, ".")
	for i := 0; i < len(ai) && i < len(bi); i++ {
		na, errA := strconv.ParseUint(ai[i], 10, 64)
		nb, errB := strconv.ParseUint(bi[i], 10, 64)
		var c int
		switch {
		case errA == nil && errB == nil:
			c = cmpInt(na, nb)
		case errA == nil:
			c = -1
		case errB == nil:
			c = 1
		default:
			c = strings.Compare(ai[i], bi[i])
		}
		if c != 0 {
			return c, nil
		}
	}
	return cmpInt(uint64(len(ai)), uint64(len(bi))), nil
}

type semver struct {
	nums [3]uint64
	pre  string
}

func parseSemver(s string) (semver, error) {
	m := reSemver.FindStringSubmatch(s)
	if m == nil {
		return semver{}, fmt.Errorf("plugin: %q is not a semantic version", s)
	}
	var v semver
	for i := range 3 {
		n, err := strconv.ParseUint(m[i+1], 10, 64)
		if err != nil {
			return semver{}, fmt.Errorf("plugin: %q: %v", s, err)
		}
		v.nums[i] = n
	}
	v.pre = m[4]
	return v, nil
}

func cmpInt(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func httpsURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && len(s) <= 300
}
