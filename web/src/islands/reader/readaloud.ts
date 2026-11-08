/**
 * Read-aloud with on-device voices only (ADR 0012 §6). A voice whose
 * localService is not exactly true may send the chapter text to a third
 * party, so it is never used, and nothing is spoken without a chosen local
 * voice (the browser default could be a network voice).
 */

export interface VoiceLike { name: string; lang: string; localService: boolean; default?: boolean; voiceURI?: string }

export interface UtteranceLike { voice: VoiceLike | null; lang: string; text: string; rate: number; onend: (() => void) | null; onerror: (() => void) | null }

export interface SynthLike {
  getVoices(): VoiceLike[];
  speak(u: UtteranceLike): void;
  cancel(): void;
  addEventListener?(type: 'voiceschanged', fn: () => void): void;
  removeEventListener?(type: 'voiceschanged', fn: () => void): void;
}

/** localVoices keeps the voices that run on this device. */
export function localVoices(voices: readonly VoiceLike[]): VoiceLike[] {
  return voices.filter((v) => v && v.localService === true);
}

function base(lang: string): string {
  return (lang || '').toLowerCase().split(/[-_]/)[0];
}

/**
 * pickVoice chooses an on-device voice for the chapter language: an exact
 * match, then the same base language (a local default first). It returns
 * null when no local voice speaks the language.
 */
export function pickVoice(voices: readonly VoiceLike[], lang: string): VoiceLike | null {
  const local = localVoices(voices);
  const want = (lang || '').toLowerCase();
  const exact = local.filter((v) => v.lang.toLowerCase().replace('_', '-') === want);
  if (exact.length) return exact.find((v) => v.default) || exact[0];
  const same = local.filter((v) => base(v.lang) === base(lang));
  if (same.length) return same.find((v) => v.default) || same[0];
  return null;
}

/** ReadAloud speaks text with a local voice for the chapter language. */
export class ReadAloud {
  private voice: VoiceLike | null = null;
  private listeners = new Set<() => void>();
  private readonly onVoices = () => {
    this.refresh();
    this.listeners.forEach((fn) => fn());
  };

  constructor(
    private readonly synth: SynthLike | null | undefined,
    private readonly makeUtterance: ((text: string) => UtteranceLike) | null,
    private readonly lang: string,
  ) {
    this.refresh();
    synth?.addEventListener?.('voiceschanged', this.onVoices);
  }

  /** available reports whether an on-device voice exists for the language. */
  get available(): boolean {
    return !!this.synth && !!this.makeUtterance && this.voice !== null;
  }

  get voiceName(): string {
    return this.voice?.name ?? '';
  }

  /** onChange is called when the browser's voice list changes. */
  onChange(fn: () => void): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  refresh(): void {
    try {
      this.voice = this.synth ? pickVoice(this.synth.getVoices() || [], this.lang) : null;
    } catch {
      this.voice = null;
    }
  }

  /** speak cancels anything playing and reads text; it returns false when nothing could be spoken. */
  speak(text: string, onEnd?: () => void): boolean {
    const voice = this.voice;
    if (!this.synth || !this.makeUtterance || !voice || voice.localService !== true || !text.trim()) return false;
    this.synth.cancel();
    const u = this.makeUtterance(text);
    u.voice = voice;
    u.lang = voice.lang;
    u.rate = 1;
    u.onend = onEnd ?? null;
    u.onerror = onEnd ?? null;
    this.synth.speak(u);
    return true;
  }

  stop(): void {
    try {
      this.synth?.cancel();
    } catch {
      /* nothing to stop */
    }
  }

  destroy(): void {
    this.stop();
    this.synth?.removeEventListener?.('voiceschanged', this.onVoices);
    this.listeners.clear();
  }
}

/** browserReadAloud wires ReadAloud to window.speechSynthesis when the browser has it. */
export function browserReadAloud(lang: string): ReadAloud {
  const w = typeof window === 'undefined' ? undefined : window;
  const synth = w && 'speechSynthesis' in w ? (w.speechSynthesis as unknown as SynthLike) : null;
  const ctor = w && 'SpeechSynthesisUtterance' in w ? w.SpeechSynthesisUtterance : null;
  return new ReadAloud(synth, ctor ? (text: string) => new ctor(text) as unknown as UtteranceLike : null, lang);
}
