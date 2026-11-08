<script lang="ts">
 import {onDestroy} from 'svelte';
 import type {Point} from './types';
 let {ratio,t,onChange,disabled=false}:{ratio:number;t:(key:string)=>string;onChange:(blob:Blob|null)=>void;disabled?:boolean}=$props();
 let canvas:HTMLCanvasElement;let strokes=$state.raw<Point[][]>([]),active:Point[]|null=null,serial=0;
 // Bound both sides even for unusually tall or wide page documents.
 const width=$derived(Math.max(64,Math.round(800*Math.min(1,ratio)))),height=$derived(Math.max(64,Math.round(800/Math.max(1,ratio))));
 function redraw(){if(!canvas)return;const ctx=canvas.getContext('2d');if(!ctx)return;ctx.fillStyle='#fff';ctx.fillRect(0,0,width,height);ctx.strokeStyle='#111';ctx.lineWidth=3;ctx.lineJoin='round';ctx.lineCap='round';for(const stroke of strokes){ctx.beginPath();stroke.forEach((p,i)=>i?ctx.lineTo(p.x*width,p.y*height):ctx.moveTo(p.x*width,p.y*height));ctx.stroke();}}
 function emit(){const n=++serial;if(!strokes.length){onChange(null);return;}canvas.toBlob(blob=>{if(n===serial)onChange(blob);},'image/png');}
 function point(e:PointerEvent):Point{const r=canvas.getBoundingClientRect();return{x:Math.max(0,Math.min(1,(e.clientX-r.left)/r.width)),y:Math.max(0,Math.min(1,(e.clientY-r.top)/r.height))};}
 function down(e:PointerEvent){if(disabled||e.button!==0)return;e.preventDefault();canvas.setPointerCapture(e.pointerId);active=[point(e)];strokes=[...strokes,active];redraw();}
 function move(e:PointerEvent){if(!active||disabled)return;active.push(point(e));redraw();}
 function up(){if(!active)return;active=null;emit();}
 $effect(()=>{void [height,strokes];redraw();});
 onDestroy(()=>serial++);
</script>
<!-- The canvas fills the proof frame; its help and Clear sit below the frame
     so both proofs keep the same size and alignment. -->
<div class="mv-control__draw">
 <canvas bind:this={canvas} {width} {height} onpointerdown={down} onpointermove={move} onpointerup={up} onpointercancel={up} aria-label={t('control.drawing')} aria-describedby="sketch-drawing-help"></canvas>
 <div class="mv-control__draw-tools">
  <p id="sketch-drawing-help" class="mv-ed__help">{t('control.drawing_help')}</p>
  <button type="button" class="mv-btn mv-btn--quiet mv-btn--sm" disabled={disabled||!strokes.length} onclick={()=>{strokes=[];active=null;redraw();emit();}}>{t('control.clear_drawing')}</button>
 </div>
</div>
