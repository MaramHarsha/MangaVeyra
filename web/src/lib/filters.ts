/**
 * Filter forms that apply on change. A server-rendered GET filter form keeps
 * an Apply button for browsers without JavaScript (class mv-nojs-only); with
 * JavaScript each select marked data-autosubmit submits its form as soon as
 * it changes, so no extra click is needed.
 */
export function installAutoSubmit(doc: Document = document): () => void {
  const onChange = (e: Event) => {
    const t = e.target;
    if (!(t instanceof HTMLSelectElement || t instanceof HTMLInputElement) || !t.hasAttribute('data-autosubmit')) return;
    const form = t.form;
    if (!form) return;
    if (typeof form.requestSubmit === 'function') form.requestSubmit();
    else form.submit();
  };
  doc.addEventListener('change', onChange);
  return () => doc.removeEventListener('change', onChange);
}
