package backup

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// TestService exposes the service's internals to the external tests.
type TestService = service

// NewTestService builds a second service on the same dependencies (another "process").
func NewTestService(d *core.Deps) *TestService { return newService(d) }

func (s *service) ClaimSlot(ctx context.Context, slot time.Time) (bool, error) {
	return s.claimSlot(ctx, slot)
}
func (s *service) Acquire(ctx context.Context) (func(), error) { return s.acquire(ctx, "test") }
func (s *service) RunDue(ctx context.Context) error            { return s.runDue(ctx) }
func (s *service) Sweep(ctx context.Context) error             { return s.sweep(ctx) }
func (s *service) SetNow(fn func() time.Time)                  { s.now = fn }
func (s *service) RestoreArchive(ctx context.Context, id, actor string) (*RestoreReport, error) {
	return s.restoreArchive(ctx, id, actor)
}
func (s *service) Destination(ctx context.Context) (string, error) { return s.destination(ctx) }
func (s *service) Announce(ctx context.Context)                    { s.announce(ctx, presencePre+"test") }

// ReadManifest opens an archive and returns its manifest.
func ReadManifest(path string) (Manifest, error) {
	a, err := openArchive(path)
	if err != nil {
		return Manifest{}, err
	}
	defer a.Close()
	return a.M, nil
}

// VerifyArchive checks every entry against the manifest.
func VerifyArchive(path string) error {
	a, err := openArchive(path)
	if err != nil {
		return err
	}
	defer a.Close()
	for name := range a.M.Entries {
		if err := a.verify(name); err != nil {
			return err
		}
	}
	return nil
}

// TableOrder returns the foreign-key-safe table order of a database.
func TableOrder(ctx context.Context, q db.Querier) ([]string, map[string][]string, error) {
	c, err := loadCatalog(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	return c.Order, c.Deferred, nil
}

// Fingerprints renders every table in the dialect-independent archive form,
// one line per row, so two databases of different dialects can be compared.
// where filters rows per table (SQL condition).
func Fingerprints(ctx context.Context, d *db.DB, skip []string, where map[string]string) (map[string][]string, error) {
	out := map[string][]string{}
	err := d.WithReadTx(ctx, func(tx *db.Tx) error {
		c, err := loadCatalog(ctx, tx)
		if err != nil {
			return err
		}
		for _, name := range c.Order {
			if name == metaTable || slices.Contains(skip, name) {
				continue
			}
			var buf bytes.Buffer
			if _, err := dumpTable(ctx, tx, c.Tables[name], where[name], nil, nil, &buf); err != nil {
				return err
			}
			lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
			if len(lines) == 1 && lines[0] == "" {
				lines = nil
			}
			out[name] = lines
		}
		return nil
	})
	return out, err
}
