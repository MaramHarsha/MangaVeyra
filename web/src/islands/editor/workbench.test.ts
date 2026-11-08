import {describe, it, expect, vi, afterEach} from 'vitest';
// Vitest resolves `svelte` to its server build; components need the client runtime.
vi.mock('svelte', async () => await import(/* @vite-ignore */ ('../../../node_modules/svelte/src/index-client.js' as string)));
import {mount, unmount, flushSync} from 'svelte';
import {readFileSync} from 'node:fs';
import ControlWorkbench from './ControlWorkbench.svelte';
import {plural} from './document';
import {reactiveProps} from './test-props.svelte';
import type {Job, Page, Version} from './types';

const locales = '../../../../internal/i18n/locales/';
const en = JSON.parse(readFileSync(new URL(locales + 'en/editor.json', import.meta.url), 'utf8')) as Record<string, string>;
const messages = Object.fromEntries(Object.entries(en).map(([k, v]) => [k.replace(/^editor\./, ''), v]));
// The same lookup Editor.svelte uses, including ".one"/".other" plural forms.
const t = (key: string, args: Record<string, string | number> = {}) => Object.entries(args).reduce((s, [k, v]) => s.replaceAll(`{${k}}`, String(v)), typeof args.count === 'number' && !(key in messages) ? plural(messages, key, args.count, 'en') : messages[key] || key);
const api = '/api/v1/editor/p/project/chapters/chapter';

function page(version = 3): Page {
  return {schema: 'mangaveyra.page.v1', id: 'p', project_id: 'project', chapter_id: 'chapter', number: 1, width: 600, height: 800, direction: 'rtl', locked: false, version,
    panels: [{id: 'a', order: 0, points: [{x: 0, y: 0}, {x: 600, y: 0}, {x: 600, y: 800}, {x: 0, y: 800}], prompt: 'Rin runs.', prompt_source: 'script', locked: false,
      layers: [{id: 'L', kind: 'character', character_id: 'rin', order: 1, visible: true, locked: false, opacity: 1, transform: {}, provenance: 'ai', asset: {id: 'x', url: '/after.png'}}]}],
    bubbles: []};
}
const quote = (layers = 1) => ({quote: {provider_id: 'builtin', model_id: 'placeholder', placeholder: true, estimate: {}, layers}, model_name: 'Placeholder art'});
const drift = (version: number, o: Record<string, unknown> = {}) => ({page_id: 'p', panel_id: 'a', layer_id: 'L', version, chapter_id: 'chapter', page_number: 1, panel_number: 1, asset_id: 'x', url: '', score: .78, identity: .78, locked: false, ...o});
const lock = (version: number, o: Record<string, unknown> = {}) => ({status: {character_id: 'rin', status: 'assessed', score: .8, threshold: .9, panels: 1, references: 1, unscored: 0, can_train: false, can_score: true, training_progress: 0, drift: [drift(version)], drift_total: 1, ...o}, name: 'Rin Asagiri', references: [{id: 'r1', url: '/ref.png', kind: 'front'}]});

interface Call {method: string; url: string; body: Record<string, unknown> | undefined}
type Route = (c: Call) => {status: number; body: unknown} | undefined;
function server(route: Route): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    const c: Call = {method: init.method || 'GET', url: String(url), body: typeof init.body === 'string' ? JSON.parse(init.body) : undefined};
    calls.push(c);
    const out = route(c) || {status: 404, body: {error: {code: 'not_found', message: 'We could not find that page.'}}};
    return new Response(JSON.stringify(out.body), {status: out.status, headers: {'Content-Type': 'application/json'}});
  }));
  return calls;
}
async function settle() { for (let i = 0; i < 12; i++) { await new Promise(r => setTimeout(r, 0)); flushSync(); } }

let cleanup: (() => void) | null = null;
afterEach(() => { cleanup?.(); cleanup = null; vi.unstubAllGlobals(); vi.restoreAllMocks(); document.body.replaceChildren(); });

