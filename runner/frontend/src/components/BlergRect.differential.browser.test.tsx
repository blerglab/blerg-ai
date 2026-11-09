import { expect, it } from 'vitest'
import { compileVegaViewForDifferentialTesting } from '../../../../packages/chat/src/visual/vegaRuntime'
import { readVisualTheme } from '../../../../packages/chat/src/visual/theme'
import { withinTolerance } from '../../../../packages/chat/src/visual/blerg/differential'
import { renderBlergChart } from '../../../../packages/chat/src/visual/blerg/render'

// BLG-452 design doc §3.10, step 3: rect (heatmap) enters classify.ts's SHIPPED_MARKS only once
// its corpus passes here. Like line.ts's gate, this compares our own band-scale cell size
// against Vega's own named band scale directly (view.scale), since a heatmap's margin sizing
// (driven by category label widths on both axes) is its own, separate concern from this mark's
// scale math.

const noTitle = {axis: {title: null}}
const spec = {
  data: {values: [
    {x: 'A', y: '1', v: 1}, {x: 'B', y: '1', v: 5}, {x: 'A', y: '2', v: 3}, {x: 'B', y: '2', v: 9},
  ]},
  mark: 'rect', encoding: {x: {field: 'x', type: 'ordinal', ...noTitle}, y: {field: 'y', type: 'ordinal', ...noTitle}, color: {field: 'v', type: 'quantitative'}},
}

it('matches Vega\'s rect band-scale step within tolerance', async () => {
  const width = 640, height = 360
  const container = document.createElement('div')
  const theme = readVisualTheme(container)

  const {view} = await compileVegaViewForDifferentialTesting({...spec, width, height}, theme)
  try {
    const vegaX = view.scale('x') as {(v: string): number; bandwidth: () => number}
    const vegaY = view.scale('y') as {(v: string): number; bandwidth: () => number}

    const ours = document.createElement('div')
    renderBlergChart(spec, theme, ours, width, height)
    const cells = [...ours.querySelectorAll('rect')].slice(1) // index 0 is the background rect
      .filter(el => el.getAttribute('data-legend-swatch') !== 'true') // exclude the continuous legend's own gradient swatch (BLG-452 priority 2)
    expect(cells.length).toBe(4)

    // Band *ratios* must match: our cell width as a fraction of the step between two adjacent
    // categories should equal Vega's, since that ratio is pure band-scale math (paddingInner),
    // independent of either side's total plot width.
    const ourWidths = cells.map(c => Number(c.getAttribute('width')))
    const ourHeights = cells.map(c => Number(c.getAttribute('height')))
    const ourStepX = Math.abs(Number(cells[1].getAttribute('x')) - Number(cells[0].getAttribute('x'))) || ourWidths[0]
    const vegaStepX = Math.abs(vegaX('B') - vegaX('A'))
    const vegaStepY = Math.abs(vegaY('2') - vegaY('1'))
    expect(withinTolerance(ourWidths[0] / ourStepX, vegaX.bandwidth() / vegaStepX, 0.01)).toBe(true)
    const ourStepY = Math.abs(Number(cells[2].getAttribute('y')) - Number(cells[0].getAttribute('y'))) || ourHeights[0]
    expect(withinTolerance(ourHeights[0] / ourStepY, vegaY.bandwidth() / vegaStepY, 0.01)).toBe(true)
  } finally { view.finalize() }
}, 15_000)

it('continuous color legend: title and tick labels match Vega\'s own legend scenegraph (BLG-452 priority 2)', async () => {
  // Confirmed empirically (not assumed): Vega's continuous legend titles with the field name and
  // labels d3-array ticks *within* the domain (2/4/6/8 for a 1..9 domain here), not the domain's
  // raw min/max — legend.ts's drawContinuousLegend was written to match this exactly.
  const width = 640, height = 360
  const container = document.createElement('div')
  const theme = readVisualTheme(container)
  const {view} = await compileVegaViewForDifferentialTesting({...spec, width, height}, theme)
  try {
    interface SceneItem { items?: SceneItem[]; marktype?: string; role?: string; text?: string }
    const root = (view.scenegraph() as unknown as {root: SceneItem}).root
    const vegaLabels: string[] = []
    let vegaTitle = ''
    function visit(node: SceneItem) {
      if (node.role === 'legend-label' && node.marktype === 'text') vegaLabels.push(...(node.items ?? []).map(i => String(i.text)))
      if (node.role === 'legend-title' && node.marktype === 'text') vegaTitle = String((node.items ?? [])[0]?.text ?? '')
      for (const item of node.items ?? []) visit(item)
    }
    visit(root)

    const ours = document.createElement('div')
    renderBlergChart(spec, theme, ours, width, height)
    const ourTexts = [...ours.querySelectorAll('text')].map(el => el.textContent ?? '')
    expect(ourTexts).toContain(vegaTitle)
    for (const label of vegaLabels) expect(ourTexts).toContain(label)
  } finally { view.finalize() }
}, 15_000)
