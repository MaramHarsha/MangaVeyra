package stub

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/sdk/mvpack"
)

// ---- test harness ----

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type harness struct {
	t     *testing.T
	s     *Server
	ts    *httptest.Server
	clock *fakeClock
}

func newHarness(t *testing.T, opts Options) *harness {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)}
	opts.Now = clock.Now
	s, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	s.SetPublicURL(ts.URL)
	return &harness{t: t, s: s, ts: ts, clock: clock}
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func (r response) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("response is not a JSON object (%d): %s", r.status, r.body)
	}
	return m
}

func (r response) errorCode(t *testing.T) string {
	t.Helper()
	e, ok := r.json(t)["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error envelope in %s", r.body)
	}
	return e["code"].(string)
}

func (h *harness) do(method, path, token string, body io.Reader, contentType string, hdr ...string) response {
	h.t.Helper()
	req, err := http.NewRequest(method, h.ts.URL+path, body)
	if err != nil {
		h.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	recordResponse(h.t, method, path, res, b)
	return response{status: res.StatusCode, header: res.Header, body: b}
}

// recordResponse appends API responses to $WORLDSTUB_RECORD/responses.jsonl so
// sdk/api/conformance.py can validate every response the tests saw against
// the schemas in world.openapi.yaml.
var recordMu sync.Mutex

func recordResponse(t *testing.T, method, path string, res *http.Response, body []byte) {
	dir := os.Getenv("WORLDSTUB_RECORD")
	if dir == "" || !isAPI(path) {
		return
	}
	u, _ := url.Parse(path)
	line, _ := json.Marshal(map[string]any{
		"test": t.Name(), "method": method, "path": u.Path, "status": res.StatusCode,
		"content_type": res.Header.Get("Content-Type"), "body": string(body),
	})
	recordMu.Lock()
	defer recordMu.Unlock()
	f, err := os.OpenFile(filepath.Join(dir, "responses.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

func (h *harness) get(path, token string) response { return h.do("GET", path, token, nil, "") }

func (h *harness) sendJSON(method, path, token string, v any) response {
	b, _ := json.Marshal(v)
	return h.do(method, path, token, bytes.NewReader(b), "application/json")
}

func (h *harness) form(path string, v url.Values) response {
	return h.do("POST", path, "", strings.NewReader(v.Encode()), "application/x-www-form-urlencoded")
}

func wantStatus(t *testing.T, r response, want int) {
	t.Helper()
	if r.status != want {
		t.Fatalf("status %d, want %d: %s", r.status, want, r.body)
	}
}

// link runs the device flow and returns an access token for handle ("new" for a fresh account).
func (h *harness) link(handle string) string {
	h.t.Helper()
	r := h.form("/oauth/device_authorization", url.Values{"client_id": {clientID}})
	wantStatus(h.t, r, 200)
	d := r.json(h.t)
	if !h.s.opts.AutoApprove {
		if err := h.s.ApproveDevice(d["user_code"].(string), handle); err != nil {
			h.t.Fatal(err)
		}
	}
	r = h.form("/oauth/token", url.Values{"client_id": {clientID}, "grant_type": {deviceGrant}, "device_code": {d["device_code"].(string)}})
	wantStatus(h.t, r, 200)
	return r.json(h.t)["access_token"].(string)
}

// registerKey registers a fresh server key and returns a signer for it.
func (h *harness) registerKey(token, name string) *mvpack.Signer {
	h.t.Helper()
	seed := sha256.Sum256([]byte("test key " + name))
	priv := ed25519.NewKeyFromSeed(seed[:])
	r := h.sendJSON("POST", "/v1/servers", token, map[string]string{
		"name": name, "public_key": base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)), "app_version": "1.0.0",
	})
	if r.status != 201 && r.status != 200 {
		h.t.Fatalf("register key: %d %s", r.status, r.body)
	}
	return &mvpack.Signer{KeyID: r.json(h.t)["id"].(string), Key: priv, Role: mvpack.SignerServer, Now: h.clock.Now}
}

var tinyPNG = func() []byte { return placeholderPNG(8, 8, 40, 0) }()

// buildPack makes a character pack credited to the given World name.
// packFile is an extra payload file for buildPack.
type packFile struct {
	path string
	role mvpack.Role
	data []byte
}

func buildPack(t *testing.T, name, display, handle, version string, signer *mvpack.Signer, extra ...packFile) []byte {
	t.Helper()
	m := mvpack.Manifest{
		Kind: mvpack.KindCharacter, ID: slugify(name), Name: name, Version: version,
		CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Licence: "CC-BY-4.0",
		AILabel: mvpack.AILabel{Art: mvpack.AIHuman, Design: mvpack.AIHuman},
		Credit:  mvpack.Credit{DisplayName: display, Handle: handle},
	}
	var buf bytes.Buffer
	w, err := mvpack.NewWriter(&buf, m)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AddBytes("refs/front.png", mvpack.RoleReference, tinyPNG); err != nil {
		t.Fatal(err)
	}
	for _, e := range extra {
		if err := w.AddBytes(e.path, e.role, e.data); err != nil {
			t.Fatal(err)
		}
	}
	var signers []*mvpack.Signer
	if signer != nil {
		signers = append(signers, signer)
	}
	if _, err := w.Close(signers...); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// submit uploads a pack with its signed manifest taken from inside the pack.
func (h *harness) submit(token string, pack []byte, hdr ...string) response {
	h.t.Helper()
	p, err := mvpack.Open(bytes.NewReader(pack), int64(len(pack)), nil)
	if err != nil {
		h.t.Fatal(err)
	}
	sm := map[string]any{"manifest": json.RawMessage(p.CanonicalManifest())}
	if sig := p.Signature(); sig != nil {
		sm["signature"] = sig
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	smb, _ := json.Marshal(sm)
	_ = mw.WriteField("signed_manifest", string(smb))
	_ = mw.WriteField("declarations", `{"rights_held":true,"original":true,"ai_label_correct":true}`)
	fw, _ := mw.CreateFormFile("pack", "pack.mvpack")
	_, _ = fw.Write(pack)
	_ = mw.Close()
	return h.do("POST", "/v1/submissions", token, &body, mw.FormDataContentType(), hdr...)
}

func checkStatuses(t *testing.T, sub map[string]any) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, c := range sub["checks"].([]any) {
		cm := c.(map[string]any)
		out[cm["name"].(string)] = cm["status"].(string)
	}
	return out
}

// ---- device flow ----

func TestDeviceFlow(t *testing.T) {
	h := newHarness(t, Options{Seed: true})
	r := h.form("/oauth/device_authorization", url.Values{"client_id": {clientID}, "scope": {"profile submit"}})
	wantStatus(t, r, 200)
	d := r.json(t)
	if !strings.HasSuffix(d["verification_uri"].(string), "/device") || d["interval"].(float64) != 5 {
		t.Fatalf("unexpected device response %v", d)
	}
	poll := func() response {
		return h.form("/oauth/token", url.Values{"client_id": {clientID}, "grant_type": {deviceGrant}, "device_code": {d["device_code"].(string)}})
	}
	r = poll()
	if r.status != 400 || r.json(t)["error"] != "authorization_pending" {
		t.Fatalf("want authorization_pending, got %d %s", r.status, r.body)
	}
	if r = poll(); r.json(t)["error"] != "slow_down" {
		t.Fatalf("want slow_down when polling too fast, got %s", r.body)
	}
	h.clock.Advance(11 * time.Second)

	// The browser page shows the code and approves it.
	page := h.get("/device?user_code="+strings.ToLower(d["user_code"].(string)), "")
	wantStatus(t, page, 200)
	if !bytes.Contains(page.body, []byte("Submit items for review")) || !bytes.Contains(page.body, []byte("not the real World platform")) {
		t.Fatalf("device page misses scope list or stub banner")
	}
	r = h.form("/device", url.Values{"user_code": {d["user_code"].(string)}, "account": {"lin-park"}, "decision": {"approve"}})
	wantStatus(t, r, 200)

	r = poll()
	wantStatus(t, r, 200)
	tok := r.json(t)
	if tok["token_type"] != "Bearer" || tok["scope"] != "profile submit" {
		t.Fatalf("bad token response %v", tok)
	}
	if r.header.Get("Cache-Control") != "no-store" {
		t.Fatal("token response must not be cached")
	}
	if r = poll(); r.json(t)["error"] != "invalid_grant" {
		t.Fatalf("a device code is single use, got %s", r.body)
	}

	me := h.get("/v1/me", tok["access_token"].(string))
	wantStatus(t, me, 200)
	if me.json(t)["handle"] != "lin-park" {
		t.Fatalf("linked the wrong account: %s", me.body)
	}
	// The token has no library scope.
	wantStatus(t, h.get("/v1/library", tok["access_token"].(string)), 401)

	// Refresh tokens rotate.
	r = h.form("/oauth/token", url.Values{"client_id": {clientID}, "grant_type": {"refresh_token"}, "refresh_token": {tok["refresh_token"].(string)}})
	wantStatus(t, r, 200)
	fresh := r.json(t)["access_token"].(string)
	wantStatus(t, h.get("/v1/me", tok["access_token"].(string)), 401)
	wantStatus(t, h.get("/v1/me", fresh), 200)

	// Revocation.
	wantStatus(t, h.form("/oauth/revoke", url.Values{"token": {fresh}}), 200)
	wantStatus(t, h.get("/v1/me", fresh), 401)

	// Access tokens expire.
	other := h.link("aiko")
	h.clock.Advance(accessTTL + time.Second)
	if r := h.get("/v1/me", other); r.status != 401 || r.errorCode(t) != "unauthorized" {
		t.Fatalf("expired token accepted: %d %s", r.status, r.body)
	}
}

func TestDeviceFlowDeniedExpiredAndErrors(t *testing.T) {
	h := newHarness(t, Options{Seed: true})
	if r := h.form("/oauth/device_authorization", url.Values{"client_id": {"someone-else"}}); r.status != 401 || r.json(t)["error"] != "invalid_client" {
		t.Fatalf("unknown client accepted: %d %s", r.status, r.body)
	}
	if r := h.form("/oauth/device_authorization", url.Values{"client_id": {clientID}, "scope": {"payments"}}); r.json(t)["error"] != "invalid_scope" {
		t.Fatalf("unknown scope accepted: %s", r.body)
	}
	if r := h.form("/oauth/token", url.Values{"client_id": {clientID}, "grant_type": {"password"}}); r.json(t)["error"] != "unsupported_grant_type" {
		t.Fatalf("password grant accepted: %s", r.body)
	}

	d := h.form("/oauth/device_authorization", url.Values{"client_id": {clientID}}).json(t)
	r := h.form("/device", url.Values{"user_code": {d["user_code"].(string)}, "decision": {"deny"}})
	wantStatus(t, r, 200)
	r = h.form("/oauth/token", url.Values{"client_id": {clientID}, "grant_type": {deviceGrant}, "device_code": {d["device_code"].(string)}})
	if r.json(t)["error"] != "access_denied" {
		t.Fatalf("want access_denied, got %s", r.body)
	}

	d = h.form("/oauth/device_authorization", url.Values{"client_id": {clientID}}).json(t)
	h.clock.Advance(deviceTTL + time.Second)
	r = h.form("/oauth/token", url.Values{"client_id": {clientID}, "grant_type": {deviceGrant}, "device_code": {d["device_code"].(string)}})
	if r.json(t)["error"] != "expired_token" {
		t.Fatalf("want expired_token, got %s", r.body)
	}
	if err := h.s.ApproveDevice(d["user_code"].(string), "aiko"); !errors.Is(err, errNotFound) {
		t.Fatalf("approving an expired code: %v", err)
	}
	wantStatus(t, h.form("/device", url.Values{"user_code": {"ZZZZ-ZZZZ"}, "decision": {"approve"}}), 404)

	// A cross-site browser post to the approval form is refused.
	r = h.do("POST", "/device", "", strings.NewReader("user_code=X&decision=approve"), "application/x-www-form-urlencoded",
		"Origin", "https://evil.example", "Sec-Fetch-Site", "cross-site")
	wantStatus(t, r, 403)
}

func TestAutoApprove(t *testing.T) {
	h := newHarness(t, Options{Seed: true, AutoApprove: true})
	tok := h.link("") // approved on issue
	if me := h.get("/v1/me", tok).json(t); me["handle"] != "jerry-ink" {
		t.Fatalf("auto-approve should link the seeded demo account, got %v", me["handle"])
	}

	fresh := newHarness(t, Options{AutoApprove: true})
	d := fresh.form("/oauth/device_authorization", url.Values{"client_id": {clientID}}).json(t)
	r := fresh.form("/oauth/token", url.Values{"client_id": {clientID}, "grant_type": {deviceGrant}, "device_code": {d["device_code"].(string)}})
	wantStatus(t, r, 200)
	me := fresh.get("/v1/me", r.json(t)["access_token"].(string)).json(t)
	if me["needs_profile"] != true || me["handle"] != nil {
		t.Fatalf("an unseeded auto-approve creates an account without a World name: %v", me)
	}

	if _, err := New(Options{AutoApprove: true, AutoApproveAs: "aiko"}); err == nil {
		t.Fatal("auto-approve as a seeded handle without --seed must fail")
	}
	if _, err := New(Options{Seed: true, AutoApprove: true, AutoApproveAs: "nobody"}); err == nil {
		t.Fatal("auto-approve as an unknown handle must fail")
	}
}

// ---- names ----

func TestHandleAvailability(t *testing.T) {
	h := newHarness(t, Options{Seed: true})
	cases := []struct {
		handle, reason string
		available      bool
	}{
		{"fresh-name", "", true},
		{"Bad_Name", "invalid", false},
		{"ab", "invalid", false},
		{"a--b", "invalid", false},
		{"aiko", "taken", false},
		{"admin", "reserved", false},
		{"mangaveyra", "reserved", false},
		{"rin-asagiri", "blocked", false},         // name of another creator's item
		{"captain-placeholder", "blocked", false}, // stub known-character list
		{"jerry", "previous_handle", false},       // Jerry Ink's old handle, reserved for six months
	}
	for _, c := range cases {
		r := h.get("/v1/handles/"+c.handle, "")
		wantStatus(t, r, 200)
		m := r.json(t)
		reason, _ := m["reason"].(string)
		if m["available"] != c.available || reason != c.reason {
			t.Errorf("%s: available=%v reason=%q, want %v %q", c.handle, m["available"], reason, c.available, c.reason)
		}
		if reason != "" && m["message"] == nil {
			t.Errorf("%s: a rejection must carry a message", c.handle)
		}
	}
	if m := h.get("/v1/handles/jerry", "").json(t); m["redirect_to"] != "jerry-ink" {
		t.Errorf("previous handle should redirect to jerry-ink, got %v", m["redirect_to"])
	}
	if m := h.get("/v1/handles/aiko", "").json(t); len(m["suggestions"].([]any)) == 0 {
		t.Error("a taken handle should come with suggestions")
	}
	// The owner sees their previous handle as available and their own item names as fine.
	tok := h.link("jerry-ink")
	if m := h.get("/v1/handles/jerry", tok).json(t); m["available"] != true || m["reason"] != "own_previous" {
		t.Errorf("own previous handle: %v", m)
	}
	if m := h.get("/v1/handles/rin-asagiri", tok).json(t); m["available"] != true {
		t.Errorf("creator may use their own item's name: %v", m)
	}
	// The profile page of an old handle redirects.
	r := h.get("/@jerry", "")
	if r.status != 301 || r.header.Get("Location") != "/@jerry-ink" {
		t.Errorf("old handle page: %d %q", r.status, r.header.Get("Location"))
	}
	wantStatus(t, h.get("/@jerry-ink", ""), 200)
	wantStatus(t, h.get("/@nobody-here", ""), 404)
}

func TestProfileUpdate(t *testing.T) {
	h := newHarness(t, Options{Seed: true})
	tok := h.link("new")
	me := h.get("/v1/me", tok).json(t)
	if me["needs_profile"] != true {
		t.Fatal("a new account needs a World name")
	}

	r := h.sendJSON("PATCH", "/v1/me/profile", tok, map[string]any{"display_name": strings.Repeat("x", 41), "handle": "admin"})
	wantStatus(t, r, 422)
	details := r.json(t)["error"].(map[string]any)["details"].([]any)
	codes := map[string]bool{}
	for _, d := range details {
		codes[d.(map[string]any)["code"].(string)] = true
	}
	if !codes["display_name_invalid"] || !codes["handle_reserved"] {
		t.Fatalf("want both problems reported, got %s", r.body)
	}
	wantStatus(t, h.sendJSON("PATCH", "/v1/me/profile", tok, map[string]any{"unknown": 1}), 400)
	wantStatus(t, h.sendJSON("PATCH", "/v1/me/profile", tok, map[string]any{}), 400)

	r = h.sendJSON("PATCH", "/v1/me/profile", tok, map[string]any{"display_name": "Mika Tern", "handle": "mika-tern", "bio": "Draws harbour cats."})
	wantStatus(t, r, 200)
	me = r.json(t)
	if me["handle"] != "mika-tern" || me["needs_profile"] != false || me["next_handle_change_at"] == nil {
		t.Fatalf("profile not saved: %s", r.body)
	}

	// Handles change once per 30 days; display names any time.
	h.clock.Advance(24 * time.Hour)
	tok = h.link("mika-tern") // the first token expired
	r = h.sendJSON("PATCH", "/v1/me/profile", tok, map[string]any{"handle": "mika-ink"})
	if r.status != 422 || !bytes.Contains(r.body, []byte("handle_change_too_soon")) {
		t.Fatalf("second change within 30 days: %d %s", r.status, r.body)
	}
	wantStatus(t, h.sendJSON("PATCH", "/v1/me/profile", tok, map[string]any{"display_name": "Mika T."}), 200)

	h.clock.Advance(30 * 24 * time.Hour)
	tok = h.link("mika-tern")
	wantStatus(t, h.sendJSON("PATCH", "/v1/me/profile", tok, map[string]any{"handle": "mika-ink"}), 200)
	m := h.get("/v1/handles/mika-tern", "").json(t)
	if m["reason"] != "previous_handle" || m["redirect_to"] != "mika-ink" {
		t.Fatalf("old handle should be reserved and redirect: %v", m)
	}
	// After six months the old handle is released.
	h.clock.Advance(handleReserve + time.Hour)
	if m := h.get("/v1/handles/mika-tern", "").json(t); m["available"] != true {
		t.Fatalf("old handle should be free after six months: %v", m)
	}
}

// ---- servers ----

func TestServerKeys(t *testing.T) {
	h := newHarness(t, Options{Seed: true})
	tok := h.link("aiko")
	seed := sha256.Sum256([]byte("server key"))
	pub := base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(seed[:]).Public().(ed25519.PublicKey))

	r := h.sendJSON("POST", "/v1/servers", tok, map[string]string{"name": "koi.studio.lan", "public_key": pub})
	wantStatus(t, r, 201)
	key := r.json(t)
	id := key["id"].(string)
	if !strings.HasPrefix(id, "srv_") || len(key["fingerprint"].(string)) != 64 {
		t.Fatalf("bad key response %v", key)
	}
	// Registering the same key again is idempotent.
	wantStatus(t, h.sendJSON("POST", "/v1/servers", tok, map[string]string{"name": "koi.studio.lan", "public_key": pub}), 200)
	// Another account cannot claim it.
	if r := h.sendJSON("POST", "/v1/servers", h.link("kestrel"), map[string]string{"name": "x", "public_key": pub}); r.status != 409 || r.errorCode(t) != "key_in_use" {
		t.Fatalf("key reuse across accounts: %d %s", r.status, r.body)
	}
	if r := h.sendJSON("POST", "/v1/servers", tok, map[string]string{"name": "x", "public_key": "bm90IGEga2V5"}); r.status != 422 {
		t.Fatalf("invalid key accepted: %d", r.status)
	}

	list := h.get("/v1/servers", tok).json(t)["items"].([]any)
	if len(list) != 2 { // the seeded key and the new one
		t.Fatalf("want 2 keys, got %d", len(list))
	}
	wantStatus(t, h.do("DELETE", "/v1/servers/"+id, tok, nil, ""), 204)
	wantStatus(t, h.do("DELETE", "/v1/servers/"+id, h.link("lin-park"), nil, ""), 404)
	if r := h.sendJSON("POST", "/v1/servers", tok, map[string]string{"name": "again", "public_key": pub}); r.errorCode(t) != "key_revoked" {
		t.Fatalf("a revoked key cannot be re-registered: %s", r.body)
	}
	wantStatus(t, h.get("/v1/servers", ""), 401)
}

// ---- submissions ----

func TestSubmissionManualReview(t *testing.T) {
	h := newHarness(t, Options{Seed: true, CheckDelay: 2 * time.Second})
	tok := h.link("aiko")
	signer := h.registerKey(tok, "koi.studio.lan")
	pack := buildPack(t, "Paper Crow Scout", "aiko", "aiko", "1.0.0", signer)

	r := h.submit(tok, pack, "Idempotency-Key", "abc")
	wantStatus(t, r, 202)
	sub := r.json(t)
	id := sub["id"].(string)
	if sub["status"] != "in_review" || r.header.Get("Location") != "/v1/submissions/"+id {
		t.Fatalf("bad submission response %s", r.body)
	}
	for name, st := range checkStatuses(t, sub) {
		if st != "pending" {
			t.Fatalf("check %s should be pending right after upload, is %s", name, st)
		}
	}
	if len(sub["checks"].([]any)) != len(checkNames) {
		t.Fatalf("want %d checks", len(checkNames))
	}
	// Retrying with the same idempotency key returns the same submission.
	if again := h.submit(tok, pack, "Idempotency-Key", "abc").json(t); again["id"] != id {
		t.Fatal("idempotency key ignored")
	}
	if r := h.submit(tok, pack); r.status != 409 || r.errorCode(t) != "duplicate_version" {
		t.Fatalf("same version twice: %d %s", r.status, r.body)
	}

	h.clock.Advance(3 * time.Second)
	sub = h.get("/v1/submissions/"+id, tok).json(t)
	for name, st := range checkStatuses(t, sub) {
		if st != "passed" {
			t.Fatalf("check %s: %s", name, st)
		}
	}
	if sub["status"] != "in_review" || sub["queue_position"].(float64) != 1 {
		t.Fatalf("clean submission waits for review: %v", sub["status"])
	}

	// Manual review through the stub endpoint.
	r = h.sendJSON("POST", "/_stub/review", "", map[string]string{"submission_id": id, "decision": "approved", "message": "Lovely scout."})
	wantStatus(t, r, 200)
	sub = h.get("/v1/submissions/"+id, tok).json(t)
	review := sub["review"].(map[string]any)
	if sub["status"] != "approved" || review["message"] != "Lovely scout." || review["automated"] != false || sub["world_item_id"] == nil {
		t.Fatalf("approval not recorded: %v", sub)
	}
	itemID := sub["world_item_id"].(string)

	// The item is in the catalogue, owned by its creator and countersigned by World.
	item := h.get("/v1/catalog/"+itemID, tok).json(t)
	if item["name"] != "Paper Crow Scout" || item["availability"] != "owned" || item["credit_line"] != "Character by aiko (@aiko), CC BY 4.0" {
		t.Fatalf("catalogue item: %v", item)
	}
	if h.get("/v1/catalog/"+itemID, "").json(t)["availability"] != "free" {
		t.Fatal("anonymous viewers see a free item as free")
	}
	dl := h.get("/v1/library/"+itemID+"/download", tok).json(t)
	pk := fetchPack(t, dl["url"].(string))
	p, err := mvpack.Open(bytes.NewReader(pk), int64(len(pk)), nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Verify(mvpack.VerifyOptions{Keys: h.s.Keys(), RequireSigner: mvpack.SignerWorld})
	if err != nil || len(res.Signatures) != 2 {
		t.Fatalf("downloaded pack should carry the server signature and World's: %v %+v", err, res)
	}

	// Approved submissions cannot be reviewed again.
	r = h.sendJSON("POST", "/_stub/review", "", map[string]string{"submission_id": id, "decision": "rejected"})
	if r.status != 409 || r.errorCode(t) != "not_reviewable" {
		t.Fatalf("re-review: %d %s", r.status, r.body)
	}

	// A new version of the same item uses world_item_id.
	m := mvpack.Manifest{Kind: mvpack.KindCharacter, ID: "paper-crow-scout", Name: "Paper Crow Scout", Version: "1.1.0",
		CreatedAt: h.clock.Now(), Licence: "CC-BY-4.0", AILabel: mvpack.AILabel{Art: mvpack.AIHuman},
		Credit: mvpack.Credit{DisplayName: "aiko", Handle: "aiko", WorldItemID: itemID}}
	var buf bytes.Buffer
	w, _ := mvpack.NewWriter(&buf, m)
	_ = w.AddBytes("refs/front.png", mvpack.RoleReference, tinyPNG)
	if _, err := w.Close(signer); err != nil {
		t.Fatal(err)
	}
	r = h.submit(tok, buf.Bytes())
	wantStatus(t, r, 202)
	if err := h.s.Review(r.json(t)["id"].(string), "approved", ""); err != nil {
		t.Fatal(err)
	}
	item = h.get("/v1/catalog/"+itemID, "").json(t)
	if item["version"] != "1.1.0" || len(item["versions"].([]any)) != 2 {
		t.Fatalf("new version not published: %v", item["versions"])
	}

	// Listing with a status filter and counts.
	list := h.get("/v1/submissions?status=approved", tok).json(t)
	if len(list["items"].([]any)) != 2 || list["counts"].(map[string]any)["all"].(float64) != 2 {
		t.Fatalf("list: %v", list)
	}
	wantStatus(t, h.get("/v1/submissions?status=nope", tok), 400)
	// Other accounts cannot see it.
	wantStatus(t, h.get("/v1/submissions/"+id, h.link("kestrel")), 404)
}

func fetchPack(t *testing.T, u string) []byte {
	t.Helper()
	res, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/vnd.mangaveyra.mvpack" {
		t.Fatalf("download: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	b, _ := io.ReadAll(res.Body)
	return b
}

func TestSubmissionAutoApproveAfterDelay(t *testing.T) {
	h := newHarness(t, Options{Seed: true, CheckDelay: time.Second, ReviewDelay: 10 * time.Second})
	tok := h.link("aiko")
	r := h.submit(tok, buildPack(t, "Moth Lantern", "aiko", "aiko", "1.0.0", h.registerKey(tok, "koi")))
	wantStatus(t, r, 202)
	sub := r.json(t)
	if sub["estimated_decision_at"] == nil {
		t.Fatal("with a review delay the stub estimates the decision time")
	}
	h.clock.Advance(5 * time.Second)
	if st := h.get("/v1/submissions/"+sub["id"].(string), tok).json(t)["status"]; st != "in_review" {
		t.Fatalf("approved too early: %v", st)
	}
	h.clock.Advance(6 * time.Second)
	sub = h.get("/v1/submissions/"+sub["id"].(string), tok).json(t)
	if sub["status"] != "approved" || sub["review"].(map[string]any)["automated"] != true {
		t.Fatalf("want automatic approval, got %v", sub)
	}
}

func TestSubmissionFailedChecksAndAppeal(t *testing.T) {
	h := newHarness(t, Options{Seed: true, CheckDelay: -1})
	tok := h.link("aiko")

	// Unsigned pack: the signature check fails and the submission is rejected.
	r := h.submit(tok, buildPack(t, "Unsigned Heron", "aiko", "aiko", "1.0.0", nil))
	wantStatus(t, r, 202)
	sub := r.json(t)
	if sub["status"] != "rejected" || checkStatuses(t, sub)["signature"] != "failed" || sub["can_appeal"] != true {
		t.Fatalf("unsigned pack: %s", r.body)
	}
	id := sub["id"].(string)
	if r := h.sendJSON("POST", "/v1/submissions/"+id+"/appeal", tok, map[string]string{"message": "short"}); r.status != 422 {
		t.Fatalf("too-short appeal accepted: %d", r.status)
	}
	r = h.sendJSON("POST", "/v1/submissions/"+id+"/appeal", tok, map[string]string{"message": "The key was registered a minute later; please look again."})
	wantStatus(t, r, 200)
	sub = r.json(t)
	if sub["status"] != "in_review" || sub["appeal"].(map[string]any)["state"] != "open" || sub["can_appeal"] != false {
		t.Fatalf("appeal: %s", r.body)
	}
	if err := h.s.Review(id, "rejected", "Still unsigned."); err != nil {
		t.Fatal(err)
	}
	sub = h.get("/v1/submissions/"+id, tok).json(t)
	if sub["appeal"].(map[string]any)["state"] != "upheld" || sub["can_appeal"] != false {
		t.Fatalf("appeal outcome: %v", sub)
	}
	if r := h.sendJSON("POST", "/v1/submissions/"+id+"/appeal", tok, map[string]string{"message": "Please look once more at it."}); r.status != 409 {
		t.Fatalf("second appeal of the same decision: %d", r.status)
	}

	signer := h.registerKey(tok, "koi")
	// EICAR test string (appended to an image): the fake malware scanner flags it.
	infected := append(append([]byte{}, tinyPNG...), eicar...)
	r = h.submit(tok, buildPack(t, "Glass Moth", "aiko", "aiko", "1.0.0", signer, packFile{"refs/side.png", mvpack.RoleReference, infected}))
	if sub := r.json(t); sub["status"] != "rejected" || checkStatuses(t, sub)["malware"] != "failed" {
		t.Fatalf("EICAR not flagged: %s", r.body)
	}
	// Close to a known character: held for a human.
	r = h.submit(tok, buildPack(t, "Captain Placeholder", "aiko", "aiko", "1.0.0", signer))
	sub = r.json(t)
	if sub["status"] != "held" || checkStatuses(t, sub)["known_character"] != "warning" {
		t.Fatalf("known character: %s", r.body)
	}
	// A held submission can be approved by a reviewer.
	if err := h.s.Review(sub["id"].(string), "changes_requested", "Rename the character, please."); err == nil {
		sub = h.get("/v1/submissions/"+sub["id"].(string), tok).json(t)
		if sub["status"] != "changes_requested" || sub["review"].(map[string]any)["message"] != "Rename the character, please." {
			t.Fatalf("changes requested: %v", sub)
		}
	} else {
		t.Fatal(err)
	}
	// Same name as another creator's item: similarity warning.
	r = h.submit(tok, buildPack(t, "Pip the Gull", "aiko", "aiko", "1.0.0", signer))
	if st := checkStatuses(t, r.json(t))["similarity"]; st != "warning" {
		t.Fatalf("similarity: %s", r.body)
	}
}

func TestSubmissionValidation(t *testing.T) {
	h := newHarness(t, Options{Seed: true})
	tok := h.link("aiko")
	signer := h.registerKey(tok, "koi")

	// Credit must match the World name.
	r := h.submit(tok, buildPack(t, "Wrong Credit", "Someone", "someone", "1.0.0", signer))
	if r.status != 422 || !bytes.Contains(r.body, []byte("credit_mismatch")) {
		t.Fatalf("credit mismatch: %d %s", r.status, r.body)
	}
	// Accounts without a World name cannot submit.
	newTok := h.link("new")
	if r := h.submit(newTok, buildPack(t, "No Name", "aiko", "aiko", "1.0.0", nil)); r.status != 409 || r.errorCode(t) != "profile_incomplete" {
		t.Fatalf("profile incomplete: %d %s", r.status, r.body)
	}
	// Missing declarations and pack.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("signed_manifest", `{"manifest":{}}`)
	_ = mw.Close()
	if r := h.do("POST", "/v1/submissions", tok, &body, mw.FormDataContentType()); r.status != 422 {
		t.Fatalf("incomplete upload: %d %s", r.status, r.body)
	}
	// Not multipart.
	wantStatus(t, h.sendJSON("POST", "/v1/submissions", tok, map[string]string{}), 400)
	// A signed manifest that differs from the one in the pack.
	pack := buildPack(t, "Tide Lamp", "aiko", "aiko", "1.0.0", signer)
	other := buildPack(t, "Tide Lamp", "aiko", "aiko", "1.0.1", signer)
	p, _ := mvpack.Open(bytes.NewReader(other), int64(len(other)), nil)
	body.Reset()
	mw = multipart.NewWriter(&body)
	smb, _ := json.Marshal(map[string]any{"manifest": json.RawMessage(p.CanonicalManifest()), "signature": p.Signature()})
	_ = mw.WriteField("signed_manifest", string(smb))
	_ = mw.WriteField("declarations", `{"rights_held":true,"original":true,"ai_label_correct":true}`)
	fw, _ := mw.CreateFormFile("pack", "p.mvpack")
	_, _ = fw.Write(pack)
	_ = mw.Close()
	if r := h.do("POST", "/v1/submissions", tok, &body, mw.FormDataContentType()); r.status != 422 || !bytes.Contains(r.body, []byte("manifest_mismatch")) {
		t.Fatalf("manifest mismatch: %d %s", r.status, r.body)
	}
	// Uploads are size-limited.
	small := newHarness(t, Options{Seed: true, MaxUpload: 1024})
	stok := small.link("aiko")
	big := buildPack(t, "Big One", "aiko", "aiko", "1.0.0", small.registerKey(stok, "koi"), packFile{"data/pad.json", mvpack.RoleData, []byte(`{"pad":"` + strings.Repeat("x", 4096) + `"}`)})
	if r := small.submit(stok, big); r.status != 413 {
		t.Fatalf("oversized upload: %d %s", r.status, r.body)
	}
}

// ---- catalogue and library ----

func TestCatalog(t *testing.T) {
	h := newHarness(t, Options{Seed: true})
	list := func(q string) []map[string]any {
		t.Helper()
		r := h.get("/v1/catalog?"+q, "")
		wantStatus(t, r, 200)
		var out []map[string]any
		for _, it := range r.json(t)["items"].([]any) {
			out = append(out, it.(map[string]any))
		}
		return out
	}
	names := func(items []map[string]any) string {
		var n []string
		for _, it := range items {
			n = append(n, it["name"].(string))
		}
		return strings.Join(n, ",")
	}
	all := list("limit=100")
	if len(all) != len(seedItems) {
		t.Fatalf("want %d items, got %d", len(seedItems), len(all))
	}
	if got := names(list("kind=character&q=gull")); got != "Pip the Gull" {
		t.Fatalf("search: %s", got)
	}
	if got := names(list("kind=template")); got != "Night Market layout,Rooftop chase" {
		t.Fatalf("templates include layouts: %s", got)
	}
	for _, it := range list("hand_drawn_only=true") {
		if it["hand_drawn"] != true {
			t.Fatalf("hand-drawn filter let %s through", it["name"])
		}
	}
	for _, it := range list("licence=CC0-1.0,CC-BY-4.0") {
		if l := it["licence"]; l != "CC0-1.0" && l != "CC-BY-4.0" {
			t.Fatalf("licence filter let %s through", l)
		}
	}
	if got := names(list("works_with=flux2-klein")); got != "Lantern Warden" {
		t.Fatalf("works_with: %s", got)
	}
	if got := list("availability=paid"); len(got) != 3 {
		t.Fatalf("paid items: %s", names(got))
	}

	// Cursor pagination walks every item exactly once.
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		r := h.get("/v1/catalog?limit=4"+cursor, "")
		m := r.json(t)
		for _, it := range m["items"].([]any) {
			id := it.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatalf("item %s returned twice", id)
			}
			seen[id] = true
		}
		next, _ := m["next_cursor"].(string)
		if next == "" {
			break
		}
		cursor = "&cursor=" + next
		if pages > 10 {
			t.Fatal("pagination does not end")
		}
	}
	if len(seen) != len(seedItems) {
		t.Fatalf("pagination saw %d items", len(seen))
	}

	for _, q := range []string{"kind=comic", "limit=0", "cursor=zz", "hand_drawn_only=maybe", "licence=GPL-3.0", "sort=cheapest"} {
		if r := h.get("/v1/catalog?"+q, ""); r.status != 400 || r.errorCode(t) != "bad_request" {
			t.Errorf("%s: %d %s", q, r.status, r.body)
		}
	}
	wantStatus(t, h.get("/v1/catalog/wi_nope", ""), 404)
	detail := h.get("/v1/catalog/wi_rin_asagiri", "").json(t)
	if len(detail["versions"].([]any)) != 3 || detail["from_series"] != "Tidebreak Courier" {
		t.Fatalf("detail: %v", detail)
	}
}

// TestNoPriceFields walks every catalogue and library response and fails on
// anything that looks like a price (product rule 1).
func TestNoPriceFields(t *testing.T) {
	h := newHarness(t, Options{Seed: true})
	tok := h.link("jerry-ink")
	forbidden := []string{"price", "currency", "amount", "cost", "checkout", "buy", "purchase"}
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				for _, f := range forbidden {
					if strings.Contains(strings.ToLower(k), f) {
						t.Errorf("%s has field %q", path, k)
					}
				}
				walk(path+"."+k, val)
			}
		case []any:
			for _, val := range x {
				walk(path, val)
			}
		}
	}
	for _, p := range []string{"/v1/catalog?limit=100", "/v1/catalog/wi_iron_koi", "/v1/library?limit=100", "/v1/library/wi_iron_koi/download"} {
		r := h.get(p, tok)
		wantStatus(t, r, 200)
		var v any
		_ = json.Unmarshal(r.body, &v)
		walk(p, v)
	}
	page := strings.ToLower(string(h.get("/items/wi_iron_koi", "").body))
	for _, f := range []string{"price", "checkout", "buy now", "$", "€"} {
		if strings.Contains(page, f) {
			t.Errorf("item page contains %q", f)
		}
	}
	if !strings.Contains(page, "paid · on world") {
		t.Error("paid items are labelled Paid · on World")
	}
}

