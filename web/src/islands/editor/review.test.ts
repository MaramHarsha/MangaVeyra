import {describe, it, expect, vi, afterEach} from 'vitest';
// Vitest resolves `svelte` to its server build; components need the client runtime.
vi.mock('svelte', async () => await import(/* @vite-ignore */ ('../../../node_modules/svelte/src/index-client.js' as string)));
import {mount, unmount, flushSync} from 'svelte';
import {readFileSync} from 'node:fs';
import ReviewDrawer from './ReviewDrawer.svelte';
import {ReviewStore} from './review.svelte';
import {reactiveProps} from './test-props.svelte';
import type {ReviewData} from './collab';

const locales = '../../../../internal/i18n/locales/';
const en = JSON.parse(readFileSync(new URL(locales + 'en/collab.json', import.meta.url), 'utf8')) as Record<string, string>;
const t = (key: string, args: Record<string, string | number> = {}) => Object.entries(args).reduce((s, [k, v]) => s.replaceAll(`{${k}}`, String(v)), en[key] || key);
const base = '/api/v1/collab/p/project';
const person = (id: string, name: string, role: string) => ({id, name, username: name.toLowerCase(), role});
const jerry = person('u1', 'Jerry', 'owner'), jun = person('u2', 'Jun', 'letterer'), aiko = person('u3', 'Aiko', 'artist');

function data(o: Partial<ReviewData> = {}): ReviewData {
  const c = (id: string, label: string, status: string, author: typeof jerry, body: string, next: string[] = []) => ({id, panel_id: 'p' + id, label, x: 30, y: 270, body, status, author, created_at: '2026-11-09T10:00:00Z', replies: [], next});
  return {page_id: 'page7', chapter_id: 'ch', page_number: 7, page_version: 12,
    comments: [c('c1', '7.3', 'needs_changes', jerry, 'Left hand looks off. Fix just that region?', ['open', 'done', 'approved']), c('c2', '7.2', 'done', jun, 'Bubble 2 crowds Kaito’s face; moved it.', ['open', 'needs_changes', 'approved']),
      c('c3', '7.4', 'open', aiko, 'Lamp should be dark until scene 3.', ['needs_changes', 'done', 'approved']), c('c4', '7.1', 'approved', jerry, 'Good establishing shot.')] as ReviewData['comments'],
    counts: {open: 3, resolved: 1, needs_changes: 1, approved: 1},
    members: [{user_id: 'u1', name: 'Jerry', username: 'jerry', role: 'owner', scope: {}}, {user_id: 'u2', name: 'Jun', username: 'jun', role: 'letterer', scope: {layer: 'lettering'}}, {user_id: 'u3', name: 'Aiko', username: 'aiko', role: 'artist', scope: {page_from: 1, page_to: 20}}],
    viewer: {id: 'u1', role: 'owner', can_comment: true, can_review: true, can_manage: true}, ...o};
}

interface Call {method: string; url: string; body: Record<string, unknown> | undefined}
function server(reply: (c: Call) => unknown): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    const c: Call = {method: init.method || 'GET', url: String(url), body: typeof init.body === 'string' ? JSON.parse(init.body) : undefined};
    calls.push(c);
    const out = reply(c);
    return new Response(JSON.stringify(out ?? {error: {code: 'forbidden', message: 'Your role on this project cannot do that.'}}), {status: out ? 200 : 403, headers: {'Content-Type': 'application/json'}});
  }));
  return calls;
}
async function settle() { for (let i = 0; i < 12; i++) { await new Promise(r => setTimeout(r, 0)); flushSync(); } }
const buttons = (el: HTMLElement) => Array.from(el.querySelectorAll<HTMLButtonElement>('button'));
const byText = (el: HTMLElement, text: string) => buttons(el).find(b => b.textContent?.trim() === text);

let target: HTMLElement | null = null, app: ReturnType<typeof mount> | null = null;
afterEach(() => { if (app) unmount(app); target?.remove(); app = null; target = null; vi.unstubAllGlobals(); });

