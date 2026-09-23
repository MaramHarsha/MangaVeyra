package netguard_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core/netguard"
)

func TestCheckIP(t *testing.T) {
	strict := netguard.Policy{}
	private := netguard.Policy{AllowPrivate: true}
	cases := []struct {
		ip              string
		strict, private bool // allowed?
	}{
		{"93.184.216.34", true, true},
		{"2606:4700:4700::1111", true, true},
		{"127.0.0.1", false, true},
		{"10.1.2.3", false, true},
		{"172.20.0.5", false, true},
		{"192.168.1.10", false, true},
		{"100.64.0.1", false, true},
		{"::1", false, true},
		{"fd12:3456::1", false, true},
		{"169.254.169.254", false, false}, // cloud metadata
		{"169.254.10.1", false, false},
		{"fe80::1", false, false},
		{"fd00:ec2::254", false, false},
		{"100.100.100.200", false, false},
		{"0.0.0.0", false, false},
		{"::", false, false},
		{"224.0.0.1", false, false},
		{"255.255.255.255", false, false},
		{"::ffff:169.254.169.254", false, false}, // IPv4-mapped metadata
		{"::ffff:127.0.0.1", false, true},
		{"64:ff9b::a9fe:a9fe", false, false}, // NAT64 → 169.254.169.254
		{"2002:a9fe:a9fe::1", false, false},  // 6to4 → 169.254.169.254
		{"2002:0a00:0001::1", false, true},   // 6to4 → 10.0.0.1
	}
	for _, c := range cases {
		ip := netip.MustParseAddr(c.ip)
		if got := strict.CheckIP(ip) == nil; got != c.strict {
			t.Errorf("strict %s: allowed=%v, want %v", c.ip, got, c.strict)
		}
		if got := private.CheckIP(ip) == nil; got != c.private {
			t.Errorf("allow-private %s: allowed=%v, want %v", c.ip, got, c.private)
		}
	}
}

type fakeResolver map[string][]netip.Addr

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	return f[host], nil
}

func TestCheckURL(t *testing.T) {
	ctx := context.Background()
	r := fakeResolver{
		"push.example.org": {netip.MustParseAddr("93.184.216.34")},
		"rebind.example":   {netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("169.254.169.254")},
		"lan.example":      {netip.MustParseAddr("192.168.1.20")},
	}
	p := netguard.Policy{}
	ok := []string{"https://push.example.org/send/abc", "https://93.184.216.34/x"}
	for _, u := range ok {
		if _, err := p.CheckURL(ctx, u, r); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	bad := []string{
		"http://push.example.org/", "ftp://push.example.org/", "https://user:pw@push.example.org/",
		"https://169.254.169.254/latest/meta-data", "https://[::1]:8080/", "https://localhost/",
		"https://ollama.local/", "https://rebind.example/", "https://lan.example/", "https:///nohost", "file:///etc/passwd",
	}
	for _, u := range bad {
		if _, err := p.CheckURL(ctx, u, r); err == nil {
			t.Errorf("%s accepted", u)
		}
	}
	lan := netguard.Policy{AllowPrivate: true, AllowHTTP: true}
	if _, err := lan.CheckURL(ctx, "http://lan.example:11434/v1", r); err != nil {
		t.Errorf("allow-private policy refused a LAN URL: %v", err)
	}
	if _, err := lan.CheckURL(ctx, "http://169.254.169.254/", r); !errors.Is(err, netguard.ErrBlocked) {
		t.Errorf("metadata must stay blocked with private networks allowed: %v", err)
	}
}

// TestClientBlocksAtDialTime: a name that passed CheckURL but resolves to a
// blocked address at connect time (DNS rebinding) is refused by the dialer.
func TestClientBlocksAtDialTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("internal")) }))
	defer srv.Close()
	c := netguard.Policy{AllowHTTP: true}.Client(5 * time.Second)
	_, err := c.Get(srv.URL) // 127.0.0.1
	if err == nil || !errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("loopback request not blocked: %v", err)
	}
	// Redirects into a blocked range are refused too.
	open := netguard.Policy{AllowHTTP: true, AllowPrivate: true}.Client(5 * time.Second)
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data", http.StatusFound)
	}))
	defer redir.Close()
	_, err = open.Get(redir.URL)
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("redirect to metadata not blocked: %v", err)
	}
	resp, err := open.Get(srv.URL)
	if err != nil {
		t.Fatalf("allow-private client: %v", err)
	}
	resp.Body.Close()
}
