import type {Bubble, Character, Job, Layer, Layout, Page, Panel, Point, Rect} from './types';

export function clone<T>(value: T): T { return structuredClone(value); }

export function bounds(points: Point[]): Rect {
  const xs = points.map(p => p.x), ys = points.map(p => p.y);
  const x = Math.min(...xs), y = Math.min(...ys);
  return {x, y, w: Math.max(...xs) - x, h: Math.max(...ys) - y};
}

export function contains(points: Point[], p: Point): boolean {
  let inside = false;
  for (let i = 0, j = points.length - 1; i < points.length; j = i++) {
    const a = points[i], b = points[j];
    if ((a.y > p.y) !== (b.y > p.y) && p.x < (b.x - a.x) * (p.y - a.y) / (b.y - a.y) + a.x) inside = !inside;
  }
  return inside;
}

// Signed media URLs and the stored revision number change on every load and
// save without changing the document, so neither makes a draft dirty.
export function canonical(page: Page): string {
  return JSON.stringify(page, (key, value) => key === 'url' || key === 'version' ? undefined : value);
}

export class DraftHistory {
  current: Page; base: Page; past: Page[] = []; future: Page[] = [];
  constructor(page: Page, readonly limit = 40) { this.current = clone(page); this.base = clone(page); }
  get dirty() { return canonical(this.current) !== canonical(this.base); }
  commit(page: Page) {
    if (canonical(page) === canonical(this.current)) return;
    this.past.push(clone(this.current));
    if (this.past.length > this.limit) this.past.shift();
    this.current = clone(page);
    this.future = [];
  }
  undo() { if (!this.past.length) return; this.future.push(clone(this.current)); this.current = this.past.pop()!; }
  redo() { if (!this.future.length) return; this.past.push(clone(this.current)); this.current = this.future.pop()!; }
  /** saved replaces the whole history with a freshly loaded page. */
  saved(page: Page) { this.current = clone(page); this.base = clone(page); this.past = []; this.future = []; }
  /** accept takes a saved page as the draft and base, keeping undo and redo. */
  accept(page: Page) { this.current = clone(page); this.base = clone(page); }
  /** rebase moves the base to a newer saved revision while the draft keeps edits made during the save. */
  rebase(page: Page) { this.base = clone(page); this.current = {...this.current, version: page.version}; }
}

const alphabet = '0123456789abcdefghjkmnpqrstvwxyz';
export function newID(): string {
  let time = Date.now(), head = '';
  for (let i = 0; i < 10; i++) { head = alphabet[time % 32] + head; time = Math.floor(time / 32); }
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  return head + Array.from(bytes, b => alphabet[b & 31]).join('');
}

export function movePanel(page: Page, id: string, dx: number, dy: number): Page {
  const next = clone(page), pn = next.panels.find(p => p.id === id);
  if (!pn || page.locked || pn.locked) return next;
  const box = bounds(pn.points);
  dx = Math.max(-box.x, Math.min(dx, page.width - box.x - box.w));
  dy = Math.max(-box.y, Math.min(dy, page.height - box.y - box.h));
  pn.points = pn.points.map(p => ({x: p.x + dx, y: p.y + dy}));
  return next;
}

export function moveBubble(page: Page, id: string, dx: number, dy: number): Page {
  const next = clone(page), b = next.bubbles.find(b => b.id === id), panel = next.panels.find(p => p.id === b?.panel_id);
  if (!b || page.locked || panel?.locked || panel?.layers.some(l => l.kind === 'lettering' && l.locked)) return next;
  b.box.x = Math.max(0, Math.min(page.width - b.box.w, b.box.x + dx));
  b.box.y = Math.max(0, Math.min(page.height - b.box.h, b.box.y + dy));
  return next;
}

export function layerPlacement(panel: Panel, width: number, height: number, transform: Layer['transform']) {
  const b = bounds(panel.points), scale = Math.max(b.w / width, b.h / height) * (transform.scale || 1);
  return {x: b.x + b.w / 2 + (transform.offset_x || 0), y: b.y + b.h / 2 + (transform.offset_y || 0), scaleX: scale * (transform.flip_x ? -1 : 1), scaleY: scale, rotation: (transform.rotate || 0) * Math.PI / 180};
}

