package pagedoc

import (
	"encoding/json"
	"errors"
	"math"
)

// Pose uses normalized coordinates. The joint order is head, neck, right
// shoulder/elbow/wrist, left shoulder/elbow/wrist, hip, right knee/ankle,
// left knee/ankle. No executable workflow or external resource is accepted.
type Pose struct {
	Schema string  `json:"schema"`
	Preset string  `json:"preset,omitempty"`
	Joints []Point `json:"joints"`
}

var PoseEdges = [][2]int{{0, 1}, {1, 2}, {2, 3}, {3, 4}, {1, 5}, {5, 6}, {6, 7}, {1, 8}, {8, 9}, {9, 10}, {8, 11}, {11, 12}}

func PosePresets() map[string]Pose {
	return map[string]Pose{
		"leap":   {Schema: "mangaveyra.pose.v1", Preset: "leap", Joints: []Point{{.55, .13}, {.51, .26}, {.43, .29}, {.27, .18}, {.15, .10}, {.59, .29}, {.73, .17}, {.84, .08}, {.45, .55}, {.25, .65}, {.15, .89}, {.68, .61}, {.82, .77}}},
		"crouch": {Schema: "mangaveyra.pose.v1", Preset: "crouch", Joints: []Point{{.55, .25}, {.5, .38}, {.4, .4}, {.25, .59}, {.16, .75}, {.6, .4}, {.7, .57}, {.82, .7}, {.47, .63}, {.24, .7}, {.17, .9}, {.73, .7}, {.85, .9}}},
		"run":    {Schema: "mangaveyra.pose.v1", Preset: "run", Joints: []Point{{.58, .1}, {.54, .25}, {.44, .29}, {.28, .4}, {.4, .52}, {.62, .29}, {.78, .2}, {.68, .1}, {.45, .54}, {.28, .72}, {.1, .7}, {.66, .67}, {.6, .93}}},
	}
}

func DecodePose(raw map[string]any) (Pose, error) {
	var out Pose
	if len(raw) == 0 {
		return out, nil
	}
	if len(raw) > 3 {
		return out, errorsPose
	}
	b, err := json.Marshal(raw)
	if err != nil || len(b) > 4096 {
		return out, errorsPose
	}
	if json.Unmarshal(b, &out) != nil || out.Schema != "mangaveyra.pose.v1" || len(out.Joints) != 13 {
		return out, errorsPose
	}
	for k := range raw {
		if k != "schema" && k != "preset" && k != "joints" {
			return out, errorsPose
		}
	}
	if out.Preset != "" {
		if _, ok := PosePresets()[out.Preset]; !ok {
			return out, errorsPose
		}
	}
	for _, p := range out.Joints {
		if math.IsNaN(p.X) || math.IsNaN(p.Y) || math.IsInf(p.X, 0) || math.IsInf(p.Y, 0) || p.X < 0 || p.X > 1 || p.Y < 0 || p.Y > 1 {
			return out, errorsPose
		}
	}
	return out, nil
}

var errorsPose = errors.New("invalid pose")

// Earlier API clients could keep opaque pose metadata. Retain bounded legacy
// data on saves; the new interactive schema is validated strictly. Generation
// only accepts the explicit supported schema and never dispatches arbitrary data.
func ValidatePose(raw map[string]any) error {
	if raw["schema"] == "mangaveyra.pose.v1" {
		_, err := DecodePose(raw)
		return err
	}
	b, err := json.Marshal(raw)
	if err != nil || len(b) > 4096 {
		return errorsPose
	}
	return nil
}
