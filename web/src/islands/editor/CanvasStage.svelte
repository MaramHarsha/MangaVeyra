<script lang="ts">
 import {onMount,type Snippet} from 'svelte';
 // This official adapter installs static shader/uniform synchronizers for CSP
 // environments. Despite its package name, it removes Pixi's eval requirement.
 import 'pixi.js/unsafe-eval';
 import {Application,Assets,Container,Graphics,Sprite} from 'pixi.js';
 import type {Page,Lettering,Point} from './types';
 import {bounds,layerPlacement,letteringOpacity,panelToolbarPlacement} from './document';
 let {page,lettering,selectedPanel='',selectedBubble='',showOrder=false,canArt=false,canLetter=false,previewURL='',previewPanel='',previewLabel='',panelActions,zoom='100',t,onPanel,onBubble,onMovePanel,onMoveBubble}:{page:Page;lettering:Lettering[];selectedPanel?:string;selectedBubble?:string;showOrder?:boolean;canArt?:boolean;canLetter?:boolean;previewURL?:string;previewPanel?:string;previewLabel?:string;panelActions?:Snippet;zoom?:string;t:(key:string,args?:Record<string,string|number>)=>string;onPanel:(id:string)=>void;onBubble:(id:string)=>void;onMovePanel:(id:string,dx:number,dy:number)=>void;onMoveBubble:(id:string,dx:number,dy:number)=>void}=$props();
 let displayWidth=$state(520);const toolbar=$derived(panelToolbarPlacement(page,selectedPanel,displayWidth));
 let host:HTMLDivElement;let svg:SVGSVGElement;let app= $state<Application|null>(null);let failure=$state('');let serial=0;let disposed=false;
 let drag=$state<{kind:'panel'|'bubble';id:string;start:Point;dx:number;dy:number}|null>(null);
 // Page units per CSS pixel: labels and badges keep a constant on-screen size.
 const unit=$derived(page.width/Math.max(1,displayWidth));
 // The selected bubble is drawn last so its outline is never hidden by a later bubble.
 const ordered=$derived([...lettering].sort((a,b)=>(a.bubble_id===selectedBubble?1:0)-(b.bubble_id===selectedBubble?1:0)));
 const preview=$derived.by(()=>{const pn=page.panels.find(p=>p.id===previewPanel);if(!pn||!previewLabel)return null;const b=bounds(pn.points);return {left:b.x/page.width*100,top:(b.y+b.h)/page.height*100}});
 const point=(e:PointerEvent):Point=>{const r=svg.getBoundingClientRect();return{x:(e.clientX-r.left)*page.width/r.width,y:(e.clientY-r.top)*page.height/r.height}};
 function down(e:PointerEvent,kind:'panel'|'bubble',id:string){if(e.button!==0)return;if(kind==='panel')onPanel(id);else onBubble(id);const panel=page.panels.find(p=>p.id===(kind==='panel'?id:page.bubbles.find(b=>b.id===id)?.panel_id));if(page.locked||panel?.locked||(kind==='bubble'&&panel?.layers.some(l=>l.kind==='lettering'&&l.locked))||(kind==='panel'?!canArt:!canLetter))return;e.preventDefault();drag={kind,id,start:point(e),dx:0,dy:0}}
 function move(e:PointerEvent){if(!drag)return;const p=point(e);drag={...drag,dx:p.x-drag.start.x,dy:p.y-drag.start.y}}
 function up(){if(!drag)return;const d=drag;drag=null;if(Math.abs(d.dx)+Math.abs(d.dy)<1)return;if(d.kind==='panel')onMovePanel(d.id,d.dx,d.dy);else onMoveBubble(d.id,d.dx,d.dy)}
 function polygon(points:Point[]){return points.map(p=>`${p.x},${p.y}`).join(' ')}
 // A panel label ("7.3 · RIN VAULTS · LOW") clipped to its panel's width.
 function panelLabel(i:number,prompt:string,width:number){const size=10*unit,max=Math.max(4,Math.floor((width-size*3.6)/(size*.62)));const text=`${page.number}.${i+1}${prompt.trim()?` · ${prompt.trim().replace(/\s+/g,' ').toUpperCase()}`:''}`;return Array.from(text).length>max?Array.from(text).slice(0,max-1).join('')+'…':text}
 async function render(p:Page,candidateURL='',candidatePanel=''){if(!app)return;const token=++serial;const stage=new Container();const scale=Math.min(1,2048/p.width,3072/p.height);app.renderer.resize(Math.ceil(p.width*scale),Math.ceil(p.height*scale));stage.scale.set(scale);const paper=new Graphics().rect(0,0,p.width,p.height).fill(0xffffff);stage.addChild(paper);
  try{for(const pn of p.panels){const group=new Container(),clip=new Graphics().poly(pn.points.flatMap(v=>[v.x,v.y])).fill(0xffffff);group.addChild(clip);group.mask=clip;group.addChild(new Graphics().poly(pn.points.flatMap(v=>[v.x,v.y])).fill(0xdce3f3));if(candidateURL&&pn.id===candidatePanel){const texture=await Assets.load(candidateURL);if(disposed||token!==serial){stage.destroy({children:true});return}const sprite=new Sprite(texture),tr=layerPlacement(pn,texture.width,texture.height,{});sprite.anchor.set(.5);sprite.position.set(tr.x,tr.y);sprite.scale.set(tr.scaleX,tr.scaleY);group.addChild(sprite)}else for(const l of pn.layers){if(l.kind==='lettering'||!l.visible||!l.asset?.url)continue;const texture=await Assets.load(l.asset.url);if(disposed||token!==serial){stage.destroy({children:true});return}const sprite=new Sprite(texture),tr=layerPlacement(pn,l.asset.width||texture.width,l.asset.height||texture.height,l.transform);sprite.anchor.set(.5);sprite.position.set(tr.x,tr.y);sprite.scale.set(tr.scaleX,tr.scaleY);sprite.rotation=tr.rotation;sprite.alpha=l.opacity;sprite.blendMode=l.blend||'normal';group.addChild(sprite)}stage.addChild(group);stage.addChild(new Graphics().poly(pn.points.flatMap(v=>[v.x,v.y])).stroke({color:0x070a12,width:4}))}
   if(disposed||token!==serial){stage.destroy({children:true});return}const old=app.stage.removeChildren();old.forEach(c=>c.destroy({children:true}));app.stage.addChild(stage);failure='';
  }catch{stage.destroy({children:true});failure=t('error.canvas')}
 }
 onMount(()=>{const instance=new Application();void instance.init({width:520,height:735,backgroundAlpha:0,antialias:true,resolution:1,preference:'webgl'}).then(()=>{if(disposed){instance.destroy(true,{children:true});return}host.appendChild(instance.canvas);app=instance;void render(page)}).catch(()=>failure=t('error.canvas'));return()=>{disposed=true;serial++;if(app){app.destroy(true,{children:true,texture:false});app=null}}});
 onMount(()=>{const resize=new ResizeObserver(()=>displayWidth=svg.getBoundingClientRect().width||520);resize.observe(svg);return()=>resize.disconnect()});
 $effect(()=>{const p=page,url=previewURL,panel=previewPanel;if(app)void render(p,url,panel)});
