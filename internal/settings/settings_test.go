package settings_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

func TestInstanceDefaults(t *testing.T) {
	testdb.ForEach(t, func(t *testing.T, d *db.DB) {
		s := settings.New(d)
		inst, err := s.Instance(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if inst.Telemetry || inst.LocalOnly || inst.World.Enabled {
			t.Fatal("privacy defaults must be off")
		}
		if inst.Limits.RunningGPUJobs != 2 || inst.SignUp != settings.SignUpInvite || inst.RoutingMode != settings.RoutingBalanced {
			t.Fatalf("defaults: %+v", inst)
		}
		if err := inst.Validate(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestInstanceUpdatePersistsAndMergesDefaults(t *testing.T) {
	testdb.ForEach(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		s := settings.New(d)
		var seen settings.Instance
		s.OnChange(func(i settings.Instance) { seen = i })
		_, err := s.UpdateInstance(ctx, "", func(i *settings.Instance) error {
			i.Name = "Harbour Ghosts Studio"
			i.SignUp = settings.SignUpOpen
			i.Limits.RunningGPUJobs = 3
			i.Maintenance.Enabled = true
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if seen.Name != "Harbour Ghosts Studio" {
			t.Fatal("watcher not called")
		}
		fresh := settings.New(d)
		inst, err := fresh.Instance(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if inst.Name != "Harbour Ghosts Studio" || inst.SignUp != "open" || inst.Limits.RunningGPUJobs != 3 || !inst.Maintenance.Enabled {
			t.Fatalf("not persisted: %+v", inst)
		}
		if inst.Limits.RunningJobs != 4 || inst.Content.DefaultAgeRating != "all" {
			t.Fatalf("defaults lost: %+v", inst)
		}
		// Only changed keys are stored.
		var n int
		_ = d.Get(ctx, &n, `SELECT COUNT(*) FROM instance_settings`)
		if n != 4 {
			t.Fatalf("stored %d keys, want 4", n)
		}
		// Invalid values are rejected and nothing changes.
		_, err = s.UpdateInstance(ctx, "", func(i *settings.Instance) error { i.Mode = "galaxy"; return nil })
		if err == nil {
			t.Fatal("invalid mode accepted")
		}
		sentinel := errors.New("stop")
		if _, err := s.UpdateInstance(ctx, "", func(i *settings.Instance) error { return sentinel }); !errors.Is(err, sentinel) {
			t.Fatal(err)
		}
	})
}

func TestModuleKeys(t *testing.T) {
	testdb.ForEach(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		s := settings.New(d)
		var v struct{ Pins []string }
		ok, err := s.GetKey(ctx, "ai.routing", &v)
		if ok || err != nil {
			t.Fatal(ok, err)
		}
		if err := s.SetKey(ctx, "ai.routing", map[string]any{"Pins": []string{"flux2-klein"}}, ""); err != nil {
			t.Fatal(err)
		}
		if ok, err := s.GetKey(ctx, "ai.routing", &v); !ok || err != nil || len(v.Pins) != 1 {
			t.Fatal(ok, err, v)
		}
		if err := s.SetKey(ctx, "nodot", 1, ""); err == nil {
			t.Fatal("unprefixed key accepted")
		}
		// module keys do not leak into Instance decoding
		if _, err := s.Instance(ctx); err != nil {
			t.Fatal(err)
		}
	})
}

func TestUserPrefs(t *testing.T) {
	testdb.ForEach(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		s := settings.New(d)
		uid := db.NewID()
		if _, err := d.Exec(ctx, `INSERT INTO users (id, username) VALUES (?, ?)`, uid, "sora"); err != nil {
			t.Fatal(err)
		}
		p, err := s.UserPrefs(ctx, uid)
		if err != nil || p.Theme != "dark" || p.TextSize != 100 || !p.ShowRanksXP || p.ReducedMotion != "system" {
			t.Fatalf("defaults %+v %v", p, err)
		}
		if _, err := s.UpdateUserPrefs(ctx, uid, func(p *settings.UserPrefs) error {
			p.Theme = "light"
			p.DyslexiaFont = true
			p.Reading.Mode = "webtoon"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		p, _ = s.UserPrefs(ctx, uid)
		if p.Theme != "light" || !p.DyslexiaFont || p.Reading.Mode != "webtoon" || p.Reading.Background != "black" {
			t.Fatalf("saved %+v", p)
		}
		if _, err := s.UpdateUserPrefs(ctx, uid, func(p *settings.UserPrefs) error { p.Theme = "neon"; return nil }); err == nil {
			t.Fatal("invalid theme accepted")
		}
	})
}

func TestNightWindow(t *testing.T) {
	n := settings.NightMode{StartHour: 22, EndHour: 6}
	at := func(h int) time.Time { return time.Date(2026, 1, 1, h, 0, 0, 0, time.UTC) }
	if !n.InWindow(at(23)) || !n.InWindow(at(3)) || n.InWindow(at(12)) {
		t.Fatal("wrapping window")
	}
	n = settings.NightMode{StartHour: 1, EndHour: 7}
	if !n.InWindow(at(1)) || n.InWindow(at(7)) {
		t.Fatal("plain window")
	}
}