export function canUseLayout(page: Page, layout: Layout): boolean {
  return layout.panels >= page.panels.length || page.panels.slice(layout.panels).every(p => !p.prompt && !p.layers.some(l => l.asset) && !page.bubbles.some(b => b.panel_id === p.id));
}

export function layoutPoints(layout: Layout, tilt = true): Point[][] {
  return layout.points.map(points => {
    if (tilt) return clone(points);
    const b = bounds(points);
    return [{x: b.x, y: b.y}, {x: b.x + b.w, y: b.y}, {x: b.x + b.w, y: b.y + b.h}, {x: b.x, y: b.y + b.h}];
  });
}

export function applyLayout(page: Page, layout: Layout, tilt = true): Page {
  if (page.locked || page.panels.some(p => p.locked) || !canUseLayout(page, layout)) throw Error('layout');
  const p = clone(page);
  p.layout_id = layout.id;
  p.panels = layoutPoints(layout, tilt).map((points, i) => {
    const old = p.panels[i];
    if (old) return {...old, points: clone(points), order: i};
    return {id: newID(), order: i, points: clone(points), prompt: '', prompt_source: 'user', locked: false, layers: [
      {id: newID(), kind: 'background', order: 0, visible: true, locked: false, opacity: 1, transform: {}, provenance: 'human'},
      {id: newID(), kind: 'lettering', order: 1, visible: true, locked: false, opacity: 1, transform: {}, provenance: 'human'},
    ]};
  });
  return p;
}

/* ---- content languages and lettering fonts (plan 0004 shared contract) ---- */

export const CONTENT_LANGUAGES = ['en', 'ja', 'ko', 'zh-Hans', 'zh-Hant', 'es', 'fr', 'de', 'pt', 'hi'] as const;
export type FontKey = 'comic' | 'sfx' | 'jp' | 'ko' | 'sc' | 'tc' | 'deva';
export const FONT_KEYS: FontKey[] = ['comic', 'sfx', 'jp', 'ko', 'sc', 'tc', 'deva'];
/** The family each lettering font key renders with (proper names, not translated). */
export const FONT_FAMILIES: Record<FontKey, string> = {
  comic: 'Comic Neue Bold', sfx: 'Bangers', jp: 'Noto Sans JP', ko: 'Noto Sans KR', sc: 'Noto Sans SC', tc: 'Noto Sans TC', deva: 'Noto Sans Devanagari',
};

/** normalizeLanguage mirrors pagedoc.NormalizeContentLanguage ("" when unsupported). */
export function normalizeLanguage(lang: string): string {
  const raw = (lang || '').trim();
  if ((CONTENT_LANGUAGES as readonly string[]).includes(raw)) return raw;
  const lower = raw.toLowerCase().replaceAll('_', '-');
  if (lower === 'zh-hant' || lower.startsWith('zh-hant-') || ['zh-tw', 'zh-hk', 'zh-mo'].includes(lower)) return 'zh-Hant';
  if (lower === 'zh' || lower === 'zh-hans' || lower.startsWith('zh-hans-') || ['zh-cn', 'zh-sg'].includes(lower)) return 'zh-Hans';
  const base = lower.split('-')[0];
  return base !== 'zh' && (CONTENT_LANGUAGES as readonly string[]).includes(base) ? base : '';
}

/** verticalAllowed mirrors pagedoc.VerticalAllowed. */
export function verticalAllowed(lang: string): boolean {
  return ['ja', 'ko', 'zh-Hans', 'zh-Hant'].includes(normalizeLanguage(lang));
}

/** verticalByDefault mirrors pagedoc.VerticalByDefault. */
export function verticalByDefault(lang: string): boolean {
  return ['ja', 'zh-Hant'].includes(normalizeLanguage(lang));
}

/** defaultFont is the font key a new bubble starts with in a content language. */
export function defaultFont(lang: string, kind: Bubble['kind']): FontKey {
  switch (normalizeLanguage(lang)) {
    case 'ja': return 'jp';
    case 'ko': return 'ko';
    case 'zh-Hans': return 'sc';
    case 'zh-Hant': return 'tc';
    case 'hi': return 'deva';
  }
  return kind === 'sfx' ? 'sfx' : 'comic';
}

