// Package discover builds E6 Explore (/explore) and E8 Search (/search):
// shelves, rankings (rising / week / all time) and full-text search with
// filters that surface newcomers (no pay-for-rank).
//
// Everything listed comes from publish.Service.ListSeries with Listed=true for
// the viewer, so unlisted, private, unpublished and (for anonymous visitors)
// mature series never appear; on personal and private servers anonymous
// visitors are sent to sign in. Rankings use only reads recorded by the
// analytics module and follows on this server. Search matches the 0006
// full-text index (FTS5 on SQLite, the GIN tsvector on PostgreSQL) with
// parsed words only, plus creator names and CJK substrings, then filters the
// candidates through publish.Service. The hand-drawn filter keeps series whose
// every AI label role is human (ADR 0012 §4).
package discover
