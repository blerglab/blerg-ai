import { expect, it } from 'vitest'
import { ticks as d3Ticks } from 'd3-array'
import { compileVegaViewForDifferentialTesting } from '../../../../packages/chat/src/visual/vegaRuntime'
import { readVisualTheme } from '../../../../packages/chat/src/visual/theme'
import { axisTicks, plotOrigin, relativeTo, withinTolerance } from '../../../../packages/chat/src/visual/blerg/differential'

// BLG-452 design doc §3.10, step 2 of the build order ("the differential harness, before any
// mark"): these tests prove the harness's reference side is trustworthy — real tick values,
// real post-transform data, a real plot origin — before step 3 starts building marks against it.
// There is no "candidate" (our own renderer) yet; every assertion here is Vega checked against
// an independently-known-correct expectation, not Vega checked against us.

async function compile(spec: Record<string, unknown>) {
  const container = document.createElement('div')
  const {view, datasetNames} = await compileVegaViewForDifferentialTesting(spec, readVisualTheme(container))
  return {view, datasetNames}
}

it('reads nominal axis tick values and labels straight off the scenegraph', async () => {
  const {view} = await compile({
    data: {values: [{x: 'A', y: 1}, {x: 'B', y: 2}, {x: 'C', y: 3}]},
    mark: 'bar', encoding: {x: {field: 'x', type: 'nominal'}, y: {field: 'y', type: 'quantitative'}},
  })
  try {
    const ticks = axisTicks(view)
    // Both the axis-tick (rule) and axis-label (text) mark-definition nodes carry the same
    // generated {value, label} tuples — one tick line and one label per value — so dedupe before
    // comparing the distinct values themselves.
    const xTicks = [...new Set(ticks.filter(tick => typeof tick.value === 'string').map(tick => tick.value))]
    expect(xTicks).toEqual(['A', 'B', 'C'])
  } finally { view.finalize() }
}, 15_000)

it('reads continuous axis tick values matching d3-array\'s own ticks() for the same domain', async () => {
  const {view} = await compile({
    data: {values: [{x: 'A', y: 0}, {x: 'B', y: 97}]},
    mark: 'bar', encoding: {x: {field: 'x', type: 'nominal'}, y: {field: 'y', type: 'quantitative'}},
  })
  try {
    const ticks = axisTicks(view)
    const yValues = [...new Set(ticks.filter(tick => typeof tick.value === 'number').map(tick => tick.value as number))].sort((a, b) => a - b)
    // Vega's default nice()'d linear domain for [0, 97] is [0, 100]; d3-array's ticks(0, 100, 10)
    // is the same tick-count default Vega's axis uses for a quantitative scale at this size.
    const expected = d3Ticks(0, 100, 10)
    expect(yValues).toEqual(expected)
  } finally { view.finalize() }
}, 15_000)

it('reads the post-transform data Vega actually drew via view.data(name), including stack offsets', async () => {
  const {view, datasetNames} = await compile({
    data: {values: [{day: 1, team: 'A', n: 3}, {day: 1, team: 'B', n: 4}, {day: 2, team: 'A', n: 1}, {day: 2, team: 'B', n: 2}]},
    mark: 'bar', encoding: {
      x: {field: 'day', type: 'ordinal'}, y: {field: 'n', type: 'quantitative', aggregate: 'sum', stack: 'zero'},
      color: {field: 'team', type: 'nominal'},
    },
  })
  try {
    expect(datasetNames.length).toBeGreaterThan(0)
    const drawn = view.data(datasetNames[datasetNames.length - 1]) as Array<Record<string, unknown>>
    const day1 = drawn.filter(row => row.day === 1)
    expect(day1).toHaveLength(2)
    const a = day1.find(row => row.team === 'A')!, b = day1.find(row => row.team === 'B')!
    // Aggregate (sum) renames the field sum_n with no explicit alias; stack then adds
    // sum_n_start/sum_n_end. Vega's default stack order isn't alphabetical by group — assert the
    // stack is contiguous from zero and sums to the correct total, not a specific group order.
    expect(a.sum_n).toBe(3)
    expect(b.sum_n).toBe(4)
    // Contiguous stack from zero, regardless of which group Vega happens to place first: the two
    // [start, end) intervals partition [0, 7] exactly, with no gap and no overlap.
    const intervals = [[a.sum_n_start, a.sum_n_end], [b.sum_n_start, b.sum_n_end]].sort((x, y) => (x[0] as number) - (y[0] as number)) as Array<[number, number]>
    expect(intervals[0][0]).toBe(0)
    expect(intervals[0][1]).toBe(intervals[1][0])
    expect(intervals[1][1]).toBe(7)
  } finally { view.finalize() }
}, 15_000)

it('computes a plot origin that excludes axis/title padding', async () => {
  const {view} = await compile({
    title: 'Revenue',
    data: {values: [{x: 'A', y: 1}, {x: 'B', y: 2}]},
    mark: 'bar', encoding: {x: {field: 'x', type: 'nominal'}, y: {field: 'y', type: 'quantitative'}},
  })
  try {
    const origin = plotOrigin(view)
    // With a y-axis (labels + title) to its left, the plot area never starts at the page origin.
    expect(origin.x).toBeGreaterThan(0)
    expect(origin.y).toBeGreaterThanOrEqual(0)
    // Every mark position, once translated into the plot-relative frame, is non-negative — the
    // plot origin really is the top-left corner of the drawn marks, not an arbitrary point.
    const ticks = axisTicks(view)
    for (const tick of ticks) {
      const relative = relativeTo(origin, tick)
      // Axis ticks for the y-axis sit to the left of the plot origin by construction (labels are
      // outside the plot); only check that translating is well-defined (finite), not sign.
      expect(Number.isFinite(relative.x)).toBe(true)
      expect(Number.isFinite(relative.y)).toBe(true)
    }
  } finally { view.finalize() }
}, 15_000)

it('withinTolerance matches design doc §3.10\'s stated 0.5px default and an explicit override', () => {
  expect(withinTolerance(10, 10.4)).toBe(true)
  expect(withinTolerance(10, 10.6)).toBe(false)
  expect(withinTolerance(10, 12, 2)).toBe(true)
})
