package notify

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core/netguard"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// Web push (RFC 8030) with VAPID (RFC 8292) and aes128gcm payload encryption (RFC 8291).

const vapidSettingsKey = "notify.vapid"

// ErrBadSubscription is returned for invalid push subscriptions.
var ErrBadSubscription = errors.New("notify: invalid push subscription")

// Subscription is a browser PushSubscription.
type Subscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256DH string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

type storedVAPID struct {
	Public     string `json:"public"`      // base64url uncompressed P-256 point (applicationServerKey)
	PrivateEnc string `json:"private_enc"` // sealed PKCS#8/SEC1 DER
}

type pushSender struct {
	s      *Service
	client *http.Client

	mu   sync.Mutex
	key  *ecdsa.PrivateKey
	pubB string
}

func newPushSender(s *Service) *pushSender {
	// The guarded transport re-checks the resolved address of every connection
	// (DNS rebinding); push services never need redirects.
	c := s.pushPolicy().Client(20 * time.Second)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &pushSender{s: s, client: c}
}

// pushPolicy is the SSRF policy for push endpoints: https only, public
// addresses unless private networks are allowed; link-local and cloud
// metadata addresses are always refused.
func (s *Service) pushPolicy() netguard.Policy {
	return netguard.Policy{AllowPrivate: s.opts.AllowPrivatePushEndpoints}
}

// SetPushHTTPClient replaces the HTTP client used for push delivery (tests).
func (s *Service) SetPushHTTPClient(c *http.Client) { s.push.client = c }

// VAPIDPublicKey returns the applicationServerKey browsers subscribe with
// (generated and stored on first use).
func (s *Service) VAPIDPublicKey(ctx context.Context) (string, error) {
	if err := s.push.load(ctx); err != nil {
		return "", err
	}
	return s.push.pubB, nil
}

