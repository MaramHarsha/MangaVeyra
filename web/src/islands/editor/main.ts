import {mount as mountSvelte,unmount} from 'svelte';
import '../../styles/editor.css';
import Editor from './Editor.svelte';
import type {Props} from './types';
export function mount(el:HTMLElement,props:Record<string,unknown>):()=>void {el.replaceChildren();const app=mountSvelte(Editor,{target:el,props:props as unknown as Props});return ()=>void unmount(app)}
