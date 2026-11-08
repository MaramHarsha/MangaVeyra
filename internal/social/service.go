package social

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/publish"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

// Errors returned by the social service. ErrNotFound hides anything the
// viewer may not see, like publish.ErrNotFound.
var (
	ErrNotFound  = errors.New("social: not found")
	ErrInvalid   = errors.New("social: invalid")
	ErrForbidden = errors.New("social: forbidden")
)

// Paging bounds.
const (
	DefaultFollowLimit  = 100
	MaxFollowLimit      = 500
	DefaultCommentLimit = 20
	MaxCommentLimit     = 50
)

// service implements Follows plus the comment, report and follow writes the
// HTTP handlers use. pub is resolved in Init.
type service struct {
	db   *db.DB
	auth *auth.Service
	pub  publish.Service
	now  func() time.Time
	// settings holds the content rules (blocked words, ADR 0013 §2).
	settings *settings.Service
}

var _ Follows = (*service)(nil)

// invalid wraps ErrInvalid with the offending field so the API can name it.
type invalidError struct{ field, key string }

func (e *invalidError) Error() string { return "social: invalid " + e.field }
func (e *invalidError) Unwrap() error { return ErrInvalid }

func invalid(field, key string) error { return &invalidError{field: field, key: key} }

func clampLimit(n, def, max int) int {
	if n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// keyset is an opaque (time, id) cursor.
type keyset struct {
	T  time.Time `json:"t,omitzero"`
	ID string    `json:"id"`
}

func encodeCursor(t time.Time, id string) string {
	k := keyset{ID: id}
	if !t.IsZero() {
		k.T = t.UTC()
	}
	b, _ := json.Marshal(k)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string, needTime bool) (keyset, error) {
	var k keyset
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) > 256 || json.Unmarshal(b, &k) != nil || k.ID == "" || len(k.ID) > 64 || (needTime && k.T.IsZero()) {
		return keyset{}, fmt.Errorf("%w: cursor", ErrInvalid)
	}
	k.T = k.T.UTC()
	return k, nil
}

func validID(id string) bool {
	id = strings.TrimSpace(id)
	return id != "" && len(id) <= 64
}

// viewerOf maps a signed-in user to a publish viewer (anonymous when nil).
func viewerOf(u *auth.User) publish.Viewer {
	if u == nil {
		return publish.Viewer{}
	}
	return publish.Viewer{UserID: u.ID, Admin: u.IsAdmin()}
}

// accessErr turns publish's not-found into ours and keeps other errors.
func accessErr(err error) error {
	if errors.Is(err, publish.ErrNotFound) || errors.Is(err, publish.ErrInvalid) {
		return ErrNotFound
	}
	return err
}
