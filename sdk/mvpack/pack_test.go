// SPDX-License-Identifier: MIT
// Copyright (c) 2026 MangaVeyra contributors

package mvpack

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func serverKeys() KeySet {
	return KeySet{"srv_7b1d": testKey("server").Public().(ed25519.PublicKey)}
}

func TestRoundTripSignedPack(t *testing.T) {
	pack := buildCharacterPack(t, testSigner("server", "srv_7b1d", SignerServer))
	p, err := openBytes(t, pack, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := p.Manifest()
	if m.Kind != KindCharacter || len(m.Files) != 5 || m.Credit.String() != "Jerry Ink (@jerry-ink)" {
		t.Fatalf("unexpected manifest: %+v", m)
	}
	res, err := p.Verify(VerifyOptions{Keys: serverKeys(), RequireTrusted: true, RequireSigner: SignerServer})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Signed || !res.Trusted() || res.Files != 5 || len(res.Signatures) != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if got, want := res.Signatures[0].Fingerprint, Fingerprint(testKey("server").Public().(ed25519.PublicKey)); got != want {
		t.Fatalf("fingerprint %s, want %s", got, want)
	}

	rc, err := p.Open("refs/front.png")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(got, pngBytes(t, 40)) {
		t.Fatalf("payload mismatch: %v", err)
	}
}

func TestWriterIsDeterministic(t *testing.T) {
	s := testSigner("server", "srv_7b1d", SignerServer)
	a, b := buildCharacterPack(t, s), buildCharacterPack(t, s)
	if !bytes.Equal(a, b) {
		t.Fatal("two builds from the same input differ")
	}
}

func TestUnsignedPack(t *testing.T) {
	p, err := openBytes(t, buildCharacterPack(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Verify(VerifyOptions{})
	if err != nil || res.Signed {
		t.Fatalf("unsigned integrity check: %+v %v", res, err)
	}
	if _, err := p.Verify(VerifyOptions{RequireSigned: true}); !errors.Is(err, ErrUnsigned) {
		t.Fatalf("want ErrUnsigned, got %v", err)
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	signed := buildCharacterPack(t, testSigner("server", "srv_7b1d", SignerServer))
	cases := map[string]struct {
		pack []byte
		want error
	}{
		"payload byte flipped": {rewriteEntry(t, signed, "refs/side.png", func(b []byte) []byte {
			c := bytes.Clone(b)
			c[len(c)-5] ^= 0xFF
			return c
		}), ErrHashMismatch},
		"manifest edited": {rewriteEntry(t, signed, ManifestName, func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"Rin Asagiri"`), []byte(`"Rin Asagiri 2"`), 1)
		}), ErrBadSignature},
		"signature forged": {rewriteEntry(t, signed, SignatureName, func(b []byte) []byte {
			var sf SignatureFile
			_ = json.Unmarshal(b, &sf)
			sf.Signatures[0].Sig = sf.Signatures[0].Sig[:10] + flipB64(sf.Signatures[0].Sig[10]) + sf.Signatures[0].Sig[11:]
			out, _ := json.Marshal(sf)
			return out
		}), ErrBadSignature},
		"signed file list edited": {rewriteEntry(t, signed, SignatureName, func(b []byte) []byte {
			var sf SignatureFile
			_ = json.Unmarshal(b, &sf)
			sf.Files[0].Size++
			out, _ := json.Marshal(sf)
			return out
		}), ErrBadSignature},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := openBytes(t, c.pack, nil)
			if err == nil {
				_, err = p.Verify(VerifyOptions{Keys: serverKeys()})
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("want %v, got %v", c.want, err)
			}
		})
	}
}

func flipB64(c byte) string {
	if c == 'A' {
		return "B"
	}
	return "A"
}

func TestVerifyKeyTrust(t *testing.T) {
	pack := buildCharacterPack(t, testSigner("server", "srv_7b1d", SignerServer))
	p, err := openBytes(t, pack, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Unknown key id: integrity holds but the signature is not trusted.
	res, err := p.Verify(VerifyOptions{Keys: KeySet{}})
	if err != nil || res.Trusted() {
		t.Fatalf("unknown key: %+v %v", res, err)
	}
	if _, err := p.Verify(VerifyOptions{Keys: KeySet{}, RequireTrusted: true}); !errors.Is(err, ErrUntrustedKey) {
		t.Fatalf("want ErrUntrustedKey, got %v", err)
	}
	// Same key id, different key (someone reused a registered id): hard failure.
	impostor := KeySet{"srv_7b1d": testKey("other").Public().(ed25519.PublicKey)}
	if _, err := p.Verify(VerifyOptions{Keys: impostor}); !errors.Is(err, ErrUntrustedKey) {
		t.Fatalf("want ErrUntrustedKey, got %v", err)
	}
	// Signed by a server, but World's signature is required.
	if _, err := p.Verify(VerifyOptions{Keys: serverKeys(), RequireSigner: SignerWorld}); !errors.Is(err, ErrUntrustedKey) {
		t.Fatalf("want ErrUntrustedKey, got %v", err)
	}
}

func TestAddSignatureCountersigns(t *testing.T) {
	pack := buildCharacterPack(t, testSigner("server", "srv_7b1d", SignerServer))
	p, err := openBytes(t, pack, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	world := testSigner("world", "world_2026", SignerWorld)
	if err := AddSignature(p, &out, world); err != nil {
		t.Fatal(err)
	}
	p2, err := openBytes(t, out.Bytes(), nil)
	if err != nil {
		t.Fatal(err)
	}
	keys := serverKeys()
	keys["world_2026"] = testKey("world").Public().(ed25519.PublicKey)
	res, err := p2.Verify(VerifyOptions{Keys: keys, RequireSigner: SignerWorld})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Signatures) != 2 || !res.Signatures[0].Trusted || !res.Signatures[1].Trusted {
		t.Fatalf("want two trusted signatures, got %+v", res.Signatures)
	}
	if err := AddSignature(p2, io.Discard, world); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("signing twice with one key: want ErrBadSignature, got %v", err)
	}
}

func TestExtract(t *testing.T) {
	pack := buildCharacterPack(t, testSigner("server", "srv_7b1d", SignerServer))
	p, err := openBytes(t, pack, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := p.Extract(dir, VerifyOptions{Keys: serverKeys(), RequireTrusted: true}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{ManifestName, SignatureName, "cover.png", "refs/front.png", "lora/rin-asagiri.safetensors", "profile.json"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	got, _ := os.ReadFile(filepath.Join(dir, "refs", "side.png"))
	if !bytes.Equal(got, pngBytes(t, 60)) {
		t.Error("extracted content differs")
	}
	// Extracting twice into the same directory never overwrites.
	if _, err := p.Extract(dir, VerifyOptions{}); err == nil {
		t.Error("second extract into a non-empty directory should fail")
	}
}

func TestOpenRejectsHostileArchives(t *testing.T) {
	png := pngBytes(t, 10)
	good := map[string][]byte{"refs/a.png": png}
	roles := map[string]Role{"refs/a.png": RoleReference}
	small := DefaultLimits()
	small.MaxImageSize = 64
	small.MaxFiles = 1
	bomb := DefaultLimits()
	bomb.RatioCheckMinSize = 1024

	cases := []struct {
		name string
		pack []byte
		opts *Options
		want error
	}{
		{"zip slip", craftPack(t, KindCharacter, good, roles, nil, rawEntry{name: "../evil.png", data: png}), nil, ErrUnsafePath},
		{"nested zip slip", craftPack(t, KindCharacter, good, roles, nil, rawEntry{name: "refs/../../evil.png", data: png}), nil, ErrUnsafePath},
		{"absolute path", craftPack(t, KindCharacter, good, roles, nil, rawEntry{name: "/etc/evil.png", data: png}), nil, ErrUnsafePath},
		{"backslash", craftPack(t, KindCharacter, good, roles, nil, rawEntry{name: `refs\evil.png`, data: png}), nil, ErrUnsafePath},
		{"drive letter", craftPack(t, KindCharacter, good, roles, nil, rawEntry{name: "C:evil.png", data: png}), nil, ErrUnsafePath},
		{"hidden file", craftPack(t, KindCharacter, good, roles, nil, rawEntry{name: "refs/.evil.png", data: png}), nil, ErrUnsafePath},
		{"device name", craftPack(t, KindCharacter, good, roles, nil, rawEntry{name: "refs/con.png", data: png}), nil, ErrUnsafePath},
		{"executable", craftPack(t, KindCharacter, good, roles, nil, rawEntry{name: "setup.exe", data: []byte("MZ\x90\x00")}), nil, ErrDisallowedFile},
		{"script", craftPack(t, KindCharacter, good, roles, nil, rawEntry{name: "run.sh", data: []byte("#!/bin/sh\n")}), nil, ErrDisallowedFile},
		{"wasm", craftPack(t, KindCharacter, good, roles, nil, rawEntry{name: "plugin.wasm", data: []byte("\x00asm")}), nil, ErrDisallowedFile},
		{"uppercase extension", craftPack(t, KindCharacter, good, roles, nil, rawEntry{name: "refs/b.PNG", data: png}), nil, ErrDisallowedFile},
		{"symlink", craftPack(t, KindCharacter, good, roles, nil, rawEntry{name: "refs/link.png", data: []byte("/etc/passwd"), mode: fs.ModeSymlink | 0o777}), nil, ErrUnsupported},
		{"case collision", craftPack(t, KindCharacter, good, roles, nil, rawEntry{name: "refs/A.png", data: png}), nil, ErrDuplicateEntry},
		{"exact duplicate", craftPack(t, KindCharacter, good, roles, nil, rawEntry{name: "refs/a.png", data: png}), nil, ErrDuplicateEntry},
		{"unlisted file", craftPack(t, KindCharacter, good, roles, nil, rawEntry{name: "refs/b.png", data: png}), nil, ErrUnlistedFile},
		{"listed but missing", craftPack(t, KindCharacter, good, roles, func(m *Manifest) {
			m.Files = append(m.Files, File{Path: "refs/b.png", Role: RoleReference, MediaType: "image/png", Size: 1, SHA256: strings.Repeat("0", 64)})
		}), nil, ErrMissingFile},
		{"size disagrees with header", craftPack(t, KindCharacter, good, roles, func(m *Manifest) { m.Files[0].Size++ }), nil, ErrSizeMismatch},
		{"encrypted", craftPack(t, KindCharacter, good, roles, nil, rawEntry{name: "refs/b.png", data: png, flags: 0x1}), nil, ErrUnsupported},
		{"unknown method", craftPack(t, KindCharacter, good, roles, nil, rawEntry{name: "refs/b.png", data: png, method: 99}), nil, ErrUnsupported},
		{"missing manifest", craftZip(t, []rawEntry{{name: "refs/a.png", data: png}}), nil, ErrMissingManifest},
		{"invalid manifest", craftPack(t, KindCharacter, good, roles, func(m *Manifest) { m.Credit.Handle = "Jerry Ink" }), nil, ErrInvalidManifest},
		{"too large", craftPack(t, KindCharacter, map[string][]byte{"refs/a.png": append(bytes.Clone(png), make([]byte, 100)...)}, roles, nil), &Options{Limits: &small}, ErrTooLarge},
		{"too many files", craftPack(t, KindCharacter, map[string][]byte{"refs/a.png": png[:40], "refs/b.png": png[:40]},
			map[string]Role{"refs/a.png": RoleReference, "refs/b.png": RoleReference}, nil), &Options{Limits: &small}, ErrTooManyFiles},
		{"zip bomb", craftPack(t, KindCharacter, good, roles, nil,
			rawEntry{name: "refs/bomb.png", data: append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 4<<20)...), method: zip.Deflate, flags: 0x800}),
			&Options{Limits: &bomb}, ErrZipBomb},
		{"not a zip", []byte("definitely not a zip file"), nil, ErrUnsupported},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := openBytes(t, c.pack, c.opts)
			if !errors.Is(err, c.want) {
				t.Fatalf("want %v, got %v", c.want, err)
			}
		})
	}
}

func TestVerifyRejectsBadContent(t *testing.T) {
	png := pngBytes(t, 10)
	cases := []struct {
		name  string
		files map[string][]byte
		roles map[string]Role
		want  error
	}{
		{"executable disguised as png", map[string][]byte{"refs/a.png": append([]byte("MZ"), png...)},
			map[string]Role{"refs/a.png": RoleReference}, ErrExecutable},
		{"elf disguised as png", map[string][]byte{"refs/a.png": append([]byte("\x7fELF"), png...)},
			map[string]Role{"refs/a.png": RoleReference}, ErrExecutable},
		{"jpeg that is text", map[string][]byte{"refs/a.png": png, "refs/b.jpg": []byte("hello, not a jpeg")},
			map[string]Role{"refs/a.png": RoleReference, "refs/b.jpg": RoleReference}, ErrContentMismatch},
		{"broken json", map[string][]byte{"refs/a.png": png, "profile.json": []byte(`{"voice": `)},
			map[string]Role{"refs/a.png": RoleReference, "profile.json": RoleProfile}, ErrContentMismatch},
		{"json with bom", map[string][]byte{"refs/a.png": png, "profile.json": []byte("\xEF\xBB\xBF{}")},
			map[string]Role{"refs/a.png": RoleReference, "profile.json": RoleProfile}, ErrContentMismatch},
		{"safetensors with bad header", map[string][]byte{"refs/a.png": png, "w.safetensors": append([]byte{5, 0, 0, 0, 0, 0, 0, 0}, []byte("{abcd")...)},
			map[string]Role{"refs/a.png": RoleReference, "w.safetensors": RoleLoRA}, ErrContentMismatch},
		{"safetensors header too long", map[string][]byte{"refs/a.png": png, "w.safetensors": append([]byte{0xFF, 0xFF, 0, 0, 0, 0, 0, 0}, []byte("{}")...)},
			map[string]Role{"refs/a.png": RoleReference, "w.safetensors": RoleLoRA}, ErrContentMismatch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := openBytes(t, craftPack(t, KindCharacter, c.files, c.roles, nil), nil)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if _, err := p.Verify(VerifyOptions{}); !errors.Is(err, c.want) {
				t.Fatalf("want %v, got %v", c.want, err)
			}
		})
	}
}

func TestVerifyRejectsLyingSizes(t *testing.T) {
	png := pngBytes(t, 10)
	data := append(bytes.Clone(png), make([]byte, 2000)...)
	// The ZIP header and the manifest both claim 100 bytes; the stream holds more.
	pack := craftPack(t, KindCharacter, map[string][]byte{"refs/a.png": png}, map[string]Role{"refs/a.png": RoleReference},
		func(m *Manifest) { m.Files[0].Size = 100 })
	pack = rewriteEntryRaw(t, pack, "refs/a.png", rawEntry{name: "refs/a.png", data: data, method: zip.Deflate, lieSize: 100})
	p, err := openBytes(t, pack, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := p.Verify(VerifyOptions{}); !errors.Is(err, ErrSizeMismatch) && !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("want size or hash mismatch, got %v", err)
	}
}

func rewriteEntryRaw(t *testing.T, pack []byte, name string, repl rawEntry) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(pack), int64(len(pack)))
	if err != nil {
		t.Fatal(err)
	}
	var entries []rawEntry
	for _, f := range zr.File {
		if f.Name == name {
			entries = append(entries, repl)
			continue
		}
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		rc.Close()
		entries = append(entries, rawEntry{name: f.Name, data: data, method: f.Method})
	}
	return craftZip(t, entries)
}

func TestManifestExtensionsRoundTrip(t *testing.T) {
	pack := craftPack(t, KindCharacter, map[string][]byte{"refs/a.png": pngBytes(t, 1)}, map[string]Role{"refs/a.png": RoleReference},
		func(m *Manifest) {
			m.Extensions = map[string]json.RawMessage{"x-studio": json.RawMessage(`{"panel_count":12}`)}
		})
	p, err := openBytes(t, pack, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Manifest().Extensions["x-studio"]) != `{"panel_count":12}` {
		t.Fatalf("extension lost: %v", p.Manifest().Extensions)
	}
	if !bytes.Contains(p.CanonicalManifest(), []byte(`"x-studio":{"panel_count":12}`)) {
		t.Fatalf("extension not covered by canonical bytes: %s", p.CanonicalManifest())
	}
}

func TestParseManifestRejectsUnknownAndDuplicateMembers(t *testing.T) {
	m := baseManifest(KindCharacter)
	m.Files = []File{{Path: "refs/a.png", Role: RoleReference, MediaType: "image/png", Size: 1, SHA256: strings.Repeat("a", 64)}}
	b, _ := json.Marshal(m)
	if _, err := ParseManifest(b); err != nil {
		t.Fatalf("baseline manifest should parse: %v", err)
	}
	unknown := bytes.Replace(b, []byte(`{"mvpack"`), []byte(`{"price":1,"mvpack"`), 1)
	if _, err := ParseManifest(unknown); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("unknown member: want ErrInvalidManifest, got %v", err)
	}
	nested := bytes.Replace(b, []byte(`"handle":"jerry-ink"`), []byte(`"handle":"jerry-ink","email":"x"`), 1)
	if _, err := ParseManifest(nested); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("unknown nested member: want ErrInvalidManifest, got %v", err)
	}
	dup := bytes.Replace(b, []byte(`{"mvpack":1`), []byte(`{"mvpack":1,"mvpack":1`), 1)
	if _, err := ParseManifest(dup); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("duplicate member: want ErrInvalidManifest, got %v", err)
	}
}

func TestWriterRejects(t *testing.T) {
	png := pngBytes(t, 10)
	cases := []struct {
		name string
		kind Kind
		path string
		role Role
		data []byte
		want error
	}{
		{"zip slip", KindCharacter, "../x.png", RoleReference, png, ErrUnsafePath},
		{"reserved name", KindCharacter, "manifest.json", RoleData, []byte("{}"), ErrUnsafePath},
		{"space in name", KindCharacter, "my ref.png", RoleReference, png, ErrUnsafePath},
		{"executable extension", KindCharacter, "tool.exe", RoleData, []byte("MZ"), ErrDisallowedFile},
		{"executable content", KindCharacter, "refs/a.png", RoleReference, []byte("\x7fELF\x02\x01\x01"), ErrExecutable},
		{"wrong content", KindCharacter, "refs/a.webp", RoleReference, png, ErrContentMismatch},
		{"role mismatch", KindCharacter, "refs/a.png", RoleLoRA, png, ErrDisallowedFile},
		{"font outside plugin", KindCharacter, "fonts/a.otf", RoleFont, []byte("OTTO\x00\x00"), ErrDisallowedFile},
		{"invalid json", KindCharacter, "profile.json", RoleProfile, []byte(`{"a":`), ErrContentMismatch},
		{"too large", KindCharacter, "refs/a.png", RoleReference, append(bytes.Clone(png), make([]byte, 64<<20)...), ErrTooLarge},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, err := NewWriter(io.Discard, baseManifest(c.kind))
			if err != nil {
				t.Fatal(err)
			}
			if err := w.AddBytes(c.path, c.role, c.data); !errors.Is(err, c.want) {
				t.Fatalf("want %v, got %v", c.want, err)
			}
		})
	}
}

func TestWriterPoisonedAfterStreamFailure(t *testing.T) {
	w, _ := NewWriter(io.Discard, baseManifest(KindCharacter))
	if err := w.AddBytes("profile.json", RoleProfile, []byte(`{broken`)); err == nil {
		t.Fatal("expected failure")
	}
	if err := w.AddBytes("refs/a.png", RoleReference, pngBytes(t, 1)); err == nil {
		t.Fatal("writer must refuse further files after a failed stream")
	}
	if _, err := w.Close(); err == nil {
		t.Fatal("Close must fail after a failed stream")
	}
}

func TestWriterValidatesOnClose(t *testing.T) {
	m := baseManifest(KindCharacter)
	w, _ := NewWriter(io.Discard, m)
	_ = w.AddBytes("profile.json", RoleProfile, []byte(`{}`))
	_, err := w.Close()
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(err.Error(), "reference image") {
		t.Fatalf("want validation error about references, got %v", err)
	}
}

func TestPluginPackWithFont(t *testing.T) {
	m := baseManifest(KindPlugin)
	m.ID, m.Name, m.Licence = "hand-lettering-fonts", "Hand-lettering fonts", "OFL-1.1"
	m.WorksWith = nil
	m.Plugin = &PluginRef{Manifest: "plugin.json"}
	var buf bytes.Buffer
	w, _ := NewWriter(&buf, m)
	if err := w.AddBytes("plugin.json", RolePluginManifest, []byte(`{"id":"inkwell.hand-lettering-fonts"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.AddBytes("fonts/inkwell.woff2", RoleFont, []byte("wOF2\x00\x01\x00\x00fake")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	p, err := openBytes(t, buf.Bytes(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Verify(VerifyOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenFile(t *testing.T) {
	name := filepath.Join(t.TempDir(), "rin.mvpack")
	if err := os.WriteFile(name, buildCharacterPack(t), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := OpenFile(name, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := p.Verify(VerifyOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestLicenceToken(t *testing.T) {
	key := testKey("world-licence")
	keys := KeySet{"wlt_1": key.Public().(ed25519.PublicKey)}
	tok, err := SignLicenceToken(LicenceClaims{ItemID: "wi_iron_koi", AccountID: "acc_1", Handle: "jerry-ink",
		ItemVersion: "1.0.0", Licence: "personal", Acquisition: "paid", IssuedAt: fixedTime.Unix()}, "wlt_1", key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseLicenceToken(tok, keys)
	if err != nil {
		t.Fatal(err)
	}
	if c.ItemID != "wi_iron_koi" || c.KeyID != "wlt_1" || !c.Issued().Equal(fixedTime) {
		t.Fatalf("claims %+v", c)
	}
	if strings.Contains(tok, "=") || !strings.HasPrefix(tok, "mvlt1.") {
		t.Fatalf("token format %q", tok)
	}
	for name, bad := range map[string]string{
		"tampered":   strings.Replace(tok, tok[10:12], "AA", 1),
		"no sig":     strings.Join(strings.Split(tok, ".")[:2], "."),
		"bad prefix": "mvlt2" + tok[5:],
	} {
		if _, err := ParseLicenceToken(bad, keys); !errors.Is(err, ErrLicenceToken) {
			t.Errorf("%s: want ErrLicenceToken, got %v", name, err)
		}
	}
	if _, err := ParseLicenceToken(tok, KeySet{}); !errors.Is(err, ErrLicenceToken) {
		t.Errorf("unknown key: want ErrLicenceToken, got %v", err)
	}
}
