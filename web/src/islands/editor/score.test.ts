import {describe, expect, it} from 'vitest';
import {scorePercent} from './score';

describe('measured score labels', () => {
  it('keeps a failing measurement below the visible passing boundary', () => {
    expect(scorePercent(.899999)).toBe(89);
    expect(scorePercent(.9 - Number.EPSILON)).toBe(89);
    expect(scorePercent(.9)).toBe(90);
    expect(scorePercent(.9003)).toBe(90);
  });
  it('retains exact percentages despite floating point representation', () => {
    expect(scorePercent(.58)).toBe(58);
    expect(scorePercent(1)).toBe(100);
  });
});
