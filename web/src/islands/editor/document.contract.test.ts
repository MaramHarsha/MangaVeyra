import {describe, it, expect} from 'vitest';
import {
  Autosave, DraftHistory, FONT_FAMILIES, FONT_KEYS, type SaveResult, cameraOption, defaultFont, initials, jobSeconds, layerLabel,
  moveBubbleTo, newBubble, normalizeLanguage, ptToPx, pxToPt, savedAgo, setBubbleKind, shotOption, verticalAllowed, verticalByDefault,
} from './document';
import type {Layer, Page, Rect} from './types';

function page(): Page {
  return {schema: 'mangaveyra.page.v1', id: 'p', project_id: 'project', chapter_id: 'chapter', number: 1, width: 600, height: 800, direction: 'rtl', locked: false, version: 1,
    panels: [{id: 'panel', order: 0, points: [{x: 50, y: 50}, {x: 550, y: 50}, {x: 550, y: 750}, {x: 50, y: 750}], prompt: 'Rin runs.', prompt_source: 'script', locked: false,
      layers: [{id: 'background', kind: 'background', order: 0, visible: true, locked: false, opacity: 1, transform: {}, provenance: 'ai'}]}],
    bubbles: []};
}

function twoPanels(): Page {
  const p = page();
  p.panels[0].points = [{x: 50, y: 50}, {x: 550, y: 50}, {x: 550, y: 390}, {x: 50, y: 390}];
  const lower = structuredClone(p.panels[0]);
  lower.id = 'lower';
  lower.points = [{x: 50, y: 410}, {x: 550, y: 410}, {x: 550, y: 750}, {x: 50, y: 750}];
  p.panels.push(lower);
  return p;
}

const inside = (p: Page, id: string, r: Rect) => {
  const b = p.panels.find(v => v.id === id)!.points;
  return r.x >= b[0].x && r.y >= b[0].y && r.x + r.w <= b[2].x && r.y + r.h <= b[2].y;
};
const apart = (a: Rect, b: Rect) => a.x >= b.x + b.w || b.x >= a.x + a.w || a.y >= b.y + b.h || b.y >= a.y + a.h;

describe('bubble placement', () => {
  it('places each new bubble inside the selected panel, clear of earlier bubbles', () => {
    const p = twoPanels();
    for (const kind of ['speech', 'thought', 'caption', 'sfx'] as const) {
      const b = newBubble(p, 'lower', kind, 'en');
      expect(b.panel_id).toBe('lower');
      expect(inside(p, 'lower', b.box)).toBe(true);
      for (const other of p.bubbles) expect(apart(b.box, other.box)).toBe(true);
      p.bubbles.push(b);
    }
    expect(new Set(p.bubbles.map(b => `${b.box.x},${b.box.y}`)).size).toBe(4);
  });

  it('starts in the reading-order corner of the panel', () => {
    const rtl = twoPanels(), ltr = twoPanels();
    ltr.direction = 'ltr';
    const a = newBubble(rtl, 'panel', 'speech', 'en').box, b = newBubble(ltr, 'panel', 'speech', 'en').box;
    expect(a.x + a.w).toBeGreaterThan(450);
    expect(b.x).toBeLessThan(150);
    expect(a.y).toBeLessThan(120);
  });

  it('sizes bubbles by type instead of one fixed box', () => {
    const p = page();
    const caption = newBubble(p, 'panel', 'caption', 'en').box, thought = newBubble(p, 'panel', 'thought', 'en').box;
    expect(caption.h).toBeLessThan(thought.h);
    expect(caption).not.toEqual(thought);
  });

  it('cascades inside the panel and the page when the panel is full', () => {
    const p = page();
    p.panels[0].points = [{x: 100, y: 100}, {x: 260, y: 100}, {x: 260, y: 220}, {x: 100, y: 220}];
    const seen = new Set<string>();
    for (let i = 0; i < 5; i++) {
      const b = newBubble(p, 'panel', 'speech', 'en');
      expect(b.box.x).toBeGreaterThanOrEqual(99);
      expect(b.box.x + b.box.w).toBeLessThanOrEqual(261);
      expect(b.box.y).toBeGreaterThanOrEqual(0);
      expect(b.box.y + b.box.h).toBeLessThanOrEqual(p.height);
      seen.add(`${b.box.x},${b.box.y}`);
      p.bubbles.push(b);
    }
    expect(seen.size).toBeGreaterThan(1);
  });

  it('keeps the speech tail close to its bubble and inside the panel', () => {
    const p = twoPanels(), b = newBubble(p, 'lower', 'speech', 'en');
    expect(Math.abs(b.tail.tip.y - (b.box.y + b.box.h))).toBeLessThanOrEqual(48);
    expect(b.tail.tip.y).toBeLessThanOrEqual(750);
    expect(b.tail.tip.x).toBeGreaterThanOrEqual(b.box.x);
    expect(b.tail.tip.x).toBeLessThanOrEqual(b.box.x + b.box.w);
  });
});

