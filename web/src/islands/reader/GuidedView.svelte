<script lang="ts">
 import type { Direction, Page, T } from './types';
 import { frame, maskPath, orderedPanels, panelPosition, wholePage, type GuidedStep } from './guided';

 let {
  pages, steps, index, dir, t, onstep, onpagemode, onimgerror,
 }: {
  pages: Page[]; steps: GuidedStep[]; index: number; dir: Direction; t: T;
  onstep: (delta: number) => void; onpagemode: () => void; onimgerror: () => void;
 } = $props();

 let box: HTMLDivElement | undefined = $state();
 let availW = $state(800), availH = $state(500);

 const step = $derived(steps[index]);
 const page = $derived(pages.find((p) => p.number === step?.page));
 const panel = $derived(page ? orderedPanels(page).find((pn) => pn.order === step?.panel) : undefined);
 const position = $derived(panelPosition(steps, index));
 const f = $derived(page ? (panel ? frame(panel, page, availW, availH) : wholePage(page, availW, availH)) : null);
 const mask = $derived(panel ? maskPath(panel) : '');
 const alt = $derived((panel?.alt_text || page?.alt || '').trim());
 // RTL turns forward to the left: the left button goes back with a right chevron (E3 guided).
 const backIcon = $derived(dir === 'rtl' ? 'chevron-right' : 'chevron-left');
 const nextIcon = $derived(dir === 'rtl' ? 'chevron-left' : 'chevron-right');

 $effect(() => {
  if (!box) return;
  const ro = new ResizeObserver(() => {
   if (!box) return;
   const narrow = box.clientWidth < 600;
   availW = Math.max(120, box.clientWidth - (narrow ? 32 : 96) - 6);
   availH = Math.max(120, box.clientHeight - (narrow ? 32 : 64) - 6);
  });
  ro.observe(box);
  return () => ro.disconnect();
 });
</script>

<div class="mv-rd-guided">
 <div class="mv-rd-guided__stage" bind:this={box}>
  {#if page && f}
   <div class="mv-rd-guided__frame" style={`width:${f.width}px;height:${f.height}px`} role="img"
    aria-roledescription={t('mode.guided')} aria-label={`${t('panel.of', { panel: position, total: step.total })}. ${alt}`}>
    {#if page.src}
     <img class="mv-rd-guided__img" src={page.src} alt="" draggable="false" onerror={onimgerror}
      style={`width:${f.imageW}px;height:${f.imageH}px;transform:translate(${f.imageX}px,${f.imageY}px)`} />
    {/if}
    {#if panel && mask}
     <svg class="mv-rd-guided__mask" viewBox={`0 0 ${panel.w} ${panel.h}`} preserveAspectRatio="none" aria-hidden="true">
      <path d={mask} fill-rule="evenodd" />
     </svg>
    {/if}
    <span class="mv-rd-guided__label" data-theme="light" aria-hidden="true">{t('panel.label', { panel: position, total: step.total })}</span>
   </div>
  {/if}
 </div>
 <div class="mv-rd-guided__dock">
  {#if alt}
   <p class="mv-rd-guided__alt"><i class="ti ti-accessible" aria-hidden="true"></i> <em>{t('alt_text')}</em> {alt}</p>
  {/if}
  <div class="mv-rd-guided__controls">
   <button type="button" class="mv-btn mv-btn--icon mv-rd-guided__btn" aria-label={t('prev.panel')} title={t('prev.panel')} disabled={index <= 0} onclick={() => onstep(-1)}>
    <i class={`ti ti-${backIcon}`} aria-hidden="true"></i>
   </button>
   <div class="mv-seg mv-rd-seg" role="group" aria-label={t('mode')}>
    <button type="button" class="mv-seg__item" aria-pressed="false" onclick={onpagemode}>{t('mode.paged')}</button>
    <button type="button" class="mv-seg__item" aria-pressed="true">{t('mode.guided')}</button>
   </div>
   <button type="button" class="mv-btn mv-btn--icon mv-rd-guided__btn" aria-label={t('next.panel')} title={t('next.panel')} onclick={() => onstep(1)}>
    <i class={`ti ti-${nextIcon}`} aria-hidden="true"></i>
   </button>
  </div>
 </div>
</div>
