import { describe, expect, it } from 'vitest';
import { isTypingTarget, shortcutFor } from './keys';

const ev = (key: string, target: EventTarget | null = document.body, mods: Partial<KeyboardEvent> = {}) =>
  ({ key, target, ctrlKey: false, metaKey: false, altKey: false, ...mods }) as KeyboardEvent;

describe('shortcutFor', () => {
  it('maps / and ? outside text fields', () => {
    expect(shortcutFor(ev('/'))).toBe('search');
    expect(shortcutFor(ev('?'))).toBe('help');
    expect(shortcutFor(ev('a'))).toBeNull();
  });
  it('ignores keys while typing or with modifiers', () => {
    const input = document.createElement('input');
    expect(isTypingTarget(input)).toBe(true);
    expect(shortcutFor(ev('/', input))).toBeNull();
    const box = document.createElement('input');
    box.type = 'checkbox';
    expect(shortcutFor(ev('/', box))).toBe('search');
    expect(shortcutFor(ev('/', document.body, { ctrlKey: true }))).toBeNull();
  });
});
