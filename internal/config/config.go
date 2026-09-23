// Package config loads the MangaVeyra server configuration.
//
// Configuration comes from three layers, later layers winning:
//
//  1. built-in defaults (SQLite in ./data, local storage, :8080),
//  2. an optional YAML file (mangaveyra serve --config mangaveyra.yaml),
//  3. MV_* environment variables (documented in .env.example).
//
// The data directory layout is:
//
//	<data>/mangaveyra.db   SQLite database (when MV_DB is not set)
//	<data>/media/          local media storage (when MV_STORAGE=local)
//	<data>/secret.key      auto-generated master key (when MV_MASTER_KEY is not set)
//	<data>/manifests/      model manifest overrides
//	<data>/locales/        community interface translations (optional)
//	<data>/backups/        backup archives
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// DefaultWorldURL is the public World hub the app links to when World is enabled by an admin.
const DefaultWorldURL = "https://world.mangaveyra.org"

// Config is the complete server configuration.
type Config struct {
	// Addr is the listen address of the HTTP server (MV_ADDR), e.g. ":8080".
	Addr string `yaml:"addr"`
	// DataDir holds the SQLite database, local media, the secret key and backups (MV_DATA_DIR).
	DataDir string `yaml:"data_dir"`
	// DB is the database URL (MV_DB): sqlite:///abs/path.db, sqlite://relative.db or postgres://user:pass@host/db.
	// Empty means SQLite at <DataDir>/mangaveyra.db.
	DB string `yaml:"db"`
	// PublicURL is the externally reachable base URL (MV_PUBLIC_URL), used for links in emails,
	// signed media URLs handed to workers and the CSRF trusted origin.
	PublicURL string `yaml:"public_url"`
	// MasterKey encrypts secrets at rest (MV_MASTER_KEY). Empty means <DataDir>/secret.key is used (created on first run).
	MasterKey string `yaml:"master_key"`
	// Storage selects the media store (MV_STORAGE): "local" or "s3".
	Storage string `yaml:"storage"`
	// S3 configures the S3-compatible store when Storage is "s3".
	S3 S3Config `yaml:"s3"`
	// SMTP configures outgoing email. Email is disabled when SMTP.Host is empty.
	SMTP SMTPConfig `yaml:"smtp"`
	// WorkerToken is a shared secret that lets docker-compose workers pair without an admin-issued token (MV_WORKER_TOKEN).
	WorkerToken string `yaml:"worker_token"`
	// WorldURL is the base URL of the World hub (MV_WORLD_URL). World stays off until an admin links the server.
	WorldURL string `yaml:"world_url"`
	// LogLevel is one of debug, info, warn, error (MV_LOG_LEVEL).
	LogLevel string `yaml:"log_level"`
	// Dev enables development conveniences: text logs, verbose errors, no-cache static files (MV_DEV).
	Dev bool `yaml:"dev"`
	// TrustedProxies lists proxy IPs or CIDRs whose X-Forwarded-For / X-Forwarded-Proto headers are honoured (MV_TRUSTED_PROXIES, comma-separated).
	TrustedProxies []string `yaml:"trusted_proxies"`
	// AllowPrivateProviderURLs lets users point providers at private or loopback addresses (MV_ALLOW_PRIVATE_PROVIDER_URLS).
	// Link-local and cloud metadata ranges stay blocked regardless.
	AllowPrivateProviderURLs bool `yaml:"allow_private_provider_urls"`
	// MaxUploadMB caps a single upload in megabytes (MV_MAX_UPLOAD_MB).
	MaxUploadMB int `yaml:"max_upload_mb"`
	// ShutdownTimeout bounds graceful shutdown (MV_SHUTDOWN_TIMEOUT, Go duration).
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

// S3Config configures an S3-compatible object store (AWS S3, MinIO, Cloudflare R2, Backblaze B2, Garage...).
type S3Config struct {
	Endpoint  string `yaml:"endpoint"`   // MV_S3_ENDPOINT, host[:port] without scheme, e.g. "s3.amazonaws.com" or "minio:9000"
	Region    string `yaml:"region"`     // MV_S3_REGION
	Bucket    string `yaml:"bucket"`     // MV_S3_BUCKET
	AccessKey string `yaml:"access_key"` // MV_S3_ACCESS_KEY
	SecretKey string `yaml:"secret_key"` // MV_S3_SECRET_KEY
	UseSSL    bool   `yaml:"use_ssl"`    // MV_S3_USE_SSL (default true)
	PathStyle bool   `yaml:"path_style"` // MV_S3_PATH_STYLE: force path-style addressing (MinIO)
	Prefix    string `yaml:"prefix"`     // MV_S3_PREFIX: optional key prefix inside the bucket
}

// SMTPConfig configures outgoing email.
type SMTPConfig struct {
	Host     string `yaml:"host"`     // MV_SMTP_HOST
	Port     int    `yaml:"port"`     // MV_SMTP_PORT (default 587)
	Username string `yaml:"username"` // MV_SMTP_USERNAME
	Password string `yaml:"password"` // MV_SMTP_PASSWORD
	From     string `yaml:"from"`     // MV_SMTP_FROM, e.g. "MangaVeyra <noreply@example.org>"
	// TLS is "starttls" (default), "tls" (implicit TLS, port 465) or "none" (local relays only).
	TLS string `yaml:"tls"` // MV_SMTP_TLS
}

// Enabled reports whether an SMTP relay is configured.
func (s SMTPConfig) Enabled() bool { return s.Host != "" }

// Default returns the built-in defaults.
func Default() Config {
	return Config{
		Addr:            ":8080",
		DataDir:         "data",
		Storage:         "local",
		WorldURL:        DefaultWorldURL,
		LogLevel:        "info",
		MaxUploadMB:     50,
		ShutdownTimeout: 20 * time.Second,
		S3:              S3Config{UseSSL: true, Region: "us-east-1"},
		SMTP:            SMTPConfig{Port: 587, TLS: "starttls"},
	}
}

// Load builds the configuration from defaults, the optional YAML file at path and the environment.
// getenv is usually os.Getenv; tests pass a map lookup.
func Load(path string, getenv func(string) string) (*Config, error) {
	c := Default()
	if getenv == nil {
		getenv = os.Getenv
	}
	if path == "" {
		path = getenv("MV_CONFIG")
	}
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("config: read %s: %w", path, err)
		}
		if err := yaml.Unmarshal(b, &c); err != nil {
			return nil, fmt.Errorf("config: parse %s: %w", path, err)
		}
	}
	if err := applyEnv(&c, getenv); err != nil {
		return nil, err
	}
	if err := c.normalize(); err != nil {
		return nil, err
	}
	return &c, nil
}

