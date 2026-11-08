package update_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/admin/adminapi"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/backup"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
	"github.com/MaramHarsha/MangaVeyra/internal/update"
)

func forEachDialect(t *testing.T, fn func(t *testing.T, dialect string)) {
	for _, d := range testdb.Dialects() {
		t.Run(d, func(t *testing.T) { fn(t, d) })
	}
}

func TestChecksAreOptIn(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect string) {
		e := newEnv(t, dialect, withHooks(func(h *update.TestHooks) {
			h.Client = &http.Client{Transport: failingTransport{t}}
		}))
		if _, err := e.m.Check(e.ctx); !errors.Is(err, update.ErrChecksOff) {
			t.Fatalf("check with checks off: %v", err)
		}
		update.Tick(e.m, e.ctx) // the daily loop does nothing either
		if notice, _ := core.Use[adminapi.UpdateNotice](e.ta.Deps); notice != nil {
			if v, ok := notice.AvailableUpdate(e.ctx); ok {
				t.Fatalf("G5 offers %q with checks off", v)
			}
		}
		admin := e.adminClient()
		resp := admin.Get("/admin/update")
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(resp.Body), "Update checks are off") {
			t.Fatalf("settings view: %d %s", resp.StatusCode, resp.Body)
		}
		// Checks on, but this build has no trusted key: still no request.
		e.hooks.Keys = nil
		update.SetTestHooks(e.m, e.hooks)
		e.enableChecks("")
		if _, err := e.m.Check(e.ctx); !errors.Is(err, update.ErrNoKey) {
			t.Fatalf("check without a key: %v", err)
		}
		if st := update.GetCheckState(e.m, e.ctx); st.Error != "no_key" {
			t.Fatalf("check state = %+v", st)
		}
		if e.release.hits.Load() != 0 {
			t.Fatal("the release server was contacted")
		}
		resp = admin.Get("/admin/update/settings")
		if !strings.Contains(string(resp.Body), "no trusted release key") {
			t.Fatalf("no-key notice missing: %s", resp.Body)
		}
	})
}

