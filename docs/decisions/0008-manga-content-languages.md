# ADR 0008: Manga content languages and lettering fonts

- Status: Accepted for Phase 1
- Date: 2026-11-07
- Boundary: `internal/pagedoc/languages.go` (contract), `internal/lettering`, consumed by studio, gen, editor,
  compose and export

## Context

Creators must write, letter and export manga in English, Japanese, Korean, Simplified Chinese, Traditional Chinese,
Spanish, French, German, Portuguese and Hindi with real glyphs everywhere. Every glyph is drawn on the server (the
editor canvas and SVG proof receive server-shaped outlines; previews and PNG/CBZ/PDF exports compose them), so the
binary must carry the fonts. The interface itself stays English and Japanese; this decision covers content only.

## Decisions

1. **Language set.** `pagedoc.ContentLanguages` (`en, ja, ko, zh-Hans, zh-Hant, es, fr, de, pt, hi`) is the only
   list. Projects store the canonical tag from `NormalizeContentLanguage` (`zh-TW` → `zh-Hant`, `pt-BR` → `pt`) and
   reject other languages. Bubble text falls back exact tag → same language under another spelling →
   `ContentLanguages` order. Vertical lettering is honoured only where `VerticalAllowed` (ja, ko, zh-Hans, zh-Hant);
   new and auto-placed lettering is vertical where `VerticalByDefault` (ja, zh-Hant).
2. **Fonts.** All are SIL OFL 1.1 and embedded; provenance, hashes and licence texts are in
   `internal/lettering/fonts/LICENSES.md`. Keys: Comic Neue Bold (`comic`), Bangers (`sfx`), Noto Sans JP/KR/SC/TC
   Black (`jp`/`ko`/`sc`/`tc`) and Noto Sans Devanagari Black, full build (`deva`). The new Noto files are the
   official noto-cjk SubsetOTF v2.004 and notofonts Devanagari v2.007 files converted to unhinted WOFF2 with every
   code point kept: KR 2,295,308 B, SC 4,896,476 B, TC 3,379,580 B, Devanagari 58,340 B. Total +10.6 MB; the binary
   grows from 44.7 MB to 55.4 MB. The CJK and Devanagari fonts load lazily on first use.
3. **Default font key.** ja→`jp`, ko→`ko`, zh-Hans→`sc`, zh-Hant→`tc`, hi→`deva`; Latin languages use `sfx` for SFX
   bubbles and `comic` otherwise. An explicit style font wins. The editor offers all seven keys.
4. **Fallback.** Text is split into grapheme clusters, and each cluster takes the first bundled font that has all of
   its glyphs. Han: the language's regional font, then the bubble's font if it is CJK, then the language's CJK order.
   Kana and CJK punctuation: language CJK font, bubble CJK font, then `jp`. Hangul: `ko` first. Devanagari: `deva`.
   Latin and everything else: bubble font, language font, `comic`, `sfx`, `deva` (whose Latin covers letters such as
   Ÿ), then the CJK fonts. Characters without a script of their own take the script of the preceding text. HarfBuzz
   (go-text/typesetting) shapes each run once, and measurement, auto-fit, wrapping and drawing use the same glyphs.
   Supported scripts never emit `.notdef` (tested); unsupported scripts may.
5. **Line breaking.** Unicode line-break rules, never inside a grapheme or shaping cluster. Kinsoku: no line start on
   `，。、．！？；：」』）》〉】〕ー…‥・々` and small kana; no line end on `「『（《〈【〔`. Korean and Hindi wrap at spaces
   and split between clusters only for a word longer than the line. A kinsoku conflict lets the line run long and
   auto-fit shrinks the text.
6. **Vertical text.** CJK stays upright and uses the fonts' `vert` forms: ja, ko and zh-Hans move 、。 to the top
   right; zh-Hant keeps them centred (the Taiwan convention). The long-vowel mark, dashes and brackets without a
   `vert` form rotate 90°. Latin is set sideways. Columns run right to left and share one top edge; ruby sits to the
   right of its column.
7. **Offline scripts and exports.** The placeholder text provider writes deterministic, original template scripts
   with dialogue in the project language, so pages-from-script auto-letters without any provider key. Export
   filenames keep Unicode letters, marks and digits (80 runes) and are sent with an ASCII fallback plus
   `filename*=UTF-8''…`; ComicInfo `LanguageISO` is the canonical tag; the credits page exists in all ten languages.
8. **Shape fitting.** Auto-fit wraps each row against the bubble's available width at that row, using a safe inner
   ellipse for speech, whisper, cloud and burst outlines, and an inner rectangle for captions and SFX. It checks the
   drawn glyph bounds, ruby and text stroke before accepting a size. Vertical columns keep their shared top edge.
   Text that still cannot fit at the minimum size retains the existing explicit overflow state.

## Consequences

The binary grows by about 10.7 MB, and one embedded font set serves the editor, the proof and every export
identically. Auto-fit decides each size from cached glyph metrics and outline boxes and draws only the chosen size;
each bubble has a fixed work budget, and only per-bubble shaping and the final drawing hold the engine mutex, so other
layouts interleave between bubbles. Page layout honours request cancellation.
Adding a script needs a new font key, fallback entry and tests; adding an interface locale is a separate decision.
