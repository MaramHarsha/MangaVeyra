package notify

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
)

// DigestRenderer turns a member's unread notifications into an email.
type DigestRenderer func(ctx context.Context, instanceName, link string, items []Notification) (subject, text string)

// SetDigestRenderer replaces the default plain-text digest (the web layer may render nicer email).
func (s *Service) SetDigestRenderer(fn DigestRenderer) {
	s.mu.Lock()
	s.digest = fn
	s.mu.Unlock()
}

func defaultDigest(ctx context.Context, instanceName, link string, items []Notification) (string, string) {
	var b strings.Builder
	b.WriteString(i18n.T(ctx, "email.digest.intro", "count", len(items)))
	b.WriteString("\n\n")
	for i, n := range items {
		if i == 30 {
			fmt.Fprintf(&b, "… (+%d)\n", len(items)-30)
			break
		}
		fmt.Fprintf(&b, "• %s", n.Title)
		if n.Body != "" {
			fmt.Fprintf(&b, " — %s", truncate(strings.ReplaceAll(n.Body, "\n", " "), 140))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n" + link + "\n")
	return i18n.T(ctx, "email.digest.subject", "instance", instanceName), b.String()
}

// RunDigests emails every member whose daily or weekly digest is due and
// who has unread notifications since their last digest. It returns how many
// digests were sent.
func (s *Service) RunDigests(ctx context.Context) (int, error) {
	if s.opts.Mailer == nil {
		return 0, nil
	}
	type due struct {
		UserID       string     `db:"user_id"`
		Prefs        string     `db:"prefs"`
		LastDigestAt *time.Time `db:"last_digest_at"`
	}
	var rows []due
	if err := s.db.Select(ctx, &rows, `SELECT user_id, prefs, last_digest_at FROM notification_prefs WHERE prefs LIKE '%"digest":"daily"%' OR prefs LIKE '%"digest":"weekly"%'`); err != nil {
		return 0, err
	}
	inst := s.settings.MustInstance(ctx)
	now := s.now()
	sent := 0
	for _, r := range rows {
		p, err := s.Prefs(ctx, r.UserID)
		if err != nil {
			continue
		}
		period := 24 * time.Hour
		if p.Digest == DigestWeekly {
			period = 7 * 24 * time.Hour
		}
		since := now.Add(-period)
		if r.LastDigestAt != nil {
			if now.Sub(*r.LastDigestAt) < period {
				continue
			}
			since = *r.LastDigestAt
		}
		var items []row
		if err := s.db.Select(ctx, &items, `SELECT `+cols+` FROM notifications WHERE user_id = ? AND read_at IS NULL AND created_at > ? ORDER BY created_at DESC LIMIT 100`,
			r.UserID, since); err != nil {
			return sent, err
		}
		if _, err := s.db.Exec(ctx, `UPDATE notification_prefs SET last_digest_at = ? WHERE user_id = ?`, now, r.UserID); err != nil {
			return sent, err
		}
		if len(items) == 0 {
			continue
		}
		rc, err := s.recipient(ctx, r.UserID)
		if err != nil || rc.Email == nil || *rc.Email == "" {
			continue
		}
		list := make([]Notification, len(items))
		for i, it := range items {
			list[i] = it.notification()
		}
		lctx := i18n.WithLocale(ctx, firstNonEmpty(rc.Locale, inst.DefaultLocale))
		s.mu.RLock()
		render := s.digest
		s.mu.RUnlock()
		subject, text := render(lctx, inst.Name, s.absolute(ctx, "/notifications"), list)
		if err := s.opts.Mailer.Send(ctx, Email{To: *rc.Email, Subject: subject, Text: text}); err != nil {
			s.log.WarnContext(ctx, "notify: digest", "user", r.UserID, "err", err)
			continue
		}
		sent++
	}
	return sent, nil
}

// Start runs the digest loop (hourly) until ctx ends. It implements core.Starter.
func (s *Service) Start(ctx context.Context) error {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.Wait()
			return nil
		case <-t.C:
			if _, err := s.RunDigests(ctx); err != nil {
				s.log.WarnContext(ctx, "notify: digests", "err", err)
			}
			if _, err := s.Prune(ctx, 0); err != nil {
				s.log.WarnContext(ctx, "notify: prune", "err", err)
			}
		}
	}
}