func TestCheckOffersOnlySignedReleases(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect string) {
		e := newEnv(t, dialect)
		e.enableChecks("")

		// Tampered manifest, wrong key: refused, nothing offered.
		e.publish(publishOpts{tamper: true})
		if _, err := e.m.Check(e.ctx); !errors.Is(err, update.ErrBadSignature) {
			t.Fatalf("tampered: %v", err)
		}
		_, other, _ := ed25519.GenerateKey(nil)
		e.publish(publishOpts{key: other})
		if _, err := e.m.Check(e.ctx); !errors.Is(err, update.ErrBadSignature) {
			t.Fatalf("wrong key: %v", err)
		}
		if st := update.GetCheckState(e.m, e.ctx); st.Error != "signature" {
			t.Fatalf("state = %+v", st)
		}
		if e.offer() != nil {
			t.Fatal("an unverified release was offered")
		}
		// A pre-release on the stable channel is not offered.
		e.publish(publishOpts{version: "1.0.2-beta.1"})
		if rec, err := e.m.Check(e.ctx); err != nil || rec != nil {
			t.Fatalf("pre-release on stable: %v %v", rec, err)
		}

		// A signed patch release is offered, admins are notified once.
		e.publish(publishOpts{security: true, patch: true})
		rec, err := e.m.Check(e.ctx)
		if err != nil || rec == nil {
			t.Fatalf("check: %v %v", rec, err)
		}
		if rec.FromVersion != "1.0.0" || rec.ToVersion != "1.0.2" || rec.Status != update.StatusAvailable {
			t.Fatalf("offer = %+v", rec)
		}
		o := rec.Offer()
		if o.Kind != update.KindPatch || !o.Security || !o.AllPatch || o.Asset == nil || len(o.Releases) != 2 {
			t.Fatalf("offer details = %+v", o)
		}
		if _, err := e.m.Check(e.ctx); err != nil {
			t.Fatal(err)
		}
		var n int
		_ = e.ta.Deps.DB.Get(e.ctx, &n, `SELECT COUNT(*) FROM notifications WHERE user_id = ? AND kind = 'update'`, e.ta.Admin.ID)
		if n != 1 {
			t.Fatalf("admin notifications = %d", n)
		}
		var rows int
		_ = e.ta.Deps.DB.Get(e.ctx, &rows, `SELECT COUNT(*) FROM updates WHERE status = 'available'`)
		if rows != 1 {
			t.Fatalf("available rows = %d", rows)
		}
		st, _ := core.Use[update.Status](e.ta.Deps)
		if info := st.UpdateStatus(e.ctx); !info.Available || info.Version != "1.0.2" || info.Kind != update.KindPatch || !info.ChecksOn {
			t.Fatalf("status = %+v", info)
		}
		notice, ok := core.Use[adminapi.UpdateNotice](e.ta.Deps)
		if !ok {
			t.Fatal("adminapi.UpdateNotice is not provided")
		}
		if v, ok := notice.AvailableUpdate(e.ctx); !ok || v != "1.0.2" {
			t.Fatalf("AvailableUpdate = %q %v", v, ok)
		}
		if body := string(e.adminClient().Get("/admin").Body); !strings.Contains(body, "Update to 1.0.2") {
			t.Fatal("the G5 header does not offer the update")
		}

		admin := e.adminClient()
		body := string(admin.Get("/admin/update").Body)
		for _, want := range []string{"Update available · 1.0.2", "is on 1.0.0", "Patch · safe", "Signed by MangaVeyra", "Security fix",
			"What’s new in 1.0.1 and 1.0.2", "Fixes panel locks", "1. Automatic backup", "last backup 18.1 GB", "5. Clean up",
			"Keep the backup for 7 days", "Install future patch updates automatically at 03:00", "Remind me next login",
			"Back up and update", "Other members see a small banner only"} {
			if !strings.Contains(body, want) {
				t.Errorf("available view lacks %q", want)
			}
		}
		for _, banned := range []string{"~3 min", "no downtime"} {
			if strings.Contains(body, banned) {
				t.Errorf("available view claims %q", banned)
			}
		}

		// Beta channel: a manifest for another channel is refused; a beta pre-release is offered.
		if err := update.SaveConfig(e.m, e.ctx, update.Config{Checks: true, ReleaseURL: e.release.URL, Channel: update.ChannelBeta}); err != nil {
			t.Fatal(err)
		}
		e.release.put("/beta.json", e.release.files["/stable.json"])
		e.release.put("/beta.json.sig", e.release.files["/stable.json.sig"])
		if _, err := e.m.Check(e.ctx); !errors.Is(err, update.ErrChannel) {
			t.Fatalf("stable manifest on the beta channel: %v", err)
		}
		e.publish(publishOpts{version: "1.1.0-beta.1", channel: update.ChannelBeta})
		if rec, err := e.m.Check(e.ctx); err != nil || rec == nil || rec.ToVersion != "1.1.0-beta.1" || rec.Offer().Kind != update.KindMinor {
			t.Fatalf("beta: %+v %v", rec, err)
		}
	})
}

func TestUpdateRoutesAreForAdminsOnly(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect string) {
		e := newEnv(t, dialect)
		_, member := e.ta.UserClient(t, auth.RoleCreator)
		for _, p := range []string{"/admin/update", "/admin/update/settings", "/admin/update/log", "/admin/update/progress"} {
			if r := member.Get(p); r.StatusCode != http.StatusNotFound {
				t.Errorf("member GET %s = %d", p, r.StatusCode)
			}
		}
		for _, p := range []string{"/admin/update/start", "/admin/update/settings", "/admin/update/check", "/admin/update/later", "/admin/update/ack"} {
			if r := post(member, p, form("id", "x")); r.StatusCode != http.StatusNotFound {
				t.Errorf("member POST %s = %d", p, r.StatusCode)
			}
		}
		if r := e.ta.NewClient().Get("/admin/update"); r.StatusCode != http.StatusSeeOther {
			t.Errorf("anonymous = %d", r.StatusCode)
		}
		raw, _, err := e.ta.Deps.Auth.CreateToken(e.ctx, auth.TokenInput{UserID: e.ta.Admin.ID, Kind: httpx.TokenAPI, Name: "ci", Scopes: []string{"*"}})
		if err != nil {
			t.Fatal(err)
		}
		if r := post(e.ta.BearerClient(raw), "/admin/update/start", form("id", "x")); r.StatusCode != http.StatusForbidden {
			t.Errorf("admin token POST start = %d", r.StatusCode)
		}
	})
}

