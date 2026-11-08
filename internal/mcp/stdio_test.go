package mcp_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MaramHarsha/MangaVeyra/internal/mcp"
)

// bridge runs RunStdio in-process on pipes and returns an SDK client session
// speaking to it over newline-delimited JSON, as a desktop app would.
func bridge(t *testing.T, f *fixture, opts mcp.StdioOptions) (*mcpsdk.ClientSession, <-chan error, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	toBridgeR, toBridgeW := io.Pipe()
	fromBridgeR, fromBridgeW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := mcp.RunStdio(ctx, opts, toBridgeR, fromBridgeW)
		_ = fromBridgeW.Close()
		done <- err
	}()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "desktop-app", Version: "1.0"}, nil)
	s, err := client.Connect(ctx, &mcpsdk.IOTransport{Reader: fromBridgeR, Writer: toBridgeW}, nil)
	t.Cleanup(func() {
		_ = toBridgeW.Close()
	})
	return s, done, err
}

func TestStdioBridgeRoundTrip(t *testing.T) {
	forEachDialect(t, func(t *testing.T, f *fixture) {
		raw, _ := f.token("Desktop app", scopesRead, []string{f.project.ID}, nil)
		s, done, err := bridge(t, f, mcp.StdioOptions{URL: f.ta.URL, Token: raw})
		if err != nil {
			t.Fatalf("connect through the bridge: %v", err)
		}
		// A read-only token sees only the read tools through the bridge too.
		names := toolNames(t, s)
		for _, n := range names {
			if n == "edit_script" || n == "queue_generation" {
				t.Fatalf("read-only token lists %s", n)
			}
		}
		got := mustCall(t, s, "list_projects", nil)["projects"].([]any)
		if len(got) != 1 || str(got[0].(map[string]any)["title"]) != f.project.Title {
			t.Fatalf("projects through the bridge %v", got)
		}
		refused := call(t, s, "edit_script", map[string]any{"project_id": f.project.ID})
		if !refused.Err {
			t.Fatalf("edit tool callable through a read-only bridge: %+v", refused)
		}
		_ = s.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("bridge exit: %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("bridge did not exit when stdin closed")
		}
	})
}

func TestStdioBridgeReportsRefusedTokens(t *testing.T) {
	f := newFixture(t, "sqlite")
	if err := mcp.RunStdio(context.Background(), mcp.StdioOptions{URL: f.ta.URL, Token: "mvk_not_an_agent"}, strings.NewReader(""), io.Discard); !errors.Is(err, mcp.ErrStdioConfig) {
		t.Fatalf("non-agent token: %v", err)
	}
	if err := mcp.RunStdio(context.Background(), mcp.StdioOptions{URL: "ftp://example.invalid", Token: "mva_x"}, strings.NewReader(""), io.Discard); !errors.Is(err, mcp.ErrStdioConfig) {
		t.Fatalf("bad url: %v", err)
	}
	raw, info := f.token("Revoked desktop", scopesRead, nil, nil)
	if err := f.ta.Deps.Auth.RevokeToken(f.ctx, f.owner.ID, info.ID, false); err != nil {
		t.Fatal(err)
	}
	_, _, err := bridge(t, f, mcp.StdioOptions{URL: f.ta.URL + "/mcp", Token: raw})
	if err == nil || !strings.Contains(err.Error(), "MangaVeyra refused") {
		t.Fatalf("revoked token through the bridge: %v", err)
	}
}
