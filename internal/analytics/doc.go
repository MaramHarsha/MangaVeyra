// Package analytics records read events and reports completion and drop-off by
// page and followers over time (ADR 0012 §8).
//
//   - Recorder (recorder.go) is provided as analytics.Recorder. It checks
//     publish.Service.CanRead, validates and clamps events, limits each viewer
//     (members by account, anonymous visitors by session hash) and stores
//     user_id for members and only a salted, daily-rotating session hash for
//     anonymous visitors. The salt is random per UTC day (salts.go, table
//     analytics_day_salts, migration 0403) and deleted
//     once the day is over. The first open of a chapter per reader and UTC day
//     also adds one to series.view_count in the same transaction.
//   - ReadingSessions (session.go) is provided as analytics.ReadingSessions:
//     it accepts only the server-signed first-party mv_rs cookie and starts a
//     new anonymous session only within the per-address new-session limit.
//     The reader uses it for the open it records when it renders a chapter.
//   - POST /api/v1/analytics/events (events_api.go) takes batches from the
//     reader island under the same session rules.
//   - RollUp (aggregate.go) folds raw events older than 90 days into daily
//     aggregates in read_event_days (migration 0402) and deletes them in the
//     same transaction, so raw events are never deleted without aggregating.
//   - F2 /studio/analytics (page.go, views.templ) shows creators the reads,
//     completion, drop-off by page and followers of their own series.
//
// Nothing leaves the server and no IP address, user agent or raw session ID
// is stored.
package analytics