func TestLibraryAndDownload(t *testing.T) {
	h := newHarness(t, Options{Seed: true})
	tok := h.link("jerry-ink")
	r := h.get("/v1/library?limit=100", tok)
	wantStatus(t, r, 200)
	lib := r.json(t)
	items := lib["items"].([]any)
	// Jerry owns his five items plus four acquired ones.
	if len(items) != 9 || lib["counts"].(map[string]any)["all"].(float64) != 9 {
		t.Fatalf("library has %d items", len(items))
	}
	var signalFox map[string]any
	for _, it := range items {
		e := it.(map[string]any)
		claims, err := mvpack.ParseLicenceToken(e["licence_token"].(string), h.s.Keys())
		if err != nil {
			t.Fatalf("licence token: %v", err)
		}
		item := e["item"].(map[string]any)
		if claims.ItemID != item["id"] || claims.Handle != "jerry-ink" || item["availability"] != "owned" {
			t.Fatalf("token/item mismatch: %+v %v", claims, item)
		}
		if item["id"] == "wi_signal_fox" {
			signalFox = e
		}
	}
	if signalFox["acquired_version"] != "1.2.0" || signalFox["latest_version"] != "1.3.0" || signalFox["acquisition"] != "paid" {
		t.Fatalf("paid item acquired on World keeps its version info: %v", signalFox)
	}
	if n := len(h.get("/v1/library?kind=character", tok).json(t)["items"].([]any)); n != 5 {
		t.Fatalf("character filter: %d", n)
	}

	// Download: short-lived URL + licence token + a World-signed pack.
	r = h.get("/v1/library/wi_signal_fox/download?version=1.2.0", tok)
	wantStatus(t, r, 200)
	dl := r.json(t)
	pk := fetchPack(t, dl["url"].(string))
	sum := sha256.Sum256(pk)
	if dl["sha256"] != hexString(sum[:]) || int(dl["size"].(float64)) != len(pk) || dl["version"] != "1.2.0" {
		t.Fatalf("download metadata does not match the file: %v", dl)
	}
	p, err := mvpack.Open(bytes.NewReader(pk), int64(len(pk)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Verify(mvpack.VerifyOptions{Keys: h.s.Keys(), RequireSigner: mvpack.SignerWorld}); err != nil {
		t.Fatalf("World signature: %v", err)
	}
	if p.Manifest().Credit.Handle != "kestrel" {
		t.Fatalf("credit: %+v", p.Manifest().Credit)
	}
	if _, err := mvpack.ParseLicenceToken(dl["licence_token"].(string), h.s.Keys()); err != nil {
		t.Fatal(err)
	}
	// The link expires.
	h.clock.Advance(downloadTTL + time.Second)
	if res, _ := http.Get(dl["url"].(string)); res.StatusCode != http.StatusGone {
		t.Fatalf("expired link: %d", res.StatusCode)
	}

	wantStatus(t, h.get("/v1/library/wi_glass_orchard_wash/download", tok), 403)
	wantStatus(t, h.get("/v1/library/wi_rin_asagiri/download?version=9.9.9", tok), 404)

	// Claiming: free items join the library, paid ones point to World.
	other := h.link("kestrel")
	r = h.sendJSON("POST", "/v1/library", other, map[string]string{"item_id": "wi_pip_the_gull"})
	wantStatus(t, r, 201)
	if r.json(t)["acquisition"] != "free" {
		t.Fatalf("claim: %s", r.body)
	}
	wantStatus(t, h.sendJSON("POST", "/v1/library", other, map[string]string{"item_id": "wi_pip_the_gull"}), 200)
	r = h.sendJSON("POST", "/v1/library", other, map[string]string{"item_id": "wi_iron_koi"})
	if r.status != 403 || r.errorCode(t) != "get_on_world" {
		t.Fatalf("paid claim: %d %s", r.status, r.body)
	}
	wantStatus(t, h.sendJSON("POST", "/v1/library", other, map[string]string{"item_id": "wi_nope"}), 404)
}

func hexString(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 2*len(b))
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&15])
	}
	return string(out)
}

