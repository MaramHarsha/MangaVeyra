<!-- SPDX-License-Identifier: MIT -->
# The `.mvpack` format, version 1

`.mvpack` is the portable file format MangaVeyra uses to move reusable creative work between servers and World:
characters, style packs, story and page templates, panel layouts, series listings and manifest-only plugins.
A self-hosted server writes a pack when a person exports or submits something, World checks and countersigns it
when it is approved, and any server can import it again.

This specification, the JSON Schemas in `schema/` and the Go package in this directory are licensed **MIT**
(see `sdk/LICENSE`), so tools outside the AGPL app, including the closed World platform, can read and write packs.

The key words MUST, MUST NOT, SHOULD and MAY are used as in RFC 2119. A *writer* produces packs, a *reader*
consumes them. The Go package `github.com/MaramHarsha/MangaVeyra/sdk/mvpack` is the reference implementation;
where this text and the code disagree, the text wins and the code has a bug.

| | |
|---|---|
| File extension | `.mvpack` |
| Media type | `application/vnd.mangaveyra.mvpack` |
| Container | ZIP (PKWARE APPNOTE 6.3), no ZIP64 extensions required |
| Manifest | `manifest.json` at the root, `"mvpack": 1` |
| Signatures | `signature.json` at the root (optional for local exports, required for World) |
| Manifest schema | [`schema/manifest.schema.json`](schema/manifest.schema.json) (JSON Schema 2020-12) |
| Signature schema | [`schema/signature.schema.json`](schema/signature.schema.json) |

## 1. Goals and non-goals

* **Data, never code.** A pack carries images, model weights in the `safetensors` format, JSON documents and, in
  plugin packs only, fonts. Nothing in a pack is ever executed. Plugins that run code (the planned WebAssembly
  sandbox, see `sdk/plugin/PLUGINS.md`) will need a later format version.
* **Safe to open.** Every rule below exists so that a reader can process a hostile file with bounded memory, bounded
  disk use and no writes outside its target directory.
* **Honest credit and provenance.** Every pack names its creator by their World name and labels how AI was used,
  per creative role, with the same scale chapters use.
* **Verifiable.** A signature binds the exact manifest and every payload byte to a key registered on World.
* **No commerce.** Packs never carry prices, currencies, payment links or checkout state, not even in extension
  members. Whether an item is free or paid is World's business and is shown only as a label.

## 2. Container

A pack is a ZIP archive with:

* `manifest.json` at the root (required);
* `signature.json` at the root (optional; see §7);
* payload files, each listed exactly once in the manifest's `files` array;
* optionally, directory entries (names ending in `/`) with no content, which readers ignore.

Writers MUST and readers MUST enforce:

1. Entries use compression method 0 (stored) or 8 (deflate). Any other method is an error.
2. No entry is encrypted (general-purpose flag bit 0).
3. Every entry is a regular file or a directory. Symbolic links, devices and other special files are errors.
4. Entry names follow the path rules of §4 (payload files) or are exactly `manifest.json` / `signature.json`.
5. No two entries have names that are equal when compared case-insensitively (`Refs/A.png` and `refs/a.png`
   collide on common file systems).
6. Every payload entry is listed in the manifest and every listed file is present. Extra files are an error, not
   something to skip.
7. The uncompressed size in the ZIP header equals the `size` declared in the manifest.
8. The archive, the number of entries and every size respect the limits of §5.

Entry order is not significant. The reference writer stores payload files in the order they were added,
then `manifest.json`, then `signature.json`; it stores images and weights uncompressed (they do not compress) and
deflates JSON; it sets every entry's modification time to the manifest's `created_at`. With these rules the same
inputs always produce byte-identical packs, which keeps hashes and test vectors stable. Other writers SHOULD do
the same but readers MUST NOT depend on it.

## 3. Manifest

`manifest.json` is a UTF-8 JSON object without a byte-order mark. It MUST be valid under
[`schema/manifest.schema.json`](schema/manifest.schema.json) and the cross-field rules below, which the schema cannot
express. Duplicate member names and unknown members are errors, with one exception: members whose names start
with `x-` are **extensions** that readers MUST preserve and MAY ignore. Extensions MUST NOT carry prices or payment
data (§1).

