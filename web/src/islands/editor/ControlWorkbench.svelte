<script lang="ts">
 import {scorePercent} from './score';
 import {onMount, onDestroy, untrack} from 'svelte';
 import Button from './Button.svelte';
 import SketchCanvas from './SketchCanvas.svelte';
 import {request, APIError} from './api';
 import {alphaBounds, changedPanels, jobMessageKey, driftEvidence, firstName, historyRows, imageSize, lockState, quoteChip, sketchFileOK, sketchSizeOK} from './control';
 import type {Page, Version, Job, LockStatus, DriftPanel, RepairTarget, Point} from './types';

 type T = (key: string, args?: Record<string, string | number>) => string;
 let {kind, page, api, lang, chapter, characterID = '', characters = [], writable = false, blocked = '', activeJob = null, notice = '', conflict = false, t, onClose, onRestore, onJob, onDirty, onReload = () => {}, onDismiss = () => {}, onCharacter = () => {}}: {
  kind: 'history' | 'sketch' | 'lockfix'; page: Page; api: string; lang: string; chapter: number; characterID?: string; characters?: {id: string; name: string}[];
  writable?: boolean; blocked?: string; activeJob?: Job | null; notice?: string; conflict?: boolean; t: T;
  onClose: () => void; onRestore: (v: Version, undo?: boolean) => void; onJob: (j: Job) => void; onDirty: (dirty: boolean) => void;
  onReload?: () => void; onDismiss?: () => void; onCharacter?: (id: string) => void;
 } = $props();

 /* ---- shared ---- */
 let loading = $state(false), busy = $state(false), error = $state(''), retry = $state<(() => void) | null>(null);
 let dead = false, serial = 0, abort: AbortController | null = null, actionAbort: AbortController | null = null, handled = '';
 const running = $derived(activeJob?.state === 'queued' || activeJob?.state === 'running');
 const base = $derived(`${api}/pages/${page.id}`);
 const can = $derived(writable && !blocked);
 const decimal = $derived(new Intl.NumberFormat(lang, {minimumFractionDigits: 2, maximumFractionDigits: 2}));
 function fail(e: unknown, again: (() => void) | null = null) {
  if (dead || e instanceof DOMException && e.name === 'AbortError') return;
  error = e instanceof APIError ? e.message : t('error.network');
  retry = again;
 }
 function clearError() { error = ''; retry = null; }

 /* ---- D10 history ---- */
 let versions = $state.raw<Version[]>([]), selected = $state<Version | null>(null);
 let compare = $state.raw<{id: string; outlined: string[]; count: number; width: number; height: number; panels: {id: string; points: Point[]}[]} | null>(null), compareSeq = 0, compareAbort: AbortController | null = null;
 const rows = $derived(historyRows(versions, page.version));
 const changed = $derived(compare && compare.id === selected?.id ? compare : null);
 async function loadHistory() {
  const n = ++serial; abort?.abort(); abort = new AbortController(); loading = true; clearError();
  try {
   const out = await request<{versions: Version[]}>(`${base}/history`, 'GET', undefined, AbortSignal.any([abort.signal, AbortSignal.timeout(15000)]));
   if (dead || n !== serial) return;
   versions = out.versions;
   selected = versions.find(v => v.id === selected?.id && v.number !== page.version) || versions.find(v => v.number < page.version) || versions[0] || null;
  } catch (e) { fail(e, () => void loadHistory()); } finally { if (n === serial) loading = false; }
 }
 // The compared snapshot marks the panels that differ from the saved page.
 async function loadCompare(v: Version) {
  const n = ++compareSeq; compareAbort?.abort(); compareAbort = new AbortController();
  try {
   const out = await request<{page: Page}>(`${base}/versions/${v.id}`, 'GET', undefined, AbortSignal.any([compareAbort.signal, AbortSignal.timeout(15000)]));
   if (dead || n !== compareSeq) return;
   const diff = changedPanels(page, out.page);
   compare = {id: v.id, ...diff, width: out.page.width, height: out.page.height, panels: out.page.panels.filter(p => diff.outlined.includes(p.id)).map(p => ({id: p.id, points: p.points}))};
  } catch { if (n === compareSeq) compare = null; }
 }
 $effect(() => { if (kind === 'history') { void page.version; untrack(() => void loadHistory()); } });
 $effect(() => { const v = selected; void page.version; if (kind === 'history' && v) untrack(() => void loadCompare(v)); });
 const historyCaption = $derived(!selected ? t('history.empty') : !changed ? t('control.snapshot', {number: selected.number}) : changed.count ? t('control.snapshot_changes', {number: selected.number, count: changed.count}) : t('control.snapshot_same', {number: selected.number}));

 /* ---- D4 sketch ---- */
 let source = $state<'upload' | 'draw'>('upload'), sketchFrom = $state<'upload' | 'draw'>('upload'), sketch = $state<Blob | null>(null), sourceURL = $state(''), strength = $state(.62), finish = $state('screentone');
 let file = $state<HTMLInputElement>(), afterURL = $state('');
 const ratio = $derived(page.width / Math.max(1, page.height));
 function setSketch(blob: Blob | null, from: 'upload' | 'draw' = 'draw') { if (sourceURL) URL.revokeObjectURL(sourceURL); sketch = blob; sketchFrom = from; sourceURL = blob ? URL.createObjectURL(blob) : ''; afterURL = ''; onDirty(!!blob); }
 // The visible source is what gets inked: a fresh drawing surface replaces an upload.
 function drawHere() { if (source === 'draw') return; source = 'draw'; if (sketch) setSketch(null); clearError(); }
 // Client checks mirror the server's limits so the creator gets the specific
 // reason before uploading; the server still validates every file.
 async function pickFile(e: Event) {
  const input = e.currentTarget as HTMLInputElement, f = input.files?.[0];
  if (!f) return;
  const size = sketchFileOK(f.type, f.size) ? await imageSize(f) : null;
  if (dead) return;
  if (!size || !sketchSizeOK(size.width, size.height)) { input.value = ''; error = t('control.sketch_invalid'); retry = null; return; }
  clearError(); source = 'upload'; setSketch(f, 'upload');
 }
 async function ink() {
  if (!sketch || !can || !quote || running || busy) return;
  busy = true; clearError(); actionAbort?.abort(); actionAbort = new AbortController();
  const signal = AbortSignal.any([actionAbort.signal, AbortSignal.timeout(30000)]);
  try {
   const form = new FormData(); form.append('file', sketch, 'sketch.png'); form.append('base_version', String(page.version));
   const uploaded = await request<{id: string}>(`${base}/sketch`, 'POST', form, signal);
   if (dead) return;
   const out = await request<{job: Job}>(`${base}/regenerate`, 'POST', {base_version: page.version, sketch_asset_id: uploaded.id, strength, finish}, signal);
   if (dead) return;
   onDirty(false); onJob(out.job);
  } catch (e) {
   if (e instanceof APIError && (e.status === 422 || e.status === 413)) { if (!dead) { error = t('control.sketch_invalid'); retry = null; } }
   else fail(e);
  } finally { if (!dead) busy = false; }
 }

 /* ---- D12 character lock ---- */
 let character = $state(untrack(() => characterID)), missing = $state(false);
 let status = $state<LockStatus | null>(null), name = $state(''), refs = $state.raw<{id: string; url: string; kind: string}[]>([]), chosen = $state<string[]>([]), drift = $state<DriftPanel | null>(null), afterScore = $state<number | undefined>(undefined);
 let region = $state<{w: number; h: number; box: {x: number; y: number; w: number; h: number}} | null>(null);
 const choices = $derived(status?.drift.filter(d => chosen.includes(d.layer_id)) || []);
 const lock = $derived(lockState(status));
 const evidence = $derived(driftEvidence(drift, status?.threshold || .9));
 const short = $derived(firstName(name));
 async function loadLock(keep = false) {
  if (!character) { missing = false; status = null; return; }
  const n = ++serial; abort?.abort(); abort = new AbortController(); loading = true;
  try {
   const out = await request<{status: LockStatus; name: string; references: typeof refs}>(`${api}/lock/${character}`, 'GET', undefined, AbortSignal.any([abort.signal, AbortSignal.timeout(15000)]));
   if (dead || n !== serial) return;
   const before = chosen;
   status = out.status; name = out.name; refs = out.references; missing = false;
   if (retry) clearError();
   const open = out.status.drift.filter(d => !d.locked);
   // A refresh keeps the creator's selection where those layers still drift.
   chosen = keep ? open.filter(d => before.includes(d.layer_id)).slice(0, 24).map(d => d.layer_id) : open.slice(0, 24).map(d => d.layer_id);
   if (!keep) drift = out.status.drift[0] || null;
  } catch (e) {
   if (n !== serial) return;
   if (e instanceof APIError && e.status === 404) { missing = true; status = null; name = ''; refs = []; chosen = []; drift = null; clearError(); }
   else fail(e, () => void loadLock(keep));
  } finally { if (n === serial) loading = false; }
 }
 function chooseCharacter(id: string) { character = id; drift = null; afterURL = ''; afterScore = undefined; status = null; onCharacter(id); void loadLock(); }
 async function repair() {
  if (!choices.length || !can || !quote || running || busy) return;
  busy = true; clearError();
  try {
   const targets: RepairTarget[] = choices.map(d => ({page_id: d.page_id, panel_id: d.panel_id, layer_id: d.layer_id, version: d.version}));
   const out = await request<{job: Job}>(`${api}/lock/repair`, 'POST', {character_id: character, targets}, AbortSignal.timeout(30000));
   if (!dead) onJob(out.job);
  } catch (e) { fail(e); } finally { if (!dead) busy = false; }
 }
 function toggle(d: DriftPanel) { if (d.locked) return; if (chosen.includes(d.layer_id)) chosen = chosen.filter(id => id !== d.layer_id); else if (chosen.length < 24) chosen = [...chosen, d.layer_id]; }
 function pick(d: DriftPanel) { drift = d; afterURL = ''; afterScore = undefined; }
 // The before proof outlines the character layer's visible pixels; no face or
 // part region is inferred.
 async function measure(url: string) {
  region = null;
  if (!url || typeof document === 'undefined') return;
  try {
   const img = new Image(); img.decoding = 'async'; img.src = url; await img.decode();
   if (dead || drift?.url !== url) return;
   const scale = Math.min(1, 256 / Math.max(img.naturalWidth, img.naturalHeight)), w = Math.max(1, Math.round(img.naturalWidth * scale)), h = Math.max(1, Math.round(img.naturalHeight * scale));
   const canvas = document.createElement('canvas'); canvas.width = w; canvas.height = h;
   const ctx = canvas.getContext('2d', {willReadFrequently: true}); if (!ctx) return;
   ctx.drawImage(img, 0, 0, w, h);
   const box = alphaBounds(ctx.getImageData(0, 0, w, h).data, w, h);
   if (box && drift?.url === url) region = {w: img.naturalWidth, h: img.naturalHeight, box};
  } catch { region = null; }
 }
 $effect(() => { const url = kind === 'lockfix' ? drift?.url || '' : ''; untrack(() => void measure(url)); });

 /* ---- estimates (D4 / D12) ---- */
 interface Quote {quote: {provider_id: string; model_id: string; placeholder: boolean; estimate: {usd?: number; gpu_seconds?: number}; layers: number}; model_name: string}
 let quote = $state<Quote | null>(null), quoting = $state(false), quoteFailed = $state(false), quoteSeq = 0, quoteAbort: AbortController | null = null;
 async function refreshQuote() {
  if (kind === 'history') return;
  const n = ++quoteSeq; quoteAbort?.abort(); quote = null; quoteFailed = false;
  if (!can || kind === 'lockfix' && !choices.length) { quoting = false; return; }
  quoteAbort = new AbortController(); quoting = true;
  try {
   const signal = AbortSignal.any([quoteAbort.signal, AbortSignal.timeout(15000)]);
   const out = kind === 'sketch'
    ? await request<Quote>(`${base}/quote?kind=sketch`, 'GET', undefined, signal)
    : await request<Quote>(`${api}/lock/quote`, 'POST', {character_id: character, targets: choices.map(d => ({page_id: d.page_id, panel_id: d.panel_id, layer_id: d.layer_id, version: d.version}))}, signal);
   if (dead || n !== quoteSeq) return;
   quote = out;
   if (retry) clearError();
  } catch (e) {
   if (n !== quoteSeq) return;
   if (!(e instanceof DOMException && e.name === 'AbortError')) quoteFailed = true;
   fail(e, kind === 'lockfix' ? () => void loadLock(true) : () => void refreshQuote());
  } finally { if (n === quoteSeq) quoting = false; }
 }
 // D4 re-quotes for the saved page revision. D12 re-quotes only for the
 // selected targets' own revisions, which a finished job refreshes first.
 const quoteKey = $derived(kind === 'sketch' ? `${page.id}:${page.version}` : choices.map(d => `${d.layer_id}:${d.version}`).join(','));
 $effect(() => { void [kind, quoteKey, can]; untrack(() => void refreshQuote()); });
 const chip = $derived(quoteChip({blocked, needsSelection: kind === 'lockfix' && !choices.length, loading: quoting, failed: quoteFailed, ready: !!quote}));
 const money = (usd: number) => new Intl.NumberFormat(lang, {style: 'currency', currency: 'USD', minimumFractionDigits: 6, maximumFractionDigits: 6}).format(usd);
 const quoteText = $derived(chip === 'blocked' ? t('control.quote_blocked') : chip === 'select' ? t('control.quote_select') : chip === 'loading' ? t('control.quote_loading') : chip === 'failed' || !quote ? t('control.quote_failed')
  : quote.quote.placeholder ? t('control.quote_placeholder') : (quote.quote.estimate.usd || 0) > 0 ? t('control.quote', {route: quote.model_name, amount: money(quote.quote.estimate.usd || 0)}) : t('control.quote_local', {route: quote.model_name, seconds: Math.ceil(quote.quote.estimate.gpu_seconds || 0)}));
 const quoteTitle = $derived(chip === 'failed' ? t('control.quote_unavailable') : chip === 'blocked' ? blocked : quoteText);

 /* ---- jobs ---- */
 async function finishJob(j: Job) {
  if (kind === 'sketch') { afterURL = `${base}/proof?v=${page.version}&job=${j.id}`; return; }
  if (kind !== 'lockfix') return;
  // Any finished job can change target revisions: reload the evidence (and
  // with it the selected targets) before anything is quoted again.
  const target = drift;
  await loadLock(true);
  if (dead || !target || j.type !== 'character.repair') return;
  try {
   const out = await request<{page: Page}>(`${api}/pages/${target.page_id}`, 'GET', undefined, AbortSignal.timeout(15000));
   if (dead || drift?.layer_id !== target.layer_id) return;
   const layer = out.page.panels.find(p => p.id === target.panel_id)?.layers.find(l => l.id === target.layer_id);
   afterURL = layer?.asset?.url || ''; afterScore = layer?.lock_score;
  } catch (e) { fail(e); }
 }
 $effect(() => { const j = activeJob; if (j?.state === 'done' && j.id !== handled) { handled = j.id; untrack(() => void finishJob(j)); } });

 onMount(() => { if (kind === 'lockfix') void loadLock(); });
 onDestroy(() => { dead = true; serial++; quoteSeq++; compareSeq++; abort?.abort(); quoteAbort?.abort(); actionAbort?.abort(); compareAbort?.abort(); if (sourceURL) URL.revokeObjectURL(sourceURL); onDirty(false); });

 const title = $derived(kind === 'history' ? t('control.history_inspector', {number: page.number}) : kind === 'sketch' ? t('control.sketch_title') : name ? t('control.lockfix_title', {name}) : t('control.lockfix_heading'));
 const subtitle = $derived(kind === 'history' ? t('control.branch_count', {count: 1}) : kind === 'sketch' ? t('control.sketch_subtitle', {number: page.number})
  : !character || missing ? t('control.choose_hint') : lock === 'unassessed' ? t('control.lock_unassessed', {count: status?.unscored ?? 0, chapter}) : lock === 'loading' ? '' : t('control.lock_subtitle', {count: status?.drift_total ?? status?.drift.length ?? 0, chapter, threshold: Math.round((status?.threshold || .9) * 100)}));
 const beforeCaption = $derived(drift ? t('control.drift_before', {panel: `${drift.page_number}.${drift.panel_number}`, score: scorePercent(drift.score)}) : !character || missing || lock === 'loading' ? t('control.before') : lock === 'unassessed' ? t('control.before_unassessed') : t('control.before_clear'));
 const evidenceTag = $derived(evidence && short ? t(`control.evidence_${evidence}`, {name: short}) : '');
 const refLabel = (k: string) => t(`control.ref_${k}`) === `control.ref_${k}` ? t('control.references') : t(`control.ref_${k}`);
 const actionLabel = (v: Version) => t(`history.action.${v.action}`) === `history.action.${v.action}` ? t('control.saved_change') : t(`history.action.${v.action}`);
 const versionIcon = (v: Version) => v.actor_kind === 'agent' ? 'robot' : v.action === 'lock' ? 'lock' : ['generate_layer', 'generate_page', 'character_repair'].includes(v.action) ? 'sparkles' : 'history';
 const stamp = (at: string) => `${new Intl.DateTimeFormat(lang, {dateStyle: 'short', timeStyle: 'short', timeZone: 'UTC'}).format(new Date(at))} UTC`;
