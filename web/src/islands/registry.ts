/**
 * Island registry. Every island lives in src/islands/<name>/main.ts and
 * exports mount(el, props) returning an unmount function. Vite turns each
 * into its own chunk, loaded only when a page contains
 * <div data-island="<name>" data-props='{...}'>.
 */
export type Unmount = () => void;
export interface IslandModule {
  mount(el: HTMLElement, props: Record<string, unknown>): Unmount | void;
}

const loaders = import.meta.glob<IslandModule>('./*/main.ts');

/** islandNames lists the registered islands. */
export function islandNames(): string[] {
  return Object.keys(loaders).map((p) => p.split('/')[1]).sort();
}

/** parseProps reads data-props JSON, returning {} for missing or invalid JSON. */
export function parseProps(el: HTMLElement): Record<string, unknown> {
  const raw = el.dataset.props;
  if (!raw) return {};
  try {
    const v = JSON.parse(raw);
    return v && typeof v === 'object' ? (v as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}

const mounted = new WeakMap<HTMLElement, Unmount | void>();

/** mountIslands mounts every island under root that is not mounted yet. */
export async function mountIslands(root: ParentNode = document): Promise<number> {
  const els = Array.from(root.querySelectorAll<HTMLElement>('[data-island]')).filter((el) => !mounted.has(el));
  let n = 0;
  await Promise.all(
    els.map(async (el) => {
      const name = el.dataset.island ?? '';
      const load = loaders[`./${name}/main.ts`];
      if (!load) {
        console.warn(`mangaveyra: unknown island "${name}"`);
        return;
      }
      mounted.set(el, undefined);
      try {
        const mod = await load();
        mounted.set(el, mod.mount(el, parseProps(el)));
        el.dataset.islandMounted = 'true';
        n++;
      } catch (err) {
        mounted.delete(el);
        console.error(`mangaveyra: island "${name}" failed`, err);
      }
    }),
  );
  return n;
}

/** unmountIslands tears down islands under root (called before HTMX swaps them out). */
export function unmountIslands(root: ParentNode): void {
  root.querySelectorAll<HTMLElement>('[data-island-mounted]').forEach((el) => {
    const fn = mounted.get(el);
    if (typeof fn === 'function') fn();
    mounted.delete(el);
    delete el.dataset.islandMounted;
  });
}
