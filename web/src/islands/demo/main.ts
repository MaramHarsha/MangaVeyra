import { mount as svelteMount, unmount } from 'svelte';
import Demo from './Demo.svelte';

/** mount renders the demo island into el and returns its teardown. */
export function mount(el: HTMLElement, props: Record<string, unknown>): () => void {
  el.replaceChildren(); // drop the server-rendered placeholder
  const app = svelteMount(Demo, {
    target: el,
    props: { label: String(props.label ?? 'Panels inked'), start: Number(props.start ?? 0) },
  });
  return () => void unmount(app);
}
