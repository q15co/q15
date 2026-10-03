// Fail at a fixture/setup boundary instead of erasing nullability with `!`.
export function required<T>(value: T | null | undefined): T {
  if (value === undefined || value === null) throw new Error("Required test value is missing.");
  return value;
}
