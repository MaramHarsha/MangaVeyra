// Package stub is a DEVELOPMENT-ONLY, in-memory fake of the World public API
// (sdk/api/world.openapi.yaml). It is NOT the real World platform: it has no
// persistence, no real review, no malware scanning and no payments of any kind.
// It exists so the app's World module can be tested and developed locally.
//
// Use it in tests with httptest:
//
//	s, _ := stub.New(stub.Options{Seed: true, AutoApprove: true})
//	ts := httptest.NewServer(s)
//	s.SetPublicURL(ts.URL)
//
// or run it as a binary with `go run ./cmd/worldstub`.
package stub

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MaramHarsha/MangaVeyra/sdk/mvpack"
)

// Options configures the stub.
type Options struct {
	// PublicURL is the base URL clients use to reach the stub (verification
	// URIs, download URLs, item pages). Defaults to http://127.0.0.1:8090.
	PublicURL string
	// Seed loads demo accounts, catalogue items and libraries (original sample
	// content only).
	Seed bool
	// AutoApprove approves every device code as soon as it is issued, as
	// AutoApproveAs, so tests and local development need no browser step.
	AutoApprove bool
	// AutoApproveAs is the handle of the seeded account used by AutoApprove, or
	// "new" to create a fresh account without a World name. Defaults to
	// "jerry-ink" when seeded and "new" otherwise.
	AutoApproveAs string
	// CheckDelay is how long automated checks stay "pending". Zero means the
	// default of 2s; a negative value completes them immediately.
	CheckDelay time.Duration
	// ReviewDelay approves submissions whose checks passed after this long.
	// Zero means only manual review (POST /_stub/review or the /_stub page).
	ReviewDelay time.Duration
	// PollInterval is the device-flow polling interval. Default 5s.
	PollInterval time.Duration
	// RateLimit is the number of API requests allowed per client per minute. Default 600.
	RateLimit int
	// MaxUpload bounds submission uploads. Default 256 MiB.
	MaxUpload int64
	// Now returns the current time; tests inject a fake clock. Default time.Now.
	Now func() time.Time
	// Logger receives one line per request. Default: discard.
	Logger *slog.Logger
}

// Server is the stub. It implements http.Handler.
type Server struct {
	opts Options
	mux  *http.ServeMux

	packKeyID, licenceKeyID string
	packKey, licenceKey     ed25519.PrivateKey

	mu            sync.Mutex
	publicURL     string
	accounts      map[string]*account
	handles       map[string]string        // current handle → account id
	prevHandles   map[string]*handleRecord // previous handle → record
	devices       map[string]*deviceAuth   // device code → auth
	userCodes     map[string]string        // user code → device code
	tokens        map[string]*token        // access token → token
	refresh       map[string]*token        // refresh token → token
	servers       map[string]*serverKey    // key id → key
	submissions   map[string]*submission
	subOrder      []string // newest first
	idempotency   map[string]string
	items         map[string]*item
	itemOrder     []string
	library       map[string]map[string]*libEntry // account id → item id → entry
	downloads     map[string]*download
	rate          map[string]*rateWindow
	blockedNames  map[string]bool
	knownCharName []string
}

// Stub defaults.
const (
	DefaultAddr      = "127.0.0.1:8090"
	defaultPublicURL = "http://" + DefaultAddr
	clientID         = "mangaveyra-server"
	accessTTL        = time.Hour
	deviceTTL        = 10 * time.Minute
	downloadTTL      = 5 * time.Minute
	handleCooldown   = 30 * 24 * time.Hour
	handleReserve    = 183 * 24 * time.Hour // six months
)

