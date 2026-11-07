import { afterEach, describe, expect, it, vi } from 'vitest';
import { installPageTurns } from './transitions';

function transition(reason: unknown) {
  const rejected = () => {
    const p = Promise.reject(reason);
    return p;
  };
  return { ready: rejected(), finished: rejected(), updateCallbackDone: rejected(), skipTransition: vi.fn() };
}

function fire(type: 'pageswap' | 'pagereveal', vt: ReturnType<typeof transition> | null) {
  const e = new Event(type);
  Object.defineProperty(e, 'viewTransition', { value: vt });
  window.dispatchEvent(e);
}

let cleanup: (() => void) | undefined;
afterEach(() => {
  cleanup?.();
  delete document.documentElement.dataset.transition;
});

describe('cross-document page turns', () => {
  it('skips ordinary navigations and settles aborted transitions', async () => {
    const unhandled = vi.fn();
    process.on('unhandledRejection', unhandled);
    cleanup = installPageTurns(window, document.documentElement);
    const swap = transition(new Error('Transition was aborted because of invalid state'));
    const reveal = transition(new Error('ViewTransition opt-in disabled'));
    fire('pageswap', swap);
    fire('pagereveal', reveal);
    fire('pagereveal', null);
    await new Promise((r) => setTimeout(r, 0));
    process.off('unhandledRejection', unhandled);
    expect(swap.skipTransition).toHaveBeenCalledTimes(1);
    expect(reveal.skipTransition).toHaveBeenCalledTimes(1);
    expect(unhandled).not.toHaveBeenCalled();
  });

  it('keeps the designed page turn on wizard pages', async () => {
    document.documentElement.dataset.transition = 'turn';
    cleanup = installPageTurns(window, document.documentElement);
    const reveal = transition(new Error('skipped'));
    fire('pagereveal', reveal);
    await new Promise((r) => setTimeout(r, 0));
    expect(reveal.skipTransition).not.toHaveBeenCalled();
  });
});
