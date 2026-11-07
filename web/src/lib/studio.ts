/**
 * Studio progressive enhancement for the B3 New project wizard and the C1
 * Story editor. Both pages are complete without it: the wizard renders every
 * step stacked in one form, and the Prose / Panels toggle is a pair of links.
 * With it, the wizard shows one step at a time (stepper, Back / Next, live
 * summary) and the story toggle switches views without losing unsaved edits.
 */

export const WIZARD_STEPS = ['premise', 'genre', 'style', 'format', 'generate'] as const;
export type WizardStep = (typeof WIZARD_STEPS)[number];

function isStep(v: string | undefined | null): v is WizardStep {
  return !!v && (WIZARD_STEPS as readonly string[]).includes(v);
}

/** checkedLabels returns the [data-label] text of the checked inputs named name. */
function checkedLabels(form: HTMLFormElement, name: string): string[] {
  const out: string[] = [];
  form.querySelectorAll<HTMLInputElement>(`input[name="${name}"]`).forEach((input) => {
    if (!input.checked) return;
    const text = input.closest('label')?.querySelector('[data-label]')?.textContent?.trim();
    if (text) out.push(text);
  });
  return out;
}

function fieldValue(form: HTMLFormElement, name: string): string {
  const el = form.elements.namedItem(name);
  if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement || el instanceof HTMLSelectElement) return el.value;
  if (el instanceof RadioNodeList) return el.value;
  return '';
}

/** Wizard is one enhanced B3 form. */
export class Wizard {
  readonly root: HTMLElement;
  readonly form: HTMLFormElement;
  step: WizardStep;

  constructor(root: HTMLElement) {
    const form = root.querySelector('form');
    if (!form) throw new Error('wizard: no form');
    this.root = root;
    this.form = form;
    this.step = isStep(root.dataset.current) ? root.dataset.current : 'premise';
    root.classList.add('is-enhanced');
    root.querySelector<HTMLElement>('[data-premise-tries]')?.removeAttribute('hidden');
    this.bind();
    this.syncMode();
    this.syncFormat();
    this.syncSummary();
    this.show(this.step, false);
  }

  private section(step: WizardStep): HTMLElement | null {
    return this.root.querySelector<HTMLElement>(`[data-step="${step}"]`);
  }

  private bind(): void {
    const { root, form } = this;
    root.addEventListener('click', (e) => {
      const t = e.target as HTMLElement;
      const link = t.closest<HTMLElement>('[data-step-link]');
      if (link && isStep(link.dataset.stepLink)) {
        e.preventDefault();
        this.show(link.dataset.stepLink, true);
        return;
      }
      if (t.closest('[data-wizard-next]')) {
        e.preventDefault();
        this.next();
        return;
      }
      if (t.closest('[data-wizard-back]')) {
        e.preventDefault();
        this.back();
        return;
      }
      const suggestion = t.closest<HTMLElement>('[data-premise-suggestion]');
      if (suggestion) {
        e.preventDefault();
        const premise = form.elements.namedItem('premise');
        if (premise instanceof HTMLTextAreaElement) {
          premise.value = suggestion.dataset.premiseSuggestion ?? '';
          premise.dispatchEvent(new Event('input', { bubbles: true }));
          premise.focus();
        }
      }
    });
    // Enter in a field means Next until the last step; only Generate submits.
    form.addEventListener('submit', (e) => {
      if (this.step !== WIZARD_STEPS[WIZARD_STEPS.length - 1]) {
        e.preventDefault();
        this.next();
      }
    });
    form.addEventListener('input', () => this.syncSummary());
    form.addEventListener('change', (e) => {
      const t = e.target as HTMLInputElement;
      if (t.name === 'mode') this.syncMode();
      if (t.name === 'format') this.syncFormat();
      this.syncSummary();
    });
  }