describe('content language defaults', () => {
  it('chooses the contract font for each language and SFX in Latin scripts', () => {
    const expected: Record<string, string> = {en: 'comic', ja: 'jp', ko: 'ko', 'zh-Hans': 'sc', 'zh-Hant': 'tc', es: 'comic', fr: 'comic', de: 'comic', pt: 'comic', hi: 'deva'};
    for (const [lang, font] of Object.entries(expected)) {
      expect(defaultFont(lang, 'speech')).toBe(font);
      expect(newBubble(page(), 'panel', 'speech', lang).style.font).toBe(font);
    }
    expect(defaultFont('en', 'sfx')).toBe('sfx');
    expect(defaultFont('fr', 'sfx')).toBe('sfx');
    expect(defaultFont('ja', 'sfx')).toBe('jp');
    expect(defaultFont('hi', 'sfx')).toBe('deva');
  });

  it('allows vertical lettering only for CJK and defaults it for Japanese and Traditional Chinese', () => {
    for (const lang of ['ja', 'ko', 'zh-Hans', 'zh-Hant']) expect(verticalAllowed(lang)).toBe(true);
    for (const lang of ['en', 'es', 'fr', 'de', 'pt', 'hi']) expect(verticalAllowed(lang)).toBe(false);
    expect(['ja', 'zh-Hant'].every(verticalByDefault)).toBe(true);
    expect(['ko', 'zh-Hans', 'en', 'hi'].some(verticalByDefault)).toBe(false);
    expect(newBubble(page(), 'panel', 'speech', 'zh-Hant').vertical).toBe(true);
    expect(newBubble(page(), 'panel', 'speech', 'ko').vertical).toBe(false);
    expect(verticalByDefault('zh-TW')).toBe(true);
    expect(normalizeLanguage('ko-KR')).toBe('ko');
    expect(normalizeLanguage('zh')).toBe('zh-Hans');
    expect(normalizeLanguage('xx')).toBe('');
  });

  it('lists every font key with its family name', () => {
    expect(FONT_KEYS).toEqual(['comic', 'sfx', 'jp', 'ko', 'sc', 'tc', 'deva']);
    expect(FONT_KEYS.map(k => FONT_FAMILIES[k])).toEqual(['Comic Neue Bold', 'Bangers', 'Noto Sans JP', 'Noto Sans KR', 'Noto Sans SC', 'Noto Sans TC', 'Noto Sans Devanagari']);
  });

  it('changes a bubble type and shape while keeping its text, box and a chosen font', () => {
    const b = newBubble(page(), 'panel', 'speech', 'en');
    b.texts.en.text = 'Run!';
    const box = {...b.box};
    setBubbleKind(b, 'sfx', 'en');
    expect([b.kind, b.shape, b.style.font, b.tail.enabled, b.texts.en.text]).toEqual(['sfx', 'none', 'sfx', false, 'Run!']);
    expect(b.box).toEqual(box);
    b.style.font = 'jp';
    setBubbleKind(b, 'shout', 'en');
    expect([b.shape, b.style.font, b.tail.enabled]).toEqual(['burst', 'jp', true]);
  });
});

