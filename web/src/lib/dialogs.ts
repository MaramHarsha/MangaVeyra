/**
 * Keyboard behaviour for server-rendered dialogs and dropdown menus.
 *
 * Static dialogs marked `data-modal-upgrade` (E2 Export) render open so the
 * page works without JavaScript. With JavaScript they are re-opened as real
 * modals, so focus moves inside, Tab stays inside and Escape closes them; on
 * close the browser goes to `data-close-href`, as the dialog's close link does.
 *
 * Dropdown menus (`details.mv-dropdown` with `[role^=menuitem]` items) get
 * ArrowUp/ArrowDown/Home/End focus movement; Tab and Escape are unchanged.
 */

export function upgradeStaticDialogs(doc: Document = document): number {
  let upgraded = 0;
  doc.querySelectorAll<HTMLDialogElement>('dialog[data-modal-upgrade][open]').forEach((dlg) => {
    if (typeof dlg.showModal !== 'function' || dlg.dataset.upgraded) return;
    dlg.dataset.upgraded = '1';
    try {
      dlg.removeAttribute('open');
      dlg.showModal();
      upgraded++;
      const href = dlg.dataset.closeHref || '';
      if (href.startsWith('/') && !href.startsWith('//')) {
        dlg.addEventListener('close', () => navigate(href), { once: true });
      }
    } catch {
      dlg.setAttribute('open', ''); // keep it usable, just not trapped
    }
  });
  return upgraded;
}

let navigate = (href: string) => window.location.assign(href);
/** setNavigate replaces navigation in tests. */
export function setNavigate(fn: (href: string) => void) {
  navigate = fn;
}

const MENU_KEYS = new Set(['ArrowDown', 'ArrowUp', 'Home', 'End']);

/** menuArrowKey moves focus between the enabled items of the open dropdown the event came from. */
export function menuArrowKey(e: KeyboardEvent, doc: Document = document): boolean {
  if (!MENU_KEYS.has(e.key)) return false;
  const target = e.target instanceof Element ? e.target : null;
  const open = target?.closest<HTMLDetailsElement>('details.mv-dropdown[open]');
  if (!open) return false;
  const items = Array.from(open.querySelectorAll<HTMLElement>('[role^="menuitem"]')).filter(
    (el) => !el.hasAttribute('disabled') && el.getAttribute('aria-disabled') !== 'true' && !el.hidden,
  );
  if (items.length === 0) return false;
  const current = items.indexOf(doc.activeElement as HTMLElement);
  let next: number;
  if (e.key === 'ArrowDown') next = current < 0 ? 0 : (current + 1) % items.length;
  else if (e.key === 'ArrowUp') next = current < 0 ? items.length - 1 : (current - 1 + items.length) % items.length;
  else if (e.key === 'End') next = items.length - 1;
  else next = 0;
  e.preventDefault();
  items[next].focus();
  return true;
}