// ---- cross-cutting ----

func TestRateLimitAndErrors(t *testing.T) {
	h := newHarness(t, Options{Seed: true, RateLimit: 3})
	for i := 0; i < 3; i++ {
		r := h.get("/v1/meta", "")
		wantStatus(t, r, 200)
		if r.header.Get("RateLimit-Limit") != "3" || r.header.Get("X-World-Stub") != "true" {
			t.Fatalf("headers: %v", r.header)
		}
	}
	r := h.get("/v1/meta", "")
	if r.status != 429 || r.header.Get("Retry-After") == "" || r.header.Get("RateLimit-Remaining") != "0" || r.errorCode(t) != "rate_limited" {
		t.Fatalf("rate limit: %d %v %s", r.status, r.header, r.body)
	}
	h.clock.Advance(time.Minute)
	wantStatus(t, h.get("/v1/meta", ""), 200)
	// Browser pages are not rate limited.
	wantStatus(t, h.get("/_stub", ""), 200)

	h2 := newHarness(t, Options{Seed: true})
	if r := h2.get("/v1/unknown", ""); r.status != 404 || r.errorCode(t) != "not_found" {
		t.Fatalf("unknown endpoint: %d %s", r.status, r.body)
	}
	r = h2.get("/v1/me", "")
	if r.status != 401 || !strings.HasPrefix(r.header.Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("missing token: %d %v", r.status, r.header)
	}
	meta := h2.get("/v1/meta", "").json(t)
	if meta["stub"] != true || !strings.Contains(meta["name"].(string), "not the real platform") {
		t.Fatalf("meta must say it is a stub: %v", meta)
	}
	keys := h2.get("/v1/keys", "").json(t)["keys"].([]any)
	if len(keys) != 2 {
		t.Fatalf("keys: %v", keys)
	}
}