func waitInstall(t *testing.T, e *env) *update.Record {
	t.Helper()
	waitIdle(t, e)
	return e.attempt()
}

func waitIdle(t *testing.T, e *env) {
	t.Helper()
	done := make(chan struct{})
	go func() { e.m.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("install did not finish")
	}
}

func TestInstallHappyPath(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect string) {
		e := newEnv(t, dialect)
		e.enableChecks("")
		e.publish(publishOpts{patch: true})
		rec, err := e.m.Check(e.ctx)
		if err != nil || rec == nil {
			t.Fatalf("check: %v", err)
		}
		admin := e.adminClient()
		resp := post(admin, "/admin/update/start", form("id", rec.ID, "toggles", "1"))
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("start = %d %s", resp.StatusCode, resp.Body)
		}
		att := waitInstall(t, e)
		select {
		case exe := <-e.restarted:
			if exe != e.exe {
				t.Fatalf("restart %s, want %s", exe, e.exe)
			}
		default:
			t.Fatalf("no restart requested; attempt = %s %s %s", att.Status, att.Error, update.LogText(att))
		}
		run := att.Run()
		if att.Status != update.StatusHealthCheck || run.Phase != update.PhaseRestarting || att.BackupID == nil {
			t.Fatalf("attempt = %s %+v", att.Status, run)
		}
		for _, k := range []string{update.StepBackup, update.StepDownload, update.StepMigrate, update.StepHealth} {
			if s := run.Step(k); s.State != update.StateDone {
				t.Errorf("step %s = %s", k, s.State)
			}
		}
		if run.Pending != 2 || run.Applied != 2 || run.Workers != 2 || run.BackupBytes != 18_200_000_000 {
			t.Errorf("measured values = pending %d applied %d workers %d backup %d", run.Pending, run.Applied, run.Workers, run.BackupBytes)
		}
		if got := readFile(t, e.exe); got != string(fakeRelease(t)) {
			t.Fatalf("the executable was not swapped (%d bytes)", len(got))
		}
		if got := readFile(t, e.exe+".prev"); got != "old binary 1.0.0" {
			t.Fatalf("previous binary = %q", got)
		}
		data := e.ta.Deps.Cfg.DataDir
		if readFile(t, filepath.Join(data, "fake-migrated")) != "1.0.2" {
			t.Fatal("the new binary's migrate did not run against this data directory")
		}
		var cand map[string]string
		_ = json.Unmarshal([]byte(readFile(t, filepath.Join(data, "fake-serve-env"))), &cand)
		if cand["candidate"] != "1" || !strings.HasPrefix(cand["addr"], "127.0.0.1:") {
			t.Fatalf("candidate env = %v", cand)
		}
		if !e.maintenance() {
			t.Fatal("maintenance must stay on until the new binary starts")
		}
		if len(e.backups.created) != 1 || e.backups.created[0].Kind != "pre_update" || e.backups.created[0].KeepFor != 0 {
			t.Fatalf("backups = %+v", e.backups.created)
		}
		if body := string(admin.Get("/admin/update").Body); !strings.Contains(body, "Updating to 1.0.2") ||
			!strings.Contains(body, "restarting on 1.0.2") || !strings.Contains(body, `http-equiv="refresh" content="8"`) {
			t.Fatalf("restarting view: %s", body)
		}

		// The new binary starts: it finishes the update.
		update.SetVersion(e.m, "1.0.2")
		if err := update.Resume(e.m, e.ctx); err != nil {
			t.Fatal(err)
		}
		att = e.attempt()
		run = att.Run()
		if att.Status != update.StatusDone || !run.BackupDeleted || att.FinishedAt == nil {
			t.Fatalf("finished = %s %+v", att.Status, run)
		}
		if len(e.backups.deleted) != 1 || e.backups.deleted[0] != *att.BackupID {
			t.Fatalf("deleted = %v", e.backups.deleted)
		}
		if e.maintenance() {
			t.Fatal("maintenance must be off after the update")
		}
		var locks int
		_ = e.ta.Deps.DB.Get(e.ctx, &locks, `SELECT COUNT(*) FROM instance_settings WHERE key = 'update.lock'`)
		if locks != 0 {
			t.Fatal("the install lock was not released")
		}
		if e.offer() != nil {
			t.Fatal("the installed release is still offered")
		}
		body := string(admin.Get("/admin/update").Body)
		for _, want := range []string{"LEVEL UP", "is on 1.0.2", "The backup was deleted to free 18.2 GB.", "Created, then deleted after success",
			"App, database and storage OK · 2 workers online", "Continue"} {
			if !strings.Contains(body, want) {
				t.Errorf("done view lacks %q", want)
			}
		}
		log := string(admin.Get("/admin/update/log").Body)
		if !strings.Contains(log, "Update from 1.0.0 to 1.0.2 started") || !strings.Contains(log, "Maintenance mode off") {
			t.Fatalf("log = %s", log)
		}
		if r := post(admin, "/admin/update/ack", form("id", att.ID)); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("ack = %d", r.StatusCode)
		}
		if body := string(admin.Get("/admin/update").Body); !strings.Contains(body, "Updates · ") {
			t.Fatalf("after Continue the settings view shows: %s", body)
		}
		var audits int
		_ = e.ta.Deps.DB.Get(e.ctx, &audits, `SELECT COUNT(*) FROM audit_log WHERE action IN ('update.start', 'update.done')`)
		if audits != 2 {
			t.Fatalf("audit entries = %d", audits)
		}
	})
}

