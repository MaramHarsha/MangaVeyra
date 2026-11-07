package ai

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"go.yaml.in/yaml/v3"
)

// ModelManifest is the validated catalogue format. Prices and remote endpoints
// are deliberately absent: only a compiled adapter may quote remote spending.
type ModelManifest struct {
	ID         string         `json:"id" yaml:"id"`
	Name       string         `json:"name" yaml:"name"`
	Maker      string         `json:"maker" yaml:"maker"`
	Kind       string         `json:"kind" yaml:"kind"`
	Licence    string         `json:"licence" yaml:"licence"`
	Commercial bool           `json:"commercial" yaml:"commercial"`
	Providers  []string       `json:"providers" yaml:"providers"`
	References int            `json:"references" yaml:"references"`
	TextInArt  bool           `json:"text_in_art" yaml:"text_in_art"`
	BW         bool           `json:"bw" yaml:"bw"`
	Edit       bool           `json:"edit" yaml:"edit"`
	Mask       bool           `json:"mask" yaml:"mask"`
	MaxWidth   int            `json:"max_width" yaml:"max_width"`
	MaxHeight  int            `json:"max_height" yaml:"max_height"`
	MaxCount   int            `json:"max_count" yaml:"max_count"`
	VRAMMB     int            `json:"vram_mb" yaml:"vram_mb"`
	MinVRAMMB  int            `json:"min_vram_mb" yaml:"min_vram_mb"`
	Quality    float64        `json:"quality" yaml:"quality"`
	Speed      float64        `json:"speed" yaml:"speed"`
	GPUSeconds float64        `json:"gpu_seconds" yaml:"gpu_seconds"`
	Worker     map[string]any `json:"worker,omitempty" yaml:"worker,omitempty"`
}

//go:embed catalog/*.yaml
var catalogueFiles embed.FS

type modelCatalog struct {
	db        *db.DB
	directory string
}

func validModelID(id string) bool {
	if id == "" || len(id) > 160 || strings.Contains(id, "..") {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == '/') {
			return false
		}
	}
	return true
}

func validateManifest(m ModelManifest) error {
	if !validModelID(m.ID) || strings.TrimSpace(m.Name) == "" || len(m.Name) > 160 || len(m.Maker) > 160 || m.Kind != "image" || m.Licence == "" || len(m.Providers) == 0 || len(m.Providers) > 8 || m.References < 0 || m.References > 32 || m.MaxWidth < 64 || m.MaxWidth > 4096 || m.MaxHeight < 64 || m.MaxHeight > 4096 || m.MaxCount < 1 || m.MaxCount > 4 || m.VRAMMB < 0 || m.VRAMMB > 512000 {
		return errors.New("ai: invalid model manifest")
	}
	for _, v := range []float64{m.Quality, m.Speed, m.GPUSeconds} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 86400 {
			return errors.New("ai: invalid model score")
		}
	}
	for _, p := range m.Providers {
		if p != PlaceholderID && p != providerKindOpenRouter && p != providerKindOpenAI && p != providerKindFal && p != providerKindWorker {
			return errors.New("ai: unsupported model adapter")
		}
	}
	if m.Mask && !m.Edit {
		return errors.New("ai: mask requires editing")
	}
	return nil
}

func decodeManifest(data []byte) (ModelManifest, error) {
	var m ModelManifest
	if len(data) > 256<<10 {
		return m, errors.New("ai: manifest too large")
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return m, errors.New("ai: invalid model manifest")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return m, errors.New("ai: multiple manifest documents")
	}
	return m, validateManifest(m)
}

func (c *modelCatalog) list(ctx context.Context) ([]ModelManifest, error) {
	byID := map[string]ModelManifest{}
	entries, err := catalogueFiles.ReadDir("catalog")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		b, err := catalogueFiles.ReadFile("catalog/" + e.Name())
		if err != nil {
			return nil, err
		}
		m, err := decodeManifest(b)
		if err != nil {
			return nil, fmt.Errorf("ai: embedded catalogue: %w", err)
		}
		byID[m.ID] = m
	}
	if c.directory != "" {
		entries, err := os.ReadDir(c.directory)
		if err != nil && !os.IsNotExist(err) {
			return nil, errors.New("ai: catalogue unavailable")
		}
		if len(entries) > 256 {
			return nil, errors.New("ai: too many manifests")
		}
		for _, e := range entries {
			if e.IsDir() || e.Type()&os.ModeSymlink != 0 || !(strings.HasSuffix(e.Name(), ".yaml") || strings.HasSuffix(e.Name(), ".yml") || strings.HasSuffix(e.Name(), ".json")) {
				continue
			}
			f, err := os.Open(filepath.Join(c.directory, e.Name()))
			if err != nil {
				return nil, errors.New("ai: catalogue unavailable")
			}
			b, err := io.ReadAll(io.LimitReader(f, (256<<10)+1))
			f.Close()
			if err != nil {
				return nil, err
			}
			m, err := decodeManifest(b)
			if err != nil {
				return nil, err
			}
			if m.ID == PlaceholderArt {
				return nil, errors.New("ai: placeholder cannot be overridden")
			}
			byID[m.ID] = m
		}
	}
	// Global file/admin overrides only. Personal custom text models are tied to
	// their provider and do not get to replace another user's image catalogue.
	if c.db != nil {
		var rows []struct {
			ID       string `db:"id"`
			Manifest string `db:"manifest"`
			Enabled  bool   `db:"enabled"`
		}
		if err := c.db.Select(ctx, &rows, `SELECT id, manifest, enabled FROM models WHERE provider_id IS NULL AND owner_id IS NULL ORDER BY id LIMIT 257`); err != nil {
			return nil, err
		}
		if len(rows) > 256 {
			return nil, errors.New("ai: too many manifests")
		}
		for _, row := range rows {
			if row.ID == PlaceholderArt {
				continue
			}
			if !row.Enabled {
				delete(byID, row.ID)
				continue
			}
			m, err := decodeManifest([]byte(row.Manifest))
			if err != nil || m.ID != row.ID {
				return nil, errors.New("ai: invalid catalogue override")
			}
			byID[m.ID] = m
		}
	}
	out := make([]ModelManifest, 0, len(byID))
	for _, m := range byID {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (c *modelCatalog) lookup(ctx context.Context, id string) (ModelManifest, error) {
	ms, err := c.list(ctx)
	if err != nil {
		return ModelManifest{}, err
	}
	for _, m := range ms {
		if m.ID == id {
			return m, nil
		}
	}
	return ModelManifest{}, ErrNoProvider
}
func (c *modelCatalog) workerManifest(ctx context.Context, id string) (json.RawMessage, error) {
	m, err := c.lookup(ctx, id)
	if err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

func modelSupports(m ModelManifest, n ImageNeeds) bool {
	return n.References <= m.References && (!n.TextInArt || m.TextInArt) && (!n.BW || m.BW) && (!n.Edit || m.Edit) && (!n.Mask || m.Mask) && n.Width <= m.MaxWidth && n.Height <= m.MaxHeight && n.Count <= m.MaxCount
}
func hasProvider(m ModelManifest, p string) bool {
	for _, v := range m.Providers {
		if v == p {
			return true
		}
	}
	return false
}
