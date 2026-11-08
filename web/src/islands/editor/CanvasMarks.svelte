<script lang="ts">
 // Review pins for open comments and the live cursors of the others on the
 // page, drawn over the page proof in page-relative positions (D1/D7).
 import type {Peer, Pin} from './collab';
 let {pins = [], peers = [], t, onPin}: {pins?: Pin[]; peers?: Peer[]; t: (key: string, args?: Record<string, string | number>) => string; onPin: (id: string) => void} = $props();
 const cursors = $derived(peers.filter(p => p.x !== null && p.y !== null));
</script>

<div class="mv-marks">
 {#each pins as p (p.id)}
  <button type="button" class="mv-marks__pin" style:left={`${p.x * 100}%`} style:top={`${p.y * 100}%`} aria-label={t('collab.pin', {number: p.number, panel: p.label})} title={t('collab.pin', {number: p.number, panel: p.label})} onclick={() => onPin(p.id)}>{p.number}</button>
 {/each}
 {#each cursors as c (c.conn)}
  <div class={`mv-marks__cursor mv-marks__cursor--c${c.color}`} style:left={`${(c.x || 0) * 100}%`} style:top={`${(c.y || 0) * 100}%`} aria-hidden="true">
   <i class="ti ti-pointer-filled"></i><span>{c.name}</span>
  </div>
 {/each}
</div>
