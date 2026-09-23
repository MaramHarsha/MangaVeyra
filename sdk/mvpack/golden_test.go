// SPDX-License-Identifier: MIT
// Copyright (c) 2026 MangaVeyra contributors

package mvpack

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Run `go test ./sdk/mvpack -run Golden -update` after an intentional format change.
var update = flag.Bool("update", false, "rewrite testdata golden files")

const goldenKeyID = "srv_testdata"

// goldenPacks builds the example packs kept in testdata. Everything is
// deterministic (fixed key, fixed time), so the bytes are stable.
func goldenPacks(t *testing.T) map[string][]byte {
	signer := testSigner("server", goldenKeyID, SignerServer)
	out := map[string][]byte{"rin-asagiri.mvpack": buildCharacterPack(t, signer)}

	build := func(name string, m Manifest, add func(w *Writer) error) {
		var buf bytes.Buffer
		w, err := NewWriter(&buf, m)
		if err != nil {
			t.Fatal(err)
		}
		if err := add(w); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := w.Close(signer); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out[name] = buf.Bytes()
	}

	layout := baseManifest(KindLayout)
	layout.ID, layout.Name, layout.Version = "night-market-layout", "Night Market layout", "1.0.0"
	layout.Summary, layout.Licence = "Five-panel night market spread with a wide establishing shot.", "CC-BY-4.0"
	layout.AILabel = AILabel{Design: AIHuman}
	layout.Credit = Credit{DisplayName: "lin.park", Handle: "lin-park", WorldItemID: "wi_night_market"}
	layout.Tags, layout.WorksWith = []string{"layout", "night"}, nil
	build("night-market-layout.mvpack", layout, func(w *Writer) error {
		if err := w.AddBytes("cover.png", RoleCover, pngBytes(t, 90)); err != nil {
			return err
		}
		return w.AddBytes("layout.json", RoleLayout, []byte(`{"page":{"w":1600,"h":2400},"panels":[`+
			`{"order":1,"points":[[0,0],[1600,0],[1600,900],[0,760]]},`+
			`{"order":2,"points":[[0,780],[800,900],[800,1650],[0,1650]]},`+
			`{"order":3,"points":[[820,905],[1600,920],[1600,1650],[820,1650]]},`+
			`{"order":4,"points":[[0,1670],[1000,1670],[1000,2400],[0,2400]]},`+
			`{"order":5,"points":[[1020,1670],[1600,1670],[1600,2400],[1020,2400]]}]}`))
	})

	series := baseManifest(KindSeriesListing)
	series.ID, series.Name, series.Version = "tidebreak-courier", "Tidebreak Courier", "1.0.0"
	series.Summary, series.Licence = "A courier races the tide across a drowned harbour city.", "all-rights-reserved"
	series.AILabel = AILabel{Script: AIHuman, Art: AIAssisted, Lettering: AIHuman}
	series.ContentRating = RatingTeen
	series.SeriesListing = &SeriesListing{URL: "https://studio.local/series/tidebreak-courier", Languages: []string{"en", "ja"}, Status: "ongoing", Chapters: 12}
	series.WorksWith = nil
	build("tidebreak-courier.mvpack", series, func(w *Writer) error {
		return w.AddBytes("cover.png", RoleCover, pngBytes(t, 30))
	})

	plugin := baseManifest(KindPlugin)
	plugin.ID, plugin.Name, plugin.Version = "harbour-ghosts-theme", "Harbour Ghosts theme", "0.2.0"
	plugin.Summary, plugin.Licence = "A misty teal reader theme.", "MIT"
	plugin.AILabel = AILabel{Design: AIHuman}
	plugin.Credit = Credit{DisplayName: "kestrel", Handle: "kestrel"}
	plugin.Plugin = &PluginRef{Manifest: "plugin.json"}
	plugin.Tags, plugin.WorksWith = []string{"theme"}, nil
	build("harbour-ghosts-theme.mvpack", plugin, func(w *Writer) error {
		if err := w.AddBytes("plugin.json", RolePluginManifest, []byte(`{"mvplugin":1,"id":"kestrel.harbour-ghosts-theme",`+
			`"name":"Harbour Ghosts theme","version":"0.2.0","kind":"theme","licence":"MIT","min_app_version":"1.0.0",`+
			`"entry":{"type":"manifest","contributes":{"themes":["themes/harbour-ghosts.json"]}},"permissions":[],`+
			`"source":{"repo":"https://example.org/kestrel/harbour-ghosts-theme","tag":"v0.2.0","signed_tag":true}}`)); err != nil {
			return err
		}
		return w.AddBytes("themes/harbour-ghosts.json", RoleTheme, []byte(`{"name":"Harbour Ghosts","tokens":{"--mv-bg":"#061417","--mv-accent":"#3DB8B0"}}`))
	})

	style := baseManifest(KindStyle)
	style.ID, style.Name, style.Version = "salt-ink", "Salt ink", "1.0.0"
	style.Summary, style.Licence = "Heavy brush ink with coarse screentone.", "CC-BY-SA-4.0"
	style.AILabel = AILabel{Art: AIHuman}
	style.Extensions = map[string]json.RawMessage{"x-studio": json.RawMessage(`{"exported_from":"studio.local"}`)}
	style.Character = nil
	build("salt-ink.mvpack", style, func(w *Writer) error {
		if err := w.AddBytes("preview/sample.png", RolePreview, pngBytes(t, 5)); err != nil {
			return err
		}
		return w.AddBytes("style.json", RoleStylePreset, []byte(`{"line_weight":1.4,"screentone":"coarse-dot","finish":"bw_screentone"}`))
	})
	return out
}

