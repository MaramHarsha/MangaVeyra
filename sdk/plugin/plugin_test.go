// SPDX-License-Identifier: MIT
// Copyright (c) 2026 MangaVeyra contributors

package plugin

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func load(t *testing.T, file string) []byte {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestExampleManifests(t *testing.T) {
	valid, _ := filepath.Glob("testdata/valid/*.json")
	invalid, _ := filepath.Glob("testdata/invalid/*.json")
	if len(valid) == 0 || len(invalid) == 0 {
		t.Fatal("missing testdata")
	}
	for _, f := range valid {
		if _, err := Parse(load(t, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	for _, f := range invalid {
		if _, err := Parse(load(t, f)); !errors.Is(err, ErrInvalidManifest) {
			t.Errorf("%s: want ErrInvalidManifest, got %v", f, err)
		}
	}
}

func TestMvpackGoldenPluginManifestIsValid(t *testing.T) {
	// The plugin.json inside sdk/mvpack's golden plugin pack must satisfy this package.
	b := []byte(`{"mvplugin":1,"id":"kestrel.harbour-ghosts-theme","name":"Harbour Ghosts theme","version":"0.2.0","kind":"theme",` +
		`"licence":"MIT","min_app_version":"1.0.0","entry":{"type":"manifest","contributes":{"themes":["themes/harbour-ghosts.json"]}},` +
		`"permissions":[],"source":{"repo":"https://example.org/kestrel/harbour-ghosts-theme","tag":"v0.2.0","signed_tag":true}}`)
	if _, err := Parse(b); err != nil {
		t.Fatal(err)
	}
}

func TestRiskAndDiff(t *testing.T) {
	old, err := Parse(load(t, "testdata/valid/ferry-schedule-sync.json"))
	if err != nil {
		t.Fatal(err)
	}
	if old.MaxRisk() != RiskHigh || !old.RunsCode() {
		t.Fatalf("max risk %q, runs code %v", old.MaxRisk(), old.RunsCode())
	}
	next := *old
	next.Permissions = []Permission{old.Permissions[0], {ID: "reader.ui", Risk: RiskLow, Reason: "Adds a button."}}
	added, removed := PermissionDiff(old, &next)
	if !slices.Equal(added, []string{"reader.ui"}) || !slices.Equal(removed, []string{"network.fetch"}) {
		t.Fatalf("diff added=%v removed=%v", added, removed)
	}
	if next.MaxRisk() != RiskMedium {
		t.Fatalf("max risk after removing network: %q", next.MaxRisk())
	}
	fonts, _ := Parse(load(t, "testdata/valid/hand-lettering-fonts.json"))
	if fonts.MaxRisk() != RiskLow || fonts.RunsCode() {
		t.Fatalf("fonts plugin: risk %q code %v", fonts.MaxRisk(), fonts.RunsCode())
	}
}

func TestCompatibleWith(t *testing.T) {
	m, _ := Parse(load(t, "testdata/valid/page-flip-sounds.json")) // min 2.0.0
	for v, want := range map[string]bool{"1.0.0": false, "1.9.9": false, "2.0.0-rc.1": false, "2.0.0": true, "2.1.0": true, "junk": false} {
		if got := m.CompatibleWith(v); got != want {
			t.Errorf("CompatibleWith(%s) = %v, want %v", v, got, want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	// SemVer 2.0 §11 precedence example, in ascending order.
	order := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.10.0", "2.0.0+build.5"}
	for i := range order {
		for j := range order {
			c, err := CompareVersions(order[i], order[j])
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if c != want {
				t.Errorf("CompareVersions(%s, %s) = %d, want %d", order[i], order[j], c, want)
			}
		}
	}
	if _, err := CompareVersions("1.0", "1.0.0"); err == nil {
		t.Error("want error for invalid version")
	}
}

func TestPermissionRegistryIsConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Permissions {
		if seen[p.ID] || p.Label == "" || p.Risk.rank() == 0 {
			t.Errorf("bad registry entry %+v", p)
		}
		seen[p.ID] = true
		if p.NeedsHosts && !p.Code {
			t.Errorf("%s needs hosts but is not a code permission", p.ID)
		}
	}
}

// Duplicate or case-variant members would let World's review tools and the app
// read different permissions from the same signed plugin.json.
func TestParseRejectsAmbiguousMembers(t *testing.T) {
	good := load(t, "testdata/valid/page-flip-sounds.json")
	if _, err := Parse(good); err != nil {
		t.Fatal(err)
	}
	dup := bytes.Replace(good, []byte(`"permissions": [`), []byte(`"permissions": [], "permissions": [`), 1)
	variant := bytes.Replace(good, []byte(`"permissions": [`), []byte(`"Permissions": [], "permissions": [`), 1)
	for name, doc := range map[string][]byte{"duplicate": dup, "case variant": variant} {
		if bytes.Equal(doc, good) {
			t.Fatalf("%s: fixture changed", name)
		}
		if _, err := Parse(doc); !errors.Is(err, ErrInvalidManifest) {
			t.Errorf("%s: want ErrInvalidManifest, got %v", name, err)
		}
	}
}
