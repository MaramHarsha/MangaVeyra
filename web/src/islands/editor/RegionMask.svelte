<script lang="ts">
 import {onMount} from 'svelte';
 import {maskPoint} from './document';
 import type {Asset,Point,Rect} from './types';
 import Button from './Button.svelte';
 let {asset,candidateURL='',tool='brush',feather=12,size=32,t,onMask,disabled=false,selection=null,initialMask=null}:{asset:Asset;candidateURL?:string;selection?:Rect|null;initialMask?:Blob|null;tool?:string;feather?:number;size?:number;t:(key:string)=>string;onMask:(blob:Blob|null)=>void;disabled?:boolean}=$props();
 const comparisonID=`region-comparison-${Math.random().toString(36).slice(2)}`;let split=$state(50);
 let canvas:HTMLCanvasElement;let ctx:CanvasRenderingContext2D|null=null;let points:Point[]=[];let drawing=false;let dirty=$state(false);
 const point=(e:PointerEvent)=>maskPoint({x:e.clientX,y:e.clientY},canvas.getBoundingClientRect(),asset.width||1,asset.height||1);
 function start(e:PointerEvent){if(disabled||e.button!==0||!ctx)return;canvas.setPointerCapture(e.pointerId);drawing=true;points=[point(e)];ctx.strokeStyle='#ffffff';ctx.fillStyle='#ffffff';ctx.lineWidth=size;ctx.lineCap='round';ctx.lineJoin='round';ctx.beginPath();ctx.moveTo(points[0].x,points[0].y)}
 function move(e:PointerEvent){if(!drawing||!ctx)return;const p=point(e);points.push(p);if(tool==='brush'){ctx.lineTo(p.x,p.y);ctx.stroke()}}
 function end(){if(!drawing||!ctx)return;drawing=false;if(tool==='box'&&points.length){const a=points[0],b=points.at(-1)!;ctx.fillRect(Math.min(a.x,b.x),Math.min(a.y,b.y),Math.abs(b.x-a.x),Math.abs(b.y-a.y))}else if(tool==='lasso'&&points.length>2){ctx.beginPath();ctx.moveTo(points[0].x,points[0].y);for(const p of points.slice(1))ctx.lineTo(p.x,p.y);ctx.closePath();ctx.fill()}else if(points.length===1){ctx.beginPath();ctx.arc(points[0].x,points[0].y,size/2,0,Math.PI*2);ctx.fill()}dirty=true;emit()}
 // Exact-white pixels define the editable core; feathering must preserve it.
 function emit(){const out=document.createElement('canvas');out.width=canvas.width;out.height=canvas.height;const c=out.getContext('2d')!;c.fillStyle='#000000';c.fillRect(0,0,out.width,out.height);if(feather>0){c.filter=`blur(${feather}px)`;c.drawImage(canvas,0,0);c.filter='none'}c.drawImage(canvas,0,0);out.toBlob(onMask,'image/png')}
 function clear(){ctx?.clearRect(0,0,canvas.width,canvas.height);dirty=false;onMask(null)}
 onMount(()=>{ctx=canvas.getContext('2d');if(initialMask)void createImageBitmap(initialMask).then(image=>{if(!ctx)return;ctx.drawImage(image,0,0);const data=ctx.getImageData(0,0,canvas.width,canvas.height);for(let i=0;i<data.data.length;i+=4){data.data[i+3]=data.data[i];data.data[i]=255;data.data[i+1]=255;data.data[i+2]=255}ctx.putImageData(data,0,0);image.close();dirty=true});return()=>{ctx=null}});
 $effect(()=>{if(selection&&ctx){ctx.clearRect(0,0,canvas.width,canvas.height);ctx.fillStyle='#ffffff';ctx.fillRect(Math.max(0,selection.x),Math.max(0,selection.y),Math.max(1,selection.w),Math.max(1,selection.h));dirty=true;emit()}});
 $effect(()=>{if(feather>=0&&dirty&&canvas)emit()});
</script>
<div class="mv-ed__region-proof">
 <img src={asset.url} alt={t('region.before')} width={asset.width} height={asset.height}/>
 {#if candidateURL}<svg class="mv-ed__region-comparison" viewBox={`0 0 ${asset.width||1} ${asset.height||1}`} aria-hidden="true"><defs><clipPath id={comparisonID}><rect x={(asset.width||1)*split/100} y="0" width={(asset.width||1)*(1-split/100)} height={asset.height||1}/></clipPath></defs><image href={candidateURL} width={asset.width||1} height={asset.height||1} clip-path={`url(#${comparisonID})`} preserveAspectRatio="none"/><line x1={(asset.width||1)*split/100} x2={(asset.width||1)*split/100} y1="0" y2={asset.height||1}/></svg>{/if}
 <div class="mv-ed__comparison-labels" aria-hidden="true"><span>{t('region.before')}</span><span>{candidateURL?t('region.after'):t('region.selection')}</span></div>
 <canvas bind:this={canvas} width={asset.width||1} height={asset.height||1} onpointerdown={start} onpointermove={move} onpointerup={end} onpointercancel={end} aria-label={t('region.paint')} tabindex="0"></canvas>
</div>
{#if candidateURL}<label class="mv-ed__comparison-range"><span>{t('region.compare')}</span><input class="mv-range" type="range" min="0" max="100" bind:value={split}/></label>{/if}
<div class="mv-ed__region-actions"><Button icon="eraser" onclick={clear} disabled={!dirty||disabled}>{t('region.clear')}</Button><p>{t('region.keyboard')}</p></div>
