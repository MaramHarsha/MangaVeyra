package authui

import "testing"

func TestDeriveUsername(t *testing.T) {
	for _, tc := range []struct{ email, display, want string }{
		{"sora@example.org", "", "sora"},
		{"Ki.Ra-9@example.org", "", "ki.ra-9"},
		{"a@example.org", "", "member"},
		{"", "Mira Tide", "miratide"},
		{"settings@example.org", "", "settings1"},
		{"..x__y..@example.org", "", "x__y"},
	} {
		if got := deriveUsername(tc.email, tc.display); got != tc.want {
			t.Errorf("deriveUsername(%q, %q) = %q, want %q", tc.email, tc.display, got, tc.want)
		}
	}
}
