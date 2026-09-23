package notify_test

import (
	"bufio"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/notify"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

type pub struct {
	mu  sync.Mutex
	got []string
}

func (p *pub) Publish(topic, typ string, data any) {
	p.mu.Lock()
	p.got = append(p.got, topic+" "+typ)
	p.mu.Unlock()
}

func TestNotificationsInAppGroupingAndRead(t *testing.T) {
	for _, d := range testdb.Dialects() {
		t.Run(d, func(t *testing.T) {
			ta := testutil.NewTestApp(t, testutil.WithDialect(d))
			ctx := context.Background()
			n := ta.Deps.Notify
			p := &pub{}
			n.SetPublisher(p)
			u, c := ta.UserClient(t, auth.RoleCreator)
			first, err := n.Notify(ctx, notify.Input{UserID: u.ID, Kind: notify.KindComment, Title: "Kaito commented on page 7", GroupKey: "comments:ch1"})
			if err != nil {
				t.Fatal(err)
			}
			second, _ := n.Notify(ctx, notify.Input{UserID: u.ID, Kind: notify.KindComment, Title: "3 new comments on chapter 1", GroupKey: "comments:ch1"})
			if second.ID != first.ID || second.Title != "3 new comments on chapter 1" {
				t.Fatalf("grouping: %+v vs %+v", first, second)
			}
			n.Notify(ctx, notify.Input{UserID: u.ID, Kind: notify.KindFollow, Title: "Sora Vell followed Tidebreak Courier", Link: "/u/sora"})
			if got := strings.Join(p.got, ";"); !strings.Contains(got, "user:"+u.ID+" notification.new") {
				t.Fatalf("no ws push: %s", got)
			}
			var list struct {
				Items  []notify.Notification `json:"items"`
				Unread int                   `json:"unread"`
			}
			c.Get("/api/v1/notifications").JSON(t, &list)
			if len(list.Items) != 2 || list.Unread != 2 {
				t.Fatalf("list: %+v", list)
			}
			c.PostJSON("/api/v1/notifications/read", map[string]any{"ids": []string{first.ID}})
			if cnt, _ := n.UnreadCount(ctx, u.ID); cnt != 1 {
				t.Fatalf("unread after read one: %d", cnt)
			}
			c.PostJSON("/api/v1/notifications/read", map[string]any{})
			if cnt, _ := n.UnreadCount(ctx, u.ID); cnt != 0 {
				t.Fatalf("unread after read all: %d", cnt)
			}
			// other users cannot read them
			_, c2 := ta.UserClient(t, auth.RoleCreator)
			c2.Get("/api/v1/notifications").JSON(t, &list)
			if len(list.Items) != 0 {
				t.Fatal("leaked notifications")
			}
			// prefs round trip + validation
			r := c.SendJSON("PUT", "/api/v1/notifications/prefs", map[string]any{"digest": "daily", "quiet_hours": map[string]any{"enabled": true, "start": "23:00", "end": "07:30", "timezone": "Asia/Tokyo"}})
			if r.StatusCode != 200 {
				t.Fatalf("prefs: %s", r.Body)
			}
			pr, _ := n.Prefs(ctx, u.ID)
			if pr.Digest != "daily" || pr.QuietHours.Timezone != "Asia/Tokyo" || !pr.Channel(notify.KindMention).Email {
				t.Fatalf("prefs merge: %+v", pr)
			}
			if r := c.SendJSON("PUT", "/api/v1/notifications/prefs", map[string]any{"digest": "hourly"}); r.StatusCode != 422 {
				t.Fatalf("invalid digest accepted: %d", r.StatusCode)
			}
		})
	}
}

// fakeSMTP is a minimal SMTP server that records one message per connection.
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	msgs []string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeSMTP{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	w := func(line string) { io.WriteString(c, line+"\r\n") }
	w("220 fake ESMTP")
	var data strings.Builder
	inData := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		if inData {
			if line == ".\r\n" {
				inData = false
				s.mu.Lock()
				s.msgs = append(s.msgs, data.String())
				s.mu.Unlock()
				w("250 queued")
				continue
			}
			data.WriteString(line)
			continue
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			w("250 fake")
		case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"), strings.HasPrefix(cmd, "RSET"), strings.HasPrefix(cmd, "NOOP"):
			w("250 ok")
		case cmd == "DATA":
			inData = true
			w("354 go ahead")
		case cmd == "QUIT":
			w("221 bye")
			return
		default:
			w("500 unknown")
		}
	}
}

func (s *fakeSMTP) messages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.msgs...)
}

