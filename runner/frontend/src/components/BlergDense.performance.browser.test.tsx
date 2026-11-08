import { expect, it } from 'vitest'
import { readVisualTheme } from '../../../../packages/chat/src/visual/theme'
import { renderBlergChart } from '../../../../packages/chat/src/visual/blerg/render'

// BLG-452 design doc §3.6, step 4's dense-data gate: a committed CI budget, not a soft target,
// at the stated 10,000-point cap (vegaPolicy.ts's MAX_ARRAY, which §3.6 also uses as the line/
// area point budget, budget.ts's MAX_POINTS.path). Runs in runner/frontend's existing
// `browser-performance` Vitest project (real Chromium via @vitest/browser-playwright, serial,
// after every other project — vitest.config.ts's browserProject(true)), not jsdom: jsdom
// estimates text width instead of measuring it, which would make a jsdom-only timing number
// meaningless for a layout whose left margin depends on real label measurement (layout.ts's
// measureText).
//
// Hardware this was authored and measured against (recorded per §3.6's own requirement — an open
// question in the design doc flags that the *actual* CI runner's spec still needs confirming by
// whoever wires this into the real CI pipeline): 32 vCPU / 31 GiB RAM sandbox host, headless
// Chromium via @vitest/browser-playwright.

const FIRST_PAINT_BUDGET_MS = 300
const RERENDER_BUDGET_MS = 150
const POINT_COUNT = 10_000 // vegaPolicy.ts's MAX_ARRAY / budget.ts's MAX_POINTS.path ceiling

function denseLineSpec() {
  const values = Array.from({length: POINT_COUNT}, (_, i) => ({x: i, y: Math.sin(i / 97) * 50 + 50}))
  return {data: {values}, mark: 'line', encoding: {x: {field: 'x', type: 'quantitative'}, y: {field: 'y', type: 'quantitative'}}}
}

it(`draws a ${POINT_COUNT}-point line within the committed ${FIRST_PAINT_BUDGET_MS}ms first-paint budget`, () => {
  const width = 1024, height = 600
  const container = document.createElement('div')
  const theme = readVisualTheme(container)
  const spec = denseLineSpec()

  const start = performance.now()
  const result = renderBlergChart(spec, theme, container, width, height)
  const elapsed = performance.now() - start

  expect(result.svg).toContain('<path')
  const vertices=JSON.parse(container.querySelector('[data-vertex-keys]')!.getAttribute('data-vertex-keys')!)
  expect(vertices.length).toBeLessThanOrEqual(1024)
  expect(container.querySelector('[role="listitem"]')).toHaveAttribute('aria-label',expect.stringContaining('10000 data points'))
  result.dispose()
  expect(elapsed).toBeLessThan(FIRST_PAINT_BUDGET_MS)
}, 15_000)

it(`re-renders the same ${POINT_COUNT}-point line (a theme/resize redraw) within the committed ${RERENDER_BUDGET_MS}ms budget`, () => {
  const width = 1024, height = 600
  const container = document.createElement('div')
  const theme = readVisualTheme(container)
  const spec = denseLineSpec()

  renderBlergChart(spec, theme, container, width, height) // first paint, not timed here
  const start = performance.now()
  const result = renderBlergChart(spec, theme, container, width, height)
  const elapsed = performance.now() - start

  expect(result.svg).toContain('<path')
  const vertices=JSON.parse(container.querySelector('[data-vertex-keys]')!.getAttribute('data-vertex-keys')!)
  expect(vertices.length).toBeLessThanOrEqual(1024)
  expect(container.querySelector('[role="listitem"]')).toHaveAttribute('aria-label',expect.stringContaining('10000 data points'))
  result.dispose()
  expect(elapsed).toBeLessThan(RERENDER_BUDGET_MS)
}, 15_000)
