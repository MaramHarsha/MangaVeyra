/**
 * Reading direction, spreads, tap zones and keyboard steps for the E3 paged
 * reader. Manga (RTL) turns forward to the left, comics (LTR) to the right;
 * the keyboard arrows and tap zones follow the same direction.
 */
import type { Direction, Mode } from './types';

export type Step = 'next' | 'prev' | 'first' | 'last' | 'menu' | null;

export interface PageSize { number: number; width: number; height: number }

/** wide reports a landscape page that fills a spread on its own. */
export function wide(p: PageSize): boolean {
  return p.width > 0 && p.height > 0 && p.width > p.height * 1.1;
}

/**
 * spreads groups pages into two-page views: the first page (the cover side)
 * stands alone, then pages pair up in order; a wide page always stands alone,
 * and a page left without a partner before it stands alone too.
 */
export function spreads(pages: PageSize[]): number[][] {
  const out: number[][] = [];
  let i = 0;
  if (pages.length > 0) {
    out.push([pages[0].number]);
    i = 1;
  }
  while (i < pages.length) {
    const a = pages[i];
    const b = pages[i + 1];
    if (wide(a) || !b || wide(b)) {
      out.push([a.number]);
      i += 1;
      continue;
    }
    out.push([a.number, b.number]);
    i += 2;
  }
  return out;
}

/** singles is one view per page. */
export function singles(pages: PageSize[]): number[][] {
  return pages.map((p) => [p.number]);
}

/** viewIndexOf returns the view that shows page (0 when none does). */
export function viewIndexOf(views: number[][], page: number): number {
  const i = views.findIndex((v) => v.includes(page));
  return i < 0 ? 0 : i;
}

/** effectiveMode resolves "auto": Spread on wide screens, Single on narrow ones. */
export function effectiveMode(mode: Mode, viewportWidth: number, webtoon: boolean): Exclude<Mode, 'auto'> {
  if (webtoon) return mode === 'guided' ? 'guided' : 'webtoon';
  if (mode === 'auto') return viewportWidth >= 1100 ? 'spread' : 'paged';
  return mode;
}

/**
 * zoneStep maps a click or tap on the stage to a page step. Edges: the
 * outer thirds turn pages and the middle toggles the chrome. L-shape: the
 * forward zone also covers the bottom strip. Forward is left for RTL.
 */
export function zoneStep(x: number, y: number, width: number, height: number, dir: Direction, lshape = false): Step {
  if (width <= 0) return null;
  const fx = x / width;
  const fy = height > 0 ? y / height : 0.5;
  const forwardLeft = dir === 'rtl';
  if (lshape && fy >= 2 / 3) return 'next';
  if (fx < 1 / 3) return forwardLeft ? 'next' : 'prev';
  if (fx > 2 / 3) return forwardLeft ? 'prev' : 'next';
  return 'menu';
}

/** keyStep maps a key to a step; arrows follow the reading direction. */
export function keyStep(key: string, dir: Direction, mode: Exclude<Mode, 'auto'>): Step {
  const forwardLeft = dir === 'rtl';
  switch (key) {
    case 'ArrowLeft':
      return mode === 'webtoon' ? null : forwardLeft ? 'next' : 'prev';
    case 'ArrowRight':
      return mode === 'webtoon' ? null : forwardLeft ? 'prev' : 'next';
    case 'PageDown':
      return mode === 'webtoon' ? null : 'next';
    case 'PageUp':
      return mode === 'webtoon' ? null : 'prev';
    case ' ':
    case 'Spacebar':
      return mode === 'webtoon' ? null : 'next';
    case 'Home':
      return 'first';
    case 'End':
      return 'last';
  }
  return null;
}

/** viewOrder orders a spread's pages left to right on screen. */
export function viewOrder(view: number[], dir: Direction): number[] {
  return dir === 'rtl' ? [...view].reverse() : view;
}

/** progressText is "14–15 / 34" (or "7 / 34" for one page). */
export function progressText(view: number[], total: number): string {
  if (view.length === 0) return `0 / ${total}`;
  const first = Math.min(...view);
  const last = Math.max(...view);
  return first === last ? `${first} / ${total}` : `${first}–${last} / ${total}`;
}

/** progressFraction is how far into the chapter the view's last page is (0–1). */
export function progressFraction(view: number[], total: number): number {
  if (total <= 0 || view.length === 0) return 0;
  return Math.min(1, Math.max(...view) / total);
}