func (s *fakeSMTP) port() string {
	_, p, _ := net.SplitHostPort(s.ln.Addr().String())
	return p
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestEmailDeliveryResetAndDigest(t *testing.T) {
	smtpd := newFakeSMTP(t)
	ta := testutil.NewTestApp(t, testutil.WithEnv("MV_SMTP_HOST", "127.0.0.1", "MV_SMTP_PORT", smtpd.port(), "MV_SMTP_TLS", "none",
		"MV_SMTP_FROM", "Tidebreak Studio <noreply@example.org>", "MV_PUBLIC_URL", "https://manga.example.org"))
	ctx := context.Background()
	n := ta.Deps.Notify
	if !n.EmailEnabled() || !ta.Deps.Auth.EmailEnabled() {
		t.Fatal("email should be enabled")
	}
	u := ta.CreateUser(t, "rin", auth.RoleCreator)

	// A mention is emailed immediately (default prefs).
	n.Notify(ctx, notify.Input{UserID: u.ID, Kind: notify.KindMention, Title: "Old Wren mentioned you", Body: "Check panel 7.3, Rin.", Link: "/studio/p/x"})
	n.Wait()
	msgs := smtpd.messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "To: <rin@example.org>") || !strings.Contains(msgs[0], "https://manga.example.org/studio/p/x") ||
		!strings.Contains(msgs[0], "Subject: [MangaVeyra] Old Wren mentioned you") {
		t.Fatalf("mention email: %q", msgs)
	}

	// Password reset email with a working link.
	if r := ta.NewClient().PostJSON("/api/v1/auth/password/forgot", map[string]string{"email": "rin@example.org"}); r.StatusCode != 200 {
		t.Fatalf("forgot: %s", r.Body)
	}
	if r := ta.NewClient().PostJSON("/api/v1/auth/password/forgot", map[string]string{"email": "nobody@example.org"}); r.StatusCode != 200 {
		t.Fatalf("unknown email must look the same: %s", r.Body)
	}
	waitFor(t, func() bool { return len(smtpd.messages()) == 2 })
	decoded, _ := io.ReadAll(quotedprintable.NewReader(strings.NewReader(smtpd.messages()[1])))
	reset := string(decoded)
	i := strings.Index(reset, "https://manga.example.org/reset/")
	if i < 0 {
		t.Fatalf("reset email: %q", reset)
	}
	token := strings.TrimSpace(strings.SplitN(reset[i+len("https://manga.example.org/reset/"):], "\r\n", 2)[0])
	if _, err := ta.Deps.Auth.ResetPassword(ctx, token, "fresh password here"); err != nil {
		t.Fatalf("reset with emailed token %q: %v", token, err)
	}

	// Quiet hours suppress email; digest users get one batched email instead.
	all := notify.DefaultPrefs()
	all.QuietHours = notify.QuietHours{Enabled: true, Start: "00:00", End: "23:59"}
	if err := n.SetPrefs(ctx, u.ID, all); err != nil {
		t.Fatal(err)
	}
	n.Notify(ctx, notify.Input{UserID: u.ID, Kind: notify.KindMention, Title: "quiet please"})
	n.Wait()
	if len(smtpd.messages()) != 2 {
		t.Fatal("email sent during quiet hours")
	}
	digest := notify.DefaultPrefs()
	digest.Digest = notify.DigestDaily
	if err := n.SetPrefs(ctx, u.ID, digest); err != nil {
		t.Fatal(err)
	}
	n.Notify(ctx, notify.Input{UserID: u.ID, Kind: notify.KindMention, Title: "Digest item two"})
	n.Wait()
	if len(smtpd.messages()) != 2 {
		t.Fatal("digest users must not get immediate email")
	}
	sent, err := n.RunDigests(ctx)
	if err != nil || sent != 1 {
		t.Fatalf("digests: %d %v", sent, err)
	}
	d := smtpd.messages()[2]
	if !strings.Contains(d, "Digest item two") || !strings.Contains(d, "quiet please") || !strings.Contains(d, "digest") {
		t.Fatalf("digest email: %q", d)
	}
	if sent, _ := n.RunDigests(ctx); sent != 0 {
		t.Fatal("digest sent twice in one day")
	}
}

