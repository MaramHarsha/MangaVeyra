import { beforeEach, describe, expect, it } from 'vitest';
import { StoryViews, Wizard, enhanceStudio, mirror } from './studio';

// A trimmed copy of the server-rendered B3 markup (internal/studioui/templates.templ).
const wizardHTML = (current = 'premise') => `
<div class="mv-wizard" data-wizard data-current="${current}">
  <form method="post" action="/studio/new/premise" novalidate>
    <ol>
      ${['premise', 'genre', 'style', 'format', 'generate']
        .map((s) => `<li data-step-item="${s}"><a href="#mv-step-${s}" data-step-link="${s}">${s}</a></li>`)
        .join('')}
    </ol>
    <section data-step="premise" data-next-label="Next: genre"><h2 tabindex="-1">Story</h2>
      <input name="title" required value="">
      <label><input type="radio" name="mode" value="premise" checked></label>
      <label><input type="radio" name="mode" value="adapt"></label>
      <div data-mode-panel="premise"><textarea name="premise"></textarea>
        <div data-premise-tries hidden><button type="button" data-premise-suggestion="A gull steals one letter a day">x</button></div>
      </div>
      <div data-mode-panel="adapt"><textarea name="source_text"></textarea><input type="file" name="source_file"></div>
    </section>
    <section data-step="genre" data-next-label="Next: style"><h2 tabindex="-1">Genre</h2>
      <label><input type="checkbox" name="genres" value="action"><span data-label>Action</span></label>
      <label><input type="checkbox" name="genres" value="mystery"><span data-label>Mystery</span></label>
    </section>
    <section data-step="style" data-next-label="Next: format"><h2 tabindex="-1">Style</h2>
      <label><input type="radio" name="style" value="seinen_bw" checked><span data-label>Seinen · B/W screentone</span></label>
      <label><input type="radio" name="style" value="chibi"><span data-label>Chibi · 4-koma</span></label>
    </section>
    <section data-step="format" data-next-label="Next: generate"><h2 tabindex="-1">Format</h2>
      <label><input type="radio" name="format" value="manga_rtl" checked><strong data-label>Manga · right to left</strong></label>
      <label><input type="radio" name="format" value="webtoon"><strong data-label>Vertical webtoon</strong></label>
      <select name="page_size"><option value="b5">B5</option></select>
    </section>
    <section data-step="generate"><h2 tabindex="-1">Generate</h2><input name="chapter_title" value="Chapter 1"></section>
    <footer>
      <button type="button" data-wizard-back hidden>Back</button>
      <button type="button" data-wizard-next hidden><span data-wizard-next-text>Next: genre</span></button>
      <button type="submit" data-wizard-submit>Generate</button>
    </footer>
  </form>
  <aside>
    <span data-summary="title" data-empty="Untitled manga">Untitled manga</span>
    <p data-summary="premise" data-empty="Your premise appears here." data-quote="“{text}”" data-adapt="Adapting existing text"></p>
    <dd data-summary="genres" data-empty="Not chosen yet"></dd>
    <dd data-summary="style"></dd>
    <dd data-summary="format"></dd>
  </aside>
</div>`;

function setup(current?: string): { root: HTMLElement; wizard: Wizard; form: HTMLFormElement } {
  document.body.innerHTML = wizardHTML(current);
  const root = document.querySelector<HTMLElement>('[data-wizard]')!;
  const wizard = new Wizard(root);
  return { root, wizard, form: root.querySelector('form')! };
}

const visible = (root: HTMLElement) =>
  Array.from(root.querySelectorAll<HTMLElement>('section[data-step]'))
    .filter((s) => !s.hidden)
    .map((s) => s.dataset.step);

const click = (el: Element | null) => el?.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));

