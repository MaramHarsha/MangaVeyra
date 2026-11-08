// Package editioncheck is a test-only package guarding product rule 1: no
// payment UI in the open-source edition. Its tests fail the build when
// forbidden words (prices, checkout, credits, wallets, tips, subscriptions,
// payouts, upgrade prompts) appear in UI templates or locale files.
//
// It scans every value of every embedded locale catalog (with a short list of
// non-payment exceptions such as a book's credits page), the static website in
// site/ and the install guide in docs/install/.
package editioncheck
