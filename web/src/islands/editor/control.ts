// Pure rules for the D4 / D10 / D12 control workbenches. They are kept out of
// the Svelte components so the behaviour stays testable without a browser.
import type {Bubble, DriftPanel, LockStatus, Page, Panel, Version} from './types';

/* ---- D4 sketch limits (ADR 0010: 16 MiB, 64–4096 px per side, 16 MP) ---- */

export const SKETCH_TYPES = ['image/png', 'image/jpeg', 'image/webp'];
export const SKETCH_MAX_BYTES = 16 << 20;
export const SKETCH_MIN_SIDE = 64;
export const SKETCH_MAX_SIDE = 4096;
// The server decodes with MaxPixels 16 << 20; the same bound applies here.
export const SKETCH_MAX_PIXELS = 16 << 20;

/** sketchFileOK checks what is known before decoding: the type and byte size. */
export function sketchFileOK(type: string, size: number): boolean {
  return SKETCH_TYPES.includes(type) && size > 0 && size <= SKETCH_MAX_BYTES;
}

/** sketchSizeOK checks decoded pixel dimensions against the server's limits. */
export function sketchSizeOK(width: number, height: number): boolean {
  return Number.isFinite(width) && Number.isFinite(height) && width >= SKETCH_MIN_SIDE && height >= SKETCH_MIN_SIDE &&
    width <= SKETCH_MAX_SIDE && height <= SKETCH_MAX_SIDE && width * height <= SKETCH_MAX_PIXELS;
}

/** imageSize decodes only the image header where the browser allows it. */
export async function imageSize(blob: Blob): Promise<{width: number; height: number} | null> {
  try {
    const bitmap = await createImageBitmap(blob);
    const size = {width: bitmap.width, height: bitmap.height};
    bitmap.close();
    return size;
  } catch {
    return null;
  }
}

/* ---- D1 / D4 writability ---- */

export interface Access {art: boolean; generate: boolean; chapterLocked: boolean}

/**
 * pageBlock names why a whole-page art action (D4 "Ink this page", "Regenerate
 * this page") cannot run, or '' when it can. It mirrors the server's page-scope
 * rule: any locked panel or non-lettering layer blocks the page.
 */
export function pageBlock(page: Page | null | undefined, access: Access): string {
  if (!page) return 'control.blocked_page';
  if (!access.art || !access.generate) return 'control.blocked_permission';
  if (access.chapterLocked) return 'control.blocked_chapter';
  if (page.locked) return 'control.blocked_page';
  if (page.panels.some(p => p.locked || p.layers.some(l => l.locked && l.kind !== 'lettering'))) return 'control.blocked_panel';
  return '';
}

/* ---- D10 history ---- */

export interface HistoryRow {version: Version; action: 'undo' | 'restore' | 'current'; target?: Version}

/**
 * historyRows gives each snapshot its action. The newest snapshot is the saved
 * page itself, so it offers Undo (restoring the snapshot before it) instead of
 * a Restore that would change nothing.
 */
export function historyRows(versions: Version[], pageVersion: number): HistoryRow[] {
  return versions.map((version, i) => {
    if (version.number !== pageVersion) return {version, action: 'restore', target: version};
    const previous = versions.slice(i + 1).find(v => v.number < version.number);
    return previous && i === 0 ? {version, action: 'undo', target: previous} : {version, action: 'current'};
  });
}

// Signed asset URLs change on every read, and lock scores are derived; neither
// is part of a saved change.
function panelSignature(p: Panel, bubbles: Bubble[]): string {
  return JSON.stringify({
    points: p.points, prompt: p.prompt, camera: p.camera || '', shot: p.shot || '', style: p.style || '', pose: p.pose || null, locked: p.locked,
    layers: [...p.layers].sort((a, b) => a.order - b.order).map(l => ({
      id: l.id, kind: l.kind, name: l.name || '', character: l.character_id || '', asset: l.asset?.id || '', mask: l.mask?.id || '',
      visible: l.visible, locked: l.locked, opacity: l.opacity, blend: l.blend || 'normal', transform: l.transform || {},
    })),
    bubbles: bubbles.filter(b => b.panel_id === p.id).sort((a, b) => a.order - b.order).map(b => ({
      id: b.id, kind: b.kind, order: b.order, box: b.box, shape: b.shape, tail: b.tail, style: b.style, vertical: b.vertical, texts: b.texts, speaker: b.speaker_character_id || '',
    })),
  });
}