async function open(d: ReviewData, reply: (c: Call) => unknown = () => d, extra: Record<string, unknown> = {}) {
  const calls = server(c => c.method === 'GET' ? d : reply(c));
  const store = new ReviewStore(base, 'offline');
  await store.load('page7');
  target = document.createElement('div');
  document.body.appendChild(target);
  const props = reactiveProps({store, pageNumber: 7, pageVersion: 12, panelID: 'pc1', panelLabel: '7.3', compose: 0, focus: '', lang: 'en', t, onClose: vi.fn(), onFocus: vi.fn(), onConfirm: vi.fn(), ...extra});
  app = mount(ReviewDrawer, {target, props});
  await settle();
  return {calls, el: target, store, props};
}

describe('review drawer (D7)', () => {
  it('shows the page review with counts, filters and the roles on the project', async () => {
    const {el} = await open(data());
    expect(el.querySelector('.mv-review__head strong')?.textContent).toBe('Review · page 7');
    expect(el.querySelector('.mv-review__head span')?.textContent).toBe('3 open · 1 resolved');
    const items = () => Array.from(el.querySelectorAll('.mv-review__item')).map(i => [i.querySelector('strong')?.textContent, i.querySelector('.mv-review__label')?.textContent, i.querySelector('.mv-badge')?.textContent?.trim()]);
    expect(items()).toEqual([['Jerry · Owner', '7.3', 'Needs changes'], ['Jun · Letterer', '7.2', 'Done'], ['Aiko · Artist', '7.4', 'Open']]);
    byText(el, 'Needs changes')!.click(); await settle();
    expect(items().map(i => i[1])).toEqual(['7.3']);
    byText(el, 'Approved')!.click(); await settle();
    expect(items().map(i => i[1])).toEqual(['7.1']);
    const roles = Array.from(el.querySelectorAll('.mv-review__role')).map(r => r.textContent?.trim());
    expect(roles).toEqual(['JerryOwner', 'JunLetterer · lettering layer only', 'AikoArtist · pages 1–20']);
    expect(byText(el, 'Approve page 7')?.disabled).toBe(false);
  });

  it('changes a status through the status menu and posts replies and comments', async () => {
    const d = data();
    const {el, calls} = await open(d, c => (c.url.endsWith('/status') || c.url.endsWith('/replies') || c.url.endsWith('/comments')) ? d : undefined);
    el.querySelector<HTMLButtonElement>('[data-status="c3"]')!.click(); await settle();
    const menu = el.querySelector('[data-menu="status-c3"]')!;
    expect(Array.from(menu.querySelectorAll('[role="menuitem"]')).map(b => b.textContent)).toEqual(['Needs changes', 'Mark done', 'Approve']);
    (Array.from(menu.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')).find(b => b.textContent === 'Mark done'))!.click(); await settle();
    expect(calls.at(-1)).toEqual({method: 'POST', url: `${base}/comments/c3/status`, body: {status: 'done'}});

    el.querySelector<HTMLButtonElement>('.mv-review__item[data-comment="c1"] .mv-review__reply')!.click(); await settle();
    const reply = el.querySelector<HTMLTextAreaElement>('#reply-c1')!;
    reply.value = 'Fixed the hand.'; reply.dispatchEvent(new Event('input')); await settle();
    reply.closest('form')!.requestSubmit(); await settle();
    expect(calls.at(-1)).toEqual({method: 'POST', url: `${base}/comments/c1/replies`, body: {body: 'Fixed the hand.'}});

    el.querySelector<HTMLButtonElement>('.mv-review__new')!.click(); await settle();
    const box = el.querySelector<HTMLTextAreaElement>('.mv-review__composer textarea')!;
    box.value = 'Rin needs her scarf.'; box.dispatchEvent(new Event('input'));
    const who = el.querySelector<HTMLSelectElement>('.mv-review__composer select')!;
    who.value = 'u3'; who.dispatchEvent(new Event('change')); await settle();
    box.closest('form')!.requestSubmit(); await settle();
    expect(calls.at(-1)).toEqual({method: 'POST', url: `${base}/pages/page7/comments`, body: {panel_id: 'pc1', body: 'Rin needs her scarf.', assignee_id: 'u3'}});
  });

  it('approves the page at its version, and only reviewers may', async () => {
    const d = data();
    const approved = data({approval: {by: jerry, at: '2026-11-09T10:05:00Z', version: 12, current: true}});
    const {el, calls} = await open(d, c => c.url.endsWith('/approval') ? approved : undefined);
    byText(el, 'Approve page 7')!.click(); await settle();
    expect(calls.at(-1)).toEqual({method: 'POST', url: `${base}/pages/page7/approval`, body: {version: 12}});
    expect(byText(el, 'Approve page 7')?.disabled).toBe(true);
    expect(el.querySelector('.mv-review__foot .mv-review__meta')?.textContent).toContain('Approved by Jerry');
    if (app) unmount(app); target?.remove();
    const letterer = await open(data({viewer: {id: 'u2', role: 'letterer', can_comment: true, can_review: false, can_manage: false}, comments: data().comments.map(c => ({...c, next: c.author.id === 'u2' ? ['open'] : []})) as ReviewData['comments']}));
    expect(byText(letterer.el, 'Approve page 7')?.disabled).toBe(true);
    expect(letterer.el.querySelector('[data-status="c3"]')).toBeNull();
    expect(letterer.el.querySelector('[data-status="c2"]')).not.toBeNull();
    expect(letterer.el.querySelectorAll('.mv-review__edit').length).toBe(0);
  });

  it('shows server refusals and edits a member’s role and scope', async () => {
    const d = data();
    const {el, calls} = await open(d, c => c.method === 'PATCH' ? {member: {}} : undefined);
    el.querySelectorAll<HTMLButtonElement>('.mv-review__edit')[1].click(); await settle();
    const [role, kind] = Array.from(el.querySelectorAll<HTMLSelectElement>('.mv-review__member select'));
    expect(role.value).toBe('artist');
    expect(Array.from(kind.options).map(o => o.value)).toEqual(['all', 'pages']);
    const [from, to] = Array.from(el.querySelectorAll<HTMLInputElement>('.mv-review__member input'));
    to.value = '24'; to.dispatchEvent(new Event('input')); from.value = '2'; from.dispatchEvent(new Event('input')); await settle();
    el.querySelector<HTMLFormElement>('.mv-review__member')!.requestSubmit(); await settle();
    expect(calls.find(c => c.method === 'PATCH')).toEqual({method: 'PATCH', url: `${base}/members/u3`, body: {role: 'artist', scope: {page_from: 2, page_to: 24}}});
    el.querySelector<HTMLButtonElement>('[data-status="c1"]')!.click(); await settle();
    el.querySelector<HTMLButtonElement>('[data-menu="status-c1"] [role="menuitem"]')!.click(); await settle();
    expect(el.querySelector('[role="alert"]')?.textContent).toContain('Your role on this project cannot do that.');
  });

  it('reloads when another editor changes this page’s review', async () => {
    vi.useFakeTimers({toFake: ['setTimeout', 'clearTimeout']});
    try {
      const calls = server(() => data());
      const store = new ReviewStore(base, 'offline');
      await store.load('page7');
      store.changed({type: 'collab.changed', data: {page_id: 'other'}});
      store.changed({type: 'collab.changed', data: {page_id: 'page7'}});
      store.changed({type: 'collab.changed', data: {page_id: 'page7'}});
      vi.advanceTimersByTime(200);
      await Promise.resolve();
      expect(calls.filter(c => c.method === 'GET').length).toBe(2);
    } finally { vi.useRealTimers(); }
  });
});
