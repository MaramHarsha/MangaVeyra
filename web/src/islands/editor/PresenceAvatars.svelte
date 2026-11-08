<script lang="ts">
 // D1 top-bar avatars: who is on this page (online ring) and the other
 // members. The stack opens the D7 review drawer.
 import {initials} from './document';
 import type {Avatar} from './collab';
 let {shown = [], more = 0, open = false, t, onToggle}: {shown?: Avatar[]; more?: number; open?: boolean; t: (key: string, args?: Record<string, string | number>) => string; onToggle: () => void} = $props();
 const names = $derived(shown.map(a => a.here ? t('collab.presence.here', {name: a.name}) : a.name).join(', '));
</script>

<button type="button" class="mv-presence" class:empty={!shown.length} aria-pressed={open} aria-label={names ? `${t('collab.review.open')} · ${names}` : t('collab.review.open')} title={names || t('collab.review.open')} onclick={onToggle}>
 {#if shown.length}
  <span class="mv-avatar-stack">
   {#each shown as a (a.id)}<span class={`mv-avatar mv-avatar--sm mv-avatar--c${a.tone}`} class:mv-avatar--online={a.here} aria-hidden="true">{initials(a.name)}</span>{/each}
   {#if more}<span class="mv-avatar-stack__more" aria-hidden="true">+{more}</span>{/if}
  </span>
 {:else}
  <i class="ti ti-users" aria-hidden="true"></i>
 {/if}
</button>
