package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// ErrEmailDisabled is returned when no SMTP relay is configured.
var ErrEmailDisabled = errors.New("notify: email is not configured")

// Email is an outgoing message. HTML is optional; Text is always sent.
type Email struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// SMTPConfig configures the relay.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string // "Name <addr@example.org>" or "addr@example.org"
	// TLS is "starttls" (required upgrade), "tls" (implicit TLS) or "none" (local relays only).
	TLS string
	// InsecureSkipVerify disables certificate checks (tests only).
	InsecureSkipVerify bool
	Timeout            time.Duration
}

// SMTPMailer sends email with net/smtp.
type SMTPMailer struct {
	cfg  SMTPConfig
	from *mail.Address
}

// NewSMTPMailer validates the configuration.
func NewSMTPMailer(cfg SMTPConfig) (*SMTPMailer, error) {
	if cfg.Host == "" {
		return nil, ErrEmailDisabled
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	if cfg.TLS == "" {
		cfg.TLS = "starttls"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	from := cfg.From
	if from == "" {
		from = "MangaVeyra <noreply@" + cfg.Host + ">"
	}
	addr, err := mail.ParseAddress(from)
	if err != nil {
		return nil, fmt.Errorf("notify: MV_SMTP_FROM: %w", err)
	}
	return &SMTPMailer{cfg: cfg, from: addr}, nil
}

// Send delivers one message.
func (m *SMTPMailer) Send(ctx context.Context, e Email) error {
	to, err := mail.ParseAddress(e.To)
	if err != nil {
		return fmt.Errorf("notify: bad recipient: %w", err)
	}
	if strings.ContainsAny(e.Subject, "\r\n") {
		return errors.New("notify: subject contains a line break")
	}
	msg, err := buildMessage(m.from, to, e)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
	defer cancel()
	addr := net.JoinHostPort(m.cfg.Host, strconv.Itoa(m.cfg.Port))
	tlsCfg := &tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: m.cfg.InsecureSkipVerify} //nolint:gosec // opt-in for tests
	var conn net.Conn
	d := &net.Dialer{}
	if m.cfg.TLS == "tls" {
		conn, err = (&tls.Dialer{NetDialer: d, Config: tlsCfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("notify: smtp dial: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	c, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("notify: smtp: %w", err)
	}
	defer c.Close()
	if err := c.Hello(helloName()); err != nil {
		return err
	}
	if m.cfg.TLS == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("notify: SMTP server does not offer STARTTLS (set MV_SMTP_TLS=none only for local relays)")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("notify: starttls: %w", err)
		}
	}
	if m.cfg.Username != "" {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)); err != nil {
				return fmt.Errorf("notify: smtp auth: %w", err)
			}
		}
	}
	if err := c.Mail(m.from.Address); err != nil {
		return err
	}
	if err := c.Rcpt(to.Address); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func helloName() string {
	return "localhost"
}

func buildMessage(from, to *mail.Address, e Email) ([]byte, error) {
	var b bytes.Buffer
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	domain := "mangaveyra.local"
	if at := strings.LastIndex(from.Address, "@"); at >= 0 {
		domain = from.Address[at+1:]
	}
	hdr := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	hdr("From", from.String())
	hdr("To", to.String())
	hdr("Subject", mime.QEncoding.Encode("utf-8", e.Subject))
	hdr("Date", time.Now().UTC().Format(time.RFC1123Z))
	hdr("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	hdr("MIME-Version", "1.0")
	hdr("Auto-Submitted", "auto-generated")
	writePart := func(ct, body string) error {
		fmt.Fprintf(&b, "Content-Type: %s; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", ct)
		qp := quotedprintable.NewWriter(&b)
		if _, err := qp.Write([]byte(strings.ReplaceAll(body, "\n", "\r\n"))); err != nil {
			return err
		}
		if err := qp.Close(); err != nil {
			return err
		}
		b.WriteString("\r\n")
		return nil
	}
	if e.HTML == "" {
		if err := writePart("text/plain", e.Text); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	}
	boundary := "mv-" + hex.EncodeToString(id)
	hdr("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	if err := writePart("text/plain", e.Text); err != nil {
		return nil, err
	}
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	if err := writePart("text/html", e.HTML); err != nil {
		return nil, err
	}
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.Bytes(), nil
}
