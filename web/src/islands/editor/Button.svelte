<script lang="ts">
 import type {Snippet} from 'svelte';
 let {children,onclick,icon='',label='',variant='secondary',disabled=false,busy=false,small=false,full=false,type='button'}:{children?:Snippet;onclick?:(event:MouseEvent)=>void;icon?:string;label?:string;variant?:string;disabled?:boolean;busy?:boolean;small?:boolean;full?:boolean;type?:'button'|'submit'}=$props();
</script>
<button {type} class={`mv-btn mv-btn--${variant}${small?' mv-btn--sm':''}${full?' mv-btn--full':''}`} disabled={disabled||busy} aria-busy={busy} aria-label={label||undefined} title={label||undefined} {onclick}>
 {#if icon}<i class={`ti ti-${busy?'loader-2':icon}`} aria-hidden="true"></i>{/if}{@render children?.()}
</button>
