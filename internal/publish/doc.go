// Package publish manages series, chapter publishing per language, schedules, visibility, age ratings,
// licences and per-role AI labels.
//
// Screens: E1 publish flow, E1b published, series settings (E5 belongs to social).
//
// The read side (contract.go, implemented in service.go) is provided as
// publish.Service: reader, social, discover and analytics read published
// series, chapters and frozen pages only through it, and every method applies
// the ADR 0012 access rules in access.go. Package publishtest builds fixtures
// for their tests.
//
// The write side is session-only (a human action, never a token): series
// settings (series_write.go), the publish job that freezes pages through
// compose and lettering (freeze.go, job.go), deterministic alt-text drafts
// (alttext.go), covers (cover.go), the schedule loop and new-chapter
// notifications (lifecycle.go).
package publish