func applyEnv(c *Config, getenv func(string) string) error {
	var errs []error
	str := func(key string, dst *string) {
		if v, ok := lookup(getenv, key); ok {
			*dst = v
		}
	}
	boolean := func(key string, dst *bool) {
		if v, ok := lookup(getenv, key); ok {
			b, err := strconv.ParseBool(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("config: %s: %q is not a boolean", key, v))
				return
			}
			*dst = b
		}
	}
	integer := func(key string, dst *int) {
		if v, ok := lookup(getenv, key); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("config: %s: %q is not an integer", key, v))
				return
			}
			*dst = n
		}
	}
	str("MV_ADDR", &c.Addr)
	str("MV_DATA_DIR", &c.DataDir)
	str("MV_DB", &c.DB)
	str("MV_PUBLIC_URL", &c.PublicURL)
	str("MV_MASTER_KEY", &c.MasterKey)
	str("MV_STORAGE", &c.Storage)
	str("MV_S3_ENDPOINT", &c.S3.Endpoint)
	str("MV_S3_REGION", &c.S3.Region)
	str("MV_S3_BUCKET", &c.S3.Bucket)
	str("MV_S3_ACCESS_KEY", &c.S3.AccessKey)
	str("MV_S3_SECRET_KEY", &c.S3.SecretKey)
	boolean("MV_S3_USE_SSL", &c.S3.UseSSL)
	boolean("MV_S3_PATH_STYLE", &c.S3.PathStyle)
	str("MV_S3_PREFIX", &c.S3.Prefix)
	str("MV_SMTP_HOST", &c.SMTP.Host)
	integer("MV_SMTP_PORT", &c.SMTP.Port)
	str("MV_SMTP_USERNAME", &c.SMTP.Username)
	str("MV_SMTP_PASSWORD", &c.SMTP.Password)
	str("MV_SMTP_FROM", &c.SMTP.From)
	str("MV_SMTP_TLS", &c.SMTP.TLS)
	str("MV_WORKER_TOKEN", &c.WorkerToken)
	str("MV_WORLD_URL", &c.WorldURL)
	str("MV_LOG_LEVEL", &c.LogLevel)
	boolean("MV_DEV", &c.Dev)
	if v, ok := lookup(getenv, "MV_TRUSTED_PROXIES"); ok {
		c.TrustedProxies = splitList(v)
	}
	boolean("MV_ALLOW_PRIVATE_PROVIDER_URLS", &c.AllowPrivateProviderURLs)
	integer("MV_MAX_UPLOAD_MB", &c.MaxUploadMB)
	if v, ok := lookup(getenv, "MV_SHUTDOWN_TIMEOUT"); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("config: MV_SHUTDOWN_TIMEOUT: %w", err))
		} else {
			c.ShutdownTimeout = d
		}
	}
	return errors.Join(errs...)
}

