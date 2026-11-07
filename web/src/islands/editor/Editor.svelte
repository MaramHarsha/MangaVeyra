<script lang="ts">
 import {onMount, onDestroy, tick, untrack} from 'svelte';
 import Button from './Button.svelte';
 import CanvasStage from './CanvasStage.svelte';
 import RegionMask from './RegionMask.svelte';
 import {request, APIError} from './api';
 import {showToast} from '../../lib/toast';
 import {
  Autosave, DraftHistory, FONT_FAMILIES, FONT_KEYS, applyLayout, avatarTone, bounds, bubbleWritable, cameraOption, canReorderBubble,
  canUseLayout, canonical, clone, defaultFont, initials, jobSeconds, layerLabel, layoutPoints, moveBubble, moveBubbleTo, movePanel,
  humanize, newBubble, newID, panelLabel as labelFor, plural, ptToPx, pxToPt, reorderBubble, rubyFor, savedAgo, setBubbleKind, shortTitle, shotOption, verticalAllowed,
  type SaveResult, type SaveState,
 } from './document';
 import type {Beat, Bubble, Candidate, Compute, Job, Layer, Layout, Lettering, Page, PageResponse, Panel, Props, Rect, Version} from './types';

 let {initial: bootstrap, mode: initialMode = '', tool: initialTool = '', panel: initialTray = '', job: initialJob = ''}: Props = $props();
 const initial = untrack(() => bootstrap), bootMode = untrack(() => initialMode), bootTool = untrack(() => initialTool), bootTray = untrack(() => initialTray), bootJob = untrack(() => initialJob);
 // Messages with a {count} use the catalogue's plural forms (".one"/".other").
 const t = (key: string, args: Record<string, string | number> = {}) => Object.entries(args).reduce((s, [k, v]) => s.replaceAll(`{${k}}`, String(v)), typeof args.count === 'number' && !(key in initial.messages) ? plural(initial.messages, key, args.count, initial.lang) : initial.messages[key] || key);
 const lang = initial.project.language;
 const characters = initial.characters;
 const KINDS: Bubble['kind'][] = ['speech', 'thought', 'shout', 'whisper', 'caption', 'sfx'];
 const KIND_ICONS: Record<Bubble['kind'], string> = {speech: 'message-circle', thought: 'cloud', shout: 'bolt', whisper: 'message-dots', caption: 'square', sfx: 'letter-case'};
 const ZOOMS = ['50', '75', '100', '150', '200'];
 const MASK_TOOLS: [string, string][] = [['brush', 'brush'], ['lasso', 'lasso'], ['box', 'square']];
 const number = new Intl.NumberFormat(initial.lang, {maximumFractionDigits: 1});

 /* ---- elements ---- */
 let owner: HTMLDivElement;
 let dialog: HTMLDialogElement;
 let layoutDialog: HTMLDialogElement;
 let layoutTrigger: HTMLElement | null = null;
 let menuTrigger: HTMLElement | null = null;

 /* ---- document ---- */
 let page = $state.raw<Page | null>(initial.current ? clone(initial.current.page) : null);
 let lettering = $state.raw<Lettering[]>(initial.current?.lettering || []);
 let pages = $state.raw(initial.pages);
 let history: DraftHistory | null = initial.current ? new DraftHistory(initial.current.page) : null;
 let epoch = 0;
 let canUndo = $state(false), canRedo = $state(false), dirty = $state(false);
 let busy = $state(false), error = $state(''), conflict = $state(false), saveError = false;

 /* ---- autosave ---- */
 const summaryTime = (id?: string) => { const s = pages.find(p => p.id === id); const at = s?.updated_at ? Date.parse(s.updated_at) : NaN; return Number.isFinite(at) ? at : null; };
 const firstSaved = summaryTime(initial.current?.page.id);
 let saveState = $state<SaveState>('saved');
 let savedAt = $state<number | null>(firstSaved);
 let now = $state(Date.now());
 const autosave = new Autosave(persist, () => !!history && history.dirty, a => { saveState = a.state; savedAt = a.savedAt; });
 autosave.reset(firstSaved);

 /* ---- interface state ---- */
 let mode = $state(bootMode === 'lettering' ? 'lettering' : 'pages');
 let tool = $state(bootTool === 'region' ? 'region' : 'select');
 let tray = $state(bootTray === 'layouts' || bootTray === 'variations' ? bootTray : 'properties');
 let zoom = $state('100');
 let menu = $state('');
 let panelID = $state(initial.current?.page.panels[0]?.id || '');
 let layerID = $state(initial.current?.page.panels[0]?.layers.find(l => l.kind !== 'lettering')?.id || '');
 let bubbleID = $state(initial.current?.page.bubbles[0]?.id || '');
 let nextKind = $state<Bubble['kind']>('speech');
 let proof = $state(false), tilt = $state(true), scope = $state('panel');
 let layoutFilter = $state('suggested');
 let pickerLayouts = $state.raw<Layout[]>(initial.layouts);
 let pickerBeat = $state<Beat | null>(initial.current ? initial.beats?.[String(initial.current.page.number)] || null : null);
 let pickerBlank = $state(false);
 let pickedLayout = $state(initial.layouts.find(l => l.suggested)?.id || initial.layouts[0]?.id || '');

 /* ---- jobs ---- */
 let activeJob = $state<Job | null>(null), candidates = $state.raw<Candidate[]>([]), candidateIndex = $state(0), versions = $state.raw<Version[]>([]);
 let compute = $state<Compute | null>(initial.compute || null), recent = $state.raw<Job[]>([]);
 let jobID = bootJob, jobAbort: AbortController | null = null, pollTimer: ReturnType<typeof setTimeout> | undefined;
 let previewTimer: ReturnType<typeof setTimeout> | undefined, previewAbort: AbortController | null = null, previewSeq = 0;
 let queueTimer: ReturnType<typeof setTimeout> | undefined, queueInterval: ReturnType<typeof setInterval> | undefined, clockInterval: ReturnType<typeof setInterval> | undefined;
 let destroyed = false;

 /* ---- region and lettering ---- */
 let mask = $state<Blob | null>(null), maskTool = $state('brush'), feather = $state(12), brushSize = $state(32), regionPrompt = $state(''), clearMask = $state(0);
 let rectangle = $state<Rect>({x: 0, y: 0, w: 100, h: 100}), rectangleSelection = $state<Rect | null>(null), regionWidth = $state(0);
 let rubyStart = $state(0), rubyEnd = $state(1), rubyText = $state('');
 let dragBubble = $state('');
 let confirmTitle = $state(''), confirmBody = $state(''), confirmAction: (() => void) | null = null;

 /* ---- derived ---- */
 const panel = $derived(page?.panels.find(p => p.id === panelID));
 const panelIndex = $derived(page && panel ? page.panels.indexOf(panel) : -1);
 const panelName = $derived(page && panel ? `${page.number}.${panelIndex + 1}` : '');
 const layer = $derived(panel?.layers.find(l => l.id === layerID));
 const textLayer = $derived(panel?.layers.find(l => l.kind === 'lettering'));
 const bubble = $derived(page?.bubbles.find(b => b.id === bubbleID));
 const activeLayout = $derived(pickerLayouts.find(l => l.id === pickedLayout));
 const selectedCandidate = $derived(candidates.find(c => c.index === candidateIndex));
 // Page-level edits (layouts) need no selected panel; panel edits do.
 const writablePage = $derived(!!page && initial.permissions.art && !initial.chapter.locked && !page.locked && !page.panels.some(p => p.locked) && !busy);
 const writableArt = $derived(!!page && !!panel && initial.permissions.art && !initial.chapter.locked && !page.locked && !panel.locked && !busy);
 const writableLetter = $derived(!!page && !!panel && initial.permissions.letter && !initial.chapter.locked && !page.locked && !panel.locked && !textLayer?.locked && !busy);
 const selectedText = $derived(bubble?.texts[lang] || {text: '', source: 'human', status: 'draft'});
 const currentKind = $derived(bubble?.kind || nextKind);
 const characterLayers = $derived(panel?.layers.filter(l => l.kind === 'character') || []);
 const styleOptions = $derived.by(() => { const list = [...(initial.styles || [])]; const current = panel?.style || initial.project.style; if (current && !list.some(o => o.id === current)) list.push({id: current, label: current}); return list; });
 const sortedBubbles = $derived([...(page?.bubbles || [])].sort((a, b) => a.order - b.order));
 const bubbleLayout = $derived(lettering.find(l => l.bubble_id === bubbleID));
 const autoPt = $derived(bubbleLayout?.font_size && page ? Math.round(pxToPt(bubbleLayout.font_size, page.width, initial.print?.width_mm)) : 0);
 const currentPt = $derived(bubble?.style.size && page ? Math.round(pxToPt(bubble.style.size, page.width, initial.print?.width_mm)) : 0);
 const verticalOK = $derived(verticalAllowed(lang));
 const promptTag = $derived(!panel ? '' : panel.prompt_source === 'script' ? t('from_script') : panel.script_panel_id ? t('from_script_edited') : panel.prompt ? t('edited') : '');
 const regionZoom = $derived.by(() => { if (!page || !panel || !regionWidth) return 100; const b = bounds(panel.points); return Math.max(1, Math.round(regionWidth / (b.w * 520 / page.width) * 100)); });
 const drift = $derived(candidates.find(c => c.lock_score !== undefined && c.lock_score < .9));
 const driftName = $derived(characters.find(c => c.id === characterLayers[0]?.character_id)?.name || characterLayers[0]?.name || '');
 const jobRunning = $derived(activeJob?.state === 'queued' || activeJob?.state === 'running');

 /* ---- routing preview (the chip and help line before anything is queued) ---- */
 interface RouteInfo {available: boolean; model_id?: string; model_name?: string; provider_id?: string; remote: boolean; placeholder: boolean; estimate_seconds?: number; remote_cost: boolean; message?: string}
 let route = $state<RouteInfo | null>(null);
 let routeSeq = 0;
 const routeWhere = $derived(!route?.available ? '' : route.placeholder ? t('route.builtin') : route.remote ? route.provider_id || '' : t('route.local'));
 const routeText = $derived(!route?.available ? route?.message || t('route.pending') : [route.model_name || route.model_id || '', routeWhere, route.estimate_seconds ? t('route.estimate', {seconds: Math.round(route.estimate_seconds)}) : ''].filter(Boolean).join(' · '));
 const routeHelp = $derived(!route?.available ? t('four_variations') : t('four_variations_route', {where: compute?.gpu || routeWhere, cost: route.remote_cost ? t('route.own_key') : t('route.no_cost')}));
 async function refreshRoute() {
  if (!page || !panel || !initial.permissions.generate) { route = null; return; }
  const seq = ++routeSeq, kind = tool === 'region' ? 'region' : 'panel';
  const query = new URLSearchParams({kind, panel_id: panel.id, layer_id: kind === 'region' ? layerID : ''});
  try { const out = await request<RouteInfo>(`${pageAPI()}/route?${query}`); if (seq === routeSeq && !destroyed) route = out; } catch { if (seq === routeSeq) route = null; }
 }
 $effect(() => { void [page?.id, panelID, tool, layerID, characterLayers.length]; untrack(() => void refreshRoute()); });
 // D8 "Reroll only one layer": characters first, then background and FX, with short names.
 const rerollLayers = $derived([...(panel?.layers.filter(l => l.kind !== 'lettering') || [])].sort((a, b) => ({character: 0, background: 1, fx: 2, lettering: 3})[a.kind] - ({character: 0, background: 1, fx: 2, lettering: 3})[b.kind]));
 function rerollName(l: Layer) { const name = label(l).split(' · ')[0].trim(); return l.kind === 'character' ? name.split(/\s+/)[0] : name; }
 function historyAction(action: string) { return initial.messages[`history.action.${action}`] || humanize(action); }

 const meter = $derived.by(() => {
  const c = compute, jobs = (c?.queued || 0) + (c?.running || 0);
  if (!c) return {text: t('meter.no_worker'), title: t('meter.title_none', {jobs: 0}), load: 0, idle: false, offline: true};
  const text = c.queued > 0 ? t('meter.queued', {count: c.queued}) : c.running > 0 ? t('meter.running', {count: c.running}) : c.workers_online > 0 ? t('meter.idle') : t('meter.no_worker');
  const title = c.gpu ? t('meter.title_gpu', {gpu: c.gpu, used: gb(c.vram_used_mb), total: gb(c.vram_total_mb), jobs}) : c.workers_online > 0 ? t('meter.title_workers', {count: c.workers_online, jobs}) : t('meter.title_none', {jobs});
  return {text, title, load: Math.round(Math.min(1, Math.max(0, c.load)) * 100), idle: jobs === 0 && c.workers_online > 0, offline: c.workers_online === 0 && jobs === 0};
 });
 const workerLine = $derived.by(() => {
  const c = compute;
  if (c?.gpu) return t('footer.worker_gpu', {gpu: c.gpu, used: gb(c.vram_used_mb), total: gb(c.vram_total_mb)});
  if (c?.workers_online && c.worker_name) return t('footer.worker', {name: c.worker_name});
  return t('footer.no_worker', {host: c?.host || location.hostname});
 });
 const footerEnd = $derived(tool === 'region' && page ? t('footer.region', {step: page.version + 1}) : mode === 'lettering' ? t('lettering_independent') : tray === 'variations' ? workerLine : t('footer.selfhost', {host: compute?.host || location.hostname}));
 const saveText = $derived(!page ? '' : saveState === 'saving' ? t('save.saving') : saveState === 'dirty' ? t('save.dirty') : saveState === 'saved' ? (savedAt ? savedAgo(savedAt, now, t, ms => new Intl.DateTimeFormat(initial.lang, {dateStyle: 'medium'}).format(ms)) : t('save.never')) : t(`save.${saveState}`));
 const variationStatus = $derived.by(() => {
  if (!activeJob) return t('variations.empty');
  const parts: string[] = [];
  if (activeJob.state === 'done') parts.push(t('variations.ready', {ready: candidates.length, total: 4}), t('duration.seconds', {count: jobSeconds(activeJob, now)}));
  else if (activeJob.state === 'queued') parts.push(t('variations.queued_at', {position: (activeJob.position || 0) + 1}));
  else if (activeJob.state === 'running') parts.push(t('variations.generating', {percent: Math.round(activeJob.progress * 100)}), t('duration.seconds', {count: jobSeconds(activeJob, now)}));
  else parts.push(t(`job.${activeJob.state}`));
  if (activeJob.model_name) parts.push(activeJob.model_name);
  return parts.join(' · ');
 });
 const layoutHint = $derived.by(() => {
  if (!page) return '';
  const b = pickerBeat;
  if (b && b.panels > 0) {
   const details = [t('layout.beat.panels', {count: b.panels})];
   for (const k of ['wide', 'medium', 'close'] as const) if (b[k] > 0) details.push(t(`layout.beat.${k}`, {count: b[k]}));
   return t('layout.beat', {page: page.number, details: details.join(', ')});
  }
  if (pickerBlank || !page.panels.length) return t('layout.hint_blank', {page: page.number});
  return t('layout.hint', {page: page.number, count: page.panels.length});
 });
 // Suggested layouts best fit first (server rank), then the rest in catalogue order.
 const rankedLayouts = $derived([...pickerLayouts].sort((a, b) => (a.rank || 99) - (b.rank || 99)));
 const pageHasWork = $derived(!!page && (page.bubbles.length > 0 || page.panels.some(p => p.prompt || p.layers.some(l => l.asset))));

 function gb(mb?: number) { return number.format((mb || 0) / 1024); }
 function label(l: Layer) { return layerLabel(l, characters, t); }
 function characterName(id?: string) { return characters.find(c => c.id === id)?.name || ''; }
 function panelNameOf(id?: string) { const p = pages.length && page ? page.panels.findIndex(v => v.id === id) : -1; return p >= 0 && page ? `${page.number}.${p + 1}` : ''; }

 /* ---- drafts and saving ---- */
 function emitDirty() { if (owner) document.dispatchEvent(new CustomEvent('mv:unsaved', {detail: {owner, dirty: dirty || mask !== null}})); }
 function sync() { if (!history) return; page = clone(history.current); dirty = history.dirty; canUndo = history.past.length > 0; canRedo = history.future.length > 0; emitDirty(); }
 function mark(next: Page, preview = true) {
  if (!history) return;
  previewSeq++; previewAbort?.abort();
  history.commit(next);
  sync();
  autosave.change();
  if (preview) schedulePreview();
 }
 function update(fn: (p: Page) => void, preview = true) { if (!page || busy) return; const next = clone(page); fn(next); mark(next, preview); }
 function editPanel(fn: (p: Panel) => void) { if (!writableArt) return; update(p => { const pn = p.panels.find(v => v.id === panelID); if (pn) fn(pn); }, false); }
 function editLayer(fn: (l: Layer) => void) { if (!writableArt || layer?.locked) return; editPanel(p => { const l = p.layers.find(v => v.id === layerID); if (l) fn(l); }); }
 function editBubble(fn: (b: Bubble) => void) { if (!writableLetter) return; update(p => { const b = p.bubbles.find(v => v.id === bubbleID); if (b) fn(b); }); }
 const pageAPI = (id = page?.id || '') => `${initial.api}/pages/${id}`;

 // persist saves the draft as it is now. Edits made while the request runs
 // stay in the draft and are saved by the next run.
 async function persist(): Promise<SaveResult> {
  if (!history) return 'ok';
  const run = epoch, sent = clone(history.current), base = history.base.version;
  try {
   const out = await request<PageResponse>(pageAPI(sent.id), 'PUT', {page: {...sent, version: base}, base_version: base});
   if (destroyed || run !== epoch || !history) return 'ok';
   if (canonical(history.current) === canonical(sent)) { history.accept(out.page); lettering = out.lettering; } else history.rebase(out.page);
   sync();
   pages = pages.map(p => p.id === out.page.id ? {...p, version: out.page.version, panels: out.page.panels.length, locked: out.page.locked, updated_at: new Date().toISOString()} : p);
   if (saveError) { error = ''; saveError = false; }
   return 'ok';
  } catch (e) {
   if (destroyed || run !== epoch) return 'ok';
   saveError = true;
   if (e instanceof APIError) {
    error = e.message;
    if (e.status === 409) { conflict = true; return 'conflict'; }
    if (e.status === 423) { conflict = true; return 'locked'; }
    if (e.status === 422 || e.status === 400 || e.status === 413) return 'invalid';
    return 'error';
   }
   error = t('error.network');
   return 'error';
  }
 }
 // flush saves now; false when the draft could not be saved.
 async function flush(): Promise<boolean> { return !dirty ? true : autosave.flush(); }
 // leave runs proceed once the draft is saved, else asks through the shared unsaved dialog.
 async function leave(proceed: () => void) {
  if (!dirty && !mask) { proceed(); return; }
  if (dirty && await flush() && !mask) { proceed(); return; }
  document.dispatchEvent(new CustomEvent('mv:guard-navigation', {detail: {proceed: () => { dirty = false; mask = null; emitDirty(); proceed(); }, trigger: document.activeElement instanceof HTMLElement ? document.activeElement : undefined}}));
 }
 function followLink(e: MouseEvent, href: string) {
  if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || (!dirty && !mask)) return;
  e.preventDefault();
  void leave(() => location.assign(href));
 }
 function guard(proceed: () => void) {
  if (dirty || mask) document.dispatchEvent(new CustomEvent('mv:guard-navigation', {detail: {proceed, trigger: document.activeElement instanceof HTMLElement ? document.activeElement : undefined}}));
  else proceed();
 }
 function selectPanel(id: string) { panelID = id; layerID = page?.panels.find(p => p.id === id)?.layers.find(l => l.kind !== 'lettering')?.id || ''; const b = page?.bubbles.find(v => v.panel_id === id); if (b) bubbleID = b.id; }
 // A page always has its first panel selected when the selection is gone
 // (a new page, a page switch, a layout applied, an undo), so the inspector
 // never shows a form that edits nothing.
 $effect(() => { const p = page, id = panelID; if (p && !p.panels.some(v => v.id === id)) untrack(() => selectPanel(p.panels[0]?.id || '')); });
 function selectBubble(id: string) { bubbleID = id; const b = page?.bubbles.find(v => v.id === id); if (b?.panel_id) panelID = b.panel_id; if (mode !== 'lettering') switchMode('lettering'); tray = 'properties'; tool = 'select'; }
 function setMask(blob: Blob | null) { mask = blob; emitDirty(); }
 function switchMode(next: string) { if (dirty) void flush(); mode = next; tool = 'select'; tray = 'properties'; menu = ''; }
 function confirm(title: string, body: string, action: () => void) { confirmTitle = title; confirmBody = body; confirmAction = action; void tick().then(() => dialog.showModal()); }
 function accept() { const action = confirmAction; confirmAction = null; dialog.close(); action?.(); }
 function fail(e: unknown) {
  if (e instanceof DOMException && e.name === 'AbortError') return;
  saveError = false;
  error = e instanceof APIError ? e.message : t('error.network');
  if (e instanceof APIError && (e.status === 409 || e.status === 423)) conflict = true;
 }
 function toast(title: string) { showToast({kind: 'success', title}); }
 function install(v: PageResponse, preserveMask = false, saved: number | null = Date.now()) {
  epoch++; previewSeq++; previewAbort?.abort(); clearTimeout(previewTimer);
  page = clone(v.page); lettering = v.lettering; history = new DraftHistory(v.page);
  dirty = false; canUndo = false; canRedo = false; conflict = false; error = ''; saveError = false;
  if (!preserveMask) mask = null;
  autosave.reset(saved);
  emitDirty();
  if (!page.panels.some(p => p.id === panelID)) selectPanel(page.panels[0]?.id || '');
  if (!page.bubbles.some(b => b.id === bubbleID)) bubbleID = page.bubbles[0]?.id || '';
  pages = pages.map(p => p.id === page!.id ? {...p, version: page!.version, panels: page!.panels.length, locked: page!.locked} : p);
 }
 function historyURL(url: URL) { window.history.replaceState(null, '', url); }
 async function load(id: string) {
  if (busy) return;
  clearTimeout(pollTimer); jobAbort?.abort(); jobID = ''; activeJob = null; candidates = []; busy = true;
  try {
   install(await request<PageResponse>(pageAPI(id)), false, summaryTime(id));
   const url = new URL(location.href); url.searchParams.set('page', String(page!.number)); url.searchParams.delete('job'); historyURL(url);
  } catch (e) { fail(e); } finally { busy = false; }
 }
 async function refreshPages(selectFirst = false) { const b = await request<typeof initial>(initial.api); pages = b.pages; if (selectFirst && b.current) install(b.current, false, summaryTime(b.current.page.id)); }
 function reload() { if (!page) return; const id = page.id; guard(() => void load(id)); }
 function schedulePreview() { clearTimeout(previewTimer); previewTimer = setTimeout(() => void preview(false), 250); }
 async function preview(reflow: boolean) {
  if (!page) return;
  if (reflow) { if (!await flush()) return; busy = true; }
  previewAbort?.abort();
  const abort = new AbortController(); previewAbort = abort;
  const seq = ++previewSeq;
  try {
   const out = await request<{lettering: Lettering[]; bubbles: Bubble[]}>(`${pageAPI()}/lettering`, 'POST', {page, reflow}, abort.signal);
   if (seq !== previewSeq || destroyed) return;
   if (reflow) { busy = false; update(p => p.bubbles = out.bubbles, false); }
   lettering = out.lettering;
  } catch (e) { fail(e); } finally { if (reflow) busy = false; }
 }
 function undo() { if (!history || busy) return; history.undo(); sync(); autosave.change(); schedulePreview(); }
 function redo() { if (!history || busy) return; history.redo(); sync(); autosave.change(); schedulePreview(); }

 /* ---- pages ---- */
 async function addPage() {
  menu = '';
  await leave(() => void (async () => {
   busy = true;
   try { const out = await request<PageResponse>(`${initial.api}/pages`, 'POST', {layout_id: ''}); await refreshPages(); install(out); busy = false; await openLayouts(); }
   catch (e) { fail(e); } finally { busy = false; }
  })());
 }
 async function reorder(delta: number) {
  menu = '';
  if (!page || !await flush()) return;
  const ids = pages.map(p => p.id), i = ids.indexOf(page.id), j = i + delta;
  if (j < 0 || j >= ids.length) return;
  [ids[i], ids[j]] = [ids[j], ids[i]];
  busy = true;
  try { const out = await request<{pages: typeof pages}>(`${initial.api}/pages/reorder`, 'POST', {ids}); pages = out.pages; busy = false; await load(page.id); }
  catch (e) { fail(e); } finally { busy = false; }
 }
 function deletePage() {
  menu = '';
  if (!page) return;
  confirm(t('delete.title'), t('delete.body', {number: page.number}), () => void (async () => {
   busy = true;
   try { await request(pageAPI(), 'DELETE', {base_version: history?.base.version || page!.version, confirm: true}); page = null; history = null; dirty = false; mask = null; emitDirty(); await refreshPages(true); }
   catch (e) { fail(e); } finally { busy = false; }
  })());
 }
 async function lockTarget(target: 'page' | 'panel' | 'layer', id = layerID) {
  menu = '';
  if (!page) return;
  const l = panel?.layers.find(v => v.id === id);
  const locked = target === 'page' ? page.locked : target === 'panel' ? panel?.locked : l?.locked;
  const perform = () => void (async () => {
   if (!await flush()) return;
   busy = true;
   try { install(await request<PageResponse>(`${pageAPI()}/lock`, 'POST', {panel_id: target === 'page' ? '' : panelID, layer_id: target === 'layer' ? id : '', locked: !locked, confirm: !!locked, base_version: history?.base.version || page!.version})); }
   catch (e) { fail(e); } finally { busy = false; }
  })();
  if (locked) confirm(t('unlock'), t('unlock.body'), perform); else perform();
 }
 async function openHistory() {
  menu = '';
  if (!page) return;
  tray = 'history';
  try { versions = (await request<{versions: Version[]}>(`${pageAPI()}/history`)).versions; } catch (e) { fail(e); }
 }
 function revert(v: Version) {
  confirm(t('history.revert'), t('history.body', {number: v.number}), () => void (async () => {
   if (!await flush()) return;
   busy = true;
   try { install(await request<PageResponse>(`${pageAPI()}/revert`, 'POST', {version_id: v.id, base_version: history?.base.version || page!.version, confirm: true})); toast(t('version.restored')); }
   catch (e) { fail(e); } finally { busy = false; }
  })());
 }

 /* ---- layouts (D2) ---- */
 async function openLayouts() {
  menu = '';
  if (!page) return;
  layoutTrigger = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  layoutFilter = 'suggested';
  try {
   const out = await request<{layouts: Layout[]; beat?: Beat; blank?: boolean}>(`${pageAPI()}/layouts`);
   pickerLayouts = out.layouts; pickerBeat = out.beat || null; pickerBlank = !!out.blank;
  } catch { pickerLayouts = initial.layouts; pickerBeat = null; pickerBlank = !page.panels.length; }
  // The page's own layout stays selected; otherwise the best fit is
  // preselected, except on a blank page, which waits for a choice.
  const usable = rankedLayouts.filter(l => page && canUseLayout(page, l));
  pickedLayout = (usable.find(l => l.id === page?.layout_id) || (pickerBlank ? undefined : usable.find(l => l.suggested)))?.id || '';
  tray = 'layouts';
 }
 function closeLayouts() { tray = 'properties'; layoutDialog?.close(); layoutTrigger?.focus(); }
 function chooseLayout() {
  if (!page || !activeLayout || !writablePage) return;
  try { mark(applyLayout(page, activeLayout, tilt)); closeLayouts(); toast(t('layout.applied')); } catch { error = t('layout.preserve'); }
 }
 $effect(() => { if (tray === 'layouts' && layoutDialog && !layoutDialog.open) layoutDialog.showModal(); else if (tray !== 'layouts' && layoutDialog?.open) layoutDialog.close(); });

 /* ---- layers ---- */
 function addCharacter(id: string) {
  menu = '';
  if (!id) return;
  const lid = newID();
  editPanel(p => {
   const above = p.layers.findIndex(l => l.kind === 'fx' || l.kind === 'lettering');
   p.layers.splice(above < 0 ? p.layers.length : Math.max(1, above), 0, {id: lid, kind: 'character', character_id: id, name: characterName(id), order: 0, visible: true, locked: false, opacity: 1, transform: {}, provenance: 'human'});
   p.layers.forEach((l, i) => l.order = i);
  });
  layerID = lid;
 }
 function addFX() {
  menu = '';
  const lid = newID();
  editPanel(p => {
   const at = p.layers.findIndex(l => l.kind === 'lettering');
   p.layers.splice(at < 0 ? p.layers.length : at, 0, {id: lid, kind: 'fx', order: 0, visible: true, locked: false, opacity: 1, transform: {}, provenance: 'human'});
   p.layers.forEach((l, i) => l.order = i);
  });
  layerID = lid;
 }
 function toggleVisible(l: Layer) { layerID = l.id; editLayer(v => v.visible = !v.visible); }

 /* ---- lettering (D3) ---- */
 function addBubble(kind: Bubble['kind']) {
  if (!page || !writableLetter) return;
  const target = panelID || page.panels[0]?.id || '';
  const b = newBubble(page, target, kind, lang);
  update(p => p.bubbles.push(b));
  bubbleID = b.id;
  nextKind = kind;
 }
 function chooseKind(kind: Bubble['kind']) {
  nextKind = kind;
  const currentPage = page;
  if (currentPage && bubble && bubbleWritable(currentPage, bubble)) { if (bubble.kind !== kind) editBubble(b => setBubbleKind(b, kind, lang, currentPage)); }
  else addBubble(kind);
 }
 function orderBubble(id: string, delta: number) { if (!page || busy || !initial.permissions.letter || initial.chapter.locked || !canReorderBubble(page, id, delta)) return; mark(reorderBubble(page, id, delta)); }
 function gripKey(e: KeyboardEvent, id: string) {
  if (e.key !== 'ArrowUp' && e.key !== 'ArrowDown') return;
  e.preventDefault();
  orderBubble(id, e.key === 'ArrowUp' ? -1 : 1);
  void tick().then(() => owner.querySelector<HTMLElement>(`[data-grip="${id}"]`)?.focus());
 }
 function dropBubble(id: string, to: number) { if (!page || busy || !initial.permissions.letter || initial.chapter.locked) return; mark(moveBubbleTo(page, id, to)); }
 function setSizePt(value: string) { const pt = Number(value); if (!page) return; editBubble(b => b.style.size = !value || !Number.isFinite(pt) || pt <= 0 ? 0 : Math.min(1000, Math.round(ptToPx(pt, page!.width, initial.print?.width_mm) * 10) / 10)); }
 function addRuby() {
  try {
   const run = rubyFor(selectedText.text, rubyStart, rubyEnd, rubyText);
   const runs = [...(selectedText.ruby || []), run].sort((a, b) => a.start - b.start);
   if (runs.some((r, i) => i > 0 && r.start < runs[i - 1].end)) throw Error();
   editBubble(b => b.texts[lang] = {...selectedText, ruby: runs, source: 'human', status: 'draft'});
   rubyText = '';
  } catch { error = t('ruby.error'); }
 }

 /* ---- generation (D1/D8/D13) ---- */
 // A panel job's variations never stand in as region candidates.
 function makeRegion() { if (!layer?.asset) { error = t('region.no_art'); return; } if (activeJob && activeJob.type !== 'region.edit') { clearTimeout(pollTimer); jobAbort?.abort(); activeJob = null; jobID = ''; candidates = []; } tool = 'region'; mode = 'pages'; tray = 'properties'; scope = 'region'; }
 function closeRegion() { guard(() => { tool = 'select'; mask = null; scope = 'panel'; emitDirty(); }); }
 async function generate(kind = scope, layerOverride = '') {
  if (!page || !panel || !initial.permissions.generate || busy) return;
  const maskForRequest = mask;
  if (!await flush()) return;
  const target = layerOverride || layerID;
  if ((kind === 'layer' || kind === 'region') && !target) { error = t('region.no_art'); return; }
  busy = true; error = '';
  try {
   let maskID = '';
   if (kind === 'region') {
    if (!maskForRequest) throw Error('mask');
    const form = new FormData();
    form.append('file', maskForRequest, 'mask.png'); form.append('layer_id', target); form.append('base_version', String(page.version));
    maskID = (await request<{id: string}>(`${pageAPI()}/masks`, 'POST', form)).id;
   }
   const out = await request<{job: Job}>(`${pageAPI()}/generate`, 'POST', {kind, panel_id: panelID, layer_id: kind === 'panel' ? '' : target, mask_id: maskID, prompt: kind === 'region' ? regionPrompt : panel.prompt, base_version: page.version, routing_mode: 'balanced'});
   activeJob = out.job; jobID = out.job.id; candidates = [];
   tray = kind === 'region' ? 'properties' : 'variations';
   const url = new URL(location.href); url.searchParams.set('job', jobID); url.searchParams.set('panel', kind === 'region' ? 'properties' : 'variations'); if (kind === 'region') url.searchParams.set('tool', 'region'); historyURL(url);
   void pollJob(); scheduleQueue(0);
  } catch (e) { if (e instanceof Error && e.message === 'mask') error = t('region.empty'); else fail(e); } finally { busy = false; }
 }
 async function pollJob() {
  if (!jobID || destroyed) return;
  jobAbort?.abort(); jobAbort = new AbortController();
  try {
   if (!page) {
    const result = await request<{job: Job & {type: string; scope: {project_id: string; chapter_id?: string}}}>(`/api/v1/jobs/${jobID}`, 'GET', undefined, jobAbort.signal);
    const j = result.job;
    if (j.type !== 'pages.from_script' || j.scope.project_id !== initial.project.id || j.scope.chapter_id !== initial.chapter.id) throw Error('job');
    activeJob = j;
    if (j.state === 'done') { await refreshPages(true); jobID = ''; activeJob = null; return; }
   } else {
    const out = await request<{job: Job; candidates: Candidate[]}>(`${pageAPI()}/jobs/${jobID}`, 'GET', undefined, jobAbort.signal);
    const was = activeJob?.state;
    activeJob = out.job; candidates = out.candidates;
    if (!candidates.some(c => c.index === candidateIndex)) candidateIndex = candidates[0]?.index || 0;
    if (was !== out.job.state) scheduleQueue(0);
   }
   if (activeJob?.state === 'queued' || activeJob?.state === 'running') pollTimer = setTimeout(() => void pollJob(), 2000);
  } catch (e) { fail(e); }
 }
 async function keep() {
  if (!page || !activeJob || busy || !candidates.length) return;
  if (!await flush()) return;
  busy = true;
  try {
   install(await request<PageResponse>(`${pageAPI()}/jobs/${activeJob.id}/apply`, 'POST', {index: candidateIndex}));
   tray = 'properties'; tool = 'select'; scope = 'panel'; activeJob = null; jobID = ''; candidates = [];
   const url = new URL(location.href); url.searchParams.delete('job'); url.searchParams.delete('panel'); url.searchParams.delete('tool'); historyURL(url);
   toast(t('variations.kept'));
   scheduleQueue(0);
  } catch (e) { fail(e); } finally { busy = false; }
 }
 function discardRegionResult() { candidates = []; activeJob = null; jobID = ''; }

 /* ---- queue meter and D8 queue ---- */
 function scheduleQueue(ms = 400) { clearTimeout(queueTimer); queueTimer = setTimeout(() => void refreshQueue(), ms); }
 async function refreshQueue() {
  if (destroyed) return;
  try { const out = await request<{compute: Compute; jobs: Job[]}>(`${initial.api}/queue`); compute = out.compute; recent = out.jobs; } catch { /* the meter keeps its last state */ }
 }
 function queueLabel(j: Job) { return j.type === 'region.edit' ? t('queue.kind.region', {prompt: j.prompt || ''}) : j.layer_id ? t('queue.kind.layer') : t('queue.kind.panel'); }
 function queueState(j: Job) {
  if (j.state === 'done') return t('queue.done', {seconds: jobSeconds(j, now)});
  if (j.state === 'queued') return j.estimate_seconds ? t('queue.position', {position: (j.position || 0) + 1, seconds: Math.round(j.estimate_seconds)}) : t('queue.position_plain', {position: (j.position || 0) + 1});
  if (j.state === 'running') return t('queue.running_item', {percent: Math.round(j.progress * 100)});
  return j.state === 'failed' ? t('queue.failed') : t('queue.canceled');
 }
 function queueIcon(j: Job) { return j.state === 'done' ? 'circle-check' : j.state === 'running' ? 'loader-2' : j.state === 'queued' ? 'clock' : 'alert-triangle'; }
 function jobPanel(j: Job) { if (!page || j.page_id !== page.id) { const s = pages.find(p => p.id === j.page_id); return s ? `${s.number}` : ''; } return panelNameOf(j.panel_id); }

 /* ---- menus ---- */
 function toggleMenu(name: string, e: MouseEvent) {
  menuTrigger = e.currentTarget as HTMLElement;
  menu = menu === name ? '' : name;
  if (menu) void tick().then(() => owner.querySelector<HTMLElement>(`[data-menu="${name}"] [role^="menuitem"]:not([disabled])`)?.focus());
 }
 function menuKey(e: KeyboardEvent) {
  const items = Array.from((e.currentTarget as HTMLElement).querySelectorAll<HTMLElement>('[role^="menuitem"]:not([disabled])'));
  const i = items.indexOf(document.activeElement as HTMLElement);
  if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); menu = ''; menuTrigger?.focus(); }
  else if (e.key === 'ArrowDown') { e.preventDefault(); items[(i + 1) % items.length]?.focus(); }
  else if (e.key === 'ArrowUp') { e.preventDefault(); items[(i - 1 + items.length) % items.length]?.focus(); }
  else if (e.key === 'Home') { e.preventDefault(); items[0]?.focus(); }
  else if (e.key === 'End') { e.preventDefault(); items.at(-1)?.focus(); }
  else if (e.key === 'Tab') menu = '';
 }
 function outside(e: PointerEvent) { if (menu && !(e.target instanceof Element && e.target.closest('.mv-ed__menu-wrap'))) menu = ''; }

 /* ---- keyboard ---- */
 function key(e: KeyboardEvent) {
  if (e.isComposing || dialog?.open) return;
  const target = e.target as HTMLElement;
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 's') { e.preventDefault(); void autosave.flush(); return; }
  if (busy) return;
  const input = target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement || target instanceof HTMLSelectElement || target?.isContentEditable || target instanceof HTMLButtonElement && !!target.closest('[role=toolbar]');
  if ((e.metaKey || e.ctrlKey) && e.key === 'Enter' && mode === 'pages') { e.preventDefault(); void generate(tool === 'region' ? 'region' : scope); return; }
  if (input) return;
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'z') { e.preventDefault(); if (e.shiftKey) redo(); else undo(); return; }
  if ((tray === 'variations' || tool === 'region') && /^[1-4]$/.test(e.key)) { const c = candidates[Number(e.key) - 1]; if (c) candidateIndex = c.index; return; }
  if ((tray === 'variations' || tool === 'region') && e.key === 'Enter' && candidates.length) { e.preventDefault(); void keep(); return; }
  if (e.key === 'Escape') { if (menu) { menu = ''; return; } if (tray === 'layouts') closeLayouts(); else if (mode === 'lettering' && bubbleID) bubbleID = ''; else if (tool === 'region') closeRegion(); }
  if (['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight'].includes(e.key) && page && target?.closest('.mv-ed__overlay')) {
   e.preventDefault();
   const step = e.shiftKey ? 10 : 1, dx = e.key === 'ArrowLeft' ? -step : e.key === 'ArrowRight' ? step : 0, dy = e.key === 'ArrowUp' ? -step : e.key === 'ArrowDown' ? step : 0;
   if (mode === 'lettering' && bubble && writableLetter) mark(moveBubble(page, bubble.id, dx, dy)); else if (writableArt) mark(movePanel(page, panelID, dx, dy), false);
  }
 }

 /* ---- lifecycle ---- */
 function onRealtime(e: Event) { const m = (e as CustomEvent).detail as {topic?: string; type?: string}; if (m?.type?.startsWith('job.')) scheduleQueue(); }
 function onHidden() { if (document.visibilityState === 'hidden' && dirty) void autosave.flush(); }
 // Last chance when the tab closes: a keepalive save of a small draft.
 function onPageHide() {
  if (!dirty || !history || saveState === 'conflict' || saveState === 'locked') return;
  const base = history.base.version, body = JSON.stringify({page: {...history.current, version: base}, base_version: base});
  if (body.length < 60000) void fetch(pageAPI(history.current.id), {method: 'PUT', credentials: 'same-origin', keepalive: true, headers: {'Content-Type': 'application/json'}, body}).catch(() => {});
 }
 onMount(() => {
  if (jobID) void pollJob();
  emitDirty();
  void refreshQueue();
  document.addEventListener('mv:ws', onRealtime);
  document.addEventListener('visibilitychange', onHidden);
  window.addEventListener('pagehide', onPageHide);
  queueInterval = setInterval(() => { if (document.visibilityState === 'visible') void refreshQueue(); }, 15000);
  clockInterval = setInterval(() => now = Date.now(), 5000);
  if (bootTray === 'layouts' && page) void openLayouts();
 });
 onDestroy(() => {
  destroyed = true; autosave.dispose();
  clearTimeout(pollTimer); clearTimeout(previewTimer); clearTimeout(queueTimer); clearInterval(queueInterval); clearInterval(clockInterval);
  jobAbort?.abort(); previewAbort?.abort();
  document.removeEventListener('mv:ws', onRealtime);
  document.removeEventListener('visibilitychange', onHidden);
  window.removeEventListener('pagehide', onPageHide);
  if (owner) document.dispatchEvent(new CustomEvent('mv:unsaved', {detail: {owner, dirty: false}}));
 });
