/**
 * Guided panel-by-panel view: the reading sequence of frozen panels and the
 * geometry that frames one panel of a page image inside the stage.
 */
import type { GuidedBubble, GuidedPanel, Page, Point, T } from './types';

/** One guided step: a panel of a page (panel 0 is a page without panels). */
export interface GuidedStep { page: number; panel: number; index: number; total: number }

/** sequence lists every guided step of the chapter in reading order. */
export function sequence(pages: Page[]): GuidedStep[] {
  const out: GuidedStep[] = [];
  let index = 0;
  for (const p of [...pages].sort((a, b) => a.number - b.number)) {
    const panels = orderedPanels(p);
    if (panels.length === 0) {
      out.push({ page: p.number, panel: 0, index: index++, total: 1 });
      continue;
    }
    panels.forEach((pn, i) => out.push({ page: p.number, panel: pn.order, index: index++, total: panels.length }));
  }
  return out;
}

/** orderedPanels returns a page's panels by reading order, skipping empty geometry. */
export function orderedPanels(p: Page): GuidedPanel[] {
  return [...(p.panels || [])].filter((pn) => pn.w > 0 && pn.h > 0).sort((a, b) => a.order - b.order);
}

/** stepIndex finds the step of page/panel (the page's first panel when panel is 0 or unknown). */
export function stepIndex(steps: GuidedStep[], page: number, panel = 0): number {
  const exact = steps.findIndex((s) => s.page === page && s.panel === panel);
  if (exact >= 0) return exact;
  const first = steps.findIndex((s) => s.page === page);
  if (first >= 0) return first;
  const after = steps.findIndex((s) => s.page > page);
  return after >= 0 ? after : Math.max(0, steps.length - 1);
}

/** panelPosition is the 1-based position of the step's panel on its page. */
export function panelPosition(steps: GuidedStep[], i: number): number {
  const s = steps[i];
  if (!s) return 0;
  return steps.filter((o) => o.page === s.page && o.index <= s.index).length;
}

/** Frame is where a panel sits in the stage and how the page image is placed in it. */
export interface Frame { width: number; height: number; scale: number; imageX: number; imageY: number; imageW: number; imageH: number }

/**
 * frame fits the panel's bounding box (page pixels) into the available
 * stage area, never enlarging beyond maxScale, and positions the whole page
 * image so that only the panel shows inside the frame.
 */
export function frame(panel: { x: number; y: number; w: number; h: number }, page: { width: number; height: number }, availW: number, availH: number, maxScale = 4): Frame {
  const bw = Math.max(1, panel.w);
  const bh = Math.max(1, panel.h);
  const scale = Math.max(0.01, Math.min(availW / bw, availH / bh, maxScale));
  return {
    width: Math.round(bw * scale),
    height: Math.round(bh * scale),
    scale,
    imageX: -panel.x * scale,
    imageY: -panel.y * scale,
    imageW: page.width * scale,
    imageH: page.height * scale,
  };
}

/** wholePage is the frame for a page without panels. */
export function wholePage(page: { width: number; height: number }, availW: number, availH: number): Frame {
  return frame({ x: 0, y: 0, w: page.width, h: page.height }, page, availW, availH, 1.5);
}

/** maskPath is an SVG path (even-odd) covering the panel's box outside its polygon, in box coordinates. */
export function maskPath(panel: { x: number; y: number; w: number; h: number; points: Point[] }): string {
  const pts = panel.points || [];
  const box = `M0 0H${fmt(panel.w)}V${fmt(panel.h)}H0Z`;
  if (pts.length < 3) return '';
  const poly = pts.map((p, i) => `${i === 0 ? 'M' : 'L'}${fmt(p.x - panel.x)} ${fmt(p.y - panel.y)}`).join('') + 'Z';
  return box + poly;
}

/** outlinePoints are the polygon's points relative to its box (SVG points attribute). */
export function outlinePoints(panel: { x: number; y: number; points: Point[] }): string {
  return (panel.points || []).map((p) => `${fmt(p.x - panel.x)},${fmt(p.y - panel.y)}`).join(' ');
}

function fmt(n: number): string {
  return String(Math.round(n * 10) / 10);
}

/** bubbleLine reads one lettered line ("Rin Asagiri: Keep moving!"). */
export function bubbleLine(b: GuidedBubble, t: T): string {
  const text = (b.text || '').trim();
  if (!text) return '';
  if (b.kind === 'sfx') return t('line.sfx', { text });
  if (b.kind === 'caption') return t('line.caption', { text });
  if (b.speaker && b.kind === 'thought') return t('line.thought', { speaker: b.speaker, text });
  if (b.speaker) return t('line.speech', { speaker: b.speaker, text });
  return text;
}

/** spokenText is what read-aloud says for a panel: its alt-text, then its lines in reading order. */
export function spokenText(panel: GuidedPanel | undefined, page: Page | undefined, t: T): string {
  if (!panel) return (page?.alt || '').trim();
  const lines = [...(panel.bubbles || [])].sort((a, b) => a.order - b.order).map((b) => bubbleLine(b, t)).filter(Boolean);
  return [panel.alt_text?.trim(), ...lines].filter(Boolean).join(' ');
}
