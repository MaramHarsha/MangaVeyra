package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/secrets"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

func TestApprovedRootIsADirectTemporaryChild(t *testing.T) {
	for path, want := range map[string]bool{
		"/tmp/mv-phase2-live":                 true,
		"/private/tmp/mv-phase2-live":         true,
		"/tmp/mv-phase2-":                     false,
		"/tmp/mv-phase2-live/nested":          false,
		"/tmp/other":                          false,
		"/tmp":                                false,
		"/var/tmp/mv-phase2-live":             false,
		"tmp/mv-phase2-live":                  false,
		"/tmp/mv-phase2-live/../mv-phase2-x":  false,
		"/Users/owner/mv-phase2-live":         false,
		"/private/tmp/mv-phase2-live/secrets": false,
	} {
		if got := approvedRoot(path); got != want {
			t.Errorf("approvedRoot(%q) = %v, want %v", path, got, want)
		}
	}
}

// tempRoot makes a directory directly under /tmp, as the harness requires,
// and removes it when the test ends.
func tempRoot(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mv-phase2-unittest-"+name+"-")
	if err != nil {
		t.Skipf("cannot create a directory under /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupRefusesAnythingButAMarkedTemporaryRoot(t *testing.T) {
	unmarked := tempRoot(t, "model")
	write(t, filepath.Join(unmarked, "model.safetensors"), "weights")
	marked := filepath.Join(t.TempDir(), "elsewhere")
	write(t, filepath.Join(marked, "acceptance.json"), "{}")
	link := filepath.Join("/tmp", "mv-phase2-unittest-link-"+db.NewID())
	if err := os.Symlink(marked, link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
	nested := filepath.Join(tempRoot(t, "outer"), "inner")
	write(t, filepath.Join(nested, "acceptance.json"), "{}")
	for _, path := range []string{unmarked, link, nested, marked, "/tmp/mv-phase2-", "relative/mv-phase2-x", "/tmp"} {
		if err := cleanupRoot(path); err == nil {
			t.Errorf("cleanup accepted %s", path)
		}
	}
	for _, kept := range []string{filepath.Join(unmarked, "model.safetensors"), filepath.Join(marked, "acceptance.json"), filepath.Join(nested, "acceptance.json")} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("refused cleanup removed %s: %v", kept, err)
		}
	}
}

func TestCleanupScrubsAndRemovesTheApprovedRoot(t *testing.T) {
	root := tempRoot(t, "live")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	write(t, outside, "keep me")
	for name, content := range map[string]string{"acceptance.json": "{}", "worker-token": "token", "secret.key": "throwaway", throwawayLogin: "password", "mangaveyra.db": "rows", "media/projects/p/page.png": "pixels"} {
		write(t, filepath.Join(root, name), content)
	}
	if err := os.Symlink(outside, filepath.Join(root, "media", "link")); err != nil {
		t.Fatal(err)
	}
	if err := cleanupRoot(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("approved root still exists: %v", err)
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "keep me" {
		t.Fatalf("cleanup followed a symlink out of the root: %q %v", b, err)
	}
}

// Init copies one provider row re-encrypted under the isolated instance's own
// key and an account without its password hash, email or TOTP secret.
func TestCopyConnectionReencryptsUnderTheIsolatedKey(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	sourceKey := secrets.GenerateKey()
	write(t, filepath.Join(source, "secret.key"), sourceKey+"\n")
	parsed, err := secrets.ParseKey(sourceKey)
	if err != nil {
		t.Fatal(err)
	}
	sourceBox, err := secrets.New(parsed)
	if err != nil {
		t.Fatal(err)
	}
	src, err := db.Open(ctx, "sqlite", filepath.Join(source, "mangaveyra.db"), db.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = src.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	user, provider := db.NewID(), db.NewID()
	ownerHash, err := auth.HashPassword("Owner_Real_Password_2026!", auth.FastHashParams)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = src.Exec(ctx, `INSERT INTO users (id,username,email,display_name,password_hash,role,status,totp_secret_enc,totp_enabled,totp_recovery) VALUES (?,?,?,?,?,?,?,?,?,?)`, user, "tide-owner", "owner@example.test", "Tide Owner", ownerHash, "admin", "active", "v1:sealed-totp", true, `["recovery"]`); err != nil {
		t.Fatal(err)
	}
	apiKey := "sk-isolated-test-key"
	if _, err = src.Exec(ctx, `INSERT INTO providers (id,kind,name,key_enc,key_hint,enabled,scope,owner_id) VALUES (?,?,?,?,?,?,?,?)`, provider, "openai", "OpenAI", sourceBox.Seal([]byte(apiKey), []byte(providerKeyAAD(provider))), "…-key", true, "server", user); err != nil {
		t.Fatal(err)
	}
	if err = src.Close(); err != nil {
		t.Fatal(err)
	}
	target := testdb.Open(t, "sqlite")
	targetBox, err := secrets.New(bytes.Repeat([]byte{7}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err = copyConnection(ctx, target, targetBox, source, root, user, provider); err != nil {
		t.Fatal(err)
	}
	var sealed string
	if err = target.Get(ctx, &sealed, `SELECT key_enc FROM providers WHERE id=?`, provider); err != nil {
		t.Fatal(err)
	}
	if got, err := targetBox.Open(sealed, []byte(providerKeyAAD(provider))); err != nil || string(got) != apiKey {
		t.Fatalf("provider key is not sealed under the isolated key: %v", err)
	}
	if _, err := sourceBox.Open(sealed, []byte(providerKeyAAD(provider))); err == nil {
		t.Fatal("the isolated row still opens with the production key")
	}
	var u struct {
		Username string  `db:"username"`
		Email    *string `db:"email"`
		Hash     string  `db:"password_hash"`
		TOTP     string  `db:"totp_secret_enc"`
		Enabled  bool    `db:"totp_enabled"`
		Recovery string  `db:"totp_recovery"`
		Role     string  `db:"role"`
	}
	if err = target.Get(ctx, &u, `SELECT username,email,password_hash,totp_secret_enc,totp_enabled,totp_recovery,role FROM users WHERE id=?`, user); err != nil {
		t.Fatal(err)
	}
	if u.Username != "tide-owner" || u.Role != "admin" || u.Email != nil || u.Hash == ownerHash || u.TOTP != "" || u.Enabled || u.Recovery != "[]" {
		t.Fatalf("isolated account copied private credentials: %+v", u)
	}
	login, err := os.ReadFile(filepath.Join(root, throwawayLogin))
	if err != nil {
		t.Fatal(err)
	}
	password := strings.TrimPrefix(strings.Split(strings.TrimSpace(string(login)), "\n")[1], "password: ")
	if !auth.VerifyPassword(password, u.Hash) || auth.VerifyPassword("Owner_Real_Password_2026!", u.Hash) {
		t.Fatal("the isolated account does not use only its throwaway password")
	}
	if info, err := os.Stat(filepath.Join(root, throwawayLogin)); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("throwaway login permissions: %v %v", info.Mode(), err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 1 {
		t.Fatalf("init wrote more than the throwaway login into the root: %v", entries)
	}
}
