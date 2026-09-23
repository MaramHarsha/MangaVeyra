package stub

import (
	"crypto/rand"
	"net/http"
	"slices"
	"strings"
	"time"
)

// deviceAuth is one pending RFC 8628 authorization.
type deviceAuth struct {
	DeviceCode string
	UserCode   string
	Scope      []string
	ExpiresAt  time.Time
	Interval   time.Duration
	LastPoll   time.Time
	Status     string // pending | approved | denied
	AccountID  string
}

type token struct {
	Access, Refresh string
	AccountID       string
	Scope           []string
	ExpiresAt       time.Time
}

var knownScopes = []string{"profile", "servers", "submit", "library"}

const deviceGrant = "urn:ietf:params:oauth:grant-type:device_code"

// userCodeAlphabet avoids vowels (no accidental words) and look-alike characters.
const userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"

func newUserCode() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	var sb strings.Builder
	for i, c := range b {
		if i == 4 {
			sb.WriteByte('-')
		}
		sb.WriteByte(userCodeAlphabet[int(c)%len(userCodeAlphabet)])
	}
	return sb.String()
}

// normalizeUserCode accepts codes typed in any case, with or without the hyphen.
func normalizeUserCode(s string) string {
	s = strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(s))
	if len(s) != 8 {
		return s
	}
	return s[:4] + "-" + s[4:]
}

func parseScope(raw string) ([]string, bool) {
	if strings.TrimSpace(raw) == "" {
		return slices.Clone(knownScopes), true
	}
	var out []string
	for _, sc := range strings.Fields(raw) {
		if !slices.Contains(knownScopes, sc) {
			return nil, false
		}
		if !slices.Contains(out, sc) {
			out = append(out, sc)
		}
	}
	return out, true
}

func (s *Server) handleDeviceAuthorization(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, "invalid_request", "Body must be application/x-www-form-urlencoded.")
		return
	}
	if r.PostForm.Get("client_id") != clientID {
		writeOAuthError(w, "invalid_client", "client_id must be "+clientID+".")
		return
	}
	scope, ok := parseScope(r.PostForm.Get("scope"))
	if !ok {
		writeOAuthError(w, "invalid_scope", "Known scopes: "+strings.Join(knownScopes, " ")+".")
		return
	}
	now := s.opts.Now()
	s.mu.Lock()
	d := &deviceAuth{
		DeviceCode: randomID("dc_", 24), UserCode: newUserCode(), Scope: scope,
		ExpiresAt: now.Add(deviceTTL), Interval: s.opts.PollInterval, Status: "pending",
	}
	for _, taken := s.userCodes[d.UserCode]; taken; _, taken = s.userCodes[d.UserCode] {
		d.UserCode = newUserCode()
	}
	if s.opts.AutoApprove {
		if a := s.autoApproveAccount(now); a != nil {
			d.Status, d.AccountID = "approved", a.ID
		}
	}
	s.devices[d.DeviceCode] = d
	s.userCodes[d.UserCode] = d.DeviceCode
	base := s.publicURL
	s.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               d.DeviceCode,
		"user_code":                 d.UserCode,
		"verification_uri":          base + "/device",
		"verification_uri_complete": base + "/device?user_code=" + d.UserCode,
		"expires_in":                int(deviceTTL / time.Second),
		"interval":                  int(s.opts.PollInterval / time.Second),
	})
}

// autoApproveAccount returns the account used by --auto-approve; s.mu must be held.
func (s *Server) autoApproveAccount(now time.Time) *account {
	if s.opts.AutoApproveAs == "new" {
		return s.newAccount(now)
	}
	return s.findAccountByHandle(s.opts.AutoApproveAs)
}

// newAccount creates a World account without a World name; s.mu must be held.
func (s *Server) newAccount(now time.Time) *account {
	a := &account{ID: randomID("acc_", 10), DisplayName: "New creator", CreatedAt: now, TwoFactor: true}
	s.accounts[a.ID] = a
	return a
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, "invalid_request", "Body must be application/x-www-form-urlencoded.")
		return
	}
	if r.PostForm.Get("client_id") != clientID {
		writeOAuthError(w, "invalid_client", "client_id must be "+clientID+".")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case deviceGrant:
		s.deviceToken(w, r.PostForm.Get("device_code"))
	case "refresh_token":
		s.refreshToken(w, r.PostForm.Get("refresh_token"))
	default:
		writeOAuthError(w, "unsupported_grant_type", "Use the device code or refresh token grant.")
	}
}

