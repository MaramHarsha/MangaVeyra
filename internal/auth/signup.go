package auth

import (
	"context"
	"crypto/rand"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

// Invite lets someone sign up (required when sign-up mode is "invite").
type Invite struct {
	ID        string     `db:"id" json:"id"`
	Code      string     `db:"code" json:"code"`
	Email     string     `db:"email" json:"email,omitempty"`
	Role      string     `db:"role" json:"role"`
	Note      string     `db:"note" json:"note,omitempty"`
	MaxUses   int        `db:"max_uses" json:"max_uses"`
	Uses      int        `db:"uses" json:"uses"`
	CreatedBy *string    `db:"created_by" json:"created_by,omitempty"`
	ExpiresAt *time.Time `db:"expires_at" json:"expires_at,omitempty"`
	RevokedAt *time.Time `db:"revoked_at" json:"revoked_at,omitempty"`
	CreatedAt time.Time  `db:"created_at" json:"created_at"`
	// URL is the sign-up link (filled by the service).
	URL string `db:"-" json:"url"`
}

// Valid reports whether the invite can still be used at now.
func (i *Invite) Valid(now time.Time) bool {
	return i.RevokedAt == nil && i.Uses < i.MaxUses && (i.ExpiresAt == nil || i.ExpiresAt.After(now))
}

// InviteInput describes a new invite.
type InviteInput struct {
	Email     string        // optional: only this address may use it (and it is emailed there)
	Role      string        // creator (default), reader or admin
	MaxUses   int           // default 1
	ExpiresIn time.Duration // default 7 days; negative = never
	Note      string
}

const inviteCols = `id, code, email, role, note, max_uses, uses, created_by, expires_at, revoked_at, created_at`

func newInviteCode() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b[:4]) + "-" + string(b[4:8]) + "-" + string(b[8:])
}

// CreateInvite creates an invite from the acting admin (by) and emails it when an address is given and SMTP works.
func (s *Service) CreateInvite(ctx context.Context, by string, in InviteInput) (*Invite, error) {
	in.Email = NormalizeEmail(in.Email)
	if err := ValidateEmail(in.Email); err != nil {
		return nil, err
	}
	if in.Role == "" {
		in.Role = RoleCreator
	}
	if !validRole(in.Role) {
		return nil, fieldError("role", "error.validation")
	}
	if in.MaxUses <= 0 {
		in.MaxUses = 1
	}
	if in.MaxUses > 1000 {
		return nil, fieldError("max_uses", "error.validation")
	}
	now := s.now()
	var exp *time.Time
	switch {
	case in.ExpiresIn == 0:
		t := now.Add(7 * 24 * time.Hour)
		exp = &t
	case in.ExpiresIn > 0:
		t := now.Add(in.ExpiresIn)
		exp = &t
	}
	inv := &Invite{ID: db.NewID(), Code: newInviteCode(), Email: in.Email, Role: in.Role, Note: in.Note, MaxUses: in.MaxUses, ExpiresAt: exp, CreatedAt: now}
	if by != "" {
		inv.CreatedBy = &by
	}
	_, err := s.db.Exec(ctx, `INSERT INTO invites (id, code, email, role, note, max_uses, created_by, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, inv.ID, inv.Code, inv.Email, inv.Role, inv.Note, inv.MaxUses, db.NullString(by), exp, now)
	if err != nil {
		return nil, err
	}
	inv.URL = s.inviteURL(ctx, inv.Code)
	s.Audit(ctx, AuditEntry{Action: "invite.create", TargetKind: "invite", TargetID: inv.ID, Data: map[string]any{"role": inv.Role, "max_uses": inv.MaxUses}})
	if m := s.mail(); m != nil && inv.Email != "" {
		inst := s.settings.MustInstance(ctx)
		inviter := "An admin"
		if by != "" {
			if u, err := s.UserByID(ctx, by); err == nil {
				inviter = u.Name()
			}
		}
		args := i18n.Args{"instance": inst.Name, "inviter": inviter, "link": inv.URL}
		subject := i18n.T(ctx, "email.invite.subject", args)
		body := i18n.T(ctx, "email.invite.body", args)
		if err := m.SendEmail(ctx, inv.Email, subject, body); err != nil {
			s.log.WarnContext(ctx, "auth: send invite email", "err", err)
		}
	}
	return inv, nil
}

func (s *Service) inviteURL(ctx context.Context, code string) string {
	return s.PublicURL(ctx) + "/signup?invite=" + code
}

// LookupInvite returns an invite by code (whether or not it is still valid).
func (s *Service) LookupInvite(ctx context.Context, code string) (*Invite, error) {
	var inv Invite
	if err := s.db.Get(ctx, &inv, `SELECT `+inviteCols+` FROM invites WHERE code = ?`, strings.ToLower(strings.TrimSpace(code))); err != nil {
		if db.IsNotFound(err) {
			return nil, ErrInviteInvalid
		}
		return nil, err
	}
	inv.URL = s.inviteURL(ctx, inv.Code)
	return &inv, nil
}

// ListInvites lists invites, newest first.
func (s *Service) ListInvites(ctx context.Context, includeInactive bool) ([]Invite, error) {
	var out []Invite
	q := `SELECT ` + inviteCols + ` FROM invites`
	args := []any{}
	if !includeInactive {
		q += ` WHERE revoked_at IS NULL AND uses < max_uses AND (expires_at IS NULL OR expires_at > ?)`
		args = append(args, s.now())
	}
	if err := s.db.Select(ctx, &out, q+` ORDER BY created_at DESC`, args...); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].URL = s.inviteURL(ctx, out[i].Code)
	}
	return out, nil
}

// RevokeInvite disables an invite.
func (s *Service) RevokeInvite(ctx context.Context, id string) error {
	res, err := s.db.Exec(ctx, `UPDATE invites SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, s.now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrInviteInvalid
	}
	s.Audit(ctx, AuditEntry{Action: "invite.revoke", TargetKind: "invite", TargetID: id})
	return nil
}

