package mcp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// Token scopes an agent token carries. The G13 form maps Read, Edit and
// Generate onto them; Studio requires the write scope for generation too.
const (
	scopeRead     = "projects:read"
	scopeWrite    = "projects:write"
	scopeGenerate = "generate"
)

// allowFlag is the per-person "Allow AI agents (MCP)" switch in G13
// (settings.UserPrefs.Flags, on unless turned off).
const allowFlag = "agents.mcp"

// extraTokenID carries the stored token ID from the HTTP layer to the tools.
const extraTokenID = "mangaveyra_token_id"

// agent is the token a tool call runs as. It is re-resolved for every call, so
// a token revoked, expired or narrowed mid-session stops working at once.
type agent struct {
	tok   *httpx.Token
	user  *auth.User
	actor studio.Actor
}

func (a *agent) has(scope string) bool { return a.tok.HasScope(scope) }

// broadToken reports a token whose scopes reach past the agent surface: "*"
// and "admin" would let an admin's agent act on projects its owner is not a
// member of, so MCP refuses them outright.
func broadToken(t *httpx.Token) bool {
	return slices.Contains(t.Scopes, "*") || slices.Contains(t.Scopes, "admin")
}

// gateError is an HTTP refusal at /mcp before any MCP message is read.
func (m *Module) gate(r *http.Request) (*httpx.Token, error) {
	id := httpx.IdentityFrom(r.Context())
	if id.Token == nil {
		// A browser session is not an agent: MCP needs a bearer agent token.
		return nil, httpx.ErrUnauthorized()
	}
	if id.Token.Kind != httpx.TokenAgent || id.User == nil {
		return nil, httpx.ErrForbidden("MCP needs an agent token (mva_...)")
	}
	if broadToken(id.Token) {
		return nil, httpx.ErrForbidden("agent tokens with the admin scope cannot use MCP")
	}
	if !m.allowed(r.Context(), id.User.ID) {
		return nil, httpx.ErrForbidden("AI agents are turned off for this account")
	}
	if ok, wait := m.requests.Allow("t:" + id.Token.ID); !ok {
		return nil, httpx.ErrRateLimited(wait)
	}
	return id.Token, nil
}

// allowed reports the owner's "Allow AI agents (MCP)" switch.
func (m *Module) allowed(ctx context.Context, userID string) bool {
	p, err := m.d.Settings.UserPrefs(ctx, userID)
	if err != nil {
		return false
	}
	return p.Flag(allowFlag, true)
}

// verifyToken hands the already verified identity to the SDK, which carries
// it to every tool call of the request as TokenInfo.
func verifyToken(ctx context.Context, _ string, r *http.Request) (*mcpauth.TokenInfo, error) {
	id := httpx.IdentityFrom(r.Context())
	if id.Token == nil || id.User == nil {
		return nil, mcpauth.ErrInvalidToken
	}
	return &mcpauth.TokenInfo{UserID: id.User.ID, Scopes: id.Token.Scopes,
		Extra: map[string]any{extraTokenID: id.Token.ID}}, nil
}

// agentFor re-resolves the token of a tool call and checks scopes.
func (m *Module) agentFor(ctx context.Context, req *mcpsdk.CallToolRequest, scopes ...string) (*agent, error) {
	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil {
		return nil, errors.New("this call carries no agent token")
	}
	ti := req.Extra.TokenInfo
	tokenID, _ := ti.Extra[extraTokenID].(string)
	tok, u, err := m.d.Auth.ResolveStoredToken(ctx, tokenID)
	if err != nil || tok == nil || u == nil || tok.Kind != auth.TokenAgent || u.ID != ti.UserID || tok.UserID != u.ID {
		return nil, errors.New("this agent token is no longer valid (revoked or expired)")
	}
	if broadToken(tok) {
		return nil, errors.New("agent tokens with the admin scope cannot use MCP")
	}
	if !m.allowed(ctx, u.ID) {
		return nil, errors.New("AI agents are turned off for this account")
	}
	for _, s := range scopes {
		if !tok.HasScope(s) {
			return nil, fmt.Errorf("this token does not have the %s access this tool needs", scopeName(s))
		}
	}
	return &agent{tok: tok, user: u, actor: studio.Actor{UserID: u.ID, TokenID: tok.ID}}, nil
}