export function bubbleShape(kind: Bubble['kind']): string {
  return kind === 'sfx' ? 'none' : kind === 'caption' ? 'rect' : kind === 'shout' ? 'burst' : kind === 'thought' ? 'cloud' : 'ellipse';
}

/** Default bubble sizes in page pixels at a 1600 px wide page. */
const BUBBLE_SIZES: Record<Bubble['kind'], {w: number; h: number}> = {
  speech: {w: 340, h: 180}, thought: {w: 360, h: 200}, shout: {w: 380, h: 220},
  whisper: {w: 320, h: 160}, caption: {w: 380, h: 120}, sfx: {w: 300, h: 170},
};

export function bubbleSize(page: Page, panel: Panel | undefined, kind: Bubble['kind']): {w: number; h: number} {
  const base = BUBBLE_SIZES[kind] || BUBBLE_SIZES.speech, s = page.width / 1600;
  const area = panel ? bounds(panel.points) : {x: 0, y: 0, w: page.width, h: page.height};
  return {w: Math.max(60, Math.min(base.w * s, area.w * .7)), h: Math.max(40, Math.min(base.h * s, area.h * .45))};
}

function overlaps(a: Rect, b: Rect, pad: number): boolean {
  return a.x < b.x + b.w + pad && b.x < a.x + a.w + pad && a.y < b.y + b.h + pad && b.y < a.y + a.h + pad;
}

/**
 * placeBubble finds free space for a new bubble of size inside a panel: the
 * first spot in reading order (rows top to bottom; right to left on manga
 * pages) that stays inside the panel and clear of every bubble already on the
 * page. A full panel cascades the bubble from its reading-order corner.
 */
export function placeBubble(page: Page, panelID: string, size: {w: number; h: number}): Rect {
  const panel = page.panels.find(p => p.id === panelID);
  const area = panel ? bounds(panel.points) : {x: page.width * .1, y: page.height * .1, w: page.width * .8, h: page.height * .8};
  const w = Math.min(size.w, area.w), h = Math.min(size.h, area.h);
  const margin = Math.max(8, Math.min(32, area.w * .04, area.h * .04));
  const pad = Math.max(6, page.width * .008);
  const step = Math.max(8, Math.min(w, h) / 4);
  const rtl = page.direction === 'rtl';
  const taken = page.bubbles.map(b => b.box);
  const inside = (r: Rect) => !panel || [
    {x: r.x, y: r.y}, {x: r.x + r.w, y: r.y}, {x: r.x + r.w, y: r.y + r.h}, {x: r.x, y: r.y + r.h}, {x: r.x + r.w / 2, y: r.y + r.h / 2},
  ].every(p => contains(panel.points, p));
  const left = area.x + margin, right = area.x + area.w - margin - w;
  const top = area.y + margin, bottom = area.y + area.h - margin - h;
  for (let y = top; y <= bottom + .01; y += step) {
    for (let i = 0; left + i * step <= right + .01; i++) {
      const x = rtl ? right - i * step : left + i * step;
      const r = {x, y, w, h};
      if (inside(r) && !taken.some(t => overlaps(r, t, pad))) return clampRect(r, page);
    }
  }
  const n = page.bubbles.filter(b => b.panel_id === panelID).length + 1, offset = 24 * (page.width / 1600) * n;
  const x = rtl ? Math.max(left, right - offset) : Math.min(right, left + offset);
  return clampRect({x, y: Math.min(Math.max(top, top + offset), Math.max(top, bottom)), w, h}, page);
}

function clampRect(r: Rect, page: Page): Rect {
  const w = Math.min(r.w, page.width), h = Math.min(r.h, page.height);
  return {x: Math.max(0, Math.min(page.width - w, r.x)), y: Math.max(0, Math.min(page.height - h, r.y)), w, h};
}

