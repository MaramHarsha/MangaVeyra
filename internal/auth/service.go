// Package auth owns accounts and authentication: users and roles, argon2id
// passwords, hashed session tokens with "stay signed in", sign-up modes and
// invites, password reset, TOTP two-factor with recovery codes, bearer tokens
// for agents (mva_), workers (mvw_) and personal API use (mvk_), login rate
// limiting, project-role helpers and the audit log.
//
// The package exposes a Service plus JSON handlers; HTML pages (login,
// sign-up, settings) are built by the web layer on top of the Service.
package auth

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/mail"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/secrets"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

// User is the account type shared with the HTTP layer.
type User = httpx.User

// Site roles.
const (
	RoleAdmin   = httpx.RoleAdmin
	RoleCreator = httpx.RoleCreator
	RoleReader  = httpx.RoleReader
)

// Account statuses.
const (
	StatusActive    = "active"
	StatusSuspended = "suspended"
	StatusPending   = "pending"
)

// Mailer sends plain-text email. The notify package implements it.
type Mailer interface {
	EmailEnabled() bool
	SendEmail(ctx context.Context, to, subject, text string) error
}

// Options configure the Service.
type Options struct {
	// PublicURL is the external base URL used in reset and invite links.
	PublicURL string
	// WorkerToken is the MV_WORKER_TOKEN shared secret ("" disables it).
	WorkerToken string
	// SecureCookies forces the Secure cookie attribute (in addition to HTTPS requests).
	SecureCookies bool
	// HashParams overrides DefaultHashParams (tests use FastHashParams).
	HashParams *HashParams
	Logger     *slog.Logger
}

// Service implements authentication and account management.
type Service struct {
	db       *db.DB
	settings *settings.Service
	box      *secrets.Box
	opts     Options
	params   HashParams
	log      *slog.Logger
	mailer   atomic.Pointer[Mailer]
	dummy    string

	loginIP   *httpx.Limiter
	loginUser *httpx.Limiter
	resetRL   *httpx.Limiter
	reauthRL  *httpx.Limiter
	hasAdmin  atomic.Bool

	// Now is the clock (tests may replace it).
	Now func() time.Time
}

