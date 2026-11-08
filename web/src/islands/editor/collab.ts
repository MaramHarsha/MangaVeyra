/**
 * D7 collaboration logic (ADR 0013 §8): review data from /api/v1/collab, the
 * drawer's filters and labels, comment pins, and live cursors on the page's
 * doc:page:{id} topic. Presence carries only a cursor position and a colour;
 * the server adds who sent it. Everything here is pure or takes its clock and
 * socket as arguments so it can be tested without a browser.
 */
import {avatarTone} from './document';

export type Status = 'open' | 'needs_changes' | 'done' | 'approved';
export type Filter = 'open' | 'needs_changes' | 'approved';
export const FILTERS: Filter[] = ['open', 'needs_changes', 'approved'];
export const STATUSES: Status[] = ['open', 'needs_changes', 'done', 'approved'];

export interface Person {id: string; name: string; username: string; role?: string}
export interface Reply {id: string; body: string; author: Person; agent?: boolean; created_at: string}
export interface ReviewComment {id: string; panel_id: string; label: string; x: number; y: number; body: string; status: Status; author: Person; assignee?: Person; agent?: boolean; created_at: string; resolved_by?: Person; resolved_at?: string; replies: Reply[]; next: Status[]}
export interface MemberScope {layer?: string; page_from?: number; page_to?: number}
export interface Member {user_id: string; name: string; username: string; role: string; scope: MemberScope}
export interface Approval {by: Person; at: string; version: number; current: boolean}
export interface ReviewData {page_id: string; chapter_id: string; page_number: number; page_version: number; comments: ReviewComment[]; counts: {open: number; resolved: number; needs_changes: number; approved: number}; approval?: Approval; members: Member[]; viewer: {id: string; role?: string; can_comment: boolean; can_review: boolean; can_manage: boolean}}

type T = (key: string, args?: Record<string, string | number>) => string;

/** inFilter: Open lists everything not approved yet (open, needs changes, done). */
export function inFilter(c: ReviewComment, f: Filter): boolean {
  return f === 'open' ? c.status !== 'approved' : c.status === f;
}

export function filterComments(list: ReviewComment[], f: Filter): ReviewComment[] {
  return list.filter(c => inFilter(c, f));
}

/** statusTone maps a status to the kit's badge colour; the word is always shown too. */
export function statusTone(s: Status): string {
  return s === 'needs_changes' ? 'warning' : s === 'done' ? 'success' : s === 'approved' ? 'accent' : 'info';
}

/** roleLine is the D7 roles row: "Letterer · lettering layer only", "Artist · pages 1–20". */
export function roleLine(m: Pick<Member, 'role' | 'scope'>, t: T): string {
  const parts = [t(`collab.role.${m.role}`)];
  if (m.scope?.layer === 'lettering') parts.push(t('collab.scope.lettering'));
  if (m.scope?.page_from && m.scope?.page_to) parts.push(t('collab.scope.pages', {from: m.scope.page_from, to: m.scope.page_to}));
  return parts.join(' · ');
}

/** authorLine is "Jerry · Editor" (with the agent label for token comments). */
export function authorLine(p: Person, agent: boolean | undefined, t: T): string {
  const parts = [p.name || p.username || '?'];
  if (p.role) parts.push(t(`collab.role.${p.role}`));
  if (agent) parts.push(t('collab.agent'));
  return parts.join(' · ');
}

export interface Pin {id: string; number: number; label: string; x: number; y: number}

/** pins are the canvas markers for comments still open, numbered as the Open list shows them. */
export function pins(list: ReviewComment[], width: number, height: number): Pin[] {
  return filterComments(list, 'open').map((c, i) => ({id: c.id, number: i + 1, label: c.label,
    x: Math.max(0, Math.min(1, c.x / Math.max(1, width))), y: Math.max(0, Math.min(1, c.y / Math.max(1, height)))}));
}