</script>

<section class="mv-ed__stage mv-control__stage" aria-label={title}>
 {#if notice}
  <div class="mv-notice mv-notice--error" role="alert"><i class="ti ti-alert-triangle" aria-hidden="true"></i><p>{notice}</p>{#if conflict}<Button small onclick={onReload}>{t('reload')}</Button>{:else}<button type="button" class="mv-close" aria-label={t('dismiss')} onclick={onDismiss}><i class="ti ti-x" aria-hidden="true"></i></button>{/if}</div>
 {/if}
 {#if error}
  <div class="mv-notice mv-notice--error" role="alert"><i class="ti ti-alert-triangle" aria-hidden="true"></i><p>{error}</p>{#if retry}<Button small onclick={retry}>{t('refresh')}</Button>{:else}<button type="button" class="mv-close" aria-label={t('dismiss')} onclick={clearError}><i class="ti ti-x" aria-hidden="true"></i></button>{/if}</div>
 {/if}
 <div class={`mv-control__pair mv-control__pair--${kind}`}>
  {#if kind === 'history'}
   <figure>
    <figcaption>{t('control.main_now')}</figcaption>
    <img src={`${base}/proof?v=${page.version}`} alt={t('control.current_page', {number: page.number})}/>
   </figure>
   <figure class="mv-control__compared">
    <figcaption>{historyCaption}</figcaption>
    {#if selected}
     <span class="mv-control__proof">
      <img src={`${base}/versions/${selected.id}?image=1`} alt={t('control.snapshot', {number: selected.number})}/>
      {#if changed?.panels.length}
       <svg class="mv-control__changes" viewBox={`0 0 ${changed.width} ${changed.height}`} preserveAspectRatio="xMidYMid meet" aria-hidden="true">
        {#each changed.panels as p (p.id)}<polygon points={p.points.map(q => `${q.x},${q.y}`).join(' ')} vector-effect="non-scaling-stroke"/>{/each}
       </svg>
      {/if}
     </span>
    {:else}<div class="mv-control__placeholder"><i class="ti ti-history" aria-hidden="true"></i><p>{t('history.empty')}</p></div>{/if}
   </figure>
  {:else if kind === 'sketch'}
   <figure>
    <figcaption>{t((sketch ? sketchFrom : source) === 'draw' ? 'control.your_sketch_drawn' : 'control.your_sketch_uploaded')}</figcaption>
    <div class="mv-control__frame" style:--mv-ratio={ratio}>
     {#if source === 'draw'}<SketchCanvas {ratio} {t} disabled={busy || running || !can} onChange={(blob) => setSketch(blob, 'draw')}/>
     {:else if sourceURL}<img src={sourceURL} alt={t(sketchFrom === 'draw' ? 'control.your_sketch_drawn' : 'control.your_sketch_uploaded')}/>
     {:else}<div class="mv-control__placeholder"><i class="ti ti-upload" aria-hidden="true"></i><p>{t('control.upload_hint')}</p><Button onclick={() => file?.click()} disabled={!can}>{t('control.upload')}</Button></div>{/if}
     {#if source === 'draw' || sourceURL}<span class="mv-control__tag" aria-hidden="true">{t('control.sketch_tag', {number: page.number})}</span>{/if}
    </div>
   </figure>
   <i class="ti ti-arrow-right mv-control__arrow" aria-hidden="true"></i>
   <figure>
    <figcaption>{t('control.inked', {strength: decimal.format(strength)})}</figcaption>
    <div class="mv-control__frame" style:--mv-ratio={ratio}>
     {#if afterURL}<img src={afterURL} alt={t('control.inked_result')}/>
     {:else}<div class="mv-control__placeholder"><i class={`ti ti-${running ? 'loader-2' : 'brush'}`} aria-hidden="true"></i><p>{t(running ? 'control.generating' : 'control.result_pending')}</p></div>{/if}
    </div>
   </figure>
  {:else}
   <figure class="mv-control__before" class:mv-control__before--drift={!!drift}>
    <figcaption>{beforeCaption}</figcaption>
    {#if drift?.url}
     <span class="mv-control__proof">
      <img class="mv-control__character-art" src={drift.url} alt={t('control.character_before', {name})}/>
      {#if region}
       <svg class="mv-control__region" viewBox={`0 0 ${region.w} ${region.h}`} preserveAspectRatio="xMidYMid meet" aria-hidden="true"><rect x={region.box.x * region.w} y={region.box.y * region.h} width={region.box.w * region.w} height={region.box.h * region.h} vector-effect="non-scaling-stroke"/></svg>
      {/if}
      {#if evidenceTag}<span class="mv-control__tag">{evidenceTag}</span>{/if}
     </span>
    {:else}
     <div class="mv-control__placeholder"><i class={`ti ti-${!character || missing ? 'user-question' : 'lock'}`} aria-hidden="true"></i><p>{missing ? t('control.character_missing') : !character ? t('control.choose_hint') : t(lock === 'unassessed' ? 'control.no_assessment' : 'control.no_drift')}</p></div>
    {/if}
   </figure>
   <i class="ti ti-arrow-right mv-control__arrow" aria-hidden="true"></i>
   <figure class="mv-control__after" class:mv-control__after--pass={!!afterURL && afterScore !== undefined && afterScore >= (status?.threshold || .9)} class:mv-control__after--fail={!!afterURL && afterScore !== undefined && afterScore < (status?.threshold || .9)}>
    <figcaption>{t('control.after_fix', {score: afterScore === undefined ? t('control.unscored') : scorePercent(afterScore) + '%'})}</figcaption>
    {#if afterURL}
     <span class="mv-control__proof">
      <img class="mv-control__character-art" class:mv-control__pass={afterScore !== undefined && afterScore >= (status?.threshold || .9)} class:mv-control__fail={afterScore !== undefined && afterScore < (status?.threshold || .9)} src={afterURL} alt={t('control.character_after', {name})}/>
      {#if short}<span class="mv-control__tag">{t('control.only_layer', {name: short})}</span>{/if}
     </span>
    {:else}<div class="mv-control__placeholder"><i class={`ti ti-${running ? 'loader-2' : 'brush'}`} aria-hidden="true"></i><p>{t(running ? 'control.generating' : 'control.result_pending')}</p></div>{/if}
   </figure>
  {/if}
 </div>
</section>

<aside class="mv-ed__right mv-control__inspector">
 <div class="mv-ed__section-head mv-ed__inspector-head"><div><strong>{title}</strong>{#if subtitle}<small title={subtitle}>{subtitle}</small>{/if}</div><button type="button" class="mv-iconlink" onclick={onClose} aria-label={t('close')}><i class="ti ti-x" aria-hidden="true"></i></button></div>
 <div class="mv-ed__inspector-scroll">
  {#if loading}<p class="mv-ed__help mv-ed__pad" role="status">{t('control.loading')}</p>{/if}
  {#if kind === 'history'}
   <section class="mv-ed__property"><strong>{t('control.branches')}</strong><div class="mv-control__branch"><span><i class="ti ti-git-branch" aria-hidden="true"></i> main</span><span class="mv-ed__help">{t('control.current')}</span></div></section>
   <div class="mv-control__versions">
    {#each rows as row (row.version.id)}
     {@const v = row.version}
     <article class="mv-ed__version mv-control__version" data-version={v.number} class:selected={selected?.id === v.id}>
      <i class={`ti ti-${versionIcon(v)}`} aria-hidden="true"></i>
      <button type="button" class="mv-control__version-pick" aria-pressed={selected?.id === v.id} onclick={() => selected = v}>
       <strong>{actionLabel(v)}</strong>
       <span>{t('history.version', {number: v.number})} · <time datetime={v.created_at}>{stamp(v.created_at)}</time></span>
      </button>
      {#if row.action === 'undo'}<button type="button" class="mv-control__version-action" disabled={!can || busy || running} onclick={() => row.target && onRestore(row.target, true)}>{t('undo')}</button>
      {:else}<button type="button" class="mv-control__version-action" disabled={!can || busy || running || row.action === 'current'} onclick={() => onRestore(v)}>{t('control.restore')}</button>{/if}
     </article>
    {:else}{#if !loading}<p class="mv-ed__help mv-ed__pad">{t('history.empty')}</p>{/if}{/each}
   </div>
  {:else if kind === 'sketch'}
   <section class="mv-ed__property">
    <strong>{t('control.source')}</strong>
    <div class="mv-seg mv-seg--full mv-control__source">
     <button type="button" class="mv-seg__item" aria-pressed={source === 'upload'} disabled={busy || running || !can} onclick={() => { source = 'upload'; file?.click(); }}>{t('control.upload')}</button>
     <button type="button" class="mv-seg__item" aria-pressed={source === 'draw'} disabled={busy || running || !can} onclick={drawHere}>{t('control.draw')}</button>
     <button type="button" class="mv-seg__item" disabled title={t('soon')}>{t('control.camera')}<span class="mv-soon">{t('soon')}</span></button>
    </div>
    <input class="mv-sr-only" bind:this={file} type="file" accept="image/png,image/jpeg,image/webp" aria-label={t('control.upload')} onchange={pickFile}/>
    <p class="mv-ed__help">{t('control.sketch_limits')}</p>
   </section>
   <section class="mv-ed__property">
    <label for="sketch-strength">{t('control.strength')}</label>
    <div class="mv-control__range-labels"><span>{t('control.loose')}</span><output for="sketch-strength">{decimal.format(strength)}</output><span>{t('control.exact')}</span></div>
    <input id="sketch-strength" class="mv-range mv-ed__thin-range" type="range" min="0.05" max="1" step="0.01" bind:value={strength} style:--mv-p={`${(strength - .05) / .95 * 100}%`} disabled={busy || running || !can}/>
   </section>
   <section class="mv-ed__property">
    <strong>{t('control.detect')} <span class="mv-soon">{t('soon')}</span></strong>
    <label class="mv-check"><input type="checkbox" checked disabled/><span class="mv-check__box" aria-hidden="true"><i class="ti ti-check"></i></span><span>{t('control.kept_panels', {count: page.panels.length})}</span></label>
    <label class="mv-check"><input type="checkbox" checked disabled/><span class="mv-check__box" aria-hidden="true"><i class="ti ti-check"></i></span><span>{t('control.kept_characters')}</span></label>
    <label class="mv-check"><input type="checkbox" checked disabled/><span class="mv-check__box" aria-hidden="true"><i class="ti ti-check"></i></span><span>{t('control.kept_lettering')}</span></label>
    <p class="mv-ed__help">{t('control.detect_hint')}</p>
   </section>
   <section class="mv-ed__property">
    <strong>{t('control.finish')}</strong>
    <div class="mv-seg mv-seg--full">{#each ['screentone', 'grey', 'colour'] as option}<button type="button" class="mv-seg__item" aria-pressed={finish === option} disabled={busy || running || !can} onclick={() => finish = option}>{t(`control.${option}`)}</button>{/each}</div>
   </section>
  {:else if !character || missing}
   <section class="mv-ed__property">
    <label for="lock-character">{t('control.choose_character')}</label>
    <span class="mv-select-wrap"><select id="lock-character" class="mv-select" value="" onchange={(e) => { const id = e.currentTarget.value; if (id) chooseCharacter(id); }}><option value="">{t('control.choose_character_short')}</option>{#each characters as c (c.id)}<option value={c.id}>{c.name}</option>{/each}</select><i class="ti ti-chevron-down" aria-hidden="true"></i></span>
    <p class="mv-ed__help">{missing ? t('control.character_missing') : t('control.choose_hint')}</p>
   </section>
  {:else}
   <section class="mv-ed__property">
    <strong>{t('control.references')}</strong>
    <div class="mv-control__refs">{#each refs as ref (ref.id)}<figure><img src={ref.url} alt={refLabel(ref.kind)}/><figcaption>{refLabel(ref.kind)}</figcaption></figure>{/each}</div>
   </section>
   <section class="mv-ed__property">
    <strong>{t('control.drifted')}</strong>
    {#each status?.drift || [] as d (d.layer_id)}
     <div class="mv-control__drift" class:selected={drift?.layer_id === d.layer_id}>
      <label class="mv-check"><input type="checkbox" checked={chosen.includes(d.layer_id)} disabled={d.locked || busy || running || !can || !chosen.includes(d.layer_id) && chosen.length >= 24} onchange={() => toggle(d)}/><span class="mv-check__box" aria-hidden="true"><i class="ti ti-check"></i></span><span class="mv-sr-only">{t('control.select_panel', {panel: `${d.page_number}.${d.panel_number}`})}</span></label>
      <button type="button" class="mv-control__drift-pick" aria-pressed={drift?.layer_id === d.layer_id} onclick={() => pick(d)}>{#if d.url}<img src={d.url} alt=""/>{/if}<span>{d.page_number}.{d.panel_number}</span><span class="mv-control__score">{scorePercent(d.score)}%{#if d.locked} · {t('locked')}{/if}</span></button>
     </div>
    {:else}{#if !loading && status}<p class="mv-ed__help">{t(lock === 'unassessed' ? 'control.no_assessment' : 'control.no_drift')}</p>{/if}{/each}
    <p class="mv-ed__help">{t('control.selection_limit')}</p>
    {#if (status?.drift_total || 0) > (status?.drift.length || 0)}<p class="mv-ed__help">{t('control.first_drift_page')}</p>{/if}
   </section>
   <section class="mv-ed__property"><strong>{t('control.what_changes')}</strong><p class="mv-ed__help">{t('control.repair_scope', {name})}</p></section>
  {/if}
 </div>
 <div class="mv-ed__inspector-actions mv-control__actions" class:mv-control__actions--history={kind === 'history'}>
  {#if running}<div role="status"><p>{t('control.generating')} · {Math.round((activeJob?.progress || 0) * 100)}%</p><progress class="mv-ed__progress" value={activeJob?.progress || 0} max="1" aria-label={t('progress')}></progress></div>{/if}
  {#if activeJob?.state === 'failed' || activeJob?.state === 'canceled'}<p class="mv-notice mv-notice--error" role="alert">{t(jobMessageKey(activeJob))}</p>{/if}
  {#if kind === 'history'}
   <Button full icon="git-branch" disabled>{t('control.new_branch')}<span class="mv-soon">{t('soon')}</span></Button>
   <Button full icon="git-merge" disabled>{t('control.merge_branch')}<span class="mv-soon">{t('soon')}</span></Button>
  {:else}
   <div class="mv-route" title={quoteTitle}><i class="ti ti-route" aria-hidden="true"></i><strong>{t('auto')}</strong><span class="mv-route__pick">→ {quoteText}</span></div>
   <Button full variant="primary" icon={kind === 'sketch' ? 'brush' : 'wand'} busy={busy} disabled={!can || !quote || running || busy || kind === 'sketch' && !sketch || kind === 'lockfix' && !choices.length} onclick={() => kind === 'sketch' ? void ink() : void repair()}>{kind === 'sketch' ? t('control.ink_page') : t('control.fix_panels', {count: choices.length})}</Button>
   {#if blocked}<p class="mv-ed__help mv-control__blocked" role="status"><i class="ti ti-lock" aria-hidden="true"></i>{blocked}</p>
   {:else if kind === 'sketch' && quote}<p class="mv-ed__help">{t('control.quote_scope', {count: quote.quote.layers})}</p>{/if}
  {/if}
  {#if activeJob}<a class="mv-btn mv-btn--quiet mv-btn--sm" href={`/queue?job=${activeJob.id}`}>{t('queue.open')}</a>{/if}
 </div>
</aside>
