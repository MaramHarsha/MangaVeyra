// Package social implements series follows, user follows, public comments
// and reports, and renders the E5 series page (/s/{series}) and the F1
// creator profile (/u/{username}).
//
// Ownership: social writes the follows, user_follows and reports tables and
// the public rows of the comments table (visibility 'public', target_kind
// chapter, panel or series). Every series, publication and access decision
// goes through publish.Service: a comment, follow or report is only read or
// written when the viewer may read its series (and, for chapter and panel
// comments, its publication). Following and unfollowing keep
// series.follower_count in step inside the same transaction with one narrow
// UPDATE (the only write to a publish-owned table).
//
// Other modules use social.Follows (contract.go) through
// core.Use[social.Follows]; the reader island uses the comments JSON API:
//
//	GET    /api/v1/social/comments?target_kind=&target_id=&series=[&cursor=&limit=&page=&panel=]
//	POST   /api/v1/social/comments              {series_id, target_kind, target_id, anchor, body, parent_id?}
//	PATCH  /api/v1/social/comments/{id}         {body}            author only
//	DELETE /api/v1/social/comments/{id}                           author only
//	POST   /api/v1/social/comments/{id}/hide                      series owner only
//	DELETE /api/v1/social/comments/{id}/hide                      series owner only (unhide)
//	POST   /api/v1/social/reports               {target_kind, target_id, series_id?, reason, details?}
//	GET|PUT|DELETE /api/v1/social/series/{id}/follow   {notify?}
//	GET|PUT|DELETE /api/v1/social/users/{username}/follow
//
// Comment targets: target_kind=series uses the series ID; chapter and panel
// use the chapter publication ID (Publication.ID, one per language). A panel
// comment carries anchor {"page": n, "panel": order} naming the frozen page
// number and the guided panel order; a chapter comment may carry {"page": n};
// a series comment has the empty anchor {}. Anchors are stored canonically so
// the optional page/panel list filters match exactly. Owner hiding is stored
// in comments.resolved_at/resolved_by (unused by public comments otherwise):
// hidden comments stay visible to their author and the series owner only.
// Deleting clears the body and sets deleted_at. Bodies are plain text, at
// most MaxCommentRunes characters, and always rendered escaped.
package social
