<!--
  Demo island: proves the Svelte 5 + Vite pipeline end to end (props from the
  server, reactive state, scoped CSS extracted to a file under the strict CSP).
-->
<script lang="ts">
  let { label = 'Panels inked', start = 0 }: { label?: string; start?: number } = $props();
  let count = $state(start);
  const burst = $derived(count > 0 && count % 5 === 0);
</script>

<div class="demo">
  <span class="mv-mono">SVELTE ISLAND</span>
  <strong>{label}: <output>{count}</output></strong>
  <div class="row">
    <button type="button" class="mv-btn mv-btn--primary mv-btn--sm" onclick={() => count++}>
      <i class="ti ti-plus" aria-hidden="true"></i>Ink one
    </button>
    <button type="button" class="mv-btn mv-btn--ghost mv-btn--sm" onclick={() => (count = 0)}>Reset</button>
    {#if burst}<span class="mv-sfx mv-sfx--xs mv-sfx--accent">DON!</span>{/if}
  </div>
</div>

<style>
  .demo {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 14px 16px;
    border: 1px dashed var(--mv-line);
  }
  .row {
    display: flex;
    align-items: center;
    gap: 10px;
  }
</style>
