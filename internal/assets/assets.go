// Package assets stores binary files (generated art, uploads, exports) in the
// configured storage.Store and records them in the assets table. Every module
// that keeps a file goes through this service so that ownership, size, image
// dimensions, checksums and signed URLs are handled in one place.
//
// The service is provided on core.Deps by the module in this package; use it
// with core.MustUse[*assets.Service](d).
package assets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
)

// Sources accepted by the assets table.
const (
	SourceUpload = "upload"
	SourceAI     = "ai"
	SourceWorker = "worker"
	SourceImport = "import"
	SourceSystem = "system"
	SourceWorld  = "world"
	SourcePack   = "pack"
)

// MaxBytes is the largest file Save accepts (64 MiB).
const MaxBytes = 64 << 20

// ErrNotFound is returned when an asset ID does not exist.
var ErrNotFound = errors.New("assets: not found")

// Asset is one stored file.
type Asset struct {
	ID          string          `json:"id" db:"id"`
	Key         string          `json:"-" db:"key"`
	ContentType string          `json:"content_type" db:"content_type"`
	Bytes       int64           `json:"bytes" db:"bytes"`
	Width       int             `json:"width" db:"width"`
	Height      int             `json:"height" db:"height"`
	DurationMS  int             `json:"duration_ms" db:"duration_ms"`
	SHA256      string          `json:"sha256" db:"sha256"`
	Kind        string          `json:"kind" db:"kind"`
	Source      string          `json:"source" db:"source"`
	OwnerID     *string         `json:"owner_id,omitempty" db:"owner_id"`
	ProjectID   *string         `json:"project_id,omitempty" db:"project_id"`
	Meta        json.RawMessage `json:"meta" db:"meta"`
	CreatedAt   time.Time       `json:"created_at" db:"created_at"`
}

// SaveParams describes a file to store.
type SaveParams struct {
	// OwnerID and ProjectID attribute the file (both optional).
	OwnerID, ProjectID string
	// Kind is a free label such as "layer", "character_ref", "export", "upload".
	Kind string
	// Source is one of the Source* constants (default SourceUpload).
	Source string
	// ContentType is sniffed from Data when empty.
	ContentType string
	// Data is the file content (at most MaxBytes).
	Data []byte
	// Meta is stored as JSON (nil → {}).
	Meta any
	// Prefix is the storage key prefix (default "projects/<project>/<kind>" or "assets/<kind>").
	Prefix string
}

// Service stores and reads assets.
type Service struct {
	db    *db.DB
	store storage.Store
	media *storage.Signer
}

// New creates the service.
func New(d *db.DB, st storage.Store, media *storage.Signer) *Service {
	return &Service{db: d, store: st, media: media}
}

// Save writes the file to storage and inserts its row. When the insert fails
// the stored object is removed again.
func (s *Service) Save(ctx context.Context, p SaveParams) (Asset, error) {
	a, err := s.put(ctx, p)
	if err != nil {
		return Asset{}, err
	}
	if err := insert(ctx, s.db, &a); err != nil {
		_ = s.store.Delete(context.WithoutCancel(ctx), a.Key)
		return Asset{}, err
	}
	return a, nil
}

// SaveTx writes the file to storage and inserts its row inside tx. If tx later
// rolls back, the stored object is orphaned (harmless; a sweeper may remove
// objects without rows).
func (s *Service) SaveTx(ctx context.Context, tx *db.Tx, p SaveParams) (Asset, error) {
	a, err := s.put(ctx, p)
	if err != nil {
		return Asset{}, err
	}
	if err := insert(ctx, tx, &a); err != nil {
		_ = s.store.Delete(context.WithoutCancel(ctx), a.Key)
		return Asset{}, err
	}
	return a, nil
}

func (s *Service) put(ctx context.Context, p SaveParams) (Asset, error) {
	if len(p.Data) == 0 {
		return Asset{}, errors.New("assets: empty file")
	}
	if len(p.Data) > MaxBytes {
		return Asset{}, fmt.Errorf("assets: file is larger than %d MiB", MaxBytes>>20)
	}
	ct := strings.TrimSpace(p.ContentType)
	if ct == "" || ct == "application/octet-stream" {
		ct = storage.DetectContentType(p.Data)
	}
	src := p.Source
	if src == "" {
		src = SourceUpload
	}
	switch src {
	case SourceUpload, SourceAI, SourceWorker, SourceImport, SourceSystem, SourceWorld, SourcePack:
	default:
		return Asset{}, fmt.Errorf("assets: unknown source %q", src)
	}
	kind := p.Kind
	if kind == "" {
		kind = "upload"
	}
	prefix := p.Prefix
	if prefix == "" {
		if p.ProjectID != "" {
			prefix = "projects/" + p.ProjectID + "/" + kind
		} else {
			prefix = "assets/" + kind
		}
	}
	meta := []byte("{}")
	if p.Meta != nil {
		b, err := json.Marshal(p.Meta)
		if err != nil {
			return Asset{}, fmt.Errorf("assets: meta: %w", err)
		}
		meta = b
	}
	sum := sha256.Sum256(p.Data)
	a := Asset{
		ID:          db.NewID(),
		Key:         storage.NewKey(prefix, ct),
		ContentType: ct,
		Bytes:       int64(len(p.Data)),
		SHA256:      hex.EncodeToString(sum[:]),
		Kind:        kind,
		Source:      src,
		Meta:        meta,
		CreatedAt:   db.Now(),
	}
	if p.OwnerID != "" {
		a.OwnerID = &p.OwnerID
	}
	if p.ProjectID != "" {
		a.ProjectID = &p.ProjectID
	}
	if strings.HasPrefix(ct, "image/") {
		if cfg, _, err := storage.ImageConfig(p.Data, storage.DefaultImageLimits); err == nil {
			a.Width, a.Height = cfg.Width, cfg.Height
		} else if errors.Is(err, storage.ErrImageTooLarge) {
			return Asset{}, err
		}
	}
	if err := storage.PutBytes(ctx, s.store, a.Key, p.Data, ct); err != nil {
		return Asset{}, fmt.Errorf("assets: store: %w", err)
	}
	return a, nil
}

