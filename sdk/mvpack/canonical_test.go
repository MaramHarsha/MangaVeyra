// SPDX-License-Identifier: MIT
// Copyright (c) 2026 MangaVeyra contributors

package mvpack

import (
	"errors"
	"math"
	"testing"
)

func TestCanonicalizeRFC8785Vectors(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			// RFC 8785 §3.2.2 example.
			"rfc-3.2.2",
			`{
			  "numbers": [333333333.33333329, 1E30, 4.50, 2e-3, 0.000000000000000000000000001],
			  "string": "\u20ac$\u000F\u000aA'\u0042\u0022\u005c\\\"\/",
			  "literals": [null, true, false]
			}`,
			`{"literals":[null,true,false],"numbers":[333333333.3333333,1e+30,4.5,0.002,1e-27],"string":"€$\u000f\nA'B\"\\\\\"/"}`,
		},
		{
			// RFC 8785 §3.2.3 sorting by UTF-16 code units.
			"rfc-3.2.3",
			`{"\u20ac":"Euro Sign","\r":"Carriage Return","\ufb33":"Hebrew Letter Dalet With Dagesh","1":"One",` +
				`"\ud83d\ude00":"Emoji: Grinning Face","\u0080":"Control","\u00f6":"Latin Small Letter O With Diaeresis"}`,
			"{\"\\r\":\"Carriage Return\",\"1\":\"One\",\"\u0080\":\"Control\",\"ö\":\"Latin Small Letter O With Diaeresis\"," +
				"\"€\":\"Euro Sign\",\"😀\":\"Emoji: Grinning Face\",\"\ufb33\":\"Hebrew Letter Dalet With Dagesh\"}",
		},
		{"nested", `{ "b" : [ {"z":1,"a":2} ], "a" : {} }`, `{"a":{},"b":[{"a":2,"z":1}]}`},
		{"scalar", ` "x" `, `"x"`},
		{"empty-array", `[ ]`, `[]`},
		{"line-sep-literal", `"\u2028"`, "\"\u2028\""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Canonicalize([]byte(c.in))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Fatalf("got  %s\nwant %s", got, c.want)
			}
			again, err := Canonicalize(got)
			if err != nil || string(again) != string(got) {
				t.Fatalf("not idempotent: %s (%v)", again, err)
			}
		})
	}
}

func TestFormatES6Number(t *testing.T) {
	cases := map[float64]string{
		0:                       "0",
		math.Copysign(0, -1):    "0",
		1:                       "1",
		-1.5:                    "-1.5",
		1e20:                    "100000000000000000000",
		1e21:                    "1e+21",
		123456789012345680000:   "123456789012345680000",
		0.000001:                "0.000001",
		1e-7:                    "1e-7",
		5e-324:                  "5e-324",
		1.7976931348623157e308:  "1.7976931348623157e+308",
		9007199254740993:        "9007199254740992",
		295147905179352830000.0: "295147905179352830000",
		4.35:                    "4.35",
		0.30000000000000004:     "0.30000000000000004",
	}
	for in, want := range cases {
		if got := FormatES6Number(in); got != want {
			t.Errorf("FormatES6Number(%v) = %s, want %s", in, got, want)
		}
	}
}

func TestCanonicalizeRejects(t *testing.T) {
	for name, in := range map[string]string{
		"duplicate":     `{"a":1,"a":2}`,
		"nested-dup":    `{"x":{"k":1,"k":1}}`,
		"trailing":      `{"a":1} {"b":2}`,
		"invalid-utf8":  "\"\xff\"",
		"overflow":      `1e400`,
		"syntax":        `{"a" 1}`,
		"unterminated":  `[1,2`,
		"empty":         ``,
		"too-deep":      deepArray(MaxCanonicalDepth + 2),
		"bare-trailing": `1 x`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Canonicalize([]byte(in)); !errors.Is(err, ErrCanonical) {
				t.Fatalf("want ErrCanonical, got %v", err)
			}
		})
	}
}

func deepArray(n int) string {
	s := ""
	for range n {
		s += "["
	}
	for range n {
		s += "]"
	}
	return s
}
