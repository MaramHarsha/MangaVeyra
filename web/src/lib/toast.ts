/**
 * Speech-bubble toasts (design 04 §12). Toasts come from HX-Trigger response
 * headers ({"mv:toast": {...}}), from realtime notifications and from code.
 */
export type ToastKind = 'success' | 'error' | 'warning' | 'info';

export interface ToastOptions {
  kind?: ToastKind;
  title: string;
  body?: string;
  /** Optional action link. */
  href?: string;
  action?: string;
  /** Auto-dismiss after ms (0 keeps it until closed). Errors stay by default. */
  timeout?: number;
}

const ICONS: Record<ToastKind, string> = {
  success: 'ti-circle-check',
  error: 'ti-alert-triangle',
  warning: 'ti-alert-circle',
  info: 'ti-info-circle',
};

/** toastContainer returns (creating when missing) the live region holding toasts. */
export function toastContainer(doc: Document = document): HTMLElement {
  let el = doc.getElementById('mv-toasts');
  if (!el) {
    el = doc.createElement('div');
    el.id = 'mv-toasts';
    el.className = 'mv-toasts';
    el.setAttribute('role', 'status');
    el.setAttribute('aria-live', 'polite');
    doc.body.appendChild(el);
  }
  return el;
}

/** normalizeToast accepts a string or a loose object (e.g. parsed from a header) and returns options. */
export function normalizeToast(input: unknown): ToastOptions | null {
  if (typeof input === 'string') return input ? { title: input, kind: 'info' } : null;
  if (!input || typeof input !== 'object') return null;
  const o = input as Record<string, unknown>;
  const title = typeof o.title === 'string' ? o.title : typeof o.message === 'string' ? o.message : '';
  if (!title) return null;
  const kind = (['success', 'error', 'warning', 'info'] as const).includes(o.kind as ToastKind) ? (o.kind as ToastKind) : 'info';
  const out: ToastOptions = { title, kind };
  if (typeof o.body === 'string') out.body = o.body;
  if (typeof o.href === 'string' && o.href.startsWith('/') && !o.href.startsWith('//')) out.href = o.href;
  if (typeof o.action === 'string') out.action = o.action;
  if (typeof o.timeout === 'number') out.timeout = o.timeout;
  return out;
}

/** showToast renders a toast and returns its element. Text is always set with textContent. */
export function showToast(opts: ToastOptions, doc: Document = document): HTMLElement {
  const kind = opts.kind ?? 'info';
  const el = doc.createElement('div');
  el.className = `mv-toast mv-toast--${kind}`;
  el.setAttribute('role', kind === 'error' ? 'alert' : 'status');

  const icon = doc.createElement('i');
  icon.className = `ti ${ICONS[kind]}`;
  icon.setAttribute('aria-hidden', 'true');
  el.appendChild(icon);

  const text = doc.createElement('div');
  text.className = 'mv-toast__text';
  const title = doc.createElement('strong');
  title.className = 'mv-toast__title';
  title.textContent = opts.title;
  text.appendChild(title);
  if (opts.body) {
    const body = doc.createElement('span');
    body.className = 'mv-toast__body';
    body.textContent = opts.body;
    text.appendChild(body);
  }
  el.appendChild(text);

  if (opts.href) {
    const a = doc.createElement('a');
    a.className = 'mv-toast__action';
    a.href = opts.href;
    a.textContent = opts.action || 'View';
    el.appendChild(a);
  }
  const close = doc.createElement('button');
  close.type = 'button';
  close.className = 'mv-close mv-toast__close';
  close.setAttribute('aria-label', doc.documentElement.dataset.closeLabel || 'Close');
  close.innerHTML = '<i class="ti ti-x" aria-hidden="true"></i>';
  close.addEventListener('click', () => el.remove());
  el.appendChild(close);

  const box = toastContainer(doc);
  box.appendChild(el);
  while (box.children.length > 4) box.firstElementChild?.remove();

  const timeout = opts.timeout ?? (kind === 'error' ? 0 : 6000);
  if (timeout > 0) setTimeout(() => el.remove(), timeout);
  return el;
}
