/**
 * G11 Model routing: the designed Guards box and mode switch have no Save
 * button, so with JavaScript every change saves at once. The server form
 * (`form[data-autosave]`) still has a Save button for pages without
 * JavaScript; `.mv-nojs-only` hides it here. Inputs outside the form that name
 * it with `form=` (the mode switch in the page head) save the same way.
 */
import { showToast } from './toast';

type Fetcher = (input: string, init: RequestInit) => Promise<Pick<Response, 'ok'>>;

/** autosaveForm returns the auto-saving form a changed control belongs to. */
export function autosaveForm(target: EventTarget | null): HTMLFormElement | null {
  if (!(target instanceof HTMLInputElement || target instanceof HTMLSelectElement)) return null;
  const form = target.form;
  return form && form.hasAttribute('data-autosave') ? form : null;
}

/** saveForm posts the form (with every associated control) and reports the outcome. */
export async function saveForm(form: HTMLFormElement, fetcher: Fetcher = fetch): Promise<boolean> {
  const body = new URLSearchParams();
  new FormData(form).forEach((value, key) => {
    if (typeof value === 'string') body.append(key, value);
  });
  try {
    const res = await fetcher(form.action, {
      method: 'POST',
      body,
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    });
    return res.ok;
  } catch {
    return false;
  }
}

/** installAutosave wires change-to-save for every `form[data-autosave]`. */
export function installAutosave(doc: Document = document, fetcher: Fetcher = fetch): () => void {
  const pending = new WeakMap<HTMLFormElement, Promise<void>>();
  const onChange = (e: Event) => {
    const form = autosaveForm(e.target);
    if (!form) return;
    // Saves run one after another so the last change always wins.
    const previous = pending.get(form) ?? Promise.resolve();
    const next = previous.then(async () => {
      const ok = await saveForm(form, fetcher);
      const title = ok ? form.dataset.savedLabel : form.dataset.errorLabel;
      if (title) showToast({ kind: ok ? 'success' : 'error', title });
    });
    pending.set(form, next);
  };
  doc.addEventListener('change', onChange);
  return () => doc.removeEventListener('change', onChange);
}

// The browser bundle installs itself; unit tests install with a fake fetch.
if (typeof document !== 'undefined' && import.meta.env.MODE !== 'test') installAutosave();
