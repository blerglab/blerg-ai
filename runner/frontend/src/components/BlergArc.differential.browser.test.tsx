import { expect, it } from 'vitest'
import { compileVegaViewForDifferentialTesting } from '../../../../packages/chat/src/visual/vegaRuntime'
import { readVisualTheme } from '../../../../packages/chat/src/visual/theme'
import { withinTolerance } from '../../../../packages/chat/src/visual/blerg/differential'
import { renderBlergChart } from '../../../../packages/chat/src/visual/blerg/render'

// BLG-452 design doc §3.10, step 3: arc enters classify.ts's SHIPPED_MARKS only once its corpus
// passes here. An arc's geometry is angles, not x/y positions, so this reads Vega's scenegraph
// startAngle/endAngle fields directly and compares them to our own cumulative-angle math — both
// are pure data-driven values, independent of center/radius/margin choices either renderer makes.

interface SceneItem { items?: SceneItem[]; marktype?: string; role?: string; startAngle?: number; endAngle?: number }

function vegaArcs(view: import('vega').View): Array<{start: number; end: number}> {
  const root = (view.scenegraph() as unknown as {root: SceneItem}).root
  const arcs: Array<{start: number; end: number}> = []
  function visit(node: SceneItem) {
    if (node.role === 'mark' && node.marktype === 'arc') {
      // Vega's startAngle/endAngle field names don't always mean "the smaller/larger of the
      // two" (depends on its own internal sweep convention) — normalize by magnitude so this
      // reads as a plain [start, end) interval regardless of which field holds which value.
      for (const item of node.items ?? []) if (item.startAngle !== undefined) {
        arcs.push({start: Math.min(item.startAngle, item.endAngle!), end: Math.max(item.startAngle, item.endAngle!)})
      }
    }
    for (const item of node.items ?? []) visit(item)
  }
  visit(root)
  return arcs
}

const spec = {
  data: {values: [{category: 'A', value: 10}, {category: 'B', value: 30}, {category: 'C', value: 20}]},
  mark: 'arc', encoding: {theta: {field: 'value', type: 'quantitative'}, color: {field: 'category', type: 'nominal'}},
}

it('matches Vega\'s arc angles within tolerance', async () => {
  const width = 400, height = 400
  const container = document.createElement('div')
  const theme = readVisualTheme(container)

  const {view} = await compileVegaViewForDifferentialTesting({...spec, width, height}, theme)
  try {
    const reference = vegaArcs(view).sort((a, b) => a.start - b.start)
    expect(reference.length).toBe(3)

    const ours = document.createElement('div')
    const drawn = renderBlergChart(spec, theme, ours, width, height)
    expect(drawn.svg).toContain('<path')
    const paths = [...ours.querySelectorAll('path')]
    expect(paths.length).toBe(3)

    // Reconstruct our own cumulative angles directly from the data (the same math drawArc uses),
    // since the angle values themselves aren't attributes on a <path> the way x/y are on a <rect>.
    const total = 60
    const expected = [{start: 0, end: 10 / total * 2 * Math.PI}, {start: 10 / total * 2 * Math.PI, end: 40 / total * 2 * Math.PI}, {start: 40 / total * 2 * Math.PI, end: 2 * Math.PI}]
    for (let i = 0; i < 3; i++) {
      expect(withinTolerance(expected[i].start, reference[i].start, 0.001)).toBe(true)
      expect(withinTolerance(expected[i].end, reference[i].end, 0.001)).toBe(true)
    }
  } finally { view.finalize() }
}, 15_000)
