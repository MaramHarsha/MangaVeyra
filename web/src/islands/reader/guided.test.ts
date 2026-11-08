import { describe, expect, it } from 'vitest';
import { frame, maskPath, panelPosition, sequence, spokenText, stepIndex } from './guided';
import { comicsIslandMessages } from './testdata';
import { translator, type Page } from './types';
import { anchorOf, anchoredHere, authorName, items, listURL, load, postBody } from './comments';

const page = (number: number, panels: Page['panels']): Page => ({ id: `p${number}`, number, width: 160, height: 240, src: '', alt: `Page ${number}`, panels });
const panel = (order: number, y: number): Page['panels'][number] => ({
  order, points: [{ x: 0, y }, { x: 160, y }, { x: 160, y: y + 100 }, { x: 0, y: y + 120 }], x: 0, y, w: 160, h: 120,
  alt_text: `Panel ${order} alt`, bubbles: [{ order: 2, kind: 'sfx', text: 'SPLASH' }, { order: 1, kind: 'speech', speaker: 'Rin Asagiri', text: 'Keep moving!' }],
});

describe('guided sequence', () => {
  const pages = [page(2, [panel(2, 120), panel(1, 0)]), page(1, [panel(1, 0)]), page(3, [])];
  const steps = sequence(pages);
  it('walks panels in reading order, page by page', () => {
    expect(steps.map((s) => `${s.page}.${s.panel}`)).toEqual(['1.1', '2.1', '2.2', '3.0']);
    expect(panelPosition(steps, 2)).toBe(2);
    expect(steps[2].total).toBe(2);
  });
  it('finds where to start from a page or a bookmarked panel', () => {
    expect(stepIndex(steps, 2)).toBe(1);
    expect(stepIndex(steps, 2, 2)).toBe(2);
    expect(stepIndex(steps, 9)).toBe(3);
  });
});

describe('guided frame', () => {
  it('fits the panel box into the stage and offsets the page image', () => {
    const f = frame({ x: 0, y: 120, w: 160, h: 120 }, { width: 160, height: 240 }, 800, 400);
    expect(f.scale).toBeCloseTo(400 / 120);
    expect(f.height).toBe(400);
    expect(f.imageY).toBeCloseTo(-120 * (400 / 120));
    expect(f.imageW).toBeCloseTo(160 * (400 / 120));
  });
  it('never enlarges beyond the cap', () => {
    expect(frame({ x: 0, y: 0, w: 10, h: 10 }, { width: 10, height: 10 }, 1000, 1000).scale).toBe(4);
  });
  it('masks the box outside a slanted panel', () => {
    expect(maskPath({ ...panel(1, 0) })).toBe('M0 0H160V120H0ZM0 0L160 0L160 100L0 120Z');
    expect(maskPath({ x: 0, y: 0, w: 1, h: 1, points: [] })).toBe('');
  });
});

describe('alt-text and read-aloud text', () => {
  const t = translator(comicsIslandMessages);
  it('reads the alt-text, then the lines in reading order', () => {
    const p = page(1, [panel(1, 0)]);
    expect(spokenText(p.panels[0], p, t)).toBe('Panel 1 alt Rin Asagiri: Keep moving! Sound: SPLASH');
    expect(spokenText(undefined, p, t)).toBe('Page 1');
  });
});

describe('comments anchoring', () => {
  it('targets the publication and anchors panel comments to page and panel', () => {
    expect(listURL('/api/v1/social/comments', 'panel', 'pub1', 'ser1')).toBe('/api/v1/social/comments?target_kind=panel&target_id=pub1&series=ser1');
    expect(postBody('ser1', 'pub1', 'Nice!', { page: 3, panel: 2 })).toEqual({ series_id: 'ser1', target_kind: 'panel', target_id: 'pub1', anchor: { page: 3, panel: 2 }, body: 'Nice!' });
    expect(postBody('ser1', 'pub1', 'Nice!', null).target_kind).toBe('chapter');
    expect(anchorOf({ id: 'c', body: '', anchor: '{"page":3,"panel":2}' })).toEqual({ page: 3, panel: 2 });
    expect(anchorOf({ id: 'c', body: '', anchor: {} })).toBeNull();
    expect(listURL('/api/v1/social/comments', 'panel', 'pub1', 'ser1', 4)).toContain('&page=4');
    expect(authorName({ id: 'c', body: '', author: { id: 'u', username: 'jo.okafor', display_name: 'Jo Okafor' } })).toBe('Jo Okafor');
    expect(authorName({ id: 'c', body: '', author: { username: 'jo.okafor' } })).toBe('jo.okafor');
    expect(items({ items: [{ id: 'a' }] })).toHaveLength(1);
    expect(items(null)).toEqual([]);
  });
  it('keeps comments whose anchor no longer names a panel at chapter level (re-frozen chapter)', async () => {
    const pages = [page(1, [panel(1, 0), panel(2, 120)]), page(2, [panel(1, 0)])];
    expect(anchoredHere({ page: 1, panel: 2 }, pages)).toBe(true);
    expect(anchoredHere({ page: 2, panel: 2 }, pages)).toBe(false);
    expect(anchoredHere({ page: 9, panel: 1 }, pages)).toBe(false);
    expect(anchoredHere(null, pages)).toBe(false);
    const panelItems = [
      { id: 'a', body: 'here', anchor: { page: 1, panel: 2 } },
      { id: 'b', body: 'past the end', anchor: { page: 9, panel: 1 } },
      { id: 'c', body: 'hidden', anchor: { page: 1, panel: 1 }, hidden: true },
    ];
    const fake = (async (url: string) => new Response(JSON.stringify({ items: url.includes('target_kind=panel') ? panelItems : [{ id: 'd', body: 'chapter' }] }))) as unknown as typeof fetch;
    const got = await load('/api/v1/social/comments', 'pub1', 'ser1', 1, pages, fake);
    expect(got.state).toBe('ready');
    expect(got.panel.map((c) => c.id)).toEqual(['a']);
    expect(got.chapter.map((c) => c.id)).toEqual(['d', 'b']);
    const down = (async () => new Response('{}', { status: 404 })) as unknown as typeof fetch;
    expect((await load('/x', 'pub1', 'ser1', 1, pages, down)).state).toBe('unavailable');
  });
});
