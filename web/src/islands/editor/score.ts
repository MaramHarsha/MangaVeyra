// Whole-percent labels must never round a below-threshold measurement up to
// the 90% passing boundary. Progress indicators retain their usual rounding.
export function scorePercent(score: number): number {
  const percent = Math.max(0, Math.min(100, Math.floor(score * 100 + 1e-9)));
  return score < .9 && percent >= 90 ? 89 : percent;
}