/**
 * changedPanels compares two page documents panel by panel. `outlined` lists
 * the panels of `other` that differ (they can be marked on its proof); `count`
 * also includes panels that exist only in `current`.
 */
export function changedPanels(current: Page, other: Page): {outlined: string[]; count: number} {
  const now = new Map(current.panels.map(p => [p.id, panelSignature(p, current.bubbles)]));
  const outlined = other.panels.filter(p => now.get(p.id) !== panelSignature(p, other.bubbles)).map(p => p.id);
  const added = current.panels.filter(p => !other.panels.some(o => o.id === p.id)).length;
  return {outlined, count: outlined.length + added};
}

/* ---- D12 character lock ---- */

/** firstName gives the short form the design uses in proof tags ("RIN"). */
export function firstName(name: string): string {
  return name.trim().split(/\s+/)[0] || '';
}

/**
 * driftEvidence names which measured part fell below the threshold, from the
 * stored evidence only: identity, outfit or both. '' when nothing is known.
 */
export function driftEvidence(d: DriftPanel | null | undefined, threshold: number): '' | 'identity' | 'outfit' | 'both' {
  if (!d) return '';
  const identity = d.identity < threshold, outfit = d.outfit !== undefined && d.outfit !== null && d.outfit < threshold;
  return identity && outfit ? 'both' : identity ? 'identity' : outfit ? 'outfit' : '';
}

/** lockState summarises a chapter's evidence for the D12 subtitle and before caption. */
export function lockState(status: LockStatus | null): 'loading' | 'unassessed' | 'drift' | 'clear' {
  if (!status) return 'loading';
  if (status.drift.length || (status.drift_total || 0) > 0) return 'drift';
  return status.score === undefined || status.score === null ? 'unassessed' : 'clear';
}

/**
 * quoteChip decides what the route chip says before dispatch. Only a failed
 * estimate asks for Refresh; a missing selection or a lock is not an error.
 */
export function quoteChip(o: {blocked: string; needsSelection: boolean; loading: boolean; failed: boolean; ready: boolean}): 'blocked' | 'select' | 'loading' | 'failed' | 'ready' {
  if (o.blocked) return 'blocked';
  if (o.needsSelection) return 'select';
  if (o.ready) return 'ready';
  if (o.failed) return 'failed';
  return 'loading';
}

/** alphaBounds finds the visible (non-transparent) box of an RGBA buffer in 0–1 units. */
export function alphaBounds(data: Uint8ClampedArray, width: number, height: number, cutoff = 16): {x: number; y: number; w: number; h: number} | null {
  let x0 = width, y0 = height, x1 = -1, y1 = -1;
  for (let y = 0; y < height; y++) for (let x = 0; x < width; x++) {
    if (data[(y * width + x) * 4 + 3] <= cutoff) continue;
    if (x < x0) x0 = x;
    if (x > x1) x1 = x;
    if (y < y0) y0 = y;
    if (y > y1) y1 = y;
  }
  if (x1 < 0) return null;
  const box = {x: x0 / width, y: y0 / height, w: (x1 - x0 + 1) / width, h: (y1 - y0 + 1) / height};
  // A fully opaque raster has no separable character region to outline.
  return box.w > .98 && box.h > .98 ? null : box;
}

/* ---- Job failure copy ---- */

/** Safe public failure codes with their own editor copy (editor.job.error.<code>). */
export const JOB_ERROR_CODES = ['provider_verification', 'provider_auth', 'spend_limit', 'no_provider', 'rate_limited', 'timeout', 'safety', 'provider_refused', 'local_only', 'scope_locked'] as const;

/** jobMessageKey picks the message for a job state, using the specific copy for a known failure code. */
export function jobMessageKey(job: {state: string; error_code?: string}): string {
  if (job.state === 'failed' && job.error_code && (JOB_ERROR_CODES as readonly string[]).includes(job.error_code)) return `job.error.${job.error_code}`;
  return `job.${job.state}`;
}
