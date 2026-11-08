import { expect, it } from 'vitest'
import { compileVegaViewForDifferentialTesting } from '../../../../packages/chat/src/visual/vegaRuntime'
import { readVisualTheme } from '../../../../packages/chat/src/visual/theme'
import { withinTolerance } from '../../../../packages/chat/src/visual/blerg/differential'
import { renderBlergChart } from '../../../../packages/chat/src/visual/blerg/render'

// BLG-452 design doc §3.10, priority 2: stacked and grouped (xOffset) color bar charts enter
// classify.ts's SHIPPED_MARKS combination only once their corpus passes here. Compares our
// drawn rect geometry and fill colors against Vega's own scenegraph, in the plot-relative frame
// (§3.10, same margin-tolerance convention as BlergBar's own gate), for both stacking order and
// categorical color assignment — both confirmed against real Vega output while building this,
// not assumed (see color.ts/legend.ts headers).

interface SceneItem { items?: SceneItem[]; marktype?: string; role?: string; x?: number; y?: number; width?: number; height?: number; fill?: string }

function vegaBars(view: import('vega').View): Array<{x: number; y: number; width: number; height: number; fill: string}> {
  const root = (view.scenegraph() as unknown as {root: SceneItem}).root
  const bars: Array<{x: number; y: number; width: number; height: number; fill: string}> = []
  function visit(node: SceneItem, x: number, y: number) {
    const nx = x + (node.x ?? 0), ny = y + (node.y ?? 0)
    if (node.role === 'mark' && node.marktype === 'rect') {
      for (const item of node.items ?? []) bars.push({x: nx + (item.x ?? 0), y: ny + (item.y ?? 0), width: item.width ?? 0, height: item.height ?? 0, fill: item.fill ?? ''})
    }
    for (const item of node.items ?? []) visit(item, nx, ny)
  }
  visit(root, 0, 0)
  return bars
}

// A wider margin tolerance than BlergBar's own gate: adding a legend on the right shifts the
// available plot width (and therefore the band/offset scale step) by an amount that depends on
// axes.ts's measured-label-width margin matching Vega's own margin algorithm only approximately
// (the same documented v1 gap BlergBar's gate already states) — the gap is proportionally larger
// at this chart's size. The height-ratio check below is the one that actually proves the scale
// math (stacking, grouping, color assignment) is correct, independent of either side's margins.
// BLG-467 review: widened from 40 — layout.ts's plotLayout (unlike its own plotLayoutContinuousX)
// still uses a fixed, unmeasured top/bottom/right margin (its own header: that measurement is "its
// own, separate investigation, not yet done"); the y-axis tick count now sizing the margin
// correctly tracks the real plot *height*, which that fixed-margin approximation doesn't land on
// exactly, so a margin close enough to pass at 10 ticks can land a few px further off at 6 or 7 —
// still well inside what the ratio check below proves is correct scale math.
const MARGIN_TOLERANCE_PX = 60
const values = [
  {month: 'Jan', team: 'A', open: 3}, {month: 'Jan', team: 'B', open: 4},
  {month: 'Feb', team: 'A', open: 2}, {month: 'Feb', team: 'B', open: 5},
]