// New creates a stub server.
func New(opts Options) (*Server, error) {
	if opts.PublicURL == "" {
		opts.PublicURL = defaultPublicURL
	}
	switch {
	case opts.CheckDelay == 0:
		opts.CheckDelay = 2 * time.Second
	case opts.CheckDelay < 0:
		opts.CheckDelay = 0
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = 5 * time.Second
	}
	if opts.RateLimit == 0 {
		opts.RateLimit = 600
	}
	if opts.MaxUpload == 0 {
		opts.MaxUpload = 256 << 20
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.AutoApproveAs == "" {
		opts.AutoApproveAs = "new"
		if opts.Seed {
			opts.AutoApproveAs = "jerry-ink"
		}
	}
	s := &Server{
		opts:         opts,
		publicURL:    strings.TrimRight(opts.PublicURL, "/"),
		packKeyID:    "world-stub-pack-2026",
		licenceKeyID: "world-stub-licence-2026",
		packKey:      devKey("pack-signing"),
		licenceKey:   devKey("licence-tokens"),
		accounts:     map[string]*account{},
		handles:      map[string]string{},
		prevHandles:  map[string]*handleRecord{},
		devices:      map[string]*deviceAuth{},
		userCodes:    map[string]string{},
		tokens:       map[string]*token{},
		refresh:      map[string]*token{},
		servers:      map[string]*serverKey{},
		submissions:  map[string]*submission{},
		idempotency:  map[string]string{},
		items:        map[string]*item{},
		library:      map[string]map[string]*libEntry{},
		downloads:    map[string]*download{},
		rate:         map[string]*rateWindow{},
		blockedNames: map[string]bool{},
		// Stub-only trigger names for the known_character check. They are
		// deliberately generic placeholders, not real characters.
		knownCharName: []string{"Captain Placeholder", "Famous Test Hero", "Sample Mascot"},
	}
	for _, n := range s.knownCharName {
		s.blockedNames[slugify(n)] = true
		s.blockedNames[nameSkeleton(slugify(n))] = true
	}
	if opts.AutoApprove && opts.AutoApproveAs != "new" && !opts.Seed {
		return nil, fmt.Errorf("worldstub: --auto-approve-as %q needs --seed (or use \"new\")", opts.AutoApproveAs)
	}
	if opts.Seed {
		if err := s.seed(); err != nil {
			return nil, err
		}
		if opts.AutoApprove && opts.AutoApproveAs != "new" {
			if _, ok := s.handles[opts.AutoApproveAs]; !ok {
				return nil, fmt.Errorf("worldstub: no seeded account with handle %q", opts.AutoApproveAs)
			}
		}
	}
	s.routes()
	return s, nil
}

// devKey derives a fixed, PUBLICLY KNOWN development key. Anything signed with
// it proves nothing outside local development.
func devKey(label string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("mangaveyra worldstub DEV ONLY key: " + label))
	return ed25519.NewKeyFromSeed(seed[:])
}

// SetPublicURL changes the base URL used in generated links (handy with httptest).
func (s *Server) SetPublicURL(u string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publicURL = strings.TrimRight(u, "/")
}

// PackKey returns the stub's World pack-signing key id and public key.
func (s *Server) PackKey() (string, ed25519.PublicKey) {
	return s.packKeyID, s.packKey.Public().(ed25519.PublicKey)
}

// LicenceKey returns the stub's licence-token key id and public key.
func (s *Server) LicenceKey() (string, ed25519.PublicKey) {
	return s.licenceKeyID, s.licenceKey.Public().(ed25519.PublicKey)
}

// Keys returns a resolver for both World keys (role "world"), for verifying packs and tokens.
func (s *Server) Keys() mvpack.RoleKeySet {
	return mvpack.RoleKeySet{
		s.packKeyID:    {Public: s.packKey.Public().(ed25519.PublicKey), Role: mvpack.SignerWorld},
		s.licenceKeyID: {Public: s.licenceKey.Public().(ed25519.PublicKey), Role: mvpack.SignerWorld},
	}
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := s.opts.Now()
	w.Header().Set("X-World-Stub", "true")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	rec := &statusRecorder{ResponseWriter: w, status: 200}
	if isAPI(r.URL.Path) {
		if !s.allow(rec, r) {
			return
		}
	}
	s.mux.ServeHTTP(rec, r)
	s.opts.Logger.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status, "dur", s.opts.Now().Sub(start))
}