func TestInstallFailuresRestoreTheBackup(t *testing.T) {
	cases := []struct {
		name, mode, code, detail string
		truncate, tamper         bool
	}{
		{name: "migrate fails", mode: "migrate_fail", code: "UPD-MIGRATE-0602", detail: "Database change 0602 failed"},
		{name: "health check fails", mode: "health_fail", code: "UPD-HEALTH", detail: "did not pass its health check"},
		{name: "download truncated", truncate: true, code: "UPD-VERIFY", detail: "truncated"},
		{name: "asset tampered", tamper: true, code: "UPD-VERIFY", detail: "does not match the signed checksum"},
		{name: "wrong version", mode: "wrong_version", code: "UPD-VERIFY", detail: "reports"},
	}
	forEachDialect(t, func(t *testing.T, dialect string) {
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				t.Setenv("MVFAKE_MODE", c.mode)
				e := newEnv(t, dialect, withHooks(func(h *update.TestHooks) { h.HealthTimeout = 3 * time.Second }))
				e.enableChecks("")
				e.publish(publishOpts{})
				e.release.truncate, e.release.tamper = c.truncate, c.tamper
				rec, err := e.m.Check(e.ctx)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := e.m.Install(e.ctx, rec.ID, e.ta.Admin.ID, update.InstallOptions{}); err != nil {
					t.Fatal(err)
				}
				att := waitInstall(t, e)
				run := att.Run()
				if att.Status != update.StatusRolledBack || !run.Restored || run.Code != c.code {
					t.Fatalf("attempt = %s %+v\n%s", att.Status, run.Code, update.LogText(att))
				}
				if len(e.backups.restored) != 1 || e.backups.restored[0] != *att.BackupID {
					t.Fatalf("restored = %v", e.backups.restored)
				}
				if readFile(t, e.exe) != "old binary 1.0.0" {
					t.Fatal("the old binary was not kept")
				}
				if _, err := os.Stat(e.exe + ".prev"); !os.IsNotExist(err) {
					t.Fatal("no swap may happen on failure")
				}
				if _, err := os.Stat(filepath.Join(e.ta.Deps.Cfg.DataDir, "updates", "1.0.2")); !os.IsNotExist(err) {
					t.Fatal("the download was not removed")
				}
				if e.maintenance() {
					t.Fatal("maintenance must be off after a restored failure")
				}
				select {
				case <-e.restarted:
					t.Fatal("restart requested after a failure")
				default:
				}
				if o := e.offer(); o == nil || o.ToVersion != "1.0.2" {
					t.Fatal("the release must be offered again (Try again)")
				}
				body := string(e.adminClient().Get("/admin/update").Body)
				for _, want := range []string{"Update didn’t finish · restored 1.0.0", "Nothing was lost.", "Error code " + c.code,
					"Rolled back to 1.0.0 automatically", "The backup is kept until an update succeeds.", "Try again", "Copy log", c.detail} {
					if !strings.Contains(body, want) {
						t.Errorf("failed view lacks %q", want)
					}
				}
				if c.mode == "migrate_fail" && !strings.Contains(body, "Free space on the database disk") {
					t.Error("disk-full advice missing")
				}
			})
		}
	})
}

