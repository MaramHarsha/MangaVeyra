import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ScriptEditor } from './script';

const html = `
<div data-story-views>
  <header>
    <input name="scene.s1.title" value="Rooftop chase" form="mv-c1-script">
    <span data-save-state data-saved="All changes saved" data-saving="Saving…" data-dirty="Unsaved changes"
      data-failed="Not saved · retrying" data-conflict="Changed elsewhere · reload" data-locked="Chapter locked"
      data-invalid="Not saved · a field is not valid">All changes saved</span>
    <form data-make-pages><input type="hidden" name="revision" value="r1" data-revision></form>
  </header>
  <form class="mv-c1__script" id="mv-c1-script" action="/studio/p/p1/story/c1/script" method="post" data-autosave>
    <input type="hidden" name="revision" value="r1" data-revision>
    <input type="hidden" name="scene" value="s1">
    <div data-panel="p1" data-panel-label="7.1">
      <button type="submit" name="add_line" value="p1" data-add-line="p1">+</button>
      <div data-lines="p1">
        <div class="mv-c1__line" data-line="p1.0" data-kind="speech">
          <input name="panel.p1.dialogue.0.speaker" value="Kaito"><textarea name="panel.p1.dialogue.0.text">Late.</textarea>
        </div>
      </div>
      <div data-line-controls="p1">
        <div class="mv-c1__linectl" data-line="p1.0">
          <select name="panel.p1.dialogue.0.kind" data-line-kind="p1.0"><option value="speech" selected>Speech</option><option value="caption">Caption</option></select>
          <button type="submit" name="remove_line" value="p1.0" data-remove-line="p1.0">x</button>
        </div>
      </div>
    </div>
    <template data-line-template>
      <div class="mv-c1__line" data-line="__PANEL__.__INDEX__" data-kind="speech">
        <label for="speaker-__PANEL__-__INDEX__">Panel __LABEL__ · Dialogue __NUMBER__</label>
        <input id="speaker-__PANEL__-__INDEX__" name="panel.__PANEL__.dialogue.__INDEX__.speaker" value="">
        <textarea name="panel.__PANEL__.dialogue.__INDEX__.text"></textarea>
      </div>
      <div class="mv-c1__linectl" data-line="__PANEL__.__INDEX__">
        <select name="panel.__PANEL__.dialogue.__INDEX__.kind" data-line-kind="__PANEL__.__INDEX__"><option value="speech">Speech</option></select>
      </div>
    </template>
  </form>
</div>`;

type Reply = { status: number; body: unknown };

function fakeFetch(replies: Reply[]) {
  return vi.fn(async () => {
    const r = replies.shift() ?? { status: 200, body: { state: 'saved', revision: 'r-last' } };
    return new Response(JSON.stringify(r.body), { status: r.status, headers: { 'Content-Type': 'application/json' } });
  });
}

let form: HTMLFormElement;
const state = () => document.querySelector<HTMLElement>('[data-save-state]')!;
const field = (name: string) => form.elements.namedItem(name) as HTMLInputElement | HTMLTextAreaElement;
const type = (name: string, value: string) => {
  const el = field(name);
  el.value = value;
  el.dispatchEvent(new Event('input', { bubbles: true }));
};

beforeEach(() => {
  vi.useFakeTimers();
  document.body.innerHTML = html;
  form = document.querySelector('form.mv-c1__script')!;
});
afterEach(() => vi.useRealTimers());

