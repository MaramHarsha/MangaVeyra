// SPDX-License-Identifier: MIT
// Copyright (c) 2026 MangaVeyra contributors

package mvpack

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors. Every error returned by this package wraps one of them, so
// callers can classify failures with errors.Is and show a precise reason.
var (
	// ErrUnsafePath: an entry name breaks the path rules (absolute, "..",
	// backslashes, control characters, reserved names, too long or too deep).
	ErrUnsafePath = errors.New("mvpack: unsafe path")
	// ErrDisallowedFile: the file type is not on the allow-list for this pack kind.
	ErrDisallowedFile = errors.New("mvpack: file type not allowed")
	// ErrExecutable: the content looks like an executable, script, WebAssembly
	// module or nested archive. Content packs never carry code.
	ErrExecutable = errors.New("mvpack: executable content not allowed")
	// ErrContentMismatch: the bytes do not match the file extension (sniffing failed).
	ErrContentMismatch = errors.New("mvpack: content does not match file type")
	// ErrTooLarge: a size limit was exceeded.
	ErrTooLarge = errors.New("mvpack: size limit exceeded")
	// ErrTooManyFiles: the archive has more entries than allowed.
	ErrTooManyFiles = errors.New("mvpack: too many files")
	// ErrZipBomb: the compression ratio of an entry is implausibly high.
	ErrZipBomb = errors.New("mvpack: compression ratio too high")
	// ErrDuplicateEntry: two entries share a name (compared case-insensitively).
	ErrDuplicateEntry = errors.New("mvpack: duplicate entry")
	// ErrUnsupported: the ZIP uses encryption, an unknown compression method,
	// symlinks or another feature the format does not allow.
	ErrUnsupported = errors.New("mvpack: unsupported archive feature")
	// ErrMissingManifest: there is no manifest.json at the archive root.
	ErrMissingManifest = errors.New("mvpack: manifest.json missing")
	// ErrInvalidManifest: manifest.json is not valid; see ValidationError for details.
	ErrInvalidManifest = errors.New("mvpack: invalid manifest")
	// ErrUnlistedFile: the archive contains a payload file that the manifest does not list.
	ErrUnlistedFile = errors.New("mvpack: file not listed in manifest")
	// ErrMissingFile: the manifest lists a file that the archive does not contain.
	ErrMissingFile = errors.New("mvpack: listed file missing")
	// ErrHashMismatch: a file's SHA-256 does not match the manifest.
	ErrHashMismatch = errors.New("mvpack: SHA-256 mismatch")
	// ErrSizeMismatch: a file's size does not match the manifest or ZIP header.
	ErrSizeMismatch = errors.New("mvpack: size mismatch")
	// ErrUnsigned: a signature was required but the pack has none.
	ErrUnsigned = errors.New("mvpack: pack is not signed")
	// ErrBadSignature: a signature does not verify or signature.json is malformed.
	ErrBadSignature = errors.New("mvpack: bad signature")
	// ErrUntrustedKey: the signing key is unknown, revoked or does not match the trusted key.
	ErrUntrustedKey = errors.New("mvpack: untrusted signing key")
)

// Problem is one reason a manifest failed validation.
type Problem struct {
	// Field is a JSON-pointer-like location, e.g. "credit.handle" or "files[2].path".
	Field string `json:"field"`
	// Message explains the rule that was broken.
	Message string `json:"message"`
}

// ValidationError lists every problem found in a manifest. It wraps ErrInvalidManifest.
type ValidationError struct {
	Problems []Problem
}

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

func (e *ValidationError) orNil() error {
	if len(e.Problems) == 0 {
		return nil
	}
	return e
}

func pathErr(sentinel error, name, format string, args ...any) error {
	return fmt.Errorf("%w: %q: %s", sentinel, name, fmt.Sprintf(format, args...))
}
