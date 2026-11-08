package pagedoc

import (
	"encoding/json"
	"math"
	"testing"
)

func TestPoseBoundsAndLegacyMetadata(t *testing.T) {
	for name, p := range PosePresets() {
		b, _ := json.Marshal(p)
		var raw map[string]any
		_ = json.Unmarshal(b, &raw)
		if _, err := DecodePose(raw); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	legacy := map[string]any{"keypoints": []any{[]float64{.5, .1}}}
	if ValidatePose(legacy) != nil {
		t.Fatal("legacy metadata lost")
	}
	if _, err := DecodePose(legacy); err == nil {
		t.Fatal("opaque legacy payload became executable pose control")
	}
	for _, x := range []float64{-.1, 1.1, math.NaN(), math.Inf(1)} {
		p := PosePresets()["run"]
		p.Joints[0].X = x
		raw := map[string]any{"schema": p.Schema, "joints": p.Joints}
		if ValidatePose(raw) == nil {
			t.Fatalf("accepted unbounded joint %v", x)
		}
	}
}
