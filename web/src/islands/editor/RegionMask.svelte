<script lang="ts">
 import {onMount,untrack} from 'svelte';
 import {maskPoint} from './document';
 import type {Asset,Point,Rect} from './types';
 let {asset,candidateURL='',tool='brush',feather=12,size=32,label='',t,onMask,onDisplay,disabled=false,selection=null,initialMask=null,clearKey=0}:{asset:Asset;candidateURL?:string;clearKey?:number;selection?:Rect|null;initialMask?:Blob|null;tool?:string;feather?:number;size?:number;label?:string;t:(key:string,args?:Record<string,string|number>)=>string;onMask:(blob:Blob|null)=>void;onDisplay?:(width:number)=>void;disabled?:boolean}=$props();
 const comparisonID=`region-comparison-${Math.random().toString(36).slice(2)}`;
 const width=$derived(asset.width||1),height=$derived(asset.height||1);
 let split=$state(50);
 // The mask lives off screen in exact layer pixels (white = change); the
 // visible canvas shows it tinted and takes the pointer.
 let view:HTMLCanvasElement;let frame:HTMLDivElement;let image:HTMLImageElement;let mask:HTMLCanvasElement|null=null;let ctx:CanvasRenderingContext2D|null=null;
 let points:Point[]=[];let drawing=false;let dirty=$state(false);let dragging=false;
 let coverage=$state<{percent:number;x:number;y:number}|null>(null);
 const point=(e:PointerEvent)=>maskPoint({x:e.clientX,y:e.clientY},view.getBoundingClientRect(),width,height);
 function refresh(){const v=view?.getContext('2d');if(!v||!mask)return;v.clearRect(0,0,view.width,view.height);v.globalCompositeOperation='source-over';v.drawImage(mask,0,0);v.globalCompositeOperation='source-in';v.fillStyle='rgba(61,139,255,.38)';v.fillRect(0,0,view.width,view.height);v.globalCompositeOperation='source-over'}
 function start(e:PointerEvent){if(disabled||e.button!==0||!ctx)return;view.setPointerCapture(e.pointerId);drawing=true;points=[point(e)];ctx.strokeStyle='#ffffff';ctx.fillStyle='#ffffff';ctx.lineWidth=size;ctx.lineCap='round';ctx.lineJoin='round';ctx.beginPath();ctx.moveTo(points[0].x,points[0].y)}
 function move(e:PointerEvent){if(!drawing||!ctx)return;const p=point(e);points.push(p);if(tool==='brush'){ctx.lineTo(p.x,p.y);ctx.stroke();refresh()}}
 function end(){if(!drawing||!ctx)return;drawing=false;if(tool==='box'&&points.length){const a=points[0],b=points.at(-1)!;ctx.fillRect(Math.min(a.x,b.x),Math.min(a.y,b.y),Math.abs(b.x-a.x),Math.abs(b.y-a.y))}else if(tool==='lasso'&&points.length>2){ctx.beginPath();ctx.moveTo(points[0].x,points[0].y);for(const p of points.slice(1))ctx.lineTo(p.x,p.y);ctx.closePath();ctx.fill()}else if(points.length===1){ctx.beginPath();ctx.arc(points[0].x,points[0].y,size/2,0,Math.PI*2);ctx.fill()}dirty=true;refresh();emit()}
 // Share of the layer the selection covers, and where its lower-left corner sits.
 function measure(){if(!ctx)return;const data=ctx.getImageData(0,0,width,height).data;let n=0,minX=width,maxY=0;for(let y=0;y<height;y++)for(let x=0;x<width;x++){if(data[(y*width+x)*4+3]>0){n++;if(x<minX)minX=x;if(y>maxY)maxY=y}}coverage=n?{percent:Math.max(1,Math.round(n/(width*height)*100)),x:minX/width*100,y:Math.min(92,(maxY+1)/height*100)}:null}
 // Exact-white pixels define the editable core; feathering must preserve it.
 function emit(){if(!mask)return;measure();const out=document.createElement('canvas');out.width=mask.width;out.height=mask.height;const c=out.getContext('2d')!;c.fillStyle='#000000';c.fillRect(0,0,out.width,out.height);if(feather>0){c.filter=`blur(${feather}px)`;c.drawImage(mask,0,0);c.filter='none'}c.drawImage(mask,0,0);out.toBlob(onMask,'image/png')}
 function clear(){ctx?.clearRect(0,0,width,height);refresh();dirty=false;coverage=null;onMask(null)}
 function splitAt(e:PointerEvent){const r=frame.getBoundingClientRect();split=Math.round(Math.max(0,Math.min(100,(e.clientX-r.left)/r.width*100)))}
 function handleDown(e:PointerEvent){if(e.button!==0)return;e.preventDefault();e.stopPropagation();(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);dragging=true;splitAt(e)}
 function handleMove(e:PointerEvent){if(dragging)splitAt(e)}
 function handleUp(){dragging=false}
 onMount(()=>{mask=document.createElement('canvas');mask.width=width;mask.height=height;ctx=mask.getContext('2d',{willReadFrequently:true});if(initialMask)void createImageBitmap(initialMask).then(bitmap=>{if(!ctx)return;ctx.drawImage(bitmap,0,0);const data=ctx.getImageData(0,0,width,height);for(let i=0;i<data.data.length;i+=4){data.data[i+3]=data.data[i];data.data[i]=255;data.data[i+1]=255;data.data[i+2]=255}ctx.putImageData(data,0,0);bitmap.close();dirty=true;refresh();measure()});const resize=new ResizeObserver(()=>onDisplay?.(image?.getBoundingClientRect().width||0));resize.observe(image);return()=>{ctx=null;mask=null;resize.disconnect()}});
 $effect(()=>{if(selection&&ctx){ctx.clearRect(0,0,width,height);ctx.fillStyle='#ffffff';ctx.fillRect(Math.max(0,selection.x),Math.max(0,selection.y),Math.max(1,selection.w),Math.max(1,selection.h));dirty=true;refresh();emit()}});
 $effect(()=>{if(feather>=0&&dirty&&mask)emit()});
 // The inspector's Clear action bumps clearKey.
 let seenClear=untrack(()=>clearKey);
 $effect(()=>{if(clearKey!==seenClear){seenClear=clearKey;untrack(clear)}});
