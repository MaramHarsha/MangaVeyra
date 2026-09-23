package notify

import (
	"errors"
	"net/http"

	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
)

// Name implements core.Module.
func (s *Service) Name() string { return "notify" }

// Routes registers the notification and push JSON API (the G4 page itself is rendered by the web layer).
//
//	GET  /api/v1/notifications?unread=1&kind=&cursor=&limit=
//	GET  /api/v1/notifications/unread-count
//	POST /api/v1/notifications/read          {"ids": [...]} (empty = all)
//	GET  /api/v1/notifications/prefs         PUT /api/v1/notifications/prefs
//	POST /api/v1/notifications/test
//	GET  /api/v1/push/vapid-public-key
//	POST /api/v1/push/subscriptions          {"endpoint", "keys": {"p256dh", "auth"}}
//	DELETE /api/v1/push/subscriptions        {"endpoint"}
func (s *Service) Routes(r *httpx.Router) {
	g := r.Group("/api/v1", httpx.RequireUser)
	g.API("GET /notifications", s.hList)
	g.API("GET /notifications/unread-count", s.hUnread)
	g.API("POST /notifications/read", s.hRead)
	g.API("GET /notifications/prefs", s.hGetPrefs)
	g.API("PUT /notifications/prefs", s.hSetPrefs, httpx.RequireHuman)
	g.API("POST /notifications/test", s.hTest, httpx.RequireHuman)
	g.API("GET /push/vapid-public-key", s.hVAPID)
	g.API("POST /push/subscriptions", s.hSubscribe, httpx.RequireHuman)
	g.API("DELETE /push/subscriptions", s.hUnsubscribe, httpx.RequireHuman)
}

func (s *Service) hList(w http.ResponseWriter, r *http.Request) error {
	u := httpx.UserFrom(r.Context())
	p := httpx.ParsePage(r, 30, 100)
	q := r.URL.Query()
	list, err := s.List(r.Context(), u.ID, ListOptions{UnreadOnly: q.Get("unread") == "1", Kind: q.Get("kind"), Before: p.Cursor, Limit: p.Limit + 1})
	if err != nil {
		return err
	}
	unread, err := s.UnreadCount(r.Context(), u.ID)
	if err != nil {
		return err
	}
	page := httpx.Paginate(list, p.Limit, func(n Notification) string { return n.ID })
	httpx.JSON(w, http.StatusOK, map[string]any{"items": page.Items, "next_cursor": page.NextCursor, "unread": unread})
	return nil
}

func (s *Service) hUnread(w http.ResponseWriter, r *http.Request) error {
	n, err := s.UnreadCount(r.Context(), httpx.UserFrom(r.Context()).ID)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]int{"unread": n})
	return nil
}

func (s *Service) hRead(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		IDs []string `json:"ids"`
	}
	if r.ContentLength != 0 {
		if err := httpx.DecodeJSON(w, r, &in); err != nil {
			return err
		}
	}
	n, err := s.MarkRead(r.Context(), httpx.UserFrom(r.Context()).ID, in.IDs)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]int64{"marked": n})
	return nil
}

func (s *Service) hGetPrefs(w http.ResponseWriter, r *http.Request) error {
	p, err := s.Prefs(r.Context(), httpx.UserFrom(r.Context()).ID)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, p)
	return nil
}

func (s *Service) hSetPrefs(w http.ResponseWriter, r *http.Request) error {
	uid := httpx.UserFrom(r.Context()).ID
	p, err := s.Prefs(r.Context(), uid)
	if err != nil {
		return err
	}
	if err := httpx.DecodeJSON(w, r, &p); err != nil {
		return err
	}
	if err := s.SetPrefs(r.Context(), uid, p); err != nil {
		return httpx.ErrValidation(map[string]string{"prefs": err.Error()})
	}
	httpx.JSON(w, http.StatusOK, p)
	return nil
}

func (s *Service) hTest(w http.ResponseWriter, r *http.Request) error {
	n, err := s.Notify(r.Context(), Input{UserID: httpx.UserFrom(r.Context()).ID, Kind: KindSystem, Title: "Test notification", Body: "Notifications work. BA-DUM!"})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, n)
	return nil
}

func (s *Service) hVAPID(w http.ResponseWriter, r *http.Request) error {
	k, err := s.VAPIDPublicKey(r.Context())
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"public_key": k})
	return nil
}

func (s *Service) hSubscribe(w http.ResponseWriter, r *http.Request) error {
	var sub Subscription
	if err := httpx.DecodeJSON(w, r, &sub); err != nil {
		return err
	}
	if err := s.Subscribe(r.Context(), httpx.UserFrom(r.Context()).ID, sub, r.UserAgent()); err != nil {
		if errors.Is(err, ErrBadSubscription) {
			return httpx.ErrValidation(map[string]string{"subscription": err.Error()})
		}
		return err
	}
	httpx.JSON(w, http.StatusCreated, map[string]bool{"ok": true})
	return nil
}

func (s *Service) hUnsubscribe(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Endpoint string `json:"endpoint"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	if err := s.Unsubscribe(r.Context(), httpx.UserFrom(r.Context()).ID, in.Endpoint); err != nil {
		return err
	}
	httpx.OK(w)
	return nil
}