/** scopeFor builds the stored member scope from the roles editor fields. */
export function scopeFor(kind: string, from: number, to: number): MemberScope {
  if (kind === 'lettering') return {layer: 'lettering'};
  if (kind === 'pages') return {page_from: Math.max(1, Math.round(from) || 1), page_to: Math.max(1, Math.round(to) || 1)};
  return {};
}

export function scopeKind(s: MemberScope): string {
  return s.layer === 'lettering' ? 'lettering' : s.page_from ? 'pages' : 'all';
}

/** scopeChoices lists the limits a role can take (the server enforces the same rule). */
export function scopeChoices(role: string): string[] {
  if (role === 'letterer' || role === 'editor') return ['all', 'lettering', 'pages'];
  if (role === 'artist') return ['all', 'pages'];
  return ['all'];
}

export const ASSIGNABLE_ROLES = ['writer', 'artist', 'letterer', 'editor', 'viewer'];

// ---- presence (live cursors) ---------------------------------------------------

/** Throttled calls: the first runs at once, later ones at most every ms, the last always wins. */
export interface Clock {now(): number; set(fn: () => void, ms: number): unknown; clear(id: unknown): void}
const realClock: Clock = {now: () => Date.now(), set: (fn, ms) => setTimeout(fn, ms), clear: id => clearTimeout(id as ReturnType<typeof setTimeout>)};

export function throttle<A extends unknown[]>(fn: (...args: A) => void, ms: number, clock: Clock = realClock) {
  let last = -Infinity, timer: unknown = null, pending: A | null = null;
  const run = () => { timer = null; if (!pending) return; const a = pending; pending = null; last = clock.now(); fn(...a); };
  return {
    call(...args: A) {
      pending = args;
      const wait = last + ms - clock.now();
      if (wait <= 0 && timer === null) run();
      else if (timer === null) timer = clock.set(run, Math.max(0, wait));
    },
    /** flush sends a pending call now. */
    flush() { if (timer !== null) { clock.clear(timer); run(); } },
    cancel() { if (timer !== null) clock.clear(timer); timer = null; pending = null; },
  };
}

/** The presence state this tab sends: a page-relative cursor (0–1) and a colour. */
export interface CursorState {x: number | null; y: number | null; color: number}

export function cursorState(x: number | null, y: number | null, color: number): CursorState {
  const r = (v: number | null) => v === null || !Number.isFinite(v) ? null : Math.round(Math.max(0, Math.min(1, v)) * 10000) / 10000;
  const cx = r(x), cy = r(y);
  return cx === null || cy === null ? {x: null, y: null, color} : {x: cx, y: cy, color};
}

export interface Peer {conn: string; id: string; name: string; agent: boolean; x: number | null; y: number | null; color: number}

interface PresenceUser {conn?: string; id?: string; username?: string; display_name?: string; agent?: boolean; state?: unknown}

/** peerFrom reads a hub presence entry, keeping only a valid cursor and colour from the sender's state. */
export function peerFrom(u: PresenceUser): Peer | null {
  if (!u || typeof u.conn !== 'string' || typeof u.id !== 'string') return null;
  const s = (u.state && typeof u.state === 'object' ? u.state : {}) as Record<string, unknown>;
  const num = (v: unknown) => typeof v === 'number' && Number.isFinite(v) && v >= 0 && v <= 1 ? v : null;
  let x = num(s.x), y = num(s.y);
  if (x === null || y === null) x = y = null;
  const color = typeof s.color === 'number' && Number.isInteger(s.color) && s.color >= 1 && s.color <= 6 ? s.color : avatarTone(u.id);
  return {conn: u.conn, id: u.id, name: u.display_name || u.username || '', agent: !!u.agent, x, y, color};
}