| Member | Type | Rules |
|---|---|---|
| `mvpack` | integer | Format version, `1`. |
| `kind` | string | `character`, `style`, `template`, `layout`, `series-listing` or `plugin`. |
| `id` | string | Pack id, 1–64 of `a–z 0–9 -`, starting and ending with a letter or digit. Stable across versions. |
| `name` | string | 1–80 characters on one line. |
| `version` | string | Semantic version (`1.2.0`, `2.0.0-beta.1`). World requires a new version for every submission. |
| `summary` | string | Optional, ≤ 280 characters. |
| `description` | string | Optional, ≤ 4000 characters. |
| `created_at` | string | RFC 3339 timestamp in UTC. |
| `licence` | string | A licence id from §3.2. |
| `licence_notes` | string | Optional, ≤ 500 characters (for example "credit the series, not only me"). |
| `ai_label` | object | Honest provenance, §3.3. |
| `content_rating` | string | Optional: `all-ages` (default), `teen`, `mature`. |
| `credit` | object | The creator's World name, §3.4. |
| `tags` | array | Optional, ≤ 20 unique tags of 1–32 `a–z 0–9 -`. |
| `works_with` | array | Optional, ≤ 20 unique model ids (lowercase, digits, `.`, `_`, `-`), e.g. `illustrious-xl`. |
| `character` | object | Only for `kind: character`: `from_series` (≤ 80), `trigger_word` (≤ 64), `base_model` (model id). |
| `series_listing` | object | Required for, and only allowed with, `kind: series-listing`, §3.5. |
| `plugin` | object | Required for, and only allowed with, `kind: plugin`: `{"manifest": "<path of plugin.json>"}`. |
| `files` | array | Every payload file, §3.6. |

### 3.1 Kinds

| Kind | What it is | Typical files |
|---|---|---|
| `character` | A reusable character: reference sheet, optional LoRA, profile | ≥ 1 `reference` image (required), `lora`, `profile`, `cover`, `preview` |
| `style` | A drawing style preset | `style-preset`, `preview`, optional `lora` |
| `template` | A story or page template (beats, page count, panel plans) | `template`, `preview` |
| `layout` | Panel layout geometry for one or more pages | `layout`, `preview` |
| `series-listing` | A pointer to a series hosted on the creator's own server. World lists it; it does not host chapters. | `series`, `cover`, `preview`; never weights |
| `plugin` | A manifest-only plugin (`sdk/plugin`) | `plugin-manifest` (required), `model-manifest`, `layout`, `theme`, `font` |

World's catalogue groups `template` and `layout` packs under its *Templates* tab and shows `series-listing` packs
under *Series*.

### 3.2 Licences

| Id | Meaning | Credit | Commercial use | Derivatives | May pass the pack on |
|---|---|---|---|---|---|
| `CC-BY-4.0` | Creative Commons Attribution 4.0 | yes | yes | yes | yes |
| `CC-BY-SA-4.0` | … ShareAlike 4.0 | yes | yes | same licence | yes |
| `CC-BY-NC-4.0` | … NonCommercial 4.0 | yes | no | yes | yes |
| `CC-BY-NC-SA-4.0` | … NonCommercial-ShareAlike 4.0 | yes | no | same licence | yes |
| `CC-BY-ND-4.0` | … NoDerivatives 4.0 | yes | yes | no | yes |
| `CC0-1.0` | Public domain dedication | no | yes | yes | yes |
| `MIT`, `Apache-2.0` | Open source licences (plugins, data) | yes | yes | yes | yes |
| `OFL-1.1` | SIL Open Font License (font plugins) | yes | yes | yes | yes |
| `personal` | The owner may use the item in their own works but not redistribute the pack. Usual for paid items. | yes | yes | in own works | no |
| `all-rights-reserved` | Viewing and listing only (series listings) | yes | no | no | no |

Plugin packs MUST use an open licence (not `personal` or `all-rights-reserved`). The app's licence guard uses these
flags, for example to stop a `CC-BY-NC-4.0` character from being used in a series the creator publishes
commercially.

### 3.3 AI label

