package authui

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/secrets"
)

// The two-step login keeps the verified-but-incomplete login in a short-lived
// cookie sealed with AES-GCM (secrets.Box), so the password never appears in
// the page and the code step works without server-side state.
const (
	pendingCookie = "mv_login_2fa"
	pendingAAD    = "authui:login-2fa"
	pendingTTL    = 5 * time.Minute
)

type pendingLogin struct {
	Identifier string `json:"i"`
	Password   string `json:"p"`
	Remember   bool   `json:"r"`
	Next       string `json:"n,omitempty"`
	Expires    int64  `json:"e"`
}

func setPending(w http.ResponseWriter, r *http.Request, box *secrets.Box, p pendingLogin) {
	b, _ := json.Marshal(p)
	http.SetCookie(w, &http.Cookie{Name: pendingCookie, Value: box.Seal(b, []byte(pendingAAD)), Path: "/login",
		MaxAge: int(pendingTTL.Seconds()), HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: httpx.IsHTTPS(r)})
}

func readPending(r *http.Request, box *secrets.Box) (pendingLogin, bool) {
	var p pendingLogin
	c, err := r.Cookie(pendingCookie)
	if err != nil || c.Value == "" {
		return p, false
	}
	b, err := box.Open(c.Value, []byte(pendingAAD))
	if err != nil || json.Unmarshal(b, &p) != nil || time.Now().Unix() > p.Expires {
		return pendingLogin{}, false
	}
	return p, true
}

func clearPending(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: pendingCookie, Value: "", Path: "/login", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: httpx.IsHTTPS(r)})
}
