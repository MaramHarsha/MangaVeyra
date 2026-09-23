package auth

import (
	"context"
	"errors"
	"net/http"

	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

// Sentinel errors. They are *httpx.APIError values so handlers can return them
// directly; compare with errors.Is.
var (
	ErrInvalidCredentials = httpx.KeyedError(http.StatusUnauthorized, "invalid_credentials", "auth.login.invalid")
	ErrTOTPRequired       = httpx.KeyedError(http.StatusUnauthorized, "totp_required", "auth.login.totp_required")
	ErrTOTPInvalid        = httpx.KeyedError(http.StatusUnauthorized, "totp_invalid", "auth.login.totp_invalid")
	ErrSuspended          = httpx.KeyedError(http.StatusForbidden, "suspended", "auth.login.suspended")
	ErrPending            = httpx.KeyedError(http.StatusForbidden, "pending", "auth.login.pending")
	ErrSignupClosed       = httpx.KeyedError(http.StatusForbidden, "signup_closed", "auth.signup.closed")
	ErrInviteRequired     = httpx.KeyedError(http.StatusForbidden, "invite_required", "auth.signup.invite_required")
	ErrInviteInvalid      = httpx.KeyedError(http.StatusForbidden, "invite_invalid", "auth.signup.invite_invalid")
	ErrSetupRequired      = httpx.KeyedError(http.StatusServiceUnavailable, httpx.CodeSetupRequired, "error.setup_required")
	ErrResetInvalid       = httpx.KeyedError(http.StatusBadRequest, "reset_invalid", "auth.reset.invalid")
	ErrNoEmail            = httpx.KeyedError(http.StatusConflict, "email_not_configured", "auth.reset.no_email")
	ErrWrongPassword      = httpx.KeyedError(http.StatusForbidden, "wrong_password", "auth.password.wrong")
	ErrUserNotFound       = httpx.KeyedError(http.StatusNotFound, httpx.CodeNotFound, "error.not_found")
	ErrTokenInvalid       = httpx.NewError(http.StatusUnauthorized, httpx.CodeUnauthorized, "invalid or revoked token")
	ErrLastAdmin          = httpx.NewError(http.StatusConflict, "last_admin", "the last admin cannot be demoted, suspended or deleted")
)

// FieldError is a validation error for one input field, carrying an i18n key.
type FieldError struct {
	Field string
	Key   string
	Args  []any
}

// Error renders the English message.
func (e *FieldError) Error() string {
	return e.Field + ": " + i18n.Default().Translate("en", e.Key, e.Args...)
}

func fieldError(field, key string, args ...any) error {
	return &FieldError{Field: field, Key: key, Args: args}
}

// APIError converts service errors into API errors in the context's language:
// field errors become a 422 with per-field messages.
func APIError(ctx context.Context, err error) error {
	var fe *FieldError
	if errors.As(err, &fe) {
		return httpx.ErrValidation(map[string]string{fe.Field: i18n.T(ctx, fe.Key, fe.Args...)})
	}
	return err
}
