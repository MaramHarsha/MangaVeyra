<script lang="ts">
 import type { ChapterLink, Provenance, T } from './types';

 let { label, webtoon, provenance, prev, next, seriesUrl, t }: {
  label: string; webtoon: boolean; provenance: Provenance[]; prev: ChapterLink | null; next: ChapterLink | null; seriesUrl: string; t: T;
 } = $props();

 const icon = (kind: string) => (kind === 'human' ? 'brush' : 'sparkles');
</script>

<section class="mv-rd-end" aria-labelledby="mv-rd-end-title">
 <span class="mv-label-mono mv-rd-end__kicker">{label}</span>
 <h2 class="mv-rd-end__title" id="mv-rd-end-title">{webtoon ? t('end.title.episode') : t('end.title')}</h2>
 {#if provenance.length}
  <div class="mv-rd-end__labels" aria-label={t('end.provenance')}>
   {#each provenance as p}
    <span class="mv-badge mv-badge--text"><i class={`ti ti-${icon(p.kind)}`} aria-hidden="true"></i>{p.label}</span>
   {/each}
  </div>
 {/if}
 <div class="mv-rd-end__actions">
  {#if prev}
   <a class="mv-btn mv-btn--ghost" href={prev.href}><i class="ti ti-chevron-left" aria-hidden="true"></i>{t('prev_chapter')}</a>
  {/if}
  {#if next}
   <a class="mv-btn mv-btn--primary" href={next.href}><i class="ti ti-arrow-right" aria-hidden="true"></i>{t('end.next')}</a>
  {:else}
   <span class="mv-small mv-muted">{t('end.last')}</span>
  {/if}
  {#if seriesUrl}
   <a class="mv-btn mv-btn--quiet" href={seriesUrl}>{t('end.series')}</a>
  {/if}
 </div>
</section>