// invalidManifests mutates the golden character manifest; each result must be
// rejected by ParseManifest and by schema/manifest.schema.json.
var invalidManifests = map[string]func(m map[string]any){
	"bad-handle-uppercase":     func(m map[string]any) { m["credit"].(map[string]any)["handle"] = "Jerry-Ink" },
	"bad-handle-double-hyphen": func(m map[string]any) { m["credit"].(map[string]any)["handle"] = "jerry--ink" },
	"bad-handle-too-short":     func(m map[string]any) { m["credit"].(map[string]any)["handle"] = "jo" },
	"display-name-too-long":    func(m map[string]any) { m["credit"].(map[string]any)["display_name"] = strings.Repeat("x", 41) },
	"unknown-kind":             func(m map[string]any) { m["kind"] = "font-pack" },
	"unknown-member-price":     func(m map[string]any) { m["price"] = 5 },
	"unknown-licence":          func(m map[string]any) { m["licence"] = "WTFPL" },
	"missing-licence":          func(m map[string]any) { delete(m, "licence") },
	"empty-ai-label":           func(m map[string]any) { m["ai_label"] = map[string]any{} },
	"bad-ai-value":             func(m map[string]any) { m["ai_label"] = map[string]any{"art": "mostly-human"} },
	"wrong-format-version":     func(m map[string]any) { m["mvpack"] = 2 },
	"executable-file":          func(m map[string]any) { setFirstFile(m, "path", "tools/setup.exe") },
	"path-traversal":           func(m map[string]any) { setFirstFile(m, "path", "../cover.png") },
	"absolute-path":            func(m map[string]any) { setFirstFile(m, "path", "/cover.png") },
	"device-name":              func(m map[string]any) { setFirstFile(m, "path", "refs/aux.png") },
	"media-type-mismatch":      func(m map[string]any) { setFirstFile(m, "media_type", "image/webp") },
	"role-mismatch":            func(m map[string]any) { setFirstFile(m, "role", "lora") },
	"bad-sha256":               func(m map[string]any) { setFirstFile(m, "sha256", "ABC") },
	"font-in-character-pack": func(m map[string]any) {
		m["files"] = append(m["files"].([]any), map[string]any{"path": "fonts/a.otf", "role": "font", "media_type": "font/otf", "size": 4, "sha256": strings.Repeat("0", 64)})
	},
	"character-without-reference": func(m map[string]any) {
		m["files"] = slices.DeleteFunc(m["files"].([]any), func(f any) bool { return f.(map[string]any)["role"] == "reference" })
	},
	"series-listing-without-block": func(m map[string]any) { m["kind"] = "series-listing"; delete(m, "character") },
	"plugin-with-personal-licence": func(m map[string]any) {
		m["kind"], m["licence"], m["plugin"] = "plugin", "personal", map[string]any{"manifest": "profile.json"}
		delete(m, "character")
	},
	"bad-version": func(m map[string]any) { m["version"] = "v1.2" },
}