describe('display helpers', () => {
  const t = (key: string) => ({'layer.background': '背景', 'layer.fx': '効果', 'layer.lettering': '文字', 'layer.character': 'キャラクター'} as Record<string, string>)[key] || key;
  const layer = (kind: Layer['kind'], name?: string, character_id?: string): Layer => ({id: kind, kind, name, character_id, order: 0, visible: true, locked: false, opacity: 1, transform: {}, provenance: 'human'});

  it('localizes default layer names by kind and keeps character and custom names', () => {
    expect(layerLabel(layer('lettering', 'Lettering'), [], t)).toBe('文字');
    expect(layerLabel(layer('fx', 'Effects'), [], t)).toBe('効果');
    expect(layerLabel(layer('background', 'Background'), [], t)).toBe('背景');
    expect(layerLabel(layer('background'), [], t)).toBe('背景');
    expect(layerLabel(layer('background', 'Background · rooftop'), [], t)).toBe('Background · rooftop');
    expect(layerLabel(layer('fx', 'FX'), [], t)).toBe('効果');
    expect(layerLabel(layer('character', 'Rin Asagiri', 'c1'), [{id: 'c1', name: 'Rin Asagiri'}], t)).toBe('Rin Asagiri');
    expect(layerLabel(layer('character', '', 'c1'), [{id: 'c1', name: 'Rin Asagiri'}], t)).toBe('Rin Asagiri');
    expect(layerLabel(layer('character', 'Character', 'c1'), [{id: 'c1', name: 'Rin Asagiri'}], t)).toBe('Rin Asagiri');
    expect(layerLabel(layer('character', 'Character', 'gone'), [], t)).toBe('キャラクター');
    expect(layerLabel(layer('character', 'Mira Wend', 'gone'), [], t)).toBe('Mira Wend');
  });

  it('selects the camera and shot segments for scripted values', () => {
    expect(['low angle', 'eye level', 'high angle', "bird's-eye view", 'storyboard'].map(v => cameraOption(v))).toEqual(['low', 'eye', 'high', 'bird', '']);
    expect(['wide establishing', 'medium shot', 'extreme close-up', 'neutral'].map(v => shotOption(v))).toEqual(['wide', 'medium', 'close', '']);
  });

  it('converts lettering sizes between page pixels and print points', () => {
    // 40 px on a 1600 px wide B5 page is 4.55 mm, about 12.9 pt.
    expect(Math.round(pxToPt(40, 1600, 182))).toBe(13);
    expect(ptToPx(pxToPt(40, 1600, 182), 1600, 182)).toBeCloseTo(40);
    expect(pxToPt(40, 1600)).toBe(40);
  });

  it('builds chip initials and job timings', () => {
    expect(initials('Rin Asagiri')).toBe('RA');
    expect(initials('凛')).toBe('凛');
    expect(jobSeconds({created_at: '2026-01-01T00:00:00Z', started_at: '2026-01-01T00:00:02Z', finished_at: '2026-01-01T00:00:40Z'})).toBe(38);
    expect(jobSeconds({created_at: '2026-01-01T00:00:00Z'}, Date.parse('2026-01-01T00:00:05Z'))).toBe(5);
  });

  it('formats the footer save time', () => {
    const tt = (key: string, args: Record<string, string | number> = {}) => `${key}:${Object.values(args).join(',')}`;
    expect(savedAgo(1000, 3000, tt)).toBe('save.just_now:');
    expect(savedAgo(0, 12000, tt)).toBe('save.seconds:12');
    expect(savedAgo(0, 125000, tt)).toBe('save.minutes:2');
    expect(savedAgo(0, 7200000, tt)).toBe('save.hours:2');
    expect(savedAgo(0, 90000000, tt, () => 'Jan 1')).toBe('save.date:Jan 1');
  });

  it('moves a bubble to a reading position one step at a time', () => {
    const p = page();
    for (let i = 0; i < 4; i++) p.bubbles.push(newBubble(p, 'panel', 'speech', 'en'));
    const id = p.bubbles[3].id, moved = moveBubbleTo(p, id, 0);
    expect([...moved.bubbles].sort((a, b) => a.order - b.order)[0].id).toBe(id);
    expect(moved.bubbles.map(b => b.order).sort()).toEqual([0, 1, 2, 3]);
  });

  it('ignores the stored revision number when deciding whether a draft is dirty, keeping undo across saves', () => {
    const p = page(), h = new DraftHistory(p);
    h.rebase({...structuredClone(p), version: 7});
    expect(h.dirty).toBe(false);
    expect(h.current.version).toBe(7);
    const edited = structuredClone(h.current);
    edited.note = 'x';
    h.commit(edited);
    h.accept({...edited, version: 8});
    expect(h.dirty).toBe(false);
    expect(h.past).toHaveLength(1);
  });
});

