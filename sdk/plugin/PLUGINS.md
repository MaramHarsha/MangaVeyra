<!-- SPDX-License-Identifier: MIT -->
# MangaVeyra plugins

A plugin extends a MangaVeyra server with new models, fonts, layout templates, themes and, later, code that runs in
a WebAssembly sandbox. This document, the manifest schema in [`schema/plugin.schema.json`](schema/plugin.schema.json)
and the Go package `github.com/MaramHarsha/MangaVeyra/sdk/plugin` are licensed **MIT** (see `sdk/LICENSE`), so
plugins and plugin tooling can use any licence the plugin rules allow.

| | App v1 | App v2 (planned) |
|---|---|---|
| Manifest-only plugins (`entry.type: manifest`) | **Installed and used** | Installed and used |
| WebAssembly plugins (`entry.type: wasm`, ABI 2) | Listed in the directory, never run | Run in the sandbox described in §5 |

Plugins never get more than they declare, never bypass the app's rules (change scope, locks, local-only mode,
the licence guard) and never see payment data, because there is none.

## 1. The manifest: `plugin.json`

Every plugin has a `plugin.json` at its root. The Go package parses it strictly (unknown members are errors) and the
JSON Schema describes the same rules.

```json
{
  "mvplugin": 1,
  "id": "lin-park.community-model-manifests",
  "name": "Community model manifests",
  "version": "0.4.1",
  "kind": "model-adapter",
  "description": "Model manifests for community checkpoints.",
  "licence": "CC0-1.0",
  "authors": [{ "name": "lin.park", "handle": "lin-park" }],
  "homepage": "https://github.com/lin-park/community-model-manifests",
  "min_app_version": "1.0.0",
  "entry": {
    "type": "manifest",
    "contributes": {
      "models": ["models/glass-orchard-xl.json"],
      "layout_templates": ["layouts/four-tier.json"]
    }
  },
  "permissions": [
    { "id": "models.provide", "risk": "low", "reason": "Adds model manifests to the catalogue." },
    { "id": "layout.templates", "risk": "low", "reason": "Adds a four-tier page layout." }
  ],
  "source": {
    "repo": "https://github.com/lin-park/community-model-manifests",
    "tag": "v0.4.1",
    "commit": "0f6b3c2d9a8e7f6b5c4d3e2f1a0b9c8d7e6f5a4b",
    "signed_tag": true
  }
}
```

| Member | Rules |
|---|---|
| `mvplugin` | Format version, `1`. |
| `id` | `<world handle>.<name>`: the developer's World handle, a dot, and 2–64 of `a–z 0–9 -`. The handle prefix makes ids globally unique and ties a plugin to its developer account. |
| `name` | 1–60 characters. |
| `version` | Semantic version. Every release is a new version. |
| `kind` | Directory category (screen G7): `model-adapter`, `exporter`, `reader`, `lettering` or `theme`. |
| `description` | Optional, ≤ 500 characters. |
| `licence` | `MIT`, `Apache-2.0`, `BSD-2-Clause`, `BSD-3-Clause`, `ISC`, `MPL-2.0` (code), `OFL-1.1` (fonts), `CC0-1.0` or `CC-BY-4.0` (data). A plugin distributed as a `.mvpack` also needs a pack licence from `sdk/mvpack/SPEC.md` §3.2 (the BSD, ISC and MPL licences are not pack licences yet; use `MIT` or `Apache-2.0` for the pack). |
| `authors` | Optional: `name` (1–40) and World `handle`. |
| `homepage` | Optional `https` URL. |
| `min_app_version` | Lowest MangaVeyra version the plugin works with (semantic version precedence). The app refuses to enable it on older servers. |
| `entry` | How the app loads it, §2 and §5. |
| `permissions` | Required, may be `[]`. Every permission the plugin uses, §3. |
| `source` | The public release the build comes from: `repo` (https), `tag` (release tag), optional `commit` (full hash), `signed_tag` (whether the tag carries a verified signature). World only accepts plugins from a public repository and requires `signed_tag: true` for plugins that run code; manifest-only plugins should use a signed tag too. |