export function newBubble(page: Page, panelID: string, kind: Bubble['kind'], lang: string): Bubble {
  const panel = page.panels.find(p => p.id === panelID);
  const area = panel ? bounds(panel.points) : {x: 0, y: 0, w: page.width, h: page.height};
  const box = placeBubble(page, panelID, bubbleSize(page, panel, kind));
  const below = Math.min(area.y + area.h, page.height, box.y + box.h + Math.min(48, area.h * .06));
  const tipY = below > box.y + box.h ? below : Math.max(0, box.y - Math.min(48, area.h * .06));
  return {
    id: newID(), panel_id: panel?.id, kind, order: page.bubbles.length, box, shape: bubbleShape(kind),
    tail: {enabled: kind === 'speech' || kind === 'shout', tip: {x: box.x + box.w * (page.direction === 'rtl' ? .4 : .6), y: tipY}},
    style: {auto_fit: true, font: defaultFont(lang, kind), size: 0},
    vertical: verticalByDefault(lang),
    texts: {[lang]: {text: '', source: 'human', status: 'draft'}},
  };
}

/** setBubbleKind changes a bubble's type and shape, keeping its text and box. */
export function setBubbleKind(bubble: Bubble, kind: Bubble['kind'], lang: string): void {
  const wasDefaultFont = !bubble.style.font || bubble.style.font === defaultFont(lang, bubble.kind);
  bubble.kind = kind;
  bubble.shape = bubbleShape(kind);
  if (wasDefaultFont) bubble.style.font = defaultFont(lang, kind);
  if (kind === 'caption' || kind === 'sfx') bubble.tail.enabled = false;
  else if (kind === 'speech' || kind === 'shout') bubble.tail.enabled = true;
}

export function maskPoint(client: Point, rect: {left: number; top: number; width: number; height: number}, width: number, height: number): Point {
  return {x: Math.max(0, Math.min(width, (client.x - rect.left) * width / rect.width)), y: Math.max(0, Math.min(height, (client.y - rect.top) * height / rect.height))};
}

export function rubyFor(text: string, start: number, end: number, reading: string) {
  const length = Array.from(text).length;
  if (!Number.isInteger(start) || !Number.isInteger(end) || start < 0 || end <= start || end > length || !reading.trim()) throw Error('ruby');
  return {start, end, text: reading.trim()};
}

export function letteringOpacity(page: Page, bubbleID: string): number {
  const bubble = page.bubbles.find(b => b.id === bubbleID), panel = page.panels.find(p => p.id === bubble?.panel_id), layer = panel?.layers.find(l => l.kind === 'lettering');
  return layer ? (layer.visible ? layer.opacity : 0) : 1;
}

export function bubbleWritable(page: Page, bubble: Bubble): boolean {
  const panel = page.panels.find(p => p.id === bubble.panel_id);
  return !page.locked && !panel?.locked && !panel?.layers.some(l => l.kind === 'lettering' && l.locked);
}

export function canReorderBubble(page: Page, id: string, delta: number): boolean {
  const sorted = [...page.bubbles].sort((a, b) => a.order - b.order), i = sorted.findIndex(b => b.id === id), other = sorted[i + delta];
  return i >= 0 && !!other && bubbleWritable(page, sorted[i]) && bubbleWritable(page, other);
}

export function reorderBubble(page: Page, id: string, delta: number): Page {
  const next = clone(page);
  if (!canReorderBubble(page, id, delta)) return next;
  const sorted = [...next.bubbles].sort((a, b) => a.order - b.order), i = sorted.findIndex(b => b.id === id), other = sorted[i + delta];
  [sorted[i].order, other.order] = [other.order, sorted[i].order];
  return next;
}

/** moveBubbleTo moves a bubble to reading position `to` one step at a time, stopping at locked lettering. */
export function moveBubbleTo(page: Page, id: string, to: number): Page {
  let next = clone(page);
  const sorted = () => [...next.bubbles].sort((a, b) => a.order - b.order);
  let from = sorted().findIndex(b => b.id === id);
  if (from < 0) return next;
  to = Math.max(0, Math.min(next.bubbles.length - 1, to));
  while (from !== to) {
    const delta = to > from ? 1 : -1;
    if (!canReorderBubble(next, id, delta)) break;
    next = reorderBubble(next, id, delta);
    from += delta;
  }
  return next;
}

