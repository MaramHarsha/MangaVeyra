import { describe, expect, it } from 'vitest';
import { islandNames, parseProps } from './registry';

describe('island registry', () => {
  it('registers the demo island', () => {
    expect(islandNames()).toContain('demo');
    expect(islandNames()).toContain('editor');
  });
  it('parses props safely', () => {
    const el = document.createElement('div');
    el.dataset.props = '{"label":"Pages","start":3}';
    expect(parseProps(el)).toEqual({ label: 'Pages', start: 3 });
    el.dataset.props = '{broken';
    expect(parseProps(el)).toEqual({});
  });
});
