import {describe, it, expect, vi} from 'vitest';
import {readFileSync} from 'node:fs';
import {PagePresence, applyPresence, authorLine, avatars, cursorState, filterComments, peerFrom, pins, roleLine, scopeChoices, scopeFor, scopeKind, statusTone, throttle, type Clock, type Member, type Peer, type RealtimeLike, type ReviewComment} from './collab';

const locales = '../../../../internal/i18n/locales/';
const en = JSON.parse(readFileSync(new URL(locales + 'en/collab.json', import.meta.url), 'utf8')) as Record<string, string>;
const t = (key: string, args: Record<string, string | number> = {}) => Object.entries(args).reduce((s, [k, v]) => s.replaceAll(`{${k}}`, String(v)), en[key] || key);

const person = (id: string, name: string, role = '') => ({id, name, username: name.toLowerCase(), role});
function comment(id: string, status: ReviewComment['status'], o: Partial<ReviewComment> = {}): ReviewComment {
  return {id, panel_id: 'p' + id, label: '7.' + id, x: 30, y: 270, body: 'Left hand looks off.', status, author: person('u1', 'Jerry', 'editor'), created_at: '2026-11-09T10:00:00Z', replies: [], next: [], ...o};
}

/** A clock the test moves by hand. */
function manualClock() {
  let now = 0, seq = 0;
  const timers = new Map<number, {at: number; fn: () => void}>();
  const clock: Clock = {now: () => now, set: (fn, ms) => { timers.set(++seq, {at: now + ms, fn}); return seq; }, clear: id => { timers.delete(id as number); }};
  const advance = (ms: number) => {
    now += ms;
    for (const [id, tm] of [...timers].sort((a, b) => a[1].at - b[1].at)) if (tm.at <= now) { timers.delete(id); tm.fn(); }
  };
  return {clock, advance};
}

describe('review lists', () => {
  const list = [comment('3', 'needs_changes'), comment('2', 'done'), comment('4', 'open'), comment('1', 'approved')];
  it('shows open, done and needs-changes comments under Open, and approved ones apart', () => {
    expect(filterComments(list, 'open').map(c => c.status)).toEqual(['needs_changes', 'done', 'open']);
    expect(filterComments(list, 'needs_changes').map(c => c.id)).toEqual(['3']);
    expect(filterComments(list, 'approved').map(c => c.id)).toEqual(['1']);
  });
  it('numbers pins for the open comments in list order, page-relative, without covering each other', () => {
    const spread = [comment('3', 'needs_changes'), comment('2', 'done', {x: 440}), comment('4', 'open', {x: 120, y: 620}), comment('1', 'approved')];
    expect(pins(spread, 520, 735)).toEqual([
      {id: '3', number: 1, label: '7.3', x: 30 / 520, y: 270 / 735},
      {id: '2', number: 2, label: '7.2', x: 440 / 520, y: 270 / 735},
      {id: '4', number: 3, label: '7.4', x: 120 / 520, y: 620 / 735},
    ]);
    const shared = pins(list, 520, 735).map(p => p.x);
    expect(shared[0]).toBe(30 / 520);
    expect(shared[1]).toBeCloseTo(30 / 520 + 0.055);
    expect(shared[2]).toBeCloseTo(30 / 520 + 0.11);
    expect(pins([comment('9', 'open', {x: 9000, y: -4})], 100, 100)[0]).toMatchObject({x: 1, y: 0});
  });
  it('labels roles, scopes and authors as the D7 drawer does', () => {
    const m = (role: string, scope = {}): Member => ({user_id: 'x', name: 'Jun', username: 'jun', role, scope});
    expect(roleLine(m('owner'), t)).toBe('Owner');
    expect(roleLine(m('letterer', {layer: 'lettering'}), t)).toBe('Letterer · lettering layer only');
    expect(roleLine(m('artist', {page_from: 1, page_to: 20}), t)).toBe('Artist · pages 1–20');
    expect(authorLine(person('a', 'Jerry', 'editor'), false, t)).toBe('Jerry · Editor');
    expect(authorLine(person('a', 'Jun', 'letterer'), true, t)).toBe('Jun · Letterer · Agent');
    expect(statusTone('needs_changes')).toBe('warning');
    expect(statusTone('done')).toBe('success');
    expect(statusTone('open')).toBe('info');
  });
  it('offers only the limits a role can take', () => {
    expect(scopeChoices('letterer')).toEqual(['all', 'lettering', 'pages']);
    expect(scopeChoices('artist')).toEqual(['all', 'pages']);
    expect(scopeChoices('writer')).toEqual(['all']);
    expect(scopeFor('pages', 1, 20)).toEqual({page_from: 1, page_to: 20});
    expect(scopeFor('lettering', 0, 0)).toEqual({layer: 'lettering'});
    expect(scopeKind({page_from: 3, page_to: 4})).toBe('pages');
  });
});

