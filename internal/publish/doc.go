// Package publish manages series, chapter publishing per language, schedules, visibility, age ratings,
// licences and per-role AI labels.
//
// Screens: E1 publish flow, E5 series page.
//
// The read side (contract.go, implemented in service.go) is provided as
// publish.Service: reader, social, discover and analytics read published
// series, chapters and frozen pages only through it, and every method applies
// the ADR 0012 access rules in access.go. Package publishtest builds fixtures
// for their tests. The E1 publish flow, page freezing and the scheduler are
// not implemented yet.
package publish
