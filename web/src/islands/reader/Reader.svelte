<script lang="ts">
 import { onMount, untrack } from 'svelte';
 import type { Bookmark, DownloadState, Mode, Page, ReaderProps } from './types';
 import { translator } from './types';
 import { effectiveMode, keyStep, progressFraction, progressText, singles, spreads, viewIndexOf, viewOrder, zoneStep, type Step } from './direction';
 import { orderedPanels, sequence, spokenText, stepIndex } from './guided';
 import { browserReadAloud } from './readaloud';
 import { builtAssets, expired, readMeta, removeChapter, saveChapter, type Manifest } from './offline';
 import { clampZoom, loadLocal, localRow, merge, saveLocal, serverRow, storage, type ReaderSettings } from './prefs';
 import { beaconSend, ReadingEvents } from './events';
 import { call, rememberPending, takePending, type ProgressBody } from './api';
 import { showToast } from '../../lib/toast';
 import SettingsPanel from './SettingsPanel.svelte';
 import GuidedView from './GuidedView.svelte';
 import Comments from './Comments.svelte';
 import ChapterEnd from './ChapterEnd.svelte';

 let { boot, host }: { boot: ReaderProps; host: HTMLElement } = $props();
 const p = untrack(() => boot);
 const root = untrack(() => host);
 const t = translator(p.messages);
 const webtoon = p.series.format === 'webtoon';
 const store = storage();
 const total = p.pages.length;
 const modes: Exclude<Mode, 'auto'>[] = webtoon ? ['webtoon', 'guided'] : ['paged', 'spread', 'guided', 'webtoon'];

 /* ---- state ---- */
 let settings = $state<ReaderSettings>(merge(p.prefs, loadLocal(store, p.series.id), p.signed_in, webtoon));
 let pages = $state.raw<Page[]>(p.pages);
 let vw = $state(typeof window === 'undefined' ? 1440 : window.innerWidth);
 const mode = $derived(effectiveMode(settings.mode, vw, webtoon));
 const views = $derived(mode === 'spread' ? spreads(pages) : singles(pages));
 let current = $state(Math.min(Math.max(1, p.start_page || 1), Math.max(1, total)));
 const viewIdx = $derived(viewIndexOf(views, current));
 const view = $derived(views[viewIdx] || []);
 let atEnd = $state(false);
 const steps = sequence(p.pages);
 let step = $state(stepIndex(steps, p.start_page || 1, p.start_panel || 0));
 let settingsOpen = $state(untrack(() => settings.settings_open ?? (!webtoon && vw >= 1280)));
 let commentsOpen = $state(false);
 let chromeHidden = $state(false);
 let bookmarks = $state.raw<Bookmark[]>(p.bookmarks);
 let download = $state<DownloadState | null>(p.download);
 let onDevice = $state(false);
 let downloading = $state(-1);
 let online = $state(typeof navigator === 'undefined' ? true : navigator.onLine);
 let scrollPct = $state(0);
 let stageEl: HTMLElement | undefined = $state();
 let turnKey = $state(0);

 const pageOf = (n: number) => pages.find((pg) => pg.number === n);

 /* ---- page size: fit the view into the stage (E3: 520 × 735 at 1440 × 900) ---- */
 let stageW = $state(0), stageH = $state(0);
 const pageHeight = $derived.by(() => {
  const shown = view.map(pageOf).filter((pg): pg is Page => !!pg && pg.width > 0 && pg.height > 0);
  if (!shown.length || stageW <= 0 || stageH <= 0) return 0;
  const ratio = Math.max(...shown.map((pg) => pg.height / pg.width));
  const byHeight = stageH - 165;
  const byWidth = ((stageW - 48 - (shown.length - 1) * 2) / shown.length) * ratio;
  return Math.max(120, Math.min(byHeight, byWidth)) * settings.zoom;
 });
 const pageStyle = (pg: Page) => (pageHeight > 0 ? `height:${Math.round(pageHeight)}px;width:${Math.round((pageHeight * pg.width) / Math.max(1, pg.height))}px` : '');
 const guidedStep = $derived(steps[step]);
 const readingPage = $derived(mode === 'guided' ? guidedStep?.page ?? current : current);
 const readingPanel = $derived(mode === 'guided' ? guidedStep?.panel ?? 0 : 0);
 const bookmarked = $derived(bookmarks.find((b) => b.page === readingPage && (Number(b.panel) || 0) === readingPanel));
 const fraction = $derived(mode === 'webtoon' ? scrollPct : mode === 'guided' ? (steps.length ? (step + 1) / steps.length : 0) : progressFraction(view, total));

 /* ---- read-aloud (on-device voices only) ---- */
 const voice = browserReadAloud(p.publication.lang);
 let voiceReady = $state(voice.available);
 let voiceName = $state(voice.voiceName);
 voice.onChange(() => {
  voiceReady = voice.available;
  voiceName = voice.voiceName;
 });

 /* ---- reading events ---- */
 const events = new ReadingEvents({ url: p.api.events, seriesId: p.series.id, publicationId: p.publication.id, total, enabled: p.events, send: beaconSend });

 /* ---- host attributes (background, brightness, zoom, motion, chrome) ---- */
 $effect(() => {
  root.dataset.bg = settings.background;
  root.dataset.mode = mode;
  root.dataset.turn = settings.reduced_motion ? 'off' : 'on';
  root.dataset.settings = settingsOpen || commentsOpen ? 'open' : 'closed';
  root.dataset.chrome = chromeHidden ? 'hidden' : 'shown';
  root.style.setProperty('--mv-rd-zoom', String(settings.zoom));
  root.style.setProperty('--mv-rd-bright', String(settings.brightness));
 });

 /* ---- settings ---- */
 let prefsTimer: ReturnType<typeof setTimeout> | null = null;
 function change(patch: Partial<ReaderSettings>) {
  const before = { mode: settings.mode, direction: settings.direction, background: settings.background, zoom: settings.zoom };
  settings = { ...settings, ...patch, zoom: clampZoom(patch.zoom ?? settings.zoom) };
  saveLocal(store, p.series.id, localRow(settings, p.signed_in));
  const server = before.mode !== settings.mode || before.direction !== settings.direction || before.background !== settings.background || before.zoom !== settings.zoom;
  if (p.signed_in && server) {
   if (prefsTimer) clearTimeout(prefsTimer);
   prefsTimer = setTimeout(() => {
    void call('PUT', p.api.prefs, serverRow(settings, effectiveMode(settings.mode, vw, webtoon)));
   }, 500);
  }
  if (patch.read_aloud === false) voice.stop();
  if (patch.read_aloud) speakCurrent();
 }

 function setMode(m: Exclude<Mode, 'auto'>) {
  if (m === mode) return;
  if (m === 'guided') step = stepIndex(steps, current);
  else if (mode === 'guided' && guidedStep) current = guidedStep.page;
  atEnd = false;
  change({ mode: m });
  if (m === 'webtoon') requestAnimationFrame(() => scrollToPage(current, false));
 }

 function toggleSettings() {
  settingsOpen = !settingsOpen;
  commentsOpen = false;
  settings = { ...settings, settings_open: settingsOpen };
  saveLocal(store, p.series.id, localRow(settings, p.signed_in));
 }

 /* ---- navigation ---- */
 function go(s: Step) {
  if (!s) return;
  if (s === 'menu') {
   chromeHidden = !chromeHidden;
   return;
  }
  if (mode === 'guided') {
   if (s === 'next') guidedMove(1);
   else if (s === 'prev') guidedMove(-1);
   else if (s === 'first') step = 0;
   else if (s === 'last') step = Math.max(0, steps.length - 1);
   return;
  }
  if (mode === 'webtoon') {
   if (s === 'first') scrollToPage(1, true);
   if (s === 'last') scrollToPage(total, true);
   return;
  }
  if (s === 'next') {
   if (atEnd) {
    if (p.next) location.href = p.next.href;
    return;
   }
   if (viewIdx >= views.length - 1) {
    atEnd = true;
    return;
   }
   current = views[viewIdx + 1][0];
  } else if (s === 'prev') {
   if (atEnd) {
    atEnd = false;
    return;
   }
   if (viewIdx > 0) current = views[viewIdx - 1][0];
  } else if (s === 'first') {
   atEnd = false;
   current = views[0]?.[0] ?? 1;
  } else if (s === 'last') {
   atEnd = false;
   current = views[views.length - 1]?.[0] ?? total;
  }
  turnKey++;
 }

 function guidedMove(delta: number) {
  const n = step + delta;
  if (n < 0) return;
  if (n >= steps.length) {
   if (p.next) location.href = p.next.href;
   return;
  }
  step = n;
 }

 function onStageClick(e: MouseEvent) {
  if (mode !== 'paged' && mode !== 'spread') return;
  const target = e.target as HTMLElement;
  if (target.closest('a,button,input,textarea,select,label,.mv-rd-end')) return;
  if (!settings.tap_zones || !stageEl) return;
  const r = stageEl.getBoundingClientRect();
  go(zoneStep(e.clientX - r.left, e.clientY - r.top, r.width, r.height, settings.direction, p.prefs.tap_l_shape));
 }

 function onKey(e: KeyboardEvent) {
  if (e.defaultPrevented || e.altKey || e.ctrlKey || e.metaKey) return;
  const el = e.target as HTMLElement | null;
  if (el && (el.closest('input,textarea,select,[contenteditable="true"]') || (el.closest('button,a') && (e.key === ' ' || e.key === 'Enter')))) return;
  if (e.key === 'Escape') {
   if (commentsOpen) commentsOpen = false;
   else if (settingsOpen) toggleSettings();
   return;
  }
  if (e.key === '+' || e.key === '=') return void change({ zoom: settings.zoom + 0.25 });
  if (e.key === '-' || e.key === '_') return void change({ zoom: settings.zoom - 0.25 });
  if (e.key === '0') return void change({ zoom: 1 });
  const s = keyStep(e.key, settings.direction, mode);
  if (s) {
   e.preventDefault();
   go(s);
  }
 }

 /* ---- webtoon scroll ---- */
 let pageEls: Record<number, HTMLElement> = {};
 function registerPage(el: HTMLElement, n: number) {
  pageEls[n] = el;
  return { destroy() { delete pageEls[n]; } };
 }
 function scrollToPage(n: number, smooth: boolean) {
  const el = pageEls[n];
  if (el && stageEl) stageEl.scrollTo({ top: el.offsetTop - 8, behavior: smooth && !settings.reduced_motion ? 'smooth' : 'auto' });
 }
 let lastScroll = 0;
 function onScroll() {
  if (!stageEl || mode !== 'webtoon') return;
  const max = stageEl.scrollHeight - stageEl.clientHeight;
  scrollPct = max > 0 ? Math.min(1, stageEl.scrollTop / max) : 1;
  const mid = stageEl.scrollTop + stageEl.clientHeight * 0.4;
  let n = current;
  for (const [k, el] of Object.entries(pageEls)) {
   if (el.offsetTop <= mid && el.offsetTop + el.offsetHeight > mid) n = Number(k);
  }
  if (n !== current) current = n;
  if (p.prefs.hide_chrome) chromeHidden = stageEl.scrollTop > lastScroll && stageEl.scrollTop > 80;
  lastScroll = stageEl.scrollTop;
 }

 /* ---- progress ---- */
 let progressTimer: ReturnType<typeof setTimeout> | null = null;
 function saveProgress(page: number, percent: number, done: boolean) {
  if (!p.signed_in) return;
  if (progressTimer) clearTimeout(progressTimer);
  progressTimer = setTimeout(async () => {
   const body: ProgressBody = { series_id: p.series.id, publication_id: p.publication.id, page, percent: Math.min(1, Math.max(0, percent)), completed: done };
   const r = await call('PUT', p.api.progress, body);
   if (r.offline) rememberPending(body);
  }, 1200);
 }

 $effect(() => {
  const page = readingPage;
  const done = mode === 'webtoon' ? scrollPct >= 0.98 : atEnd || page >= total;
  untrack(() => {
   events.pageShown(page);
   saveProgress(page, mode === 'webtoon' ? scrollPct : page / Math.max(1, total), done);
  });
 });

 $effect(() => {
  if (atEnd) untrack(() => events.pageShown(total));
 });

 /* ---- read-aloud on page or panel change ---- */
 function speakCurrent() {
  if (!settings.read_aloud || !voiceReady) return;
  if (mode === 'guided' && guidedStep) {
   const pg = pageOf(guidedStep.page);
   const pn = pg ? orderedPanels(pg).find((x) => x.order === guidedStep.panel) : undefined;
   voice.speak(spokenText(pn, pg, t));
   return;
  }
  const shown = mode === 'webtoon' ? [current] : view;
  const text = shown.map((n) => {
   const pg = pageOf(n);
   if (!pg) return '';
   const panels = orderedPanels(pg);
   return panels.length ? panels.map((pn) => spokenText(pn, pg, t)).join(' ') : spokenText(undefined, pg, t);
  }).join(' ');
  voice.speak(text);
 }
 $effect(() => {
  void readingPage;
  void readingPanel;
  void mode;
  untrack(() => speakCurrent());
 });

 /* ---- bookmarks ---- */
 async function toggleBookmark() {
  if (!p.signed_in) {
   showToast({ kind: 'info', title: t('bookmark.signin'), href: p.login_url, action: t('bookmark') });
   return;
  }
  if (bookmarked) {
   const r = await call('DELETE', `${p.api.bookmarks}/${encodeURIComponent(bookmarked.id)}`);
   if (r.ok || r.status === 404) {
    bookmarks = bookmarks.filter((b) => b.id !== bookmarked!.id);
    showToast({ kind: 'success', title: t('bookmark.removed') });
   } else showToast({ kind: 'error', title: r.offline ? t('error.network') : t('refresh.failed') });
   return;
  }
  const r = await call<Bookmark>('POST', p.api.bookmarks, { series_id: p.series.id, publication_id: p.publication.id, page: readingPage, panel: readingPanel });
  if (r.ok && r.data) {
   bookmarks = [...bookmarks.filter((b) => b.id !== r.data!.id), r.data];
   showToast({ kind: 'success', title: t('bookmark.added') });
  } else showToast({ kind: 'error', title: r.offline ? t('error.network') : t('refresh.failed') });
 }

 /* ---- offline download ---- */
 async function toggleDownload() {
  if (!p.signed_in) {
   showToast({ kind: 'info', title: t('download.signin'), href: p.login_url, action: t('download') });
   return;
  }
  if (typeof caches === 'undefined') {
   showToast({ kind: 'warning', title: t('download.unsupported') });
   return;
  }
  if (downloading >= 0) return;
  if (onDevice && download) {
   await removeChapter(caches, p.publication.id);
   await call('DELETE', `${p.api.downloads}/${encodeURIComponent(download.id)}`);
   onDevice = false;
   download = null;
   showToast({ kind: 'success', title: t('download.removed') });
   return;
  }
  downloading = 0;
  const r = await call<Manifest>('POST', p.api.downloads, { series_id: p.series.id, publication_id: p.publication.id });
  if (!r.ok || !r.data) {
   downloading = -1;
   showToast({ kind: 'error', title: r.offline ? t('error.network') : t('download.failed') });
   return;
  }
  const man = r.data;
  try {
   const bytes = await saveChapter(man, caches, fetch, { assets: builtAssets(), onProgress: (d, n) => (downloading = Math.round((d / n) * 100)) });
   const fin = await call<DownloadState>('PUT', `${p.api.downloads}/${encodeURIComponent(man.download.id)}`, { bytes });
   if (!fin.ok) throw new Error('finish');
   download = fin.data ?? { ...man.download, status: 'ready' } as DownloadState;
   onDevice = true;
   showToast({ kind: 'success', title: t('download.done'), body: t('download.expires', { date: new Date(man.download.expires_at * 1000).toLocaleDateString(p.ui_lang) }) });
  } catch {
   await removeChapter(caches, p.publication.id);
   await call('DELETE', `${p.api.downloads}/${encodeURIComponent(man.download.id)}`);
   download = null;
   showToast({ kind: 'error', title: t('download.failed') });
  } finally {
   downloading = -1;
  }
 }

 /* ---- signed URL refresh ---- */
 let refreshing = false;
 let lastRefresh = 0;
 async function refreshURLs() {
  if (refreshing || Date.now() - lastRefresh < 15000) return;
  refreshing = true;
  lastRefresh = Date.now();
  const r = await call<{ pages: Page[] }>('GET', p.api.chapter);
  refreshing = false;
  if (r.ok && r.data?.pages) {
   const fresh = new Map(r.data.pages.map((pg) => [pg.id, pg.src]));
   pages = pages.map((pg) => ({ ...pg, src: fresh.get(pg.id) || pg.src }));
  }
 }

 /* ---- lifecycle ---- */
 onMount(() => {
  const resize = () => (vw = window.innerWidth);
  const goOnline = async () => {
   online = true;
   for (const body of takePending()) {
    const r = await call('PUT', p.api.progress, body);
    if (r.offline) rememberPending(body);
   }
  };
  const goOffline = () => (online = false);
  const hide = () => {
   events.leave();
   voice.stop();
  };
  const vis = () => {
   if (document.visibilityState === 'hidden') events.flush();
  };
  window.addEventListener('resize', resize);
  window.addEventListener('keydown', onKey);
  window.addEventListener('online', goOnline);
  window.addEventListener('offline', goOffline);
  window.addEventListener('pagehide', hide);
  document.addEventListener('visibilitychange', vis);
  const refreshEvery = Math.max(30, (p.url_ttl || 600) - 90) * 1000;
  const timer = setInterval(() => void refreshURLs(), refreshEvery);
  if (online) void goOnline();
  if (typeof caches !== 'undefined') {
   void readMeta(caches, p.publication.id).then((meta) => {
    onDevice = !!meta && !expired(meta, Date.now() / 1000) && download?.status !== 'expired';
   }).catch(() => undefined);
  }
  if (mode === 'webtoon') requestAnimationFrame(() => scrollToPage(current, false));
  return () => {
   window.removeEventListener('resize', resize);
   window.removeEventListener('keydown', onKey);
   window.removeEventListener('online', goOnline);
   window.removeEventListener('offline', goOffline);
   window.removeEventListener('pagehide', hide);
   document.removeEventListener('visibilitychange', vis);
   clearInterval(timer);
   hide();
   voice.destroy();
  };
 });

 const chapterLabel = p.publication.label;
 const pageAlt = (pg: Page) => pg.alt || t('page.alt', { page: pg.number });
 const progressLabel = $derived(mode === 'webtoon' ? `${Math.round(scrollPct * 100)}%` : mode === 'guided' ? t('panel.of', { panel: step + 1, total: steps.length }) : atEnd ? `${total} / ${total}` : progressText(view, total));
 const dirLabel = $derived(t(`dir.${settings.direction}`));
 const downloadLabel = $derived(downloading >= 0 ? t('download.progress', { percent: downloading }) : onDevice ? t('download.offline') : t('download'));
 const bookmarkLabel = $derived(bookmarked ? t('bookmark.remove') : t('bookmark.add'));
 const showComments = $derived(p.comments && settings.panel_comments);