describe('Wizard', () => {
  beforeEach(() => {
    window.history.replaceState(null, '', '/studio/new/premise');
    window.scrollTo = () => undefined; // jsdom does not implement scrolling
  });

  it('shows one step at a time with the stepper and buttons in line', () => {
    const { root } = setup();
    expect(root.classList.contains('is-enhanced')).toBe(true);
    expect(visible(root)).toEqual(['premise']);
    expect(root.querySelector('[data-wizard-back]')?.hasAttribute('hidden')).toBe(true);
    expect(root.querySelector('[data-wizard-next]')?.hasAttribute('hidden')).toBe(false);
    expect(root.querySelector('[data-wizard-submit]')?.hasAttribute('hidden')).toBe(true);
    expect(root.querySelector('[data-premise-tries]')?.hasAttribute('hidden')).toBe(false);
    expect(root.querySelector('[data-step-link="premise"]')?.getAttribute('aria-current')).toBe('step');
  });

  it('blocks Next on an invalid step, then moves forward and back', () => {
    const { root, form } = setup();
    click(root.querySelector('[data-wizard-next]'));
    expect(visible(root)).toEqual(['premise']); // title is required
    (form.elements.namedItem('title') as HTMLInputElement).value = 'Tidebreak Courier';
    (form.elements.namedItem('premise') as HTMLTextAreaElement).value = 'A courier who cannot read maps.';
    click(root.querySelector('[data-wizard-next]'));
    expect(visible(root)).toEqual(['genre']);
    expect(root.dataset.step).toBe('genre');
    expect(window.location.pathname).toBe('/studio/new/genre');
    expect(root.querySelector('[data-wizard-next-text]')?.textContent).toBe('Next: style');
    expect(root.querySelector('[data-step-item="premise"]')?.classList.contains('is-done')).toBe(true);
    click(root.querySelector('[data-wizard-back]'));
    expect(visible(root)).toEqual(['premise']);
  });

  it('opens the step named by the server (a step with a validation error)', () => {
    const { root } = setup('format');
    expect(visible(root)).toEqual(['format']);
    expect(window.location.pathname).toBe('/studio/new/format');
  });

  it('jumps with the stepper and only submits from the last step', () => {
    const { root, form } = setup();
    click(root.querySelector('[data-step-link="generate"]'));
    expect(visible(root)).toEqual(['generate']);
    expect(root.querySelector('[data-wizard-submit]')?.hasAttribute('hidden')).toBe(false);
    expect(root.querySelector('[data-wizard-next]')?.hasAttribute('hidden')).toBe(true);
    const submitAt = (step: string) => {
      click(root.querySelector(`[data-step-link="${step}"]`));
      const ev = new Event('submit', { bubbles: true, cancelable: true });
      form.dispatchEvent(ev);
      return ev.defaultPrevented;
    };
    expect(submitAt('generate')).toBe(false);
    expect(submitAt('style')).toBe(true); // Enter on an earlier step means Next
    expect(visible(root)).toEqual(['format']);
  });

  it('keeps the summary in step with the form', () => {
    const { root, form } = setup();
    const summary = (k: string) => root.querySelector(`[data-summary="${k}"]`)?.textContent;
    expect(summary('title')).toBe('Untitled manga');
    expect(summary('style')).toBe('Seinen · B/W screentone');
    expect(summary('format')).toBe('Manga · right to left');
    expect(summary('genres')).toBe('Not chosen yet');
    const title = form.elements.namedItem('title') as HTMLInputElement;
    title.value = 'Tidebreak Courier';
    title.dispatchEvent(new Event('input', { bubbles: true }));
    expect(summary('title')).toBe('Tidebreak Courier');
    click(root.querySelector('[data-premise-suggestion]'));
    expect((form.elements.namedItem('premise') as HTMLTextAreaElement).value).toBe('A gull steals one letter a day');
    expect(summary('premise')).toBe('“A gull steals one letter a day”');
    for (const g of form.querySelectorAll<HTMLInputElement>('input[name="genres"]')) g.checked = true;
    form.dispatchEvent(new Event('change', { bubbles: true }));
    expect(summary('genres')).toBe('Action · Mystery');
  });

  it('switches premise and adaptation fields so only one set posts', () => {
    const { root, form } = setup();
    const adapt = form.querySelector<HTMLInputElement>('input[name="mode"][value="adapt"]')!;
    const premise = form.elements.namedItem('premise') as HTMLTextAreaElement;
    const source = form.elements.namedItem('source_text') as HTMLTextAreaElement;
    expect(source.disabled).toBe(true);
    expect(premise.required).toBe(true);
    adapt.checked = true;
    adapt.dispatchEvent(new Event('change', { bubbles: true }));
    expect(source.disabled).toBe(false);
    expect(premise.disabled).toBe(true);
    expect(root.querySelector<HTMLElement>('[data-mode-panel="premise"]')?.hidden).toBe(true);
    expect(root.querySelector('[data-summary="premise"]')?.textContent).toBe('Adapting existing text');
  });

  it('disables the page size for a webtoon', () => {
    const { form } = setup();
    const size = form.elements.namedItem('page_size') as HTMLSelectElement;
    expect(size.disabled).toBe(false);
    const webtoon = form.querySelector<HTMLInputElement>('input[name="format"][value="webtoon"]')!;
    webtoon.checked = true;
    webtoon.dispatchEvent(new Event('change', { bubbles: true }));
    expect(size.disabled).toBe(true);
  });
});