func TestStubPages(t *testing.T) {
	h := newHarness(t, Options{Seed: true, CheckDelay: -1})
	tok := h.link("aiko")
	r := h.submit(tok, buildPack(t, "Harbour Kite", "aiko", "aiko", "1.0.0", h.registerKey(tok, "koi")))
	id := r.json(t)["id"].(string)
	// A pending device code shows up on the control panel.
	d := h.form("/oauth/device_authorization", url.Values{"client_id": {clientID}}).json(t)

	page := h.get("/_stub", "")
	wantStatus(t, page, 200)
	for _, want := range []string{"not the real World platform", id, "Harbour Kite", d["user_code"].(string), "Rin Asagiri", "Paid · on World", "@jerry-ink"} {
		if !bytes.Contains(page.body, []byte(want)) {
			t.Errorf("control panel misses %q", want)
		}
	}
	if page.header.Get("Content-Security-Policy") == "" {
		t.Error("pages carry a CSP")
	}
	// Review through the HTML form.
	r = h.form("/_stub/review", url.Values{"submission_id": {id}, "decision": {"rejected"}, "message": {"Please add a side view."}})
	if r.status != 303 {
		t.Fatalf("form review: %d %s", r.status, r.body)
	}
	if st := h.get("/v1/submissions/"+id, tok).json(t)["status"]; st != "rejected" {
		t.Fatalf("status after form review: %v", st)
	}
	wantStatus(t, h.form("/_stub/review", url.Values{"submission_id": {"sub_nope"}, "decision": {"approved"}}), 404)
	if r := h.sendJSON("POST", "/_stub/review", "", map[string]string{"submission_id": id, "decision": "maybe"}); r.status != 422 {
		t.Fatalf("bad decision: %d", r.status)
	}

	item := h.get("/items/wi_rin_asagiri", "")
	wantStatus(t, item, 200)
	if !bytes.Contains(item.body, []byte("Character by Jerry Ink (@jerry-ink), CC BY 4.0")) {
		t.Error("item page shows the credit line")
	}
	wantStatus(t, h.get("/items/wi_nope", ""), 404)
	cover := h.get("/_stub/covers/wi_rin_asagiri.png", "")
	if cover.status != 200 || !bytes.HasPrefix(cover.body, []byte("\x89PNG")) {
		t.Fatalf("cover: %d", cover.status)
	}
	if r := h.get("/", ""); r.status != 302 || r.header.Get("Location") != "/_stub" {
		t.Fatalf("root: %d", r.status)
	}
}