// project checks the token may use projectID for action and returns it.
func (m *Module) project(ctx context.Context, a *agent, projectID string, action studio.Action) (studio.Project, error) {
	projectID = strings.TrimSpace(projectID)
	if !a.tok.AllowsProject(projectID) {
		return studio.Project{}, errors.New("this token is not allowed in that project")
	}
	if err := m.studio.Authorize(ctx, a.actor, projectID, action); err != nil {
		return studio.Project{}, toolError(err)
	}
	return m.studio.GetProject(ctx, projectID)
}

// writeAllowed applies the per-token write limit.
func (m *Module) writeAllowed(a *agent) error {
	if ok, _ := m.writes.Allow("t:" + a.tok.ID); !ok {
		return errors.New("too many changes in a short time: wait a few seconds and try again")
	}
	return nil
}

func scopeName(s string) string {
	switch s {
	case scopeRead:
		return "read"
	case scopeWrite:
		return "edit"
	case scopeGenerate:
		return "generate"
	}
	return s
}

// toolError turns a service error into a short message an agent can act on,
// without internal details.
func toolError(err error) error {
	if err == nil {
		return nil
	}
	var ge *ai.GenerateError
	var ae *httpx.APIError
	switch {
	case errors.Is(err, context.Canceled):
		return errors.New("the request was canceled")
	case errors.Is(err, studio.ErrNotFound), errors.Is(err, pagestore.ErrNotFound), errors.Is(err, sql.ErrNoRows):
		return errors.New("not found in the projects this token can use")
	case errors.Is(err, studio.ErrForbidden):
		return errors.New("this token (or its owner's project role) is not allowed to do that here")
	case errors.Is(err, studio.ErrLocked), errors.Is(err, pagestore.ErrLocked):
		return errors.New("locked: only a person can unlock it; ask with request_human_action (action \"unlock\")")
	case errors.Is(err, studio.ErrConflict), errors.Is(err, pagestore.ErrVersion):
		return errors.New("it changed since you read it: read it again and retry with the new revision")
	case errors.Is(err, studio.ErrInvalid), errors.Is(err, pagestore.ErrInvalid):
		msg := strings.TrimPrefix(err.Error(), studio.ErrInvalid.Error()+": ")
		if msg == err.Error() || msg == "" {
			return errors.New("the request is not valid")
		}
		return fmt.Errorf("the request is not valid: %s", msg)
	case errors.As(err, &ge):
		if ge.SafeMessage != "" {
			return fmt.Errorf("%s (%s)", ge.SafeMessage, ge.Code)
		}
		return fmt.Errorf("generation was refused (%s)", ge.Code)
	case errors.As(err, &ae) && ae.Status < 500:
		if ae.Message != "" {
			return errors.New(ae.Message)
		}
		return errors.New(strings.ReplaceAll(ae.Code, "_", " "))
	}
	return errors.New("the server could not do that right now")
}

// audit records an agent write in the audit log with the token as actor.
func (m *Module) audit(ctx context.Context, a *agent, action, targetKind, targetID, projectID string, data map[string]any) {
	m.d.Auth.Audit(ctx, auth.AuditEntry{ActorID: a.user.ID, ActorKind: "agent", TokenID: a.tok.ID, Action: action,
		TargetKind: targetKind, TargetID: targetID, ProjectID: projectID, Data: data})
}

// sessionUser returns the signed-in person of a G13 request.
func sessionUser(r *http.Request) *httpx.User { return core.UserFrom(r.Context()) }