// Keep the toolbar at the selected panel's lower edge, in physical CSS pixels.
export function panelToolbarPlacement(page: Page, panelID: string, displayWidth: number) {
  const panel = page.panels.find(p => p.id === panelID);
  if (!panel || displayWidth <= 0) return null;
  const scale = page.width / displayWidth, width = Math.min(504, Math.max(0, displayWidth - 16)), height = displayWidth < 480 ? 104 : 48, b = bounds(panel.points);
  return {x: Math.max(0, Math.min(page.width - width * scale, b.x)), y: Math.max(0, Math.min(page.height - height * scale, b.y + b.h + 12 * scale)), scale, width, height};
}

/* ---- display helpers ---------------------------------------------------- */

const DEFAULT_LAYER_NAMES: Record<Layer['kind'], string[]> = {
  background: ['background'], fx: ['effects', 'fx'], lettering: ['lettering'], character: ['character'],
};

/**
 * layerLabel is the name a layer is shown with. The default names the server
 * stores when it creates layers ("Background", "Effects"/"FX", "Lettering",
 * "Character") are labels, so they show in the interface language; any other
 * stored name is the creator's and shows as stored. A character layer without
 * its own name shows its character's name. (The export screen's editor shell
 * uses the same rule.)
 */
export function layerLabel(layer: Layer, characters: Character[], t: (key: string) => string): string {
  const name = (layer.name || '').trim();
  if (name && !DEFAULT_LAYER_NAMES[layer.kind]?.includes(name.toLowerCase())) return name;
  if (layer.kind === 'character' && layer.character_id) {
    const character = characters.find(c => c.id === layer.character_id)?.name;
    if (character) return character;
  }
  return t(`layer.${layer.kind}`);
}

/** cameraOption maps a stored or scripted camera ("low angle") to a segmented option. */
export function cameraOption(value = ''): '' | 'low' | 'eye' | 'high' | 'bird' {
  const v = value.toLowerCase();
  if (v.includes('bird') || v.includes('overhead') || v.includes('top-down') || v.includes('aerial')) return 'bird';
  if (v.includes('low') || v.includes('worm')) return 'low';
  if (v.includes('high')) return 'high';
  if (v.includes('eye') || v.includes('level') || v.includes('straight')) return 'eye';
  return '';
}

/** shotOption maps a stored or scripted shot ("extreme close-up") to a segmented option. */
export function shotOption(value = ''): '' | 'wide' | 'medium' | 'close' {
  const v = value.toLowerCase();
  if (v.includes('close') || v.includes('detail')) return 'close';
  if (v.includes('medium') || v.includes('mid') || v.includes('waist')) return 'medium';
  if (v.includes('wide') || v.includes('establish') || v.includes('long') || v.includes('full') || v.includes('splash')) return 'wide';
  return '';
}

/** initials for an avatar chip ("Rin Asagiri" → "RA", "凛" → "凛"). */
export function initials(name: string): string {
  const words = name.trim().split(/\s+/).filter(Boolean);
  if (!words.length) return '?';
  const first = Array.from(words[0])[0] || '', second = words.length > 1 ? Array.from(words[words.length - 1])[0] || '' : '';
  return (first + second).toUpperCase();
}

/** avatarTone picks one of the kit's six avatar colours from an id. */
export function avatarTone(id: string): number {
  let h = 0;
  for (const c of id) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  return h % 6 + 1;
}

/** pxToPt converts a page-pixel font size to points at the print trim width. */
export function pxToPt(px: number, pageWidth: number, trimWidthMM?: number): number {
  if (!trimWidthMM || pageWidth <= 0) return px;
  return px * trimWidthMM / pageWidth * 72 / 25.4;
}

/** ptToPx converts points back to page pixels. */
export function ptToPx(pt: number, pageWidth: number, trimWidthMM?: number): number {
  if (!trimWidthMM || pageWidth <= 0) return pt;
  return pt * 25.4 / 72 * pageWidth / trimWidthMM;
}

/** seconds between two ISO times (or until now), rounded, never negative. */
export function elapsed(from?: string, to?: string, now = Date.now()): number {
  if (!from) return 0;
  const a = Date.parse(from), b = to ? Date.parse(to) : now;
  return Number.isFinite(a) && Number.isFinite(b) ? Math.max(0, Math.round((b - a) / 1000)) : 0;
}

