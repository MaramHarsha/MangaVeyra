package ai

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/core/netguard"
)

// privateEndpointsKey is the ai module's instance setting that lets custom
// providers reach loopback and private-network servers (a local Ollama or LM
// Studio). It is a module key, so it lives beside the typed instance settings
// without widening them.
const privateEndpointsKey = "ai.private_endpoints"

type privateEndpointsSetting struct {
	Allow bool `json:"allow"`
}

// moduleKeys is the part of settings.Service the policy needs. Tests may pass
// a fake; production passes *settings.Service.
type moduleKeys interface {
	GetKey(ctx context.Context, key string, dest any) (bool, error)
	SetKey(ctx context.Context, key string, v any, by string) error
}

// privateEndpointPolicy is the live admin choice for private-network provider
// endpoints. Until an admin decides in the UI, MV_ALLOW_PRIVATE_PROVIDER_URLS
// is the default. It is read on every connection test and every request, so a
// change applies at once. Link-local and metadata addresses are refused by
// netguard whatever this says.
type privateEndpointPolicy struct {
	keys     moduleKeys
	fallback bool
}

// Allowed reports whether private-network endpoints may be contacted now. A
// settings read failure fails closed.
func (p *privateEndpointPolicy) Allowed(ctx context.Context) bool {
	if p == nil {
		return false
	}
	if p.keys == nil {
		return p.fallback
	}
	var v privateEndpointsSetting
	found, err := p.keys.GetKey(ctx, privateEndpointsKey, &v)
	if err != nil {
		return false
	}
	if !found {
		return p.fallback
	}
	return v.Allow
}

// Set stores the admin's choice. Callers authorize the admin first.
func (p *privateEndpointPolicy) Set(ctx context.Context, allow bool, by string) error {
	if p == nil || p.keys == nil {
		return errProviderUnavailable
	}
	return p.keys.SetKey(ctx, privateEndpointsKey, privateEndpointsSetting{Allow: allow}, by)
}

// netPolicy is the netguard policy for user-typed provider URLs right now.
func (p *privateEndpointPolicy) netPolicy(ctx context.Context) netguard.Policy {
	return netguard.Policy{AllowPrivate: p.Allowed(ctx), AllowHTTP: true}
}

// errPrivateEndpointsOff is returned before any connection is attempted while
// private-network endpoints are off.
var errPrivateEndpointsOff = errors.New("ai: private-network provider endpoints are off")

// privateGate refuses every request while private-network endpoints are off.
// The local text client only ever dials loopback or private addresses, so the
// live setting gates the whole client. Checking per request (not per dial)
// means a kept-alive connection cannot outlive an admin turning it off.
type privateGate struct {
	policy *privateEndpointPolicy
	next   http.RoundTripper
}

func (g privateGate) RoundTrip(req *http.Request) (*http.Response, error) {
	if !g.policy.Allowed(req.Context()) {
		return nil, errPrivateEndpointsOff
	}
	return g.next.RoundTrip(req)
}

// endpointNeverAllowed reports a URL whose literal host is link-local,
// metadata or otherwise reserved: no setting may allow it. Host names are
// resolved and re-checked by the guarded transport when the endpoint is
// tested, never when it is saved.
func endpointNeverAllowed(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil {
		return false
	}
	return netguard.Policy{AllowPrivate: true, AllowHTTP: true}.CheckIP(ip) != nil
}
