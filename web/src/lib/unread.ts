/** Keep the shared bell's visual and accessible notification counts in sync. */
export function updateUnread(count: number, doc: Document = document): void {
  const n = Number.isFinite(count) ? Math.max(0, Math.floor(count)) : 0;
  doc.querySelectorAll<HTMLElement>('[data-unread-dot]').forEach(dot => { dot.hidden = n === 0; });
  doc.querySelectorAll<HTMLElement>('[data-unread-base]').forEach(bell => {
    const text = (bell.dataset.unreadLabel ?? '').replaceAll('{count}', String(n));
    bell.setAttribute('aria-label', `${bell.dataset.unreadBase ?? ''}${n > 0 ? ` · ${text}` : ''}`);
    const label = bell.querySelector<HTMLElement>('[data-unread-count]');
    if (label) { label.textContent = text; label.hidden = n === 0; }
  });
}
