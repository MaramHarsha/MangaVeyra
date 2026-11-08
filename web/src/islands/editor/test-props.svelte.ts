// Test-only helper: a $state props object, so a component mounted in vitest
// sees prop changes the way it does when the editor updates them.
export function reactiveProps<T extends Record<string, unknown>>(value: T): T {
  const props = $state(value);
  return props;
}
