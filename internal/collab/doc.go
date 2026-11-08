// Package collab is editor collaboration (D7, ADR 0013 §8): internal review
// comments anchored to panels with a status per comment (open, needs changes,
// done, approved), replies, page approval, the project's member roles and
// scopes, and the live-cursor topic doc:page:{id}.
//
// Review comments are comments rows with visibility 'internal' (internal/social
// owns the public ones). A top-level comment targets a panel (target_kind
// 'panel'); its anchor holds the page, the pin position in page units and the
// panel label at the time ("7.3"). Replies share the root's target and point
// at it through parent_id. A page approval is an internal comment row with
// target_kind 'page' and status 'approved'; its anchor records the page
// version that was approved.
//
// Rules: anyone who can read the project comments and replies; owners and
// editors (and site admins) set needs changes or approved and approve pages;
// the comment's author or assignee marks it done and may reopen it from done.
// Personal API tokens may read (projects:read) and comment (projects:write)
// within their project list; statuses, approvals and member changes need a
// browser session (people, not agents, review and share). Agent tokens are
// refused everywhere here, including doc:page: agents act only through /mcp,
// behind the owner's "Allow AI agents" switch, the per-token limits and the
// agent log.
//
// Realtime: every change publishes "collab.changed" with ids only on the
// page's doc:page:{id} topic; open editors refetch through the API, which
// re-authorizes. Presence on that topic carries only a cursor position and
// colour; the hub adds the member's name. Nothing reads a page's Yjs
// document, so the topic takes no document writes (UPDATE or SNAPSHOT).
package collab
