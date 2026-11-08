import { describe, expect, it } from 'vitest';
import { loadLocal, localRow, merge, saveLocal, sanitize, serverRow } from './prefs';
import type { Prefs } from './types';

const server: Prefs = { mode: 'auto', direction: 'rtl', background: 'ink', zoom: 1, tap_zones: true, tap_l_shape: false, panel_comments: false, read_aloud: false, hide_chrome: false, reduced_motion: false, stored: false };

class MemoryStorage {
  data = new Map<string, string>();
  getItem(k: string) {
    return this.data.get(k) ?? null;
  }
  setItem(k: string, v: string) {
    this.data.set(k, v);
  }
}

describe('local reading settings', () => {
  it('round-trips per series and ignores malformed values', () => {
    const s = new MemoryStorage();
    expect(saveLocal(s, 'series-a', { mode: 'guided', brightness: 0.7, tap_zones: false })).toBe(true);
    expect(loadLocal(s, 'series-a')).toEqual({ mode: 'guided', brightness: 0.7, tap_zones: false });
    expect(loadLocal(s, 'series-b')).toEqual({});
    s.setItem('mv:reader:series-c', '{not json');
    expect(loadLocal(s, 'series-c')).toEqual({});
    expect(sanitize({ mode: 'sideways', zoom: 99, brightness: -1, read_aloud: 'yes' })).toEqual({ zoom: 3, brightness: 0.4 });
  });
  it('survives storage that throws (private windows, blocked site data)', () => {
    const throwing = { getItem: () => { throw new Error('denied'); }, setItem: () => { throw new Error('denied'); } };
    expect(loadLocal(throwing, 'x')).toEqual({});
    expect(saveLocal(throwing, 'x', { mode: 'paged' })).toBe(false);
    expect(loadLocal(null, 'x')).toEqual({});
  });
});

describe('merge', () => {
  it('anonymous readers take everything from the device', () => {
    const m = merge(server, { mode: 'spread', direction: 'ltr', background: 'paper', zoom: 1.5, brightness: 0.8 }, false, false);
    expect(m).toMatchObject({ mode: 'spread', direction: 'ltr', background: 'paper', zoom: 1.5, brightness: 0.8 });
  });
  it('members keep stored server settings and device-only toggles', () => {
    const stored = { ...server, mode: 'guided' as const, direction: 'ltr' as const, stored: true };
    const m = merge(stored, { mode: 'spread', direction: 'rtl', reduced_motion: true, read_aloud: true }, true, false);
    expect(m).toMatchObject({ mode: 'guided', direction: 'ltr', reduced_motion: true, read_aloud: true });
  });
  it('a member without a stored row still sees the device choice', () => {
    expect(merge(server, { mode: 'paged' }, true, false).mode).toBe('paged');
  });
  it('webtoon series always read vertically', () => {
    const m = merge({ ...server, direction: 'vertical', mode: 'webtoon' }, { mode: 'spread', direction: 'rtl' }, false, true);
    expect(m.direction).toBe('vertical');
    expect(m.mode).toBe('webtoon');
    expect(merge({ ...server, direction: 'vertical', mode: 'webtoon' }, { mode: 'guided' }, false, true).mode).toBe('guided');
  });
  it('splits rows for the server and the device', () => {
    const m = merge(server, {}, true, false);
    expect(serverRow({ ...m, zoom: 9 }, 'spread')).toEqual({ mode: 'spread', direction: 'rtl', background: 'ink', zoom: 3 });
    expect(localRow(m, true)).not.toHaveProperty('mode');
    expect(localRow(m, false)).toHaveProperty('mode', 'auto');
  });
});
