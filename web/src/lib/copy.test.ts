import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { copyTarget, installCopy } from './copy';

let cleanup: (() => void) | undefined;

beforeEach(() => {
  document.body.innerHTML = `
    <input id="invite-link-1" type="text" readonly value="https://studio.local/signup?invite=abcd-efgh-jkmn">
    <button type="button" data-copy="invite-link-1" data-copied-label="Link copied."><span>Copy link</span></button>
    <button type="button" data-copy="missing">Copy</button>`;
});
afterEach(() => {
  cleanup?.();
  document.getElementById('mv-toasts')?.remove();
});

describe('G5 copy buttons', () => {
  it('finds only the read-only input a button names', () => {
    const [ok, missing] = document.querySelectorAll('[data-copy]');
    expect(copyTarget(ok)?.id).toBe('invite-link-1');
    expect(copyTarget(missing)).toBeNull();
  });

  it('copies the link and confirms with a toast', async () => {
    const write = vi.fn(async () => {});
    cleanup = installCopy(document, write);
    document.querySelector<HTMLElement>('[data-copy="invite-link-1"] span')!.click();
    await vi.waitFor(() => expect(write).toHaveBeenCalledWith('https://studio.local/signup?invite=abcd-efgh-jkmn'));
    await vi.waitFor(() => expect(document.getElementById('mv-toasts')?.textContent).toContain('Link copied.'));
  });

  it('selects the text when the clipboard is not available', async () => {
    cleanup = installCopy(document, async () => {
      throw new Error('denied');
    });
    const input = document.getElementById('invite-link-1') as HTMLInputElement;
    document.querySelector<HTMLElement>('[data-copy="invite-link-1"]')!.click();
    await vi.waitFor(() => expect(document.activeElement).toBe(input));
    expect(input.selectionStart).toBe(0);
    expect(input.selectionEnd).toBe(input.value.length);
  });
});
