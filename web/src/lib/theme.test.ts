import { describe, expect, it } from 'vitest';
import { applyPrefs, nearestTextSize, nextTheme, resolvedTheme } from './theme';

describe('applyPrefs', () => {
  it('writes the attributes the CSS reads', () => {
    const el = document.createElement('html');
    applyPrefs({ theme: 'light', motion: 'on', textSize: 110, textures: false, strongFocus: true, underlineLinks: true, dyslexiaFont: true }, el);
    expect(el.dataset.theme).toBe('light');
    expect(el.getAttribute('data-motion')).toBe('reduce');
    expect(el.getAttribute('data-text-size')).toBe('112');
    expect(el.getAttribute('data-textures')).toBe('off');
    expect(el.getAttribute('data-focus')).toBe('strong');
    expect(el.getAttribute('data-links')).toBe('underline');
    expect(el.getAttribute('data-font')).toBe('dyslexic');
    applyPrefs({ motion: 'system', textSize: 100, textures: true }, el);
    expect(el.hasAttribute('data-motion')).toBe(false);
    expect(el.hasAttribute('data-text-size')).toBe(false);
    expect(el.hasAttribute('data-textures')).toBe(false);
    applyPrefs({ motion: 'off' }, el);
    expect(el.getAttribute('data-motion')).toBe('full');
  });
  it('snaps text sizes', () => {
    expect(nearestTextSize(88)).toBe(87);
    expect(nearestTextSize(140)).toBe(150);
  });
  it('resolves system themes and toggles', () => {
    const el = document.createElement('html');
    el.dataset.theme = 'system';
    expect(resolvedTheme(el, { matches: true })).toBe('light');
    expect(resolvedTheme(el, { matches: false })).toBe('dark');
    expect(nextTheme('dark')).toBe('light');
  });
});