describe('cursor presence', () => {
  it('throttles cursor updates: first at once, then at most one per interval with the latest position', () => {
    const {clock, advance} = manualClock();
    const sent: number[] = [];
    const th = throttle((x: number) => sent.push(x), 80, clock);
    th.call(1); th.call(2); th.call(3);
    expect(sent).toEqual([1]);
    advance(79); expect(sent).toEqual([1]);
    advance(1); expect(sent).toEqual([1, 3]);
    for (let i = 0; i < 100; i++) { th.call(10 + i); advance(4); }
    // 400 ms of moves at 250 Hz: one message per 80 ms window, never more.
    expect(sent.length).toBeLessThanOrEqual(2 + 400 / 80 + 1);
    // The trailing call always delivers the latest position.
    advance(200); expect(sent.at(-1)).toBe(109);
    // Leaving the page cancels a pending update.
    th.call(500); th.call(600); th.cancel(); advance(200); expect(sent.at(-1)).toBe(500);
  });
  it('sends only a rounded page-relative position and a colour', () => {
    expect(cursorState(0.123456, 1.4, 2)).toEqual({x: 0.1235, y: 1, color: 2});
    expect(cursorState(null, 0.5, 3)).toEqual({x: null, y: null, color: 3});
    expect(cursorState(Number.NaN, 0.5, 3)).toEqual({x: null, y: null, color: 3});
  });
  it('keeps nothing but cursor and colour from a peer, and names it from the server', () => {
    const p = peerFrom({conn: 'c1', id: 'u2', username: 'jun', display_name: 'Jun', state: {x: 0.5, y: 0.25, color: 2, text: 'secret script'}});
    expect(p).toEqual({conn: 'c1', id: 'u2', name: 'Jun', agent: false, x: 0.5, y: 0.25, color: 2});
    expect(peerFrom({conn: 'c1', id: 'u2', state: {x: 4, y: 0.5, color: 99, name: 'Spoof'}})).toMatchObject({x: null, y: null, name: ''});
    expect(peerFrom({id: 'u2'} as never)).toBeNull();
  });
  it('tracks joins, moves and leaves on its topic and ignores my own tabs', () => {
    const topic = 'doc:page:a';
    let peers: Peer[] = [];
    peers = applyPresence(peers, {topic, type: 'presence.list', data: [{conn: 'c1', id: 'me', state: {x: .1, y: .1, color: 1}}, {conn: 'c2', id: 'u2', display_name: 'Jun'}]}, topic, 'me');
    expect(peers.map(p => p.conn)).toEqual(['c2']);
    peers = applyPresence(peers, {topic, type: 'presence.join', data: {conn: 'c3', id: 'u3', display_name: 'Aiko', state: {x: .2, y: .3, color: 3}}}, topic, 'me');
    peers = applyPresence(peers, {topic, type: 'presence', data: {conn: 'c2', id: 'u2', display_name: 'Jun', state: {x: .7, y: .6, color: 2}}}, topic, 'me');
    expect(peers.map(p => [p.conn, p.x])).toEqual([['c2', .7], ['c3', .2]]);
    expect(applyPresence(peers, {topic: 'doc:page:b', type: 'presence.leave', data: {conn: 'c2'}}, topic, 'me')).toBe(peers);
    peers = applyPresence(peers, {topic, type: 'presence.leave', data: {conn: 'c2'}}, topic, 'me');
    expect(peers.map(p => p.conn)).toEqual(['c3']);
  });
  it('shows people on the page first with the online ring, then other members, never me', () => {
    const peers: Peer[] = [{conn: 'c', id: 'u2', name: 'Jun', agent: false, x: null, y: null, color: 2}];
    const members: Member[] = ['me', 'u2', 'u3', 'u4', 'u5', 'u6'].map(id => ({user_id: id, name: id, username: id, role: 'artist', scope: {}}));
    const out = avatars(peers, members, 'me', 4);
    expect(out.shown.map(a => [a.id, a.here])).toEqual([['u2', true], ['u3', false], ['u4', false], ['u5', false]]);
    expect(out.more).toBe(1);
  });
  it('follows one page topic, re-announces itself after a reconnect and leaves when the page changes', () => {
    const {clock, advance} = manualClock();
    const calls: unknown[][] = [];
    let handler: ((m: {topic?: string; type: string; data?: unknown}) => void) | null = null;
    const rt: RealtimeLike = {subscribe: tp => calls.push(['sub', tp]), unsubscribe: tp => calls.push(['unsub', tp]), presence: (tp, d) => calls.push(['presence', tp, d]), on: (_type, fn) => { handler = fn; return () => { handler = null; }; }};
    const changes = vi.fn();
    const p = new PagePresence(rt, 'me', 4, changes, 80, clock);
    p.join('a');
    handler!({topic: 'doc:page:a', type: 'subscribed'});
    p.move(0.5, 0.5); p.move(0.6, 0.6); p.move(0.6, 0.6);
    advance(100);
    handler!({topic: 'doc:page:a', type: 'presence.join', data: {conn: 'c2', id: 'u2', display_name: 'Jun', state: {x: .3, y: .3, color: 2}}});
    expect(p.peers.map(v => v.name)).toEqual(['Jun']);
    handler!({topic: 'doc:page:a', type: 'subscribed'});
    p.join('b');
    expect(calls).toEqual([
      ['sub', 'doc:page:a'],
      ['presence', 'doc:page:a', {x: null, y: null, color: 4}],
      ['presence', 'doc:page:a', {x: 0.5, y: 0.5, color: 4}],
      ['presence', 'doc:page:a', {x: 0.6, y: 0.6, color: 4}],
      ['presence', 'doc:page:a', {x: 0.6, y: 0.6, color: 4}],
      ['unsub', 'doc:page:a'],
      ['sub', 'doc:page:b'],
    ]);
    expect(p.peers).toEqual([]);
    expect(changes).toHaveBeenLastCalledWith([]);
    p.dispose();
    expect(handler).toBeNull();
  });
});
