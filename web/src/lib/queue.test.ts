// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { installQueueRefresh } from './queue';

const markup = (text = 'Working', active = true) => `<div id="queue-live" data-queue-refresh="/queue/status" data-queue-active="${active}" data-queue-admin="true"><span>${text}</span><details><summary>Actions</summary><button>Cancel job</button></details></div>`;
const response = (html: string) => new Response(html, { status: 200, headers: { 'content-type': 'text/html' } });

describe('queue snapshot updates', () => {
  let cleanup: (() => void) | undefined;
  beforeEach(() => {
    vi.useFakeTimers();
    document.body.innerHTML = markup() + '<div data-queue-error hidden>Refresh failed</div>';
    Object.defineProperty(document, 'hidden', { value: false, configurable: true });
  });
  afterEach(() => { cleanup?.(); cleanup = undefined; vi.useRealTimers(); document.body.innerHTML = ''; });

  it('uses only the local read endpoint, refreshes status and stops after completion', async () => {
    const request = vi.fn<typeof fetch>().mockResolvedValue(response(markup('Completed', false)));
    const rt = { subscribe: vi.fn(), unsubscribe: vi.fn() };
    cleanup = installQueueRefresh(document, window, request, rt);
    expect(rt.subscribe).toHaveBeenCalledWith('admin:queue');
    await vi.advanceTimersByTimeAsync(3000);
    expect(request).toHaveBeenCalledTimes(1);
    expect(request.mock.calls[0][0]).toBe(new URL('/queue/status', location.href).href);
    expect(request.mock.calls[0][1]).toMatchObject({ cache: 'no-store', credentials: 'same-origin', redirect: 'error' });
    expect(document.getElementById('queue-live')?.textContent).toContain('Completed');
    await vi.advanceTimersByTimeAsync(9000);
    expect(request).toHaveBeenCalledTimes(1);
    cleanup(); cleanup = undefined;
    expect(rt.unsubscribe).toHaveBeenCalledWith('admin:queue');
  });

  it('keeps an open action menu and keyboard focus intact while progress changes', async () => {
    const request = vi.fn<typeof fetch>().mockResolvedValue(response(markup('New status')));
    cleanup = installQueueRefresh(document, window, request);
    const details = document.querySelector('details')!;
    details.open = true;
    await vi.advanceTimersByTimeAsync(3000);
    expect(request).not.toHaveBeenCalled();
    details.open = false;
    const summary = document.querySelector('summary')!;
    summary.focus();
    await vi.advanceTimersByTimeAsync(3000);
    expect(request).not.toHaveBeenCalled();
    summary.blur();
    await vi.advanceTimersByTimeAsync(3000);
    expect(request).toHaveBeenCalledTimes(1);
  });

  it('discards an in-flight snapshot when a user starts interacting', async () => {
    let resolve!: (r: Response) => void;
    const request = vi.fn<typeof fetch>().mockImplementation(() => new Promise(r => { resolve = r; }));
    cleanup = installQueueRefresh(document, window, request);
    await vi.advanceTimersByTimeAsync(3000);
    document.querySelector('details')!.open = true;
    resolve(response(markup('Should not replace the menu')));
    await vi.advanceTimersByTimeAsync(0);
    expect(document.querySelector('details')!.open).toBe(true);
    expect(document.getElementById('queue-live')?.textContent).toContain('Working');
  });

  it('keeps the previous snapshot on failure or session expiry and aborts on navigation', async () => {
    const request = vi.fn<typeof fetch>().mockResolvedValue(new Response('<h1>Log in</h1>', { headers: { 'content-type': 'text/html' } }));
    cleanup = installQueueRefresh(document, window, request);
    await vi.advanceTimersByTimeAsync(3000);
    expect(document.querySelector<HTMLElement>('[data-queue-error]')!.hidden).toBe(false);
    expect(document.getElementById('queue-live')?.textContent).toContain('Working');
    let signal: AbortSignal | undefined;
    request.mockImplementation((_url, options) => new Promise((_resolve, reject) => {
      signal = options?.signal ?? undefined;
      signal?.addEventListener('abort', () => reject(new Error('aborted')));
    }));
    await vi.advanceTimersByTimeAsync(5000);
    document.dispatchEvent(new CustomEvent('htmx:beforeSwap', { detail: { target: document.body } }));
    expect(signal?.aborted).toBe(true);
    document.body.innerHTML = '<h1>Another screen</h1>';
    document.dispatchEvent(new CustomEvent('htmx:afterSettle'));
    await vi.advanceTimersByTimeAsync(10000);
    expect(request).toHaveBeenCalledTimes(2);
    expect(document.body.textContent).toBe('Another screen');
  });

  it('refreshes an idle queue after a job event without accepting a remote endpoint', async () => {
    document.body.innerHTML = markup('Idle', false);
    const request = vi.fn<typeof fetch>().mockResolvedValue(response(markup('Queued')));
    cleanup = installQueueRefresh(document, window, request);
    await vi.advanceTimersByTimeAsync(5000);
    expect(request).not.toHaveBeenCalled();
    document.dispatchEvent(new CustomEvent('mv:ws', { detail: { type: 'job.queued' } }));
    await vi.advanceTimersByTimeAsync(250);
    expect(request).toHaveBeenCalledTimes(1);
    document.querySelector<HTMLElement>('#queue-live')!.dataset.queueRefresh = 'https://example.invalid/queue/status';
    await vi.advanceTimersByTimeAsync(3000);
    expect(request).toHaveBeenCalledTimes(1);
  });
});
