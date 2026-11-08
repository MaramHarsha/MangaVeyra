// Package mcp is the built-in MCP server and its G13 screen (ADR 0013 §7).
//
// /mcp serves the official Go SDK's streamable HTTP transport (stateless, JSON
// responses) to bearer agent tokens only: browser sessions, API and worker
// tokens are refused, each token is rate limited, and the owner can switch
// all their agents off. `mangaveyra mcp-stdio` bridges stdin/stdout to /mcp
// for desktop apps.
//
// Tools act as the token's owner within its scopes (read, edit, generate) and
// project list, through the same Studio, page store and generation services,
// locks, revision fences and spend reservations as the UI. Publish/schedule,
// deleting pages or projects, sharing or changing roles and unlocking are
// never tools: request_human_action records a pending request a person
// reviews in G13, and generation estimated above the token's approval
// threshold waits the same way. Every agent change is recorded in
// agent_actions and the audit log (actor_kind agent with the token id), page
// art in version history, and G13 offers Undo where history supports it.
//
// Screens: G13 agents & MCP.
package mcp