/** jobSeconds is how long a job ran (or has been running). */
export function jobSeconds(job: Pick<Job, 'created_at' | 'started_at' | 'finished_at'>, now = Date.now()): number {
  return elapsed(job.started_at || job.created_at, job.finished_at, now);
}

/** savedAgo formats the footer's save time. */
export function savedAgo(savedAt: number, now: number, t: (key: string, args?: Record<string, string | number>) => string, date: (ms: number) => string = ms => new Date(ms).toLocaleDateString()): string {
  const s = Math.max(0, Math.round((now - savedAt) / 1000));
  if (s < 5) return t('save.just_now');
  if (s < 60) return t('save.seconds', {count: s});
  if (s < 3600) return t('save.minutes', {count: Math.floor(s / 60)});
  if (s < 86400) return t('save.hours', {count: Math.floor(s / 3600)});
  return t('save.date', {date: date(savedAt)});
}

/* ---- autosave ------------------------------------------------------------ */

export type SaveState = 'saved' | 'dirty' | 'saving' | 'conflict' | 'locked' | 'invalid' | 'error';
export type SaveResult = 'ok' | 'conflict' | 'locked' | 'invalid' | 'error';
export interface Clock {set(fn: () => void, ms: number): unknown; clear(id: unknown): void; now(): number}
const realClock: Clock = {set: (fn, ms) => setTimeout(fn, ms), clear: id => clearTimeout(id as ReturnType<typeof setTimeout>), now: () => Date.now()};

/**
 * Autosave saves a draft a short while after the last change, at once on
 * flush (Cmd/Ctrl+S, mode or page switch, leaving), and never runs two saves
 * at the same time. A revision conflict or a lock stops it until the draft is
 * reloaded; a network failure retries with backoff; invalid documents wait
 * for the next change.
 */
export class Autosave {
  state: SaveState = 'saved';
  savedAt: number | null = null;
  private timer: unknown = null;
  private running: Promise<SaveResult> | null = null;
  private retries = 0;
  constructor(
    private readonly save: () => Promise<SaveResult>,
    private readonly dirty: () => boolean,
    private readonly notify: (a: Autosave) => void = () => {},
    readonly delay = 1500,
    private readonly clock: Clock = realClock,
  ) {}

  get blocked(): boolean { return this.state === 'conflict' || this.state === 'locked'; }
  get saving(): boolean { return this.running !== null; }

  /** change records an edit and schedules a save. */
  change(): void {
    if (this.blocked) return;
    if (!this.dirty()) { this.cancel(); if (!this.running) this.set('saved'); return; }
    if (!this.running) this.set('dirty');
    this.schedule(this.delay);
  }

  /** flush saves now (after any save in flight) and reports whether the draft is saved. */
  async flush(): Promise<boolean> {
    this.cancel();
    while (this.running) await this.running;
    if (this.blocked) return false;
    if (!this.dirty()) { if (this.state !== 'saved') this.set('saved'); return true; }
    this.set('saving');
    const run = this.save();
    this.running = run;
    let result: SaveResult;
    try { result = await run; } catch { result = 'error'; } finally { this.running = null; }
    switch (result) {
      case 'ok':
        this.retries = 0;
        this.savedAt = this.clock.now();
        if (this.dirty()) { this.set('dirty'); this.schedule(this.delay); return false; }
        this.set('saved');
        return true;
      case 'error':
        this.retries++;
        this.set('error');
        this.schedule(Math.min(30000, 2000 * 2 ** Math.min(this.retries, 4)));
        return false;
      default:
        this.set(result);
        return false;
    }
  }

  /** reset marks a freshly loaded or reloaded page as saved and clears a conflict or lock. */
  reset(savedAt: number | null = this.clock.now()): void {
    this.cancel();
    this.retries = 0;
    this.savedAt = savedAt;
    this.set('saved');
  }

  dispose(): void { this.cancel(); }

  private schedule(ms: number) {
    this.cancel();
    this.timer = this.clock.set(() => { this.timer = null; void this.flush(); }, ms);
  }

  private cancel() {
    if (this.timer !== null) this.clock.clear(this.timer);
    this.timer = null;
  }

  private set(state: SaveState) { this.state = state; this.notify(this); }
}
