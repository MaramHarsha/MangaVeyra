package studioui

import (
	"math"
	"testing"
)

func TestLockPercentDoesNotRoundDriftIntoThePassingBoundary(t *testing.T) {
	for _, tt := range []struct {
		score float64
		want  string
	}{{.899999, "89%"}, {math.Nextafter(.9, 0), "89%"}, {.9, "90%"}, {.9003, "90%"}, {.58, "58%"}, {1, "100%"}} {
		if got := lockPercent(&tt.score); got != tt.want {
			t.Fatalf("score %v rendered %s; want %s", tt.score, got, tt.want)
		}
	}
	if got := lockPercent(nil); got != "" {
		t.Fatalf("missing evidence rendered %s", got)
	}
}
