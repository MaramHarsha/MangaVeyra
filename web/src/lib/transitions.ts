/**
 * Cross-document page turns. motion.css opts every page into view transitions
 * (`@view-transition { navigation: auto }`) so the designed page turn can run
 * between wizard steps, which set html[data-transition="turn"]. Every other
 * navigation skips its transition. A transition the browser aborts or skips
 * rejects its promises; unhandled, those surface as "Transition was aborted
 * because of invalid state" page errors, so both ends settle them. The main
 * script is render-blocking, so the reveal listener exists before the first
 * frame of the new page.
 */

export interface PageViewTransition {
  ready: Promise<unknown>;
  finished: Promise<unknown>;
  updateCallbackDone: Promise<unknown>;
  skipTransition(): void;
}

type PageTransitionEvent = Event & { viewTransition?: PageViewTransition | null };

/** settleTransition keeps a page turn only where it is designed. */
export function settleTransition(e: Event, root: HTMLElement): void {
  const vt = (e as PageTransitionEvent).viewTransition;
  if (!vt) return;
  for (const p of [vt.ready, vt.finished, vt.updateCallbackDone]) p?.catch(() => undefined);
  if (root.dataset.transition !== 'turn') vt.skipTransition();
}

/** installPageTurns listens on both ends of a cross-document navigation. */
export function installPageTurns(win: Window, root: HTMLElement): () => void {
  const onEvent = (e: Event) => settleTransition(e, root);
  win.addEventListener('pageswap', onEvent);
  win.addEventListener('pagereveal', onEvent);
  return () => {
    win.removeEventListener('pageswap', onEvent);
    win.removeEventListener('pagereveal', onEvent);
  };
}
