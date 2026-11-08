import { describe, expect, it, vi } from 'vitest';
import { localVoices, pickVoice, ReadAloud, type SynthLike, type UtteranceLike, type VoiceLike } from './readaloud';

const v = (name: string, lang: string, localService: boolean, isDefault = false): VoiceLike => ({ name, lang, localService, default: isDefault });

const network = v('Cloud English', 'en-US', false, true);
const kyoko = v('Kyoko', 'ja-JP', true);
const daniel = v('Daniel', 'en-GB', true);
const samantha = v('Samantha', 'en-US', true);

function fakeSynth(voices: VoiceLike[]) {
  const spoken: UtteranceLike[] = [];
  let listener: (() => void) | null = null;
  const synth: SynthLike & { spoken: UtteranceLike[]; voices: VoiceLike[]; fire(): void } = {
    spoken,
    voices,
    getVoices: () => synth.voices,
    speak: (u) => spoken.push(u),
    cancel: vi.fn(),
    addEventListener: (_t, fn) => (listener = fn),
    removeEventListener: () => (listener = null),
    fire: () => listener?.(),
  };
  return synth;
}
const utter = (text: string): UtteranceLike => ({ text, voice: null, lang: '', rate: 1, onend: null, onerror: null });

describe('voice filtering (ADR 0012 §6)', () => {
  it('keeps only voices that run on the device', () => {
    expect(localVoices([network, kyoko, daniel, v('Odd', 'en', undefined as unknown as boolean)])).toEqual([kyoko, daniel]);
  });
  it('never picks a network voice, even as the default or the only match', () => {
    expect(pickVoice([network], 'en')).toBeNull();
    expect(pickVoice([network, daniel], 'en-US')).toBe(daniel);
  });
  it('prefers an exact language, then the same base language', () => {
    expect(pickVoice([daniel, samantha, network], 'en-US')).toBe(samantha);
    expect(pickVoice([daniel, kyoko], 'en')).toBe(daniel);
    expect(pickVoice([daniel, kyoko], 'ja')).toBe(kyoko);
    expect(pickVoice([daniel], 'ja')).toBeNull();
  });
});

describe('ReadAloud', () => {
  it('is unavailable without a local voice and speaks nothing', () => {
    const synth = fakeSynth([network]);
    const r = new ReadAloud(synth, utter, 'en');
    expect(r.available).toBe(false);
    expect(r.speak('Keep moving!')).toBe(false);
    expect(synth.spoken).toHaveLength(0);
  });
  it('is unavailable without the Web Speech API', () => {
    expect(new ReadAloud(null, utter, 'en').available).toBe(false);
    expect(new ReadAloud(fakeSynth([daniel]), null, 'en').available).toBe(false);
  });
  it('speaks with the chosen local voice and its language', () => {
    const synth = fakeSynth([network, kyoko]);
    const r = new ReadAloud(synth, utter, 'ja');
    expect(r.available).toBe(true);
    expect(r.voiceName).toBe('Kyoko');
    expect(r.speak('屋上に行く！')).toBe(true);
    expect(synth.spoken[0].voice).toBe(kyoko);
    expect(synth.spoken[0].lang).toBe('ja-JP');
    expect(synth.cancel).toHaveBeenCalled();
    expect(r.speak('   ')).toBe(false);
  });
  it('follows voice list changes', () => {
    const synth = fakeSynth([]);
    const r = new ReadAloud(synth, utter, 'en');
    const changed = vi.fn();
    r.onChange(changed);
    expect(r.available).toBe(false);
    synth.voices = [network, daniel];
    synth.fire();
    expect(changed).toHaveBeenCalled();
    expect(r.available).toBe(true);
    expect(r.voiceName).toBe('Daniel');
    r.destroy();
  });
});
