package backup_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/backup"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func form(c *testutil.Client, path string, v url.Values) *testutil.Response {
	return c.Do(http.MethodPost, path, strings.NewReader(v.Encode()), "application/x-www-form-urlencoded")
}

func runJobs(t *testing.T, ta *testutil.TestApp) {
	t.Helper()
	for i := 0; i < 10; i++ {
		ran, err := ta.Deps.Executor.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			return
		}
	}
}

func TestBackupRoutesAreForAdminsOnly(t *testing.T) {
	ta := testutil.NewTestApp(t)
	_, creator := ta.UserClient(t, auth.RoleCreator)
	b, err := svcOf(t, ta).Create(context.Background(), backup.Request{Kind: backup.KindManual})
	if err != nil {
		t.Fatal(err)
	}
	for _, rt := range []struct{ method, path string }{
		{"GET", "/admin/backup"}, {"GET", "/admin/backup/status"}, {"POST", "/admin/backup/now"}, {"POST", "/admin/backup/export"},
		{"POST", "/admin/backup/schedule"}, {"POST", "/admin/backup/upload"}, {"POST", "/admin/backup/restore"},
		{"GET", "/admin/backup/archives/" + b.ID + "/download"}, {"POST", "/admin/backup/archives/" + b.ID + "/delete"},
	} {
		if r := creator.Do(rt.method, rt.path, nil, ""); r.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s for a member: %d", rt.method, rt.path, r.StatusCode)
		}
	}
	if r := ta.NewClient().Get("/admin/backup"); r.StatusCode == http.StatusOK {
		t.Fatal("signed-out visitor saw G10")
	}
	if got, _ := svcOf(t, ta).Get(context.Background(), b.ID); got.Status != backup.StatusDone {
		t.Fatal("a member deleted an archive")
	}
}

