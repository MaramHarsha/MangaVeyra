package mcp

import (
	"net/http"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
)

// instructions tell a connected assistant how this server behaves.
const instructions = `MangaVeyra is a self-hosted manga studio. You act as the person who created your token, only in the projects and with the access (read, edit, generate) the token grants, through the same rules as the studio UI.
Read before you write: get_script, list_characters and get_pages return a revision or version; edits and generation must send it back, and a change made since then is refused so you can read again.
Locked chapters, pages, panels and layers cannot be changed by you.
Publishing or scheduling, deleting pages or projects, sharing or changing roles, and unlocking are never tools: call request_human_action and a person decides in Agents & MCP. Generation estimated above your token's approval threshold waits for a person the same way (queue_generation returns pending_approval).
Every change you make is labelled with your token in the audit log and history, and a person can undo it.`

// mcpHandler serves the MCP endpoint: a bearer agent token is required
// (sessions and other token kinds are refused), the owner's switch must be
// on, each token is rate limited, and then the official SDK serves the
// stateless streamable HTTP transport with JSON responses.
func (m *Module) mcpHandler() http.Handler {
	sdk := mcpsdk.NewStreamableHTTPHandler(m.serverFor, &mcpsdk.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 1 << 20,
		// Every request must carry a bearer agent token a browser page cannot
		// obtain, and the global CSRF check still refuses cross-origin browser
		// posts. The SDK's loopback Host check would otherwise refuse every
		// request behind a reverse proxy on the same machine.
		DisableLocalhostProtection: true,
	})
	protected := mcpauth.RequireBearerToken(verifyToken, &mcpauth.RequireBearerTokenOptions{AllowMissingExpiration: true})(sdk)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := m.gate(r); err != nil {
			if e, ok := err.(*httpx.APIError); ok && e.Status == http.StatusUnauthorized {
				w.Header().Set("WWW-Authenticate", `Bearer realm="mangaveyra"`)
			}
			httpx.WriteError(w, r, err)
			return
		}
		protected.ServeHTTP(w, r)
	})
}

// toolSet selects the tools a token sees: tools it cannot use are not listed
// (every call still re-checks the token's current scopes).
type toolSet struct{ edit, generate bool }

// serverFor returns the MCP server for the request's token.
func (m *Module) serverFor(r *http.Request) *mcpsdk.Server {
	set := toolSet{}
	if t := httpx.TokenFrom(r.Context()); t != nil {
		set = toolSet{edit: t.HasScope(scopeWrite), generate: t.HasScope(scopeWrite) && t.HasScope(scopeGenerate)}
	}
	if s, ok := m.servers.Load(set); ok {
		return s.(*mcpsdk.Server)
	}
	s := m.newServer(set)
	actual, _ := m.servers.LoadOrStore(set, s)
	return actual.(*mcpsdk.Server)
}

func (m *Module) newServer(set toolSet) *mcpsdk.Server {
	s := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "mangaveyra", Title: "MangaVeyra", Version: m.d.Version.Version},
		&mcpsdk.ServerOptions{Instructions: instructions})
	m.addReadTools(s)
	if set.edit {
		m.addEditTools(s)
	}
	if set.generate {
		m.addGenerateTools(s)
	}
	return s
}

func readOnly(title string) *mcpsdk.ToolAnnotations {
	return &mcpsdk.ToolAnnotations{Title: title, ReadOnlyHint: true, OpenWorldHint: new(false)}
}

func writes(title string, idempotent bool) *mcpsdk.ToolAnnotations {
	return &mcpsdk.ToolAnnotations{Title: title, DestructiveHint: new(false), IdempotentHint: idempotent, OpenWorldHint: new(false)}
}
