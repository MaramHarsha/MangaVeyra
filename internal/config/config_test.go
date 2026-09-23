package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	c, err := Load("", env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":8080" || c.Storage != "local" || c.WorldURL != DefaultWorldURL {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	d, dsn, err := c.Database()
	if err != nil || d != "sqlite" || dsn != filepath.Join("data", "mangaveyra.db") {
		t.Fatalf("database = %q %q %v", d, dsn, err)
	}
	if c.MediaDir() != filepath.Join("data", "media") {
		t.Fatalf("media dir %q", c.MediaDir())
	}
}

func TestEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mv.yaml")
	if err := os.WriteFile(path, []byte("addr: \":9000\"\nlog_level: debug\nsmtp:\n  host: mail.local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path, env(map[string]string{
		"MV_ADDR":             ":7000",
		"MV_DB":               "postgres://mv:secret@db:5432/mv",
		"MV_TRUSTED_PROXIES":  "10.0.0.0/8, 127.0.0.1",
		"MV_DEV":              "true",
		"MV_SHUTDOWN_TIMEOUT": "5s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":7000" || c.LogLevel != "debug" || !c.SMTP.Enabled() || !c.Dev {
		t.Fatalf("layering broken: %+v", c)
	}
	if len(c.TrustedProxies) != 2 || c.ShutdownTimeout != 5*time.Second {
		t.Fatalf("lists/durations: %+v", c)
	}
	if d, _, _ := c.Database(); d != "postgres" {
		t.Fatalf("dialect %q", d)
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]map[string]string{
		"bad storage":  {"MV_STORAGE": "ftp"},
		"s3 no bucket": {"MV_STORAGE": "s3", "MV_S3_ENDPOINT": "minio:9000"},
		"bad db":       {"MV_DB": "mysql://x"},
		"bad bool":     {"MV_DEV": "maybe"},
		"bad url":      {"MV_PUBLIC_URL": "example.org"},
		"bad proxy":    {"MV_TRUSTED_PROXIES": "not-an-ip"},
		"bad level":    {"MV_LOG_LEVEL": "loud"},
	}
	for name, m := range cases {
		if _, err := Load("", env(m)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestParseDatabaseURL(t *testing.T) {
	cases := []struct{ in, dialect, dsn string }{
		{"sqlite:///var/lib/mv.db", "sqlite", "/var/lib/mv.db"},
		{"sqlite://data/mv.db", "sqlite", "data/mv.db"},
		{"sqlite::memory:", "sqlite", ":memory:"},
		{"postgresql://u@h/db", "postgres", "postgresql://u@h/db"},
		{"./x.db", "sqlite", "./x.db"},
	}
	for _, c := range cases {
		d, dsn, err := ParseDatabaseURL(c.in)
		if err != nil || d != c.dialect || dsn != c.dsn {
			t.Errorf("%s → %q %q %v", c.in, d, dsn, err)
		}
	}
}
