/**
 * C1 Story editor enhancement for the Panels form. Without JavaScript the
 * form is complete: Save in the header submits it, and the add/remove line
 * buttons submit it with an intent the server applies to the draft.
 *
 * With JavaScript:
 * - edits autosave after a short pause, posting the revision the page holds
 *   and taking the new revision back; conflicts and locks stop autosave and
 *   say so in the header's save state (like the design's "Draft saved");
 * - add / remove line and the line type change work in place.
 */

export type SaveState = 'saved' | 'dirty' | 'saving' | 'failed' | 'conflict' | 'locked' | 'invalid';

type Fetch = typeof fetch;

export interface ScriptEditorOptions {
  /** Pause after the last edit before saving, in ms. */
  delay?: number;
  /** Retry delay after a network or server error, in ms. */
  retry?: number;
  fetch?: Fetch;
}

const TOKENS = /__(PANEL|INDEX|NUMBER|LABEL)__/g;

/** escapeHTML protects values substituted into the line template markup. */
function escapeHTML(value: string): string {
  return value.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
}

export class ScriptEditor {
  readonly form: HTMLFormElement;
  readonly root: ParentNode;
  state: SaveState = 'saved';
  private timer: number | undefined;
  private inFlight = false;
  private queued = false;
  private stopped = false;
  private edits = 0;
  private readonly delay: number;
  private readonly retry: number;
  private readonly fetcher: Fetch;

  constructor(form: HTMLFormElement, opts: ScriptEditorOptions = {}) {
    this.form = form;
    this.root = form.closest('[data-story-views]') ?? form.ownerDocument;
    this.delay = opts.delay ?? 1200;
    this.retry = opts.retry ?? 5000;
    this.fetcher = opts.fetch ?? ((...args) => fetch(...args));
    const doc = form.ownerDocument;
    form.addEventListener('click', (e) => this.click(e));
    form.addEventListener('change', (e) => {
      const t = e.target as HTMLElement;
      if (t instanceof HTMLSelectElement && t.dataset.lineKind) this.syncKind(t);
    });
    // Fields outside the form element (the scene title) join it through the
    // form attribute, so listen on the document and filter by form owner.
    const edited = (e: Event) => {
      const t = e.target as HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement;
      if (!t || !('form' in t) || t.form !== form) return;
      // Text fields already reported every keystroke as input; their change on
      // blur is not a new edit.
      if (e.type === 'change' && !(t instanceof HTMLSelectElement) && !['checkbox', 'radio'].includes(t.type)) return;
      this.changed();
    };
    doc.addEventListener('input', edited);
    doc.addEventListener('change', edited);
    form.addEventListener('submit', (e) => {
      const submitter = (e as SubmitEvent).submitter as HTMLButtonElement | null;
      if (submitter?.dataset.addLine || submitter?.dataset.removeLine) return;
      if (!form.hasAttribute('data-autosave')) return;
      e.preventDefault();
      this.saveNow();
    });
    const saved = this.stateEl()?.dataset.state as SaveState | undefined;
    if (saved) this.state = saved;
  }

  private stateEl(): HTMLElement | null {
    return this.root.querySelector<HTMLElement>('[data-save-state]');
  }

  private setState(state: SaveState): void {
    this.state = state;
    const el = this.stateEl();
    if (!el) return;
    el.dataset.state = state;
    const label = el.dataset[state] ?? '';
    if (state === 'conflict') {
      const a = el.ownerDocument.createElement('a');
      a.href = el.ownerDocument.defaultView?.location.href ?? '';
      a.textContent = label;
      el.replaceChildren(a);
    } else {
      el.textContent = label;
    }
  }

  private click(e: MouseEvent): void {
    const t = e.target as HTMLElement;
    const add = t.closest<HTMLElement>('[data-add-line]');
    if (add) {
      e.preventDefault();
      this.addLine(add.dataset.addLine ?? '');
      return;
    }
    const remove = t.closest<HTMLElement>('[data-remove-line]');
    if (remove) {
      e.preventDefault();
      this.removeLine(remove.dataset.removeLine ?? '');
    }
  }

  /** addLine appends an empty speech line to a panel and focuses its speaker. */
  addLine(panelID: string): HTMLElement | null {
    const tpl = this.form.querySelector<HTMLTemplateElement>('template[data-line-template]');
    const lines = this.form.querySelector<HTMLElement>(`[data-lines="${CSS.escape(panelID)}"]`);
    const controls = this.form.querySelector<HTMLElement>(`[data-line-controls="${CSS.escape(panelID)}"]`);
    const row = lines?.closest<HTMLElement>('[data-panel]');
    if (!tpl || !lines || !row) return null;
    let next = 0;
    lines.querySelectorAll<HTMLElement>('[data-line]').forEach((line) => {
      const index = Number((line.dataset.line ?? '').split('.').pop());
      if (Number.isInteger(index) && index >= next) next = index + 1;
    });
    const values: Record<string, string> = {
      PANEL: panelID,
      INDEX: String(next),
      NUMBER: String(lines.children.length + 1),
      LABEL: row.dataset.panelLabel ?? '',
    };
    const html = tpl.innerHTML.replace(TOKENS, (_, key: string) => escapeHTML(values[key] ?? ''));
    const box = this.form.ownerDocument.createElement('template');
    box.innerHTML = html;
    const line = box.content.querySelector<HTMLElement>('.mv-c1__line');
    const control = box.content.querySelector<HTMLElement>('.mv-c1__linectl');
    if (!line) return null;
    lines.append(line);
    if (control && controls) controls.append(control);
    line.querySelector<HTMLInputElement>('input')?.focus();
    return line;
  }