func insert(ctx context.Context, q db.Querier, a *Asset) error {
	_, err := q.Exec(ctx, `INSERT INTO assets (id, key, content_type, bytes, width, height, duration_ms, sha256, kind, source,
		owner_id, project_id, meta, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.Key, a.ContentType, a.Bytes, a.Width, a.Height, a.DurationMS, a.SHA256, a.Kind, a.Source,
		nullPtr(a.OwnerID), nullPtr(a.ProjectID), string(a.Meta), a.CreatedAt)
	if err != nil {
		return fmt.Errorf("assets: insert: %w", err)
	}
	return nil
}

func nullPtr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

const cols = `id, key, content_type, bytes, width, height, duration_ms, sha256, kind, source, owner_id, project_id, meta, created_at`

// Get returns an asset row.
func (s *Service) Get(ctx context.Context, id string) (Asset, error) {
	return get(ctx, s.db, id)
}

// GetTx returns an asset row inside tx.
func (s *Service) GetTx(ctx context.Context, tx *db.Tx, id string) (Asset, error) {
	return get(ctx, tx, id)
}

func get(ctx context.Context, q db.Querier, id string) (Asset, error) {
	var r struct {
		ID          string    `db:"id"`
		Key         string    `db:"key"`
		ContentType string    `db:"content_type"`
		Bytes       int64     `db:"bytes"`
		Width       int       `db:"width"`
		Height      int       `db:"height"`
		DurationMS  int       `db:"duration_ms"`
		SHA256      string    `db:"sha256"`
		Kind        string    `db:"kind"`
		Source      string    `db:"source"`
		OwnerID     *string   `db:"owner_id"`
		ProjectID   *string   `db:"project_id"`
		Meta        string    `db:"meta"`
		CreatedAt   time.Time `db:"created_at"`
	}
	if err := q.Get(ctx, &r, `SELECT `+cols+` FROM assets WHERE id = ?`, id); err != nil {
		if db.IsNotFound(err) {
			return Asset{}, ErrNotFound
		}
		return Asset{}, err
	}
	meta := json.RawMessage(r.Meta)
	if len(meta) == 0 {
		meta = json.RawMessage("{}")
	}
	return Asset{ID: r.ID, Key: r.Key, ContentType: r.ContentType, Bytes: r.Bytes, Width: r.Width, Height: r.Height,
		DurationMS: r.DurationMS, SHA256: r.SHA256, Kind: r.Kind, Source: r.Source, OwnerID: r.OwnerID,
		ProjectID: r.ProjectID, Meta: meta, CreatedAt: r.CreatedAt.UTC()}, nil
}

// Read returns the asset row and its bytes (at most max bytes; 0 means MaxBytes).
func (s *Service) Read(ctx context.Context, id string, max int64) ([]byte, Asset, error) {
	a, err := s.Get(ctx, id)
	if err != nil {
		return nil, Asset{}, err
	}
	if max <= 0 {
		max = MaxBytes
	}
	b, err := storage.ReadAll(ctx, s.store, a.Key, max)
	if err != nil {
		return nil, Asset{}, fmt.Errorf("assets: read %s: %w", id, err)
	}
	return b, a, nil
}

// URL returns a signed, expiring media URL for the asset (relative, "/media/...").
func (s *Service) URL(a Asset, ttl time.Duration) string {
	if ttl <= 0 {
		ttl = time.Hour
	}
	return s.media.URL(a.Key, ttl)
}

// URLFor looks up an asset and returns its signed URL ("" when id is empty or unknown).
func (s *Service) URLFor(ctx context.Context, id string, ttl time.Duration) string {
	if id == "" {
		return ""
	}
	a, err := s.Get(ctx, id)
	if err != nil {
		return ""
	}
	return s.URL(a, ttl)
}

// Delete removes the row and the stored object.
func (s *Service) Delete(ctx context.Context, id string) error {
	a, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM assets WHERE id = ?`, id); err != nil {
		return err
	}
	return s.store.Delete(ctx, a.Key)
}

// Module provides *Service on core.Deps. It has no routes (files are served
// by the signed /media handler).
type Module struct{ svc *Service }

// NewModule creates the module and provides the service.
func NewModule(d *core.Deps) *Module {
	svc := New(d.DB, d.Store, d.Media)
	core.Provide(d, svc)
	return &Module{svc: svc}
}

// Name implements core.Module.
func (m *Module) Name() string { return "assets" }

// Routes implements core.Module.
func (m *Module) Routes(r *core.Router) {}

// From returns the service registered on d.
func From(d *core.Deps) *Service { return core.MustUse[*Service](d) }