func TestRestoreFailureKeepsMaintenanceOn(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect string) {
		t.Setenv("MVFAKE_MODE", "migrate_fail")
		e := newEnv(t, dialect)
		e.backups.failRestore = errors.New("archive unreadable")
		e.enableChecks("")
		e.publish(publishOpts{})
		rec, _ := e.m.Check(e.ctx)
		if _, err := e.m.Install(e.ctx, rec.ID, e.ta.Admin.ID, update.InstallOptions{}); err != nil {
			t.Fatal(err)
		}
		att := waitInstall(t, e)
		if run := att.Run(); att.Status != update.StatusFailed || !run.RestoreFailed {
			t.Fatalf("attempt = %s %+v", att.Status, run)
		}
		if !e.maintenance() {
			t.Fatal("maintenance must stay on when the restore failed")
		}
		body := string(e.adminClient().Get("/admin/update").Body)
		if !strings.Contains(body, "restore needed") || !strings.Contains(body, "mangaveyra backup restore "+*att.BackupID) {
			t.Fatalf("restore-failed view: %s", body)
		}
	})
}

func TestBackupFailureChangesNothing(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect string) {
		e := newEnv(t, dialect)
		e.backups.failCreate = errors.New("backup: another backup or restore is running")
		e.enableChecks("")
		e.publish(publishOpts{})
		rec, _ := e.m.Check(e.ctx)
		if _, err := e.m.Install(e.ctx, rec.ID, e.ta.Admin.ID, update.InstallOptions{}); err != nil {
			t.Fatal(err)
		}
		att := waitInstall(t, e)
		if att.Status != update.StatusFailed || att.Run().Code != "UPD-BACKUP" || len(e.backups.restored) != 0 {
			t.Fatalf("attempt = %s %s", att.Status, att.Run().Code)
		}
		if e.maintenance() || readFile(t, e.exe) != "old binary 1.0.0" {
			t.Fatal("a failed backup must leave the server as it was")
		}
		admin := e.adminClient()
		body := string(admin.Get("/admin/update").Body)
		if !strings.Contains(body, "Nothing was changed. Your server is still on 1.0.0.") {
			t.Fatalf("failed view: %s", body)
		}
		// Closing the result shows the newest offer, not the failed attempt's version.
		if r := post(admin, "/admin/update/ack", form("attempt", att.ID, "id", rec.ID)); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("ack = %d", r.StatusCode)
		}
		e.publish(publishOpts{version: "1.0.3"})
		if _, err := e.m.Check(e.ctx); err != nil {
			t.Fatal(err)
		}
		body = string(admin.Get("/admin/update").Body)
		if !strings.Contains(body, "Update available · 1.0.3") || strings.Contains(body, "Update available · 1.0.2") {
			t.Fatalf("available view after a failed attempt: %s", body)
		}
		if body := string(admin.Get("/admin/update/settings").Body); !strings.Contains(body, "Last update 1.0.0 → 1.0.2 · failed") {
			t.Fatalf("settings last update line missing: %s", body)
		}
	})
}

