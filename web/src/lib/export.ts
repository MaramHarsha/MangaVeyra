/** Keep the E2 proof in the language selected for the actual export. */
export function updateExportPreview(select: HTMLSelectElement): void {
  select.closest('.mv-exportui')?.querySelectorAll<HTMLImageElement>('[data-export-preview]').forEach(image => {
    const url = new URL(image.src, location.href);
    if (url.origin !== location.origin) return;
    url.searchParams.set('lang', select.value);
    image.src = url.href;
  });
}

document.addEventListener('change', event => {
  const target = event.target;
  if (target instanceof HTMLSelectElement && target.hasAttribute('data-export-language')) updateExportPreview(target);
});