func TestWebPushEncryptionAndDelivery(t *testing.T) {
	// A fake push service that decrypts what it receives with the subscriber's keys.
	uaKey, _ := ecdh.P256().GenerateKey(rand.Reader)
	authSecret := make([]byte, 16)
	rand.Read(authSecret)
	var mu sync.Mutex
	var got []byte
	var authz string
	pushSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		pt, err := notify.DecryptPayload(uaKey, authSecret, body)
		if err != nil || r.Header.Get("Content-Encoding") != "aes128gcm" {
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		got, authz = pt, r.Header.Get("Authorization")
		mu.Unlock()
		w.WriteHeader(201)
	}))
	defer pushSrv.Close()

	ta := testutil.NewTestApp(t, testutil.WithEnv("MV_ALLOW_PRIVATE_PROVIDER_URLS", "true"))
	ctx := context.Background()
	n := ta.Deps.Notify
	n.SetPushHTTPClient(pushSrv.Client())
	u, c := ta.UserClient(t, auth.RoleCreator)

	var vk struct {
		PublicKey string `json:"public_key"`
	}
	c.Get("/api/v1/push/vapid-public-key").JSON(t, &vk)
	if raw, err := base64.RawURLEncoding.DecodeString(vk.PublicKey); err != nil || len(raw) != 65 {
		t.Fatalf("vapid key %q", vk.PublicKey)
	}
	sub := map[string]any{"endpoint": pushSrv.URL + "/push/abc", "keys": map[string]string{
		"p256dh": base64.RawURLEncoding.EncodeToString(uaKey.PublicKey().Bytes()),
		"auth":   base64.RawURLEncoding.EncodeToString(authSecret),
	}}
	if r := c.PostJSON("/api/v1/push/subscriptions", sub); r.StatusCode != 201 {
		t.Fatalf("subscribe: %d %s", r.StatusCode, r.Body)
	}
	// http endpoints are rejected
	bad := map[string]any{"endpoint": "http://push.example.org/x", "keys": sub["keys"]}
	if r := c.PostJSON("/api/v1/push/subscriptions", bad); r.StatusCode != 422 {
		t.Fatalf("http endpoint accepted: %d", r.StatusCode)
	}
	n.Notify(ctx, notify.Input{UserID: u.ID, Kind: notify.KindJobDone, Title: "Page 7 is ready", Link: "/studio"})
	n.Wait()
	mu.Lock()
	defer mu.Unlock()
	var payload notify.PushPayload
	if err := json.Unmarshal(got, &payload); err != nil || payload.Title != "Page 7 is ready" {
		t.Fatalf("push payload %q: %v", got, err)
	}
	if !strings.HasPrefix(authz, "vapid t=") || !strings.Contains(authz, ", k="+vk.PublicKey) {
		t.Fatalf("vapid header %q", authz)
	}
	parts := strings.Split(strings.TrimPrefix(strings.SplitN(authz, ",", 2)[0], "vapid t="), ".")
	if len(parts) != 3 {
		t.Fatalf("jwt %q", authz)
	}
}

func TestPushEndpointSSRFGuard(t *testing.T) {
	ta := testutil.NewTestApp(t)
	u := ta.CreateUser(t, "sora", auth.RoleReader)
	uaKey, _ := ecdh.P256().GenerateKey(rand.Reader)
	for _, ep := range []string{"https://127.0.0.1/x", "https://169.254.169.254/latest", "https://localhost/x", "https://10.0.0.5/push",
		"https://[::ffff:169.254.169.254]/latest", "https://[::ffff:127.0.0.1]/x", "https://100.64.1.1/x", "https://0.0.0.0/x",
		"https://user:pw@push.example.org/x", "https://ollama.internal/x"} {
		var s notify.Subscription
		s.Endpoint = ep
		s.Keys.P256DH = base64.RawURLEncoding.EncodeToString(uaKey.PublicKey().Bytes())
		s.Keys.Auth = base64.RawURLEncoding.EncodeToString(make([]byte, 16))
		if err := ta.Deps.Notify.Subscribe(context.Background(), u.ID, s, ""); err == nil {
			t.Errorf("%s accepted", ep)
		}
	}
}

// TestPushMetadataBlockedWithPrivateNetworks: allowing private networks
// (air-gapped relays) must still refuse link-local / cloud metadata, both when
// subscribing and at delivery (the guarded client checks resolved addresses).
func TestPushMetadataBlockedWithPrivateNetworks(t *testing.T) {
	ta := testutil.NewTestApp(t, testutil.WithEnv("MV_ALLOW_PRIVATE_PROVIDER_URLS", "true"))
	u := ta.CreateUser(t, "pip", auth.RoleReader)
	uaKey, _ := ecdh.P256().GenerateKey(rand.Reader)
	sub := func(ep string) error {
		var s notify.Subscription
		s.Endpoint = ep
		s.Keys.P256DH = base64.RawURLEncoding.EncodeToString(uaKey.PublicKey().Bytes())
		s.Keys.Auth = base64.RawURLEncoding.EncodeToString(make([]byte, 16))
		return ta.Deps.Notify.Subscribe(context.Background(), u.ID, s, "")
	}
	if err := sub("https://10.0.0.5/push"); err != nil {
		t.Fatalf("private relay refused with private networks allowed: %v", err)
	}
	for _, ep := range []string{"https://169.254.169.254/latest", "https://[fe80::1]/x", "https://[fd00:ec2::254]/x"} {
		if err := sub(ep); err == nil {
			t.Errorf("%s accepted", ep)
		}
	}
}
