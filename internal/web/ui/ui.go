// Package ui is the MangaVeyra templ component kit: every component of the
// design system (design file 04) as a server-rendered templ component using
// the shared CSS classes from web/src/styles (mv-btn, mv-field, mv-toast…).
//
// Components never emit inline styles (the CSP forbids them); dynamic values
// such as progress use the percentage utility classes (Pct).
package ui

import (
	"fmt"
	"math"
	"strings"

	"github.com/a-h/templ"
)

// Variant is a button kind.
type Variant string

// Button kinds (design 04 §06).
const (
	Primary       Variant = "primary"
	Secondary     Variant = "secondary"
	Ghost         Variant = "ghost"
	Danger        Variant = "danger"
	DangerOutline Variant = "danger-outline"
	Quiet         Variant = "quiet"
	IconOnly      Variant = "icon"
	Bare          Variant = "bare"
)

// Size is a control size: 32 dense, 40 desktop, 48 touch.
type Size string

// Control sizes.
const (
	SM Size = "sm"
	MD Size = ""
	LG Size = "lg"
)

// Tone is a status colour used by badges, toasts and notices. Status is never
// shown by colour alone: components always pair it with an icon and a word.
type Tone string

// Tones.
const (
	ToneNeutral Tone = ""
	ToneSuccess Tone = "success"
	ToneInfo    Tone = "info"
	ToneWarning Tone = "warning"
	ToneError   Tone = "error"
	ToneText    Tone = "text"
	ToneMuted   Tone = "muted"
	ToneAccent  Tone = "accent"
	ToneNew     Tone = "new"
)

// Cls joins non-empty class names.
func Cls(parts ...string) string {
	out := parts[:0:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

// If returns s when cond is true.
func If(cond bool, s string) string {
	if cond {
		return s
	}
	return ""
}

// Pct returns the percentage utility class ("mv-p-62") for v in [0,1] (clamped).
func Pct(v float64) string {
	if math.IsNaN(v) {
		v = 0
	}
	return fmt.Sprintf("mv-p-%d", int(math.Round(math.Max(0, math.Min(1, v))*100)))
}

// PctOf returns Pct(n / total), or mv-p-0 when total is 0.
func PctOf(n, total float64) string {
	if total <= 0 {
		return "mv-p-0"
	}
	return Pct(n / total)
}

// Icon returns the Tabler icon class list for name (e.g. "sparkles").
func iconClass(name string, size int) string {
	c := "ti ti-" + name
	if size > 0 && size != 18 {
		c += fmt.Sprintf(" mv-ic-%d", size)
	}
	return c
}

// Attrs merges attribute maps (later wins).
func Attrs(maps ...templ.Attributes) templ.Attributes {
	out := templ.Attributes{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// Initials returns up to two initials for an avatar.
func Initials(name string) string {
	f := strings.Fields(strings.TrimSpace(strings.TrimPrefix(name, "@")))
	switch len(f) {
	case 0:
		return "?"
	case 1:
		r := []rune(f[0])
		if len(r) > 1 {
			return strings.ToUpper(string(r[:2]))
		}
		return strings.ToUpper(string(r))
	}
	a, b := []rune(f[0]), []rune(f[len(f)-1])
	return strings.ToUpper(string(a[0]) + string(b[0]))
}

// AvatarColor picks one of six avatar colours from a stable key (user id or name).
func AvatarColor(key string) string {
	var h uint32 = 2166136261
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	return fmt.Sprintf("mv-avatar--c%d", h%6+1)
}

// Rank letters from lowest to highest (design 04 §16b).
var Ranks = []string{"E", "D", "C", "B", "A", "S"}

// FormatInt formats n with thousands separators (1,204).
func FormatInt(n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
