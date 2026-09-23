import { describe, expect, it } from 'vitest';
import { passwordStrength } from './password';

describe('passwordStrength', () => {
  it('rates empty and short passwords', () => {
    expect(passwordStrength('').level).toBe(0);
    expect(passwordStrength('short1!').key).toBe('weak');
  });
  it('rejects common passwords', () => {
    expect(passwordStrength('password1', 8).key).toBe('weak');
  });
  it('rewards length and variety', () => {
    expect(passwordStrength('tidebreakcourier').level).toBeGreaterThanOrEqual(2);
    expect(passwordStrength('Salt-Road-Lighthouse-8').key).toBe('strong');
  });
  it('punishes repeated characters', () => {
    expect(passwordStrength('aaaaaaaaaaaaaaaa').level).toBe(1);
  });
});