func TestG10BackupNowScheduleDownloadAndDelete(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		admin := ta.Login(t, "admin")
		page := string(admin.Get("/admin/backup").Body)
		for _, want := range []string{"Backup &amp; import", "Move servers, keep copies, bring in existing comics", "Backups", "Last backup", "Never",
			"Schedule", "Off", "Destination", ta.Deps.Cfg.BackupDir(), "Back up now", "Restore…", "Import comics", "Drop CBZ, CBR",
			"Export everything: one archive with projects, characters, packs and settings. No lock-in.", `data-screen="G10"`} {
			if !strings.Contains(page, want) {
				t.Fatalf("G10 is missing %q", want)
			}
		}
		if r := form(admin, "/admin/backup/now", nil); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("back up now %d %s", r.StatusCode, r.Body)
		}
		live := string(admin.Get("/admin/backup").Body)
		if !strings.Contains(live, "Backing up") || !strings.Contains(live, `hx-get="/admin/backup/status"`) {
			t.Fatal("queued backup shows no progress")
		}
		runJobs(t, ta)
		status := admin.Get("/admin/backup/status")
		if status.Header.Get("HX-Refresh") != "true" {
			t.Fatal("finished backup does not refresh the page")
		}
		list, _ := svcOf(t, ta).List(context.Background(), backup.KindManual, 10)
		if len(list) != 1 || list[0].Status != backup.StatusDone || list[0].CreatedBy != ta.Admin.ID {
			t.Fatalf("manual backups %+v", list)
		}
		page = string(admin.Get("/admin/backup").Body)
		if !strings.Contains(page, "Today ") || !strings.Contains(page, "Manual · Today") {
			t.Fatal("last backup not shown")
		}
		dl := admin.Get("/admin/backup/archives/" + list[0].ID + "/download")
		if dl.StatusCode != 200 || !strings.Contains(dl.Header.Get("Content-Disposition"), "attachment") || int64(len(dl.Body)) != list[0].Bytes {
			t.Fatalf("download %d %s", dl.StatusCode, dl.Header)
		}
		if _, err := zip.NewReader(bytes.NewReader(dl.Body), int64(len(dl.Body))); err != nil {
			t.Fatalf("download is not an archive: %v", err)
		}
		// Schedule: validation, then a writable destination.
		bad := form(admin, "/admin/backup/schedule", url.Values{"frequency": {"daily"}, "hour": {"3"}, "weekday": {"0"}, "keep": {"0"}, "destination": {"/no/such/dir"}})
		if bad.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(bad.Body), "Enter a number from 1 to 365.") {
			t.Fatalf("schedule validation %d", bad.StatusCode)
		}
		dest := t.TempDir()
		ok := form(admin, "/admin/backup/schedule", url.Values{"frequency": {"daily"}, "hour": {"3"}, "weekday": {"0"}, "keep": {"14"}, "destination": {dest}})
		if ok.StatusCode != http.StatusSeeOther {
			t.Fatalf("schedule save %d %s", ok.StatusCode, ok.Body)
		}
		page = string(admin.Get("/admin/backup").Body)
		if !strings.Contains(page, "Daily · keep 14") || !strings.Contains(page, dest) {
			t.Fatal("schedule not shown")
		}
		// Export everything writes an export archive into the destination.
		form(admin, "/admin/backup/export", nil)
		runJobs(t, ta)
		exports, _ := svcOf(t, ta).List(context.Background(), backup.KindExport, 10)
		if len(exports) != 1 || filepath.Dir(pathOf(t, ta, exports[0].ID)) != dest {
			t.Fatalf("export %+v", exports)
		}
		if !strings.Contains(string(admin.Get("/admin/backup").Body), "Download · Today") {
			t.Fatal("export download missing")
		}
		// Delete.
		if r := form(admin, "/admin/backup/archives/"+list[0].ID+"/delete", nil); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("delete %d", r.StatusCode)
		}
		if got, _ := svcOf(t, ta).Get(context.Background(), list[0].ID); got.Status != backup.StatusDeleted {
			t.Fatalf("deleted status %s", got.Status)
		}
		if r := admin.Get("/admin/backup/archives/" + list[0].ID + "/download"); r.StatusCode != http.StatusNotFound {
			t.Fatalf("deleted archive downloadable: %d", r.StatusCode)
		}
		var actions []string
		_ = ta.Deps.DB.Select(context.Background(), &actions, `SELECT action FROM audit_log WHERE action LIKE 'backup.%' ORDER BY created_at`)
		if strings.Join(actions, ",") != "backup.create,backup.schedule,backup.create,backup.delete" {
			t.Fatalf("audit %v", actions)
		}
	})
}

func uploadArchive(t *testing.T, c *testutil.Client, data []byte) *testutil.Response {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	w, _ := mw.CreateFormFile("archive", "harbour-ghosts.mvbackup")
	_, _ = w.Write(data)
	_ = mw.Close()
	return c.Do(http.MethodPost, "/admin/backup/upload", &body, mw.FormDataContentType())
}

