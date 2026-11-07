import { describe, expect, it } from 'vitest';
import { updateExportLabel, updateExportPreview } from './export';

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

  it('moves every proof, filmstrip and canvas alike, to each newly labelled content language', () => {
    const languages: Record<string, string> = { en: 'English (original)', ko: 'Korean', 'zh-Hans': 'Chinese (Simplified)', 'zh-Hant': 'Chinese (Traditional)', hi: 'Hindi' };
    const options = Object.entries(languages).map(([tag, label]) => `<option value="${tag}">${label}</option>`).join('');
    document.body.innerHTML = `<div class="mv-exportui"><div class="mv-exportui__editor"><figure><img data-export-preview src="/export/preview/p1?lang=en"></figure><div class="mv-exportui__canvas"><img data-export-preview src="/export/preview/p1?lang=en"></div></div><select data-export-language>${options}</select></div>`;
    const select = document.querySelector('select')!;
    for (const tag of Object.keys(languages)) {
      select.value = tag;
      select.dispatchEvent(new Event('change', { bubbles: true }));
      const langs = Array.from(document.querySelectorAll<HTMLImageElement>('[data-export-preview]'), image => new URL(image.src).searchParams.get('lang'));
      expect(langs).toEqual([tag, tag]);
    }
    expect(select.selectedOptions[0].textContent).toBe('Hindi');
  });

  it('never sends a proof language to an external URL or an unrelated form', () => {
    document.body.innerHTML = `<section class="mv-exportui"><img data-export-preview src="https://unconfigured.invalid/preview"><select><option value="ja">日本語</option></select></section>`;
    updateExportPreview(document.querySelector('select')!);
    expect(document.querySelector('img')!.src).toBe('https://unconfigured.invalid/preview');
  });
});

const exportForm = (checked: string) => `<div class="mv-exportui"><form class="mv-exportui__form">
  ${['png', 'pdf', 'epub', 'video', 'cbz'].map(f => `<label><input type="radio" name="format" value="${f}"${f === checked ? ' checked' : ''}${f === 'epub' || f === 'video' ? ' disabled' : ''}></label>`).join('')}
  <button type="submit" data-export-submit="true"><span class="mv-exportui__go" data-export-label="png">Export PNG</span><span class="mv-exportui__go is-current" data-export-label="pdf">Export PDF</span><span class="mv-exportui__go" data-export-label="cbz">Export CBZ</span><span class="mv-exportui__go" data-export-label="">Export</span></button>
</form></div>`;
const current = () => Array.from(document.querySelectorAll('.mv-exportui__go.is-current'), label => label.textContent);
const choose = (format: string) => {
  const radio = document.querySelector<HTMLInputElement>(`[name=format][value=${format}]`)!;
  radio.checked = true;
  radio.dispatchEvent(new Event('change', { bubbles: true }));
};

describe('export button label', () => {
  it('names the chosen format and keeps exactly one label current', () => {
    document.body.innerHTML = exportForm('pdf');
    expect(current()).toEqual(['Export PDF']);
    choose('png');
    expect(current()).toEqual(['Export PNG']);
    choose('cbz');
    expect(current()).toEqual(['Export CBZ']);
    choose('pdf');
    expect(current()).toEqual(['Export PDF']);
  });

  it('falls back to the generic label for a format the button cannot name', () => {
    document.body.innerHTML = exportForm('pdf');
    const epub = document.querySelector<HTMLInputElement>('[name=format][value=epub]')!;
    epub.checked = true;
    updateExportLabel(epub);
    expect(current()).toEqual(['Export']);
  });

  it('ignores unchecked radios and format radios outside the export screen', () => {
    document.body.innerHTML = exportForm('pdf');
    updateExportLabel(document.querySelector<HTMLInputElement>('[name=format][value=png]')!);
    expect(current()).toEqual(['Export PDF']);
    document.body.innerHTML = `<form><input type="radio" name="format" value="png"><button data-export-submit><span class="mv-exportui__go is-current" data-export-label="pdf">Export PDF</span><span class="mv-exportui__go" data-export-label="png">Export PNG</span></button></form>`;
    choose('png');
    expect(current()).toEqual(['Export PDF']);
  });
});