</script>
<div class="mv-ed__region-proof" bind:this={frame} style:--split={`${split}%`}>
 <img bind:this={image} src={asset.url} alt={t('region.before')} width={asset.width} height={asset.height}/>
 {#if candidateURL}<svg class="mv-ed__region-comparison" viewBox={`0 0 ${width} ${height}`} aria-hidden="true"><defs><clipPath id={comparisonID}><rect x={width*split/100} y="0" width={width*(1-split/100)} height={height}/></clipPath></defs><image href={candidateURL} width={width} height={height} clip-path={`url(#${comparisonID})`} preserveAspectRatio="none"/></svg>{/if}
 <canvas bind:this={view} width={width} height={height} onpointerdown={start} onpointermove={move} onpointerup={end} onpointercancel={end} aria-label={t('region.paint')} tabindex="0" class:disabled></canvas>
 {#if label}<span class="mv-ed__region-label" aria-hidden="true">{label}</span>{/if}
 <div class="mv-ed__comparison-labels" aria-hidden="true"><span>{t('region.before')}</span><span>{candidateURL?t('region.after'):t('region.selection')}</span></div>
 {#if coverage}<span class="mv-ed__region-coverage" style:left={`${coverage.x}%`} style:top={`${coverage.y}%`}>{t('region.coverage',{tool:t(`region.${tool}`).toUpperCase(),percent:coverage.percent})}</span>{/if}
 <div class="mv-ed__compare">
  <span class="mv-ed__compare-line" aria-hidden="true"></span>
  <span class="mv-ed__compare-handle" class:idle={!candidateURL} aria-hidden="true" onpointerdown={handleDown} onpointermove={handleMove} onpointerup={handleUp} onpointercancel={handleUp}><i class="ti ti-arrows-horizontal"></i></span>
  <input class="mv-ed__compare-input" type="range" min="0" max="100" bind:value={split} aria-label={t('region.compare')} aria-valuetext={`${split}%`}/>
 </div>
</div>