func isAPI(p string) bool { return strings.HasPrefix(p, "/v1/") || strings.HasPrefix(p, "/oauth/") }

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) routes() {
	m := http.NewServeMux()
	m.HandleFunc("POST /oauth/device_authorization", s.handleDeviceAuthorization)
	m.HandleFunc("POST /oauth/token", s.handleToken)
	m.HandleFunc("POST /oauth/revoke", s.handleRevoke)
	m.HandleFunc("GET /v1/meta", s.handleMeta)
	m.HandleFunc("GET /v1/keys", s.handleKeys)
	m.HandleFunc("GET /v1/me", s.handleMe)
	m.HandleFunc("PATCH /v1/me/profile", s.handleProfile)
	m.HandleFunc("GET /v1/handles/{handle}", s.handleHandle)
	m.HandleFunc("GET /v1/servers", s.handleListServers)
	m.HandleFunc("POST /v1/servers", s.handleRegisterServer)
	m.HandleFunc("DELETE /v1/servers/{id}", s.handleRevokeServer)
	m.HandleFunc("GET /v1/submissions", s.handleListSubmissions)
	m.HandleFunc("POST /v1/submissions", s.handleCreateSubmission)
	m.HandleFunc("GET /v1/submissions/{id}", s.handleGetSubmission)
	m.HandleFunc("POST /v1/submissions/{id}/appeal", s.handleAppeal)
	m.HandleFunc("GET /v1/catalog", s.handleCatalog)
	m.HandleFunc("GET /v1/catalog/{id}", s.handleCatalogItem)
	m.HandleFunc("GET /v1/library", s.handleLibrary)
	m.HandleFunc("POST /v1/library", s.handleClaim)
	m.HandleFunc("GET /v1/library/{id}/download", s.handleDownload)
	m.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "No such endpoint in World API v1.")
	})

	// Browser pages (the human side of the device flow, item and profile pages)
	// and stub-only controls.
	m.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/_stub", http.StatusFound) })
	m.HandleFunc("GET /device", s.handleDevicePage)
	// Browser form posts are protected against cross-site requests; non-browser
	// clients (tests, curl) send no Origin or Sec-Fetch-Site and pass.
	csrf := http.NewCrossOriginProtection()
	m.Handle("POST /device", csrf.Handler(http.HandlerFunc(s.handleDeviceDecision)))
	m.HandleFunc("GET /items/{id}", s.handleItemPage)
	m.HandleFunc("GET /{at}", s.handleProfilePage) // /@handle
	m.HandleFunc("GET /_stub", s.handleStubIndex)
	m.Handle("POST /_stub/review", csrf.Handler(http.HandlerFunc(s.handleStubReview)))
	m.HandleFunc("GET /_stub/covers/{file}", s.handleCover)
	m.HandleFunc("GET /_stub/downloads/{token}/{name}", s.handleDownloadFile)
	s.mux = m
}

// ---- JSON helpers ----

type apiError struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string        `json:"code"`
	Message string        `json:"message"`
	Details []errorDetail `json:"details,omitempty"`
}

type errorDetail struct {
	Field   string `json:"field,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Value   string `json:"value,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string, details ...errorDetail) {
	writeJSON(w, status, apiError{Error: errorBody{Code: code, Message: msg, Details: details}})
}

func writeOAuthError(w http.ResponseWriter, code, desc string) {
	w.Header().Set("Cache-Control", "no-store")
	status := http.StatusBadRequest
	if code == "invalid_client" {
		status = http.StatusUnauthorized
	}
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

// decodeStrict decodes a bounded JSON body and rejects unknown fields.
func decodeStrict(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// ---- rate limiting ----

type rateWindow struct {
	start time.Time
	count int
}

// allow applies a fixed one-minute window per client (bearer token or IP) and
// sets the RateLimit-* headers.
func (s *Server) allow(w http.ResponseWriter, r *http.Request) bool {
	key := "ip:" + clientIP(r)
	if tok := bearer(r); tok != "" {
		key = "tok:" + tok
	}
	now := s.opts.Now()
	s.mu.Lock()
	win := s.rate[key]
	if win == nil || now.Sub(win.start) >= time.Minute {
		win = &rateWindow{start: now}
		s.rate[key] = win
	}
	win.count++
	count, reset := win.count, int(time.Minute-now.Sub(win.start)+time.Second-1)/int(time.Second)
	s.mu.Unlock()
	limit := s.opts.RateLimit
	h := w.Header()
	h.Set("RateLimit-Limit", strconv.Itoa(limit))
	h.Set("RateLimit-Remaining", strconv.Itoa(max(0, limit-count)))
	h.Set("RateLimit-Reset", strconv.Itoa(reset))
	if count > limit {
		h.Set("Retry-After", strconv.Itoa(reset))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many requests. Try again in a moment.")
		return false
	}
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func bearer(r *http.Request) string {
	v := r.Header.Get("Authorization")
	if len(v) > 7 && strings.EqualFold(v[:7], "bearer ") {
		return strings.TrimSpace(v[7:])
	}
	return ""
}

// ---- ids ----

var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func randomID(prefix string, n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return prefix + idEncoding.EncodeToString(b)
}

// slugify lowercases s and keeps a–z/0–9 separated by single hyphens.
func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

var errNotFound = errors.New("not found")

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func tsPtr(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	v := ts(t)
	return &v
}
