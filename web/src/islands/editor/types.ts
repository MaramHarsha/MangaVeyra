export interface Pose {schema:string;preset?:string;joints:Point[]}
export interface Point { x: number; y: number }
export interface Rect extends Point { w: number; h: number }
export interface Asset { id: string; url?: string; width?: number; height?: number }
export interface Layer { id: string; kind: 'background'|'character'|'fx'|'lettering'; name?: string; character_id?: string; asset?: Asset; mask?: Asset; order: number; visible: boolean; locked: boolean; opacity: number; blend?: 'normal'|'multiply'|'screen'; transform: {offset_x?:number;offset_y?:number;scale?:number;rotate?:number;flip_x?:boolean}; provenance:string; model_id?:string;provider_id?:string;job_id?:string;seed?:number;prompt?:string;lock_score?:number }
export interface Panel { id:string;order:number;points:Point[];script_panel_id?:string;prompt:string;prompt_source:string;camera?:string;shot?:string;style?:string;pose?:Record<string,unknown>;locked:boolean;locked_by?:string;alt_text?:string;layers:Layer[] }
export interface Ruby {start:number;end:number;text:string}
export interface BubbleText {text:string;status:string;source:string;ruby?:Ruby[]}
export interface Bubble {id:string;panel_id?:string;kind:'speech'|'thought'|'shout'|'whisper'|'caption'|'sfx';speaker_character_id?:string;script_panel_id?:string;order:number;box:Rect;shape:string;tail:{enabled:boolean;tip:Point};style:{font?:string;size?:number;auto_fit:boolean;align?:string;rotate?:number;stroke?:boolean};vertical:boolean;texts:Record<string,BubbleText>}
export interface Page {schema:string;id:string;project_id:string;chapter_id:string;number:number;width:number;height:number;direction:string;layout_id?:string;locked:boolean;locked_by?:string;version:number;panels:Panel[];bubbles:Bubble[];note?:string;thumb?:Asset}
export interface Lettering {bubble_id:string;lang:string;shape_path:string;text_path:string;rotate:number;rotation_center:Point;fill:string;stroke:string;stroke_width:number;text_color:string;text_stroke?:string;text_stroke_width?:number;overflow:boolean;font?:string;font_size?:number}
export interface PageResponse {page:Page;lettering:Lettering[]}
export interface PageSummary {id:string;number:number;panels:number;locked:boolean;version:number;thumb?:Asset;layout_id?:string;updated_at?:string}
export interface Layout {id:string;name:string;panels:number;points:Point[][];suggested:boolean;rank?:number}
export interface Character {id:string;name:string;portrait_asset_id?:string;lock_score?:number}
export interface Candidate {index:number;asset_id:string;url?:string;seed:number;lock_score?:number;layers?:{layer_id:string;kind:string;name:string;asset_id:string}[]}
export interface Job {id:string;type?:string;state:string;progress:number;step?:number;steps?:number;model_id?:string;model_name?:string;provider_id?:string;error_code?:string;created_at?:string;started_at?:string;finished_at?:string;position?:number;estimate_seconds?:number;page_id?:string;panel_id?:string;layer_id?:string;prompt?:string}
export interface Version {id:string;number:number;action:string;summary:string;created_at:string;actor_kind?:string}
export interface Compute {queued:number;running:number;workers_online:number;load:number;gpu?:string;vram_used_mb?:number;vram_total_mb?:number;worker_name?:string;host:string}
export interface Beat {panels:number;wide:number;medium:number;close:number}
export interface Option {id:string;label:string}
export interface ChapterLink {id:string;number:number;title:string;href:string}
export interface Print {format_label:string;size_label:string;width_mm?:number;height_mm?:number}
export interface Bootstrap {pose_presets?:Record<string,Pose>;project:{id:string;title:string;language:string;style:string;format:string;page_size:string;page_width:number;page_height:number};chapter:{id:string;number:number;title:string;locked:boolean};pages:PageSummary[];characters:Character[];layouts:Layout[];permissions:{art:boolean;letter:boolean;generate:boolean;lock:boolean};current?:PageResponse;api:string;lang:string;messages:Record<string,string>;chapters?:ChapterLink[];styles?:Option[];print?:Print;compute?:Compute;beats?:Record<string,Beat>;script_actions?:Record<string,string>}
export interface Props {initial:Bootstrap;mode?:string;tool?:string;panel?:string;job?:string}

export interface RepairTarget {page_id:string;panel_id:string;layer_id:string;version:number}
export interface DriftPanel extends RepairTarget {chapter_id:string;page_number:number;panel_number:number;asset_id:string;url?:string;score:number;identity:number;outfit?:number;locked?:boolean}
export interface LockStatus {character_id:string;status:string;method?:string;score?:number;panels:number;references:number;threshold:number;can_train:boolean;can_score:boolean;training_model?:string;training_job_id?:string;training_progress:number;drift:DriftPanel[];drift_total?:number;unscored?:number;outfit_pending?:number}
