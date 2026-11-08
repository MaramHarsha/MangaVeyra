import { describe, expect, it } from 'vitest';
import { effectiveMode, keyStep, progressFraction, progressText, singles, spreads, viewIndexOf, viewOrder, zoneStep } from './direction';

const page = (number: number, width = 520, height = 735) => ({ number, width, height });
const book = (n: number) => Array.from({ length: n }, (_, i) => page(i + 1));

describe('spreads', () => {
  it('keeps the first page alone and pairs the rest (E3 shows pages 14–15)', () => {
    const v = spreads(book(34));
    expect(v[0]).toEqual([1]);
    expect(v[1]).toEqual([2, 3]);
    expect(v).toContainEqual([14, 15]);
    expect(v[v.length - 1]).toEqual([34]);
    expect(progressText(v[viewIndexOf(v, 15)], 34)).toBe('14–15 / 34');
  });
  it('lets a wide page stand alone and does not pair across it', () => {
    const pages = [page(1), page(2), page(3, 1040, 735), page(4), page(5)];
    expect(spreads(pages)).toEqual([[1], [2], [3], [4, 5]]);
  });
  it('handles empty and single-page chapters', () => {
    expect(spreads([])).toEqual([]);
    expect(spreads([page(1)])).toEqual([[1]]);
    expect(singles(book(3))).toEqual([[1], [2], [3]]);
  });
  it('finds the view of a page and falls back to the first', () => {
    const v = spreads(book(6));
    expect(viewIndexOf(v, 5)).toBe(2);
    expect(viewIndexOf(v, 99)).toBe(0);
  });
});

describe('direction', () => {
  it('orders a spread right-to-left for manga and left-to-right for comics', () => {
    expect(viewOrder([14, 15], 'rtl')).toEqual([15, 14]);
    expect(viewOrder([14, 15], 'ltr')).toEqual([14, 15]);
  });
  it('maps tap zones to the reading direction', () => {
    // 1440 wide stage: left third, middle, right third.
    expect(zoneStep(100, 400, 1440, 900, 'rtl')).toBe('next');
    expect(zoneStep(1300, 400, 1440, 900, 'rtl')).toBe('prev');
    expect(zoneStep(100, 400, 1440, 900, 'ltr')).toBe('prev');
    expect(zoneStep(1300, 400, 1440, 900, 'ltr')).toBe('next');
    expect(zoneStep(720, 400, 1440, 900, 'rtl')).toBe('menu');
    expect(zoneStep(10, 10, 0, 0, 'rtl')).toBeNull();
  });
  it('turns the bottom strip forward with the L-shape layout', () => {
    expect(zoneStep(1300, 850, 1440, 900, 'rtl', true)).toBe('next');
    expect(zoneStep(1300, 400, 1440, 900, 'rtl', true)).toBe('prev');
    expect(zoneStep(720, 850, 1440, 900, 'ltr', true)).toBe('next');
  });
  it('follows the reading direction with the arrow keys', () => {
    expect(keyStep('ArrowLeft', 'rtl', 'spread')).toBe('next');
    expect(keyStep('ArrowRight', 'rtl', 'spread')).toBe('prev');
    expect(keyStep('ArrowLeft', 'ltr', 'paged')).toBe('prev');
    expect(keyStep('ArrowRight', 'ltr', 'paged')).toBe('next');
    expect(keyStep('ArrowLeft', 'rtl', 'guided')).toBe('next');
    expect(keyStep(' ', 'ltr', 'paged')).toBe('next');
    expect(keyStep('PageUp', 'rtl', 'paged')).toBe('prev');
    expect(keyStep('Home', 'rtl', 'paged')).toBe('first');
    expect(keyStep('End', 'rtl', 'webtoon')).toBe('last');
  });
  it('leaves arrows and space to native scrolling in webtoon mode', () => {
    expect(keyStep('ArrowLeft', 'vertical', 'webtoon')).toBeNull();
    expect(keyStep(' ', 'vertical', 'webtoon')).toBeNull();
    expect(keyStep('a', 'rtl', 'paged')).toBeNull();
  });
});

describe('modes and progress', () => {
  it('resolves auto to Spread on wide screens and Single on narrow ones', () => {
    expect(effectiveMode('auto', 1440, false)).toBe('spread');
    expect(effectiveMode('auto', 390, false)).toBe('paged');
    expect(effectiveMode('paged', 1440, false)).toBe('paged');
    expect(effectiveMode('spread', 390, true)).toBe('webtoon');
    expect(effectiveMode('guided', 390, true)).toBe('guided');
  });
  it('reports progress', () => {
    expect(progressText([7], 34)).toBe('7 / 34');
    expect(progressFraction([14, 15], 34)).toBeCloseTo(15 / 34);
    expect(progressFraction([], 34)).toBe(0);
  });
});