function render(o: Record<string, unknown>) {
  const target = document.createElement('div');
  document.body.append(target);
  const props = reactiveProps({kind: 'lockfix', page: page(), api, lang: 'en', chapter: 2, characterID: 'rin', characters: [{id: 'rin', name: 'Rin Asagiri'}, {id: 'kaito', name: 'Kaito Mbeki'}], writable: true, blocked: '', activeJob: null as Job | null, notice: '', conflict: false, t,
    onClose: vi.fn(), onRestore: vi.fn(), onJob: vi.fn(), onDirty: vi.fn(), onReload: vi.fn(), onDismiss: vi.fn(), onCharacter: vi.fn(), ...o});
  const app = mount(ControlWorkbench, {target, props});
  cleanup = () => unmount(app);
  return {target, props};
}
const text = (el: Element | null | undefined) => el?.textContent?.replace(/\s+/g, ' ').trim() || '';
const button = (root: Element, name: string) => [...root.querySelectorAll('button')].find(b => text(b) === name || b.getAttribute('aria-label') === name);

describe('D12 Character Lock fix', () => {
  it('reloads target revisions after a repair before quoting again, so no false conflict appears', async () => {
    let version = 3;
    const calls = server(c => {
      if (c.url === `${api}/lock/rin`) return {status: 200, body: lock(version)};
      if (c.url === `${api}/lock/quote`) {
        const targets = c.body?.targets as {version: number}[];
        return targets[0].version === version ? {status: 200, body: quote()} : {status: 409, body: {error: {message: 'This page changed elsewhere.'}}};
      }
      if (c.url === `${api}/lock/repair`) { version = 4; return {status: 202, body: {job: {id: 'j1', type: 'character.repair', state: 'queued', progress: 0}}}; }
      if (c.url === `${api}/pages/p`) return {status: 200, body: {page: page(version)}};
    });
    const {target, props} = render({});
    await settle();
    expect(text(button(target, 'Fix 1 panel'))).toBe('Fix 1 panel');
    expect(text(target.querySelector('.mv-route__pick'))).toBe('→ Placeholder art · built-in · no remote spend');
    button(target, 'Fix 1 panel')!.click();
    await settle();
    expect(props.onJob).toHaveBeenCalledTimes(1);
    // The editor follows the job and installs the repaired page.
    props.activeJob = {id: 'j1', type: 'character.repair', state: 'running', progress: .5};
    props.page = page(4);
    await settle();
    props.activeJob = {id: 'j1', type: 'character.repair', state: 'done', progress: 1};
    await settle();
    const quotes = calls.filter(c => c.url === `${api}/lock/quote`).map(c => (c.body?.targets as {version: number}[])[0].version);
    expect(quotes).toEqual([3, 4]);
    expect(target.querySelector('[role=alert]')).toBeNull();
    expect(button(target, 'Fix 1 panel')?.disabled).toBe(false);
    expect(text(target.querySelector('.mv-control__after figcaption'))).toBe('After fix · Lock Unassessed');
    expect(text(target.querySelector('.mv-control__after .mv-control__tag'))).toBe('Only Rin’s layer regenerated');
    expect(text(target.querySelector('.mv-btn--quiet'))).toBe('Open queue');
  });

  it('asks for a selection instead of reporting an unavailable estimate', async () => {
    const calls = server(c => c.url === `${api}/lock/rin` ? {status: 200, body: lock(3)} : c.url === `${api}/lock/quote` ? {status: 200, body: quote()} : undefined);
    const {target} = render({});
    await settle();
    (target.querySelector('.mv-control__drift input[type=checkbox]') as HTMLInputElement).click();
    await settle();
    expect(text(target.querySelector('.mv-route__pick'))).toBe('→ Select panels to fix');
    expect(button(target, 'Fix 0 panels')?.disabled).toBe(true);
    expect(target.querySelector('[role=alert]')).toBeNull();
    expect(button(target, 'Refresh')).toBeUndefined();
    expect(calls.filter(c => c.url.endsWith('/lock/quote'))).toHaveLength(1);
    // The D12 helper about layers belongs to D4 only.
    expect(target.textContent).not.toContain('lettering kept');
  });

  it('shows measured evidence on the proofs and translated captions', async () => {
    server(c => c.url === `${api}/lock/rin` ? {status: 200, body: lock(3)} : c.url === `${api}/lock/quote` ? {status: 200, body: quote()} : undefined);
    const {target} = render({});
    await settle();
    expect(text(target.querySelector('.mv-ed__inspector-head strong'))).toBe('Character Lock · Rin Asagiri');
    expect(text(target.querySelector('.mv-control__before figcaption'))).toBe('Panel 1.1 · Lock 78% · drift');
    expect(target.querySelector('.mv-control__before--drift')).not.toBeNull();
    expect(text(target.querySelector('.mv-control__drift.selected'))).toContain('1.1');
    expect(text(target.querySelector('.mv-control__refs figcaption'))).toBe('Front');
  });

  it('describes an unassessed chapter honestly', async () => {
    server(c => c.url === `${api}/lock/rin` ? {status: 200, body: lock(3, {score: undefined, status: 'unscored', drift: [], drift_total: 0, unscored: 3})} : undefined);
    const {target} = render({});
    await settle();
    expect(text(target.querySelector('.mv-ed__inspector-head small'))).toBe('3 panels in ch. 2 not assessed yet');
    expect(text(target.querySelector('.mv-control__before figcaption'))).toBe('Before · not assessed');
    expect(target.querySelector('.mv-control__before--drift')).toBeNull();
    expect(text(target.querySelector('.mv-route__pick'))).toBe('→ Select panels to fix');
  });

  it('offers the project characters when the requested character is missing', async () => {
    server(c => c.url === `${api}/lock/gone` ? {status: 404, body: {error: {message: 'We couldn’t find that character in this project.'}}} : c.url === `${api}/lock/kaito` ? {status: 200, body: {...lock(3), name: 'Kaito Mbeki'}} : c.url === `${api}/lock/quote` ? {status: 200, body: quote()} : undefined);
    const {target, props} = render({characterID: 'gone'});
    await settle();
    expect(text(target.querySelector('.mv-ed__inspector-head strong'))).toBe('Character Lock');
    expect(target.textContent).not.toContain('We could not find that page');
    expect(target.textContent).toContain(messages['control.character_missing']);
    const select = target.querySelector('#lock-character') as HTMLSelectElement;
    expect([...select.options].map(o => o.textContent)).toEqual(['Choose a character', 'Rin Asagiri', 'Kaito Mbeki']);
    select.value = 'kaito';
    select.dispatchEvent(new Event('change', {bubbles: true}));
    await settle();
    expect(props.onCharacter).toHaveBeenCalledWith('kaito');
    expect(text(target.querySelector('.mv-ed__inspector-head strong'))).toBe('Character Lock · Kaito Mbeki');
  });
});

