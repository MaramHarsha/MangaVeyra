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
	"io"
	"strings"
	"time"
)

// Writer streams a new pack. Add payload files with AddFile, then call Close,
// which writes manifest.json (with the collected sizes and hashes) and, when
// signers are given, signature.json. Output is deterministic: the same inputs
// produce byte-identical packs.
type Writer struct {
	zw      *zip.Writer
	m       Manifest
	limits  Limits
	seen    map[string]bool
	total   int64
	modTime time.Time
	closed  bool
	err     error
}

// NewWriter starts a pack on w. m describes the pack; its Files must be empty
// (they are filled in as files are added) and CreatedAt defaults to now.
func NewWriter(w io.Writer, m Manifest) (*Writer, error) {
	if len(m.Files) != 0 {
		return nil, errors.New("mvpack: NewWriter: manifest files are filled in by AddFile; pass an empty list")
	}
	if m.Format == 0 {
		m.Format = FormatVersion
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	m.CreatedAt = m.CreatedAt.UTC().Truncate(time.Second)
	m.Files = []File{}
	return &Writer{zw: zip.NewWriter(w), m: m, limits: DefaultLimits(), seen: map[string]bool{}, modTime: m.CreatedAt}, nil
}

// SetLimits replaces the size limits enforced while writing.
func (w *Writer) SetLimits(l Limits) { w.limits = l }

// AddBytes adds a payload file from memory.
func (w *Writer) AddBytes(name string, role Role, data []byte) error {
	return w.AddFile(name, role, bytes.NewReader(data))
}

// AddFile streams a payload file into the pack, checking the path rules, the
// allow-list, the role, the content type and the size limits as it goes.
func (w *Writer) AddFile(name string, role Role, r io.Reader) (err error) {
	if w.closed {
		return errors.New("mvpack: writer is closed")
	}
	if w.err != nil {
		return w.err
	}
	if err := CheckPath(name); err != nil {
		return err
	}
	t, ok := FileTypeOf(name)
	if !ok {
		return pathErr(ErrDisallowedFile, name, "extension is not on the allow-list")
	}
	if !RoleAccepts(role, t) {
		return pathErr(ErrDisallowedFile, name, "role %q does not accept %s files", role, t)
	}
	if t.IsFont() && w.m.Kind != KindPlugin {
		return pathErr(ErrDisallowedFile, name, "fonts are only allowed in plugin packs")
	}
	if w.seen[strings.ToLower(name)] {
		return pathErr(ErrDuplicateEntry, name, "already added")
	}
	if len(w.m.Files) >= w.limits.MaxFiles {
		return fmt.Errorf("%w: more than %d files", ErrTooManyFiles, w.limits.MaxFiles)
	}
	br := bufio.NewReaderSize(r, 4096)
	head, perr := br.Peek(sniffLen)
	if perr != nil && perr != io.EOF {
		return perr
	}
	if err := sniff(name, t, head, -1); err != nil && !(t == TypeSafetensors && errors.Is(err, ErrContentMismatch) && safetensorsHeadOK(head)) {
		return err
	}

	method := zip.Store
	if t == TypeJSON {
		method = zip.Deflate
	}
	fw, err := w.zw.CreateHeader(&zip.FileHeader{Name: name, Method: method, Modified: w.modTime})
	if err != nil {
		return w.fail(err)
	}
	max := w.limits.maxFor(t)
	h := sha256.New()
	var src io.Reader = io.LimitReader(br, max+1)
	var jsonBuf *bytes.Buffer
	if t == TypeJSON {
		jsonBuf = &bytes.Buffer{}
		src = io.TeeReader(src, jsonBuf)
	}
	n, err := io.Copy(io.MultiWriter(fw, h), src)
	if err != nil {
		return w.fail(err)
	}
	if n > max {
		return w.fail(pathErr(ErrTooLarge, name, "more than %d bytes for %s", max, t))
	}
	if jsonBuf != nil && (!json.Valid(jsonBuf.Bytes()) || bytes.HasPrefix(jsonBuf.Bytes(), []byte("\xEF\xBB\xBF"))) {
		return w.fail(pathErr(ErrContentMismatch, name, "not valid JSON (UTF-8 without BOM)"))
	}
	if t == TypeSafetensors && int64(binary.LittleEndian.Uint64(head[:8]))+8 > n {
		return w.fail(pathErr(ErrContentMismatch, name, "safetensors header longer than the file"))
	}
	w.total += n
	if w.total > w.limits.MaxTotalSize {
		return w.fail(fmt.Errorf("%w: payload exceeds %d bytes", ErrTooLarge, w.limits.MaxTotalSize))
	}
	w.seen[strings.ToLower(name)] = true
	w.m.Files = append(w.m.Files, File{Path: name, Role: role, MediaType: t.MediaType(), Size: n, SHA256: hexSum(h.Sum(nil))})
	return nil
}

// safetensorsHeadOK accepts a safetensors head whose only problem is that the
// total size is not known yet (the writer checks it after streaming).
func safetensorsHeadOK(head []byte) bool {
	if len(head) < 9 {
		return false
	}
	n := binary.LittleEndian.Uint64(head[:8])
	return n >= 2 && n <= MaxSafetensorsHeader && head[8] == '{'
}

// fail poisons the writer: a partially written entry cannot be removed from a
// ZIP stream, so the pack must be discarded.
func (w *Writer) fail(err error) error {
	w.err = fmt.Errorf("mvpack: pack is unusable after a failed AddFile: %w", err)
	return err
}

// Manifest returns a copy of the manifest as it would be written now.
func (w *Writer) Manifest() Manifest {
	m := w.m
	m.Files = append([]File(nil), w.m.Files...)
	return m
}

// Close validates the manifest, writes manifest.json and, when signers are
// given, signature.json, and finishes the archive. It returns the final
// manifest. Close does not close the underlying io.Writer.
func (w *Writer) Close(signers ...*Signer) (*Manifest, error) {
	if w.closed {
		return nil, errors.New("mvpack: writer is closed")
	}
	w.closed = true
	if w.err != nil {
		return nil, w.err
	}
	m := w.Manifest()
	if err := m.Validate(); err != nil {
		return nil, err
	}
	canonical, err := m.CanonicalBytes()
	if err != nil {
		return nil, err
	}
	pretty, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := w.writeEntry(ManifestName, append(pretty, '\n')); err != nil {
		return nil, err
	}
	if len(signers) > 0 {
		sf, err := newSignatureFile(canonical, m.Files, signers)
		if err != nil {
			return nil, err
		}
		b, _ := json.MarshalIndent(sf, "", "  ")
		if err := w.writeEntry(SignatureName, append(b, '\n')); err != nil {
			return nil, err
		}
	}
	if err := w.zw.Close(); err != nil {
		return nil, err
	}
	return &m, nil
}

func (w *Writer) writeEntry(name string, data []byte) error {
	fw, err := w.zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: w.modTime})
	if err != nil {
		return err
	}
	_, err = fw.Write(data)
	return err
}

