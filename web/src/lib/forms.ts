/** Shared progressive enhancement for server forms. Drafts stay in memory. */
type Pending = { buttons: Array<{ button: HTMLButtonElement | HTMLInputElement; disabled: boolean }> };

function fingerprint(form: HTMLFormElement): string {
  return JSON.stringify(Array.from(new FormData(form), ([key, value]) => [key,
    typeof value === 'string' ? value : [value.name, value.size, value.lastModified],
  ]));
}

/** One owner for duplicate submissions, busy state and guarded navigation. */
export function installForms(doc: Document = document): () => void {
  const win = doc.defaultView!;
  const original = new WeakMap<HTMLFormElement, string>();
  const pending = new Map<HTMLFormElement, Pending>();
  const guarded = new Set<HTMLFormElement>();
  const richDrafts = new Set<HTMLElement>();
  let destination: string | null = null;
  let continuation: (() => void) | null = null;
  let trigger: HTMLElement | null = null;
  let leaving = false;
  const dialog = () => doc.getElementById('mv-unsaved') as HTMLDialogElement | null;
  const enhance = () => {
    doc.querySelectorAll<HTMLFormElement>('form[data-guard-unsaved]').forEach(form => {
      if (!original.has(form)) original.set(form, fingerprint(form));
      guarded.add(form);
    });
  };
  const dirty = () => Array.from(richDrafts).some(owner => owner.isConnected) || Array.from(guarded).some(form =>
    form.isConnected && !pending.has(form) && fingerprint(form) !== original.get(form));
  const richChanged = (event: Event) => {
    const detail = (event as CustomEvent).detail;
    if (!(detail?.owner instanceof HTMLElement)) return;
    if (detail.dirty === true) richDrafts.add(detail.owner);
    else richDrafts.delete(detail.owner);
  };
  const guardNavigation = (event: Event) => {
    const detail = (event as CustomEvent).detail;
    if (typeof detail?.proceed !== 'function') return;
    if (!dirty()) { detail.proceed(); return; }
    const modal = dialog();
    if (!modal) return;
    continuation = detail.proceed;
    trigger = detail.trigger instanceof HTMLElement ? detail.trigger : doc.activeElement as HTMLElement;
    if (!modal.open) modal.showModal();
    modal.querySelector<HTMLElement>('[data-keep-editing]')?.focus();
  };
  const release = (form: HTMLFormElement, saved = false) => {
    const state = pending.get(form);
    if (!state) return;
    state.buttons.forEach(({ button, disabled }) => {
      button.disabled = disabled;
      button.removeAttribute('aria-disabled');
      button.removeAttribute('aria-busy');
    });
    form.removeAttribute('aria-busy');
    pending.delete(form);
    if (saved && guarded.has(form)) original.set(form, fingerprint(form));
  };
  const begin = (form: HTMLFormElement): boolean => {
    if (pending.has(form)) return false;
    const buttons = Array.from(form.querySelectorAll<HTMLButtonElement | HTMLInputElement>('button[type="submit"], button:not([type]), input[type="submit"]'))
      .map(button => ({ button, disabled: button.disabled }));
    pending.set(form, { buttons });
    form.setAttribute('aria-busy', 'true');
    buttons.forEach(({ button }) => {
      button.setAttribute('aria-busy', 'true');
      button.setAttribute('aria-disabled', 'true');
    });
    // Keep the submitter name/value in native and HTMX serialization.
    win.setTimeout(() => {
      if (pending.has(form)) buttons.forEach(({ button }) => { button.disabled = true; });
    }, 0);
    return true;
  };
  const submit = (event: Event) => {
    const form = event.target;
    if (!(form instanceof HTMLFormElement) || ['dialog', 'get'].includes(form.method.toLowerCase()) || event.defaultPrevented) return;
    if (!begin(form)) event.preventDefault();
  };
  const requestForm = (event: Event): HTMLFormElement | null => {
    const detail = (event as CustomEvent).detail;
    const elt = detail?.elt ?? detail?.requestConfig?.elt;
    return elt instanceof HTMLFormElement ? elt : elt instanceof Element ? elt.closest('form') : null;
  };
  const beforeRequest = (event: Event) => {
    const form = requestForm(event);
    if (form && form.method.toLowerCase() !== 'get' && !begin(form)) event.preventDefault();
  };
  const afterRequest = (event: Event) => {
    const detail = (event as CustomEvent).detail;
    const form = requestForm(event);
    if (form) release(form, detail?.successful === true && detail?.xhr?.status < 300);
  };
  const click = (event: MouseEvent) => {
    const target = event.target instanceof Element ? event.target : null;
    if (target?.closest('[data-discard-changes]')) {
      if (continuation) {
        const proceed = continuation;
        dialog()?.close();
        proceed();
        return;
      }
      if (!destination) return;
      leaving = true;
      const href = destination;
      dialog()?.close();
      win.location.assign(href);
      return;
    }
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const link = target?.closest<HTMLAnchorElement>('a[href]');
    if (!link || link.target && link.target !== '_self' || link.hasAttribute('download')) return;
    const url = new URL(link.href, win.location.href);
    if (!['http:', 'https:'].includes(url.protocol) || url.pathname === win.location.pathname && url.search === win.location.search && !!url.hash || !dirty()) return;
    const modal = dialog();
    if (!modal) return;
    event.preventDefault();
    destination = url.href;
    continuation = null;
    trigger = link;
    if (!modal.open) modal.showModal();
    modal.querySelector<HTMLElement>('[data-keep-editing]')?.focus();
  };
  const closed = () => {
    if (!leaving) trigger?.focus();
    destination = null;
    continuation = null;
    trigger = null;
  };
  const beforeUnload = (event: BeforeUnloadEvent) => {
    if (!leaving && dirty()) {
      event.preventDefault();
      event.returnValue = '';
    }
  };
  const pageshow = () => { leaving = false; pending.forEach((_, form) => release(form)); enhance(); };
  doc.addEventListener('submit', submit);
  doc.addEventListener('click', click);
  doc.addEventListener('htmx:beforeRequest', beforeRequest);
  doc.addEventListener('htmx:afterRequest', afterRequest);
  doc.addEventListener('htmx:afterSettle', enhance);
  doc.addEventListener('mv:unsaved', richChanged);
  doc.addEventListener('mv:guard-navigation', guardNavigation);
  dialog()?.addEventListener('close', closed);
  win.addEventListener('beforeunload', beforeUnload);
  win.addEventListener('pageshow', pageshow);
  enhance();
  return () => {
    doc.removeEventListener('submit', submit);
    doc.removeEventListener('click', click);
    doc.removeEventListener('htmx:beforeRequest', beforeRequest);
    doc.removeEventListener('htmx:afterRequest', afterRequest);
    doc.removeEventListener('htmx:afterSettle', enhance);
    doc.removeEventListener('mv:unsaved', richChanged);
    doc.removeEventListener('mv:guard-navigation', guardNavigation);
    dialog()?.removeEventListener('close', closed);
    win.removeEventListener('beforeunload', beforeUnload);
    win.removeEventListener('pageshow', pageshow);
    pending.forEach((_, form) => release(form));
  };
}