func TestInstallIsExclusive(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect string) {
		e := newEnv(t, dialect)
		e.enableChecks("")
		e.publish(publishOpts{})
		rec, _ := e.m.Check(e.ctx)
		// Another server process holds the lock.
		if ok, err := update.HoldLockAs(e.m, e.ctx, rec.ID, "other-process"); err != nil || !ok {
			t.Fatalf("hold: %v %v", ok, err)
		}
		if _, err := e.m.Install(e.ctx, rec.ID, e.ta.Admin.ID, update.InstallOptions{}); !errors.Is(err, update.ErrBusy) {
			t.Fatalf("second process: %v", err)
		}
		resp := post(e.adminClient(), "/admin/update/start", form("id", rec.ID, "toggles", "1"))
		if resp.StatusCode != http.StatusConflict || !strings.Contains(string(resp.Body), "Another update, backup or restore is running") {
			t.Fatalf("start while locked = %d", resp.StatusCode)
		}
		if len(e.backups.created) != 0 {
			t.Fatal("nothing may run while another process holds the lock")
		}
		_, _ = e.ta.Deps.DB.Exec(e.ctx, `DELETE FROM instance_settings WHERE key = 'update.lock'`)

		// Two admins press the button at once: exactly one install runs.
		var wg sync.WaitGroup
		errs := make([]error, 4)
		for i := range errs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, errs[i] = e.m.Install(e.ctx, rec.ID, e.ta.Admin.ID, update.InstallOptions{})
			}(i)
		}
		wg.Wait()
		started := 0
		for _, err := range errs {
			switch {
			case err == nil:
				started++
			case errors.Is(err, update.ErrBusy), errors.Is(err, update.ErrNotOffered):
			default:
				t.Errorf("unexpected: %v", err)
			}
		}
		if started != 1 {
			t.Fatalf("%d installs started", started)
		}
		waitInstall(t, e)
		if len(e.backups.created) != 1 {
			t.Fatalf("backups taken = %d", len(e.backups.created))
		}
	})
}

func TestContainerInstallShowsHostCommands(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect string) {
		e := newEnv(t, dialect, withHooks(func(h *update.TestHooks) {
			on := true
			h.Container = &on
		}))
		e.enableChecks("")
		e.publish(publishOpts{})
		rec, _ := e.m.Check(e.ctx)
		before := e.release.hits.Load()
		admin := e.adminClient()
		if body := string(admin.Get("/admin/update").Body); !strings.Contains(body, "runs in a container") {
			t.Fatalf("available view in a container: %s", body)
		}
		if r := post(admin, "/admin/update/start", form("id", rec.ID, "toggles", "1", "keep", "on")); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("start = %d %s", r.StatusCode, r.Body)
		}
		waitIdle(t, e)
		if e.release.hits.Load() != before {
			t.Fatal("a container install must not download a binary")
		}
		offer := e.offer()
		if offer == nil || offer.BackupID == nil || offer.Run().Mode != update.ModeContainer {
			t.Fatalf("offer = %+v", offer)
		}
		if e.maintenance() || readFile(t, e.exe) != "old binary 1.0.0" {
			t.Fatal("a container install changes nothing but the backup")
		}
		if e.backups.created[0].KeepFor != update.KeepFor {
			t.Fatalf("keep = %v", e.backups.created[0].KeepFor)
		}
		body := string(admin.Get("/admin/update").Body)
		for _, want := range []string{"Update to 1.0.2 on the host", "git pull", "docker compose up -d --build",
			"docker compose logs app | grep &#34;database migrated&#34;", "Saved · backup-"} {
			if !strings.Contains(body, want) {
				t.Errorf("container view lacks %q", want)
			}
		}
		// The new image starts.
		update.SetVersion(e.m, "1.0.2")
		if err := update.Resume(e.m, e.ctx); err != nil {
			t.Fatal(err)
		}
		att := e.attempt()
		if att.Status != update.StatusDone || len(e.backups.deleted) != 0 {
			t.Fatalf("container finish = %s deleted %v (kept for 7 days)", att.Status, e.backups.deleted)
		}
	})
}

func TestNoBackupServiceRefusesHonestly(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect string) {
		e := newEnv(t, dialect, withHooks(func(h *update.TestHooks) {
			h.Backups = nil
			h.NoBackups = true
		}))
		e.enableChecks("")
		e.publish(publishOpts{})
		rec, _ := e.m.Check(e.ctx)
		admin := e.adminClient()
		body := string(admin.Get("/admin/update").Body)
		if !strings.Contains(body, "Backups are not available on this server yet") {
			t.Fatalf("notice missing: %s", body)
		}
		if r := post(admin, "/admin/update/start", form("id", rec.ID, "toggles", "1")); r.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("start without backups = %d", r.StatusCode)
		}
		if e.maintenance() {
			t.Fatal("nothing may change")
		}
	})
}

