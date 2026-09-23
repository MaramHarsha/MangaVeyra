// SPDX-License-Identifier: MIT
// Copyright (c) 2026 MangaVeyra contributors

package mvpack

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"
)

// Limits bounds what a Reader accepts and a Writer produces. All sizes are
// uncompressed bytes unless noted.
type Limits struct {
	MaxArchiveSize      int64 // size of the .mvpack file itself
	MaxFiles            int   // payload files
	MaxTotalSize        int64 // sum of payload sizes
	MaxManifestSize     int64 // manifest.json
	MaxSignatureSize    int64 // signature.json
	MaxImageSize        int64 // each PNG / JPEG / WebP
	MaxJSONSize         int64 // each JSON payload
	MaxWeightsSize      int64 // each .safetensors
	MaxFontSize         int64 // each font
	MaxCompressionRatio int64 // uncompressed / compressed, checked for entries above RatioCheckMinSize
	RatioCheckMinSize   int64
}

// DefaultLimits returns the limits of SPEC.md §5.
func DefaultLimits() Limits {
	return Limits{
		MaxArchiveSize:      3 << 30,
		MaxFiles:            1000,
		MaxTotalSize:        3 << 30,
		MaxManifestSize:     256 << 10,
		MaxSignatureSize:    1 << 20,
		MaxImageSize:        32 << 20,
		MaxJSONSize:         4 << 20,
		MaxWeightsSize:      2 << 30,
		MaxFontSize:         16 << 20,
		MaxCompressionRatio: 100,
		RatioCheckMinSize:   1 << 20,
	}
}

func (l Limits) maxFor(t FileType) int64 {
	switch {
	case t.IsImage():
		return l.MaxImageSize
	case t == TypeJSON:
		return l.MaxJSONSize
	case t == TypeSafetensors:
		return l.MaxWeightsSize
	case t.IsFont():
		return l.MaxFontSize
	}
	return 0
}

// Options configures Open.
type Options struct {
	// Limits overrides DefaultLimits when non-zero.
	Limits *Limits
}

func (o *Options) limits() Limits {
	if o != nil && o.Limits != nil {
		return *o.Limits
	}
	return DefaultLimits()
}

// Pack is an opened, structurally validated .mvpack. Opening checks the ZIP
// directory, the path rules, the allow-list, the size limits, the manifest and
// that the manifest lists exactly the payload files present. File contents and
// signatures are checked by Verify, or lazily as each file is streamed with Open.
type Pack struct {
	zr        *zip.Reader
	manifest  *Manifest
	raw       []byte
	canonical []byte
	sig       *SignatureFile
	entries   map[string]*zip.File
	limits    Limits
	closer    io.Closer
}

// OpenFile opens a pack from disk. Call Close when done.
func OpenFile(name string, opts *Options) (*Pack, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	p, err := Open(f, st.Size(), opts)
	if err != nil {
		f.Close()
		return nil, err
	}
	p.closer = f
	return p, nil
}

// Close releases the underlying file when the pack was opened with OpenFile.
func (p *Pack) Close() error {
	if p.closer != nil {
		return p.closer.Close()
	}
	return nil
}

