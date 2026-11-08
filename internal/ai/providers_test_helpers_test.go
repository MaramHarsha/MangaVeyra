package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// ADR 0013 §9 test helpers. Every Phase 4 adapter runs against these fake
// transports or local httptest servers; nothing contacts a provider.

type recordedRequest struct {
	Method string
	URL    string
	Header http.Header
	Body   []byte
}

func (r recordedRequest) json(t *testing.T) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(r.Body, &v); err != nil {
		t.Fatalf("body is not a JSON object: %v %.200s", err, r.Body)
	}
	return v
}

// routeDoer answers by "METHOD URL-prefix" and records every request with its
// headers. Unmatched requests fail the call (never reaching a network).
type routeDoer struct {
	mu     sync.Mutex
	seen   []recordedRequest
	routes []doerRoute
}

type doerRoute struct {
	method, prefix string
	respond        func(*http.Request, []byte) (*http.Response, error)
	times          int // 0 = unlimited
}

func (d *routeDoer) on(method, prefix string, respond func(*http.Request, []byte) (*http.Response, error)) *routeDoer {
	d.routes = append(d.routes, doerRoute{method: method, prefix: prefix, respond: respond})
	return d
}

// once answers the next matching request only; later ones fall through.
func (d *routeDoer) once(method, prefix string, respond func(*http.Request, []byte) (*http.Response, error)) *routeDoer {
	d.routes = append(d.routes, doerRoute{method: method, prefix: prefix, respond: respond, times: 1})
	return d
}

func (d *routeDoer) Do(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	d.mu.Lock()
	d.seen = append(d.seen, recordedRequest{Method: req.Method, URL: req.URL.String(), Header: req.Header.Clone(), Body: body})
	var respond func(*http.Request, []byte) (*http.Response, error)
	for i := range d.routes {
		r := &d.routes[i]
		if r.method == req.Method && strings.HasPrefix(req.URL.String(), r.prefix) && r.times >= 0 {
			respond = r.respond
			if r.times == 1 {
				r.times = -1
			}
			break
		}
	}
	d.mu.Unlock()
	if respond == nil {
		return nil, errors.New("test transport: no route for " + req.Method + " " + req.URL.String())
	}
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	return respond(req, body)
}

func (d *routeDoer) requests() []recordedRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]recordedRequest(nil), d.seen...)
}

func reply(status int, payload any) func(*http.Request, []byte) (*http.Response, error) {
	return func(*http.Request, []byte) (*http.Response, error) { return httpJSON(status, payload, nil), nil }
}

func replyBytes(status int, contentType string, data []byte) func(*http.Request, []byte) (*http.Response, error) {
	return func(*http.Request, []byte) (*http.Response, error) {
		h := http.Header{}
		h.Set("Content-Type", contentType)
		return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(bytes.NewReader(data))}, nil
	}
}

func replyErr(err error) func(*http.Request, []byte) (*http.Response, error) {
	return func(*http.Request, []byte) (*http.Response, error) { return nil, err }
}

func p4Code(t *testing.T, err error) (string, bool) {
	t.Helper()
	var a *adapterError
	if errors.As(err, &a) {
		return a.code, a.retryable
	}
	var g *GenerateError
	if errors.As(err, &g) {
		return g.Code, g.Retryable
	}
	t.Fatalf("error %T %v is not an adapter error", err, err)
	return "", false
}

func usd(v *float64) string {
	if v == nil {
		return "nil"
	}
	b, _ := json.Marshal(*v)
	return string(b)
}

// createConfiguredProvider saves a custom connection with a description.
func (f *aiDBFixture) createConfiguredProvider(t *testing.T, store *providerStore, kind, base, key string, cfg any) providerView {
	t.Helper()
	view, err := store.Create(context.Background(), providerActor{UserID: f.userID, Admin: true}, providerCreate{
		Kind: kind, Name: kind + " test", Key: key, Scope: providerScopeUser, BaseURL: base, Config: cfg,
	})
	if err != nil {
		t.Fatalf("create %s: %v", kind, err)
	}
	return view
}