func TestG10UploadAndRestoreWithTypedConfirmation(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		seedServer(t, ta)
		admin := ta.Login(t, "admin")
		b, err := svcOf(t, ta).Create(ctx, backup.Request{Kind: backup.KindExport})
		if err != nil {
			t.Fatal(err)
		}
		want := fingerprints(t, ta)
		data, _ := os.ReadFile(pathOf(t, ta, b.ID))
		if r := uploadArchive(t, admin, []byte("not an archive")); r.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(r.Body), "This file cannot be restored") {
			t.Fatalf("bad upload %d", r.StatusCode)
		}
		r := uploadArchive(t, admin, data)
		loc := r.Header.Get("Location")
		if r.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/admin/backup?restore=") {
			t.Fatalf("upload %d %s", r.StatusCode, r.Body)
		}
		id := strings.TrimPrefix(loc, "/admin/backup?restore=")
		panel := string(admin.Get(loc).Body)
		if !strings.Contains(panel, "Restore a backup") || !strings.Contains(panel, `value="`+id+`" checked`) || !strings.Contains(panel, "uploaded") {
			t.Fatal("restore panel does not preselect the upload")
		}
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE projects SET title = 'Moth & Mortar'`); err != nil {
			t.Fatal(err)
		}
		if r := form(admin, "/admin/backup/restore", url.Values{"archive": {id}, "confirm": {"yes"}}); r.StatusCode != http.StatusUnprocessableEntity ||
			!strings.Contains(string(r.Body), "Type restore to confirm.") {
			t.Fatalf("wrong confirmation %d", r.StatusCode)
		}
		if r := form(admin, "/admin/backup/restore", url.Values{"archive": {id}, "confirm": {"restore"}}); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("restore %d %s", r.StatusCode, r.Body)
		}
		s := svcOf(t, ta)
		deadline := time.Now().Add(30 * time.Second)
		for s.State().Running && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if st := s.State(); st.Running || st.Err != nil {
			t.Fatalf("restore state %+v", st)
		}
		ta.Deps.Settings.Invalidate()
		if ta.Deps.Settings.MustInstance(ctx).Maintenance.Enabled {
			t.Fatal("maintenance mode left on after the restore")
		}
		got := fingerprints(t, ta)
		for table, rows := range want {
			if strings.Join(rows, "\n") != strings.Join(got[table], "\n") {
				t.Fatalf("%s differs after a G10 restore", table)
			}
		}
		page := string(admin.Get("/admin/backup").Body)
		if !strings.Contains(page, "Restored from a backup") {
			t.Fatal("restore notice missing")
		}
	})
}

func TestMemberExportHoldsOnlyTheirData(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f := seedServer(t, ta) // the demo project belongs to the admin; rin wrote a reply
		st := core.MustUse[studio.Service](ta.Deps)
		actor, _ := st.ActorFromUser(ctx, f.member.ID)
		p, err := st.CreateProject(ctx, actor, studio.NewProject{Title: "Iron Koi"})
		if err != nil {
			t.Fatal(err)
		}
		// rin keeps a sealed two-factor secret (it must not be exported) but signs in without it here.
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE users SET totp_enabled = FALSE WHERE id = ?`, f.member.ID); err != nil {
			t.Fatal(err)
		}
		c := ta.Login(t, "rin")
		r := c.Do(http.MethodPost, "/api/v1/export/me", strings.NewReader("scope=everything"), "application/x-www-form-urlencoded")
		if r.StatusCode != 200 || !strings.Contains(r.Header.Get("Content-Disposition"), "attachment") {
			t.Fatalf("export %d %s", r.StatusCode, r.Body)
		}
		zr, err := zip.NewReader(bytes.NewReader(r.Body), int64(len(r.Body)))
		if err != nil {
			t.Fatal(err)
		}
		rows := map[string][]map[string]any{}
		var m backup.Manifest
		for _, zf := range zr.File {
			rc, _ := zf.Open()
			if zf.Name == "manifest.json" {
				_ = json.NewDecoder(rc).Decode(&m)
			} else if strings.HasPrefix(zf.Name, "db/") {
				dec := json.NewDecoder(rc)
				name := strings.TrimSuffix(strings.TrimPrefix(zf.Name, "db/"), ".jsonl")
				rows[name] = []map[string]any{}
				for {
					var row map[string]any
					if dec.Decode(&row) != nil {
						break
					}
					rows[name] = append(rows[name], row)
				}
			} else if strings.HasPrefix(zf.Name, "media/") {
				t.Fatalf("rin has no files, archive holds %s", zf.Name)
			}
			_ = rc.Close()
		}
		if m.Kind != backup.KindUserExport || m.UserID != f.member.ID {
			t.Fatalf("manifest %+v", m)
		}
		for _, banned := range []string{"sessions", "api_tokens", "providers", "jobs", "instance_settings", "backups", "audit_log", "password_resets", "invites", "world_links"} {
			if _, ok := rows[banned]; ok {
				t.Fatalf("member export holds %s", banned)
			}
		}
		if u := rows["users"]; len(u) != 1 || u[0]["id"] != f.member.ID || u[0]["password_hash"] != "" || u[0]["totp_secret_enc"] != "" {
			t.Fatalf("users %v", u)
		}
		if pr := rows["projects"]; len(pr) != 1 || pr[0]["id"] != p.ID {
			t.Fatalf("projects %v", pr)
		}
		if cm := rows["comments"]; len(cm) != 1 || cm[0]["id"] != f.childID {
			t.Fatalf("comments %v", cm)
		}
		if len(rows["pages"]) != 0 || len(rows["characters"]) != 0 {
			t.Fatal("the admin's demo project leaked into rin's export")
		}
		for table, list := range rows {
			for _, row := range list {
				for _, col := range []string{"user_id", "owner_id", "author_id"} {
					if v, ok := row[col]; ok && v != nil && v != f.member.ID {
						t.Fatalf("%s row of another member: %v", table, row)
					}
				}
			}
		}
		// Settings → Data shows the export; only rin can download it.
		ex, ok, err := core.MustUse[backup.MemberExports](ta.Deps).LastUserExport(ctx, f.member.ID)
		if err != nil || !ok {
			t.Fatalf("last export %v %v", ok, err)
		}
		if r := c.Get(backup.LastExportURL(ex)); r.StatusCode != 200 {
			t.Fatalf("own export download %d", r.StatusCode)
		}
		if r := ta.Login(t, "admin").Get(backup.LastExportURL(ex)); r.StatusCode != http.StatusNotFound {
			t.Fatalf("someone else's export downloadable: %d", r.StatusCode)
		}
		if r := ta.Login(t, "admin").Get("/admin/backup/archives/" + ex.ID + "/download"); r.StatusCode != http.StatusNotFound {
			t.Fatalf("member export downloadable from G10: %d", r.StatusCode)
		}
		if body := string(c.Get("/settings/data").Body); !strings.Contains(body, "Last export") || !strings.Contains(body, backup.LastExportURL(ex)) {
			t.Fatal("Settings → Data does not show the last export")
		}
	})
}