// Open reads the ZIP directory from r and validates the pack structure.
func Open(r io.ReaderAt, size int64, opts *Options) (*Pack, error) {
	lim := opts.limits()
	if size > lim.MaxArchiveSize {
		return nil, fmt.Errorf("%w: archive is %d bytes, limit %d", ErrTooLarge, size, lim.MaxArchiveSize)
	}
	zr, err := zip.NewReader(r, size)
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return nil, fmt.Errorf("%w: not a readable ZIP archive: %v", ErrUnsupported, err)
	}
	if len(zr.File) > 2*lim.MaxFiles+2 { // payload + directory entries + manifest + signature
		return nil, fmt.Errorf("%w: %d entries", ErrTooManyFiles, len(zr.File))
	}
	p := &Pack{zr: zr, entries: map[string]*zip.File{}, limits: lim}
	lower := map[string]string{}
	var manifestFile, sigFile *zip.File
	var total int64
	payload := 0
	for _, f := range zr.File {
		name := f.Name
		if f.Flags&0x1 != 0 {
			return nil, pathErr(ErrUnsupported, name, "encrypted entry")
		}
		if f.Method != zip.Store && f.Method != zip.Deflate {
			return nil, pathErr(ErrUnsupported, name, "compression method %d", f.Method)
		}
		mode := f.Mode()
		if strings.HasSuffix(name, "/") {
			if err := CheckPath(strings.TrimSuffix(name, "/")); err != nil {
				return nil, err
			}
			if f.UncompressedSize64 != 0 {
				return nil, pathErr(ErrUnsupported, name, "directory entry with content")
			}
			continue
		}
		if mode&fs.ModeType != 0 {
			return nil, pathErr(ErrUnsupported, name, "not a regular file (%v)", mode.Type())
		}
		if prev, dup := lower[strings.ToLower(name)]; dup {
			return nil, pathErr(ErrDuplicateEntry, name, "collides with %q", prev)
		}
		lower[strings.ToLower(name)] = name
		if f.UncompressedSize64 > 1<<62 || f.CompressedSize64 > 1<<62 {
			return nil, pathErr(ErrTooLarge, name, "size overflow")
		}
		usize := int64(f.UncompressedSize64)
		switch name {
		case ManifestName:
			if usize > lim.MaxManifestSize {
				return nil, pathErr(ErrTooLarge, name, "%d bytes, limit %d", usize, lim.MaxManifestSize)
			}
			manifestFile = f
			continue
		case SignatureName:
			if usize > lim.MaxSignatureSize {
				return nil, pathErr(ErrTooLarge, name, "%d bytes, limit %d", usize, lim.MaxSignatureSize)
			}
			sigFile = f
			continue
		}
		if err := CheckPath(name); err != nil {
			return nil, err
		}
		t, ok := FileTypeOf(name)
		if !ok {
			return nil, pathErr(ErrDisallowedFile, name, "extension %q is not on the allow-list", path.Ext(name))
		}
		if max := lim.maxFor(t); usize > max {
			return nil, pathErr(ErrTooLarge, name, "%d bytes, limit %d for %s", usize, max, t)
		}
		if err := checkRatio(f, lim); err != nil {
			return nil, err
		}
		payload++
		if payload > lim.MaxFiles {
			return nil, fmt.Errorf("%w: more than %d payload files", ErrTooManyFiles, lim.MaxFiles)
		}
		total += usize
		if total > lim.MaxTotalSize {
			return nil, fmt.Errorf("%w: payload exceeds %d bytes", ErrTooLarge, lim.MaxTotalSize)
		}
		p.entries[name] = f
	}
	if manifestFile == nil {
		return nil, ErrMissingManifest
	}
	raw, err := readSmall(manifestFile, lim.MaxManifestSize)
	if err != nil {
		return nil, err
	}
	m, err := ParseManifest(raw)
	if err != nil {
		return nil, err
	}
	canonical, err := Canonicalize(raw) // signatures cover the stored document, not a re-encoding
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	p.manifest, p.raw, p.canonical = m, raw, canonical
	listed := map[string]bool{}
	for _, mf := range m.Files {
		listed[mf.Path] = true
		zf, ok := p.entries[mf.Path]
		if !ok {
			return nil, pathErr(ErrMissingFile, mf.Path, "listed in manifest.json but not in the archive")
		}
		if int64(zf.UncompressedSize64) != mf.Size {
			return nil, pathErr(ErrSizeMismatch, mf.Path, "ZIP header says %d bytes, manifest says %d", zf.UncompressedSize64, mf.Size)
		}
	}
	for name := range p.entries {
		if !listed[name] {
			return nil, pathErr(ErrUnlistedFile, name, "present in the archive but not in manifest.json")
		}
	}
	if sigFile != nil {
		rawSig, err := readSmall(sigFile, lim.MaxSignatureSize)
		if err != nil {
			return nil, err
		}
		if p.sig, err = ParseSignatureFile(rawSig); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func checkRatio(f *zip.File, lim Limits) error {
	u, c := int64(f.UncompressedSize64), int64(f.CompressedSize64)
	if u <= lim.RatioCheckMinSize || lim.MaxCompressionRatio <= 0 {
		return nil
	}
	if c == 0 || u/c > lim.MaxCompressionRatio {
		return pathErr(ErrZipBomb, f.Name, "compression ratio above %d:1", lim.MaxCompressionRatio)
	}
	return nil
}

// readSmall reads a whole entry that must be at most max bytes, enforcing the
// limit on the bytes actually produced by the decompressor.
func readSmall(f *zip.File, max int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, pathErr(ErrUnsupported, f.Name, "%v", err)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, max+1))
	if err != nil {
		return nil, pathErr(ErrUnsupported, f.Name, "%v", err)
	}
	if int64(len(b)) > max {
		return nil, pathErr(ErrTooLarge, f.Name, "more than %d bytes", max)
	}
	return b, nil
}

