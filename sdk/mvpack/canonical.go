// SPDX-License-Identifier: MIT
// Copyright (c) 2026 MangaVeyra contributors

package mvpack

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// MaxCanonicalDepth bounds the nesting depth accepted by Canonicalize so that
// hostile documents cannot exhaust the stack.
const MaxCanonicalDepth = 64

// Canonicalize returns the canonical form of a JSON document following the JSON
// Canonicalization Scheme (RFC 8785): no insignificant whitespace, object
// members sorted by the UTF-16 code units of their names, strings escaped
// minimally and numbers serialised like ECMAScript's Number.prototype.toString.
//
// Canonicalize rejects documents that JCS cannot represent faithfully or that
// are ambiguous: invalid UTF-8, duplicate object member names, numbers outside
// the IEEE-754 double range, trailing data, and nesting deeper than
// MaxCanonicalDepth.
func Canonicalize(doc []byte) ([]byte, error) {
	if !utf8.Valid(doc) {
		return nil, fmt.Errorf("%w: invalid UTF-8", ErrCanonical)
	}
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var out bytes.Buffer
	if err := canonValue(dec, &out, 0); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing data after JSON value", ErrCanonical)
	}
	return out.Bytes(), nil
}

// CanonicalMarshal marshals v with encoding/json and canonicalizes the result.
func CanonicalMarshal(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return Canonicalize(raw)
}

func canonValue(dec *json.Decoder, out *bytes.Buffer, depth int) error {
	if depth > MaxCanonicalDepth {
		return fmt.Errorf("%w: nesting deeper than %d", ErrCanonical, MaxCanonicalDepth)
	}
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCanonical, err)
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			return canonObject(dec, out, depth)
		case '[':
			return canonArray(dec, out, depth)
		default:
			return fmt.Errorf("%w: unexpected %q", ErrCanonical, t)
		}
	case string:
		writeCanonString(out, t)
	case json.Number:
		s, err := canonNumber(string(t))
		if err != nil {
			return err
		}
		out.WriteString(s)
	case bool:
		if t {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case nil:
		out.WriteString("null")
	default:
		return fmt.Errorf("%w: unexpected token %T", ErrCanonical, tok)
	}
	return nil
}

type canonMember struct {
	key   string
	units []uint16
	value []byte
}

func canonObject(dec *json.Decoder, out *bytes.Buffer, depth int) error {
	var members []canonMember
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("%w: %v", ErrCanonical, err)
		}
		key, ok := tok.(string)
		if !ok {
			return fmt.Errorf("%w: object key is not a string", ErrCanonical)
		}
		if seen[key] {
			return fmt.Errorf("%w: duplicate object member %q", ErrCanonical, key)
		}
		seen[key] = true
		var val bytes.Buffer
		if err := canonValue(dec, &val, depth+1); err != nil {
			return err
		}
		members = append(members, canonMember{key: key, units: utf16.Encode([]rune(key)), value: val.Bytes()})
	}
	if _, err := dec.Token(); err != nil { // closing '}'
		return fmt.Errorf("%w: %v", ErrCanonical, err)
	}
	slices.SortFunc(members, func(a, b canonMember) int { return slices.Compare(a.units, b.units) })
	out.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			out.WriteByte(',')
		}
		writeCanonString(out, m.key)
		out.WriteByte(':')
		out.Write(m.value)
	}
	out.WriteByte('}')
	return nil
}

func canonArray(dec *json.Decoder, out *bytes.Buffer, depth int) error {
	out.WriteByte('[')
	first := true
	for dec.More() {
		if !first {
			out.WriteByte(',')
		}
		first = false
		if err := canonValue(dec, out, depth+1); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil { // closing ']'
		return fmt.Errorf("%w: %v", ErrCanonical, err)
	}
	out.WriteByte(']')
	return nil
}

// writeCanonString writes s as a JSON string using the RFC 8785 escaping rules:
// only '"', '\\' and control characters are escaped; everything else is
// emitted as UTF-8.
func writeCanonString(out *bytes.Buffer, s string) {
	const hex = "0123456789abcdef"
	out.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 0x20 {
				out.WriteString(`\u00`)
				out.WriteByte(hex[r>>4])
				out.WriteByte(hex[r&0xF])
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
}

// canonNumber formats a JSON number the way ECMAScript's Number.prototype.toString
// does for the equivalent IEEE-754 double, as RFC 8785 requires.
func canonNumber(lit string) (string, error) {
	f, err := strconv.ParseFloat(lit, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return "", fmt.Errorf("%w: number %q is not a finite IEEE-754 double", ErrCanonical, lit)
	}
	return FormatES6Number(f), nil
}

// FormatES6Number formats f exactly like ECMAScript's Number.prototype.toString
// (ECMA-262 §6.1.6.1.20) using the shortest round-tripping digit string.
func FormatES6Number(f float64) string {
	if f == 0 {
		return "0" // covers -0 as well
	}
	neg := f < 0
	if neg {
		f = -f
	}
	// Shortest decimal digits and exponent: d.ddddde±x
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mant, expStr, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	exp, _ := strconv.Atoi(expStr)
	k := len(digits)
	n := exp + 1 // position of the decimal point relative to the digits

	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	switch {
	case k <= n && n <= 21:
		b.WriteString(digits)
		b.WriteString(strings.Repeat("0", n-k))
	case 0 < n && n <= 21:
		b.WriteString(digits[:n])
		b.WriteByte('.')
		b.WriteString(digits[n:])
	case -6 < n && n <= 0:
		b.WriteString("0.")
		b.WriteString(strings.Repeat("0", -n))
		b.WriteString(digits)
	default:
		b.WriteByte(digits[0])
		if k > 1 {
			b.WriteByte('.')
			b.WriteString(digits[1:])
		}
		b.WriteByte('e')
		if n-1 >= 0 {
			b.WriteByte('+')
		}
		b.WriteString(strconv.Itoa(n - 1))
	}
	return b.String()
}

// ErrCanonical reports a document that cannot be canonicalized.
var ErrCanonical = errors.New("mvpack: cannot canonicalize JSON")
