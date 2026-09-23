// SPDX-License-Identifier: MIT
// Copyright (c) 2026 MangaVeyra contributors

package mvpack

import (
	"bytes"
	"encoding/binary"
	"path"
	"strings"
)

// FileType is an allowed payload file type.
type FileType string

// Allowed file types. Anything else is rejected.
const (
	TypePNG         FileType = "png"
	TypeJPEG        FileType = "jpeg"
	TypeWebP        FileType = "webp"
	TypeSafetensors FileType = "safetensors"
	TypeJSON        FileType = "json"
	TypeTTF         FileType = "ttf"
	TypeOTF         FileType = "otf"
	TypeWOFF2       FileType = "woff2"
)

// fileTypeByExt maps lowercase extensions to file types.
var fileTypeByExt = map[string]FileType{
	".png":         TypePNG,
	".jpg":         TypeJPEG,
	".jpeg":        TypeJPEG,
	".webp":        TypeWebP,
	".safetensors": TypeSafetensors,
	".json":        TypeJSON,
	".ttf":         TypeTTF,
	".otf":         TypeOTF,
	".woff2":       TypeWOFF2,
}

// MediaType returns the IANA media type recorded in the manifest for t.
func (t FileType) MediaType() string {
	switch t {
	case TypePNG:
		return "image/png"
	case TypeJPEG:
		return "image/jpeg"
	case TypeWebP:
		return "image/webp"
	case TypeSafetensors:
		return "application/vnd.safetensors"
	case TypeJSON:
		return "application/json"
	case TypeTTF:
		return "font/ttf"
	case TypeOTF:
		return "font/otf"
	case TypeWOFF2:
		return "font/woff2"
	}
	return ""
}

// IsImage reports whether t is a raster image type.
func (t FileType) IsImage() bool { return t == TypePNG || t == TypeJPEG || t == TypeWebP }

// IsFont reports whether t is a font type (allowed in plugin packs only).
func (t FileType) IsFont() bool { return t == TypeTTF || t == TypeOTF || t == TypeWOFF2 }

// FileTypeOf returns the file type for a payload path by its extension. The
// extension must be lowercase. ok is false for anything not on the allow-list.
func FileTypeOf(name string) (t FileType, ok bool) {
	t, ok = fileTypeByExt[path.Ext(name)]
	return t, ok
}

// sniffLen is how many leading bytes content sniffing needs.
const sniffLen = 16

// executableMagic lists signatures of executables, scripts, WebAssembly and
// archives. A payload that starts with any of them is rejected with ErrExecutable
// whatever its extension claims.
var executableMagic = [][]byte{
	{0x7F, 'E', 'L', 'F'},    // ELF
	{'M', 'Z'},               // PE / DOS
	{'#', '!'},               // shebang script
	{0xFE, 0xED, 0xFA, 0xCE}, // Mach-O 32
	{0xFE, 0xED, 0xFA, 0xCF}, // Mach-O 64
	{0xCE, 0xFA, 0xED, 0xFE}, // Mach-O 32 LE
	{0xCF, 0xFA, 0xED, 0xFE}, // Mach-O 64 LE
	{0xCA, 0xFE, 0xBA, 0xBE}, // Mach-O fat / Java class
	{0x00, 'a', 's', 'm'},    // WebAssembly
	{'P', 'K', 0x03, 0x04},   // nested ZIP / JAR / APK
	{0x1F, 0x8B},             // gzip
	{'R', 'a', 'r', '!'},     // RAR
	{'7', 'z', 0xBC, 0xAF},   // 7-Zip
}