describe('autosave', () => {
  function clock() {
    let now = 0, id = 0;
    const timers = new Map<number, {at: number; fn: () => void}>();
    return {
      now: () => now,
      set: (fn: () => void, ms: number) => { timers.set(++id, {at: now + ms, fn}); return id; },
      clear: (i: unknown) => { timers.delete(i as number); },
      // tick advances time, firing each due timer at its own due time.
      async tick(ms: number) {
        const end = now + ms;
        for (;;) {
          const due = [...timers].filter(([, timer]) => timer.at <= end).sort((a, b) => a[1].at - b[1].at)[0];
          if (!due) break;
          timers.delete(due[0]);
          now = due[1].at;
          due[1].fn();
          for (let k = 0; k < 6; k++) await Promise.resolve();
        }
        now = end;
        for (let k = 0; k < 6; k++) await Promise.resolve();
      },
      pending: () => timers.size,
    };
  }
  // The fake save stores the edit count it started with, like the editor
  // saving a snapshot: edits made while it runs stay unsaved.
  function setup(results: SaveResult[]) {
    const c = clock();
    let edits = 0, saved = 0, hold = false, release: (() => void) | null = null;
    const calls: number[] = [];
    const a = new Autosave(async () => {
      calls.push(c.now());
      const snapshot = edits;
      if (hold) await new Promise<void>(r => { release = r; });
      const r = results.shift() || 'ok';
      if (r === 'ok') saved = snapshot;
      return r;
    }, () => edits > saved, () => {}, 1500, c);
    return {a, c, calls, edit() { edits++; a.change(); }, holdNext() { hold = true; }, release() { hold = false; release?.(); }, undoAll() { saved = edits; }};
  }

  it('saves once, 1.5 s after the last change', async () => {
    const s = setup([]);
    s.edit();
    await s.c.tick(1000);
    s.edit();
    await s.c.tick(1000);
    expect(s.calls).toHaveLength(0);
    expect(s.a.state).toBe('dirty');
    await s.c.tick(600);
    expect(s.calls).toEqual([2500]);
    expect(s.a.state).toBe('saved');
    expect(s.a.savedAt).toBe(2500);
  });

  it('saves at once on flush and reports success', async () => {
    const s = setup([]);
    s.edit();
    expect(await s.a.flush()).toBe(true);
    expect(s.calls).toEqual([0]);
    expect(s.c.pending()).toBe(0);
    expect(await s.a.flush()).toBe(true);
    expect(s.calls).toHaveLength(1);
  });

  it('never overlaps saves and saves edits made during a save afterwards', async () => {
    const s = setup([]);
    s.edit();
    s.holdNext();
    const first = s.a.flush();
    await Promise.resolve();
    expect(s.a.state).toBe('saving');
    s.edit();
    const second = s.a.flush();
    s.release();
    await first;
    await second;
    expect(s.calls).toHaveLength(2);
    expect(s.a.state).toBe('saved');
  });

  it('stops on a conflict or lock until reset, and waits for the next edit after an invalid save', async () => {
    const s = setup(['conflict']);
    s.edit();
    expect(await s.a.flush()).toBe(false);
    expect(s.a.state).toBe('conflict');
    s.edit();
    await s.c.tick(5000);
    expect(s.calls).toHaveLength(1);
    s.a.reset();
    expect(s.a.state).toBe('saved');

    const l = setup(['locked']);
    l.edit();
    await l.a.flush();
    expect(l.a.state).toBe('locked');
    expect(l.a.blocked).toBe(true);

    const v = setup(['invalid']);
    v.edit();
    await v.a.flush();
    expect(v.a.state).toBe('invalid');
    expect(v.c.pending()).toBe(0);
    v.edit();
    await v.c.tick(1500);
    expect(v.calls).toHaveLength(2);
    expect(v.a.state).toBe('saved');
  });

  it('retries network failures with backoff', async () => {
    const s = setup(['error', 'error']);
    s.edit();
    await s.a.flush();
    expect(s.a.state).toBe('error');
    await s.c.tick(3000);
    expect(s.calls).toHaveLength(1);
    await s.c.tick(1100);
    expect(s.calls).toHaveLength(2);
    expect(s.a.state).toBe('error');
    await s.c.tick(8000);
    expect(s.calls).toHaveLength(3);
    expect(s.a.state).toBe('saved');
  });

  it('returns to saved when an edit is undone before the save runs', () => {
    const s = setup([]);
    s.edit();
    expect(s.a.state).toBe('dirty');
    s.undoAll();
    s.a.change();
    expect(s.a.state).toBe('saved');
    expect(s.c.pending()).toBe(0);
  });
});