`ai_label` has up to four members, `art`, `design`, `script` and `lettering`, each `human`, `ai-assisted` or `ai`.
At least one MUST be present; an absent role does not apply to the pack (a layout has no `art`). A pack is shown as
*hand-drawn* when every present role is `human`; World's `hand_drawn_only` filter relies on this.

### 3.4 Credit

```json
"credit": { "display_name": "Jerry Ink", "handle": "jerry-ink", "world_item_id": "wi_rin_asagiri" }
```

* `display_name`: 1–40 characters, no leading or trailing spaces, no control or format characters.
* `handle`: the World handle, 3–30 of `a–z 0–9 -`, starting and ending with a letter or digit, no `--`.
  Credit always uses the **World** name, never the username on the self-hosted server.
* `world_item_id`: absent for a new item; set to the World item id when submitting a new version of an approved
  item, and present in every pack World publishes.

The credit line every project that uses the pack shows is `<Kind> by <display_name> (@<handle>), <licence name>`,
for example `Character by Jerry Ink (@jerry-ink), CC BY 4.0`.

### 3.5 Series listing

`url` (required, `http` or `https`, no user info, ≤ 500 characters), `languages` (BCP 47 tags), `status`
(`ongoing`, `complete`, `hiatus`) and `chapters` (≥ 0).

### 3.6 Files

Each entry of `files` describes one payload file:

```json
{ "path": "refs/front.png", "role": "reference", "media_type": "image/png", "size": 48213,
  "sha256": "9f2c…(64 lowercase hex digits)" }
```

* `path` follows §4 and is unique (case-insensitively).
* `role` is one of the roles in §5.2 and MUST accept the file's type.
* `media_type` MUST equal the media type of the file type (§5.1).
* `size` is the exact uncompressed size in bytes; `sha256` the lowercase hex SHA-256 of the file's bytes.
* At most one file has the role `cover`.
* A `character` pack has at least one `reference` image. A `plugin` pack's `plugin.manifest` names a listed file
  with role `plugin-manifest`.

## 4. Paths

Payload paths are relative, use `/` as the separator and MUST satisfy all of:

* 1–200 bytes in total, at most 6 segments, each segment 1–100 bytes;
* only ASCII letters, digits, `.`, `_` and `-` in each segment (no spaces, no Unicode, no `\`, no `:`);
* no empty segment (so no leading `/` and no `//`), no `.` or `..` segment, no segment starting with `.`;
* no Windows device name as a segment's base name (`con`, `prn`, `aux`, `nul`, `com1`–`com9`, `lpt1`–`lpt9`,
  in any case and with any extension);
* not `manifest.json` or `signature.json`;
* a lowercase extension from the allow-list (§5.1).

These rules make zip-slip impossible by construction; readers MUST still extract through an API that cannot
leave the target directory (the reference reader uses `os.Root`) and MUST create files exclusively (no overwrite).

## 5. File types, roles and limits

### 5.1 Allow-list

| Type | Extensions | Media type | Content check (first bytes) |
|---|---|---|---|
| PNG | `.png` | `image/png` | `89 50 4E 47 0D 0A 1A 0A` |
| JPEG | `.jpg`, `.jpeg` | `image/jpeg` | `FF D8 FF` |
| WebP | `.webp` | `image/webp` | `RIFF????WEBP` |
| safetensors | `.safetensors` | `application/vnd.safetensors` | 8-byte little-endian header length *n* with 2 ≤ *n* ≤ 16 MiB and 8 + *n* ≤ size, then `{`; the header MUST parse as a JSON object |
| JSON | `.json` | `application/json` | optional whitespace then `{` or `[`; the whole file MUST be valid UTF-8 JSON without a BOM |
| TrueType | `.ttf` | `font/ttf` | `00 01 00 00` or `true` — plugin packs only |
| OpenType | `.otf` | `font/otf` | `OTTO` — plugin packs only |
| WOFF2 | `.woff2` | `font/woff2` | `wOF2` — plugin packs only |

Anything else is rejected: executables, scripts, HTML, SVG (it can carry script), archives, pickled model weights
(`.ckpt`, `.pt`, `.bin`), WebAssembly. Independently of the extension, a payload whose first bytes are an ELF,
PE/DOS (`MZ`), Mach-O, Java class, shebang (`#!`), WebAssembly (`\0asm`), ZIP, gzip, RAR or 7-Zip signature is
rejected as executable content. `series-listing` packs MUST NOT carry `.safetensors` files.

