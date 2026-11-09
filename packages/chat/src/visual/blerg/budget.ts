// BLG-452 design doc §3.6/M10: our own, smaller-than-policy's limits. Exceeding one is a
// classification outcome (classify.ts returns 'fallback'), never a thrown error — ChartRefused
// stays reserved for the shared policy layer (vegaPolicy.ts/policy.ts), which both renderers
// inherit unchanged.
export const MAX_POINTS = {
  // line/area: a single <path>, O(n) with a small constant — safe up to the policy ceiling
  // itself (vegaPolicy.ts's MAX_ARRAY), §3.6's stated budget.
  path: 10_000,
  // bar/point/circle/square/rect/arc/rule/text: one drawn shape per datum, so bounded well below
  // the line/area case.
  discrete: 2_000,
} as const

const PATH_MARKS = new Set(['line', 'area'])

export function pointBudgetFor(markType: string): number {
  return PATH_MARKS.has(markType) ? MAX_POINTS.path : MAX_POINTS.discrete
}
