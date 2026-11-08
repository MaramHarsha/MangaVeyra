/**
 * Copy buttons (G5 invite and reset links): `button[data-copy="<input id>"]`
 * copies that read-only input's value and confirms with a toast
 * (`data-copied-label`). The buttons carry `.mv-js-only`; without JavaScript
 * the read-only field stays selectable, so the link is copied by hand.
 */
import { showToast } from './toast';

type Writer = (text: string) => Promise<void>;

/** copyTarget returns the input a copy button names, or null. */
export function copyTarget(button: Element, doc: Document = document): HTMLInputElement | null {
  const id = button.getAttribute('data-copy');
  if (!id) return null;
  const el = doc.getElementById(id);
  return el instanceof HTMLInputElement ? el : null;
}

/** installCopy wires every `[data-copy]` button; write defaults to the clipboard API. */
export function installCopy(doc: Document = document, write?: Writer): () => void {
  const onClick = async (e: Event) => {
    const t = e.target;
    if (!(t instanceof Element)) return;
    const button = t.closest('[data-copy]');
    if (!button) return;
    const input = copyTarget(button, doc);
    if (!input) return;
    e.preventDefault();
    const writer = write ?? ((text: string) => navigator.clipboard.writeText(text));
    try {
      await writer(input.value);
      const title = button.getAttribute('data-copied-label');
      if (title) showToast({ kind: 'success', title });
    } catch {
      // No clipboard access (insecure origin, denied): select the text instead.
      input.focus();
      input.select();
    }
  };
  doc.addEventListener('click', onClick);
  return () => doc.removeEventListener('click', onClick);
}

// The browser bundle installs itself; unit tests install with a fake writer.
if (typeof document !== 'undefined' && import.meta.env.MODE !== 'test') installCopy();