func lookup(getenv func(string) string, key string) (string, bool) {
	v := strings.TrimSpace(getenv(key))
	return v, v != ""
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (c *Config) normalize() error {
	var errs []error
	if c.DataDir == "" {
		c.DataDir = "data"
	}
	if c.Addr == "" {
		c.Addr = ":8080"
	}
	c.Storage = strings.ToLower(c.Storage)
	switch c.Storage {
	case "", "local":
		c.Storage = "local"
	case "s3":
		if c.S3.Endpoint == "" || c.S3.Bucket == "" {
			errs = append(errs, errors.New("config: MV_STORAGE=s3 needs MV_S3_ENDPOINT and MV_S3_BUCKET"))
		}
		c.S3.Endpoint = strings.TrimPrefix(strings.TrimPrefix(c.S3.Endpoint, "https://"), "http://")
		c.S3.Prefix = strings.Trim(c.S3.Prefix, "/")
	default:
		errs = append(errs, fmt.Errorf("config: MV_STORAGE must be local or s3, got %q", c.Storage))
	}
	c.LogLevel = strings.ToLower(c.LogLevel)
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	case "":
		c.LogLevel = "info"
	default:
		errs = append(errs, fmt.Errorf("config: MV_LOG_LEVEL must be debug, info, warn or error, got %q", c.LogLevel))
	}
	if c.PublicURL != "" {
		u, err := url.Parse(c.PublicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("config: MV_PUBLIC_URL must be an absolute http(s) URL, got %q", c.PublicURL))
		}
		c.PublicURL = strings.TrimRight(c.PublicURL, "/")
	}
	if c.WorldURL == "" {
		c.WorldURL = DefaultWorldURL
	}
	c.WorldURL = strings.TrimRight(c.WorldURL, "/")
	if _, _, err := c.Database(); err != nil {
		errs = append(errs, err)
	}
	for _, p := range c.TrustedProxies {
		if _, err := ParseIPOrCIDR(p); err != nil {
			errs = append(errs, fmt.Errorf("config: MV_TRUSTED_PROXIES: %w", err))
		}
	}
	switch strings.ToLower(c.SMTP.TLS) {
	case "", "starttls":
		c.SMTP.TLS = "starttls"
	case "tls", "none":
		c.SMTP.TLS = strings.ToLower(c.SMTP.TLS)
	default:
		errs = append(errs, fmt.Errorf("config: MV_SMTP_TLS must be starttls, tls or none, got %q", c.SMTP.TLS))
	}
	if c.SMTP.Port == 0 {
		c.SMTP.Port = 587
	}
	if c.MaxUploadMB <= 0 {
		c.MaxUploadMB = 50
	}
	if c.ShutdownTimeout <= 0 {
		c.ShutdownTimeout = 20 * time.Second
	}
	return errors.Join(errs...)
}