func TestInterruptedInstallIsRestoredOnStart(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect string) {
		e := newEnv(t, dialect)
		e.enableChecks("")
		e.publish(publishOpts{})
		rec, _ := e.m.Check(e.ctx)
		b, err := e.backups.Create(e.ctx, testBackupRequest())
		if err != nil {
			t.Fatal(err)
		}
		run := update.Run{Mode: update.ModeBinary, Steps: []update.Step{{Key: update.StepBackup, State: update.StateDone},
			{Key: update.StepDownload, State: update.StateDone}, {Key: update.StepMigrate, State: update.StateRunning}}}
		steps, _ := json.Marshal(run)
		now := db.Now()
		if _, err := e.ta.Deps.DB.Exec(e.ctx, `UPDATE updates SET status = 'migrating', backup_id = ?, steps = ?, started_at = ? WHERE id = ?`,
			b.ID, string(steps), now, rec.ID); err != nil {
			t.Fatal(err)
		}
		// The crashed process's lease ran out long ago.
		if _, err := e.ta.Deps.DB.Exec(e.ctx, `INSERT INTO instance_settings (key, value, updated_at) VALUES ('update.lock', ?, ?)`,
			`{"update_id":"`+rec.ID+`","owner":"dead"}`, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := update.Resume(e.m, e.ctx); err != nil {
			t.Fatal(err)
		}
		att := e.attempt()
		if att.Status != update.StatusRolledBack || att.Run().Code != "UPD-INTERRUPTED" || len(e.backups.restored) != 1 {
			t.Fatalf("recovered = %s %s %v", att.Status, att.Run().Code, e.backups.restored)
		}
	})
}

func TestAutoPatchInstallsAtThreeOnly(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect string) {
		var clock time.Time
		e := newEnv(t, dialect, withHooks(func(h *update.TestHooks) { h.Now = func() time.Time { return clock } }))
		if err := update.SaveConfig(e.m, e.ctx, update.Config{Checks: true, ReleaseURL: e.release.URL, Channel: update.ChannelStable,
			AutoPatch: true}); err != nil {
			t.Fatal(err)
		}
		// A minor release is never installed automatically.
		e.publish(publishOpts{version: "1.1.0", patch: true})
		clock = time.Date(2026, 11, 10, 3, 5, 0, 0, time.Local)
		update.Tick(e.m, e.ctx)
		e.m.Wait()
		if len(e.backups.created) != 0 {
			t.Fatal("a minor release was installed automatically")
		}
		// A patch release not flagged fixes-only is not either.
		e.publish(publishOpts{version: "1.0.2", patch: false})
		_, _ = e.m.Check(e.ctx)
		clock = clock.Add(24 * time.Hour)
		update.Tick(e.m, e.ctx)
		e.m.Wait()
		if len(e.backups.created) != 0 {
			t.Fatal("an unflagged patch was installed automatically")
		}
		// A flagged patch outside 03:00 waits; at 03:00 it installs once.
		e.publish(publishOpts{version: "1.0.2", patch: true})
		_, _ = e.m.Check(e.ctx)
		clock = time.Date(2026, 11, 12, 14, 0, 0, 0, time.Local)
		update.Tick(e.m, e.ctx)
		e.m.Wait()
		if len(e.backups.created) != 0 {
			t.Fatal("installed outside 03:00")
		}
		clock = time.Date(2026, 11, 13, 3, 1, 0, 0, time.Local)
		update.Tick(e.m, e.ctx)
		att := waitInstall(t, e)
		if !att.Run().Auto || att.StartedBy != nil {
			t.Fatalf("automatic attempt = %+v", att)
		}
		select {
		case <-e.restarted:
		default:
			t.Fatalf("automatic patch did not finish: %s\n%s", att.Status, update.LogText(att))
		}
	})
}

