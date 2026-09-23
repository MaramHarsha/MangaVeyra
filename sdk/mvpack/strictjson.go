// SPDX-License-Identifier: MIT
// Copyright (c) 2026 MangaVeyra contributors

package mvpack

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrMemberName reports an object member name that strict decoding could
// confuse with another one (see CheckMemberNames).
var ErrMemberName = errors.New("mvpack: member names must be lowercase snake case")

// CheckMemberNames rejects documents whose object member names are not
// lowercase snake case (ASCII a–z, 0–9 and '_'). Members of the top-level
// object whose names start with extPrefix (when extPrefix is not empty) are
// exempt, and their values are not inspected.
//
// encoding/json matches member names to struct fields case-insensitively, even
// with DisallowUnknownFields, so without this check a document such as
// {"ai_label": {"art": "human", "ART": "ai"}} passes the duplicate-member rule
// of Canonicalize (the names differ), is covered by a signature as written, and
// then decodes as "ai" in Go while a case-sensitive reader shows "human" — a
// parser differential on signed, reviewed content. Every field name of the
// pack, signature, licence-token and plugin formats is lowercase snake case, so
// the rule costs nothing for valid documents.
func CheckMemberNames(doc []byte, extPrefix string) error {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	if err := checkMemberNames(dec, 0, extPrefix); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("%w: trailing data after JSON value", ErrCanonical)
	}
	return nil
}

func checkMemberNames(dec *json.Decoder, depth int, extPrefix string) error {
	if depth > MaxCanonicalDepth {
		return fmt.Errorf("%w: nesting deeper than %d", ErrCanonical, MaxCanonicalDepth)
	}
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCanonical, err)
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '[':
		for dec.More() {
			if err := checkMemberNames(dec, depth+1, extPrefix); err != nil {
				return err
			}
		}
	case '{':
		for dec.More() {
			tok, err := dec.Token()
			if err != nil {
				return fmt.Errorf("%w: %v", ErrCanonical, err)
			}
			name, _ := tok.(string)
			if depth == 0 && extPrefix != "" && strings.HasPrefix(name, extPrefix) {
				var skip json.RawMessage
				if err := dec.Decode(&skip); err != nil {
					return fmt.Errorf("%w: %v", ErrCanonical, err)
				}
				continue
			}
			if !snakeCase(name) {
				return fmt.Errorf("%w: %q", ErrMemberName, name)
			}
			if err := checkMemberNames(dec, depth+1, extPrefix); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%w: unexpected %q", ErrCanonical, delim)
	}
	if _, err := dec.Token(); err != nil { // the closing ']' or '}'
		return fmt.Errorf("%w: %v", ErrCanonical, err)
	}
	return nil
}

func snakeCase(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}