// Database returns the driver dialect ("sqlite" or "postgres") and the driver DSN derived from DB.
func (c *Config) Database() (dialect, dsn string, err error) {
	raw := c.DB
	if raw == "" {
		return "sqlite", filepath.Join(c.DataDir, "mangaveyra.db"), nil
	}
	return ParseDatabaseURL(raw)
}

// ParseDatabaseURL splits a MangaVeyra database URL into dialect and driver DSN.
//
//	sqlite:///var/lib/mv/mv.db  → sqlite, /var/lib/mv/mv.db
//	sqlite://data/mv.db          → sqlite, data/mv.db
//	sqlite::memory:              → sqlite, :memory:
//	postgres://u:p@h:5432/db     → postgres, postgres://u:p@h:5432/db
func ParseDatabaseURL(raw string) (dialect, dsn string, err error) {
	switch {
	case strings.HasPrefix(raw, "postgres://"), strings.HasPrefix(raw, "postgresql://"):
		return "postgres", raw, nil
	case strings.HasPrefix(raw, "sqlite://"):
		p := strings.TrimPrefix(raw, "sqlite://")
		if p == "" {
			return "", "", errors.New("config: MV_DB sqlite URL has no path")
		}
		return "sqlite", p, nil
	case strings.HasPrefix(raw, "sqlite:"):
		return "sqlite", strings.TrimPrefix(raw, "sqlite:"), nil
	case strings.HasSuffix(raw, ".db"), strings.HasSuffix(raw, ".sqlite"), strings.HasSuffix(raw, ".sqlite3"):
		return "sqlite", raw, nil
	}
	return "", "", fmt.Errorf("config: MV_DB must start with sqlite:// or postgres://, got %q", redact(raw))
}

func redact(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		u.User = url.User(u.User.Username())
		return u.String()
	}
	return raw
}

// MediaDir is the local media storage root.
func (c *Config) MediaDir() string { return filepath.Join(c.DataDir, "media") }

// SecretKeyPath is the auto-generated master key file.
func (c *Config) SecretKeyPath() string { return filepath.Join(c.DataDir, "secret.key") }

// ManifestDir holds model manifest overrides.
func (c *Config) ManifestDir() string { return filepath.Join(c.DataDir, "manifests") }

// LocaleDir holds optional community interface translations.
func (c *Config) LocaleDir() string { return filepath.Join(c.DataDir, "locales") }

// BackupDir holds backup archives.
func (c *Config) BackupDir() string { return filepath.Join(c.DataDir, "backups") }

// EnsureDataDir creates the data directory tree with private permissions.
func (c *Config) EnsureDataDir() error {
	for _, d := range []string{c.DataDir, c.MediaDir(), c.ManifestDir(), c.BackupDir()} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return fmt.Errorf("config: create %s: %w", d, err)
		}
	}
	return nil
}

// SecureCookies reports whether cookies should carry the Secure attribute by default.
func (c *Config) SecureCookies() bool { return strings.HasPrefix(c.PublicURL, "https://") }

// ParseIPOrCIDR parses "10.0.0.1" or "10.0.0.0/8" into a network.
func ParseIPOrCIDR(s string) (*net.IPNet, error) {
	if strings.Contains(s, "/") {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q", s)
		}
		return n, nil
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return nil, fmt.Errorf("invalid IP %q", s)
	}
	bits := 32
	if ip.To4() == nil {
		bits = 128
	} else {
		ip = ip.To4()
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}, nil
}
