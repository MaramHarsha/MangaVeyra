import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { autosaveForm, installAutosave, saveForm } from './models';

let cleanup: (() => void) | undefined;

beforeEach(() => {
  document.body.innerHTML = `
    <div><label><input type="radio" name="mode" value="balanced" form="routing-settings" checked></label>
    <label><input type="radio" name="mode" value="best" form="routing-settings"></label></div>
    <form id="routing-settings" method="post" action="/models/routing" data-autosave data-saved-label="Routing saved." data-error-label="Routing was not saved.">
      <input type="checkbox" role="switch" name="licence_guard" value="true" checked>
      <input type="number" name="limit" value="20">
      <input type="checkbox" role="switch" name="limit_on" value="true" checked>
    </form>
    <form id="other" action="/elsewhere"><input name="q" value="x"></form>`;
});
afterEach(() => {
  cleanup?.();
  document.getElementById('mv-toasts')?.remove();
});

describe('G11 routing autosave', () => {
  it('only auto-saves forms that opt in, including controls that name them with form=', () => {
    const best = document.querySelector<HTMLInputElement>('input[value=best]')!;
    expect(autosaveForm(best)?.id).toBe('routing-settings');
    expect(autosaveForm(document.querySelector('#other input'))).toBeNull();
    expect(autosaveForm(document.body)).toBeNull();
  });

  it('posts every associated control as a urlencoded form', async () => {
    const fetcher = vi.fn(async () => ({ ok: true }));
    const form = document.getElementById('routing-settings') as HTMLFormElement;
    await expect(saveForm(form, fetcher)).resolves.toBe(true);
    const [url, init] = fetcher.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toMatch(/\/models\/routing$/);
    expect(init.method).toBe('POST');
    const body = new URLSearchParams(String(init.body));
    expect(body.get('mode')).toBe('balanced');
    expect(body.get('licence_guard')).toBe('true');
    expect(body.get('limit')).toBe('20');
    expect(body.get('limit_on')).toBe('true');
  });

  it('saves on change and toasts the outcome; failures are reported, not hidden', async () => {
    const fetcher = vi.fn(async () => ({ ok: true }));
    cleanup = installAutosave(document, fetcher);
    const guard = document.querySelector<HTMLInputElement>('[name=licence_guard]')!;
    guard.checked = false;
    guard.dispatchEvent(new Event('change', { bubbles: true }));
    await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1));
    const body = new URLSearchParams(String((fetcher.mock.calls[0] as unknown as [string, RequestInit])[1].body));
    expect(body.has('licence_guard')).toBe(false);
    await vi.waitFor(() => expect(document.getElementById('mv-toasts')?.textContent).toContain('Routing saved.'));

    fetcher.mockImplementationOnce(async () => ({ ok: false }));
    const best = document.querySelector<HTMLInputElement>('input[value=best]')!;
    best.checked = true;
    best.dispatchEvent(new Event('change', { bubbles: true }));
    await vi.waitFor(() => expect(document.getElementById('mv-toasts')?.textContent).toContain('Routing was not saved.'));

    document.querySelector<HTMLInputElement>('#other input')!.dispatchEvent(new Event('change', { bubbles: true }));
    expect(fetcher).toHaveBeenCalledTimes(2);
  });
});