## 2. Manifest-only plugins (v1)

Version 1 of the app runs **no plugin code at all**. A manifest-only plugin contributes files that the app already
knows how to read, listed under `entry.contributes` as paths relative to the plugin root:

| `contributes` member | Files | Permission | What the app does |
|---|---|---|---|
| `models` | `.json` model manifests (the same format as `internal/ai/catalog/*.yaml`, as JSON) | `models.provide` | Adds the models to *Models & providers*; the router can use them once a provider or worker can run them. New models ship without an app release. |
| `fonts` | `.ttf`, `.otf`, `.woff2` | `lettering.fonts` | Adds the fonts to the lettering tools (bubbles, captions, SFX). Font licences must allow embedding in exports; OFL fonts do. |
| `layout_templates` | `.json` panel layouts (the pack `layout` role) | `layout.templates` | Adds page layouts to the template picker. |
| `themes` | `.json` theme tokens (`--mv-*` variables) | `theme.tokens` | Adds a reader/app theme. Themes must keep WCAG AA contrast for every text/background token pair; apps should refuse themes that fail. |

A manifest entry MUST NOT declare `module`, `abi` or `hooks`, MUST contribute at least one file and MAY only request
the four low-risk permissions above. Because it carries no code, a manifest-only plugin can be installed on a
server in local-only mode and needs no sandbox.

