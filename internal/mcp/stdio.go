package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// StdioOptions configure the stdio bridge.
type StdioOptions struct {
	// URL is the server's address (MV_PUBLIC_URL); "/mcp" is added unless it
	// is already the endpoint.
	URL string
	// Token is the agent token (mva_...). Pass it in MV_AGENT_TOKEN (or the
	// desktop app's MCP "env" setting): a --token argument is visible to
	// other users of the machine in the process list.
	Token string
	// HTTPClient sends the requests (nil uses a client without a timeout,
	// since a tool call may take a while).
	HTTPClient *http.Client
}

// ErrStdioConfig explains a bridge that cannot start.
var ErrStdioConfig = errors.New("mcp-stdio needs an agent token (MV_AGENT_TOKEN, mva_...; --token also works but shows the token in the process list) and the server URL (--url or MV_PUBLIC_URL)")

// bearer adds the agent token to the bridge's requests to the server's own
// origin (scheme, host and port of the endpoint) and to no other.
type bearer struct {
	token  string
	origin *url.URL
	next   http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Del("Authorization")
	if sameOrigin(r.URL, b.origin) {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	r.Header.Set("User-Agent", "mangaveyra-mcp-stdio")
	return b.next.RoundTrip(r)
}

func sameOrigin(a, b *url.URL) bool {
	return a != nil && b != nil && strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

// errForeignRedirect stops a redirect away from the server's origin: the
// bridge never follows one (the token stays with the server it was made for).
var errForeignRedirect = errors.New("mcp-stdio: the server redirected to another address; set --url to the server's own address")

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// RunStdio serves MCP over stdin/stdout for desktop apps (called by
// `mangaveyra mcp-stdio`): every JSON-RPC message read from in is sent to the
// server's /mcp endpoint with the agent token, and every reply is written to
// out. The bridge holds no data and makes no decision itself; the server
// applies the token's scopes, projects and approvals. It returns when in ends.
func RunStdio(ctx context.Context, opts StdioOptions, in io.Reader, out io.Writer) error {
	endpoint, err := stdioEndpoint(opts.URL)
	token := strings.TrimSpace(opts.Token)
	if err != nil || !strings.HasPrefix(token, "mva_") {
		return ErrStdioConfig
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	next := hc.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	origin, _ := url.Parse(endpoint)
	client := *hc
	client.Transport = bearer{token: token, origin: origin, next: next}
	check := hc.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !sameOrigin(req.URL, origin) {
			return errForeignRedirect
		}
		if check != nil {
			return check(req, via)
		}
		if len(via) >= 10 {
			return errors.New("mcp-stdio: too many redirects")
		}
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	remote, err := (&mcpsdk.StreamableClientTransport{Endpoint: endpoint, HTTPClient: &client, DisableStandaloneSSE: true, MaxRetries: -1}).Connect(ctx)
	if err != nil {
		return err
	}
	defer remote.Close()
	local, err := (&mcpsdk.IOTransport{Reader: io.NopCloser(in), Writer: nopWriteCloser{out}}).Connect(ctx)
	if err != nil {
		return err
	}
	defer local.Close()

	var wg sync.WaitGroup
	remoteErr := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			msg, err := remote.Read(ctx)
			if err != nil {
				if ctx.Err() == nil {
					remoteErr <- err
					cancel()
				}
				return
			}
			if err := local.Write(ctx, msg); err != nil {
				cancel()
				return
			}
		}
	}()
	var result error
	for {
		msg, err := local.Read(ctx)
		if err != nil {
			// stdin closed (the desktop app quit) or the remote side failed.
			break
		}
		if err := remote.Write(ctx, msg); err != nil {
			if req, ok := msg.(*jsonrpc.Request); ok && req.IsCall() {
				reply := &jsonrpc.Response{ID: req.ID, Error: &jsonrpc.Error{Code: jsonrpc.CodeInternalError,
					Message: "MangaVeyra refused or could not be reached: " + err.Error()}}
				if werr := local.Write(ctx, reply); werr != nil {
					result = werr
					break
				}
			}
		}
	}
	cancel()
	_ = remote.Close()
	wg.Wait()
	select {
	case err := <-remoteErr:
		if result == nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
			result = fmt.Errorf("mcp-stdio: %w", err)
		}
	default:
	}
	return result
}

func stdioEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", ErrStdioConfig
	}
	u.RawQuery, u.Fragment = "", ""
	u.Path = strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/mcp") {
		u.Path += "/mcp"
	}
	return u.String(), nil
}