// New creates the auth service.
func New(d *db.DB, st *settings.Service, box *secrets.Box, opts Options) *Service {
	p := DefaultHashParams
	if opts.HashParams != nil {
		p = *opts.HashParams
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	s := &Service{
		db: d, settings: st, box: box, opts: opts, params: p, log: log,
		// 10 attempts per IP, refilling one every 30 s; 5 per account, refilling one per minute.
		loginIP:   httpx.NewLimiter(1.0/30, 10),
		loginUser: httpx.NewLimiter(1.0/60, 5),
		resetRL:   httpx.NewLimiter(1.0/300, 3),
		// Re-entering the password for sensitive changes: 10 tries, then one a minute.
		reauthRL: httpx.NewLimiter(1.0/60, 10),
		Now:      db.Now,
	}
	s.dummy, _ = HashPassword("mangaveyra-dummy-password", p)
	return s
}

// SetMailer installs the email sender (called during wiring once notify exists).
func (s *Service) SetMailer(m Mailer) { s.mailer.Store(&m) }

func (s *Service) mail() Mailer {
	if p := s.mailer.Load(); p != nil && *p != nil && (*p).EmailEnabled() {
		return *p
	}
	return nil
}

// EmailEnabled reports whether outgoing email is configured.
func (s *Service) EmailEnabled() bool { return s.mail() != nil }

func (s *Service) now() time.Time { return s.Now().UTC().Truncate(time.Microsecond) }

// PublicURL returns the base URL for links (instance setting overrides config).
func (s *Service) PublicURL(ctx context.Context) string {
	if inst, err := s.settings.Instance(ctx); err == nil && inst.PublicURL != "" {
		return strings.TrimRight(inst.PublicURL, "/")
	}
	return strings.TrimRight(s.opts.PublicURL, "/")
}

// ---- users ---------------------------------------------------------------

const userCols = `id, username, email, display_name, password_hash, role, status, avatar_key, bio, locale, timezone,
	totp_secret_enc, totp_enabled, totp_recovery, totp_last_step, xp, level, created_at, last_login_at`

type userRow struct {
	ID            string            `db:"id"`
	Username      string            `db:"username"`
	Email         sql.NullString    `db:"email"`
	DisplayName   string            `db:"display_name"`
	PasswordHash  string            `db:"password_hash"`
	Role          string            `db:"role"`
	Status        string            `db:"status"`
	AvatarKey     string            `db:"avatar_key"`
	Bio           string            `db:"bio"`
	Locale        string            `db:"locale"`
	Timezone      string            `db:"timezone"`
	TOTPSecretEnc string            `db:"totp_secret_enc"`
	TOTPEnabled   bool              `db:"totp_enabled"`
	TOTPRecovery  db.JSON[[]string] `db:"totp_recovery"`
	TOTPLastStep  int64             `db:"totp_last_step"`
	XP            int64             `db:"xp"`
	Level         int               `db:"level"`
	CreatedAt     time.Time         `db:"created_at"`
	LastLoginAt   sql.NullTime      `db:"last_login_at"`
}

func (r *userRow) user() *User {
	u := &User{
		ID: r.ID, Username: r.Username, Email: r.Email.String, DisplayName: r.DisplayName, Role: r.Role,
		Status: r.Status, AvatarKey: r.AvatarKey, Bio: r.Bio, Locale: r.Locale, Timezone: r.Timezone,
		TOTPEnabled: r.TOTPEnabled, XP: r.XP, Level: r.Level, CreatedAt: r.CreatedAt,
	}
	if r.LastLoginAt.Valid {
		t := r.LastLoginAt.Time
		u.LastLoginAt = &t
	}
	return u
}

func (s *Service) row(ctx context.Context, q db.Querier, where string, args ...any) (*userRow, error) {
	var r userRow
	if err := q.Get(ctx, &r, `SELECT `+userCols+` FROM users WHERE `+where, args...); err != nil {
		if db.IsNotFound(err) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return &r, nil
}

// UserByID returns a user.
func (s *Service) UserByID(ctx context.Context, id string) (*User, error) {
	r, err := s.row(ctx, s.db, `id = ?`, id)
	if err != nil {
		return nil, err
	}
	return r.user(), nil
}

// UserByUsername returns a user by (case-insensitive) username.
func (s *Service) UserByUsername(ctx context.Context, username string) (*User, error) {
	r, err := s.row(ctx, s.db, `username = ?`, NormalizeUsername(username))
	if err != nil {
		return nil, err
	}
	return r.user(), nil
}

// UserByLogin finds a user by email or username.
func (s *Service) UserByLogin(ctx context.Context, identifier string) (*User, error) {
	r, err := s.rowByLogin(ctx, identifier)
	if err != nil {
		return nil, err
	}
	return r.user(), nil
}

func (s *Service) rowByLogin(ctx context.Context, identifier string) (*userRow, error) {
	identifier = strings.TrimSpace(identifier)
	if strings.Contains(identifier, "@") {
		return s.row(ctx, s.db, `email = ?`, NormalizeEmail(identifier))
	}
	return s.row(ctx, s.db, `username = ?`, NormalizeUsername(identifier))
}

// ListFilter filters ListUsers.
type ListFilter struct {
	Query  string // matches username, display name or email
	Role   string
	Status string
	Limit  int
	Offset int
}

// ListUsers lists accounts for the admin users screen, newest first.
func (s *Service) ListUsers(ctx context.Context, f ListFilter) ([]*User, int, error) {
	where, args := []string{"1=1"}, []any{}
	if f.Query != "" {
		like := "%" + strings.ToLower(f.Query) + "%"
		where = append(where, "(username LIKE ? OR LOWER(display_name) LIKE ? OR email LIKE ?)")
		args = append(args, like, like, like)
	}
	if f.Role != "" {
		where = append(where, "role = ?")
		args = append(args, f.Role)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.db.Get(ctx, &total, `SELECT COUNT(*) FROM users WHERE `+cond, args...); err != nil {
		return nil, 0, err
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	var rows []userRow
	if err := s.db.Select(ctx, &rows, `SELECT `+userCols+` FROM users WHERE `+cond+` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`,
		append(args, f.Limit, f.Offset)...); err != nil {
		return nil, 0, err
	}
	out := make([]*User, len(rows))
	for i := range rows {
		out[i] = rows[i].user()
	}
	return out, total, nil
}

// NewUser describes an account to create.
type NewUser struct {
	Username    string
	Email       string
	DisplayName string
	Password    string
	Role        string
	Status      string
	Locale      string
	InviteID    string
}

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{1,30}[a-z0-9_]$`)

var reservedUsernames = map[string]bool{
	"about": true, "account": true, "agents": true, "api": true, "auth": true, "explore": true, "help": true,
	"library": true, "login": true, "logout": true, "mangaveyra": true, "mcp": true, "me": true, "media": true,
	"models": true, "moderator": true, "new": true, "notifications": true, "null": true, "official": true,
	"plugins": true, "queue": true, "root": true, "search": true, "settings": true, "setup": true, "signup": true,
	"staff": true, "static": true, "studio": true, "support": true, "system": true, "undefined": true,
	"world": true, "ws": true,
}

// NormalizeUsername lowercases and trims a username.
func NormalizeUsername(u string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(u, "@")))
}

// NormalizeEmail lowercases and trims an email address.
func NormalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

// ValidateUsername checks format and reserved names.
func ValidateUsername(u string) error {
	if !usernamePattern.MatchString(u) || strings.Contains(u, "..") {
		return fieldError("username", "auth.username.invalid")
	}
	if reservedUsernames[u] {
		return fieldError("username", "auth.username.reserved")
	}
	return nil
}

// ValidateEmail checks an email address ("" is allowed: email is optional).
func ValidateEmail(e string) error {
	if e == "" {
		return nil
	}
	a, err := mail.ParseAddress(e)
	if err != nil || a.Address != e || !strings.Contains(e[strings.LastIndex(e, "@"):], ".") || len(e) > 254 {
		return fieldError("email", "auth.email.invalid")
	}
	return nil
}

func validRole(r string) bool { return r == RoleAdmin || r == RoleCreator || r == RoleReader }

// CreateUser creates an account. It validates input and hashes the password.
func (s *Service) CreateUser(ctx context.Context, in NewUser) (*User, error) {
	var u *User
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		var err error
		u, err = s.createUserTx(ctx, tx, in)
		return err
	})
	if err != nil {
		return nil, err
	}
	if u.Role == RoleAdmin {
		s.hasAdmin.Store(true)
	}
	s.Audit(ctx, AuditEntry{Action: "user.create", TargetKind: "user", TargetID: u.ID, Data: map[string]any{"role": u.Role}})
	return u, nil
}

func (s *Service) createUserTx(ctx context.Context, tx *db.Tx, in NewUser) (*User, error) {
	in.Username = NormalizeUsername(in.Username)
	in.Email = NormalizeEmail(in.Email)
	if err := ValidateUsername(in.Username); err != nil {
		return nil, err
	}
	if err := ValidateEmail(in.Email); err != nil {
		return nil, err
	}
	if err := CheckPasswordPolicy(in.Password); err != nil {
		return nil, err
	}
	if in.Role == "" {
		in.Role = RoleCreator
	}
	if !validRole(in.Role) {
		return nil, fmt.Errorf("auth: invalid role %q", in.Role)
	}
	if in.Status == "" {
		in.Status = StatusActive
	}
	if in.DisplayName == "" {
		in.DisplayName = in.Username
	}
	if len(in.DisplayName) > 64 {
		return nil, fieldError("display_name", "error.validation")
	}
	hash, err := HashPassword(in.Password, s.params)
	if err != nil {
		return nil, err
	}
	id, now := db.NewID(), s.now()
	_, err = tx.Exec(ctx, `INSERT INTO users (id, username, email, display_name, password_hash, role, status, locale, invite_id,
		password_changed_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.Username, db.NullString(in.Email), in.DisplayName, hash, in.Role, in.Status, in.Locale, db.NullString(in.InviteID), now, now, now)
	if err != nil {
		if db.IsUniqueViolation(err) {
			if strings.Contains(err.Error(), "email") {
				return nil, fieldError("email", "auth.email.taken")
			}
			return nil, fieldError("username", "auth.username.taken")
		}
		return nil, err
	}
	r, err := s.row(ctx, tx, `id = ?`, id)
	if err != nil {
		return nil, err
	}
	return r.user(), nil
}

// HasAdmin reports whether at least one active admin exists (cached once true).
func (s *Service) HasAdmin(ctx context.Context) (bool, error) {
	if s.hasAdmin.Load() {
		return true, nil
	}
	var n int
	if err := s.db.Get(ctx, &n, `SELECT COUNT(*) FROM users WHERE role = 'admin' AND status = 'active'`); err != nil {
		return false, err
	}
	if n > 0 {
		s.hasAdmin.Store(true)
	}
	return n > 0, nil
}

func (s *Service) countActiveAdmins(ctx context.Context, q db.Querier) (int, error) {
	var n int
	err := q.Get(ctx, &n, `SELECT COUNT(*) FROM users WHERE role = 'admin' AND status = 'active'`)
	return n, err
}

// SetRole changes a user's site role. The last active admin cannot be demoted.
func (s *Service) SetRole(ctx context.Context, userID, role string) error {
	if !validRole(role) {
		return fieldError("role", "error.validation")
	}
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		r, err := s.row(ctx, tx, `id = ?`, userID)
		if err != nil {
			return err
		}
		if r.Role == RoleAdmin && role != RoleAdmin && r.Status == StatusActive {
			if n, err := s.countActiveAdmins(ctx, tx); err != nil {
				return err
			} else if n <= 1 {
				return ErrLastAdmin
			}
		}
		_, err = tx.Exec(ctx, `UPDATE users SET role = ?, updated_at = ? WHERE id = ?`, role, s.now(), userID)
		return err
	})
	if err == nil {
		s.Audit(ctx, AuditEntry{Action: "user.set_role", TargetKind: "user", TargetID: userID, Data: map[string]any{"role": role}})
	}
	return err
}

// SetStatus activates, suspends or approves an account. Suspending revokes its sessions and tokens.
func (s *Service) SetStatus(ctx context.Context, userID, status, reason string) error {
	if status != StatusActive && status != StatusSuspended && status != StatusPending {
		return fieldError("status", "error.validation")
	}
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		r, err := s.row(ctx, tx, `id = ?`, userID)
		if err != nil {
			return err
		}
		if r.Role == RoleAdmin && r.Status == StatusActive && status != StatusActive {
			if n, err := s.countActiveAdmins(ctx, tx); err != nil {
				return err
			} else if n <= 1 {
				return ErrLastAdmin
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET status = ?, suspended_reason = ?, updated_at = ? WHERE id = ?`, status, reason, s.now(), userID); err != nil {
			return err
		}
		if status == StatusSuspended {
			if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE api_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, s.now(), userID); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		s.Audit(ctx, AuditEntry{Action: "user.set_status", TargetKind: "user", TargetID: userID, Data: map[string]any{"status": status, "reason": reason}})
	}
	return err
}

// ProfileUpdate holds editable profile fields (nil = unchanged).
type ProfileUpdate struct {
	DisplayName *string
	Email       *string
	Bio         *string
	Locale      *string
	Timezone    *string
	AvatarKey   *string
	Links       *[]string
}

// UpdateProfile edits a user's own profile fields.
func (s *Service) UpdateProfile(ctx context.Context, userID string, p ProfileUpdate) (*User, error) {
	sets, args := []string{}, []any{}
	if p.DisplayName != nil {
		v := strings.TrimSpace(*p.DisplayName)
		if v == "" || len(v) > 64 {
			return nil, fieldError("display_name", "error.validation")
		}
		sets, args = append(sets, "display_name = ?"), append(args, v)
	}
	if p.Email != nil {
		e := NormalizeEmail(*p.Email)
		if err := ValidateEmail(e); err != nil {
			return nil, err
		}
		sets, args = append(sets, "email = ?", "email_verified_at = NULL"), append(args, db.NullString(e))
	}
	if p.Bio != nil {
		if len(*p.Bio) > 2000 {
			return nil, fieldError("bio", "error.validation")
		}
		sets, args = append(sets, "bio = ?"), append(args, *p.Bio)
	}
	if p.Locale != nil {
		sets, args = append(sets, "locale = ?"), append(args, *p.Locale)
	}
	if p.Timezone != nil {
		if *p.Timezone != "" {
			if _, err := time.LoadLocation(*p.Timezone); err != nil {
				return nil, fieldError("timezone", "error.validation")
			}
		}
		sets, args = append(sets, "timezone = ?"), append(args, *p.Timezone)
	}
	if p.AvatarKey != nil {
		sets, args = append(sets, "avatar_key = ?"), append(args, *p.AvatarKey)
	}
	if p.Links != nil {
		if len(*p.Links) > 10 {
			return nil, fieldError("links", "error.validation")
		}
		sets, args = append(sets, "links = ?"), append(args, db.MustJSON(*p.Links))
	}
	if len(sets) > 0 {
		sets, args = append(sets, "updated_at = ?"), append(args, s.now(), userID)
		if _, err := s.db.Exec(ctx, `UPDATE users SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
			if db.IsUniqueViolation(err) {
				return nil, fieldError("email", "auth.email.taken")
			}
			return nil, err
		}
	}
	return s.UserByID(ctx, userID)
}

// SetPassword replaces a password (admin reset or CLI) and signs the user out everywhere.
func (s *Service) SetPassword(ctx context.Context, userID, password string) error {
	if err := CheckPasswordPolicy(password); err != nil {
		return err
	}
	hash, err := HashPassword(password, s.params)
	if err != nil {
		return err
	}
	return s.db.WithTx(ctx, func(tx *db.Tx) error {
		res, err := tx.Exec(ctx, `UPDATE users SET password_hash = ?, password_changed_at = ?, updated_at = ? WHERE id = ?`, hash, s.now(), s.now(), userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrUserNotFound
		}
		_, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
		return err
	})
}

// ChangePassword checks the current password and sets a new one, keeping only
// the session keepSessionID signed in.
func (s *Service) ChangePassword(ctx context.Context, userID, current, next, keepSessionID string) error {
	r, err := s.row(ctx, s.db, `id = ?`, userID)
	if err != nil {
		return err
	}
	if err := s.checkReauth(r, current); err != nil {
		return err
	}
	if err := CheckPasswordPolicy(next); err != nil {
		return err
	}
	hash, err := HashPassword(next, s.params)
	if err != nil {
		return err
	}
	err = s.db.WithTx(ctx, func(tx *db.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = ?, password_changed_at = ?, updated_at = ? WHERE id = ?`, hash, s.now(), s.now(), userID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, keepSessionID)
		return err
	})
	if err == nil {
		s.Audit(ctx, AuditEntry{Action: "user.change_password", TargetKind: "user", TargetID: userID})
	}
	return err
}

// checkReauth verifies the current password a signed-in user re-enters for a
// sensitive change (password, two-factor, account deletion). Attempts are
// rate limited per account so a stolen session cannot brute-force it.
func (s *Service) checkReauth(r *userRow, password string) error {
	if ok, wait := s.reauthRL.Allow("u:" + r.ID); !ok {
		return httpx.ErrRateLimited(wait)
	}
	if r.PasswordHash == "" || !VerifyPassword(password, r.PasswordHash) {
		return ErrWrongPassword
	}
	s.reauthRL.Reset("u:" + r.ID)
	return nil
}

// CheckPassword verifies userID's current password (rate limited per
// account). Use it whenever a page asks a signed-in user to confirm a
// sensitive action with their password.
func (s *Service) CheckPassword(ctx context.Context, userID, password string) error {
	r, err := s.row(ctx, s.db, `id = ?`, userID)
	if err != nil {
		return err
	}
	return s.checkReauth(r, password)
}

// DeleteUser removes an account and (through foreign keys) its sessions, tokens and owned projects.
func (s *Service) DeleteUser(ctx context.Context, userID string) error {
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		r, err := s.row(ctx, tx, `id = ?`, userID)
		if err != nil {
			return err
		}
		if r.Role == RoleAdmin && r.Status == StatusActive {
			if n, err := s.countActiveAdmins(ctx, tx); err != nil {
				return err
			} else if n <= 1 {
				return ErrLastAdmin
			}
		}
		_, err = tx.Exec(ctx, `DELETE FROM users WHERE id = ?`, userID)
		return err
	})
	if err == nil {
		s.Audit(ctx, AuditEntry{Action: "user.delete", TargetKind: "user", TargetID: userID})
	}
	return err
}

// ---- project roles ---------------------------------------------------------

// Project roles.
const (
	ProjectOwner    = "owner"
	ProjectEditor   = "editor"
	ProjectWriter   = "writer"
	ProjectArtist   = "artist"
	ProjectLetterer = "letterer"
	ProjectViewer   = "viewer"
)

var projectRank = map[string]int{ProjectViewer: 1, ProjectWriter: 2, ProjectArtist: 2, ProjectLetterer: 2, ProjectEditor: 3, ProjectOwner: 4}

// ProjectRoleAllows reports whether a member with role have may act where need is required.
// Owner ⊃ editor ⊃ {writer, artist, letterer} ⊃ viewer; the three craft roles do not include each other.
func ProjectRoleAllows(have, need string) bool {
	rh, ok := projectRank[have]
	if !ok {
		return false
	}
	rn, ok := projectRank[need]
	if !ok {
		return false
	}
	if have == need || rn == 1 {
		return true
	}
	return rh >= 3 && rh >= rn
}

// ProjectRole returns the user's role in a project ("owner" for the owner,
// the member role otherwise, "" when the user has no access).
func (s *Service) ProjectRole(ctx context.Context, projectID, userID string) (string, error) {
	var role string
	err := s.db.Get(ctx, &role, `SELECT CASE WHEN p.owner_id = ? THEN 'owner' ELSE COALESCE(m.role, '') END
		FROM projects p LEFT JOIN project_members m ON m.project_id = p.id AND m.user_id = ?
		WHERE p.id = ?`, userID, userID, projectID)
	if db.IsNotFound(err) {
		return "", nil
	}
	return role, err
}
