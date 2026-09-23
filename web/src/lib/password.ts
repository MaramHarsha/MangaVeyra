/**
 * Password strength estimate for the sign-up and reset forms (display only;
 * the server enforces the length policy). Returns 0 (empty/very weak) … 4.
 */
export interface Strength {
  level: 0 | 1 | 2 | 3 | 4;
  /** i18n-free key: "empty" | "weak" | "fair" | "good" | "strong". */
  key: 'empty' | 'weak' | 'fair' | 'good' | 'strong';
}

const COMMON = new Set([
  'password', 'password1', '12345678', '123456789', 'qwertyui', 'iloveyou', 'letmein1', 'mangaveyra',
  'abcdefgh', '11111111', 'sunshine', 'princess', 'football', 'baseball', 'welcome1', 'admin123',
]);

/** passwordStrength scores length and character variety, penalising repeats and common passwords. */
export function passwordStrength(pw: string, minLength = 10): Strength {
  if (!pw) return { level: 0, key: 'empty' };
  const lower = pw.toLowerCase();
  if (pw.length < minLength || COMMON.has(lower)) return { level: 1, key: 'weak' };
  let classes = 0;
  if (/[a-z]/.test(pw)) classes++;
  if (/[A-Z]/.test(pw)) classes++;
  if (/[0-9]/.test(pw)) classes++;
  if (/[^A-Za-z0-9]/.test(pw)) classes++;
  const unique = new Set(pw).size;
  let score = 1;
  if (pw.length >= minLength + 2) score++;
  if (pw.length >= 16) score++;
  if (classes >= 3) score++;
  if (classes >= 2 && pw.length >= 20) score++;
  if (unique < Math.min(6, pw.length / 2)) score -= 2;
  if (/^(.)\1+$/.test(pw)) score = 1;
  const level = Math.max(1, Math.min(4, score)) as Strength['level'];
  const key = (['empty', 'weak', 'fair', 'good', 'strong'] as const)[level];
  return { level, key };
}
