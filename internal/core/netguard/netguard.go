// Package netguard is the SSRF guard for every outgoing request to a URL a
// user or admin typed in: AI provider endpoints, ComfyUI / OpenAI-compatible
// servers, web-push endpoints, webhooks and plugin downloads.
//
// Two layers work together:
//
//   - CheckURL validates a URL up front (scheme, host, literal and resolved
//     addresses) so forms can show a clear error when a URL is saved.
//   - Dialer / Transport / Client check the address actually connected to,
//     after DNS resolution and on every redirect hop, so DNS rebinding and
//     redirects to internal hosts cannot slip past the first check.
//
// Link-local ranges (including the 169.254.169.254 cloud metadata service),
// other metadata addresses, unspecified, multicast and reserved addresses
// are always blocked. Loopback and private networks (RFC 1918, ULA, CGNAT)
// are blocked unless the admin allows private networks
// (MV_ALLOW_PRIVATE_PROVIDER_URLS), which self-hosters need for a local
// Ollama or ComfyUI.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// ErrBlocked is returned (wrapped) for URLs and addresses the policy refuses.
var ErrBlocked = errors.New("netguard: destination not allowed")

// Policy decides which destinations are allowed.
type Policy struct {
	// AllowPrivate permits loopback and private-network addresses. Link-local
	// and metadata addresses stay blocked regardless.
	AllowPrivate bool
	// AllowHTTP permits plain http:// URLs (otherwise only https://).
	AllowHTTP bool
}

var (
	alwaysBlocked = prefixes(
		"0.0.0.0/8",          // "this network"
		"169.254.0.0/16",     // IPv4 link-local, incl. 169.254.169.254 metadata
		"100.100.100.200/32", // Alibaba Cloud metadata
		"192.0.0.0/24",       // IETF protocol assignments
		"192.0.2.0/24",       // TEST-NET-1
		"198.51.100.0/24",    // TEST-NET-2
		"203.0.113.0/24",     // TEST-NET-3
		"224.0.0.0/4",        // multicast
		"240.0.0.0/4",        // reserved + broadcast
		"::/128",             // unspecified
		"fe80::/10",          // IPv6 link-local
		"fd00:ec2::254/128",  // AWS IPv6 metadata
		"ff00::/8",           // multicast
		"100::/64",           // discard-only
		"2001:db8::/32",      // documentation
	)
	privateRanges = prefixes(
		"127.0.0.0/8",    // loopback
		"10.0.0.0/8",     // RFC 1918
		"172.16.0.0/12",  // RFC 1918
		"192.168.0.0/16", // RFC 1918
		"100.64.0.0/10",  // carrier-grade NAT
		"198.18.0.0/15",  // benchmarking
		"::1/128",        // loopback
		"fc00::/7",       // unique local
	)
	nat64  = netip.MustParsePrefix("64:ff9b::/96")
	sixTo4 = netip.MustParsePrefix("2002::/16")
)

func prefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

func inAny(ip netip.Addr, ps []netip.Prefix) bool {
	for _, p := range ps {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// embeddedIPv4 returns the IPv4 address carried by IPv4-mapped, NAT64 and 6to4 addresses.
func embeddedIPv4(ip netip.Addr) (netip.Addr, bool) {
	if ip.Is4In6() {
		return ip.Unmap(), true
	}
	b := ip.As16()
	switch {
	case nat64.Contains(ip):
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), true
	case sixTo4.Contains(ip):
		return netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}), true
	}
	return netip.Addr{}, false
}

// CheckIP reports whether ip may be contacted under p.
func (p Policy) CheckIP(ip netip.Addr) error {
	if !ip.IsValid() {
		return fmt.Errorf("%w: invalid address", ErrBlocked)
	}
	ip = ip.WithZone("")
	if v4, ok := embeddedIPv4(ip); ok {
		if err := p.CheckIP(v4); err != nil {
			return err
		}
	}
	if inAny(ip, alwaysBlocked) || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("%w: %s is a link-local, metadata or reserved address", ErrBlocked, ip)
	}
	if !p.AllowPrivate && (inAny(ip, privateRanges) || ip.IsLoopback() || ip.IsPrivate()) {
		return fmt.Errorf("%w: %s is a private or loopback address (an admin can allow private networks)", ErrBlocked, ip)
	}
	return nil
}

// Resolver looks up host addresses (net.DefaultResolver satisfies it).
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// CheckURL parses raw and checks its scheme and host. Literal IPs are checked
// directly; host names are resolved with r (nil = net.DefaultResolver) and
// every address must pass. It returns the parsed URL.
func (p Policy) CheckURL(ctx context.Context, raw string, r Resolver) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBlocked, err)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !p.AllowHTTP {
			return nil, fmt.Errorf("%w: only https URLs are allowed", ErrBlocked)
		}
	default:
		return nil, fmt.Errorf("%w: unsupported scheme %q", ErrBlocked, u.Scheme)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%w: credentials in URLs are not allowed", ErrBlocked)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("%w: missing host", ErrBlocked)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return u, p.CheckIP(ip)
	}
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if !p.AllowPrivate && (lower == "localhost" || strings.HasSuffix(lower, ".localhost") ||
		strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".internal") || strings.HasSuffix(lower, ".home.arpa")) {
		return nil, fmt.Errorf("%w: %s is a local host name (an admin can allow private networks)", ErrBlocked, host)
	}
	if r == nil {
		r = net.DefaultResolver
	}
	addrs, err := r.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("netguard: resolve %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("netguard: resolve %s: no addresses", host)
	}
	for _, a := range addrs {
		if err := p.CheckIP(a); err != nil {
			return nil, err
		}
	}
	return u, nil
}

// Control is a net.Dialer Control function that refuses connections to
// addresses the policy blocks. It runs after DNS resolution, for the exact
// address being connected to.
func (p Policy) Control(network, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBlocked, err)
	}
	return p.CheckIP(ap.Addr())
}

// Dialer returns a dialer that enforces the policy on every connection.
func (p Policy) Dialer() *net.Dialer {
	return &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second, Control: p.Control}
}

// Transport returns an HTTP transport that enforces the policy. Environment
// proxies are ignored: a proxy would connect on our behalf and bypass the check.
func (p Policy) Transport() *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           p.Dialer().DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

// Client returns an HTTP client with the guarded transport. Redirects are
// followed (at most 5) only to URLs the policy allows; the dialer re-checks
// the resolved address of every hop.
func (p Policy) Client(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: p.Transport(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("netguard: too many redirects")
			}
			switch req.URL.Scheme {
			case "https":
			case "http":
				if !p.AllowHTTP {
					return fmt.Errorf("%w: redirect to http", ErrBlocked)
				}
			default:
				return fmt.Errorf("%w: redirect to %q", ErrBlocked, req.URL.Scheme)
			}
			return nil
		},
	}
}