func (p *pushSender) load(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.key != nil {
		return nil
	}
	var st storedVAPID
	ok, err := p.s.settings.GetKey(ctx, vapidSettingsKey, &st)
	if err != nil {
		return err
	}
	if ok {
		der, err := p.s.box.OpenString(st.PrivateEnc, vapidSettingsKey)
		if err != nil {
			return fmt.Errorf("notify: open VAPID key: %w", err)
		}
		key, err := x509.ParseECPrivateKey([]byte(der))
		if err != nil {
			return fmt.Errorf("notify: parse VAPID key: %w", err)
		}
		p.key, p.pubB = key, st.Public
		return nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	pub, err := key.PublicKey.ECDH()
	if err != nil {
		return err
	}
	st = storedVAPID{Public: base64.RawURLEncoding.EncodeToString(pub.Bytes()), PrivateEnc: p.s.box.SealString(string(der), vapidSettingsKey)}
	if err := p.s.settings.SetKey(ctx, vapidSettingsKey, st, ""); err != nil {
		return err
	}
	p.key, p.pubB = key, st.Public
	return nil
}

func decodeB64(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

// Subscribe stores a browser push subscription for userID.
func (s *Service) Subscribe(ctx context.Context, userID string, sub Subscription, userAgent string) error {
	if err := s.checkEndpoint(sub.Endpoint); err != nil {
		return err
	}
	pk, err := decodeB64(sub.Keys.P256DH)
	if err != nil || len(pk) != 65 {
		return fmt.Errorf("%w: p256dh must be a 65-byte uncompressed P-256 key", ErrBadSubscription)
	}
	if _, err := ecdh.P256().NewPublicKey(pk); err != nil {
		return fmt.Errorf("%w: p256dh is not on the curve", ErrBadSubscription)
	}
	auth, err := decodeB64(sub.Keys.Auth)
	if err != nil || len(auth) != 16 {
		return fmt.Errorf("%w: auth must be 16 bytes", ErrBadSubscription)
	}
	_, err = s.db.Exec(ctx, `INSERT INTO push_subscriptions (id, user_id, endpoint, p256dh, auth, user_agent, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (endpoint) DO UPDATE SET user_id = excluded.user_id, p256dh = excluded.p256dh, auth = excluded.auth, failures = 0`,
		db.NewID(), userID, sub.Endpoint, sub.Keys.P256DH, sub.Keys.Auth, truncate(userAgent, 300), s.now())
	return err
}

// Unsubscribe removes a subscription of userID.
func (s *Service) Unsubscribe(ctx context.Context, userID, endpoint string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM push_subscriptions WHERE user_id = ? AND endpoint = ?`, userID, endpoint)
	return err
}

// checkEndpoint only allows https endpoints on public addresses (SSRF guard).
// It is syntactic (no DNS lookup); the delivery client checks resolved addresses.
func (s *Service) checkEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || len(endpoint) > 1024 {
		return fmt.Errorf("%w: endpoint", ErrBadSubscription)
	}
	if u.Scheme != "https" || u.User != nil {
		return fmt.Errorf("%w: endpoint must be https", ErrBadSubscription)
	}
	p := s.pushPolicy()
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if ip, err := netip.ParseAddr(host); err == nil {
		if err := p.CheckIP(ip); err != nil {
			return fmt.Errorf("%w: endpoint must be public", ErrBadSubscription)
		}
		return nil
	}
	if !p.AllowPrivate && (host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".home.arpa")) {
		return fmt.Errorf("%w: endpoint must be public", ErrBadSubscription)
	}
	return nil
}

type pushRow struct {
	ID       string `db:"id"`
	Endpoint string `db:"endpoint"`
	P256DH   string `db:"p256dh"`
	Auth     string `db:"auth"`
}

// PushPayload is the JSON the service worker receives.
type PushPayload struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	Link  string `json:"link,omitempty"`
}

func (p *pushSender) sendToUser(ctx context.Context, n Notification) error {
	if p.s.settings.MustInstance(ctx).LocalOnly {
		return nil // local-only mode: nothing leaves the server
	}
	var subs []pushRow
	if err := p.s.db.Select(ctx, &subs, `SELECT id, endpoint, p256dh, auth FROM push_subscriptions WHERE user_id = ? AND failures < 5`, n.UserID); err != nil {
		return err
	}
	if len(subs) == 0 {
		return nil
	}
	if err := p.load(ctx); err != nil {
		return err
	}
	payload, _ := json.Marshal(PushPayload{ID: n.ID, Kind: n.Kind, Title: n.Title, Body: truncate(n.Body, 300), Link: n.Link})
	var errs []error
	sent := false
	for _, sub := range subs {
		status, err := p.send(ctx, sub, payload)
		switch {
		case err == nil && status >= 200 && status < 300:
			sent = true
			_, _ = p.s.db.Exec(ctx, `UPDATE push_subscriptions SET failures = 0, last_used_at = ? WHERE id = ?`, p.s.now(), sub.ID)
		case status == http.StatusNotFound || status == http.StatusGone:
			_, _ = p.s.db.Exec(ctx, `DELETE FROM push_subscriptions WHERE id = ?`, sub.ID)
		default:
			_, _ = p.s.db.Exec(ctx, `UPDATE push_subscriptions SET failures = failures + 1 WHERE id = ?`, sub.ID)
			if err == nil {
				err = fmt.Errorf("push service returned %d", status)
			}
			errs = append(errs, err)
		}
	}
	if sent {
		_, _ = p.s.db.Exec(ctx, `UPDATE notifications SET pushed_at = ? WHERE id = ?`, p.s.now(), n.ID)
	}
	return errors.Join(errs...)
}

func (p *pushSender) send(ctx context.Context, sub pushRow, payload []byte) (int, error) {
	if err := p.s.checkEndpoint(sub.Endpoint); err != nil {
		return 0, err
	}
	uaPub, err := decodeB64(sub.P256DH)
	if err != nil {
		return 0, err
	}
	auth, err := decodeB64(sub.Auth)
	if err != nil {
		return 0, err
	}
	body, err := EncryptPayload(uaPub, auth, payload)
	if err != nil {
		return 0, err
	}
	u, _ := url.Parse(sub.Endpoint)
	jwt, err := p.vapidJWT(u.Scheme + "://" + u.Host)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", "86400")
	req.Header.Set("Urgency", "normal")
	req.Header.Set("Authorization", "vapid t="+jwt+", k="+p.pubB)
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	return resp.StatusCode, nil
}

func (p *pushSender) vapidJWT(aud string) (string, error) {
	sub := "mailto:admin@localhost"
	if pu := p.s.opts.PublicURL; strings.HasPrefix(pu, "https://") {
		sub = pu
	}
	head := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, _ := json.Marshal(map[string]any{"aud": aud, "exp": time.Now().Add(12 * time.Hour).Unix(), "sub": sub})
	signing := head + "." + base64.RawURLEncoding.EncodeToString(claims)
	h := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, p.key, h[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// EncryptPayload encrypts plaintext for a subscription (RFC 8291, aes128gcm, one record).
func EncryptPayload(uaPublic, authSecret, plaintext []byte) ([]byte, error) {
	curve := ecdh.P256()
	ua, err := curve.NewPublicKey(uaPublic)
	if err != nil {
		return nil, fmt.Errorf("notify: subscription key: %w", err)
	}
	as, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	secret, err := as.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPublic := as.PublicKey().Bytes()
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	cek, nonce, err := deriveKeys(secret, authSecret, uaPublic, asPublic, salt)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	const rs = 4096
	if len(plaintext)+1+gcm.Overhead() > rs {
		return nil, errors.New("notify: push payload too large")
	}
	ct := gcm.Seal(nil, nonce, append(append([]byte{}, plaintext...), 0x02), nil)
	out := make([]byte, 0, 16+4+1+len(asPublic)+len(ct))
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, rs)
	out = append(out, byte(len(asPublic)))
	out = append(out, asPublic...)
	return append(out, ct...), nil
}

// DecryptPayload reverses EncryptPayload with the subscriber's private key (used in tests).
func DecryptPayload(uaPrivate *ecdh.PrivateKey, authSecret, body []byte) ([]byte, error) {
	if len(body) < 21 {
		return nil, errors.New("short body")
	}
	salt, idlen := body[:16], int(body[20])
	if len(body) < 21+idlen {
		return nil, errors.New("short header")
	}
	asPublic := body[21 : 21+idlen]
	as, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		return nil, err
	}
	secret, err := uaPrivate.ECDH(as)
	if err != nil {
		return nil, err
	}
	cek, nonce, err := deriveKeys(secret, authSecret, uaPrivate.PublicKey().Bytes(), asPublic, salt)
	if err != nil {
		return nil, err
	}
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	pt, err := gcm.Open(nil, nonce, body[21+idlen:], nil)
	if err != nil {
		return nil, err
	}
	pt = bytes.TrimRight(pt, "\x00")
	if len(pt) == 0 || pt[len(pt)-1] != 0x02 {
		return nil, errors.New("bad padding delimiter")
	}
	return pt[:len(pt)-1], nil
}

func deriveKeys(ecdhSecret, authSecret, uaPublic, asPublic, salt []byte) (cek, nonce []byte, err error) {
	prkKey, err := hkdf.Extract(sha256.New, ecdhSecret, authSecret)
	if err != nil {
		return nil, nil, err
	}
	keyInfo := "WebPush: info\x00" + string(uaPublic) + string(asPublic)
	ikm, err := hkdf.Expand(sha256.New, prkKey, keyInfo, 32)
	if err != nil {
		return nil, nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, nil, err
	}
	if cek, err = hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16); err != nil {
		return nil, nil, err
	}
	nonce, err = hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	return cek, nonce, err
}
