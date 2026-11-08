// Package analytics records read events and reports completion and drop-off by
// page and followers over time (ADR 0012 §8).
//
//   - Recorder (recorder.go) is provided as analytics.Recorder. It checks
//     publish.Service.CanRead, validates and clamps events, limits each viewer
//     (members by account, anonymous visitors by session hash) and stores
//     user_id for members and only a salted, daily-rotating session hash for
//     anonymous visitors. The first open of a chapter per reader and UTC day
//     also adds one to series.view_count in the same transaction.
//   - POST /api/v1/analytics/events (events_api.go) takes batches from the
//     reader island and sets the first-party mv_rs session cookie.
//   - RollUp (aggregate.go) folds raw events older than 90 days into daily
//     aggregates and deletes them; it waits for the read_event_days table
//     (proposed migration in testdata/) and never deletes without aggregating.
//   - F2 /studio/analytics (page.go, views.templ) shows creators the reads,
//     completion, drop-off by page and followers of their own series.
//
// Nothing leaves the server and no IP address, user agent or raw session ID
// is stored.
package analytics
