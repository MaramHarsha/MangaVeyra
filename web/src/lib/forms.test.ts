import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { installForms } from './forms';

let cleanup: (() => void) | undefined;
let form: HTMLFormElement;
let modal: HTMLDialogElement;
const submit = () => {
  const event = new Event('submit', { bubbles: true, cancelable: true });
  form.dispatchEvent(event);
  return event;
};
const unload = () => {
  const event = new Event('beforeunload', { cancelable: true });
  window.dispatchEvent(event);
  return event.defaultPrevented;
};
beforeEach(() => {
  window.history.replaceState(null, '', '/studio/projects');
  document.body.innerHTML = `<form method="post" data-guard-unsaved><input name="name" value="Rin"><button type="submit" name="intent" value="save">Save</button></form>
    <a id="next" href="/studio/characters">Characters</a><a id="hash" href="#details">Details</a>
    <dialog id="mv-unsaved"><button data-keep-editing>Keep editing</button></dialog>`;
  form = document.querySelector('form')!;
  modal = document.querySelector('dialog')!;
  modal.showModal = vi.fn(() => { modal.open = true; });
  modal.close = vi.fn(() => { modal.open = false; modal.dispatchEvent(new Event('close')); });
  cleanup = installForms();
});
afterEach(() => { cleanup?.(); vi.useRealTimers(); });

describe('shared server form behavior', () => {
  it('prevents duplicate posts without dropping the submitter, and restores bfcache buttons', async () => {
    vi.useFakeTimers();
    const button = form.querySelector('button')!;
    expect(submit().defaultPrevented).toBe(false);
    expect(form.getAttribute('aria-busy')).toBe('true');
    expect(button.disabled).toBe(false);
    expect(new FormData(form, button).get('intent')).toBe('save');
    expect(submit().defaultPrevented).toBe(true);
    await vi.runAllTimersAsync();
    expect(button.disabled).toBe(true);
    window.dispatchEvent(new Event('pageshow'));
    expect(button.disabled).toBe(false);
    expect(form.hasAttribute('aria-busy')).toBe(false);
    expect(submit().defaultPrevented).toBe(false);
  });

  it('opens the shared dialog only for actual dirty navigation and restores focus on cancel', () => {
    const link = document.getElementById('next')!;
    const click = () => {
      const event = new MouseEvent('click', { bubbles: true, cancelable: true });
      link.dispatchEvent(event);
      return event;
    };
    expect(unload()).toBe(false);
    (form.elements.namedItem('name') as HTMLInputElement).value = 'Aoi';
    expect(unload()).toBe(true);
    expect(click().defaultPrevented).toBe(true);
    expect(modal.open).toBe(true);
    expect(document.activeElement).toBe(modal.querySelector('[data-keep-editing]'));
    modal.close();
    expect(document.activeElement).toBe(link);
    (form.elements.namedItem('name') as HTMLInputElement).value = 'Rin';
    expect(unload()).toBe(false);
  });

  it('does not guard in-page anchors, other tabs, downloads, or already handled view switches', () => {
    (form.elements.namedItem('name') as HTMLInputElement).value = 'Aoi';
    const hash = document.getElementById('hash')!;
    hash.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    const link = document.getElementById('next') as HTMLAnchorElement;
    link.target = '_blank';
    link.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    link.target = '';
    link.download = 'chapter';
    link.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    link.removeAttribute('download');
    link.addEventListener('click', event => event.preventDefault());
    link.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    expect(modal.showModal).not.toHaveBeenCalled();
  });

  it('retains unsaved data after a failed HTMX save, and clears it only on success', () => {
    const input = form.elements.namedItem('name') as HTMLInputElement;
    input.value = 'Aoi';
    const event = (name: string, detail: object = {}) => document.dispatchEvent(new CustomEvent(name, { cancelable: true, detail: { elt: form, ...detail } }));
    event('htmx:beforeRequest');
    expect(unload()).toBe(false); // this submission may leave the current page
    event('htmx:afterRequest', { successful: true, xhr: { status: 422 } });
    expect(input.value).toBe('Aoi');
    expect(unload()).toBe(true);
    event('htmx:beforeRequest');
    event('htmx:afterRequest', { successful: true, xhr: { status: 200 } });
    expect(unload()).toBe(false);
    expect(form.hasAttribute('aria-busy')).toBe(false);
  });

  it('ignores a wizard or rich island that already owns submission', () => {
    form.addEventListener('submit', event => event.preventDefault());
    submit();
    expect(form.hasAttribute('aria-busy')).toBe(false);
  });

  it('uses the same guard for rich drafts and explicit island navigation', () => {
    const owner = document.createElement('div');
    document.body.append(owner);
    const dirty = (value: boolean) => document.dispatchEvent(new CustomEvent('mv:unsaved', { detail: { owner, dirty: value } }));
    dirty(true);
    expect(unload()).toBe(true);
    const proceed = vi.fn();
    document.dispatchEvent(new CustomEvent('mv:guard-navigation', { detail: { proceed } }));
    expect(modal.open).toBe(true);
    expect(proceed).not.toHaveBeenCalled();
    modal.close();
    expect(proceed).not.toHaveBeenCalled();
    dirty(false);
    expect(unload()).toBe(false);
    document.dispatchEvent(new CustomEvent('mv:guard-navigation', { detail: { proceed } }));
    expect(proceed).toHaveBeenCalledOnce();
    dirty(true);
    owner.remove();
    expect(unload()).toBe(false);
  });
});