### 5.2 Roles

| Role | Accepts | Purpose |
|---|---|---|
| `cover` | image | Catalogue card image (at most one) |
| `preview` | image | Extra preview |
| `reference` | image | Character reference (sheet, turnaround, expression) |
| `lora` | safetensors | LoRA or adapter weights |
| `profile` | JSON | Character profile, personality, voice notes |
| `style-preset` | JSON | Style settings |
| `template` | JSON | Story or page template |
| `layout` | JSON | Panel layout geometry |
| `series` | JSON | Series listing details |
| `plugin-manifest` | JSON | `plugin.json` (see `sdk/plugin`) |
| `model-manifest` | JSON | A model manifest contributed by a plugin |
| `theme` | JSON | Theme tokens contributed by a plugin |
| `font` | font | A font contributed by a plugin |
| `data` | JSON | Any other JSON data |

### 5.3 Limits

Readers MUST enforce limits at least this strict (they MAY be stricter; World is) and MUST count bytes while
decompressing rather than trusting the ZIP headers.

| Limit | Default |
|---|---|
| Archive size | 3 GiB |
| Payload files | 1000 |
| Total uncompressed payload | 3 GiB |
| `manifest.json` | 256 KiB |
| `signature.json` | 1 MiB |
| Each image | 32 MiB |
| Each JSON payload | 4 MiB |
| Each `.safetensors` | 2 GiB |
| Each font | 16 MiB |
| Compression ratio | 100 : 1 for entries larger than 1 MiB uncompressed (zip-bomb guard) |
| JSON nesting depth (canonicalization) | 64 |

## 6. Canonical JSON

Signatures and hashes cover the **canonical** form of the manifest, not the bytes stored in the archive, so that
pretty-printing does not break a signature. The canonical form is the JSON Canonicalization Scheme (JCS,
RFC 8785): no insignificant whitespace, object members sorted by the UTF-16 code units of their names, strings with
minimal escaping, numbers serialised like ECMAScript's `Number.prototype.toString`. Canonicalization MUST fail on
invalid UTF-8, duplicate member names, numbers outside the IEEE-754 double range, trailing data and nesting deeper
than 64 levels. Extension members are part of the canonical manifest and therefore signed.