  /** removeLine drops a line ("<panel>.<index>") and its controls, then saves. */
  removeLine(key: string): void {
    this.form.querySelectorAll(`[data-line="${CSS.escape(key)}"]`).forEach((el) => el.remove());
    this.changed();
  }

  /** syncKind shows "Caption:" instead of the speaker for caption lines. */
  syncKind(select: HTMLSelectElement): void {
    const line = this.form.querySelector<HTMLElement>(`.mv-c1__line[data-line="${CSS.escape(select.dataset.lineKind ?? '')}"]`);
    if (line) line.dataset.kind = select.value;
  }

  /** changed records an edit and schedules a save. */
  changed(): void {
    if (this.stopped || !this.form.hasAttribute('data-autosave')) return;
    this.edits++;
    this.setState('dirty');
    this.schedule(this.delay);
  }

  private schedule(ms: number): void {
    const win = this.form.ownerDocument.defaultView!;
    if (this.timer !== undefined) win.clearTimeout(this.timer);
    this.timer = win.setTimeout(() => {
      this.timer = undefined;
      void this.save();
    }, ms);
  }

  saveNow(): Promise<void> {
    const win = this.form.ownerDocument.defaultView!;
    if (this.timer !== undefined) win.clearTimeout(this.timer);
    this.timer = undefined;
    return this.save();
  }

  /** save posts the form with the held revision; one request at a time. */
  async save(): Promise<void> {
    if (this.stopped) return;
    if (this.inFlight) {
      this.queued = true;
      return;
    }
    this.inFlight = true;
    const sentAt = this.edits;
    this.setState('saving');
    let retryLater = false;
    try {
      const body = new URLSearchParams();
      new FormData(this.form).forEach((value, key) => {
        if (typeof value === 'string') body.append(key, value);
      });
      const res = await this.fetcher(this.form.action, {
        method: 'POST',
        body,
        credentials: 'same-origin',
        headers: { 'X-MV-Autosave': '1', Accept: 'application/json' },
      });
      const data = (await res.json().catch(() => ({}))) as { revision?: string };
      if (res.ok && data.revision) {
        this.root.querySelectorAll<HTMLInputElement>('input[data-revision]').forEach((input) => {
          input.value = data.revision!;
        });
        if (this.edits === sentAt) {
          this.setState('saved');
          this.form.dispatchEvent(new CustomEvent('mv:form-saved', { bubbles: true, detail: { form: this.form } }));
        }
      } else if (res.status === 409) {
        this.stop('conflict');
      } else if (res.status === 423) {
        this.stop('locked');
      } else if (res.status === 422) {
        this.setState('invalid');
      } else {
        retryLater = true;
      }
    } catch {
      retryLater = true;
    } finally {
      this.inFlight = false;
    }
    if (this.stopped) return;
    if (retryLater) {
      this.setState('failed');
      this.schedule(this.retry);
    } else if (this.queued || this.edits !== sentAt) {
      this.queued = false;
      if (this.state !== 'invalid' || this.edits !== sentAt) {
        this.setState('dirty');
        this.schedule(this.delay);
      }
    }
  }

  private stop(state: SaveState): void {
    this.stopped = true;
    this.form.removeAttribute('data-autosave');
    this.setState(state);
  }
}

/** rebuildProseLines refreshes the Prose view's dialogue from the form. */
export function rebuildProseLines(root: ParentNode, form: HTMLFormElement): void {
  root.querySelectorAll<HTMLElement>('[data-prose-lines]').forEach((box) => {
    const panelID = box.dataset.proseLines ?? '';
    const lines = form.querySelectorAll<HTMLElement>(`[data-lines="${CSS.escape(panelID)}"] .mv-c1__line`);
    const labels: Record<string, string> = {
      speech: box.dataset.labelSpeech ?? '',
      thought: box.dataset.labelThought ?? '',
      caption: box.dataset.labelCaption ?? '',
    };
    box.querySelectorAll('[data-prose-line]').forEach((p) => p.remove());
    lines.forEach((line) => {
      const kind = line.dataset.kind ?? 'speech';
      const speaker = line.querySelector<HTMLInputElement>('input')?.value.trim() ?? '';
      const text = line.querySelector<HTMLTextAreaElement>('textarea')?.value.trim() ?? '';
      const p = form.ownerDocument.createElement('p');
      p.className = `mv-c1__prose-line mv-c1__prose-line--${kind}`;
      p.dataset.proseLine = line.dataset.line ?? '';
      const strong = form.ownerDocument.createElement('strong');
      strong.textContent = kind === 'caption' || !speaker ? labels[kind] ?? '' : speaker;
      const span = form.ownerDocument.createElement('span');
      span.textContent = text;
      p.append(strong, span);
      box.append(p);
    });
  });
}

const editors = new WeakMap<HTMLFormElement, ScriptEditor>();

/** enhanceScriptEditors wires every C1 Panels form under root once. */
export function enhanceScriptEditors(root: ParentNode = document, opts?: ScriptEditorOptions): ScriptEditor[] {
  const out: ScriptEditor[] = [];
  root.querySelectorAll<HTMLFormElement>('form.mv-c1__script').forEach((form) => {
    let editor = editors.get(form);
    if (!editor) {
      editor = new ScriptEditor(form, opts);
      editors.set(form, editor);
    }
    out.push(editor);
  });
  return out;
}
