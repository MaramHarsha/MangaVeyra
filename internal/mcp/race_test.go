//go:build race

package mcp_test

import "time"

// Full-page image generation is CPU-heavy under race instrumentation, like
// the lettering tests that use the same tenfold allowance.
const jobWaitTimeout = 10 * time.Minute
