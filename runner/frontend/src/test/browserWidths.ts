/** Focused rounds exercise one width; the final gate retains every layout case. */
export function browserWidths<T extends number>(widths: readonly T[], phase: string | undefined = import.meta.env.VITE_BLERG_TEST_PHASE): readonly T[] {
  if (widths.length === 0) throw new Error('A browser test needs at least one width')
  return phase === 'touched' ? widths.slice(0, 1) : widths
}
