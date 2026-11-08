package publish

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// seriesCursor is the keyset position after the last series of a page
// (published_at DESC, id DESC). It is opaque to callers.
type seriesCursor struct {
	PublishedAt time.Time `json:"t"`
	ID          string    `json:"id"`
}

func encodeSeriesCursor(publishedAt time.Time, id string) string {
	b, _ := json.Marshal(seriesCursor{PublishedAt: publishedAt.UTC(), ID: id})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeSeriesCursor(s string) (seriesCursor, error) {
	var c seriesCursor
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) > 256 || json.Unmarshal(b, &c) != nil || c.PublishedAt.IsZero() || c.ID == "" || len(c.ID) > 64 {
		return seriesCursor{}, fmt.Errorf("%w: cursor", ErrInvalid)
	}
	c.PublishedAt = c.PublishedAt.UTC()
	return c, nil
}
