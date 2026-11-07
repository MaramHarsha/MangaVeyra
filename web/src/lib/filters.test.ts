import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { installAutoSubmit } from './filters';

let cleanup: (() => void) | undefined;
beforeEach(() => {
  document.body.innerHTML = `
    <form method="get" action="/studio/projects">
      <input name="q" value="">
      <select name="status" data-autosubmit><option value="">Any</option><option value="draft">Draft</option></select>
      <select name="plain"><option value="a">A</option><option value="b">B</option></select>
      <button type="submit" class="mv-nojs-only">Apply</button>
    </form>`;
  cleanup = installAutoSubmit();
});
afterEach(() => cleanup?.());

describe('auto-submitting filters', () => {
  it('submits the filter form when a marked select changes', () => {
    const form = document.querySelector('form')!;
    const submit = vi.fn((e: Event) => e.preventDefault());
    form.addEventListener('submit', submit);
    const status = form.querySelector<HTMLSelectElement>('select[name=status]')!;
    status.value = 'draft';
    status.dispatchEvent(new Event('change', { bubbles: true }));
    expect(submit).toHaveBeenCalledTimes(1);
    expect(new FormData(form).get('status')).toBe('draft');
  });

  it('leaves unmarked controls alone', () => {
    const form = document.querySelector('form')!;
    const submit = vi.fn((e: Event) => e.preventDefault());
    form.addEventListener('submit', submit);
    const plain = form.querySelector<HTMLSelectElement>('select[name=plain]')!;
    plain.value = 'b';
    plain.dispatchEvent(new Event('change', { bubbles: true }));
    expect(submit).not.toHaveBeenCalled();
  });
});
