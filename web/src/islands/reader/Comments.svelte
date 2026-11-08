<script lang="ts">
 import type { T } from './types';
 import { anchorOf, authorName, load, postBody, type Comment, type LoadState } from './comments';
 import { call } from './api';
 import { showToast } from '../../lib/toast';

 let {
  api, seriesId, publicationId, page, panel, signedIn, loginUrl, t, onclose,
 }: {
  api: string; seriesId: string; publicationId: string; page: number; panel: number; signedIn: boolean; loginUrl: string; t: T; onclose: () => void;
 } = $props();

 let state = $state<LoadState>('loading');
 let chapter = $state.raw<Comment[]>([]);
 let panelComments = $state.raw<Comment[]>([]);
 let draft = $state('');
 let busy = $state(false);

 async function refresh(forPage: number) {
  state = 'loading';
  const r = await load(api, publicationId, seriesId, forPage);
  state = r.state;
  chapter = r.chapter;
  panelComments = r.panel;
 }

 $effect(() => {
  void refresh(page);
 });

 async function submit(e: SubmitEvent) {
  e.preventDefault();
  const body = draft.trim();
  if (!body || busy) return;
  busy = true;
  const r = await call<Comment>('POST', api, postBody(seriesId, publicationId, body, panel > 0 ? { page, panel } : null));
  busy = false;
  if (!r.ok) {
   showToast({ kind: 'error', title: t('comments.failed') });
   return;
  }
  draft = '';
  showToast({ kind: 'success', title: t('comments.posted') });
  void refresh(page);
 }

 const name = authorName;
</script>

<aside class="mv-rd-comments" aria-labelledby="mv-rd-comments-title">
 <div class="mv-rd-set__head">
  <strong class="mv-rd-set__title" id="mv-rd-comments-title">{t('comments.title')}</strong>
  <button type="button" class="mv-btn mv-btn--bare mv-btn--sm" aria-label={t('comments.close')} title={t('comments.close')} onclick={onclose}>
   <i class="ti ti-x" aria-hidden="true"></i>
  </button>
 </div>
 {#if state === 'loading'}
  <p class="mv-small mv-muted" role="status">{t('comments.loading')}</p>
 {:else if state === 'unavailable'}
  <p class="mv-small mv-muted" role="status">{t('comments.unavailable')}</p>
 {:else}
  <div class="mv-rd-comments__list">
   {#if panelComments.length}
    {#each panelComments as c (c.id)}
     <article class="mv-rd-comments__item">
      <span class="mv-label-mono">{t('comments.panel', { panel: anchorOf(c)?.panel ?? 0, page })}</span>
      {#if name(c)}<strong class="mv-small">{name(c)}</strong>{/if}
      <p>{c.body}</p>
     </article>
    {/each}
   {/if}
   {#each chapter as c (c.id)}
    <article class="mv-rd-comments__item">
     <span class="mv-label-mono">{t('comments.chapter')}</span>
     {#if name(c)}<strong class="mv-small">{name(c)}</strong>{/if}
     <p>{c.body}</p>
    </article>
   {/each}
   {#if !chapter.length && !panelComments.length}
    <p class="mv-small mv-muted">{t('comments.empty')}</p>
   {/if}
  </div>
  {#if signedIn}
   <form class="mv-rd-comments__form" onsubmit={submit}>
    <label class="mv-sr-only" for="mv-rd-comment">{t('comments.placeholder')}</label>
    <textarea id="mv-rd-comment" class="mv-textarea" rows="2" maxlength="2000" placeholder={t('comments.placeholder')} bind:value={draft}></textarea>
    <button type="submit" class="mv-btn mv-btn--primary mv-btn--sm" disabled={busy || !draft.trim()}>{t('comments.post')}</button>
   </form>
  {:else}
   <a class="mv-btn mv-btn--quiet mv-btn--sm" href={loginUrl}>{t('comments.signin')}</a>
  {/if}
 {/if}
</aside>
