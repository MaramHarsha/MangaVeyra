package mcp

import (
	"context"

	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// SetQuoteForTest makes every generation estimate usd on a remote route, so
// approval thresholds can be tested without a remote provider or network.
func SetQuoteForTest(m *Module, usd float64) {
	m.quote = func(ctx context.Context, a *agent, req genRequest) (quoteResult, error) {
		if _, err := m.dryRunQuote(ctx, a, req); err != nil {
			return quoteResult{}, err
		}
		return quoteResult{Estimate: jobs.Cost{USD: usd}, ProviderID: "test-remote", ModelID: "test-remote"}, nil
	}
}

// ResetQuoteForTest restores the real dry-run estimate.
func ResetQuoteForTest(m *Module) { m.quote = m.dryRunQuote }