// AddSignature copies pack p to dst and appends a signature by s, for example
// World's countersignature on an approved item. Existing signatures are kept.
// The pack is fully verified (integrity only) before it is re-signed.
func AddSignature(p *Pack, dst io.Writer, s *Signer) error {
	if _, err := p.Verify(VerifyOptions{}); err != nil {
		return err
	}
	sig, err := s.Sign(p.canonical, signedFiles(p.manifest.Files))
	if err != nil {
		return err
	}
	sf := &SignatureFile{Format: SignatureFormat, Files: signedFiles(p.manifest.Files)}
	sum := sha256.Sum256(p.canonical)
	sf.ManifestSHA256 = hexSum(sum[:])
	if p.sig != nil {
		sf.Signatures = append(sf.Signatures, p.sig.Signatures...)
	}
	for _, old := range sf.Signatures {
		if old.KeyID == sig.KeyID {
			return fmt.Errorf("%w: pack is already signed by key %s", ErrBadSignature, sig.KeyID)
		}
	}
	sf.Signatures = append(sf.Signatures, sig)
	if len(sf.Signatures) > 8 {
		return fmt.Errorf("%w: at most 8 signatures", ErrBadSignature)
	}
	zw := zip.NewWriter(dst)
	for _, f := range p.zr.File {
		if f.Name == SignatureName {
			continue
		}
		if err := zw.Copy(f); err != nil {
			return err
		}
	}
	b, _ := json.MarshalIndent(sf, "", "  ")
	fw, err := zw.CreateHeader(&zip.FileHeader{Name: SignatureName, Method: zip.Deflate, Modified: p.manifest.CreatedAt})
	if err != nil {
		return err
	}
	if _, err := fw.Write(append(b, '\n')); err != nil {
		return err
	}
	return zw.Close()
}