// Manifest returns the parsed manifest. Callers must not modify it.
func (p *Pack) Manifest() *Manifest { return p.manifest }

// ManifestBytes returns manifest.json exactly as stored.
func (p *Pack) ManifestBytes() []byte { return bytes.Clone(p.raw) }

// CanonicalManifest returns the RFC 8785 canonical manifest bytes that signatures cover.
func (p *Pack) CanonicalManifest() []byte { return bytes.Clone(p.canonical) }

// Signature returns the parsed signature.json, or nil when the pack is unsigned.
func (p *Pack) Signature() *SignatureFile { return p.sig }

// Open returns a stream of one payload file. The stream fails with
// ErrContentMismatch or ErrExecutable if the first bytes do not match the file
// type, with ErrSizeMismatch if it produces more or fewer bytes than declared,
// and with ErrHashMismatch at EOF if the SHA-256 differs from the manifest.
// Consumers must treat any error as fatal and discard what they read.
func (p *Pack) Open(name string) (io.ReadCloser, error) {
	mf, ok := p.manifest.File(name)
	if !ok {
		return nil, pathErr(ErrMissingFile, name, "not listed in manifest.json")
	}
	zf := p.entries[name]
	rc, err := zf.Open()
	if err != nil {
		return nil, pathErr(ErrUnsupported, name, "%v", err)
	}
	vr := &verifyingReader{rc: rc, want: mf, h: sha256.New()}
	br := bufio.NewReaderSize(vr, 4096)
	head, err := br.Peek(sniffLen)
	if err != nil && err != io.EOF {
		rc.Close()
		return nil, err
	}
	t, _ := FileTypeOf(name)
	if err := sniff(name, t, head, mf.Size); err != nil {
		rc.Close()
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{br, rc}, nil
}

// verifyingReader counts and hashes bytes and checks them against the manifest at EOF.
type verifyingReader struct {
	rc   io.ReadCloser
	want File
	h    hash.Hash
	n    int64
	done bool
}

func (v *verifyingReader) Read(b []byte) (int, error) {
	if v.done {
		return 0, io.EOF
	}
	n, err := v.rc.Read(b)
	v.n += int64(n)
	if v.n > v.want.Size {
		return 0, pathErr(ErrSizeMismatch, v.want.Path, "more than the declared %d bytes", v.want.Size)
	}
	v.h.Write(b[:n])
	if err == io.EOF {
		v.done = true
		if v.n != v.want.Size {
			return n, pathErr(ErrSizeMismatch, v.want.Path, "%d bytes, declared %d", v.n, v.want.Size)
		}
		if got := hexSum(v.h.Sum(nil)); got != v.want.SHA256 {
			return n, pathErr(ErrHashMismatch, v.want.Path, "got %s", got)
		}
	} else if err != nil {
		// archive/zip reports CRC and length problems as errors; keep them classifiable.
		return n, pathErr(ErrHashMismatch, v.want.Path, "%v", err)
	}
	return n, err
}

// SignatureResult describes one verified signature.
type SignatureResult struct {
	KeyID       string
	Signer      string
	SignedAt    time.Time
	Fingerprint string
	// Trusted is true when the key id resolved to the same public key through
	// VerifyOptions.Keys. An untrusted signature still proves integrity.
	Trusted bool
}

// VerifyOptions configures Verify.
type VerifyOptions struct {
	// Keys resolves trusted public keys. Nil checks integrity only.
	Keys KeyResolver
	// RequireSigned fails with ErrUnsigned when the pack has no signature.json.
	RequireSigned bool
	// RequireTrusted fails with ErrUntrustedKey unless at least one signature is by a trusted key.
	RequireTrusted bool
	// RequireSigner fails with ErrUntrustedKey unless a trusted signature has this signer role
	// (e.g. SignerWorld for items downloaded from World).
	RequireSigner string
}

// VerifyResult summarises a successful Verify.
type VerifyResult struct {
	Files      int
	TotalSize  int64
	Signed     bool
	Signatures []SignatureResult
}

// Trusted reports whether any signature was made with a trusted key.
func (r *VerifyResult) Trusted() bool {
	for _, s := range r.Signatures {
		if s.Trusted {
			return true
		}
	}
	return false
}

// Verify streams every payload file, checking content type, size and SHA-256,
// fully validates JSON payloads and .safetensors headers, and then checks every
// signature. Any cryptographically invalid signature fails verification, even
// when other signatures are valid.
func (p *Pack) Verify(opts VerifyOptions) (*VerifyResult, error) {
	res := &VerifyResult{}
	for _, mf := range p.manifest.Files {
		if err := p.verifyFile(mf); err != nil {
			return nil, err
		}
		res.Files++
		res.TotalSize += mf.Size
	}
	if p.sig == nil {
		if opts.RequireSigned || opts.RequireTrusted || opts.RequireSigner != "" {
			return nil, ErrUnsigned
		}
		return res, nil
	}
	res.Signed = true
	sum := sha256.Sum256(p.canonical)
	if hexSum(sum[:]) != p.sig.ManifestSHA256 {
		return nil, fmt.Errorf("%w: manifest_sha256 does not match the canonical manifest", ErrBadSignature)
	}
	want := signedFiles(p.manifest.Files)
	if len(want) != len(p.sig.Files) {
		return nil, fmt.Errorf("%w: signed file list has %d entries, manifest has %d", ErrBadSignature, len(p.sig.Files), len(want))
	}
	for i := range want {
		if want[i] != p.sig.Files[i] {
			return nil, fmt.Errorf("%w: signed file list differs at %q", ErrBadSignature, want[i].Path)
		}
	}
	signerOK := opts.RequireSigner == ""
	for _, s := range p.sig.Signatures {
		trusted, err := verifySignature(s, p.canonical, want, opts.Keys)
		if err != nil {
			return nil, err
		}
		pub, _ := ParsePublicKey(s.PublicKey)
		res.Signatures = append(res.Signatures, SignatureResult{
			KeyID: s.KeyID, Signer: s.Signer, SignedAt: s.SignedAt, Fingerprint: Fingerprint(pub), Trusted: trusted,
		})
		if trusted && s.Signer == opts.RequireSigner {
			signerOK = true
		}
	}
	if opts.RequireTrusted && !res.Trusted() {
		return nil, fmt.Errorf("%w: no signature by a trusted key", ErrUntrustedKey)
	}
	if !signerOK {
		return nil, fmt.Errorf("%w: no trusted %s signature", ErrUntrustedKey, opts.RequireSigner)
	}
	return res, nil
}

func (p *Pack) verifyFile(mf File) error {
	rc, err := p.Open(mf.Path)
	if err != nil {
		return err
	}
	defer rc.Close()
	t, _ := FileTypeOf(mf.Path)
	switch t {
	case TypeJSON:
		b, err := io.ReadAll(rc)
		if err != nil {
			return err
		}
		if !json.Valid(b) || bytes.HasPrefix(b, []byte("\xEF\xBB\xBF")) {
			return pathErr(ErrContentMismatch, mf.Path, "not valid JSON (UTF-8 without BOM)")
		}
		return nil
	case TypeSafetensors:
		var n [8]byte
		if _, err := io.ReadFull(rc, n[:]); err != nil {
			return pathErr(ErrContentMismatch, mf.Path, "truncated safetensors header")
		}
		hdr := make([]byte, binary.LittleEndian.Uint64(n[:])) // bounded by sniff
		if _, err := io.ReadFull(rc, hdr); err != nil {
			return pathErr(ErrContentMismatch, mf.Path, "truncated safetensors header")
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(bytes.TrimRight(hdr, " "), &obj); err != nil {
			return pathErr(ErrContentMismatch, mf.Path, "safetensors header is not a JSON object")
		}
	}
	_, err = io.Copy(io.Discard, rc)
	return err
}

// Extract verifies the pack and writes manifest.json, signature.json (when
// present) and every payload file below dir, which must exist and should be
// empty. Writes go through os.Root, so no path can escape dir even if the
// directory is modified concurrently. On error, dir may contain partial
// output; extract into a fresh temporary directory and move it into place on
// success.
func (p *Pack) Extract(dir string, opts VerifyOptions) (*VerifyResult, error) {
	res, err := p.Verify(opts)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	write := func(name string, r io.Reader) error {
		if d := path.Dir(name); d != "." {
			if err := root.MkdirAll(d, 0o755); err != nil {
				return err
			}
		}
		f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, r); err != nil {
			f.Close()
			_ = root.Remove(name)
			return err
		}
		return f.Close()
	}
	if err := write(ManifestName, bytes.NewReader(p.raw)); err != nil {
		return nil, err
	}
	if p.sig != nil {
		b, _ := json.MarshalIndent(p.sig, "", "  ")
		if err := write(SignatureName, bytes.NewReader(b)); err != nil {
			return nil, err
		}
	}
	for _, mf := range p.manifest.Files {
		rc, err := p.Open(mf.Path)
		if err != nil {
			return nil, err
		}
		err = write(mf.Path, rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
	}
	return res, nil
}
