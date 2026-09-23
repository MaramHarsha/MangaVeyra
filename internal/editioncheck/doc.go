// Package editioncheck is a test-only package guarding product rule 1: no
// payment UI in the open-source edition. Its tests fail the build when
// forbidden words (prices, checkout, credits, wallets, tips, subscriptions,
// payouts, upgrade prompts) appear in UI templates or locale files.
//
// The foundation ships a smoke test; the owner of the web layer extends it to
// scan internal/web templates and every locale catalog.
package editioncheck