**Distribution.** A manifest-only plugin is shipped as a `.mvpack` of kind `plugin` (see `sdk/mvpack/SPEC.md`):
`plugin.json` with role `plugin-manifest` (named by the pack's `plugin.manifest`), model manifests with role
`model-manifest`, layouts with role `layout`, themes with role `theme` and fonts with role `font`. Admins install
packs from the World catalogue (*Plugins* tab) or by uploading a file. By default a server installs only packs with
a trusted `world` signature; the admin can allow others, and the install screen then says the plugin was not
reviewed.

## 3. Permissions

Every permission has a fixed risk level. A manifest MUST state the same risk the registry gives it, so admins and
reviewers always see the same word, and MUST explain in 3–200 characters why it needs it. The install screen lists
permissions with their risk (icon + word, never colour alone), and an update that adds a permission is not applied
until an admin approves the difference (`PermissionDiff`).

| Permission | Risk | Needs code | Meaning |
|---|---|---|---|
| `models.provide` | low | | Add model manifests to the model catalogue |
| `lettering.fonts` | low | | Add fonts to the lettering tools |
| `layout.templates` | low | | Add page layout templates |
| `theme.tokens` | low | | Change theme colours and fonts |
| `reader.audio` | low | yes | Play audio in the reader |
| `reader.settings.read` | low | yes | Read reader settings (mute, reduced motion, direction) |
| `reader.ui` | low | yes | Add controls to the reader toolbar |
| `export.formats` | medium | yes | Add an export format (receives the pages being exported) |
| `import.formats` | medium | yes | Read files a person chooses to import |
| `models.invoke` | medium | yes | Run models through the router (counts toward the compute meter) |
| `project.read` | high | yes | Read project content: scripts, pages and characters |
| `project.write` | high | yes | Change project content (scope and lock rules still apply) |
| `files.read` | high | yes | Read media files stored on this server |
| `network.fetch` | high | yes | Connect to the listed hosts (1–10 lowercase host names, `*.` wildcard allowed); blocked in local-only mode |
| `secrets.use` | high | yes | Use an API key the admin assigns to this plugin |

There is no permission for payments, for other users' private data, for unlocking panels or for publishing,
deleting or sharing: those actions always need a person.

## 4. Review and signing on World

Plugins are published through World by **developer accounts** (screen H7): confirmed email, two-factor
authentication, a registered ed25519 signing key, a linked public source repository and the developer terms.

1. The developer tags a release in the public repository and submits the version (the pack, signed with their
   `developer` key, plus the source reference).
2. World builds the plugin from the tagged source in an isolated builder and compares the result with the uploaded
   pack; a mismatch rejects the submission.
3. Automated checks run as for every submission (files, malware, signature, licence), and the permission list is
   diffed against the last approved version.
4. A reviewer reads the change. The first release of every developer always gets a full code review; later
   releases get a diff review, and any new high-risk permission gets a full review again.
5. On approval World countersigns the pack (`world` signature). Servers see the update in the plugin directory,
   with the permission diff, and an admin applies it.

## 5. WebAssembly plugins — ABI 2 (planned for app v2)

> Status: **draft**. The manifest fields are final and validated today so developers can prepare plugins; the host
> interface below may still change before app v2 ships. Changes will bump `entry.abi`.

A code plugin sets `entry.type: "wasm"`, `entry.abi: 2`, `entry.module` (path of the `.wasm` file) and
`entry.hooks` (the extension points it implements, e.g. `reader.on_page_turn`). Code plugins will be distributed in
`.mvpack` format version 2, which adds a `wasm-module` role; format 1 rejects WebAssembly by design.

### 5.1 Sandbox

* **Runtime.** A pure-Go WebAssembly runtime inside the MangaVeyra process (no cgo, keeping the single static
  binary), one instance per plugin per call, WebAssembly 2.0 core with bulk memory and multi-value; no threads,
  no SIMD requirement.
* **No ambient authority.** No WASI filesystem, sockets, clocks with sub-millisecond resolution or environment
  variables. Everything a plugin can do goes through the host functions of §5.3, and each host function checks the
  plugin's granted permissions on every call.
* **Limits.** 64 MiB linear memory by default (admin-adjustable up to 256 MiB), fuel metering with a per-call
  budget, a wall-clock deadline per hook (2 s for UI hooks, 60 s for export and import hooks), at most 4 concurrent
  instances per plugin. Exceeding any limit traps the instance, the call fails cleanly, and repeated traps disable
  the plugin with a notice to the admin.
* **Isolation of data.** A plugin sees only what the hook passes in and what its permissions let it read. Project
  writes go through the same server-side checks as any edit (`studio.CheckScopeWritable`): locked panels stay
  locked and changes stay inside the hook's scope. Every write is recorded in the audit log with the plugin id.
* **Network.** `network.fetch` reaches only the hosts listed in the manifest, through the app's SSRF guard, and is
  refused entirely in local-only mode.

### 5.2 Module contract

The module MUST export:

| Export | Signature | Purpose |
|---|---|---|
| `memory` | memory | Linear memory shared with the host |
| `mv_abi_version` | `() -> i32` | Returns `2` |
| `mv_alloc` | `(size: i32) -> i32` | Allocate `size` bytes for the host to write into; 0 on failure |
| `mv_free` | `(ptr: i32, size: i32)` | Release memory returned by `mv_alloc` or passed to the host |
| `mv_init` | `(cfg_ptr: i32, cfg_len: i32) -> i32` | Optional. Called once per instance with the plugin's settings (JSON); non-zero fails the call |
| `mv_hook` | `(name_ptr: i32, name_len: i32, in_ptr: i32, in_len: i32) -> i64` | Run one hook |

**Calling convention.** Strings and payloads are UTF-8 JSON in linear memory, passed as `(ptr, len)`. The host
writes the hook name and input with `mv_alloc`, calls `mv_hook`, and receives an `i64` packing the result:
the high 32 bits are a pointer, the low 32 bits a length, of a JSON document
`{"ok": <result>}` or `{"error": {"code": "...", "message": "..."}}`. The host copies the result out and calls
`mv_free` on it. Large binary data (page images in export hooks) is never copied as a whole: the input carries
opaque handles that the plugin reads in chunks with `mv_blob_read`.

### 5.3 Host functions (import module `mv`)

| Import | Permission | Purpose |
|---|---|---|
| `mv_log(level, ptr, len)` | none | Write to the plugin log (rate-limited, visible to admins) |
| `mv_now_ms() -> i64` | none | Coarse wall clock (millisecond resolution) |
| `mv_random(ptr, len)` | none | Fill memory with random bytes |
| `mv_blob_read(handle, offset, ptr, len) -> i32` | depends on handle origin | Read a chunk of a blob the hook received |
| `mv_blob_create(kind_ptr, kind_len) -> i32` / `mv_blob_write(handle, ptr, len) -> i32` | `export.formats`, `project.write` | Produce output (an export file, a new layer image) |
| `mv_project_get(req_ptr, req_len) -> i64` | `project.read` | Read scripts, pages, panels, characters (JSON) |
| `mv_project_apply(req_ptr, req_len) -> i64` | `project.write` | Apply a scoped mutation; scope and lock rules apply |
| `mv_files_read(req_ptr, req_len) -> i64` | `files.read` | Read a stored media file as a blob handle |
| `mv_models_run(req_ptr, req_len) -> i64` | `models.invoke` | Enqueue a job through the router; returns a job id; progress arrives as `jobs.on_progress` |
| `mv_fetch(req_ptr, req_len) -> i64` | `network.fetch` | HTTP request to a listed host; body as a blob handle |
| `mv_secret(name_ptr, name_len) -> i64` | `secrets.use` | Get the API key the admin assigned to this plugin (never another key) |
| `mv_reader_settings() -> i64` | `reader.settings.read` | Reader preferences (JSON) |
| `mv_audio_play(req_ptr, req_len) -> i32` | `reader.audio` | Play a short sound bundled with the plugin |

Functions returning `i64` use the same `(ptr << 32) | len` packing and JSON envelope as `mv_hook`; memory they
return is owned by the plugin and released with `mv_free`. A call without the needed permission returns
`{"error": {"code": "permission_denied"}}` rather than trapping, so plugins can degrade gracefully.

### 5.4 Hooks

Hook names match `^[a-z]+(\.[a-z_]+)+$`. The first set planned for v2:

| Hook | Kind | Input → output |
|---|---|---|
| `reader.on_page_turn` | reader | `{series, chapter, page, direction}` → `{}` |
| `reader.toolbar` | reader | `{locale}` → `{buttons: [...]}` |
| `export.render_chapter` | exporter | `{chapter, pages: [blob handles], options}` → `{file: blob handle, media_type, file_name}` |
| `import.parse` | exporter | `{file: blob handle, file_name}` → `{pages: [...]}` (panel and bubble hints) |
| `lettering.layout` | lettering | `{bubble, text, font, box}` → `{lines, font_size}` |
| `models.adapter.request` | model-adapter | `{job, model, provider}` → HTTP request description (sent by the host through `network.fetch` rules) |
| `models.adapter.response` | model-adapter | `{job, response}` → `{images | text, usage}` |
| `jobs.on_progress` | any with `models.invoke` | `{job_id, state, progress}` → `{}` |

A plugin receives only the hooks it lists. An unknown hook name in a manifest is valid (for forward compatibility)
but the app never calls it.

## 6. Building plugins

* Validate `plugin.json` with the schema or with the Go package:
  `plugin.Parse(data)` returns every problem at once; `m.MaxRisk()`, `m.RunsCode()`, `m.CompatibleWith(v)` and
  `plugin.PermissionDiff(old, new)` are what the app's install and update screens use.
* Package with `sdk/mvpack` (`mvpack.NewWriter`, role `plugin-manifest` for `plugin.json`) and sign with your
  developer key.
* Test locally: `go run ./cmd/worldstub --seed` runs a development-only fake of World that accepts plugin
  submissions from developer accounts (the seeded `kestrel` and `jerry-ink` accounts) and serves the seeded
  *Harbour Ghosts theme* plugin.
