package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// TOTP parameters (RFC 6238 defaults understood by every authenticator app).
const (
	totpPeriod        = 30
	totpDigits        = otp.DigitsSix
	totpSkew          = 1
	recoveryCodeCount = 10
)

// TOTPEnrollment is returned when a user starts enabling two-factor auth.
type TOTPEnrollment struct {
	Secret     string `json:"secret"`
	OTPAuthURL string `json:"otpauth_url"`
}

func totpAAD(userID string) string { return "user:" + userID + ":totp" }

// BeginTOTP generates a new secret for userID (not yet active) and returns it
// with the otpauth:// URL for the QR code.
func (s *Service) BeginTOTP(ctx context.Context, userID string) (*TOTPEnrollment, error) {
	r, err := s.row(ctx, s.db, `id = ?`, userID)
	if err != nil {
		return nil, err
	}
	if r.TOTPEnabled {
		return nil, fmt.Errorf("auth: two-factor authentication is already enabled")
	}
	inst := s.settings.MustInstance(ctx)
	key, err := totp.Generate(totp.GenerateOpts{Issuer: inst.Name, AccountName: r.Username, Period: totpPeriod, Digits: totpDigits, SecretSize: 20})
	if err != nil {
		return nil, err
	}
	sealed := s.box.SealString(key.Secret(), totpAAD(userID))
	if _, err := s.db.Exec(ctx, `UPDATE users SET totp_secret_enc = ?, totp_enabled = FALSE, updated_at = ? WHERE id = ?`, sealed, s.now(), userID); err != nil {
		return nil, err
	}
	return &TOTPEnrollment{Secret: key.Secret(), OTPAuthURL: key.URL()}, nil
}

// ConfirmTOTP activates two-factor auth after the user proves the app works,
// and returns one-time recovery codes (shown once, stored hashed).
func (s *Service) ConfirmTOTP(ctx context.Context, userID, code string) ([]string, error) {
	r, err := s.row(ctx, s.db, `id = ?`, userID)
	if err != nil {
		return nil, err
	}
	if r.TOTPSecretEnc == "" || r.TOTPEnabled {
		return nil, ErrTOTPInvalid
	}
	secret, err := s.box.OpenString(r.TOTPSecretEnc, totpAAD(userID))
	if err != nil {
		return nil, err
	}
	step, ok := matchTOTP(secret, code, s.now(), 0)
	if !ok {
		return nil, ErrTOTPInvalid
	}
	codes, hashes := newRecoveryCodes()
	if _, err := s.db.Exec(ctx, `UPDATE users SET totp_enabled = TRUE, totp_recovery = ?, totp_last_step = ?, updated_at = ? WHERE id = ?`,
		db.MustJSON(hashes), step, s.now(), userID); err != nil {
		return nil, err
	}
	s.Audit(ctx, AuditEntry{ActorID: userID, Action: "auth.totp_enabled", TargetKind: "user", TargetID: userID})
	return codes, nil
}

// DisableTOTP turns two-factor auth off after re-checking the password.
func (s *Service) DisableTOTP(ctx context.Context, userID, password string) error {
	r, err := s.row(ctx, s.db, `id = ?`, userID)
	if err != nil {
		return err
	}
	if err := s.checkReauth(r, password); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `UPDATE users SET totp_enabled = FALSE, totp_secret_enc = '', totp_recovery = '[]', totp_last_step = 0, updated_at = ? WHERE id = ?`, s.now(), userID); err != nil {
		return err
	}
	s.Audit(ctx, AuditEntry{ActorID: userID, Action: "auth.totp_disabled", TargetKind: "user", TargetID: userID})
	return nil
}

