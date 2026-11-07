// Package seed creates the original demo content (Tidebreak Courier: Rin Asagiri, Kaito Mbeki, Old
// Wren, Sora Vell, Pip the Gull) with labelled screentone placeholder art for `mangaveyra seed
// --demo`.
//
// CLI: mangaveyra seed.
//
// The demo is unpublished and owned by an existing active admin/creator. A
// versioned owner-scoped marker makes repeated runs preserve user edits. Files
// are staged before the single database transaction; rows, attachments, page
// documents and version history commit together. No AI provider is required.
package seed