func TestSeedPacksAreValid(t *testing.T) {
	h := newHarness(t, Options{Seed: true})
	for _, id := range h.s.itemOrder {
		it := h.s.items[id]
		for _, v := range it.Versions {
			p, err := mvpack.Open(bytes.NewReader(v.Pack), int64(len(v.Pack)), nil)
			if err != nil {
				t.Fatalf("%s %s: %v", id, v.Version, err)
			}
			res, err := p.Verify(mvpack.VerifyOptions{Keys: h.s.Keys(), RequireSigner: mvpack.SignerWorld})
			if err != nil {
				t.Fatalf("%s %s: %v", id, v.Version, err)
			}
			if len(res.Signatures) != 2 || res.Signatures[0].Signer != mvpack.SignerServer {
				t.Fatalf("%s: want server + World signatures, got %+v", id, res.Signatures)
			}
			if p.Manifest().Credit.WorldItemID != id {
				t.Fatalf("%s: credit.world_item_id = %q", id, p.Manifest().Credit.WorldItemID)
			}
		}
	}
	// Seeding is deterministic apart from random ids: two stubs publish the same bytes.
	h2 := newHarness(t, Options{Seed: true})
	if !bytes.Equal(h.s.items["wi_rin_asagiri"].latest().Pack, h2.s.items["wi_rin_asagiri"].latest().Pack) {
		t.Error("seed packs should be reproducible")
	}
}