// sniff checks the leading bytes of a payload against its declared type.
// size is the full payload size (needed for the safetensors header check).
func sniff(name string, t FileType, head []byte, size int64) error {
	for _, m := range executableMagic {
		if bytes.HasPrefix(head, m) {
			return pathErr(ErrExecutable, name, "starts with an executable or archive signature")
		}
	}
	ok := false
	switch t {
	case TypePNG:
		ok = bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n"))
	case TypeJPEG:
		ok = bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF})
	case TypeWebP:
		ok = len(head) >= 12 && bytes.Equal(head[0:4], []byte("RIFF")) && bytes.Equal(head[8:12], []byte("WEBP"))
	case TypeSafetensors:
		if len(head) >= 9 {
			n := binary.LittleEndian.Uint64(head[:8])
			ok = n >= 2 && n <= MaxSafetensorsHeader && int64(n)+8 <= size && head[8] == '{'
		}
	case TypeJSON:
		trimmed := bytes.TrimLeft(head, " \t\r\n")
		ok = len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
		if len(trimmed) == 0 && len(head) == sniffLen {
			ok = true // long leading whitespace; full validation happens on read
		}
	case TypeTTF:
		ok = bytes.HasPrefix(head, []byte{0x00, 0x01, 0x00, 0x00}) || bytes.HasPrefix(head, []byte("true"))
	case TypeOTF:
		ok = bytes.HasPrefix(head, []byte("OTTO"))
	case TypeWOFF2:
		ok = bytes.HasPrefix(head, []byte("wOF2"))
	}
	if !ok {
		return pathErr(ErrContentMismatch, name, "bytes are not a valid %s file", t)
	}
	return nil
}

// MaxSafetensorsHeader bounds the JSON header of a .safetensors file (the
// reference implementation uses 100 MB; packs are far stricter).
const MaxSafetensorsHeader = 16 << 20

// Reserved root file names.
const (
	ManifestName  = "manifest.json"
	SignatureName = "signature.json"
)

// Path rules (see SPEC.md §4).
const (
	MaxPathLen     = 200
	MaxSegmentLen  = 100
	MaxPathDepth   = 6
	pathSegmentSet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-"
)

var windowsReserved = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// CheckPath validates a payload path against the pack path rules: relative,
// forward slashes only, ASCII letters, digits, '.', '_' and '-', no segment
// starting with '.', no Windows device names, at most MaxPathDepth segments of
// at most MaxSegmentLen bytes and MaxPathLen bytes in total. The reserved root
// names manifest.json and signature.json are not valid payload paths.
func CheckPath(name string) error {
	if name == "" {
		return pathErr(ErrUnsafePath, name, "empty path")
	}
	if len(name) > MaxPathLen {
		return pathErr(ErrUnsafePath, name, "longer than %d bytes", MaxPathLen)
	}
	if strings.HasPrefix(name, "/") {
		return pathErr(ErrUnsafePath, name, "absolute path")
	}
	if strings.ContainsRune(name, '\\') {
		return pathErr(ErrUnsafePath, name, "backslash in path")
	}
	if len(name) >= 2 && name[1] == ':' {
		return pathErr(ErrUnsafePath, name, "drive letter in path")
	}
	segs := strings.Split(name, "/")
	if len(segs) > MaxPathDepth {
		return pathErr(ErrUnsafePath, name, "deeper than %d levels", MaxPathDepth)
	}
	for _, s := range segs {
		switch {
		case s == "":
			return pathErr(ErrUnsafePath, name, "empty path segment")
		case s == "." || s == "..":
			return pathErr(ErrUnsafePath, name, "dot segment")
		case s[0] == '.':
			return pathErr(ErrUnsafePath, name, "hidden file or directory")
		case len(s) > MaxSegmentLen:
			return pathErr(ErrUnsafePath, name, "segment longer than %d bytes", MaxSegmentLen)
		}
		for i := 0; i < len(s); i++ {
			if !strings.ContainsRune(pathSegmentSet, rune(s[i])) {
				return pathErr(ErrUnsafePath, name, "character %q not allowed", s[i])
			}
		}
		base, _, _ := strings.Cut(strings.ToLower(s), ".")
		if windowsReserved[base] {
			return pathErr(ErrUnsafePath, name, "reserved device name %q", base)
		}
	}
	if name == ManifestName || name == SignatureName {
		return pathErr(ErrUnsafePath, name, "reserved name")
	}
	return nil
}