  /** stepValid reports the native validity of the current step's enabled fields. */
  private stepValid(): boolean {
    const section = this.section(this.step);
    if (!section) return true;
    const fields = section.querySelectorAll<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>('input, textarea, select');
    for (const field of Array.from(fields)) {
      if (field.disabled || field.closest('[hidden]')) continue;
      if (!field.checkValidity()) {
        field.reportValidity();
        return false;
      }
    }
    return true;
  }

  next(): void {
    if (!this.stepValid()) return;
    const i = WIZARD_STEPS.indexOf(this.step);
    if (i < WIZARD_STEPS.length - 1) this.show(WIZARD_STEPS[i + 1], true);
  }

  back(): void {
    const i = WIZARD_STEPS.indexOf(this.step);
    if (i > 0) this.show(WIZARD_STEPS[i - 1], true);
  }

  /** show opens one step and keeps the stepper, buttons and URL in line. */
  show(step: WizardStep, focus: boolean): void {
    this.step = step;
    const index = WIZARD_STEPS.indexOf(step);
    const last = index === WIZARD_STEPS.length - 1;
    this.root.dataset.step = step;
    WIZARD_STEPS.forEach((s, i) => {
      const section = this.section(s);
      if (section) section.hidden = s !== step;
      const item = this.root.querySelector<HTMLElement>(`[data-step-item="${s}"]`);
      item?.classList.toggle('is-done', i < index);
      item?.classList.toggle('is-current', i === index);
      const link = item?.querySelector<HTMLElement>('[data-step-link]');
      if (link) {
        if (i === index) link.setAttribute('aria-current', 'step');
        else link.removeAttribute('aria-current');
      }
    });
    const back = this.root.querySelector<HTMLElement>('[data-wizard-back]');
    const next = this.root.querySelector<HTMLElement>('[data-wizard-next]');
    const submit = this.root.querySelector<HTMLElement>('[data-wizard-submit]');
    if (back) back.hidden = index === 0;
    if (next) {
      next.hidden = last;
      const label = this.section(step)?.dataset.nextLabel;
      const text = next.querySelector('[data-wizard-next-text]');
      if (label && text) text.textContent = label;
    }
    if (submit) submit.hidden = !last;
    if (window.location.pathname.startsWith('/studio/new/')) {
      window.history.replaceState(window.history.state, '', `/studio/new/${step}`);
    }
    if (focus) {
      this.section(step)?.querySelector<HTMLElement>('h2')?.focus({ preventScroll: true });
      if (typeof window.scrollTo === 'function') window.scrollTo({ top: 0 });
    }
  }

  /** syncMode shows the premise or the adaptation fields and disables the other set. */
  syncMode(): void {
    const mode = fieldValue(this.form, 'mode') === 'adapt' ? 'adapt' : 'premise';
    this.root.querySelectorAll<HTMLElement>('[data-mode-panel]').forEach((panel) => {
      const active = panel.dataset.modePanel === mode;
      panel.hidden = !active;
      panel.querySelectorAll<HTMLInputElement | HTMLTextAreaElement>('input, textarea').forEach((field) => {
        field.disabled = !active;
      });
    });
    const premise = this.form.elements.namedItem('premise');
    if (premise instanceof HTMLTextAreaElement) premise.required = mode === 'premise';
  }

  /** syncFormat disables the page size for a webtoon, which is one long strip. */
  syncFormat(): void {
    const size = this.form.elements.namedItem('page_size');
    if (size instanceof HTMLSelectElement) size.disabled = fieldValue(this.form, 'format') === 'webtoon';
  }

