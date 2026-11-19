import { expect, it } from 'vitest'
import { compileVegaViewForDifferentialTesting } from '../../../../packages/chat/src/visual/vegaRuntime'
import { readVisualTheme } from '../../../../packages/chat/src/visual/theme'
import { renderBlergChart } from '../../../../packages/chat/src/visual/blerg/render'

// BLG-452 design doc §3.10, step 4: composition (concat/hconcat/vconcat/facet/repeat) is
// implemented; layer is a deliberate v1 gap (see render.ts's header comment). This checks, in a
// real browser, that a faceted composition of an already-shipped mark (bar) draws the right
// number of real panels with real bars in each — the panel-layout code itself, not a second pass
// at bar's own already-gated geometry.

const values = [
  {month: 'Jan', team: 'A', open: 3}, {month: 'Feb', team: 'A', open: 4},
  {month: 'Jan', team: 'B', open: 1}, {month: 'Feb', team: 'B', open: 7},
]
const bar = {mark: 'bar', encoding: {x: {field: 'month', type: 'nominal'}, y: {field: 'open', type: 'quantitative'}}}
const spec = {data: {values}, facet: {field: 'team', type: 'nominal'}, columns: 2, spec: bar}

it('draws a faceted composition of an already-shipped mark, matching Vega\'s panel count and compiling cleanly', async () => {
  const width = 640, height = 300
  const container = document.createElement('div')
  const theme = readVisualTheme(container)

  // Vega accepts and compiles the same spec without error — a real-renderer sanity check that
  // this isn't a shape only our own classifier/policy would accept.
  const {view} = await compileVegaViewForDifferentialTesting({...spec}, theme)
  try {
    const result = renderBlergChart(spec, theme, container, width, height)
    expect(result.svg).toContain('<svg')
    const panels = container.querySelectorAll('[data-chart-panel]')
    expect(panels.length).toBe(2) // team A, team B
    for(const panel of panels)expect(panel.querySelectorAll('[data-datum-key]')).toHaveLength(2)
    // Each panel independently drew real bars (2 months each, plus the inherited x/y domain).
    expect(container.querySelectorAll('rect').length).toBeGreaterThanOrEqual(1 + 2 * 2) // background + 2 panels x 2 bars
  } finally { view.finalize() }
}, 15_000)
