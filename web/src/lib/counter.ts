/**
 * Live character counters for ui.Textarea. The server renders "n / max" once
 * (correct without JavaScript); this keeps every counter in step while the
 * user types, pastes or the browser restores a draft. One delegated listener
 * covers content swapped in later by HTMX.
 */

/** characters counts user-perceived code points, like the server (runes). */
export function characters(value: string): number {
  return Array.from(value).length;
}

/** counterFor returns the counter element that belongs to a textarea. */
export function counterFor(field: Element): HTMLElement | null {
  if (!(field instanceof HTMLTextAreaElement) || !field.id) return null;
  const doc = field.ownerDocument;
  const byID = doc.querySelector<HTMLElement>(`[data-count-for="${CSS.escape(field.id)}"]`);
  if (byID) return byID;
  const next = field.nextElementSibling;
  return next instanceof HTMLElement && next.classList.contains('mv-field__count') ? next : null;
}

/** syncCounter writes "n / max" for one textarea. */
export function syncCounter(field: HTMLTextAreaElement): void {
  const counter = counterFor(field);
  if (!counter) return;
  const max = Number(field.getAttribute('maxlength') || counter.dataset.max || 0);
  if (!(max > 0)) return;
  const n = characters(field.value);
  counter.textContent = `${n} / ${max}`;
  counter.classList.toggle('is-full', n >= max);
}

/** syncCounters refreshes every counter under root (after load or a swap). */
export function syncCounters(root: ParentNode = document): void {
  root.querySelectorAll<HTMLTextAreaElement>('textarea[maxlength]').forEach((field) => syncCounter(field));
}

/** installCounters wires the delegated listeners once and returns a remover. */
export function installCounters(doc: Document = document): () => void {
  const onInput = (e: Event) => {
    if (e.target instanceof HTMLTextAreaElement) syncCounter(e.target);
  };
  const onSwap = (e: Event) => {
    const target = (e as CustomEvent).detail?.target;
    syncCounters(target instanceof Element ? target : doc);
  };
  const onShow = () => syncCounters(doc);
  doc.addEventListener('input', onInput);
  doc.addEventListener('htmx:afterSettle', onSwap);
  doc.defaultView?.addEventListener('pageshow', onShow);
  syncCounters(doc);
  return () => {
    doc.removeEventListener('input', onInput);
    doc.removeEventListener('htmx:afterSettle', onSwap);
    doc.defaultView?.removeEventListener('pageshow', onShow);
  };
}