func setFirstFile(m map[string]any, key string, v any) {
	m["files"].([]any)[0].(map[string]any)[key] = v
}

func TestGoldenPacks(t *testing.T) {
	packs := goldenPacks(t)
	keys := KeySet{goldenKeyID: testKey("server").Public().(ed25519.PublicKey)}
	for name, data := range packs {
		file := filepath.Join("testdata", "packs", name)
		if *update {
			writeGolden(t, file, data)
		}
		stored, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("%s: %v (run with -update to create)", name, err)
		}
		if !bytes.Equal(stored, data) {
			t.Errorf("%s: writer output changed; if intentional run go test -run Golden -update", name)
		}
		p, err := openBytes(t, stored, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := p.Verify(VerifyOptions{Keys: keys, RequireTrusted: true}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		base := strings.TrimSuffix(name, ".mvpack")
		if *update {
			writeGolden(t, filepath.Join("testdata", "manifests", "valid", base+".json"), p.ManifestBytes())
			sig, _ := json.MarshalIndent(p.Signature(), "", "  ")
			writeGolden(t, filepath.Join("testdata", "signatures", base+".json"), append(sig, '\n'))
		}
	}
	if *update {
		pub := base64.StdEncoding.EncodeToString(keys[goldenKeyID])
		writeGolden(t, filepath.Join("testdata", "keys.json"), []byte(`{"`+goldenKeyID+`": "`+pub+`"}`+"\n"))
		writeInvalidManifests(t, packs["rin-asagiri.mvpack"])
	}
}

func writeInvalidManifests(t *testing.T, pack []byte) {
	p, err := openBytes(t, pack, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range invalidManifests {
		var m map[string]any
		if err := json.Unmarshal(p.ManifestBytes(), &m); err != nil {
			t.Fatal(err)
		}
		mutate(m)
		b, _ := json.MarshalIndent(m, "", "  ")
		writeGolden(t, filepath.Join("testdata", "manifests", "invalid", name+".json"), append(b, '\n'))
	}
}

func writeGolden(t *testing.T, file string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExampleManifests(t *testing.T) {
	valid, _ := filepath.Glob(filepath.Join("testdata", "manifests", "valid", "*.json"))
	invalid, _ := filepath.Glob(filepath.Join("testdata", "manifests", "invalid", "*.json"))
	if len(valid) < 5 || len(invalid) != len(invalidManifests) {
		t.Fatalf("expected golden manifests (valid %d, invalid %d); run go test -run Golden -update", len(valid), len(invalid))
	}
	for _, f := range valid {
		b, _ := os.ReadFile(f)
		if _, err := ParseManifest(b); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	for _, f := range invalid {
		b, _ := os.ReadFile(f)
		if _, err := ParseManifest(b); !errors.Is(err, ErrInvalidManifest) {
			t.Errorf("%s: want ErrInvalidManifest, got %v", f, err)
		}
	}
}

func TestExampleSignaturesParse(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "signatures", "*.json"))
	if len(files) == 0 {
		t.Fatal("no golden signatures")
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if _, err := ParseSignatureFile(b); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}
