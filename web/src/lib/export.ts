/** Keep the E2 proof in the language selected for the actual export. */
export function updateExportPreview(select: HTMLSelectElement): void {
  select.closest('.mv-exportui')?.querySelectorAll<HTMLImageElement>('[data-export-preview]').forEach(image => {
    const url = new URL(image.src, location.href);
    if (url.origin !== location.origin) return;
    url.searchParams.set('lang', select.value);
    image.src = url.href;
  });
}

/**
 * Name the chosen format on the Export button ("Export PDF"). The server marks
 * the label for the format it rendered and CSS :has() follows the radios
 * without JavaScript; this keeps browsers without :has() in step. A format the
 * button cannot name falls back to the generic label (data-export-label="").
 */
export function updateExportLabel(radio: HTMLInputElement): void {
  const form = radio.form;
  if (!form || !radio.checked) return;
  const labels = form.querySelectorAll<HTMLElement>('[data-export-submit] [data-export-label]');
  const known = Array.from(labels).some(label => label.dataset.exportLabel === radio.value);
  labels.forEach(label => label.classList.toggle('is-current', label.dataset.exportLabel === (known ? radio.value : '')));
}

document.addEventListener('change', event => {
  const target = event.target;
  if (target instanceof HTMLSelectElement && target.hasAttribute('data-export-language')) updateExportPreview(target);
  if (target instanceof HTMLInputElement && target.type === 'radio' && target.name === 'format' && target.closest('.mv-exportui')) updateExportLabel(target);
});