</script>
<svelte:window onpointermove={move} onpointerup={up}/>
<div class="mv-ed__canvas-scroll">
 {#if failure}<p class="mv-ed__canvas-error" role="alert">{failure}</p>{/if}
 <div class={`mv-ed__proof-wrap mv-ed__proof-wrap--${zoom}`}>
  <div class="mv-ed__pixi" bind:this={host} aria-hidden="true"></div>
  <svg bind:this={svg} class="mv-ed__overlay" viewBox={`0 0 ${page.width} ${page.height}`} width={page.width} height={page.height} role="group" aria-label={t('canvas')}>
   {#each page.panels as panel,i (panel.id)}
    {@const b=bounds(panel.points)}
    <g transform={drag?.kind==='panel'&&drag.id===panel.id?`translate(${drag.dx} ${drag.dy})`:undefined}>
     <polygon class:selected={selectedPanel===panel.id} points={polygon(panel.points)} fill="transparent" role="button" tabindex="0" aria-label={t('panel.label',{page:page.number,panel:i+1})} aria-pressed={selectedPanel===panel.id} onpointerdown={(e)=>down(e,'panel',panel.id)} onkeydown={(e)=>{if(e.key==='Enter'||e.key===' '){e.preventDefault();onPanel(panel.id)}}}/>
     {#if !panel.layers.some(l=>l.asset)}<text class="mv-ed__panel-label" x={b.x+18*unit} y={b.y+b.h-14*unit} font-size={10*unit}>{panelLabel(i,panel.prompt,b.w)}{#if panel.locked}<tspan dx={6*unit}>🔒</tspan>{/if}</text>{/if}
    </g>
   {/each}
   {#each ordered as l (l.bubble_id)}
    {#if letteringOpacity(page,l.bubble_id)>0}
    <g class="mv-ed__bubble" class:selected={selectedBubble===l.bubble_id} opacity={letteringOpacity(page,l.bubble_id)} transform={`${drag?.kind==='bubble'&&drag.id===l.bubble_id?`translate(${drag.dx} ${drag.dy}) `:''}rotate(${l.rotate} ${l.rotation_center.x} ${l.rotation_center.y})`} role="button" tabindex="0" aria-label={t('bubble.selected_label',{number:(page.bubbles.find(b=>b.id===l.bubble_id)?.order??0)+1})} aria-pressed={selectedBubble===l.bubble_id} onpointerdown={(e)=>{e.stopPropagation();down(e,'bubble',l.bubble_id)}} onkeydown={(e)=>{if(e.key==='Enter'||e.key===' '){e.preventDefault();onBubble(l.bubble_id)}}}>
     {#if l.shape_path}<path d={l.shape_path} fill={l.fill} stroke={l.stroke} stroke-width={l.stroke_width}/>{/if}
     <path d={l.text_path} fill={l.text_color} stroke={l.text_stroke||'none'} stroke-width={l.text_stroke_width||0}/>
    </g>
    {/if}
   {/each}
   {#if showOrder}
    <g class="mv-ed__bubble-numbers" aria-hidden="true">
     {#each page.bubbles as bubble (bubble.id)}
      {#if letteringOpacity(page,bubble.id)>0}
       {@const dx=drag?.kind==='bubble'&&drag.id===bubble.id?drag.dx:0}{@const dy=drag?.kind==='bubble'&&drag.id===bubble.id?drag.dy:0}
       <g class="mv-ed__bubble-number" class:selected={selectedBubble===bubble.id} transform={`translate(${bubble.box.x+dx} ${bubble.box.y+dy})`}><circle r={10*unit}/><text y={4*unit} text-anchor="middle" font-size={11*unit}>{bubble.order+1}</text></g>
      {/if}
     {/each}
    </g>
   {/if}
   {#if toolbar&&panelActions}<g transform={`translate(${toolbar.x} ${toolbar.y}) scale(${toolbar.scale})`}><foreignObject width={toolbar.width} height={toolbar.height}><div class="mv-ed__panel-tools" role="toolbar" aria-label={t('panel.actions')}>{@render panelActions()}</div></foreignObject></g>{/if}
  </svg>
  {#if preview}<div class="mv-ed__preview-badge" style:left={`${preview.left}%`} style:top={`${preview.top}%`}>{previewLabel}</div>{/if}
 </div>
</div>