func TestCLI(t *testing.T) {
	ta := testutil.NewTestApp(t)
	ctx := context.Background()
	seedServer(t, ta)
	run := func(sub string, args ...string) (string, error) {
		var out bytes.Buffer
		fs := newFlagSet()
		opts := backup.CLIFlags(fs)
		pos, err := backup.ParseInterspersed(fs, args)
		if err != nil {
			return "", err
		}
		err = backup.RunCLI(ctx, ta.Deps, sub, pos, opts, &out)
		return out.String(), err
	}
	out, err := run("create", "--kind", "export")
	if err != nil || !strings.HasPrefix(out, "created ") {
		t.Fatalf("create %q %v", out, err)
	}
	id := strings.Fields(out)[1]
	if !db.ValidID(id) {
		t.Fatalf("id %q", id)
	}
	if out, err := run("list"); err != nil || !strings.Contains(out, id) || !strings.Contains(out, "export") {
		t.Fatalf("list %q %v", out, err)
	}
	if _, err := run("restore", id); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("restore without --yes: %v", err)
	}
	svcOf(t, ta).Announce(ctx)
	if _, err := run("restore", id, "--yes"); err == nil || !strings.Contains(err.Error(), "server is running") {
		t.Fatalf("restore under a running server: %v", err)
	}
	if _, err := ta.Deps.DB.Exec(ctx, `DELETE FROM instance_settings WHERE key LIKE 'backup.server.%'`); err != nil {
		t.Fatal(err)
	}
	out, err = run("restore", pathOf(t, ta, id), "--yes")
	if err != nil || !strings.HasPrefix(out, "restored ") {
		t.Fatalf("restore by path %q %v", out, err)
	}
	ta.Deps.Settings.Invalidate()
	if ta.Deps.Settings.MustInstance(ctx).Maintenance.Enabled {
		t.Fatal("CLI restore left maintenance mode on")
	}
	if out, err := run("delete", id, "--yes"); err != nil || !strings.Contains(out, "deleted") {
		t.Fatalf("delete %q %v", out, err)
	}
	if _, err := run("frobnicate"); err == nil {
		t.Fatal("unknown command accepted")
	}
}

func newFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}