</script>

<header class="mv-rd__bar">
 <a class="mv-iconlink mv-rd__back" href={p.back_url} aria-label={t('back')}><i class="ti ti-arrow-left" aria-hidden="true"></i></a>
 <div class="mv-rd__titles">
  <strong class="mv-rd__series mv-truncate">{p.series.title}</strong>
  <span class="mv-rd__chapter mv-truncate">{chapterLabel}</span>
 </div>
 <div class="mv-rd__tools">
  <div class="mv-seg mv-rd__modes mv-rd-seg" role="group" aria-label={t('mode')}>
   {#each modes as m}
    <button type="button" class="mv-seg__item" aria-pressed={mode === m} onclick={() => setMode(m)}>{t(`mode.${m}`)}</button>
   {/each}
  </div>
  <span class="mv-btn mv-btn--bare mv-btn--sm is-soon mv-rd__tool" role="img" aria-label={t('translate.soon')} title={t('translate.soon')}>
   <i class="ti ti-language" aria-hidden="true"></i>
  </span>
  <button type="button" class="mv-btn mv-btn--bare mv-btn--sm mv-rd__tool" aria-pressed={!!bookmarked} aria-label={bookmarkLabel} title={bookmarkLabel} onclick={toggleBookmark}>
   <i class={`ti ti-bookmark${bookmarked ? '-filled' : ''}`} aria-hidden="true"></i>
  </button>
  <button type="button" class="mv-btn mv-btn--bare mv-btn--sm mv-rd__tool" aria-pressed={onDevice} aria-label={downloadLabel} title={downloadLabel} aria-busy={downloading >= 0} onclick={toggleDownload}>
   <i class={`ti ti-${downloading >= 0 ? 'loader-2 mv-rd__spin' : onDevice ? 'circle-check' : 'download'}`} aria-hidden="true"></i>
  </button>
  <button type="button" class="mv-btn mv-btn--bare mv-btn--sm mv-rd__tool" aria-expanded={settingsOpen} aria-controls="mv-rd-settings" aria-label={t('settings')} title={t('settings')} onclick={toggleSettings}>
   <i class="ti ti-settings" aria-hidden="true"></i>
  </button>
 </div>
</header>

{#if !online}
 <p class="mv-rd__offline" role="status"><i class="ti ti-wifi-off" aria-hidden="true"></i> {t('offline.banner')}</p>
{/if}

<!-- Clicks on the stage are the invisible tap zones; the keyboard equivalents are the window-level arrow keys and the two zone buttons. -->
<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
<main id="main" class="mv-rd__stage" data-theme={settings.background === 'paper' ? 'light' : 'dark'} data-bg={settings.background} bind:this={stageEl} bind:clientWidth={stageW} bind:clientHeight={stageH}
 onclick={onStageClick} onscroll={onScroll} tabindex="-1" aria-label={`${p.series.title} · ${chapterLabel}`}>
 <h1 class="mv-sr-only">{p.series.title} · {chapterLabel}</h1>
 {#if mode === 'guided'}
  <GuidedView {pages} {steps} index={step} dir={settings.direction} {t} onstep={guidedMove} onimgerror={refreshURLs}
   onpagemode={() => setMode(vw >= 1100 ? 'spread' : 'paged')} />
 {:else if mode === 'webtoon'}
  <div class={['mv-rd__strip', webtoon && 'mv-rd__strip--seamless']}>
   {#each pages as pg (pg.id)}
    <figure class="mv-rd__strip-page" use:registerPage={pg.number} id={`page-${pg.number}`}>
     {#if pg.src}
      <img src={pg.src} alt={pageAlt(pg)} width={pg.width} height={pg.height} loading={pg.number > current + 2 ? 'lazy' : 'eager'} decoding="async" onerror={refreshURLs} />
     {:else}
      <div class="mv-tone mv-tone--page" role="img" aria-label={t('page.missing')}></div>
     {/if}
    </figure>
   {/each}
   <ChapterEnd label={chapterLabel} {webtoon} provenance={p.provenance} prev={p.prev} next={p.next} seriesUrl={p.series_url} {t} />
  </div>
  {#if webtoon && p.chapters.length > 0}
   <nav class="mv-rd-eps mv-box" aria-labelledby="mv-rd-eps-title">
    <strong id="mv-rd-eps-title">{t('episodes')}</strong>
    {#each p.chapters.slice().reverse().slice(0, 6) as c (c.id)}
     <a class={['mv-rd-eps__row', c.current && 'is-current']} href={c.href} aria-current={c.current ? 'page' : undefined}>
      <span class="mv-truncate">{c.label}</span>
      {#if c.current}
       <span class="mv-badge mv-badge--accent">{t('episode.reading')}</span>
      {:else if c.completed}
       <i class="ti ti-circle-check mv-rd-eps__done" aria-hidden="true"></i><span class="mv-sr-only">{t('episode.read')}</span>
      {/if}
     </a>
    {/each}
   </nav>
  {/if}
 {:else}
  {#key turnKey}
   <div class={['mv-rd__spread', view.length > 1 && 'is-double']} aria-live="polite">
    {#if atEnd}
     <ChapterEnd label={chapterLabel} {webtoon} provenance={p.provenance} prev={p.prev} next={p.next} seriesUrl={p.series_url} {t} />
    {:else}
     {#each viewOrder(view, settings.direction) as n, i (n)}
      {@const pg = pageOf(n)}
      {#if i > 0}<span class="mv-rd__gutter" aria-hidden="true"></span>{/if}
      {#if pg?.src}
       <img class="mv-rd__page" src={pg.src} alt={pageAlt(pg)} width={pg.width} height={pg.height} style={pageStyle(pg)} draggable="false" onerror={refreshURLs} />
      {:else if pg}
       <div class="mv-tone mv-tone--page mv-rd__page" style={pageStyle(pg)} role="img" aria-label={t('page.missing')}></div>
      {/if}
     {/each}
    {/if}
   </div>
  {/key}
  <button type="button" class="mv-rd__zone mv-rd__zone--left" aria-label={settings.direction === 'rtl' ? t('zone.next') : t('zone.prev')}
   onclick={(e) => { e.stopPropagation(); go(settings.direction === 'rtl' ? 'next' : 'prev'); }} tabindex={settings.tap_zones ? 0 : -1} hidden={!settings.tap_zones}></button>
  <button type="button" class="mv-rd__zone mv-rd__zone--right" aria-label={settings.direction === 'rtl' ? t('zone.prev') : t('zone.next')}
   onclick={(e) => { e.stopPropagation(); go(settings.direction === 'rtl' ? 'prev' : 'next'); }} tabindex={settings.tap_zones ? 0 : -1} hidden={!settings.tap_zones}></button>
 {/if}
</main>

{#if showComments && !commentsOpen}
 <button type="button" class="mv-btn mv-btn--secondary mv-btn--sm mv-rd__comments-btn" onclick={() => { commentsOpen = true; settingsOpen = false; }}>
  <i class="ti ti-message-circle" aria-hidden="true"></i>{t('comments')}
 </button>
{/if}

{#if commentsOpen && showComments}
 <Comments api={p.api.comments} seriesId={p.series.id} publicationId={p.publication.id} page={readingPage} panel={readingPanel}
  signedIn={p.signed_in} loginUrl={p.login_url} {t} onclose={() => (commentsOpen = false)} />
{:else if settingsOpen}
 <SettingsPanel {settings} {t} {webtoon} langs={p.langs} signedIn={p.signed_in} commentsReady={p.comments} {voiceReady} {voiceName}
  onchange={change} onclose={toggleSettings} />
{/if}

<footer class={['mv-rd__progress', mode === 'webtoon' && 'mv-rd__progress--strip']}>
 <span class="mv-rd__count">{progressLabel}</span>
 <span class="mv-rd__track" dir={settings.direction === 'rtl' && mode !== 'webtoon' ? 'rtl' : 'ltr'} role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow={Math.round(fraction * 100)} aria-label={progressLabel}>
  <span class="mv-rd__fill" style={`width:${Math.round(fraction * 1000) / 10}%`}></span>
 </span>
 {#if mode === 'webtoon'}
  {#if p.next}
   <a class="mv-rd__next" href={p.next.href}>{webtoon ? t('next_episode') : t('next_chapter')}</a>
  {:else}
   <span>{dirLabel}</span>
  {/if}
 {:else}
  <span>{dirLabel}</span>
 {/if}
</footer>
