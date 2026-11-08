package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/secrets"
)

// rootPrefix is the only name an approved temporary data root may have.
const rootPrefix = "mv-phase2-"

// throwawayLogin holds the isolated preview's one-time login. It is created
// at init and removed by the cleanup stage with the rest of the root.
const throwawayLogin = "throwaway-login"

// approvedRoot reports whether path names an approved temporary data root:
// a direct child of /tmp (or macOS's /private/tmp) named mv-phase2-*.
func approvedRoot(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	base := filepath.Base(path)
	parent := filepath.Dir(path)
	return (parent == "/tmp" || parent == "/private/tmp") && strings.HasPrefix(base, rootPrefix) && len(base) > len(rootPrefix)
}

// cleanupRoot removes an approved data root: the isolated database, its
// throwaway key, worker token, login and generated media. It refuses anything
// that is not an existing, real (non-symlink) mv-phase2- directory directly
// under the temporary directory and carrying the harness's own marker files,
// so a model directory or an unrelated path is never touched. Regular files
// are overwritten with random bytes before removal; on copy-on-write or
// flash storage that is best effort, so the removal itself is the guarantee.
// Symlinks inside the root are removed, never followed.
func cleanupRoot(root string) error {
	root = filepath.Clean(root)
	if !approvedRoot(root) {
		return errors.New("cleanup only removes an mv-phase2- directory directly under the temporary directory")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("cleanup: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("cleanup refuses a symlink or a file in place of the data directory")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || !approvedRoot(resolved) {
		return errors.New("cleanup refuses a data directory that resolves outside the temporary directory")
	}
	marked := false
	for _, marker := range []string{"acceptance.json", "worker-token"} {
		if m, err := os.Lstat(filepath.Join(resolved, marker)); err == nil && m.Mode().IsRegular() {
			marked = true
		}
	}
	if !marked {
		return errors.New("cleanup refuses a directory that is not a phase2check data root (no acceptance.json or worker-token)")
	}
	err = filepath.WalkDir(resolved, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			return scrub(path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("cleanup: %w", err)
	}
	return os.RemoveAll(resolved)
}

// scrub overwrites one regular file with random bytes and syncs it.
func scrub(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if _, err = io.CopyN(f, rand.Reader, info.Size()); err != nil {
		return err
	}
	return f.Sync()
}

// providerKeyAAD matches internal/ai's binding of a sealed key to its row.
func providerKeyAAD(id string) string { return "provider:" + id + ":key" }

// copyConnection copies only what the approved live run needs into the new
// isolated database: the saved OpenAI provider row, re-encrypted under the
// isolated instance's own freshly generated key (target), and an account row
// without the password hash, email, TOTP secret or recovery codes. The source
// master key is read into memory only to open that one sealed key; it is never
// written into the temporary root. The account gets a random throwaway
// password for the isolated preview, stored 0600 in the root.
func copyConnection(ctx context.Context, target *db.DB, box *secrets.Box, source, root, user, provider string) error {
	raw, err := os.ReadFile(filepath.Join(source, "secret.key"))
	if err != nil {
		return errors.New("cannot read the saved encryption key file")
	}
	key, err := secrets.ParseKey(string(raw))
	clear(raw)
	if err != nil {
		return errors.New("cannot parse the saved encryption key file")
	}
	sourceBox, err := secrets.New(key)
	clear(key)
	if err != nil {
		return err
	}
	src, err := sql.Open("sqlite", "file:"+filepath.Join(source, "mangaveyra.db")+"?mode=ro")
	if err != nil {
		return err
	}
	defer src.Close()
	var p struct {
		Kind, Name, BaseURL, KeyEnc, KeyHint, Config, Scope, LastTest string
		Enabled                                                       bool
		OwnerID                                                       sql.NullString
	}
	if err = src.QueryRowContext(ctx, `SELECT kind,name,base_url,key_enc,key_hint,config,enabled,scope,owner_id,last_test FROM providers WHERE id=?`, provider).Scan(&p.Kind, &p.Name, &p.BaseURL, &p.KeyEnc, &p.KeyHint, &p.Config, &p.Enabled, &p.Scope, &p.OwnerID, &p.LastTest); err != nil {
		return errors.New("saved connection not found")
	}
	if p.Kind != "openai" || !p.Enabled || p.KeyEnc == "" {
		return errors.New("source must be an enabled saved OpenAI connection")
	}
	if p.OwnerID.Valid && p.OwnerID.String != user {
		return errors.New("the saved connection belongs to another account")
	}
	plain, err := sourceBox.Open(p.KeyEnc, []byte(providerKeyAAD(provider)))
	if err != nil {
		return errors.New("cannot open the saved connection key")
	}
	sealed := box.Seal(plain, []byte(providerKeyAAD(provider)))
	clear(plain)
	var u struct {
		Username, DisplayName, Role, Status, Locale, Timezone string
	}
	if err = src.QueryRowContext(ctx, `SELECT username,display_name,role,status,locale,timezone FROM users WHERE id=?`, user).Scan(&u.Username, &u.DisplayName, &u.Role, &u.Status, &u.Locale, &u.Timezone); err != nil {
		return errors.New("saved account not found")
	}
	secret := make([]byte, 24)
	if _, err = rand.Read(secret); err != nil {
		return err
	}
	password := base64.RawURLEncoding.EncodeToString(secret)
	hash, err := auth.HashPassword(password, auth.DefaultHashParams)
	if err != nil {
		return err
	}
	now := db.Now()
	err = target.WithTx(ctx, func(tx *db.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO users (id,username,display_name,password_hash,role,status,locale,timezone,password_changed_at,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, user, u.Username, u.DisplayName, hash, u.Role, u.Status, u.Locale, u.Timezone, now, now, now); err != nil {
			return errors.New("cannot create the isolated account")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO providers (id,kind,name,base_url,key_enc,key_hint,config,enabled,scope,owner_id,last_test,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, provider, p.Kind, p.Name, p.BaseURL, sealed, p.KeyHint, p.Config, p.Enabled, p.Scope, p.OwnerID, p.LastTest, now, now); err != nil {
			return errors.New("cannot copy the re-encrypted connection into the isolated database")
		}
		return nil
	})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, throwawayLogin), []byte("username: "+u.Username+"\npassword: "+password+"\n"), 0600)
}
