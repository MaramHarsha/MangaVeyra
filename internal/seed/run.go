package seed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/lettering"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// ErrOwner means no eligible existing account was found. Seeding never creates accounts.
var ErrOwner = errors.New("seed: choose an existing active admin or creator with --owner, or create an admin first")

const demoID = "tidebreak-courier"

// demoVersion 2: character art keeps each name plate inside its figure's box
// and panels give every character its own slot (QA #12).
const demoVersion = 2

type demoMarker struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

// CLI dependency containers do not construct feature modules. Protect the
// check-and-provide sequence when callers seed concurrently in one process.
var bootstrapMu sync.Mutex

// Options control seeding.
type Options struct {
	// Demo creates the original, unpublished Tidebreak Courier studio project.
	Demo bool
	// OwnerUsername owns the demo project ("" = oldest active admin).
	OwnerUsername string
}

// Run seeds the database (called by `mangaveyra seed`).
func Run(ctx context.Context, d *core.Deps, opts Options) error {
	if !opts.Demo {
		return errors.New("seed: select --demo to create the original demo project")
	}
	if d == nil || d.DB == nil {
		return errors.New("seed: database is required")
	}
	owner, err := findOwner(ctx, d.DB, strings.TrimSpace(opts.OwnerUsername))
	if err != nil {
		return err
	}
	if exists, err := hasDemo(ctx, d.DB, owner); err != nil || exists {
		return err
	}
	if err := ensureServices(ctx, d); err != nil {
		return err
	}
	f := newFixture(owner)
	a := core.MustUse[*assets.Service](d)
	staged := make([]*assets.StagedAsset, 0, 9)
	committed := false
	defer func() {
		if !committed {
			for _, asset := range staged {
				// A canceled seed still cleans up its own uncommitted files.
				_ = a.DiscardStaged(context.WithoutCancel(ctx), asset)
			}
		}
	}()
	for _, spec := range f.artSpecs() {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := renderArt(spec)
		if err != nil {
			return fmt.Errorf("seed: draw placeholder: %w", err)
		}
		asset, err := a.Stage(ctx, assets.SaveParams{OwnerID: owner, ProjectID: f.projectID,
			Kind: spec.kind, Source: assets.SourceSystem, ContentType: "image/png", Data: data,
			Meta: map[string]any{"demo_seed": demoMarker{demoID, demoVersion}, "placeholder": true,
				"label": "Original demo placeholder art", "name": spec.name, "source": "authored_fixture"}})
		if err != nil {
			return fmt.Errorf("seed: stage placeholder: %w", err)
		}
		staged = append(staged, asset)
		f.art[spec.key] = asset.Asset().ID
	}
	if err := f.makePages(core.MustUse[lettering.Engine](d)); err != nil {
		return err
	}
	err = d.DB.WithTx(ctx, func(tx *db.Tx) error {
		// Lock before inspecting markers or allocating a slug. SQLite WithTx
		// already serializes writers; PostgreSQL seeds serialize per owner.
		if err := eligibleOwner(ctx, tx, owner, true); err != nil {
			return err
		}
		if exists, err := hasDemo(ctx, tx, owner); err != nil || exists {
			return err
		}
		if err := f.insert(ctx, tx, core.MustUse[studio.Service](d), core.MustUse[pagestore.Service](d), a, staged); err != nil {
			return err
		}
		committed = true
		return nil
	})
	// WithTx can fail during COMMIT, after its callback returned nil.
	if err != nil {
		committed = false
		return fmt.Errorf("seed: create demo: %w", err)
	}
	return nil
}

func findOwner(ctx context.Context, q db.Querier, username string) (string, error) {
	var id string
	query := `SELECT id FROM users WHERE role = 'admin' AND status = 'active' ORDER BY created_at, id LIMIT 1`
	var args []any
	if username != "" {
		query = `SELECT id FROM users WHERE username = ? AND status = 'active' AND role IN ('admin', 'creator')`
		args = append(args, username)
	}
	if err := q.Get(ctx, &id, query, args...); err != nil {
		if db.IsNotFound(err) {
			return "", ErrOwner
		}
		return "", fmt.Errorf("seed: find owner: %w", err)
	}
	return id, nil
}

func eligibleOwner(ctx context.Context, q db.Querier, id string, lock bool) error {
	query := `SELECT id FROM users WHERE id = ? AND status = 'active' AND role IN ('admin', 'creator')`
	if lock && q.Dialect() == db.Postgres {
		query += ` FOR UPDATE`
	}
	var found string
	if err := q.Get(ctx, &found, query, id); err != nil {
		if db.IsNotFound(err) {
			return ErrOwner
		}
		return fmt.Errorf("seed: lock owner: %w", err)
	}
	return nil
}

func hasDemo(ctx context.Context, q db.Querier, owner string) (bool, error) {
	var rows []struct {
		Settings string `db:"settings"`
	}
	if err := q.Select(ctx, &rows, `SELECT settings FROM projects WHERE owner_id = ?`, owner); err != nil {
		return false, fmt.Errorf("seed: find demo: %w", err)
	}
	for _, row := range rows {
		var settings struct {
			Marker demoMarker `json:"demo_seed"`
		}
		if json.Unmarshal([]byte(row.Settings), &settings) == nil && settings.Marker.ID == demoID && settings.Marker.Version > 0 {
			// Even an older/newer marked fixture belongs to the user now. Never
			// migrate or replace their edits just because this binary changed.
			return true, nil
		}
	}
	return false, nil
}

func ensureServices(ctx context.Context, d *core.Deps) error {
	bootstrapMu.Lock()
	defer bootstrapMu.Unlock()
	if _, ok := core.Use[*assets.Service](d); !ok {
		if d.Store == nil {
			return errors.New("seed: asset storage is required")
		}
		assets.NewModule(d)
	}
	if _, ok := core.Use[studio.Service](d); !ok {
		if d.Jobs == nil {
			return errors.New("seed: job queue is required to initialize Studio")
		}
		if err := studio.New(d).Init(ctx); err != nil {
			return err
		}
	}
	if _, ok := core.Use[pagestore.Service](d); !ok {
		if err := pagestore.New(d).Init(ctx); err != nil {
			return err
		}
	}
	if _, ok := core.Use[lettering.Engine](d); !ok {
		e, err := lettering.NewEngine()
		if err != nil {
			return fmt.Errorf("seed: initialize lettering: %w", err)
		}
		core.Provide[lettering.Engine](d, e)
	}
	return nil
}