</script>

{#snippet panelActions()}
 <Button small variant="primary" icon="sparkles" disabled={!writableArt || !initial.permissions.generate} onclick={() => void generate('panel')}>{t('regenerate')}</Button>
 <Button small variant="quiet" icon="lasso" disabled={!writableArt || !layer?.asset || layer?.locked} onclick={makeRegion}>{t('edit_region')}</Button>
 <Button small variant="quiet" icon="stack-2" disabled={!layer || layer.kind === 'lettering'} onclick={() => { scope = 'layer'; const details = owner.querySelector<HTMLDetailsElement>('[data-layer-properties]'); if (details) { details.open = true; details.querySelector('summary')?.focus(); } }}>{t('scope.layer')}</Button>
 <Button small variant="quiet" icon={panel?.locked ? 'lock-open' : 'lock'} disabled={!initial.permissions.lock || busy} onclick={() => void lockTarget('panel')}>{panel?.locked ? t('unlock') : t('lock')}</Button>
 <Button small variant="quiet" icon="message-circle" disabled>{t('comment')} <span class="mv-soon">{t('soon')}</span></Button>
{/snippet}

{#snippet chip(l: Layer)}
 {@const c = characters.find(v => v.id === l.character_id)}
 {@const name = c?.name || label(l)}
 {@const score = l.lock_score ?? c?.lock_score}
 <button type="button" class="mv-ed__chip" class:selected={l.id === layerID} aria-pressed={l.id === layerID} title={name} onclick={() => layerID = l.id}>
  <span class={`mv-avatar mv-avatar--xs mv-avatar--c${avatarTone(l.character_id || l.id)}`} aria-hidden="true">{initials(name)}</span>
  <span class="mv-ed__chip-name">{name}</span>
  {#if score !== undefined}<span class="mv-ed__chip-lock" class:drift={score < .9}><i class="ti ti-lock" aria-hidden="true"></i>{Math.round(score * 100)}%</span>{/if}
 </button>
{/snippet}

<svelte:window onkeydown={key} onpointerdown={outside}/>
<div bind:this={owner} class={`mv-ed${tray === 'layouts' ? ' mv-ed--layouts' : ''}${tool === 'region' ? ' mv-ed--region' : ''}${mode === 'lettering' ? ' mv-ed--lettering' : ''}`} aria-busy={busy}>
 <header class="mv-ed__bar">
  <a class="mv-ed__back" href={`/studio/p/${initial.project.id}/story/${initial.chapter.id}`} aria-label={t('back')} onclick={(e) => followLink(e, `/studio/p/${initial.project.id}/story/${initial.chapter.id}`)}><img class="mv-only-dark" src="/static/brand/mark-color-on-dark-gate-blue.svg" width="24" height="24" alt=""/><img class="mv-only-light" src="/static/brand/mark-color-on-light-gate-blue.svg" width="24" height="24" alt=""/></a>
  <div class="mv-ed__crumbs">
   <span class="mv-ed__project" title={initial.project.title}>{initial.project.title}</span>
   <i class="ti ti-chevron-right mv-ed__crumb" aria-hidden="true"></i>
   <div class="mv-ed__menu-wrap mv-ed__chapter-wrap">
    <button type="button" class="mv-ed__chapter" aria-haspopup="menu" aria-expanded={menu === 'chapters'} title={`${initial.project.title} · ${t('chapter', {number: initial.chapter.number, title: initial.chapter.title})}`} onclick={(e) => toggleMenu('chapters', e)}>
     <strong>{t('chapter', {number: initial.chapter.number, title: initial.chapter.title})}</strong><i class="ti ti-chevron-down" aria-hidden="true"></i><span class="mv-sr-only">{t('chapter.switch')}</span>
    </button>
    {#if menu === 'chapters'}
     <div class="mv-menu mv-ed__menu" role="menu" tabindex="-1" data-menu="chapters" aria-label={t('chapter.switch')} onkeydown={menuKey}>
      {#each initial.chapters || [] as c (c.id)}
       <a class="mv-menu__item" role="menuitem" href={c.href} aria-current={c.id === initial.chapter.id ? 'true' : undefined} onclick={(e) => { menu = ''; followLink(e, c.href); }}><span class="mv-ed__menu-text">{t('chapter', {number: c.number, title: c.title})}</span>{#if c.id === initial.chapter.id}<i class="ti ti-check mv-menu__meta" aria-label={t('chapter.current')}></i>{/if}</a>
      {/each}
     </div>
    {/if}
   </div>
  </div>
  <div class="mv-ed__history-buttons">
   <Button variant="bare" icon="arrow-back-up" label={t('undo')} small disabled={!canUndo || busy} onclick={undo}/>
   <Button variant="bare" icon="arrow-forward-up" label={t('redo')} small disabled={!canRedo || busy} onclick={redo}/>
  </div>
  <nav class="mv-seg mv-ed__modes" aria-label={t('mode')}>
   <button type="button" class="mv-seg__item" aria-pressed={mode === 'pages'} onclick={() => switchMode('pages')}>{t('pages')}</button>
   <button type="button" class="mv-seg__item" aria-pressed={mode === 'lettering'} onclick={() => switchMode('lettering')}>{t('lettering')}</button>
   <button type="button" class="mv-seg__item" disabled>{t('translate')}<span class="mv-soon">{t('soon')}</span></button>
   <button type="button" class="mv-seg__item" disabled>{t('animate')}<span class="mv-soon">{t('soon')}</span></button>
  </nav>
  <div class="mv-ed__bar-end">
   <button type="button" class="mv-ed__collaboration mv-iconlink" disabled aria-label={`${t('collaboration')} · ${t('soon')}`} title={`${t('collaboration')} · ${t('soon')}`}><i class="ti ti-users" aria-hidden="true"></i><span class="mv-soon">{t('soon')}</span></button>
   <span class={`mv-meter mv-ed__meter${meter.idle ? ' mv-meter--idle' : ''}${meter.offline ? ' mv-meter--offline' : ''}`} role="status" aria-label={meter.title} title={meter.title}><i class="ti ti-cpu" aria-hidden="true"></i><span class="mv-bar mv-meter__bar" style:--mv-p={`${meter.load}%`} aria-hidden="true"></span><span class="mv-meter__label">{meter.text}</span></span>
   <Button variant="bare" icon="history" label={t('history.title')} small disabled={!page} onclick={() => void openHistory()}/>
   <div class="mv-ed__menu-wrap">
    <button type="button" class="mv-btn mv-btn--sm mv-ed__zoom" aria-haspopup="menu" aria-expanded={menu === 'zoom'} aria-label={t('zoom.level', {value: zoom})} onclick={(e) => toggleMenu('zoom', e)}><i class="ti ti-zoom-in" aria-hidden="true"></i><span>{zoom}%</span></button>
    {#if menu === 'zoom'}
     <div class="mv-menu mv-ed__menu mv-ed__menu--end mv-ed__menu--narrow" role="menu" tabindex="-1" data-menu="zoom" aria-label={t('zoom')} onkeydown={menuKey}>
      {#each ZOOMS as z}<button type="button" class="mv-menu__item" role="menuitemradio" aria-checked={zoom === z} onclick={() => { zoom = z; menu = ''; menuTrigger?.focus(); }}>{z}%{#if zoom === z}<i class="ti ti-check mv-menu__meta" aria-hidden="true"></i>{/if}</button>{/each}
     </div>
    {/if}
   </div>
   <button type="button" class="mv-btn mv-btn--secondary mv-btn--sm mv-ed__preview" aria-pressed={proof} onclick={() => proof = !proof}><i class="ti ti-eye" aria-hidden="true"></i>{t('preview')}</button>
   <a class="mv-btn mv-btn--secondary mv-btn--sm mv-ed__export" href={`/studio/p/${initial.project.id}/chapters/${initial.chapter.id}/export`} onclick={(e) => followLink(e, `/studio/p/${initial.project.id}/chapters/${initial.chapter.id}/export`)}><i class="ti ti-download" aria-hidden="true"></i>{t('export')}</a>
   <button type="button" class="mv-btn mv-btn--primary mv-btn--sm mv-ed__publish" disabled aria-label={`${t('publish')} · ${t('soon')}`}>{t('publish')}<span class="mv-soon">{t('soon')}</span></button>
  </div>
 </header>

 <aside class="mv-ed__left">
  <div class="mv-ed__section-head mv-ed__rail-head">
   <strong>{t('pages')}</strong>
   <span class="mv-ed__head-actions">
    <button type="button" class="mv-iconlink" aria-label={t('page.add_layout')} title={t('page.add_layout')} disabled={!initial.permissions.art || busy || initial.chapter.locked} onclick={() => void addPage()}><i class="ti ti-layout-grid-add" aria-hidden="true"></i></button>
    <span class="mv-ed__menu-wrap">
     <button type="button" class="mv-iconlink" aria-label={t('page.actions')} title={t('page.actions')} aria-haspopup="menu" aria-expanded={menu === 'pages'} disabled={!page} onclick={(e) => toggleMenu('pages', e)}><i class="ti ti-dots" aria-hidden="true"></i></button>
     {#if menu === 'pages' && page}
      <div class="mv-menu mv-ed__menu mv-ed__menu--end" role="menu" tabindex="-1" data-menu="pages" aria-label={t('page.actions')} onkeydown={menuKey}>
       <button type="button" class="mv-menu__item" role="menuitem" disabled={!initial.permissions.art || busy || page.locked} onclick={() => void openLayouts()}><i class="ti ti-layout-grid" aria-hidden="true"></i>{t('page.change_layout')}</button>
       <button type="button" class="mv-menu__item" role="menuitem" disabled={!initial.permissions.art || busy || pages[0]?.id === page.id} onclick={() => void reorder(-1)}><i class="ti ti-arrow-up" aria-hidden="true"></i>{t('page.previous')}</button>
       <button type="button" class="mv-menu__item" role="menuitem" disabled={!initial.permissions.art || busy || pages.at(-1)?.id === page.id} onclick={() => void reorder(1)}><i class="ti ti-arrow-down" aria-hidden="true"></i>{t('page.next')}</button>
       <button type="button" class="mv-menu__item" role="menuitem" onclick={() => void openHistory()}><i class="ti ti-history" aria-hidden="true"></i>{t('history.title')}</button>
       <div class="mv-menu__sep" role="separator"></div>
       <button type="button" class="mv-menu__item" role="menuitem" disabled={!initial.permissions.lock || busy} onclick={() => void lockTarget('page')}><i class={`ti ti-${page.locked ? 'lock-open' : 'lock'}`} aria-hidden="true"></i>{page.locked ? t('unlock_page') : t('lock_page')}</button>
       <button type="button" class="mv-menu__item mv-menu__item--danger" role="menuitem" disabled={!initial.permissions.art || page.locked || busy} onclick={deletePage}><i class="ti ti-trash" aria-hidden="true"></i>{t('page.delete')}</button>
      </div>
     {/if}
    </span>
   </span>
  </div>
  <nav class="mv-ed__filmstrip" aria-label={t('pages')}>
   {#each pages as p (p.id)}
    <button type="button" class="mv-ed__page-thumb" class:selected={p.id === page?.id} aria-current={p.id === page?.id ? 'page' : undefined} aria-label={t('page.number', {number: p.number})} disabled={busy} onclick={() => { if (p.id !== page?.id) void leave(() => void load(p.id)); }}>
     <span class="mv-ed__thumb-frame">
      <img src={`${initial.api}/pages/${p.id}/thumb?v=${p.version}`} alt="" width="88" height="124" loading="lazy"/>
      {#if p.locked}<span class="mv-ed__thumb-lock"><i class="ti ti-lock" aria-label={t('locked')}></i></span>{/if}
     </span>
     <span class="mv-ed__thumb-number" aria-hidden="true">{p.number}</span>
    </button>
   {/each}
  </nav>
  <section class="mv-ed__layers" aria-label={t('layers')}>
   <div class="mv-ed__section-head mv-ed__rail-head">
    <strong>{panel && page ? `${t('layers')} · ${t('panel.label', {page: page.number, panel: panelIndex + 1})}` : t('layers')}</strong>
    <span class="mv-ed__menu-wrap">
     <button type="button" class="mv-iconlink" aria-label={t('layer.add')} title={t('layer.add')} aria-haspopup="menu" aria-expanded={menu === 'layers'} disabled={!panel || !writableArt} onclick={(e) => toggleMenu('layers', e)}><i class="ti ti-plus" aria-hidden="true"></i></button>
     {#if menu === 'layers' && panel}
      <div class="mv-menu mv-ed__menu mv-ed__menu--end mv-ed__menu--up" role="menu" tabindex="-1" data-menu="layers" aria-label={t('layer.add')} onkeydown={menuKey}>
       {#each characters.filter(c => !panel?.layers.some(l => l.character_id === c.id)) as c (c.id)}
        <button type="button" class="mv-menu__item" role="menuitem" onclick={() => addCharacter(c.id)}><i class="ti ti-user" aria-hidden="true"></i>{t('layer.add_character', {name: c.name})}</button>
       {/each}
       <button type="button" class="mv-menu__item" role="menuitem" onclick={addFX}><i class="ti ti-bolt" aria-hidden="true"></i>{t('layer.add_fx')}</button>
      </div>
     {/if}
    </span>
   </div>
   {#if panel}
    {#each [...panel.layers].reverse() as l (l.id)}
     {@const name = label(l)}
     <div class="mv-ed__layer-row" class:selected={mode === 'lettering' ? l.kind === 'lettering' : l.id === layerID}>
      <button type="button" class="mv-ed__layer-select" onclick={() => { if (l.kind === 'lettering') switchMode('lettering'); else { layerID = l.id; if (mode === 'lettering') switchMode('pages'); } }}>
       <i class={`ti ti-${l.kind === 'lettering' ? 'message-circle' : l.kind === 'character' ? 'user' : l.kind === 'fx' ? 'bolt' : 'photo'}`} aria-hidden="true"></i>
       <span>{name}</span>
       <small>{l.kind === 'lettering' ? t('bubble.count', {count: page?.bubbles.filter(b => b.panel_id === panel!.id).length || 0}) : l.kind === 'character' && (l.lock_score ?? characters.find(c => c.id === l.character_id)?.lock_score) !== undefined ? t('lock_score', {score: Math.round((l.lock_score ?? characters.find(c => c.id === l.character_id)?.lock_score ?? 0) * 100)}) : l.provenance === 'ai' ? t('ai') : l.provenance === 'mixed' ? t('mixed') : t('human')}</small>
      </button>
      <button type="button" class="mv-iconlink" aria-label={l.visible ? t('layer.hide_named', {name}) : t('layer.show_named', {name})} disabled={!writableArt || l.locked} onclick={() => toggleVisible(l)}><i class={`ti ti-${l.visible ? 'eye' : 'eye-off'}`} aria-hidden="true"></i></button>
      <button type="button" class="mv-iconlink mv-ed__layer-lock" class:locked={l.locked} aria-label={l.locked ? t('layer.unlock_named', {name}) : t('layer.lock_named', {name})} disabled={!initial.permissions.lock || busy} onclick={() => { layerID = l.kind === 'lettering' ? layerID : l.id; void lockTarget('layer', l.id); }}><i class={`ti ti-${l.locked ? 'lock' : 'lock-open'}`} aria-hidden="true"></i></button>
     </div>
    {/each}
   {/if}
  </section>
 </aside>

 <section class="mv-ed__stage" aria-label={t('canvas')}>
  {#if error}
   <div class="mv-notice mv-notice--error mv-ed__status" role="alert">
    <i class="ti ti-alert-triangle" aria-hidden="true"></i>
    <div><p>{error}</p>{#if conflict}<Button small onclick={reload}>{t('reload')}</Button>{/if}</div>
    {#if !conflict}<button type="button" class="mv-close mv-ed__status-close" aria-label={t('dismiss')} onclick={() => { error = ''; saveError = false; }}><i class="ti ti-x" aria-hidden="true"></i></button>{/if}
   </div>
  {/if}
  {#if !page}
   <div class="mv-ed__empty"><h1>{t('empty.title')}</h1><p>{activeJob ? t(`job.${activeJob.state}`) : t('empty.body')}</p>{#if activeJob}<progress max="1" value={activeJob.progress} aria-label={t('progress')}></progress><Button onclick={() => void pollJob()}>{t('refresh')}</Button>{:else}<Button variant="primary" icon="plus" disabled={!initial.permissions.art} onclick={() => void addPage()}>{t('page.add')}</Button>{/if}</div>
  {:else if tray === 'layouts'}
   <span class="mv-sr-only">{t('layouts')}</span>
  {:else if tool === 'region' && layer?.asset}
   <div class="mv-ed__region-stage">
    <div class="mv-ed__region-heading"><button type="button" class="mv-iconlink" aria-label={t('back_to_page')} onclick={closeRegion}><i class="ti ti-arrow-left" aria-hidden="true"></i></button><span>{t('region.heading', {page: page.number, panel: panelName, zoom: regionZoom})}</span></div>
    {#key layer.id}
     <RegionMask asset={layer.asset} candidateURL={selectedCandidate?.url} tool={maskTool} {feather} size={brushSize} label={panel ? labelFor(page.number, panelIndex, panel, initial.script_actions || {}, t).split(' · ').slice(1).join(' · ') : ''} clearKey={clearMask} {t} onMask={setMask} onDisplay={(w) => regionWidth = w} disabled={busy || !!candidates.length} selection={rectangleSelection} initialMask={mask}/>
    {/key}
    <fieldset class="mv-ed__region-variations">
     <legend class="mv-sr-only">{t('variations.pick')}</legend>
     {#each [0, 1, 2, 3] as i}
      {@const c = candidates[i]}
      {#if c}
       <label class="mv-ed__region-candidate" class:selected={candidateIndex === c.index}><input type="radio" name="variation" value={c.index} bind:group={candidateIndex} aria-keyshortcuts={String(i + 1)} aria-label={t('variations.candidate', {number: i + 1})}/>{#if c.url}<img src={c.url} alt=""/>{/if}<span>{t('region.var', {number: i + 1})}</span></label>
      {:else}
       <span class="mv-ed__region-candidate mv-ed__region-candidate--empty" class:mv-ed__region-candidate--busy={jobRunning} title={t('region.empty_candidate', {number: i + 1})}><span>{t('region.var', {number: i + 1})}</span></span>
      {/if}
     {/each}
     <p>{t('region.variations')}</p>
    </fieldset>
   </div>
  {:else}
   <CanvasStage {page} {lettering} actions={initial.script_actions || {}} selectedPanel={proof ? '' : panelID} selectedBubble={!proof && mode === 'lettering' ? bubbleID : ''} showOrder={!proof && mode === 'lettering'} canArt={writableArt && !proof} canLetter={writableLetter && !proof} previewURL={tray === 'variations' ? selectedCandidate?.url : undefined} previewPanel={panelID} previewLabel={tray === 'variations' && candidates.length ? t('variations.preview', {number: candidates.findIndex(c => c.index === candidateIndex) + 1}) : ''} panelActions={mode === 'pages' && tray === 'properties' && tool === 'select' && !proof ? panelActions : undefined} {zoom} {t} onPanel={selectPanel} onBubble={selectBubble} onMovePanel={(id, dx, dy) => page && mark(movePanel(page, id, dx, dy), false)} onMoveBubble={(id, dx, dy) => page && mark(moveBubble(page, id, dx, dy))}/>
  {/if}
 </section>

 <aside class="mv-ed__right">
  {#if tray === 'layouts'}
   <div class="mv-ed__section-head mv-ed__inspector-head"><div><strong>{t('page.number', {number: page?.number || 1})}</strong><small>{pageHasWork ? t('layout.page_kept', {count: page?.panels.length || 0}) : t('empty.page')}</small></div></div>
  {:else if tray === 'history'}
   <div class="mv-ed__section-head mv-ed__inspector-head"><div><strong>{t('history.title')}</strong>{#if page}<small>{t('page.number', {number: page.number})}</small>{/if}</div><button type="button" class="mv-iconlink" aria-label={t('close')} onclick={() => tray = 'properties'}><i class="ti ti-x" aria-hidden="true"></i></button></div>
   <div class="mv-ed__inspector-scroll">
    {#each versions as v (v.id)}<article class="mv-ed__version"><strong>{t('history.version', {number: v.number})}</strong><time datetime={v.created_at}>{new Intl.DateTimeFormat(initial.lang, {dateStyle: 'medium', timeStyle: 'short'}).format(new Date(v.created_at))}</time><p>{historyAction(v.action)}</p><Button small disabled={!initial.permissions.art && !initial.permissions.letter || busy} onclick={() => revert(v)}>{t('history.revert')}</Button></article>{:else}<p class="mv-ed__help mv-ed__pad">{t('history.empty')}</p>{/each}
   </div>
  {:else if tray === 'variations'}
   <div class="mv-ed__section-head mv-ed__inspector-head"><div><strong>{t('variations.heading', {panel: panelName})}</strong><small>{variationStatus}</small></div><button type="button" class="mv-iconlink" aria-label={t('close')} onclick={() => tray = 'properties'}><i class="ti ti-x" aria-hidden="true"></i></button></div>
   <div class="mv-ed__inspector-scroll">
    {#if jobRunning}<progress class="mv-ed__progress" max="1" value={activeJob?.progress || 0} aria-label={t('progress')}></progress>{/if}
    <fieldset class="mv-vars mv-ed__vars">
     <legend class="mv-sr-only">{t('variations.pick')}</legend>
     <div class="mv-vars__grid">
      {#each [0, 1, 2, 3] as i}
       {@const c = candidates[i]}
       {#if c}
        <label class="mv-ed__var" class:selected={candidateIndex === c.index}>
         <span class="mv-ed__var-frame"><input type="radio" name="variation" value={c.index} bind:group={candidateIndex} aria-keyshortcuts={String(i + 1)} aria-label={t('variations.candidate', {number: i + 1})}/>{#if c.url}<img class="mv-ed__var-image" src={c.url} alt=""/>{/if}<span class="mv-ed__var-caption" aria-hidden="true">{t('variations.var', {number: i + 1})}</span><span class="mv-ed__var-key" aria-hidden="true">{i + 1}</span></span>
         {#if c.lock_score !== undefined}<span class="mv-ed__var-lock" class:drift={c.lock_score < .9}>{t('lock_score', {score: Math.round(c.lock_score * 100)})}{#if c.lock_score < .9} · {t('variations.drift')}{/if}</span>{/if}
        </label>
       {:else}
        <span class="mv-ed__var mv-ed__var--empty" aria-hidden="true"><span class="mv-ed__var-frame" class:busy={jobRunning}><span class="mv-ed__var-caption">{t('variations.var', {number: i + 1})}</span><span class="mv-ed__var-key">{i + 1}</span></span></span>
       {/if}
      {/each}
     </div>
    </fieldset>
    <section class="mv-ed__property">
     <div class="mv-ed__property-head"><strong>{t('variations.layer')}</strong></div>
     <div class="mv-ed__reroll">{#each rerollLayers as l (l.id)}<button type="button" class="mv-ed__reroll-button" title={label(l)} disabled={busy || !initial.permissions.generate || l.locked} onclick={() => void generate('layer', l.id)}><i class="ti ti-refresh" aria-hidden="true"></i><span>{rerollName(l)}</span></button>{/each}</div>
     <p class="mv-ed__help">{drift ? t('variations.drift_hint', {number: candidates.indexOf(drift) + 1, name: driftName || t('layer.character'), score: Math.round((drift.lock_score || 0) * 100)}) : t('variations.layer_hint')}</p>
    </section>
    <section class="mv-ed__property">
     <div class="mv-ed__property-head"><strong>{t('queue')}</strong><a class="mv-ed__soon-link" href="/queue">{t('queue.open')}</a></div>
     {#if recent.length}
      <ul class="mv-ed__queue">
       {#each recent.slice(0, 5) as j (j.id)}
        <li class={`mv-ed__queue-row mv-ed__queue-row--${j.state}`}><i class={`ti ti-${queueIcon(j)}`} aria-hidden="true"></i><span class="mv-ed__mono">{jobPanel(j)}</span><span class="mv-ed__queue-label">{queueLabel(j)}</span><span class="mv-ed__mono mv-ed__queue-state">{queueState(j)}</span></li>
       {/each}
      </ul>
     {:else}<p class="mv-ed__help">{t('queue.empty')}</p>{/if}
    </section>
   </div>
   <div class="mv-ed__inspector-actions mv-ed__inspector-actions--split">
    <Button icon="refresh" disabled={busy || !panel || !initial.permissions.generate || jobRunning} onclick={() => void generate('panel')}>{t('variations.reroll')}</Button>
    <Button variant="primary" icon="check" disabled={busy || !candidates.length} onclick={() => void keep()}>{t('variations.keep', {number: candidates.findIndex(c => c.index === candidateIndex) + 1 || 1})}<span class="mv-ed__kbd" aria-hidden="true">↵</span></Button>
   </div>
  {:else if tool === 'region'}
   <div class="mv-ed__section-head mv-ed__inspector-head"><div><strong>{t('region.title')}</strong><small>{t('region.subtitle', {panel: panelName})}</small></div><button type="button" class="mv-iconlink" aria-label={t('close')} onclick={closeRegion}><i class="ti ti-x" aria-hidden="true"></i></button></div>
   <div class="mv-ed__inspector-scroll">
    <div class="mv-ed__property">
     <div class="mv-ed__property-head"><strong>{t('region.select')}</strong><button type="button" class="mv-ed__soon-link" disabled={!mask || busy || !!candidates.length} onclick={() => clearMask++}>{t('region.clear_short')}</button></div>
     <div class="mv-seg mv-seg--full mv-ed__tools" role="group" aria-label={t('region.select')}>
      {#each MASK_TOOLS as [k, icon]}<button type="button" class="mv-seg__item" aria-pressed={maskTool === k} onclick={() => maskTool = k}><i class={`ti ti-${icon}`} aria-hidden="true"></i>{t(`region.${k}`)}</button>{/each}
      <button type="button" class="mv-seg__item" disabled title={t('soon')}><i class="ti ti-click" aria-hidden="true"></i>{t('region.object')}<span class="mv-soon">{t('soon')}</span></button>
     </div>
     <label class="mv-ed__feather"><span class="mv-ed__feather-head"><span>{t('region.feather')}</span><span class="mv-ed__mono">{feather} px</span></span><input class="mv-range mv-ed__thin-range" type="range" min="0" max="64" bind:value={feather} style:--mv-p={`${feather / 64 * 100}%`}/></label>
    </div>
    <label class="mv-field mv-ed__property"><span class="mv-ed__property-head"><strong>{t('region.prompt')}</strong></span><textarea class="mv-textarea" rows="3" maxlength="4000" bind:value={regionPrompt}></textarea></label>
    <section class="mv-ed__property">
     <div class="mv-ed__property-head"><strong>{t('region.keep')}</strong></div>
     <label class="mv-check"><input type="checkbox" disabled/><span class="mv-check__box"><i class="ti ti-check" aria-hidden="true"></i></span><span>{t('region.character_lock')} <span class="mv-soon">{t('soon')}</span></span></label>
     <label class="mv-check"><input type="checkbox" disabled/><span class="mv-check__box"><i class="ti ti-check" aria-hidden="true"></i></span><span>{t('region.match_style')} <span class="mv-soon">{t('soon')}</span></span></label>
     <p class="mv-ed__guarantee"><i class="ti ti-shield-check" aria-hidden="true"></i><span>{t('region.guarantee')}</span></p>
    </section>
    <details class="mv-ed__property">
     <summary>{t('region.options')}</summary>
     <p class="mv-ed__help">{t('region.keyboard')}</p>
     <label class="mv-field"><span class="mv-field__label">{t('region.brush_size')}</span><input class="mv-input" type="number" min="1" max="512" bind:value={brushSize}/></label>
     <span class="mv-field__label">{t('region.rectangle')}</span>
     <div class="mv-ed__geometry">{#each ['x', 'y', 'w', 'h'] as axis}<label><span>{axis.toUpperCase()}</span><input class="mv-input" type="number" value={rectangle[axis as keyof Rect]} onchange={(e) => rectangle = {...rectangle, [axis]: Number(e.currentTarget.value)}}/></label>{/each}</div>
     <Button small onclick={() => rectangleSelection = {...rectangle}}>{t('region.select_rectangle')}</Button>
    </details>
    {#if activeJob && !candidates.length}<p class="mv-ed__property mv-ed__help" role="status">{variationStatus}</p>{/if}
   </div>
   <div class="mv-ed__generate">
    <div class="mv-route"><i class="ti ti-route" aria-hidden="true"></i><strong>{t('auto')}</strong><span class="mv-route__pick">→ {activeJob?.model_name ? activeJob.model_name : routeText}</span></div>
    {#if candidates.length}
     <div class="mv-ed__region-buttons">
      <Button icon="arrow-back-up" label={t('undo')} disabled={busy} onclick={discardRegionResult}/>
      <Button disabled={busy} onclick={discardRegionResult}>{t('region.try_again')}</Button>
      <Button variant="primary" icon="check" disabled={busy || !candidates.length} onclick={() => void keep()}>{t('variations.keep', {number: candidates.findIndex(c => c.index === candidateIndex) + 1 || 1})}</Button>
     </div>
    {:else}
     <Button variant="primary" full busy={busy || jobRunning} disabled={busy || jobRunning || !mask || !regionPrompt.trim() || !initial.permissions.generate} onclick={() => void generate('region')}>{t('region.generate')}</Button>
    {/if}
   </div>
  {:else if mode === 'lettering'}
   <div class="mv-ed__section-head mv-ed__inspector-head mv-ed__inspector-head--stack">
    <div class="mv-ed__head-row"><strong>{t('lettering')} · {t('page.number', {number: page?.number || 1})}</strong><button type="button" class="mv-iconlink" aria-label={t('bubble.add_kind', {kind: t(`bubble.${currentKind}`)})} title={t('bubble.add_kind', {kind: t(`bubble.${currentKind}`)})} disabled={!writableLetter || !page?.panels.length} onclick={() => addBubble(currentKind)}><i class="ti ti-plus" aria-hidden="true"></i></button></div>
    <div class="mv-ed__bubble-types" role="group" aria-label={t('bubble.type')}>
     {#each KINDS as kind}<button type="button" class="mv-ed__bubble-type" aria-pressed={currentKind === kind} disabled={!writableLetter} onclick={() => chooseKind(kind)}><i class={`ti ti-${KIND_ICONS[kind]}`} aria-hidden="true"></i>{t(`bubble.${kind}`)}</button>{/each}
    </div>
   </div>
   <fieldset class="mv-ed__inspector-scroll mv-ed__fieldset" disabled={!writableLetter}>
    {#if bubble}
     {@const speaker = characters.find(c => c.id === bubble.speaker_character_id)}
     <div class="mv-ed__property">
      <div class="mv-ed__property-head"><strong>{t('bubble.selected', {number: bubble.order + 1})}</strong></div>
      <label class="mv-field"><span class="mv-field__label">{t('speaker')}</span>
       <span class="mv-select-wrap mv-ed__speaker">{#if speaker}<span class={`mv-avatar mv-avatar--xs mv-avatar--c${avatarTone(speaker.id)}`} aria-hidden="true">{initials(speaker.name)}</span>{/if}<select class="mv-select" class:with-avatar={!!speaker} value={bubble.speaker_character_id || ''} onchange={(e) => editBubble(b => b.speaker_character_id = e.currentTarget.value || undefined)}><option value="">{t('none')}</option>{#each characters as c (c.id)}<option value={c.id}>{c.name}</option>{/each}</select><i class="ti ti-chevron-down" aria-hidden="true"></i></span>
      </label>
      <label class="mv-field"><span class="mv-field__label">{t('text')}</span><textarea class="mv-textarea" rows="2" maxlength="2000" value={selectedText.text} oninput={(e) => { const composing = (e as InputEvent).isComposing; editBubble(b => b.texts[lang] = {...selectedText, text: e.currentTarget.value, source: 'human', status: 'draft'}); if (composing) { clearTimeout(previewTimer); previewAbort?.abort(); } }} oncompositionend={schedulePreview}></textarea></label>
      <div class="mv-ed__font-row">
       <label class="mv-field"><span class="mv-field__label">{t('font')}</span><span class="mv-select-wrap"><select class="mv-select" value={bubble.style.font || ''} onchange={(e) => editBubble(b => b.style.font = e.currentTarget.value)}>{#if !bubble.style.font}<option value="">{t('font.auto', {family: FONT_FAMILIES[defaultFont(lang, bubble.kind)]})}</option>{/if}{#each FONT_KEYS as k}<option value={k}>{FONT_FAMILIES[k]}</option>{/each}</select><i class="ti ti-chevron-down" aria-hidden="true"></i></span></label>
       <label class="mv-field"><span class="mv-field__label">{t('font_size')}</span><span class="mv-ed__unit"><input class="mv-input" type="number" min="1" max="200" step="1" inputmode="numeric" aria-label={t('font_size_pt')} value={currentPt || ''} placeholder={autoPt ? String(autoPt) : ''} onchange={(e) => setSizePt(e.currentTarget.value)}/><span aria-hidden="true">{initial.print?.width_mm ? t('pt') : 'px'}</span></span></label>
      </div>
      <label class="mv-toggle mv-ed__letter-toggle"><span>{t('autofit')}</span><input type="checkbox" role="switch" checked={bubble.style.auto_fit} onchange={(e) => editBubble(b => b.style.auto_fit = e.currentTarget.checked)}/><span class="mv-toggle__track" aria-hidden="true"></span></label>
      <label class="mv-toggle mv-ed__letter-toggle"><span>{t('avoid_faces')} <span class="mv-soon">{t('soon')}</span></span><input type="checkbox" role="switch" disabled/><span class="mv-toggle__track" aria-hidden="true"></span></label>
      {#if verticalOK}<label class="mv-toggle mv-ed__letter-toggle"><span>{t('vertical')}</span><input type="checkbox" role="switch" checked={bubble.vertical} onchange={(e) => editBubble(b => b.vertical = e.currentTarget.checked)}/><span class="mv-toggle__track" aria-hidden="true"></span></label>{/if}
      <details>
       <summary>{t('advanced')}</summary>
       {#if textLayer}<label class="mv-field mv-ed__property"><span class="mv-field__label">{t('lettering')} · {t('opacity')}</span><input class="mv-range" type="range" min="0" max="1" step=".05" value={textLayer.opacity} disabled={!writableArt || textLayer.locked} oninput={(e) => { layerID = textLayer!.id; editLayer(l => l.opacity = Number(e.currentTarget.value)); }}/></label>{/if}
       <label class="mv-check"><input type="checkbox" checked={bubble.tail.enabled} onchange={(e) => editBubble(b => b.tail.enabled = e.currentTarget.checked)}/><span class="mv-check__box"><i class="ti ti-check" aria-hidden="true"></i></span><span>{t('tail')}</span></label>
       <details><summary>{t('geometry')}</summary><div class="mv-ed__geometry">{#each ['x', 'y', 'w', 'h'] as axis}<label><span>{axis.toUpperCase()}</span><input class="mv-input" type="number" value={bubble.box[axis as keyof Rect]} onchange={(e) => editBubble(b => b.box[axis as keyof Rect] = Number(e.currentTarget.value))}/></label>{/each}</div><div class="mv-ed__field-pair"><label>{t('tail_x')}<input class="mv-input" type="number" value={bubble.tail.tip.x} onchange={(e) => editBubble(b => b.tail.tip.x = Number(e.currentTarget.value))}/></label><label>{t('tail_y')}<input class="mv-input" type="number" value={bubble.tail.tip.y} onchange={(e) => editBubble(b => b.tail.tip.y = Number(e.currentTarget.value))}/></label></div><label>{t('rotation')}<input class="mv-input" type="number" value={bubble.style.rotate || 0} onchange={(e) => editBubble(b => b.style.rotate = Number(e.currentTarget.value))}/></label></details>
       <details><summary>{t('ruby.title')}</summary><div class="mv-ed__geometry"><label>{t('ruby.start')}<input class="mv-input" type="number" min="0" bind:value={rubyStart}/></label><label>{t('ruby.end')}<input class="mv-input" type="number" min="1" bind:value={rubyEnd}/></label></div><label>{t('ruby.reading')}<input class="mv-input" bind:value={rubyText}/></label><Button small onclick={addRuby}>{t('ruby.add')}</Button>{#each selectedText.ruby || [] as r, i}<p>{r.start}–{r.end} · {r.text}<Button small icon="x" label={t('ruby.remove')} onclick={() => editBubble(b => b.texts[lang] = {...selectedText, ruby: selectedText.ruby?.filter((_, j) => j !== i)})}/></p>{/each}</details>
       <Button variant="danger" small icon="trash" onclick={() => { update(p => p.bubbles = p.bubbles.filter(b => b.id !== bubbleID)); bubbleID = ''; }}>{t('bubble.delete')}</Button>
      </details>
     </div>
    {/if}
    <section class="mv-ed__property">
     <div class="mv-ed__property-head"><strong>{t('reading_order')}</strong><span class="mv-ed__mono">{page?.direction === 'rtl' ? t('rtl') : t('ltr')}</span></div>
     {#if sortedBubbles.length}
      <ol class="mv-ed__reading">
       {#each sortedBubbles as b, i (b.id)}
        {@const who = b.kind === 'caption' ? t('reading.caption') : b.kind === 'sfx' ? t('reading.sfx') : (characterName(b.speaker_character_id).split(/\s+/)[0] || t('reading.none'))}
        <li class="mv-ed__reading-row" class:selected={bubbleID === b.id} class:dragging={dragBubble === b.id} ondragover={(e) => { if (dragBubble) e.preventDefault(); }} ondrop={(e) => { e.preventDefault(); if (dragBubble) dropBubble(dragBubble, i); dragBubble = ''; }}>
         <button type="button" class="mv-ed__reading-select" aria-pressed={bubbleID === b.id} onclick={() => selectBubble(b.id)}>
          <span class="mv-ed__order" aria-hidden="true">{b.order + 1}</span>
          <span class="mv-ed__reading-who">{who}</span>
          {#if b.texts[lang]?.text}<span class="mv-ed__reading-text">{b.texts[lang].text}</span>{:else}<span class="mv-ed__reading-text is-empty">{t('reading.empty')}</span>{/if}
         </button>
         <button type="button" class="mv-ed__grip" data-grip={b.id} draggable={page && canReorderBubble(page, b.id, i ? -1 : 1) || undefined} aria-label={t('reading.move', {number: b.order + 1})} disabled={!page || !initial.permissions.letter || initial.chapter.locked || busy || !bubbleWritable(page, b)} onkeydown={(e) => gripKey(e, b.id)} ondragstart={(e) => { dragBubble = b.id; e.dataTransfer?.setData('text/plain', b.id); }} ondragend={() => dragBubble = ''}><i class="ti ti-grip-vertical" aria-hidden="true"></i></button>
        </li>
       {/each}
      </ol>
     {:else}<p class="mv-ed__help">{t('bubble.empty')}</p>{/if}
    </section>
   </fieldset>
   <div class="mv-ed__inspector-actions"><Button full icon="wand" disabled={!writableLetter || !page?.bubbles.length || page?.panels.some(p => p.locked || p.layers.some(l => l.kind === 'lettering' && l.locked))} onclick={() => void preview(true)}>{t('reflow')}</Button></div>
  {:else if !panel}
   <div class="mv-ed__section-head mv-ed__inspector-head"><div><strong>{page ? t('page.number', {number: page.number}) : t('properties')}</strong><small>{page ? t('page.panels', {count: page.panels.length}) : ''}</small></div></div>
   <div class="mv-ed__inspector-scroll">
    <div class="mv-ed__no-panel">
     <i class="ti ti-layout-grid-add" aria-hidden="true"></i>
     <strong>{page?.panels.length ? t('empty.panel') : t('panel.empty_title')}</strong>
     {#if page && !page.panels.length}<p class="mv-ed__help">{t('panel.empty_body')}</p><Button variant="primary" icon="layout-grid" disabled={!initial.permissions.art || busy || page.locked} onclick={() => void openLayouts()}>{t('panel.choose_layout')}</Button>{/if}
    </div>
   </div>
  {:else}
   <div class="mv-ed__section-head mv-ed__inspector-head">
    <div><strong>{page ? t('panel.label', {page: page.number, panel: panelIndex + 1}) : t('properties')}</strong><small title={panel.prompt}>{shortTitle(panel, initial.script_actions) || t('panel.no_prompt')}</small></div>
    <label class="mv-toggle mv-ed__lock-switch"><span>{t('lock')}</span><input type="checkbox" role="switch" checked={!!panel?.locked} disabled={!initial.permissions.lock || !panel || busy} onchange={(e) => { e.currentTarget.checked = !!panel?.locked; void lockTarget('panel'); }}/><span class="mv-toggle__track" aria-hidden="true"></span></label>
   </div>
   <fieldset class="mv-ed__inspector-scroll mv-ed__fieldset" disabled={!page || busy}>
    <div class="mv-ed__property">
     <div class="mv-ed__property-head"><strong>{t('scope')}</strong></div>
     <div class="mv-seg mv-seg--full mv-ed__scope"><button type="button" class="mv-seg__item" disabled title={t('soon')}>{t('scope.page')}<span class="mv-soon">{t('soon')}</span></button><button type="button" class="mv-seg__item" aria-pressed={scope === 'panel'} onclick={() => scope = 'panel'}>{t('scope.panel')}</button><button type="button" class="mv-seg__item" aria-pressed={scope === 'layer'} disabled={!layer || layer.kind === 'lettering'} onclick={() => scope = 'layer'}>{t('scope.layer')}</button><button type="button" class="mv-seg__item" aria-pressed={scope === 'region'} disabled={!layer?.asset} onclick={makeRegion}>{t('scope.region')}</button></div>
     <p class="mv-ed__help">{scope === 'layer' ? t('scope.layer_hint') : t('scope.panel_hint', {panel: panelName})}</p>
    </div>
    <label class="mv-field mv-ed__property"><span class="mv-ed__property-head"><strong>{t('prompt')}</strong>{#if promptTag}<span class="mv-ed__tag">{promptTag}</span>{/if}</span><textarea class="mv-textarea" rows="3" maxlength="4000" value={panel?.prompt || ''} disabled={!writableArt} oninput={(e) => editPanel(p => { p.prompt = e.currentTarget.value; p.prompt_source = 'user'; })}></textarea></label>
    <section class="mv-ed__property">
     <div class="mv-ed__property-head"><strong>{t('characters')}</strong></div>
     <div class="mv-ed__chips">
      {#each characterLayers as l (l.id)}{@render chip(l)}{/each}
      <label class="mv-ed__chip mv-ed__chip--add"><i class="ti ti-plus" aria-hidden="true"></i><select aria-label={t('character.add')} value="" disabled={!writableArt} onchange={(e) => { const id = e.currentTarget.value; e.currentTarget.value = ''; addCharacter(id); }}><option value="">{t('character.add_short')}</option>{#each characters.filter(c => !panel?.layers.some(l => l.character_id === c.id)) as c (c.id)}<option value={c.id}>{c.name}</option>{/each}</select></label>
     </div>
    </section>
    <section class="mv-ed__property">
     <div class="mv-ed__property-head"><strong>{t('pose_camera')}</strong></div>
     <div class="mv-ed__poses">{#each ['leap', 'crouch', 'run'] as pose}<button type="button" class="mv-ed__pose" disabled title={t('soon')}><span>{t(`pose.${pose}`)}</span><span class="mv-soon">{t('soon')}</span></button>{/each}<button type="button" class="mv-ed__pose mv-ed__pose--sketch" disabled title={t('soon')}><i class="ti ti-scribble" aria-hidden="true"></i>{t('sketch')}<span class="mv-soon">{t('soon')}</span></button></div>
     <div class="mv-seg mv-seg--full" role="group" aria-label={t('pose_camera')}>{#each ['low', 'eye', 'high', 'bird'] as camera}<button type="button" class="mv-seg__item" aria-pressed={(cameraOption(panel?.camera) || 'eye') === camera} disabled={!writableArt} onclick={() => editPanel(p => p.camera = camera)}>{t(`camera.${camera}`)}</button>{/each}</div>
     <div class="mv-seg mv-seg--full" role="group" aria-label={t('pose_camera')}>{#each ['wide', 'medium', 'close'] as shot}<button type="button" class="mv-seg__item" aria-pressed={(shotOption(panel?.shot) || 'medium') === shot} disabled={!writableArt} onclick={() => editPanel(p => p.shot = shot)}>{t(`shot.${shot}`)}</button>{/each}</div>
    </section>
    <label class="mv-field mv-ed__property"><span class="mv-ed__property-head"><strong>{t('style')}</strong></span><span class="mv-select-wrap"><select class="mv-select" value={panel?.style || initial.project.style} disabled={!writableArt} onchange={(e) => editPanel(p => p.style = e.currentTarget.value)}>{#each styleOptions as o (o.id)}<option value={o.id}>{o.label}</option>{/each}</select><i class="ti ti-chevron-down" aria-hidden="true"></i></span></label>
    {#if layer}<details class="mv-ed__property" data-layer-properties><summary>{t('layer.properties')} · {label(layer)}</summary><label>{t('opacity')}<input class="mv-range" type="range" min="0" max="1" step=".05" value={layer.opacity} disabled={!writableArt || layer.locked} oninput={(e) => editLayer(l => l.opacity = Number(e.currentTarget.value))}/></label><label>{t('blend')}<span class="mv-select-wrap"><select class="mv-select" value={layer.blend || 'normal'} disabled={!writableArt || layer.locked} onchange={(e) => editLayer(l => l.blend = e.currentTarget.value as Layer['blend'])}>{#each ['normal', 'multiply', 'screen'] as blend}<option value={blend}>{t(`blend.${blend}`)}</option>{/each}</select><i class="ti ti-chevron-down" aria-hidden="true"></i></span></label><div class="mv-ed__geometry">{#each ['offset_x', 'offset_y', 'scale', 'rotate'] as property}<label>{t(`transform.${property}`)}<input class="mv-input" type="number" step={property === 'scale' ? '.1' : '1'} value={layer.transform[property as keyof Layer['transform']] || 0} disabled={!writableArt || layer.locked} onchange={(e) => editLayer(l => l.transform = {...l.transform, [property]: Number(e.currentTarget.value)})}/></label>{/each}</div><label class="mv-check"><input type="checkbox" checked={layer.transform.flip_x || false} disabled={!writableArt || layer.locked} onchange={(e) => editLayer(l => l.transform.flip_x = e.currentTarget.checked)}/><span class="mv-check__box"><i class="ti ti-check" aria-hidden="true"></i></span><span>{t('flip')}</span></label></details>{/if}
    {#if panel}<details class="mv-ed__property"><summary>{t('geometry')}</summary><p>{t('geometry.hint')}</p>{#each panel.points as point, i}<div class="mv-ed__field-pair"><label>{i + 1} X<input class="mv-input" type="number" value={point.x} disabled={!writableArt} onchange={(e) => editPanel(p => p.points[i].x = Number(e.currentTarget.value))}/></label><label>{i + 1} Y<input class="mv-input" type="number" value={point.y} disabled={!writableArt} onchange={(e) => editPanel(p => p.points[i].y = Number(e.currentTarget.value))}/></label></div>{/each}</details>{/if}
   </fieldset>
   <div class="mv-ed__generate">
    <div class="mv-route"><i class="ti ti-route" aria-hidden="true"></i><strong>{t('auto')}</strong><span class="mv-route__pick">→ {routeText}</span></div>
    <Button full variant="primary" icon="sparkles" busy={busy} disabled={!page || !panel || !initial.permissions.generate || page.locked || !!panel.locked || !!layer?.locked && scope === 'layer'} onclick={() => void generate()}>{t(scope === 'layer' ? 'regenerate_layer' : 'regenerate_panel')}<span class="mv-ed__kbd">⌘↵</span></Button>
    <span class="mv-ed__help mv-ed__center">{routeHelp}</span>
   </div>
  {/if}
 </aside>

 <footer class="mv-ed__footer">
  {#if page}<span>{t('page.of', {number: page.number, count: pages.length})}</span>{/if}
  <span>{initial.print?.format_label || initial.project.format}</span>
  <span>{initial.print?.size_label || initial.project.page_size}</span>
  {#if page}<span class="mv-ed__save-state" data-save-state={saveState} role="status"><i class={`ti ti-${saveState === 'saved' ? 'circle-check' : saveState === 'saving' ? 'loader-2' : saveState === 'dirty' ? 'point-filled' : 'alert-triangle'}`} aria-hidden="true"></i>{saveText}</span>{/if}
  <span class="mv-ed__footer-end">{footerEnd}</span>
 </footer>

 <dialog bind:this={layoutDialog} class="mv-modal mv-ed__layout-dialog" aria-labelledby="editor-layout-title" aria-describedby="editor-layout-hint" onclose={() => { if (tray === 'layouts') tray = 'properties'; layoutTrigger?.focus(); }}>
  {#if page}
   <div class="mv-ed__layout-browser">
    <div class="mv-modal__head"><h1 id="editor-layout-title">{t('layout.title', {number: page.number})}</h1><div class="mv-seg"><button type="button" class="mv-seg__item" aria-pressed={layoutFilter === 'suggested'} onclick={() => layoutFilter = 'suggested'}>{t('layout.suggested')}</button><button type="button" class="mv-seg__item" aria-pressed={layoutFilter === 'all'} onclick={() => layoutFilter = 'all'}>{t('layout.all')}</button><button type="button" class="mv-seg__item mv-ed__layout-saved" disabled>{t('layout.saved')}<span class="mv-soon">{t('soon')}</span></button></div></div>
    <p class="mv-ed__layout-hint" id="editor-layout-hint"><i class="ti ti-sparkles" aria-hidden="true"></i>{layoutHint}</p>
    <div class="mv-ed__layout-body">
     <div class="mv-ed__layout-grid" role="radiogroup" aria-label={t('layouts')}>
      {#each (layoutFilter === 'all' ? pickerLayouts : rankedLayouts.filter(l => l.suggested)) as l (l.id)}
       <button type="button" class="mv-ed__layout-card" class:selected={pickedLayout === l.id} role="radio" aria-checked={pickedLayout === l.id} aria-label={`${l.name}, ${t('page.panels', {count: l.panels})}`} disabled={!canUseLayout(page, l)} onclick={() => pickedLayout = l.id}><svg viewBox={`0 0 ${page.width} ${page.height}`} preserveAspectRatio="none" aria-hidden="true"><rect width={page.width} height={page.height} class="mv-ed__layout-paper"/>{#each layoutPoints(l, tilt) as points}<polygon points={points.map(p => `${p.x},${p.y}`).join(' ')} class="mv-ed__layout-panel"/>{/each}</svg><span aria-hidden="true">{l.name}</span></button>
      {/each}
      <span class="mv-ed__layout-card mv-ed__layout-custom" aria-disabled="true"><span class="mv-ed__layout-custom-tile"><i class="ti ti-grid-dots" aria-hidden="true"></i>{t('layout.custom')}<span class="mv-soon">{t('soon')}</span></span></span>
     </div>
    </div>
    <div class="mv-modal__foot mv-ed__layout-footer"><label class="mv-toggle"><input type="checkbox" role="switch" bind:checked={tilt}/><span class="mv-toggle__track" aria-hidden="true"></span><span>{t('layout.tilt')}</span></label><Button variant="quiet" onclick={closeLayouts}>{t('cancel')}</Button><Button variant="primary" disabled={!writablePage || !activeLayout || !canUseLayout(page, activeLayout)} onclick={chooseLayout}>{t('layout.use')}</Button></div>
   </div>
  {/if}
 </dialog>
 <dialog bind:this={dialog} class="mv-modal mv-ed__dialog" aria-labelledby="editor-confirm-title" aria-describedby="editor-confirm-body" onclose={() => confirmAction = null}><div class="mv-modal__head"><h2 id="editor-confirm-title">{confirmTitle}</h2></div><div class="mv-modal__body"><p id="editor-confirm-body">{confirmBody}</p></div><div class="mv-modal__foot"><Button onclick={() => dialog.close()}>{t('cancel')}</Button><Button variant="danger" onclick={accept}>{t('confirm')}</Button></div></dialog>
</div>