// AdminDisableTOTP removes two-factor auth for a member who lost their device.
func (s *Service) AdminDisableTOTP(ctx context.Context, userID string) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET totp_enabled = FALSE, totp_secret_enc = '', totp_recovery = '[]', totp_last_step = 0, updated_at = ? WHERE id = ?`, s.now(), userID)
	if err == nil {
		s.Audit(ctx, AuditEntry{Action: "user.totp_reset", TargetKind: "user", TargetID: userID})
	}
	return err
}

// RegenerateRecoveryCodes replaces the recovery codes (requires the password).
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, userID, password string) ([]string, error) {
	r, err := s.row(ctx, s.db, `id = ?`, userID)
	if err != nil {
		return nil, err
	}
	if err := s.checkReauth(r, password); err != nil {
		return nil, err
	}
	if !r.TOTPEnabled {
		return nil, ErrTOTPInvalid
	}
	codes, hashes := newRecoveryCodes()
	_, err = s.db.Exec(ctx, `UPDATE users SET totp_recovery = ?, updated_at = ? WHERE id = ?`, db.MustJSON(hashes), s.now(), userID)
	return codes, err
}

// verifySecondFactor accepts a current TOTP code (each time step once) or an unused recovery code.
func (s *Service) verifySecondFactor(ctx context.Context, r *userRow, code string) error {
	code = strings.TrimSpace(code)
	digits := strings.ReplaceAll(code, " ", "")
	if len(digits) == int(totpDigits) && isDigits(digits) {
		secret, err := s.box.OpenString(r.TOTPSecretEnc, totpAAD(r.ID))
		if err != nil {
			return err
		}
		step, ok := matchTOTP(secret, digits, s.now(), r.TOTPLastStep)
		if !ok {
			return ErrTOTPInvalid
		}
		// Record the step atomically so the same code cannot be replayed.
		res, err := s.db.Exec(ctx, `UPDATE users SET totp_last_step = ? WHERE id = ? AND totp_last_step < ?`, step, r.ID, step)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrTOTPInvalid
		}
		return nil
	}
	h := hashToken(normalizeRecovery(code))
	remaining := make([]string, 0, len(r.TOTPRecovery.V))
	found := false
	for _, stored := range r.TOTPRecovery.V {
		if !found && subtle.ConstantTimeCompare([]byte(stored), []byte(h)) == 1 {
			found = true
			continue
		}
		remaining = append(remaining, stored)
	}
	if !found {
		return ErrTOTPInvalid
	}
	res, err := s.db.Exec(ctx, `UPDATE users SET totp_recovery = ? WHERE id = ? AND totp_recovery = ?`, db.MustJSON(remaining), r.ID, db.MustJSON(r.TOTPRecovery.V))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTOTPInvalid
	}
	s.Audit(ctx, AuditEntry{ActorID: r.ID, Action: "auth.recovery_code_used", TargetKind: "user", TargetID: r.ID, Data: map[string]any{"remaining": len(remaining)}})
	return nil
}

// matchTOTP checks code against the steps around now (±skew) that are newer
// than lastStep and returns the matching step.
func matchTOTP(secret, code string, now time.Time, lastStep int64) (int64, bool) {
	cur := now.Unix() / totpPeriod
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		step := cur + d
		if step <= lastStep {
			continue
		}
		want, err := totp.GenerateCodeCustom(secret, time.Unix(step*totpPeriod, 0).UTC(), totp.ValidateOpts{Period: totpPeriod, Digits: totpDigits, Algorithm: otp.AlgorithmSHA1})
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

// GenerateTOTPCode computes the current code for a secret (tests and the CLI).
func GenerateTOTPCode(secret string, at time.Time) (string, error) {
	return totp.GenerateCodeCustom(secret, at, totp.ValidateOpts{Period: totpPeriod, Digits: totpDigits, Algorithm: otp.AlgorithmSHA1})
}

func newRecoveryCodes() (codes, hashes []string) {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	for i := 0; i < recoveryCodeCount; i++ {
		b := make([]byte, 10)
		if _, err := rand.Read(b); err != nil {
			panic(err)
		}
		for j := range b {
			b[j] = alphabet[int(b[j])%len(alphabet)]
		}
		c := string(b[:5]) + "-" + string(b[5:])
		codes = append(codes, c)
		hashes = append(hashes, hashToken(c))
	}
	return codes, hashes
}

func normalizeRecovery(c string) string {
	c = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(c), " ", ""))
	if len(c) == 10 && !strings.Contains(c, "-") {
		c = c[:5] + "-" + c[5:]
	}
	return c
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}
