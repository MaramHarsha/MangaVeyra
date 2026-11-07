import type { Realtime, WSMessage } from './realtime';

/** Refresh the queue's read-only snapshot without interrupting a menu, form or
 * keyboard interaction. Ordinary links/forms remain complete without this. */
export function installQueueRefresh(
  doc: Document = document,
  win: Window = window,
  request: typeof fetch = fetch,
  realtime: Pick<Realtime, 'subscribe' | 'unsubscribe'> | null = null,
): () => void {
  let timer: ReturnType<typeof setTimeout> | null = null;
  let pending: AbortController | null = null;
  let disposed = false;
  let suspended = false;
  let admin = false;
  let requested = false;
  const snapshot = () => doc.querySelector<HTMLElement>('#queue-live[data-queue-refresh]');
  const error = (show: boolean) => {
    const notice = doc.querySelector<HTMLElement>('[data-queue-error]');
    if (notice) notice.hidden = !show;
  };
  const stop = () => {
    if (timer !== null) clearTimeout(timer);
    timer = null;
    pending?.abort();
    pending = null;
  };
  const interacting = (node: HTMLElement) => !!(
    node.contains(doc.activeElement) || node.querySelector('details[open], form[aria-busy="true"]') ||
    doc.querySelector('dialog[open]')
  );
  const schedule = (force = false, delay = force ? 250 : 3000) => {
    if (timer !== null) clearTimeout(timer);
    timer = null;
    const node = snapshot();
    if (disposed || suspended || !node) return;
    if (!force && node.dataset.queueActive !== 'true') return;
    timer = setTimeout(() => void refresh(), delay);
  };
  const sync = () => {
    const needsAdmin = snapshot()?.dataset.queueAdmin === 'true';
    if (needsAdmin && !admin) realtime?.subscribe('admin:queue');
    if (!needsAdmin && admin) realtime?.unsubscribe('admin:queue');
    admin = needsAdmin;
    schedule();
  };
  const refresh = async () => {
    timer = null;
    const node = snapshot();
    if (disposed || suspended || !node || pending) return;
    if (doc.hidden || interacting(node)) { schedule(true, 3000); return; }
    const url = new URL(node.dataset.queueRefresh ?? '', win.location.href);
    if (url.origin !== win.location.origin || url.pathname !== '/queue/status') return;
    const here = win.location.href;
    const controller = new AbortController();
    pending = controller;
    requested = false;
    let timedOut = false;
    const deadline = setTimeout(() => { timedOut = true; controller.abort(); }, 10000);
    let failed = false;
    try {
      const response = await request(url.href, {
        credentials: 'same-origin', cache: 'no-store', redirect: 'error',
        headers: { Accept: 'text/html' }, signal: controller.signal,
      });
      if (!response.ok || !response.headers.get('content-type')?.includes('text/html')) throw new Error('queue unavailable');
      const parsed = new DOMParser().parseFromString(await response.text(), 'text/html');
      const next = parsed.querySelector<HTMLElement>('#queue-live[data-queue-refresh]');
      if (!next) throw new Error('queue unavailable');
      if (disposed || suspended || controller.signal.aborted || !node.isConnected || snapshot() !== node ||
          win.location.href !== here || doc.hidden || interacting(node)) return;
      node.replaceWith(doc.importNode(next, true));
      error(false);
    } catch {
      if ((!controller.signal.aborted || timedOut) && node.isConnected && snapshot() === node && win.location.href === here) {
        failed = true;
        error(true);
      }
    } finally {
      clearTimeout(deadline);
      if (pending === controller) pending = null;
      if (!disposed && !suspended) schedule(failed || requested, failed ? 5000 : requested ? 250 : 3000);
    }
  };
  const message = (event: Event) => {
    const m = (event as CustomEvent<WSMessage>).detail;
    if (m && (m.type.startsWith('job.') || m.type.startsWith('worker.'))) { requested = true; schedule(true); }
  };
  const beforeSwap = (event: Event) => {
    const target = (event as CustomEvent).detail?.target as HTMLElement | undefined;
    const node = snapshot();
    if (node && target?.contains(node)) stop();
  };
  const hide = () => { suspended = true; stop(); };
  const show = () => { suspended = false; sync(); };
  doc.addEventListener('mv:ws', message);
  doc.addEventListener('htmx:beforeSwap', beforeSwap);
  doc.addEventListener('htmx:afterSettle', sync);
  win.addEventListener('pagehide', hide);
  win.addEventListener('pageshow', show);
  sync();
  return () => {
    disposed = true;
    stop();
    if (admin) realtime?.unsubscribe('admin:queue');
    doc.removeEventListener('mv:ws', message);
    doc.removeEventListener('htmx:beforeSwap', beforeSwap);
    doc.removeEventListener('htmx:afterSettle', sync);
    win.removeEventListener('pagehide', hide);
    win.removeEventListener('pageshow', show);
  };
}
