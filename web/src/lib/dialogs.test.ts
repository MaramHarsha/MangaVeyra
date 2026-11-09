// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest';
import { menuArrowKey, setNavigate, upgradeStaticDialogs } from './dialogs';

describe('upgradeStaticDialogs', () => {
  it('re-opens only opted-in static dialogs, once, and leaves on close', () => {
    document.body.innerHTML = '<dialog class="mv-modal--static" open data-modal-upgrade data-close-href="/studio/p/x/edit"><button>x</button></dialog>' +
      '<dialog class="mv-modal--static" open id="demo"></dialog>';
    const dlg = document.querySelector<HTMLDialogElement>('dialog[data-modal-upgrade]')!;
    const showModal = vi.fn(function (this: HTMLDialogElement) { this.setAttribute('open', ''); });
    dlg.showModal = showModal;
    const go = vi.fn();
    setNavigate(go);
    expect(upgradeStaticDialogs(document)).toBe(1);
    expect(upgradeStaticDialogs(document)).toBe(0);
    expect(showModal).toHaveBeenCalledOnce();
    dlg.dispatchEvent(new Event('close'));
    expect(go).toHaveBeenCalledWith('/studio/p/x/edit');
  });

  it('never navigates to an external close address', () => {
    document.body.innerHTML = '<dialog open data-modal-upgrade data-close-href="//evil.example"></dialog>';
    const dlg = document.querySelector<HTMLDialogElement>('dialog')!;
    dlg.showModal = function (this: HTMLDialogElement) { this.setAttribute('open', ''); };
    const go = vi.fn();
    setNavigate(go);
    upgradeStaticDialogs(document);
    dlg.dispatchEvent(new Event('close'));
    expect(go).not.toHaveBeenCalled();
  });

  it('keeps a dialog open when showModal throws', () => {
    document.body.innerHTML = '<dialog open data-modal-upgrade></dialog>';
    const dlg = document.querySelector<HTMLDialogElement>('dialog')!;
    dlg.showModal = () => { throw new Error('not allowed'); };
    expect(upgradeStaticDialogs(document)).toBe(0);
    expect(dlg.hasAttribute('open')).toBe(true);
  });
});

describe('menuArrowKey', () => {
  const setup = () => {
    document.body.innerHTML = `<details class="mv-dropdown" open><summary>Menu</summary><div role="menu">
      <button role="menuitem" id="a">A</button><button role="menuitem" id="b" disabled>B</button>
      <button role="menuitem" id="c">C</button><a role="menuitem" id="d" aria-disabled="true">D</a>
      <button role="menuitemradio" id="e">E</button></div></details><button id="outside">x</button>`;
  };
  const press = (key: string, from: Element) => {
    const e = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true });
    Object.defineProperty(e, 'target', { value: from });
    return { handled: menuArrowKey(e, document), prevented: e.defaultPrevented };
  };

  it('moves through enabled items and wraps', () => {
    setup();
    const summary = document.querySelector('summary')!;
    expect(press('ArrowDown', summary).handled).toBe(true);
    expect(document.activeElement?.id).toBe('a');
    press('ArrowDown', document.activeElement!);
    expect(document.activeElement?.id).toBe('c');
    press('ArrowDown', document.activeElement!);
    expect(document.activeElement?.id).toBe('e');
    press('ArrowDown', document.activeElement!);
    expect(document.activeElement?.id).toBe('a');
    press('ArrowUp', document.activeElement!);
    expect(document.activeElement?.id).toBe('e');
    press('Home', document.activeElement!);
    expect(document.activeElement?.id).toBe('a');
    expect(press('End', document.activeElement!).prevented).toBe(true);
    expect(document.activeElement?.id).toBe('e');
  });

  it('ignores other keys, closed menus and targets outside a menu', () => {
    setup();
    expect(press('Enter', document.getElementById('a')!).handled).toBe(false);
    expect(press('ArrowDown', document.getElementById('outside')!).handled).toBe(false);
    document.querySelector('details')!.open = false;
    expect(press('ArrowDown', document.querySelector('summary')!).handled).toBe(false);
  });
});
