package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
)

// customModelPrefix names the one image "model" a custom image connection
// (ComfyUI workflow, HTTP endpoint, Hugging Face image endpoint) offers:
// "custom/<provider id>". It is never a catalogue manifest.
const customModelPrefix = "custom/"

// maxCustomConfigBytes bounds a stored description (a ComfyUI workflow or an
// HTTP request template is most of it).
const maxCustomConfigBytes = 256 << 10

// customImageSpec is what a G12 image connection says about itself in the
// Describe step: the largest size and output count it serves and whether its
// model's licence allows commercial use (the licence guard reads it).
type customImageSpec struct {
	MaxWidth   int  `json:"max_width"`
	MaxHeight  int  `json:"max_height"`
	MaxCount   int  `json:"max_count"`
	Commercial bool `json:"commercial"`
}

func (s customImageSpec) valid() bool {
	return s.MaxWidth >= 64 && s.MaxWidth <= 4096 && s.MaxHeight >= 64 && s.MaxHeight <= 4096 && s.MaxCount >= 1 && s.MaxCount <= 4
}

// defaultImageSpec is the description a connection starts with: one 1024²
// image per call and no commercial claim until the admin makes one.
var defaultImageSpec = customImageSpec{MaxWidth: 1024, MaxHeight: 1024, MaxCount: 1}

func customModelID(kind, id, raw string) string {
	if kind == providerKindHuggingFace {
		if cfg, err := decodeHFConfig(raw); err == nil && cfg.Task == hfTaskText {
			return cfg.Model
		}
	}
	return customModelPrefix + id
}

// validateCustomConfig checks a stored or submitted description for a custom
// kind. Unknown fields fail closed.
func validateCustomConfig(kind, base, raw string) error {
	switch kind {
	case providerKindHuggingFace:
		cfg, err := decodeHFConfig(raw)
		if err != nil {
			return err
		}
		return validateHFConfig(base, cfg)
	case providerKindHTTP:
		cfg, err := decodeHTTPConfig(raw)
		if err != nil {
			return err
		}
		return validateHTTPConfig(base, cfg)
	case providerKindComfyUI:
		cfg, err := decodeComfyConfig(raw)
		if err != nil {
			return err
		}
		return validateComfyConfig(base, cfg)
	}
	return errProviderInvalid
}

func decodeStrict(raw string, dst any) error {
	if len(raw) > maxCustomConfigBytes {
		return errProviderInvalid
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var extra any
	if dec.Decode(dst) != nil || dec.Decode(&extra) != io.EOF {
		return errProviderInvalid
	}
	return nil
}

// customModes is a custom connection's effective routing modes (G12 step
// 4), with the local text defaults when nobody chose.
func customModes(modes, defaults []string) []string {
	if len(modes) == 0 {
		return defaults
	}
	return modes
}

func modesAllow(modes []string, mode string) bool {
	for _, m := range modes {
		if m == mode {
			return true
		}
	}
	return false
}

// validServerURL accepts the http(s) origin of a server on this machine or
// network (no credentials, query or fragment) with an optional path.
func validServerURL(raw string, allowPath bool) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	return allowPath || strings.Trim(u.Path, "/") == ""
}

// sameOrigin reports whether ref (absolute, or relative to base) stays on
// base's scheme, host and port. Generated files and status URLs are only
// fetched from the connection's own server.
func sameOrigin(base, ref string) (string, bool) {
	b, err := url.Parse(base)
	if err != nil {
		return "", false
	}
	r, err := url.Parse(ref)
	if err != nil || r.User != nil {
		return "", false
	}
	abs := b.ResolveReference(r)
	if abs.Scheme != b.Scheme || abs.Host != b.Host || abs.Fragment != "" {
		return "", false
	}
	return abs.String(), true
}

var errJSONPath = errors.New("ai: invalid JSON path")

// validJSONPath accepts dot-separated object keys and array indexes, such as
// "data.0.b64_json".
func validJSONPath(path string) bool {
	if path == "" || len(path) > 256 {
		return false
	}
	parts := strings.Split(path, ".")
	if len(parts) > 16 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, r := range p {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
				return false
			}
		}
	}
	return true
}

// lookupJSONPath walks a decoded JSON document along a validated path.
func lookupJSONPath(doc any, path string) (any, error) {
	if !validJSONPath(path) {
		return nil, errJSONPath
	}
	cur := doc
	for _, p := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[p]
			if !ok {
				return nil, errJSONPath
			}
			cur = next
		case []any:
			i := 0
			for _, r := range p {
				if r < '0' || r > '9' || i > 1<<20 {
					return nil, errJSONPath
				}
				i = i*10 + int(r-'0')
			}
			if i >= len(node) {
				return nil, errJSONPath
			}
			cur = node[i]
		default:
			return nil, errJSONPath
		}
	}
	return cur, nil
}

func decodeJSONDoc(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	return doc, nil
}