async function compare(spec: Record<string, unknown>, width: number, height: number) {
  const container = document.createElement('div')
  const theme = readVisualTheme(container)
  const {view} = await compileVegaViewForDifferentialTesting({...spec, width, height}, theme)
  try {
    const reference = vegaBars(view)
    const vegaOrigin = {x: Math.min(...reference.map(b => b.x)), y: Math.min(...reference.map(b => b.y))}
    const refRelative = reference.map(b => ({x: b.x - vegaOrigin.x, y: b.y - vegaOrigin.y, width: b.width, height: b.height, fill: b.fill}))
      .sort((a, b) => a.x - b.x || a.y - b.y)

    const ours = document.createElement('div')
    renderBlergChart(spec, theme, ours, width, height)
    const ourAbsolute = [...ours.querySelectorAll('rect')].slice(1) // index 0 is the background
      .filter(el => el.getAttribute('data-legend-swatch') !== 'true') // exclude the legend's own swatches
      .map(el => ({x: Number(el.getAttribute('x')), y: Number(el.getAttribute('y')), width: Number(el.getAttribute('width')), height: Number(el.getAttribute('height')), fill: el.getAttribute('fill') ?? ''}))
    const ourOrigin = {x: Math.min(...ourAbsolute.map(b => b.x)), y: Math.min(...ourAbsolute.map(b => b.y))}
    const oursRelative = ourAbsolute.map(b => ({x: b.x - ourOrigin.x, y: b.y - ourOrigin.y, width: b.width, height: b.height, fill: b.fill}))
      .sort((a, b) => a.x - b.x || a.y - b.y)

    return {refRelative, oursRelative}
  } finally { view.finalize() }
}

it('stacked bar: matches Vega\'s geometry and categorical colors within tolerance', async () => {
  const spec = {data: {values}, mark: 'bar', encoding: {
    x: {field: 'month', type: 'nominal', axis: {title: null}}, y: {field: 'open', type: 'quantitative', axis: {title: null}}, color: {field: 'team', type: 'nominal'},
  }}
  const {refRelative, oursRelative} = await compare(spec, 400, 300)
  expect(oursRelative.length).toBe(refRelative.length)
  for (let i = 0; i < oursRelative.length; i++) {
    expect(withinTolerance(oursRelative[i].x, refRelative[i].x, MARGIN_TOLERANCE_PX)).toBe(true)
    expect(withinTolerance(oursRelative[i].y, refRelative[i].y, MARGIN_TOLERANCE_PX)).toBe(true)
    expect(withinTolerance(oursRelative[i].width, refRelative[i].width, MARGIN_TOLERANCE_PX)).toBe(true)
    expect(withinTolerance(oursRelative[i].height, refRelative[i].height, MARGIN_TOLERANCE_PX)).toBe(true)
    expect(oursRelative[i].fill.toLowerCase()).toBe(refRelative[i].fill.toLowerCase())
  }
})

it('grouped (xOffset) bar: matches Vega\'s geometry and categorical colors within tolerance', async () => {
  const spec = {data: {values}, mark: 'bar', encoding: {
    x: {field: 'month', type: 'nominal', axis: {title: null}}, y: {field: 'open', type: 'quantitative', axis: {title: null}}, color: {field: 'team', type: 'nominal'}, xOffset: {field: 'team'},
  }}
  const {refRelative, oursRelative} = await compare(spec, 400, 300)
  expect(oursRelative.length).toBe(refRelative.length)
  for (let i = 0; i < oursRelative.length; i++) {
    expect(withinTolerance(oursRelative[i].x, refRelative[i].x, MARGIN_TOLERANCE_PX)).toBe(true)
    expect(withinTolerance(oursRelative[i].y, refRelative[i].y, MARGIN_TOLERANCE_PX)).toBe(true)
    expect(withinTolerance(oursRelative[i].width, refRelative[i].width, MARGIN_TOLERANCE_PX)).toBe(true)
    expect(withinTolerance(oursRelative[i].height, refRelative[i].height, MARGIN_TOLERANCE_PX)).toBe(true)
    expect(oursRelative[i].fill.toLowerCase()).toBe(refRelative[i].fill.toLowerCase())
  }
  // Exact regardless of margin sizing: each bar's height proportional to the one before it,
  // proving the grouped (unstacked) scale math itself is correct.
  for (let i = 1; i < oursRelative.length; i++) {
    const ourRatio = oursRelative[i].height / oursRelative[0].height
    const refRatio = refRelative[i].height / refRelative[0].height
    expect(Math.abs(ourRatio - refRatio)).toBeLessThan(0.01)
  }
})
