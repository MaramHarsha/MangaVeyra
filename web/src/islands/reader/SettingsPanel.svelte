<script lang="ts">
 import type { LangLink, Mode, T } from './types';
 import type { ReaderSettings } from './prefs';

 type Shown = Exclude<Mode, 'auto'>;
 let {
  settings, t, webtoon, langs, signedIn, commentsReady, voiceReady, voiceName, modes, mode, onmode, onchange, onclose,
 }: {
  settings: ReaderSettings; t: T; webtoon: boolean; langs: LangLink[]; signedIn: boolean; commentsReady: boolean;
  voiceReady: boolean; voiceName: string; modes: Shown[]; mode: Shown; onmode: (m: Shown) => void;
  onchange: (patch: Partial<ReaderSettings>) => void; onclose: () => void;
 } = $props();

 const dirs = [['rtl', 'settings.rtl'], ['ltr', 'settings.ltr']] as const;
 const bgs = [['ink', 'settings.bg.ink'], ['grey', 'settings.bg.grey'], ['paper', 'settings.bg.paper']] as const;
 const pct = (n: number) => `${Math.round(n * 100)}%`;
</script>

<aside class="mv-rd-set" id="mv-rd-settings" aria-labelledby="mv-rd-set-title">
 <div class="mv-rd-set__head">
  <strong id="mv-rd-set-title" class="mv-rd-set__title">{t('settings.title')}</strong>
  <button type="button" class="mv-btn mv-btn--bare mv-btn--sm mv-rd-set__close" aria-label={t('settings.close')} title={t('settings.close')} onclick={onclose}>
   <i class="ti ti-x" aria-hidden="true"></i>
  </button>
 </div>
 <!-- Phones: the bar has no room for the mode switch, so it opens the panel. -->
 <div class="mv-rd-set__group mv-rd-set__modes" role="group" aria-labelledby="mv-rd-set-mode">
  <span class="mv-rd-set__label" id="mv-rd-set-mode">{t('mode')}</span>
  <div class="mv-seg mv-seg--full mv-rd-seg">
   {#each modes as m}
    <button type="button" class="mv-seg__item" aria-pressed={mode === m} onclick={() => onmode(m)}>{t(`mode.${m}`)}</button>
   {/each}
  </div>
 </div>
 {#if !webtoon}
  <div class="mv-rd-set__group" role="group" aria-labelledby="mv-rd-set-dir">
   <span class="mv-rd-set__label" id="mv-rd-set-dir">{t('settings.direction')}</span>
   <div class="mv-seg mv-seg--full mv-rd-seg">
    {#each dirs as [value, key]}
     <button type="button" class="mv-seg__item" aria-pressed={settings.direction === value}
      onclick={() => onchange({ direction: value })}>{t(key)}</button>
    {/each}
   </div>
  </div>
 {/if}
 <div class="mv-rd-set__group" role="group" aria-labelledby="mv-rd-set-bg">
  <span class="mv-rd-set__label" id="mv-rd-set-bg">{t('settings.background')}</span>
  <div class="mv-seg mv-seg--full mv-rd-seg">
   {#each bgs as [value, key]}
    <button type="button" class="mv-seg__item" aria-pressed={settings.background === value}
     onclick={() => onchange({ background: value })}>{t(key)}</button>
   {/each}
  </div>
 </div>
 <div class="mv-rd-set__sliders">
  <label class="mv-rd-set__slider">
   <span class="mv-rd-set__label mv-rd-set__label--row">{t('settings.zoom')}<output class="mv-mono">{pct(settings.zoom)}</output></span>
   <input class="mv-range" type="range" min="0.5" max="3" step="0.25" value={settings.zoom} style={`--mv-p:${((settings.zoom - 0.5) / 2.5) * 100}%`}
    oninput={(e) => onchange({ zoom: Number((e.currentTarget as HTMLInputElement).value) })} />
  </label>
  <label class="mv-rd-set__slider">
   <span class="mv-rd-set__label mv-rd-set__label--row">{t('settings.brightness')}<output class="mv-mono">{pct(settings.brightness)}</output></span>
   <input class="mv-range" type="range" min="0.4" max="1" step="0.05" value={settings.brightness} style={`--mv-p:${((settings.brightness - 0.4) / 0.6) * 100}%`}
    oninput={(e) => onchange({ brightness: Number((e.currentTarget as HTMLInputElement).value) })} />
  </label>
 </div>
 {#if !webtoon}
  <label class="mv-rd-set__row">
   <span>{t('settings.tap_zones')}</span>
   <span class="mv-toggle"><input type="checkbox" role="switch" checked={settings.tap_zones} onchange={(e) => onchange({ tap_zones: (e.currentTarget as HTMLInputElement).checked })} /><span class="mv-toggle__track" aria-hidden="true"></span></span>
  </label>
 {/if}
 <label class="mv-rd-set__row">
  <span>{t('settings.reduced_motion')}</span>
  <span class="mv-toggle"><input type="checkbox" role="switch" checked={settings.reduced_motion} onchange={(e) => onchange({ reduced_motion: (e.currentTarget as HTMLInputElement).checked })} /><span class="mv-toggle__track" aria-hidden="true"></span></span>
 </label>
 <label class={['mv-rd-set__row', !commentsReady && 'is-soon']} title={commentsReady ? undefined : t('soon_hint')}>
  <span class="mv-rd-set__rowlabel">{t('settings.panel_comments')}{#if !commentsReady}<span class="mv-soon">{t('soon')}</span>{/if}</span>
  <span class="mv-toggle"><input type="checkbox" role="switch" disabled={!commentsReady} checked={commentsReady && settings.panel_comments} onchange={(e) => onchange({ panel_comments: (e.currentTarget as HTMLInputElement).checked })} /><span class="mv-toggle__track" aria-hidden="true"></span></span>
 </label>
 <div class="mv-rd-set__stack">
  <label class="mv-rd-set__row">
   <span>{t('settings.read_aloud')}</span>
   <span class="mv-toggle"><input type="checkbox" role="switch" aria-describedby="mv-rd-voice" disabled={!voiceReady} checked={voiceReady && settings.read_aloud} onchange={(e) => onchange({ read_aloud: (e.currentTarget as HTMLInputElement).checked })} /><span class="mv-toggle__track" aria-hidden="true"></span></span>
  </label>
  {#if !voiceReady}
   <span class="mv-rd-set__hint" id="mv-rd-voice">{t('settings.read_aloud.none')}</span>
  {:else if settings.read_aloud}
   <span class="mv-rd-set__hint" id="mv-rd-voice">{t('settings.read_aloud.voice', { name: voiceName })}</span>
  {/if}
 </div>
 <label class="mv-rd-set__row is-soon" title={t('soon_hint')}>
  <span class="mv-rd-set__rowlabel">{t('settings.living_panels')}<span class="mv-soon">{t('soon')}</span></span>
  <span class="mv-toggle"><input type="checkbox" role="switch" disabled /><span class="mv-toggle__track" aria-hidden="true"></span></span>
 </label>
 {#if langs.length > 0}
  <div class="mv-rd-set__group">
   <span class="mv-rd-set__label" id="mv-rd-set-lang">{t('settings.language')}</span>
   <nav class="mv-seg mv-seg--full mv-rd-seg" aria-labelledby="mv-rd-set-lang">
    {#each langs as l}
     <a class="mv-seg__item" href={l.href} hreflang={l.lang} lang={l.lang} aria-current={l.current ? 'true' : undefined}>{l.label}</a>
    {/each}
   </nav>
  </div>
 {/if}
 <span class="mv-rd-set__saved">{signedIn ? t('settings.saved') : t('settings.saved_device')}</span>
</aside>