func (s *Server) deviceToken(w http.ResponseWriter, code string) {
	now := s.opts.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[code]
	if !ok {
		writeOAuthError(w, "invalid_grant", "Unknown or already used device code.")
		return
	}
	if now.After(d.ExpiresAt) {
		s.dropDevice(d)
		writeOAuthError(w, "expired_token", "The code expired. Start linking again.")
		return
	}
	if !d.LastPoll.IsZero() && now.Sub(d.LastPoll) < d.Interval {
		d.Interval += 5 * time.Second
		d.LastPoll = now
		writeOAuthError(w, "slow_down", "Polling too fast; wait the interval plus 5 seconds.")
		return
	}
	d.LastPoll = now
	switch d.Status {
	case "pending":
		writeOAuthError(w, "authorization_pending", "Waiting for the code to be approved on World.")
	case "denied":
		s.dropDevice(d)
		writeOAuthError(w, "access_denied", "The request was denied on World.")
	case "approved":
		s.dropDevice(d)
		s.issueToken(w, d.AccountID, d.Scope, now)
	}
}

func (s *Server) dropDevice(d *deviceAuth) {
	delete(s.devices, d.DeviceCode)
	delete(s.userCodes, d.UserCode)
}

// issueToken creates and writes a token pair; s.mu must be held.
func (s *Server) issueToken(w http.ResponseWriter, accountID string, scope []string, now time.Time) {
	t := &token{Access: randomID("wat_", 24), Refresh: randomID("wrt_", 24), AccountID: accountID, Scope: scope, ExpiresAt: now.Add(accessTTL)}
	s.tokens[t.Access] = t
	s.refresh[t.Refresh] = t
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": t.Access, "token_type": "Bearer", "expires_in": int(accessTTL / time.Second),
		"refresh_token": t.Refresh, "scope": strings.Join(scope, " "),
	})
}

func (s *Server) refreshToken(w http.ResponseWriter, rt string) {
	now := s.opts.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.refresh[rt]
	if !ok {
		writeOAuthError(w, "invalid_grant", "Unknown or revoked refresh token.")
		return
	}
	// Refresh tokens rotate: the old pair stops working.
	delete(s.refresh, rt)
	delete(s.tokens, t.Access)
	s.issueToken(w, t.AccountID, t.Scope, now)
}

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil || r.PostForm.Get("token") == "" {
		writeOAuthError(w, "invalid_request", "token is required.")
		return
	}
	tok := r.PostForm.Get("token")
	s.mu.Lock()
	if t, ok := s.tokens[tok]; ok {
		delete(s.tokens, tok)
		delete(s.refresh, t.Refresh)
	}
	if t, ok := s.refresh[tok]; ok {
		delete(s.refresh, tok)
		delete(s.tokens, t.Access)
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// authorize resolves the bearer token and checks its scope, writing a 401 on
// failure; s.mu must be held.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, scope string) (*account, bool) {
	fail := func(msg string) (*account, bool) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="world", scope="`+scope+`"`)
		writeError(w, http.StatusUnauthorized, "unauthorized", msg)
		return nil, false
	}
	raw := bearer(r)
	if raw == "" {
		return fail("Sign in with a World access token.")
	}
	t, ok := s.tokens[raw]
	if !ok || s.opts.Now().After(t.ExpiresAt) {
		return fail("The access token is unknown, revoked or expired.")
	}
	if !slices.Contains(t.Scope, scope) {
		return fail("The token lacks the " + scope + " scope.")
	}
	a := s.accounts[t.AccountID]
	if a == nil {
		return fail("The account no longer exists.")
	}
	return a, true
}

// approveDevice records the person's decision for a user code (browser page or tests).
func (s *Server) approveDevice(userCode, accountRef string, approve bool) (string, error) {
	now := s.opts.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	dc, ok := s.userCodes[normalizeUserCode(userCode)]
	if !ok {
		return "", errNotFound
	}
	d := s.devices[dc]
	if now.After(d.ExpiresAt) {
		s.dropDevice(d)
		return "", errNotFound
	}
	if d.Status != "pending" {
		return "", errNotFound // already approved or denied
	}
	if !approve {
		d.Status = "denied"
		return "", nil
	}
	var a *account
	if accountRef == "new" || accountRef == "" {
		a = s.newAccount(now)
	} else if a = s.findAccountByHandle(accountRef); a == nil {
		a = s.accounts[accountRef]
	}
	if a == nil {
		return "", errNotFound
	}
	d.Status, d.AccountID = "approved", a.ID
	name := a.DisplayName
	if a.Handle != "" {
		name += " (@" + a.Handle + ")"
	}
	return name, nil
}

// ApproveDevice approves a pending user code as the account with the given
// handle ("new" creates an account without a World name). It is the programmatic
// equivalent of the browser page, for tests.
func (s *Server) ApproveDevice(userCode, handle string) error {
	_, err := s.approveDevice(userCode, handle, true)
	return err
}
