package catalog

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

//go:embed manifests/*.yaml
var embedded embed.FS

// Embedded returns the built-in manifest files.
func Embedded() fs.FS {
	sub, _ := fs.Sub(embedded, "manifests")
	return sub
}

// Sources.
const (
	SourceBuiltin = "builtin"
	SourceFile    = "file"
	SourceCustom  = "custom"
	SourcePlugin  = "plugin"
	SourceWorker  = "worker"
)

// Catalog is an immutable set of manifests keyed by id.
type Catalog struct {
	byID  map[string]*Manifest
	order []string
	// Problems lists files or entries that were skipped (shown to admins).
	Problems []string
}

// New builds a catalog from manifests (later entries replace earlier ones with the same id).
func New(ms ...*Manifest) *Catalog {
	c := &Catalog{byID: map[string]*Manifest{}}
	for _, m := range ms {
		c.put(m)
	}
	return c
}

func (c *Catalog) put(m *Manifest) {
	if _, ok := c.byID[m.ID]; !ok {
		c.order = append(c.order, m.ID)
	}
	c.byID[m.ID] = m
}

// Get returns a manifest by id.
func (c *Catalog) Get(id string) (*Manifest, bool) {
	m, ok := c.byID[id]
	return m, ok
}

// All returns every manifest sorted by kind order, then name.
func (c *Catalog) All() []*Manifest {
	out := make([]*Manifest, 0, len(c.byID))
	for _, id := range c.order {
		out = append(out, c.byID[id])
	}
	kindIdx := map[string]int{}
	for i, k := range Kinds {
		kindIdx[k] = i
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return kindIdx[out[i].Kind] < kindIdx[out[j].Kind]
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// ByKind returns the manifests of one kind.
func (c *Catalog) ByKind(kind string) []*Manifest {
	var out []*Manifest
	for _, m := range c.All() {
		if m.Kind == kind {
			out = append(out, m)
		}
	}
	return out
}

// Len returns the number of manifests.
func (c *Catalog) Len() int { return len(c.byID) }

// With returns a new catalog with extra manifests added (replacing same ids).
func (c *Catalog) With(extra ...*Manifest) *Catalog {
	n := &Catalog{byID: make(map[string]*Manifest, len(c.byID)+len(extra)), order: append([]string(nil), c.order...),
		Problems: append([]string(nil), c.Problems...)}
	for k, v := range c.byID {
		n.byID[k] = v
	}
	for _, m := range extra {
		n.put(m)
	}
	return n
}

// Hash returns a stable fingerprint of a manifest (change detection).
func Hash(m *Manifest) string {
	c := *m
	c.Source = ""
	b, _ := json.Marshal(c)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:8])
}

// Load reads the embedded catalogue and then every *.yaml / *.yml / *.json in
// dir (may be "" or missing). A file entry whose id already exists is merged
// over it (maps merge, lists and scalars replace), so an override file can be as
// small as an id plus the changed fields.
func Load(dir string) (*Catalog, error) {
	raw := map[string]map[string]any{}
	src := map[string]string{}
	var order []string
	var problems []string
	add := func(file string, docs []map[string]any, source string) {
		for _, d := range docs {
			id, _ := d["id"].(string)
			if id == "" {
				problems = append(problems, fmt.Sprintf("%s: entry without id", file))
				continue
			}
			if base, ok := raw[id]; ok {
				raw[id] = merge(base, d)
			} else {
				raw[id] = d
				order = append(order, id)
			}
			src[id] = source
		}
	}
	emb := Embedded()
	names, err := fs.Glob(emb, "*.yaml")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	for _, n := range names {
		b, err := fs.ReadFile(emb, n)
		if err != nil {
			return nil, err
		}
		docs, err := parseDocs(n, b)
		if err != nil {
			return nil, fmt.Errorf("catalog: built-in %s: %w", n, err)
		}
		add(n, docs, SourceBuiltin)
	}
	if dir != "" {
		entries, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			problems = append(problems, fmt.Sprintf("%s: %v", dir, err))
		}
		var files []string
		for _, e := range entries {
			ext := strings.ToLower(path.Ext(e.Name()))
			if !e.IsDir() && (ext == ".yaml" || ext == ".yml" || ext == ".json") {
				files = append(files, e.Name())
			}
		}
		sort.Strings(files)
		for _, n := range files {
			b, err := os.ReadFile(filepath.Join(dir, n))
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", n, err))
				continue
			}
			if len(b) > 1<<20 {
				problems = append(problems, fmt.Sprintf("%s: file larger than 1 MiB", n))
				continue
			}
			docs, err := parseDocs(n, b)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", n, err))
				continue
			}
			add(n, docs, SourceFile)
		}
	}
	c := &Catalog{byID: map[string]*Manifest{}}
	for _, id := range order {
		m, err := decode(raw[id])
		if err != nil {
			if src[id] == SourceBuiltin {
				return nil, fmt.Errorf("catalog: built-in %s: %w", id, err)
			}
			problems = append(problems, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		m.Source = src[id]
		c.put(m)
	}
	c.Problems = problems
	return c, nil
}

// Parse decodes one manifest from YAML or JSON and validates it.
func Parse(b []byte) (*Manifest, error) {
	docs, err := parseDocs("manifest.yaml", b)
	if err != nil {
		return nil, err
	}
	if len(docs) != 1 {
		return nil, fmt.Errorf("expected one manifest, found %d", len(docs))
	}
	return decode(docs[0])
}

func parseDocs(name string, b []byte) ([]map[string]any, error) {
	var doc any
	if strings.HasSuffix(strings.ToLower(name), ".json") {
		if err := json.Unmarshal(b, &doc); err != nil {
			return nil, err
		}
	} else if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	doc = normalize(doc)
	if m, ok := doc.(map[string]any); ok {
		if list, ok := m["models"].([]any); ok && m["id"] == nil {
			doc = list
		}
	}
	var items []any
	switch v := doc.(type) {
	case nil:
		return nil, nil
	case []any:
		items = v
	default:
		items = []any{v}
	}
	out := make([]map[string]any, 0, len(items))
	for i, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("entry %d is not a mapping", i)
		}
		out = append(out, m)
	}
	return out, nil
}

// normalize converts YAML's map[any]any (if any) into map[string]any recursively.
func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = normalize(x)
		}
		return t
	case map[any]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			m[fmt.Sprint(k)] = normalize(x)
		}
		return m
	case []any:
		for i, x := range t {
			t[i] = normalize(x)
		}
		return t
	}
	return v
}

func merge(base, over map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		if bm, ok := out[k].(map[string]any); ok {
			if om, ok := v.(map[string]any); ok {
				out[k] = merge(bm, om)
				continue
			}
		}
		out[k] = v
	}
	return out
}

func decode(m map[string]any) (*Manifest, error) {
	b, err := yaml.Marshal(m)
	if err != nil {
		return nil, err
	}
	var man Manifest
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&man); err != nil {
		return nil, err
	}
	if err := man.Validate(); err != nil {
		return nil, err
	}
	return &man, nil
}

// MarshalYAML renders a manifest as YAML (for "export manifest").
func MarshalYAML(m *Manifest) ([]byte, error) { return yaml.Marshal(m) }
