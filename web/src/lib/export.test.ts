import { describe, expect, it } from 'vitest';
import { updateExportPreview } from './export';

describe('export proof language', () => {
  it('updates the matching proof without dropping its page or other query state', () => {
    document.body.innerHTML = `<section class="mv-exportui"><img data-export-preview src="/chapter/preview/page-1?lang=en&proof=1"><select data-export-language><option value="ja">日本語</option></select></section><img id="unrelated" src="/other.png">`;
    const select = document.querySelector('select')!;
    select.dispatchEvent(new Event('change', { bubbles: true }));
    const src = new URL(document.querySelector('img')!.src);
    expect(src.pathname).toBe('/chapter/preview/page-1');
    expect(src.searchParams.get('lang')).toBe('ja');
    expect(src.searchParams.get('proof')).toBe('1');
    expect(document.getElementById('unrelated')?.getAttribute('src')).toBe('/other.png');
  });

  it('never sends a proof language to an external URL or an unrelated form', () => {
    document.body.innerHTML = `<section class="mv-exportui"><img data-export-preview src="https://unconfigured.invalid/preview"><select><option value="ja">日本語</option></select></section>`;
    updateExportPreview(document.querySelector('select')!);
    expect(document.querySelector('img')!.src).toBe('https://unconfigured.invalid/preview');
  });
});