const storyHTML = `
<div data-story-views>
  <a href="#scene-s1" data-scene-link data-panels-href="#scene-s1" data-prose-href="#prose-s1">Sc. 1</a>
  <div data-view-toggle>
    <nav class="mv-seg"><a href="/studio/p/p1/story/c1?view=prose">Prose</a><a href="/studio/p/p1/story/c1" aria-current="true">Panels</a></nav>
  </div>
  <article data-view-panel="prose" hidden>
    <h2 data-mirror="scene.s1.title">Harbour at dusk</h2>
    <strong data-mirror="panel.p1.dialogue.0.speaker" data-fallback="Caption">Kaito</strong>
  </article>
  <form data-view-panel="panels">
    <input name="scene.s1.title" value="Harbour at dusk">
    <input name="panel.p1.dialogue.0.speaker" value="Kaito">
  </form>
</div>`;

describe('StoryViews', () => {
  beforeEach(() => {
    window.history.replaceState(null, '', '/studio/p/p1/story/c1');
    document.body.innerHTML = storyHTML;
  });

  it('switches to Prose in place, showing unsaved edits', () => {
    const root = document.querySelector<HTMLElement>('[data-story-views]')!;
    new StoryViews(root);
    const form = root.querySelector('form')!;
    (form.elements.namedItem('scene.s1.title') as HTMLInputElement).value = 'Rooftop chase';
    (form.elements.namedItem('panel.p1.dialogue.0.speaker') as HTMLInputElement).value = '';
    click(root.querySelector('[data-view-toggle] a[href*="view=prose"]'));
    expect(root.querySelector<HTMLElement>('article')?.hidden).toBe(false);
    expect(form.hidden).toBe(true);
    expect(root.querySelector('h2')?.textContent).toBe('Rooftop chase');
    expect(root.querySelector('strong')?.textContent).toBe('Caption');
    expect(root.querySelector('a[data-scene-link]')?.getAttribute('href')).toBe('#prose-s1');
    expect(window.location.search).toBe('?view=prose');
    expect(root.querySelector('[data-view-toggle] a[href*="view=prose"]')?.getAttribute('aria-current')).toBe('true');

    click(root.querySelector('[data-view-toggle] a:not([href*="view"])'));
    expect(form.hidden).toBe(false);
    expect(window.location.search).toBe('');
    expect(root.querySelector('a[data-scene-link]')?.getAttribute('href')).toBe('#scene-s1');
  });

  it('marks the scene picked in the tree', () => {
    enhanceStudio(document);
    const link = document.querySelector('a[data-scene-link]')!;
    click(link);
    expect(link.getAttribute('aria-current')).toBe('location');
  });

  it('mirror ignores nodes without a matching field', () => {
    const root = document.querySelector<HTMLElement>('[data-story-views]')!;
    root.insertAdjacentHTML('beforeend', '<p data-mirror="missing">kept</p>');
    mirror(root, root.querySelector('form')!);
    expect(root.querySelector('[data-mirror="missing"]')?.textContent).toBe('kept');
  });
});