func TestShellBannerForMembersModalForAdmins(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect string) {
		e := newEnv(t, dialect)
		member := e.ta.CreateUser(t, "", auth.RoleCreator)
		req := func(u *auth.User, session string, cookie *http.Cookie) *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/studio", nil)
			if cookie != nil {
				r.AddCookie(cookie)
			}
			hu := &httpx.User{ID: u.ID, Username: u.Username, Role: u.Role}
			return r.WithContext(httpx.WithIdentity(context.Background(), httpx.Identity{User: hu, SessionID: session}))
		}
		// Nothing offered: nothing shown.
		if b, o := update.ShellFor(e.m, req(member, "s1", nil)); b != nil || o != nil {
			t.Fatal("extras without an update")
		}
		e.enableChecks("")
		e.publish(publishOpts{})
		if _, err := e.m.Check(e.ctx); err != nil {
			t.Fatal(err)
		}
		b, o := update.ShellFor(e.m, req(member, "s1", nil))
		if b == nil || o != nil {
			t.Fatal("members get the banner only")
		}
		var buf bytes.Buffer
		_ = b.Render(context.Background(), &buf)
		if s := buf.String(); !strings.Contains(s, "Update 1.0.2 available.") || strings.Contains(s, "<button") || strings.Contains(s, "<form") {
			t.Fatalf("banner = %s", s)
		}
		b, o = update.ShellFor(e.m, req(e.ta.Admin, "s2", nil))
		if b != nil || o == nil {
			t.Fatal("admins get the modal")
		}
		buf.Reset()
		_ = o.Render(context.Background(), &buf)
		if s := buf.String(); !strings.Contains(s, `role="dialog"`) || !strings.Contains(s, "Remind me next login") ||
			!strings.Contains(s, `name="next" value="/studio"`) {
			t.Fatalf("modal = %s", s)
		}
		// "Remind me next login" snoozes this session only.
		snooze := &http.Cookie{Name: "mv_update_later", Value: update.SnoozeCookie("s2", "1.0.2")}
		if _, o := update.ShellFor(e.m, req(e.ta.Admin, "s2", snooze)); o != nil {
			t.Fatal("snoozed for this login")
		}
		if _, o := update.ShellFor(e.m, req(e.ta.Admin, "s3", snooze)); o == nil {
			t.Fatal("a new login shows the modal again")
		}
		// The real endpoint sets the cookie and returns to the page.
		admin := e.adminClient()
		resp := post(admin, "/admin/update/later", form("next", "/studio/projects", "toggles", "1", "auto_patch", "on"))
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/studio/projects" {
			t.Fatalf("later = %d %s", resp.StatusCode, resp.Header.Get("Location"))
		}
		if !strings.Contains(resp.Header.Get("Set-Cookie"), "mv_update_later=") {
			t.Fatal("no snooze cookie")
		}
		var cfg update.Config
		_, _ = e.ta.Deps.Settings.GetKey(e.ctx, "update.config", &cfg)
		if !cfg.AutoPatch {
			t.Fatal("the 03:00 toggle was not saved")
		}
		// Open redirects are refused.
		if r := post(admin, "/admin/update/later", form("next", "https://evil.example")); r.Header.Get("Location") != "/studio" {
			t.Fatalf("next = %s", r.Header.Get("Location"))
		}
	})
}

func TestSettingsForm(t *testing.T) {
	forEachDialect(t, func(t *testing.T, dialect string) {
		e := newEnv(t, dialect)
		admin := e.adminClient()
		if r := post(admin, "/admin/update/settings", form("checks", "on", "release_url", "", "channel", "stable")); r.StatusCode != http.StatusUnprocessableEntity ||
			!strings.Contains(string(r.Body), "Add the release address") {
			t.Fatalf("checks without an address = %d", r.StatusCode)
		}
		if r := post(admin, "/admin/update/settings", form("checks", "on", "release_url", "ftp://x", "channel", "stable")); r.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("bad address = %d", r.StatusCode)
		}
		e.publish(publishOpts{})
		r := post(admin, "/admin/update/check", form("checks", "on", "release_url", e.release.URL+"/", "channel", "stable", "keep", "on"))
		if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/admin/update" {
			t.Fatalf("check now = %d %s %s", r.StatusCode, r.Header.Get("Location"), r.Body)
		}
		var cfg update.Config
		_, _ = e.ta.Deps.Settings.GetKey(e.ctx, "update.config", &cfg)
		if !cfg.Checks || cfg.ReleaseURL != e.release.URL || !cfg.KeepBackup || cfg.Channel != "stable" {
			t.Fatalf("saved = %+v", cfg)
		}
		if e.offer() == nil {
			t.Fatal("Check now did not record the offer")
		}
	})
}

func testBackupRequest() backup.Request { return backup.Request{Kind: backup.KindPreUpdate} }