  /** syncSummary fills "Your manga so far" from the form. */
  syncSummary(): void {
    const { root, form } = this;
    const put = (key: string, text: string) => {
      const el = root.querySelector<HTMLElement>(`[data-summary="${key}"]`);
      if (el) el.textContent = text || el.dataset.empty || '';
    };
    put('title', fieldValue(form, 'title').trim());
    const premiseEl = root.querySelector<HTMLElement>('[data-summary="premise"]');
    if (premiseEl) {
      const premise = fieldValue(form, 'premise').trim();
      if (fieldValue(form, 'mode') === 'adapt') premiseEl.textContent = premiseEl.dataset.adapt ?? '';
      else if (premise) premiseEl.textContent = (premiseEl.dataset.quote ?? '{text}').replace('{text}', premise);
      else premiseEl.textContent = premiseEl.dataset.empty ?? '';
    }
    put('genres', checkedLabels(form, 'genres').join(' · '));
    put('style', checkedLabels(form, 'style').join(''));
    put('format', checkedLabels(form, 'format').join(''));
  }
}

/** StoryViews switches the C1 Prose / Panels views in place. */
export class StoryViews {
  readonly root: HTMLElement;

  constructor(root: HTMLElement) {
    this.root = root;
    root.addEventListener('click', (e) => {
      const t = e.target as HTMLElement;
      const toggle = t.closest<HTMLAnchorElement>('[data-view-toggle] a[href]');
      if (toggle) {
        e.preventDefault();
        this.show(viewOf(toggle));
        return;
      }
      const scene = t.closest<HTMLAnchorElement>('a[data-scene-link]');
      if (scene) {
        root.querySelectorAll('a[data-scene-link][aria-current]').forEach((a) => a.removeAttribute('aria-current'));
        scene.setAttribute('aria-current', 'location');
      }
    });
  }

  /** show displays one view; Prose first copies unsaved edits from the form. */
  show(view: 'prose' | 'panels'): void {
    const { root } = this;
    const form = root.querySelector<HTMLFormElement>('form[data-view-panel="panels"]');
    if (view === 'prose' && form) mirror(root, form);
    root.querySelectorAll<HTMLElement>('[data-view-panel]').forEach((panel) => {
      panel.hidden = panel.dataset.viewPanel !== view;
    });
    root.querySelectorAll<HTMLAnchorElement>('[data-view-toggle] a[href]').forEach((a) => {
      if (viewOf(a) === view) a.setAttribute('aria-current', 'true');
      else a.removeAttribute('aria-current');
    });
    root.querySelectorAll<HTMLAnchorElement>('a[data-scene-link]').forEach((a) => {
      const href = view === 'prose' ? a.dataset.proseHref : a.dataset.panelsHref;
      if (href) a.setAttribute('href', href);
    });
    const url = new URL(window.location.href);
    if (view === 'prose') url.searchParams.set('view', 'prose');
    else url.searchParams.delete('view');
    window.history.replaceState(window.history.state, '', url.pathname + url.search + url.hash);
  }
}

function viewOf(a: HTMLAnchorElement): 'prose' | 'panels' {
  return new URL(a.href, window.location.href).searchParams.get('view') === 'prose' ? 'prose' : 'panels';
}

/** mirror copies each form field into the prose node that names it (data-mirror). */
export function mirror(root: ParentNode, form: HTMLFormElement): void {
  root.querySelectorAll<HTMLElement>('[data-mirror]').forEach((node) => {
    const name = node.dataset.mirror ?? '';
    const el = form.elements.namedItem(name);
    if (!(el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement)) return;
    const value = el.value.trim();
    node.textContent = value || node.dataset.fallback || '';
  });
}

const wizards = new WeakMap<HTMLElement, Wizard>();
const stories = new WeakMap<HTMLElement, StoryViews>();

/** enhanceStudio wires every wizard and story editor under root once. */
export function enhanceStudio(root: ParentNode = document): void {
  root.querySelectorAll<HTMLElement>('[data-wizard]').forEach((el) => {
    if (!wizards.has(el)) wizards.set(el, new Wizard(el));
  });
  root.querySelectorAll<HTMLElement>('[data-story-views]').forEach((el) => {
    if (!stories.has(el)) stories.set(el, new StoryViews(el));
  });
}

if (typeof document !== 'undefined') {
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', () => enhanceStudio());
  else enhanceStudio();
}