/** applyPresence folds one hub message for topic into the peers list (other tabs of me are dropped). */
export function applyPresence(peers: Peer[], msg: {topic?: string; type: string; data?: unknown}, topic: string, me: string): Peer[] {
  if (msg.topic !== topic) return peers;
  const keep = (p: Peer | null): p is Peer => !!p && p.id !== me;
  switch (msg.type) {
  case 'presence.list':
    return Array.isArray(msg.data) ? (msg.data as PresenceUser[]).map(peerFrom).filter(keep) : peers;
  case 'presence.join':
  case 'presence': {
    const p = peerFrom(msg.data as PresenceUser);
    if (!keep(p)) return peers;
    return peers.some(v => v.conn === p.conn) ? peers.map(v => v.conn === p.conn ? p : v) : [...peers, p];
  }
  case 'presence.leave': {
    const conn = (msg.data as PresenceUser)?.conn;
    return peers.filter(v => v.conn !== conn);
  }
  }
  return peers;
}

export interface Avatar {id: string; name: string; tone: number; here: boolean}

/** avatars: people on the page first (online ring), then other members; never me. */
export function avatars(peers: Peer[], members: Member[], me: string, max = 4): {shown: Avatar[]; more: number} {
  const seen = new Set<string>([me]), out: Avatar[] = [];
  for (const p of peers) if (!seen.has(p.id)) { seen.add(p.id); out.push({id: p.id, name: p.name, tone: p.color, here: true}); }
  for (const m of members) if (!seen.has(m.user_id)) { seen.add(m.user_id); out.push({id: m.user_id, name: m.name, tone: avatarTone(m.user_id), here: false}); }
  return {shown: out.slice(0, max), more: Math.max(0, out.length - max)};
}

export interface RealtimeLike {
  subscribe(topic: string): void;
  unsubscribe(topic: string): void;
  presence(topic: string, data: unknown): void;
  on(type: string, fn: (m: {topic?: string; type: string; data?: unknown}) => void): () => void;
}

/**
 * PagePresence follows one page at a time: it subscribes to doc:page:{id},
 * sends this tab's cursor (throttled) and keeps the peers list. Leaving the
 * page unsubscribes (the hub tells the others); closing the tab closes the
 * socket, which does the same.
 */
export class PagePresence {
  peers: Peer[] = [];
  topic = '';
  private state: CursorState;
  private send: ReturnType<typeof throttle<[CursorState]>>;
  private off: () => void = () => {};

  constructor(private rt: RealtimeLike | null, private me: string, color: number, private onChange: (peers: Peer[]) => void, interval = 80, clock?: Clock) {
    this.state = {x: null, y: null, color};
    this.send = throttle((s: CursorState) => { if (this.topic) this.rt?.presence(this.topic, s); }, interval, clock);
    if (rt) this.off = rt.on('*', m => this.receive(m));
  }

  join(pageID: string): void {
    const next = pageID ? `doc:page:${pageID}` : '';
    if (next === this.topic) return;
    this.leave();
    this.topic = next;
    if (next) this.rt?.subscribe(next);
  }

  leave(): void {
    this.send.cancel();
    if (this.topic) this.rt?.unsubscribe(this.topic);
    this.topic = '';
    this.state = {...this.state, x: null, y: null};
    this.set([]);
  }

  /** move reports the pointer at page-relative x/y (null: off the page). */
  move(x: number | null, y: number | null): void {
    const s = cursorState(x, y, this.state.color);
    if (s.x === this.state.x && s.y === this.state.y) return;
    this.state = s;
    this.send.call(s);
  }

  receive(m: {topic?: string; type: string; data?: unknown}): void {
    if (!this.topic || m.topic !== this.topic) return;
    // (Re)subscribed, e.g. after a reconnect: the hub forgot this tab, so say hello again.
    if (m.type === 'subscribed') { this.send.cancel(); this.rt?.presence(this.topic, this.state); return; }
    if (m.type === 'error') return;
    const next = applyPresence(this.peers, m, this.topic, this.me);
    if (next !== this.peers) this.set(next);
  }

  dispose(): void { this.leave(); this.off(); }

  private set(peers: Peer[]): void { this.peers = peers; this.onChange(peers); }
}
