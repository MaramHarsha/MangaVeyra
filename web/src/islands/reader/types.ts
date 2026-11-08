/** Types of the reader island's boot props (internal/reader/view.go islandProps). */
export type Mode = 'paged' | 'spread' | 'guided' | 'webtoon' | 'auto';
export type Direction = 'rtl' | 'ltr' | 'vertical';
export type Background = 'ink' | 'grey' | 'paper';

export interface Point { x: number; y: number }
export interface GuidedBubble { order: number; kind: string; speaker?: string; text: string }
export interface GuidedPanel { order: number; points: Point[]; x: number; y: number; w: number; h: number; alt_text: string; bubbles: GuidedBubble[] }
export interface Page { id: string; number: number; width: number; height: number; src: string; alt: string; panels: GuidedPanel[]; missing?: boolean }
export interface ChapterLink { id: string; number: number; title: string; label: string; href: string; completed: boolean; current: boolean }
export interface LangLink { lang: string; label: string; href: string; current: boolean }
export interface Prefs {
  mode: Mode; direction: Direction; background: Background; zoom: number;
  tap_zones: boolean; tap_l_shape: boolean; panel_comments: boolean; read_aloud: boolean; hide_chrome: boolean; reduced_motion: boolean; stored: boolean;
}
export interface Bookmark { id: string; series_id: string; publication_id: string; page: number; panel: string; note: string; created_at: string }
export interface DownloadState { id: string; status: 'pending' | 'ready' | 'expired'; expires_at: number }
export interface Provenance { role: string; kind: string; label: string }

export interface ReaderProps {
  view: 'reader';
  series: { id: string; slug: string; title: string; format: string; age_rating: string; ai_label: Record<string, string> };
  publication: { id: string; number: number; title: string; label: string; lang: string; page_count: number; ai_label: Record<string, string> };
  pages: Page[];
  url_ttl: number;
  signed_at: number;
  chapters: ChapterLink[];
  prev: ChapterLink | null;
  next: ChapterLink | null;
  langs: LangLink[];
  prefs: Prefs;
  signed_in: boolean;
  progress: { page: number; percent: number; completed: boolean } | null;
  bookmarks: Bookmark[];
  download: DownloadState | null;
  start_page: number;
  start_panel: number;
  back_url: string;
  series_url: string;
  login_url: string;
  library_url: string;
  provenance: Provenance[];
  api: { chapter: string; progress: string; prefs: string; bookmarks: string; downloads: string; events: string; comments: string };
  comments: boolean;
  events: boolean;
  ui_lang: string;
  messages: Record<string, string>;
}

export interface LibraryProps {
  view: 'library';
  tab: string;
  api: { downloads: string; bookmarks: string };
  messages: Record<string, string>;
}

/** Translator over the server-provided catalogue (keys without the "reader." prefix). */
export type T = (key: string, args?: Record<string, string | number>) => string;

export function translator(messages: Record<string, string>): T {
  return (key, args = {}) => {
    const raw = messages[key] ?? key;
    return Object.entries(args).reduce((s, [k, v]) => s.replaceAll(`{${k}}`, String(v)), raw);
  };
}