describe('D4 Sketch to manga', () => {
  it('stays disabled with the lock reason on a locked panel', async () => {
    const calls = server(() => ({status: 200, body: quote(3)}));
    const {target} = render({kind: 'sketch', blocked: messages['control.blocked_panel']});
    await settle();
    expect(button(target, 'Ink this page')?.disabled).toBe(true);
    expect(text(target.querySelector('.mv-control__blocked'))).toBe(messages['control.blocked_panel']);
    expect(text(target.querySelector('.mv-route__pick'))).toBe('→ Not available');
    expect(calls).toHaveLength(0);
    expect(button(target, 'Upload')?.disabled).toBe(true);
  });

  it('names the size limits when a sketch is outside them', async () => {
    server(() => ({status: 200, body: quote(1)}));
    vi.stubGlobal('createImageBitmap', vi.fn(async () => ({width: 5000, height: 300, close() {}})));
    const {target} = render({kind: 'sketch'});
    await settle();
    expect(text(target.querySelector('.mv-control__actions .mv-ed__help'))).toBe('Art only · 1 layer · lettering kept');
    const input = target.querySelector('input[type=file]') as HTMLInputElement;
    Object.defineProperty(input, 'files', {value: [new File([new Uint8Array(10)], 'wide.png', {type: 'image/png'})]});
    input.dispatchEvent(new Event('change', {bubbles: true}));
    await settle();
    expect(text(target.querySelector('[role=alert]'))).toContain(messages['control.sketch_invalid']);
    expect(button(target, 'Refresh')).toBeUndefined();
    expect(button(target, 'Ink this page')?.disabled).toBe(true);
  });

  it('inks only the visible source: switching to Draw here drops an upload', async () => {
    server(() => ({status: 200, body: quote(1)}));
    vi.stubGlobal('createImageBitmap', vi.fn(async () => ({width: 680, height: 960, close() {}})));
    vi.stubGlobal('URL', Object.assign(URL, {createObjectURL: () => 'blob:sketch', revokeObjectURL: () => {}}));
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null);
    const {target, props} = render({kind: 'sketch'});
    await settle();
    const input = target.querySelector('input[type=file]') as HTMLInputElement;
    Object.defineProperty(input, 'files', {value: [new File([new Uint8Array(10)], 'sketch.png', {type: 'image/png'})]});
    input.dispatchEvent(new Event('change', {bubbles: true}));
    await settle();
    expect(button(target, 'Ink this page')?.disabled).toBe(false);
    expect(props.onDirty).toHaveBeenLastCalledWith(true);
    button(target, 'Draw here')!.click();
    await settle();
    expect(button(target, 'Ink this page')?.disabled).toBe(true);
    expect(props.onDirty).toHaveBeenLastCalledWith(false);
  });

  it('labels the proofs like the design', async () => {
    server(() => ({status: 200, body: quote(1)}));
    // jsdom has no 2D canvas; the drawing surface only needs to mount here.
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null);
    const {target} = render({kind: 'sketch'});
    await settle();
    const captions = [...target.querySelectorAll('.mv-control__pair figcaption')].map(text);
    expect(captions).toEqual(['Your sketch · uploaded', 'Inked · strength 0.62']);
    expect(text(target.querySelector('.mv-route__pick'))).toBe('→ Placeholder art · built-in · no remote spend');
    expect(target.querySelector('#sketch-strength')?.classList.contains('mv-range')).toBe(true);
    button(target, 'Draw here')!.click();
    await settle();
    expect(text(target.querySelector('.mv-control__pair figcaption'))).toBe('Your sketch · drawn');
    expect(text(target.querySelector('.mv-control__tag'))).toBe('Rough pencil · page 1');
  });
});

