import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { characters, installCounters } from './counter';

let cleanup: (() => void) | undefined;

beforeEach(() => {
  document.body.innerHTML = `
    <textarea id="premise" maxlength="4000">Rin</textarea><span class="mv-field__count" aria-hidden="true">0 / 4000</span>
    <textarea id="short" maxlength="5"></textarea><span class="mv-field__count">0 / 5</span>
    <textarea id="loose"></textarea><span class="mv-field__count">untouched</span>`;
  cleanup = installCounters();
});
afterEach(() => cleanup?.());

const type = (id: string, value: string) => {
  const field = document.getElementById(id) as HTMLTextAreaElement;
  field.value = value;
  field.dispatchEvent(new Event('input', { bubbles: true }));
  return field.nextElementSibling as HTMLElement;
};

describe('live textarea counters', () => {
  it('syncs restored values on install and every keystroke after', () => {
    const counter = document.querySelector<HTMLElement>('#premise + .mv-field__count')!;
    expect(counter.textContent).toBe('3 / 4000');
    expect(type('premise', 'Rin Asagiri').textContent).toBe('11 / 4000');
    expect(type('premise', '').textContent).toBe('0 / 4000');
  });

  it('counts code points like the server and flags a full field', () => {
    expect(characters('ゴースト🌊')).toBe(5);
    const counter = type('short', 'ゴースト🌊');
    expect(counter.textContent).toBe('5 / 5');
    expect(counter.classList.contains('is-full')).toBe(true);
  });

  it('refreshes counters inside content swapped in by HTMX', () => {
    const host = document.createElement('div');
    host.innerHTML = '<textarea id="swapped" maxlength="600">Kaito</textarea><span class="mv-field__count">0 / 600</span>';
    document.body.append(host);
    document.dispatchEvent(new CustomEvent('htmx:afterSettle', { detail: { target: host } }));
    expect(host.querySelector('.mv-field__count')!.textContent).toBe('5 / 600');
  });

  it('leaves textareas without a limit alone', () => {
    expect(type('loose', 'anything').textContent).toBe('untouched');
  });
});
