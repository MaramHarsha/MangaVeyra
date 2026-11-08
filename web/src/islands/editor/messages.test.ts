import {describe, it, expect} from 'vitest';
import {readFileSync} from 'node:fs';

// Every message key the editor island asks for must exist in both editor
// catalogues, or the creator sees a raw key such as "variations.open_queue".
const sources = import.meta.glob('./*.{svelte,ts}', {query: '?raw', import: 'default', eager: true}) as Record<string, string>;
// The catalogues live outside the Vite root, so they are read from disk (a
// plain concatenation keeps Vite from turning the URL into an asset import).
const locales = '../../../../internal/i18n/locales/';
const catalogue = (lang: string) => JSON.parse(readFileSync(new URL(locales + lang + '/editor.json', import.meta.url), 'utf8')) as Record<string, string>;
const catalogues = {en: catalogue('en'), ja: catalogue('ja')};

/** firstArgument returns the source text of the first argument of the call starting at `open` ("("). */
function firstArgument(src: string, open: number): string {
  let depth = 0, quote = '';
  for (let i = open + 1; i < src.length; i++) {
    const c = src[i];
    if (quote) { if (c === '\\') i++; else if (c === quote) quote = ''; continue; }
    if (c === '\'' || c === '"' || c === '`') { quote = c; continue; }
    if ('([{'.includes(c)) depth++;
    else if (')]}'.includes(c)) { if (depth === 0) return src.slice(open + 1, i); depth--; }
    else if (c === ',' && depth === 0) return src.slice(open + 1, i);
  }
  return '';
}

/** literalKeys lists the plain string literals passed as the key of every t(...) call. */
export function literalKeys(src: string): string[] {
  const keys = new Set<string>();
  for (const m of src.matchAll(/(?<![\w.$])t\(/g)) {
    const arg = firstArgument(src, m.index! + 1);
    for (const lit of arg.matchAll(/'([^'\\]+)'|"([^"\\]+)"|`([^`$\\]+)`/g)) {
      const key = lit[1] ?? lit[2] ?? lit[3];
      // Operands of a comparison (kind === 'draw') are values, not keys.
      const before = arg.slice(0, lit.index).trimEnd(), after = arg.slice(lit.index! + lit[0].length).trimStart();
      if (/[=!]==?$/.test(before) || /^[=!]==?/.test(after)) continue;
      if (/^[a-z][\w.]*$/.test(key)) keys.add(key);
    }
  }
  return [...keys];
}

// Keys built from a template literal, listed with every value the island passes.
const dynamic = [
  ...['upload', 'draw', 'camera', 'screentone', 'grey', 'colour', 'history_inspector', 'sketch_title', 'lockfix_title'].map(k => `control.${k}`),
  ...['identity', 'outfit', 'both'].map(k => `control.evidence_${k}`),
  ...['front', 'side', 'back', 'face', 'expression', 'outfit', 'sheet', 'other'].map(k => `control.ref_${k}`),
  ...['failed', 'canceled'].map(k => `job.${k}`),
  ...['generate_page', 'character_repair', 'revert', 'save', 'lock'].map(k => `history.action.${k}`),
];

function has(lang: keyof typeof catalogues, key: string): boolean {
  const c = catalogues[lang], full = `editor.${key}`;
  return full in c || (`${full}.one` in c && `${full}.other` in c);
}

describe('editor message keys', () => {
  it('finds keys in ternaries and skips dynamic templates', () => {
    expect(literalKeys("x=t(kind==='draw'?'control.a':`control.${kind}`,{n:1});w=t('b');y=at('no');z=o.t('no')").sort()).toEqual(['b', 'control.a']);
  });

  it('exist in the English and Japanese catalogues', () => {
    const missing: string[] = [];
    for (const [file, src] of Object.entries(sources)) {
      if (file.endsWith('.test.ts')) continue;
      for (const key of literalKeys(src)) for (const lang of ['en', 'ja'] as const) if (!has(lang, key)) missing.push(`${lang}: ${key} (${file})`);
    }
    for (const key of dynamic) for (const lang of ['en', 'ja'] as const) if (!has(lang, key)) missing.push(`${lang}: ${key} (dynamic)`);
    expect(missing).toEqual([]);
  });

  it('keep the same keys in both catalogues', () => {
    expect(Object.keys(catalogues.ja).sort()).toEqual(Object.keys(catalogues.en).sort());
  });

  it('give every English count message singular and plural forms', () => {
    const bare = Object.entries(catalogues.en).filter(([k, v]) => /\{count\}/.test(v) && !/\.(one|other)$/.test(k) && /\{count\} (?:[a-z]+s)\b/.test(v)).map(([k]) => k);
    expect(bare).toEqual([]);
  });
});
