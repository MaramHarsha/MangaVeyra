import { beforeEach, describe, expect, it } from 'vitest';
import { normalizeToast, showToast } from './toast';

describe('normalizeToast', () => {
  it('accepts strings and objects', () => {
    expect(normalizeToast('Saved')).toEqual({ title: 'Saved', kind: 'info' });
    expect(normalizeToast({ kind: 'success', title: 'Chapter 8 published', body: '34 pages', href: '/s/x' })).toEqual({
      kind: 'success',
      title: 'Chapter 8 published',
      body: '34 pages',
      href: '/s/x',
    });
  });
  it('drops unsafe links and unknown kinds', () => {
    const t = normalizeToast({ kind: 'party', title: 'x', href: 'https://evil.example' });
    expect(t).toEqual({ kind: 'info', title: 'x' });
    expect(normalizeToast({ title: 'y', href: '//evil.example' })?.href).toBeUndefined();
  });
  it('rejects empty input', () => {
    expect(normalizeToast(null)).toBeNull();
    expect(normalizeToast({})).toBeNull();
    expect(normalizeToast('')).toBeNull();
  });
});

describe('showToast', () => {
  beforeEach(() => {
    document.body.innerHTML = '';
  });
  it('renders a speech-bubble toast with text content only', () => {
    const el = showToast({ kind: 'error', title: '<b>Panel 7.3 failed</b>', body: 'Moved to local.', timeout: 0 });
    expect(el.className).toBe('mv-toast mv-toast--error');
    expect(el.getAttribute('role')).toBe('alert');
    expect(el.querySelector('.mv-toast__title')?.textContent).toBe('<b>Panel 7.3 failed</b>');
    expect(el.querySelector('b')).toBeNull();
    expect(document.getElementById('mv-toasts')?.children.length).toBe(1);
  });
  it('keeps at most four toasts', () => {
    for (let i = 0; i < 6; i++) showToast({ title: `t${i}`, timeout: 0 });
    expect(document.getElementById('mv-toasts')?.children.length).toBe(4);
  });
  it('closes on the close button', () => {
    const el = showToast({ title: 'bye', timeout: 0 });
    (el.querySelector('.mv-toast__close') as HTMLButtonElement).click();
    expect(el.isConnected).toBe(false);
  });
});
