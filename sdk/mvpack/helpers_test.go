// SPDX-License-Identifier: MIT
// Copyright (c) 2026 MangaVeyra contributors

package mvpack

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"maps"
	"slices"
	"testing"
	"time"
)

// testKey returns a deterministic ed25519 key for tests. It is not secret and
// must never be used outside tests.
func testKey(label string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("mvpack test key: " + label))
	return ed25519.NewKeyFromSeed(seed[:])
}

var fixedTime = time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)

func testSigner(label, keyID, role string) *Signer {
	return &Signer{KeyID: keyID, Key: testKey(label), Role: role, Now: func() time.Time { return fixedTime }}
}

// pngBytes renders a tiny screentone-style placeholder PNG.
func pngBytes(t testing.TB, shade uint8) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 8, 8))
	for y := range 8 {
		for x := range 8 {
			v := uint8(255)
			if (x+y)%3 == 0 {
				v = shade
			}
			img.SetGray(x, y, color.Gray{Y: v})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// safetensorsBytes builds a minimal valid .safetensors file with one tensor.
func safetensorsBytes() []byte {
	hdr := []byte(`{"lora.weight":{"dtype":"F32","shape":[2],"data_offsets":[0,8]},"__metadata__":{"trigger":"rinasagiri"}}`)
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, uint64(len(hdr)))
	b.Write(hdr)
	b.Write(make([]byte, 8))
	return b.Bytes()
}

func baseManifest(kind Kind) Manifest {
	m := Manifest{
		Format:    FormatVersion,
		Kind:      kind,
		ID:        "rin-asagiri",
		Name:      "Rin Asagiri",
		Version:   "1.2.0",
		Summary:   "19-year-old courier in a long coat with a satchel.",
		CreatedAt: fixedTime,
		Licence:   "CC-BY-4.0",
		AILabel:   AILabel{Art: AIAssisted, Design: AIHuman},
		Credit:    Credit{DisplayName: "Jerry Ink", Handle: "jerry-ink"},
		Tags:      []string{"courier", "tidebreak-courier"},
		WorksWith: []string{"illustrious-xl"},
	}
	if kind == KindCharacter {
		m.Character = &CharacterInfo{FromSeries: "Tidebreak Courier", TriggerWord: "rinasagiri", BaseModel: "illustrious-xl"}
	}
	return m
}

// buildCharacterPack writes a complete character pack signed by the given signers.
func buildCharacterPack(t testing.TB, signers ...*Signer) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriter(&buf, baseManifest(KindCharacter))
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(w.AddBytes("cover.png", RoleCover, pngBytes(t, 20)))
	must(w.AddBytes("refs/front.png", RoleReference, pngBytes(t, 40)))
	must(w.AddBytes("refs/side.png", RoleReference, pngBytes(t, 60)))
	must(w.AddBytes("lora/rin-asagiri.safetensors", RoleLoRA, safetensorsBytes()))
	must(w.AddBytes("profile.json", RoleProfile, []byte(`{"personality":"Stubborn, kind, always late.","voice":"Quick, dry jokes."}`)))
	if _, err := w.Close(signers...); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func openBytes(t testing.TB, b []byte, opts *Options) (*Pack, error) {
	t.Helper()
	return Open(bytes.NewReader(b), int64(len(b)), opts)
}

// rawEntry is one entry of a hand-crafted ZIP used to build hostile packs.
type rawEntry struct {
	name   string
	data   []byte
	method uint16
	mode   fs.FileMode
	flags  uint16
	// lieSize, when > 0, is written as the uncompressed size instead of the real one.
	lieSize uint64
}

// craftZip writes entries verbatim, bypassing every Writer check.
func craftZip(t testing.TB, entries []rawEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		fh := &zip.FileHeader{Name: e.name, Method: e.method, Modified: fixedTime}
		if e.mode != 0 {
			fh.SetMode(e.mode)
		}
		if e.flags != 0 || e.lieSize != 0 || (e.method != zip.Store && e.method != zip.Deflate) {
			// Raw entry: compute sizes ourselves.
			var comp bytes.Buffer
			if e.method == zip.Deflate {
				comp.Write(deflate(t, e.data))
			} else {
				comp.Write(e.data)
			}
			fh.Flags = e.flags
			fh.CRC32 = crc(e.data)
			fh.CompressedSize64 = uint64(comp.Len())
			fh.UncompressedSize64 = uint64(len(e.data))
			if e.lieSize != 0 {
				fh.UncompressedSize64 = e.lieSize
			}
			w, err := zw.CreateRaw(fh)
			if err != nil {
				t.Fatal(err)
			}
			w.Write(comp.Bytes())
			continue
		}
		w, err := zw.CreateHeader(fh)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(e.data)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// craftPack builds a pack whose manifest honestly lists files (with correct
// hashes) but whose content bypasses the Writer checks. mutate may edit the
// manifest after the file list is computed; extra entries are added unlisted.
func craftPack(t testing.TB, kind Kind, files map[string][]byte, roles map[string]Role, mutate func(*Manifest), extra ...rawEntry) []byte {
	t.Helper()
	m := baseManifest(kind)
	var entries []rawEntry
	for _, name := range sortedKeys(files) {
		data := files[name]
		sum := sha256.Sum256(data)
		ft, _ := FileTypeOf(name)
		m.Files = append(m.Files, File{Path: name, Role: roles[name], MediaType: ft.MediaType(), Size: int64(len(data)), SHA256: hexSum(sum[:])})
		entries = append(entries, rawEntry{name: name, data: data, method: zip.Store})
	}
	if mutate != nil {
		mutate(&m)
	}
	mb, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	entries = append(entries, rawEntry{name: ManifestName, data: mb, method: zip.Deflate})
	entries = append(entries, extra...)
	return craftZip(t, entries)
}

func sortedKeys[V any](m map[string]V) []string { return slices.Sorted(maps.Keys(m)) }

// rewriteEntry returns a copy of the pack with one entry's content replaced
// (sizes updated in the ZIP header, manifest untouched unless it is the entry).
func rewriteEntry(t testing.TB, pack []byte, name string, edit func([]byte) []byte) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(pack), int64(len(pack)))
	if err != nil {
		t.Fatal(err)
	}
	var entries []rawEntry
	for _, f := range zr.File {
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		rc.Close()
		if f.Name == name {
			data = edit(data)
		}
		entries = append(entries, rawEntry{name: f.Name, data: data, method: f.Method})
	}
	return craftZip(t, entries)
}

func deflate(t testing.TB, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	fw, err := flate.NewWriter(&b, flate.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(data)
	fw.Close()
	return b.Bytes()
}

func crc(data []byte) uint32 { return crc32.ChecksumIEEE(data) }