Every member name in `manifest.json` (outside top-level `x-` extension members), `signature.json`, `plugin.json` and
licence-token claims is lowercase snake case (`a–z`, `0–9`, `_`); readers MUST reject any other member name. This
closes a parser differential: member names that differ only in case (`"art"` and `"ART"`) are distinct for
canonicalization and for case-sensitive readers but are merged by decoders that match fields case-insensitively
(Go's `encoding/json`), so without the rule two readers could disagree about signed content such as the AI label.

## 7. Signatures

`signature.json` (schema: [`schema/signature.schema.json`](schema/signature.schema.json)):

```json
{
  "mvpack_signature": 1,
  "manifest_sha256": "<hex SHA-256 of the canonical manifest>",
  "files": [ { "path": "cover.png", "size": 142, "sha256": "d81b…" } ],
  "signatures": [
    { "key_id": "srv_12ebe61dbe09acab", "signer": "server", "alg": "ed25519",
      "public_key": "<base64 of the 32-byte key>", "signed_at": "2026-09-12T10:00:00Z",
      "sig": "<base64 of the 64-byte signature>" }
  ]
}
```

* `files` is the per-file list derived from the manifest (`path`, `size`, `sha256`), **sorted by the bytes of
  `path`**, with no duplicates. It MUST equal the manifest's list exactly.
* `signatures` holds 1–8 signatures. `key_id` is 1–64 of `A–Z a–z 0–9 _ . : -`. `signer` is `server` (the key a
  self-hosted server registered with `POST /v1/servers` when it was linked), `developer` (a plugin developer's
  release key) or `world` (World's countersignature, added on approval). `public_key` and `sig` use standard,
  padded base64.

Each signature is an ed25519 signature (RFC 8032, pure Ed25519) over this exact byte string, where `\n` is a single
line feed:

```
MVPACK-SIG-1\n
key_id <key_id>\n
signer <signer>\n
signed_at <signed_at as RFC 3339 UTC, whole seconds, e.g. 2026-09-12T10:00:00Z>\n
manifest <byte length of the canonical manifest>\n
<canonical manifest bytes>\n
files <number of files>\n
<sha256> <size> <path>\n            one line per payload file, in the sorted order above
```

The context line stops a pack signature from being replayed as any other ed25519 signature made with the same key.
Paths cannot contain spaces or line feeds (§4), so the encoding is unambiguous. Adding a signature (for example
World's) appends to `signatures` and leaves the others untouched, because none of them covers `signature.json`.

## 8. Reading and verifying

A conforming reader processes a pack in this order and stops at the first failure:

1. Check the archive size, then read the ZIP central directory only. Reject encrypted entries, other compression
   methods, special files, too many entries, case-insensitive duplicates and unsafe paths (§2, §4).
2. Read `manifest.json` (bounded), canonicalize it (§6), parse it strictly and validate it (§3). Parse
   `signature.json` if present.
3. Check that the set of payload entries equals the manifest's `files`, that each ZIP header size equals the declared
   size, that every size is within §5.3 and that the compression ratio is plausible.
4. Stream each payload: check the first bytes (§5.1), count bytes (more or fewer than declared is an error), hash
   with SHA-256 and compare at EOF; validate JSON payloads and safetensors headers completely.
5. If `signature.json` exists: check `manifest_sha256` against the canonical manifest, check that `files` equals the
   manifest's list, then verify **every** signature. One invalid signature fails the whole pack, even when another
   one is valid.
6. Decide trust: a signature is *trusted* when its `key_id` resolves, through the reader's key set, to the same
   public key it carries — and, when the key set records a role per key (it SHOULD whenever it mixes World, server
   and developer keys), when the signature's `signer` equals that role. `signer` is otherwise only a claim made by
   the key holder. `signed_at` MUST be RFC 3339 UTC whole seconds (`2026-09-12T10:00:00Z`), the exact form the
   signing payload uses. A self-hosted server importing from World requires a trusted `world` signature (World's
   keys are published at `GET /v1/keys`); World requires a trusted `server` signature by a key registered to the
   submitting account; a local import MAY accept unsigned or untrusted packs after asking the person.
7. Only then extract, into a fresh directory, and move it into place on success.

Errors SHOULD be reported with a precise reason; the reference implementation wraps one sentinel error per class
(`ErrUnsafePath`, `ErrDisallowedFile`, `ErrExecutable`, `ErrZipBomb`, `ErrHashMismatch`, `ErrBadSignature`,
`ErrUntrustedKey`, …) so the app can show "rejected: `refs/a.png` does not match its SHA-256".

## 9. Licence tokens

World proves that an account may use an item with a **licence token**, returned by `GET /v1/library` and
`GET /v1/library/{id}/download` and kept by the app next to the imported item:

```
mvlt1.<base64url(JSON claims)>.<base64url(ed25519 signature)>
```

Both parts are unpadded base64url. The signature covers the ASCII bytes `mvlt1.<payload>` and is made with a World
key published at `GET /v1/keys` (`use: licence-tokens`). Claims: `v` (1), `kid`, `item_id`, `account_id`, `handle`,
`version` (the version the token was issued for; it stays valid for later versions), `licence`, `acquisition`
(`free`, `paid` or `creator`) and `iat` (Unix seconds). A licence token never contains a price. Readers accept
tokens only from keys whose role is `world`.

## 10. Versioning

`mvpack` and `mvpack_signature` are integers. A reader MUST reject versions it does not know. Additions that old
readers can safely ignore go into `x-` extensions; everything else needs a new format version, in which the
signing context line changes too (`MVPACK-SIG-2`).

## 11. Test vectors

`testdata/` holds packs and their manifests and signature files for each kind, signed with the test key in
`testdata/keys.json` (key id `srv_testdata`, public key only; the private key is derived in `helpers_test.go` and is
publicly known). `go test ./sdk/mvpack` checks that the reference implementation reproduces them byte for byte, and
`python3 sdk/mvpack/schema/validate_examples.py` checks the JSON Schemas against the valid and invalid example
manifests. An independent implementation SHOULD verify the same packs and reject the same invalid manifests.
