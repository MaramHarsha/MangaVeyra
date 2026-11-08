<script lang="ts">
 import {clone} from './document';
 import type {Pose,Point} from './types';
 let {pose,presets,disabled=false,t,onChange}:{pose?:Record<string,unknown>;presets:Record<string,Pose>;disabled?:boolean;t:(key:string,args?:Record<string,string|number>)=>string;onChange:(pose:Pose|null)=>void}=$props();
 const edges=[[0,1],[1,2],[2,3],[3,4],[1,5],[5,6],[6,7],[1,8],[8,9],[9,10],[8,11],[11,12]];
 let sketching=$state(false),joint=$state(0),drag=$state(-1);
 const current=$derived(pose?.schema==='mangaveyra.pose.v1'&&Array.isArray(pose.joints)?pose as unknown as Pose:null);
 function choose(name:string){if(disabled||!presets[name])return;onChange(clone(presets[name]));}
 function move(index:number,p:Point){if(disabled||!current)return;const next=clone(current);delete next.preset;next.joints[index]={x:Math.max(0,Math.min(1,p.x)),y:Math.max(0,Math.min(1,p.y))};onChange(next);}
 function point(e:PointerEvent){const r=e.currentTarget instanceof SVGSVGElement?e.currentTarget.getBoundingClientRect():(e.currentTarget as SVGElement).ownerSVGElement!.getBoundingClientRect();return{x:(e.clientX-r.left)/r.width,y:(e.clientY-r.top)/r.height};}
 function key(e:KeyboardEvent,i:number){if(!current||disabled||e.isComposing||e.keyCode===229)return;const delta=e.shiftKey?.05:.01,p=current.joints[i];if(['ArrowUp','ArrowDown','ArrowLeft','ArrowRight'].includes(e.key)){e.preventDefault();move(i,{x:p.x+(e.key==='ArrowRight'?delta:e.key==='ArrowLeft'?-delta:0),y:p.y+(e.key==='ArrowDown'?delta:e.key==='ArrowUp'?-delta:0)});}}
</script>
<div class="mv-ed__poses">
 {#each ['leap','crouch','run'] as name}
  <button type="button" class="mv-ed__pose" class:selected={current?.preset===name} {disabled} aria-pressed={current?.preset===name} onclick={()=>choose(name)}>
   <svg viewBox="0 0 100 100" aria-hidden="true">{#if presets[name]}{#each edges as [a,b]}<line x1={presets[name].joints[a].x*100} y1={presets[name].joints[a].y*100} x2={presets[name].joints[b].x*100} y2={presets[name].joints[b].y*100}/>{/each}<circle cx={presets[name].joints[0].x*100} cy={presets[name].joints[0].y*100} r="5"/>{/if}</svg><span>{t(`pose.${name}`)}</span>
  </button>
 {/each}
 <button type="button" class="mv-ed__pose mv-ed__pose--sketch" {disabled} aria-expanded={sketching} onclick={()=>{if(!current)choose('run');sketching=!sketching;}}><i class="ti ti-scribble" aria-hidden="true"></i>{t('sketch')}</button>
</div>
{#if sketching&&current}
 <div class="mv-ed__pose-editor">
  <p class="mv-ed__help" id="pose-help">{t('control.pose_help')}</p>
  <svg viewBox="0 0 100 100" role="group" aria-label={t('control.pose_editor')} aria-describedby="pose-help" onpointermove={(e)=>{if(drag>=0)move(drag,point(e));}} onpointerup={()=>drag=-1} onpointercancel={()=>drag=-1}>
   {#each edges as [a,b]}<line x1={current.joints[a].x*100} y1={current.joints[a].y*100} x2={current.joints[b].x*100} y2={current.joints[b].y*100}/>{/each}
   {#each current.joints as p,i}<circle cx={p.x*100} cy={p.y*100} r={i===0?5:3} class:selected={i===joint} role="slider" tabindex={disabled?-1:0} aria-label={t('control.joint',{number:i+1})} aria-valuemin="0" aria-valuemax="100" aria-valuenow={Math.round(p.x*100)} aria-valuetext={t('control.joint_position',{x:Math.round(p.x*100),y:Math.round(p.y*100)})} onfocus={()=>joint=i} onpointerdown={(e)=>{if(disabled)return;e.preventDefault();joint=i;drag=i;(e.currentTarget.ownerSVGElement as SVGSVGElement).setPointerCapture(e.pointerId);}} onkeydown={(e)=>key(e,i)}/>{/each}
  </svg>
  <button type="button" class="mv-btn mv-btn--quiet mv-btn--sm" {disabled} onclick={()=>{onChange(null);sketching=false;}}>{t('control.clear_pose')}</button>
 </div>
{/if}
