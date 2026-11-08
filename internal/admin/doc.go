// Package admin serves G5, the admin panel at /admin (ADR 0013 §1–§3): the
// Reports and AI-label disputes queues with their actions (dismiss, hide a
// comment, take a series or chapter down and restore it, suspend an account,
// ask a creator to review an AI label), Users (members, roles, suspension,
// reset links, deletion, invites, sign-up mode and approvals, per-member
// limits), Content rules and Server (instance, services status, maintenance
// mode). The maintenance page itself (G6) is rendered by the core gate.
//
// Only active site admins signed in with a browser session reach these pages;
// everyone else gets 404, and bearer tokens are refused. Every change is
// written to the audit log. Admins act on published content only: the panel
// shows report context (titles, labels, the reported comment) but never opens
// a private project.
//
// Other modules use the dependency-free subpackage adminapi (content rules,
// takedown keys, the optional update hook).
package admin