describe('D10 Version history', () => {
  const versions: Version[] = [9, 8, 7].map(n => ({id: `v${n}`, number: n, action: n === 9 ? 'generate_page' : 'save', summary: '', created_at: '2026-11-08T00:00:00Z'}));
  function history() {
    return server(c => {
      if (c.url === `${api}/pages/p/history`) return {status: 200, body: {versions}};
      if (c.url === `${api}/pages/p/versions/v8`) { const p = page(8); p.panels[0].prompt = 'Rin waits.'; return {status: 200, body: {page: p, lettering: []}}; }
    });
  }
  it('undoes the newest change and marks the changed panels on the compared proof', async () => {
    history();
    const {target, props} = render({kind: 'history', page: page(9)});
    await settle();
    const rows = [...target.querySelectorAll('.mv-ed__version')];
    expect(rows.map(r => text(r.querySelector('.mv-control__version-action')))).toEqual(['Undo', 'Restore', 'Restore']);
    expect(text(target.querySelector('.mv-control__compared figcaption'))).toBe('Version 8 · 1 change');
    expect(target.querySelectorAll('.mv-control__changes polygon')).toHaveLength(1);
    expect(target.querySelectorAll('.mv-control__pair figure img')).toHaveLength(2);
    (rows[0].querySelector('.mv-control__version-action') as HTMLButtonElement).click();
    expect(props.onRestore).toHaveBeenLastCalledWith(versions[1], true);
    button(rows[1], 'Restore')!.click();
    expect(props.onRestore).toHaveBeenLastCalledWith(versions[1]);
  });

  it('shows an editor conflict inside the workbench with Reload latest', async () => {
    history();
    const {target, props} = render({kind: 'history', page: page(9), notice: messages['error.conflict'], conflict: true});
    await settle();
    expect(text(target.querySelector('[role=alert]'))).toContain(messages['error.conflict']);
    button(target, 'Reload latest')!.click();
    expect(props.onReload).toHaveBeenCalledTimes(1);
  });
});
