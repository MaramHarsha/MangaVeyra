// Package reader serves the reader and the library (Phase 3, ADR 0012).
//
// Screens: E3 paged/spread/guided reader and E4 webtoon reader at
// /s/{series}/{chapter}, E7 library at /library/{following|history|downloads|bookmarks}.
//
// The reader page arrives complete without JavaScript (stacked frozen page
// images with alt-text, panel alt-text and dialogue for screen readers); the
// reader island (web/src/islands/reader) adds the paged, spread, guided and
// webtoon views, tap zones, read-aloud with on-device voices only, bookmarks,
// comments and offline downloads.
//
// Every read and access decision goes through publish.Service; the module
// writes only its own tables (read_progress, reader_prefs, bookmarks,
// downloads, age_confirmations), always after publish.Service says the
// viewer may read the chapter. social.Follows and analytics.Recorder are
// optional: without them the library's Following tab shows its empty state
// and reading events are not recorded.
package reader
