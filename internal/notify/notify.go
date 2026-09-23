// Package notify delivers notifications: the in-app list (pushed live over
// the WebSocket topic user:{id}), email through the configured SMTP relay,
// and browser web push (VAPID, RFC 8291 encryption). Each member controls
// channels per notification kind, quiet hours and an optional email digest.
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/secrets"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

// Notification kinds used by the core. Modules may add their own kinds.
const (
	KindJobDone   = "job_done"
	KindJobFailed = "job_failed"
	KindComment   = "comment"
	KindMention   = "mention"
	KindReview    = "review"
	KindFollow    = "follow"
	KindChapter   = "chapter"
	KindSystem    = "system"
	KindUpdate    = "update"
)

// Publisher pushes realtime messages (the ws hub implements it).
type Publisher interface {
	Publish(topic, typ string, data any)
}

// Notification is one in-app notification.
type Notification struct {
	ID        string         `json:"id"`
	UserID    string         `json:"user_id"`
	Kind      string         `json:"kind"`
	Title     string         `json:"title"`
	Body      string         `json:"body,omitempty"`
	Link      string         `json:"link,omitempty"`
	ActorID   string         `json:"actor_id,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
	GroupKey  string         `json:"group_key,omitempty"`
	ReadAt    *time.Time     `json:"read_at,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

type row struct {
	ID        string                  `db:"id"`
	UserID    string                  `db:"user_id"`
	Kind      string                  `db:"kind"`
	Title     string                  `db:"title"`
	Body      string                  `db:"body"`
	Link      string                  `db:"link"`
	ActorID   *string                 `db:"actor_id"`
	Data      db.JSON[map[string]any] `db:"data"`
	GroupKey  string                  `db:"group_key"`
	ReadAt    *time.Time              `db:"read_at"`
	CreatedAt time.Time               `db:"created_at"`
}

func (r row) notification() Notification {
	n := Notification{ID: r.ID, UserID: r.UserID, Kind: r.Kind, Title: r.Title, Body: r.Body, Link: r.Link, Data: r.Data.V,
		GroupKey: r.GroupKey, ReadAt: r.ReadAt, CreatedAt: r.CreatedAt.UTC()}
	if r.ActorID != nil {
		n.ActorID = *r.ActorID
	}
	return n
}

const cols = `id, user_id, kind, title, body, link, actor_id, data, group_key, read_at, created_at`

// Options configure the service.
type Options struct {
	PublicURL string
	Mailer    *SMTPMailer // nil disables email
	// AllowPrivatePushEndpoints permits push endpoints on private networks (tests, air-gapped push relays).
	AllowPrivatePushEndpoints bool
	Logger                    *slog.Logger
}

// Service creates and delivers notifications.
type Service struct {
	db       *db.DB
	settings *settings.Service
	box      *secrets.Box
	opts     Options
	log      *slog.Logger

	mu     sync.RWMutex
	pub    Publisher
	digest DigestRenderer
	push   *pushSender

	wg sync.WaitGroup
	// Now is the clock (tests replace it).
	Now func() time.Time
}

// New creates the service.
func New(d *db.DB, st *settings.Service, box *secrets.Box, pub Publisher, opts Options) *Service {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	s := &Service{db: d, settings: st, box: box, opts: opts, log: opts.Logger, pub: pub, Now: db.Now}
	s.digest = defaultDigest
	s.push = newPushSender(s)
	return s
}

// SetPublisher sets the realtime publisher.
func (s *Service) SetPublisher(p Publisher) {
	s.mu.Lock()
	s.pub = p
	s.mu.Unlock()
}

// EmailEnabled implements auth.Mailer.
func (s *Service) EmailEnabled() bool { return s.opts.Mailer != nil }

// SendEmail implements auth.Mailer: a plain-text email.
func (s *Service) SendEmail(ctx context.Context, to, subject, text string) error {
	if s.opts.Mailer == nil {
		return ErrEmailDisabled
	}
	return s.opts.Mailer.Send(ctx, Email{To: to, Subject: subject, Text: text})
}

// Mailer returns the SMTP mailer (nil when email is not configured).
func (s *Service) Mailer() *SMTPMailer { return s.opts.Mailer }

// Wait blocks until background deliveries finish (graceful shutdown, tests).
func (s *Service) Wait() { s.wg.Wait() }

// Input describes a notification to create.
type Input struct {
	UserID  string
	Kind    string
	Title   string
	Body    string
	Link    string
	ActorID string
	Data    map[string]any
	// GroupKey merges into an unread notification with the same key from the last hour
	// (e.g. "comments:chapter:01j...") instead of adding a new row.
	GroupKey string
}

// Notify stores a notification, pushes it to the member's open tabs and
// delivers email / web push according to their preferences.
func (s *Service) Notify(ctx context.Context, in Input) (*Notification, error) {
	if in.UserID == "" || in.Kind == "" || strings.TrimSpace(in.Title) == "" {
		return nil, fmt.Errorf("notify: user, kind and title are required")
	}
	if in.Data == nil {
		in.Data = map[string]any{}
	}
	in.Title, in.Body = truncate(in.Title, 200), truncate(in.Body, 2000)
	now := s.now()
	var r row
	err := s.db.WithTx(ctx, func(tx *db.Tx) error {
		if in.GroupKey != "" {
			var id string
			err := tx.Get(ctx, &id, `SELECT id FROM notifications WHERE user_id = ? AND group_key = ? AND read_at IS NULL AND created_at > ?
				ORDER BY created_at DESC LIMIT 1`, in.UserID, in.GroupKey, now.Add(-time.Hour))
			if err == nil {
				if _, err := tx.Exec(ctx, `UPDATE notifications SET title = ?, body = ?, link = ?, actor_id = ?, data = ?, created_at = ? WHERE id = ?`,
					in.Title, in.Body, in.Link, db.NullString(in.ActorID), db.MustJSON(in.Data), now, id); err != nil {
					return err
				}
				return tx.Get(ctx, &r, `SELECT `+cols+` FROM notifications WHERE id = ?`, id)
			}
			if !db.IsNotFound(err) {
				return err
			}
		}
		id := db.NewID()
		if _, err := tx.Exec(ctx, `INSERT INTO notifications (id, user_id, kind, title, body, link, actor_id, data, group_key, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, in.UserID, in.Kind, in.Title, in.Body, in.Link, db.NullString(in.ActorID),
			db.MustJSON(in.Data), in.GroupKey, now); err != nil {
			return err
		}
		return tx.Get(ctx, &r, `SELECT `+cols+` FROM notifications WHERE id = ?`, id)
	})
	if err != nil {
		return nil, fmt.Errorf("notify: create: %w", err)
	}
	n := r.notification()
	s.mu.RLock()
	pub := s.pub
	s.mu.RUnlock()
	if pub != nil {
		unread, _ := s.UnreadCount(ctx, n.UserID)
		pub.Publish("user:"+n.UserID, "notification.new", map[string]any{"notification": n, "unread": unread})
	}
	s.deliver(ctx, n)
	return &n, nil
}

// deliver sends email / push in the background according to preferences.
func (s *Service) deliver(ctx context.Context, n Notification) {
	prefs, err := s.Prefs(ctx, n.UserID)
	if err != nil {
		s.log.WarnContext(ctx, "notify: load prefs", "err", err)
		return
	}
	ch := prefs.Channel(n.Kind)
	if (!ch.Email && !ch.Push) || prefs.inQuietHours(s.now()) {
		return
	}
	bg := context.WithoutCancel(ctx)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(bg, time.Minute)
		defer cancel()
		if ch.Email && s.opts.Mailer != nil && prefs.Digest == DigestOff {
			if err := s.emailNotification(ctx, n); err != nil {
				s.log.Warn("notify: email", "notification", n.ID, "err", err)
			}
		}
		if ch.Push {
			if err := s.push.sendToUser(ctx, n); err != nil {
				s.log.Warn("notify: web push", "notification", n.ID, "err", err)
			}
		}
	}()
}

type recipient struct {
	Email    *string `db:"email"`
	Username string  `db:"username"`
	Locale   string  `db:"locale"`
}

func (s *Service) recipient(ctx context.Context, userID string) (recipient, error) {
	var r recipient
	err := s.db.Get(ctx, &r, `SELECT email, username, locale FROM users WHERE id = ? AND status = 'active'`, userID)
	return r, err
}

func (s *Service) emailNotification(ctx context.Context, n Notification) error {
	rc, err := s.recipient(ctx, n.UserID)
	if err != nil || rc.Email == nil || *rc.Email == "" {
		return err
	}
	inst := s.settings.MustInstance(ctx)
	lctx := i18n.WithLocale(ctx, firstNonEmpty(rc.Locale, inst.DefaultLocale))
	text := n.Title
	if n.Body != "" {
		text += "\n\n" + n.Body
	}
	if n.Link != "" {
		text += "\n\n" + s.absolute(ctx, n.Link)
	}
	text += "\n\n-- \n" + i18n.T(lctx, "email.footer", "instance", inst.Name, "link", s.absolute(ctx, "/settings/notifications"))
	if err := s.opts.Mailer.Send(ctx, Email{To: *rc.Email, Subject: "[" + inst.Name + "] " + n.Title, Text: text}); err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `UPDATE notifications SET emailed_at = ? WHERE id = ?`, s.now(), n.ID)
	return err
}

func (s *Service) absolute(ctx context.Context, link string) string {
	if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
		return link
	}
	base := s.opts.PublicURL
	if inst, err := s.settings.Instance(ctx); err == nil && inst.PublicURL != "" {
		base = inst.PublicURL
	}
	return strings.TrimRight(base, "/") + link
}

// ListOptions filter List.
type ListOptions struct {
	UnreadOnly bool
	Kind       string
	Before     string // id cursor
	Limit      int
}

// List returns a member's notifications, newest first.
func (s *Service) List(ctx context.Context, userID string, o ListOptions) ([]Notification, error) {
	q, args := `SELECT `+cols+` FROM notifications WHERE user_id = ?`, []any{userID}
	if o.UnreadOnly {
		q += ` AND read_at IS NULL`
	}
	if o.Kind != "" {
		q, args = q+` AND kind = ?`, append(args, o.Kind)
	}
	if o.Before != "" {
		q, args = q+` AND id < ?`, append(args, o.Before)
	}
	if o.Limit <= 0 || o.Limit > 200 {
		o.Limit = 50
	}
	var rows []row
	if err := s.db.Select(ctx, &rows, q+` ORDER BY id DESC LIMIT ?`, append(args, o.Limit)...); err != nil {
		return nil, err
	}
	out := make([]Notification, len(rows))
	for i, r := range rows {
		out[i] = r.notification()
	}
	return out, nil
}

// UnreadCount returns the number of unread notifications.
func (s *Service) UnreadCount(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.db.Get(ctx, &n, `SELECT COUNT(*) FROM notifications WHERE user_id = ? AND read_at IS NULL`, userID)
	return n, err
}

// MarkRead marks the given notifications (or all when ids is empty) as read.
func (s *Service) MarkRead(ctx context.Context, userID string, ids []string) (int64, error) {
	now := s.now()
	q, args := `UPDATE notifications SET read_at = ? WHERE user_id = ? AND read_at IS NULL`, []any{now, userID}
	if len(ids) > 0 {
		q += ` AND id IN (?)`
		args = append(args, ids)
		var err error
		if q, args, err = db.In(q, args...); err != nil {
			return 0, err
		}
	}
	res, err := s.db.Exec(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	s.mu.RLock()
	pub := s.pub
	s.mu.RUnlock()
	if pub != nil && n > 0 {
		unread, _ := s.UnreadCount(ctx, userID)
		pub.Publish("user:"+userID, "notification.read", map[string]any{"ids": ids, "unread": unread})
	}
	return n, nil
}

// Prune removes read notifications older than keep (default 180 days).
func (s *Service) Prune(ctx context.Context, keep time.Duration) (int64, error) {
	if keep <= 0 {
		keep = 180 * 24 * time.Hour
	}
	res, err := s.db.Exec(ctx, `DELETE FROM notifications WHERE created_at < ? AND read_at IS NOT NULL`, s.now().Add(-keep))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- preferences ---------------------------------------------------------------

// Channels are the delivery channels for one notification kind.
type Channels struct {
	InApp bool `json:"in_app"`
	Email bool `json:"email"`
	Push  bool `json:"push"`
}

// QuietHours suppress email and push (in-app still arrives) between Start and End, "HH:MM" in Timezone.
type QuietHours struct {
	Enabled  bool   `json:"enabled"`
	Start    string `json:"start"`
	End      string `json:"end"`
	Timezone string `json:"timezone"`
}

// Digest frequencies.
const (
	DigestOff    = "off"
	DigestDaily  = "daily"
	DigestWeekly = "weekly"
)

// Prefs are a member's notification preferences.
type Prefs struct {
	Channels   map[string]Channels `json:"channels"`
	QuietHours QuietHours          `json:"quiet_hours"`
	// Digest batches email into one daily or weekly message ("off" = email as it happens).
	Digest string `json:"digest"`
}

// DefaultPrefs returns the defaults: everything in-app, email for mentions,
// reviews and updates, push for finished jobs, mentions and comments.
func DefaultPrefs() Prefs {
	return Prefs{
		Channels: map[string]Channels{
			KindJobDone:   {InApp: true, Push: true},
			KindJobFailed: {InApp: true, Push: true},
			KindComment:   {InApp: true, Push: true},
			KindMention:   {InApp: true, Email: true, Push: true},
			KindReview:    {InApp: true, Email: true},
			KindFollow:    {InApp: true},
			KindChapter:   {InApp: true, Push: true},
			KindSystem:    {InApp: true, Email: true},
			KindUpdate:    {InApp: true, Email: true},
		},
		QuietHours: QuietHours{Start: "22:00", End: "08:00"},
		Digest:     DigestOff,
	}
}

// Channel returns the channels for kind (unknown kinds: in-app only).
func (p Prefs) Channel(kind string) Channels {
	if c, ok := p.Channels[kind]; ok {
		return c
	}
	return Channels{InApp: true}
}

func parseHM(s string) (int, bool) {
	h, m, ok := strings.Cut(s, ":")
	if !ok {
		return 0, false
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, false
	}
	return hh*60 + mm, true
}

func (p Prefs) inQuietHours(now time.Time) bool {
	q := p.QuietHours
	if !q.Enabled {
		return false
	}
	start, ok1 := parseHM(q.Start)
	end, ok2 := parseHM(q.End)
	if !ok1 || !ok2 || start == end {
		return false
	}
	loc := time.UTC
	if q.Timezone != "" {
		if l, err := time.LoadLocation(q.Timezone); err == nil {
			loc = l
		}
	}
	t := now.In(loc)
	cur := t.Hour()*60 + t.Minute()
	if start < end {
		return cur >= start && cur < end
	}
	return cur >= start || cur < end
}

// Validate checks the preferences.
func (p Prefs) Validate() error {
	if p.Digest != DigestOff && p.Digest != DigestDaily && p.Digest != DigestWeekly {
		return fmt.Errorf("digest must be off, daily or weekly")
	}
	if _, ok := parseHM(p.QuietHours.Start); !ok {
		return fmt.Errorf("quiet_hours.start must be HH:MM")
	}
	if _, ok := parseHM(p.QuietHours.End); !ok {
		return fmt.Errorf("quiet_hours.end must be HH:MM")
	}
	if p.QuietHours.Timezone != "" {
		if _, err := time.LoadLocation(p.QuietHours.Timezone); err != nil {
			return fmt.Errorf("quiet_hours.timezone is not a known time zone")
		}
	}
	return nil
}

// Prefs returns a member's preferences merged over the defaults.
func (s *Service) Prefs(ctx context.Context, userID string) (Prefs, error) {
	p := DefaultPrefs()
	var raw string
	err := s.db.Get(ctx, &raw, `SELECT prefs FROM notification_prefs WHERE user_id = ?`, userID)
	if db.IsNotFound(err) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	var stored Prefs
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return p, nil
	}
	for k, v := range stored.Channels {
		p.Channels[k] = v
	}
	if stored.QuietHours.Start != "" {
		p.QuietHours = stored.QuietHours
	}
	if stored.Digest != "" {
		p.Digest = stored.Digest
	}
	return p, nil
}

// SetPrefs validates and stores preferences.
func (s *Service) SetPrefs(ctx context.Context, userID string, p Prefs) error {
	if err := p.Validate(); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `INSERT INTO notification_prefs (user_id, prefs, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET prefs = excluded.prefs, updated_at = excluded.updated_at`, userID, db.MustJSON(p), s.now())
	return err
}

func (s *Service) now() time.Time { return s.Now().UTC().Truncate(time.Microsecond) }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
