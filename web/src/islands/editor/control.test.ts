import {describe, it, expect} from 'vitest';
import {alphaBounds, changedPanels, driftEvidence, firstName, historyRows, lockState, pageBlock, quoteChip, sketchFileOK, sketchSizeOK} from './control';
import type {DriftPanel, LockStatus, Page, Version} from './types';

function page(): Page {
  return {schema: 'mangaveyra.page.v1', id: 'p', project_id: 'project', chapter_id: 'chapter', number: 7, width: 600, height: 800, direction: 'rtl', locked: false, version: 9,
    panels: [
      {id: 'a', order: 0, points: [{x: 0, y: 0}, {x: 600, y: 0}, {x: 600, y: 300}, {x: 0, y: 300}], prompt: 'Rin runs.', prompt_source: 'script', locked: false,
        layers: [{id: 'bg', kind: 'background', order: 0, visible: true, locked: false, opacity: 1, transform: {}, provenance: 'ai', asset: {id: 'asset-1', url: '/media/1?sig=a'}},
          {id: 'text', kind: 'lettering', order: 3, visible: true, locked: false, opacity: 1, transform: {}, provenance: 'human'}]},
      {id: 'b', order: 1, points: [{x: 0, y: 320}, {x: 600, y: 320}, {x: 600, y: 800}, {x: 0, y: 800}], prompt: 'Kaito waits.', prompt_source: 'script', locked: false,
        layers: [{id: 'bg2', kind: 'background', order: 0, visible: true, locked: false, opacity: 1, transform: {}, provenance: 'ai'}]},
    ],
    bubbles: [{id: 'b1', panel_id: 'a', kind: 'speech', order: 0, box: {x: 10, y: 10, w: 100, h: 60}, shape: 'ellipse', tail: {enabled: true, tip: {x: 50, y: 90}}, style: {auto_fit: true}, vertical: false, texts: {en: {text: 'Late again.', status: 'draft', source: 'human'}}}]};
}
const version = (number: number): Version => ({id: `v${number}`, number, action: 'save', summary: '', created_at: '2026-11-08T00:00:00Z'});

describe('D4 sketch limits', () => {
  it('accepts PNG, JPEG and WebP up to 16 MiB', () => {
    expect(sketchFileOK('image/png', 1)).toBe(true);
    expect(sketchFileOK('image/webp', 16 << 20)).toBe(true);
    expect(sketchFileOK('image/png', (16 << 20) + 1)).toBe(false);
    expect(sketchFileOK('image/gif', 10)).toBe(false);
  });
  it('rejects sides outside 64–4096 px and images above 16 MP', () => {
    expect(sketchSizeOK(64, 4096)).toBe(true);
    expect(sketchSizeOK(4096, 4096)).toBe(true);
    expect(sketchSizeOK(5000, 300)).toBe(false);
    expect(sketchSizeOK(63, 500)).toBe(false);
    expect(sketchSizeOK(4096, 4097)).toBe(false);
    expect(sketchSizeOK(NaN, 100)).toBe(false);
  });
});

describe('page-scope writability', () => {
  const access = {art: true, generate: true, chapterLocked: false};
  it('blocks a whole-page action on a locked panel or art layer, not on locked lettering', () => {
    const p = page();
    expect(pageBlock(p, access)).toBe('');
    p.panels[0].layers[1].locked = true;
    expect(pageBlock(p, access)).toBe('');
    p.panels[0].layers[0].locked = true;
    expect(pageBlock(p, access)).toBe('control.blocked_panel');
    p.panels[0].layers[0].locked = false; p.panels[1].locked = true;
    expect(pageBlock(p, access)).toBe('control.blocked_panel');
  });
  it('names page, chapter and permission locks first', () => {
    const p = page(); p.locked = true;
    expect(pageBlock(p, access)).toBe('control.blocked_page');
    expect(pageBlock(p, {...access, chapterLocked: true})).toBe('control.blocked_chapter');
    expect(pageBlock(p, {...access, generate: false})).toBe('control.blocked_permission');
  });
});

describe('D10 history', () => {
  it('offers Undo on the newest snapshot, restoring the one before it', () => {
    const rows = historyRows([version(9), version(8), version(7)], 9);
    expect(rows.map(r => r.action)).toEqual(['undo', 'restore', 'restore']);
    expect(rows[0].target?.number).toBe(8);
    expect(rows[2].target?.number).toBe(7);
  });
  it('has nothing to undo with a single snapshot', () => {
    expect(historyRows([version(1)], 1).map(r => r.action)).toEqual(['current']);
  });
  it('marks only the panels that differ, ignoring signed URLs', () => {
    const now = page(), old = page();
    old.panels[0].layers[0].asset = {id: 'asset-1', url: '/media/1?sig=other'};
    expect(changedPanels(now, old)).toEqual({outlined: [], count: 0});
    old.panels[1].prompt = 'Kaito leaves.';
    old.bubbles[0].texts.en.text = 'Early.';
    expect(changedPanels(now, old)).toEqual({outlined: ['a', 'b'], count: 2});
  });
  it('counts panels added since the snapshot without outlining them', () => {
    const now = page(), old = page();
    old.panels = old.panels.slice(0, 1);
    expect(changedPanels(now, old)).toEqual({outlined: [], count: 1});
  });
});

describe('D12 evidence', () => {
  const drift = (identity: number, outfit?: number) => ({identity, outfit, score: Math.min(identity, outfit ?? 1)}) as DriftPanel;
  it('names the measured part below the threshold', () => {
    expect(driftEvidence(drift(.78), .9)).toBe('identity');
    expect(driftEvidence(drift(.95, .8), .9)).toBe('outfit');
    expect(driftEvidence(drift(.7, .8), .9)).toBe('both');
    expect(driftEvidence(null, .9)).toBe('');
  });
  it('separates unassessed chapters from clear ones', () => {
    const status = (o: Partial<LockStatus>) => ({drift: [], threshold: .9, ...o}) as LockStatus;
    expect(lockState(null)).toBe('loading');
    expect(lockState(status({}))).toBe('unassessed');
    expect(lockState(status({score: .95}))).toBe('clear');
    expect(lockState(status({score: .8, drift_total: 2}))).toBe('drift');
  });
  it('uses the first name in proof tags', () => {
    expect(firstName(' Rin Asagiri ')).toBe('Rin');
  });
  it('finds the visible region of a transparent character layer', () => {
    const w = 10, h = 10, data = new Uint8ClampedArray(w * h * 4);
    for (const [x, y] of [[2, 3], [6, 8]]) data[(y * w + x) * 4 + 3] = 255;
    expect(alphaBounds(data, w, h)).toEqual({x: .2, y: .3, w: .5, h: .6});
    expect(alphaBounds(new Uint8ClampedArray(w * h * 4), w, h)).toBeNull();
    expect(alphaBounds(new Uint8ClampedArray(w * h * 4).fill(255), w, h)).toBeNull();
  });
});

describe('route chip', () => {
  const base = {blocked: '', needsSelection: false, loading: false, failed: false, ready: false};
  it('asks for a selection or names a lock instead of reporting a failed estimate', () => {
    expect(quoteChip({...base, needsSelection: true})).toBe('select');
    expect(quoteChip({...base, blocked: 'Locked', failed: true})).toBe('blocked');
    expect(quoteChip({...base, loading: true})).toBe('loading');
    expect(quoteChip({...base, failed: true})).toBe('failed');
    expect(quoteChip({...base, ready: true})).toBe('ready');
  });
});