// SignupInput is a self-service sign-up.
type SignupInput struct {
	Username    string
	Email       string
	Password    string
	DisplayName string
	InviteCode  string
	Locale      string
}

// Signup creates an account according to the instance sign-up mode:
// closed rejects everyone, invite requires a valid invite, open accepts
// anyone (an invite still sets the role). The first admin is never created
// here: until one exists sign-up returns ErrSetupRequired (use the setup wizard or CLI).
func (s *Service) Signup(ctx context.Context, in SignupInput) (*User, error) {
	ok, err := s.HasAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrSetupRequired
	}
	inst, err := s.settings.Instance(ctx)
	if err != nil {
		return nil, err
	}
	code := strings.ToLower(strings.TrimSpace(in.InviteCode))
	switch inst.SignUp {
	case settings.SignUpClosed:
		return nil, ErrSignupClosed
	case settings.SignUpInvite:
		if code == "" {
			return nil, ErrInviteRequired
		}
	}
	nu := NewUser{Username: in.Username, Email: in.Email, Password: in.Password, DisplayName: in.DisplayName, Locale: in.Locale,
		Role: inst.DefaultRole, Status: StatusActive}
	if nu.Role == "" || nu.Role == RoleAdmin {
		nu.Role = RoleCreator
	}
	if code == "" && inst.RequireApproval {
		nu.Status = StatusPending
	}
	var u *User
	err = s.db.WithTx(ctx, func(tx *db.Tx) error {
		if code != "" {
			var inv Invite
			if err := tx.Get(ctx, &inv, `SELECT `+inviteCols+` FROM invites WHERE code = ?`, code); err != nil {
				if db.IsNotFound(err) {
					return ErrInviteInvalid
				}
				return err
			}
			if !inv.Valid(s.now()) {
				return ErrInviteInvalid
			}
			if inv.Email != "" && inv.Email != NormalizeEmail(in.Email) {
				return ErrInviteInvalid
			}
			res, err := tx.Exec(ctx, `UPDATE invites SET uses = uses + 1 WHERE id = ? AND uses < max_uses AND revoked_at IS NULL`, inv.ID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return ErrInviteInvalid
			}
			nu.Role, nu.InviteID, nu.Status = inv.Role, inv.ID, StatusActive
		}
		var err error
		u, err = s.createUserTx(ctx, tx, nu)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.Audit(ctx, AuditEntry{ActorID: u.ID, Action: "auth.signup", TargetKind: "user", TargetID: u.ID, Data: map[string]any{"role": u.Role, "invite": nu.InviteID != ""}})
	return u, nil
}

// ---- password reset -------------------------------------------------------

// ResetTTL is how long an emailed reset link is valid.
const ResetTTL = time.Hour

// RequestPasswordReset emails a reset link to the account with that email.
// It returns nil whether or not the account exists (no account enumeration),
// ErrNoEmail when the server cannot send email, and a rate-limit error on abuse.
func (s *Service) RequestPasswordReset(ctx context.Context, email, ip string) error {
	m := s.mail()
	if m == nil {
		return ErrNoEmail
	}
	email = NormalizeEmail(email)
	if ok, _ := s.resetRL.Allow("reset:" + email); !ok {
		return nil // silently drop: the first mails are already on their way
	}
	if ok, wait := s.resetRL.Allow("reset-ip:" + ip); !ok {
		_ = wait
		return nil
	}
	r, err := s.row(ctx, s.db, `email = ?`, email)
	if err != nil {
		if err == ErrUserNotFound {
			return nil
		}
		return err
	}
	if r.Status != StatusActive {
		return nil
	}
	token, err := s.createResetToken(ctx, r.ID, "", ip, ResetTTL)
	if err != nil {
		return err
	}
	inst := s.settings.MustInstance(ctx)
	link := s.PublicURL(ctx) + "/reset/" + token
	args := i18n.Args{"instance": inst.Name, "username": r.Username, "link": link}
	lctx := i18n.WithLocale(ctx, firstNonEmpty(r.Locale, inst.DefaultLocale))
	if err := m.SendEmail(ctx, email, i18n.T(lctx, "email.reset.subject", args), i18n.T(lctx, "email.reset.body", args)); err != nil {
		return err
	}
	s.Audit(ctx, AuditEntry{ActorID: r.ID, Action: "auth.reset_requested", TargetKind: "user", TargetID: r.ID})
	return nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func (s *Service) createResetToken(ctx context.Context, userID, by, ip string, ttl time.Duration) (string, error) {
	token := randomToken(32)
	now := s.now()
	_, err := s.db.Exec(ctx, `INSERT INTO password_resets (id, user_id, token_hash, created_by, requested_ip, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, db.NewID(), userID, hashToken(token), db.NullString(by), ip, now, now.Add(ttl))
	return token, err
}

// CreateResetLink makes a one-time reset link an admin can hand to a member
// when the server has no email (valid 24 hours).
func (s *Service) CreateResetLink(ctx context.Context, adminID, userID string) (string, error) {
	if _, err := s.UserByID(ctx, userID); err != nil {
		return "", err
	}
	token, err := s.createResetToken(ctx, userID, adminID, "", 24*time.Hour)
	if err != nil {
		return "", err
	}
	s.Audit(ctx, AuditEntry{Action: "user.reset_link", TargetKind: "user", TargetID: userID})
	return s.PublicURL(ctx) + "/reset/" + token, nil
}

// CheckResetToken reports whether a reset token is usable and returns its user.
func (s *Service) CheckResetToken(ctx context.Context, token string) (*User, error) {
	var userID string
	err := s.db.Get(ctx, &userID, `SELECT user_id FROM password_resets WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`, hashToken(token), s.now())
	if err != nil {
		if db.IsNotFound(err) {
			return nil, ErrResetInvalid
		}
		return nil, err
	}
	return s.UserByID(ctx, userID)
}

// ResetPassword sets a new password with a reset token and signs the user out everywhere.
func (s *Service) ResetPassword(ctx context.Context, token, password string) (*User, error) {
	if err := CheckPasswordPolicy(password); err != nil {
		return nil, err
	}
	hash, err := HashPassword(password, s.params)
	if err != nil {
		return nil, err
	}
	var userID string
	err = s.db.WithTx(ctx, func(tx *db.Tx) error {
		var row struct {
			ID     string `db:"id"`
			UserID string `db:"user_id"`
		}
		err := tx.Get(ctx, &row, `SELECT id, user_id FROM password_resets WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`, hashToken(token), s.now())
		if err != nil {
			if db.IsNotFound(err) {
				return ErrResetInvalid
			}
			return err
		}
		userID = row.UserID
		now := s.now()
		if _, err := tx.Exec(ctx, `UPDATE password_resets SET used_at = ? WHERE user_id = ? AND used_at IS NULL`, now, row.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = ?, password_changed_at = ?, updated_at = ? WHERE id = ?`, hash, now, now, row.UserID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = ?`, row.UserID)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.Audit(ctx, AuditEntry{ActorID: userID, Action: "auth.reset_password", TargetKind: "user", TargetID: userID})
	return s.UserByID(ctx, userID)
}