describe('C1 script editor', () => {
  it('autosaves after a pause with the held revision and takes the new one back', async () => {
    const fetch = fakeFetch([{ status: 200, body: { state: 'saved', revision: 'r2' } }]);
    const saved = vi.fn();
    form.addEventListener('mv:form-saved', saved);
    new ScriptEditor(form, { delay: 500, fetch });
    type('panel.p1.dialogue.0.text', 'You are late. Again.');
    type('scene.s1.title', 'Rooftop chase at dusk');
    expect(state().textContent).toBe('Unsaved changes');
    await vi.advanceTimersByTimeAsync(499);
    expect(fetch).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(fetch).toHaveBeenCalledTimes(1);
    const [url, init] = fetch.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toContain('/studio/p/p1/story/c1/script');
    expect((init.headers as Record<string, string>)['X-MV-Autosave']).toBe('1');
    const body = init.body as URLSearchParams;
    expect(body.get('revision')).toBe('r1');
    expect(body.get('scene.s1.title')).toBe('Rooftop chase at dusk');
    expect(body.get('panel.p1.dialogue.0.text')).toBe('You are late. Again.');
    expect(body.has('add_line')).toBe(false);
    await vi.runAllTimersAsync();
    expect(state().textContent).toBe('All changes saved');
    expect(saved).toHaveBeenCalledTimes(1);
    document.querySelectorAll<HTMLInputElement>('[data-revision]').forEach((input) => expect(input.value).toBe('r2'));
  });

  it('stops on a conflict and offers a reload, and never overwrites', async () => {
    const fetch = fakeFetch([{ status: 409, body: { state: 'conflict' } }]);
    new ScriptEditor(form, { delay: 10, fetch });
    type('panel.p1.dialogue.0.text', 'Mine');
    await vi.runAllTimersAsync();
    expect(state().querySelector('a')?.textContent).toBe('Changed elsewhere · reload');
    type('panel.p1.dialogue.0.text', 'Mine again');
    await vi.runAllTimersAsync();
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(form.hasAttribute('data-autosave')).toBe(false);
  });

  it('retries after a network failure and reports an invalid field without retrying', async () => {
    const fetch = vi.fn()
      .mockRejectedValueOnce(new TypeError('offline'))
      .mockResolvedValueOnce(new Response('{"state":"invalid"}', { status: 422 }));
    new ScriptEditor(form, { delay: 10, retry: 100, fetch });
    type('panel.p1.dialogue.0.text', 'Mine');
    await vi.advanceTimersByTimeAsync(10);
    expect(state().textContent).toBe('Not saved · retrying');
    await vi.advanceTimersByTimeAsync(100);
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(state().textContent).toBe('Not saved · a field is not valid');
    await vi.runAllTimersAsync();
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  it('adds a line from the template, removes lines and saves the removal', async () => {
    const fetch = fakeFetch([]);
    const editor = new ScriptEditor(form, { delay: 10, fetch });
    form.querySelector<HTMLButtonElement>('[data-add-line]')!.click();
    const added = form.querySelector<HTMLElement>('.mv-c1__line[data-line="p1.1"]')!;
    expect(added).not.toBeNull();
    expect(added.querySelector('label')?.textContent).toBe('Panel 7.1 · Dialogue 2');
    expect(document.activeElement).toBe(added.querySelector('input'));
    expect(form.querySelector('.mv-c1__linectl[data-line="p1.1"]')).not.toBeNull();
    expect(fetch).not.toHaveBeenCalled(); // an empty line has nothing to save yet

    form.querySelector<HTMLButtonElement>('[data-remove-line="p1.0"]')!.click();
    expect(form.querySelector('[data-line="p1.0"]')).toBeNull();
    await vi.runAllTimersAsync();
    expect(fetch).toHaveBeenCalledTimes(1);
    const body = (fetch.mock.calls[0] as unknown as [string, RequestInit])[1].body as URLSearchParams;
    expect(body.has('panel.p1.dialogue.0.text')).toBe(false);
    expect(body.get('panel.p1.dialogue.1.kind')).toBe('speech');
    expect(editor.state).toBe('saved');
  });

  it('shows Caption for caption lines when the line type changes', () => {
    new ScriptEditor(form, { delay: 10, fetch: fakeFetch([]) });
    const select = form.querySelector<HTMLSelectElement>('select[data-line-kind="p1.0"]')!;
    select.value = 'caption';
    select.dispatchEvent(new Event('change', { bubbles: true }));
    expect(form.querySelector<HTMLElement>('.mv-c1__line[data-line="p1.0"]')!.dataset.kind).toBe('caption');
  });

  it('saves at once instead of posting the page when a field submits the form', async () => {
    const fetch = fakeFetch([]);
    new ScriptEditor(form, { delay: 10_000, fetch });
    const event = new Event('submit', { bubbles: true, cancelable: true });
    form.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(true);
    await vi.advanceTimersByTimeAsync(0);
    expect(fetch).toHaveBeenCalledTimes(1);
  });
});
