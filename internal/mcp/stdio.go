package mcp

import (
	"context"
	"errors"
	"io"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
)

// ErrNotAvailable is returned until the MCP module is implemented.
var ErrNotAvailable = errors.New("mcp: the MCP server is not available in this build yet")

// RunStdio serves MCP over stdin/stdout for local agents (called by
// `mangaveyra mcp-stdio`). token is an agent token (mva_...) scoping the session.
func RunStdio(ctx context.Context, d *core.Deps, token string, in io.Reader, out io.Writer) error {
	return ErrNotAvailable
}
