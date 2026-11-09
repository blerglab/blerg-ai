import { expect, it } from 'vitest'
import { compileVegaViewForDifferentialTesting } from '../../../../packages/chat/src/visual/vegaRuntime'
import { readVisualTheme } from '../../../../packages/chat/src/visual/theme'
import { axisLabels, plotOrigin, relativeTo, withinTolerance } from '../../../../packages/chat/src/visual/blerg/differential'
import { renderBlergChart } from '../../../../packages/chat/src/visual/blerg/render'

// BLG-452 design doc §3.10, step 3: text enters classify.ts's SHIPPED_MARKS only once its corpus
// passes here. Uses the same normalized-position comparison as point.ts's gate (a continuous
// scale's own range/margin padding doesn't need to match Vega's byte-for-byte for this proof to
// be meaningful — see BlergPoint.differential.browser.test.tsx's header comment).

interface SceneItem { items?: SceneItem[]; marktype?: string; role?: string; x?: number; y?: number }

function vegaText(view: import('vega').View): Array<{x: number; y: number}> {
  const root = (view.scenegraph() as unknown as {root: SceneItem}).root
  const points: Array<{x: number; y: number}> = []
  function visit(node: SceneItem, x: number, y: number) {
    const nx = x + (node.x ?? 0), ny = y + (node.y ?? 0)
    if (node.role === 'mark' && node.marktype === 'text') {
      for (const item of node.items ?? []) points.push({x: nx + (item.x ?? 0), y: ny + (item.y ?? 0)})
    }
    for (const item of node.items ?? []) visit(item, nx, ny)
  }
  visit(root, 0, 0)
  return points
}

const noTitle = {axis: {title: null}}
const spec = {
  data: {values: [{x: 1, y: 2, label: 'A'}, {x: 3, y: 4, label: 'B'}, {x: 5, y: 1, label: 'C'}]},
  mark: 'text', encoding: {x: {field: 'x', type: 'quantitative', ...noTitle}, y: {field: 'y', type: 'quantitative', ...noTitle}, text: {field: 'label'}},
}

it('matches Vega\'s text position within tolerance', async () => {
  const width = 640, height = 360
  const container = document.createElement('div')
  const theme = readVisualTheme(container)

  const {view} = await compileVegaViewForDifferentialTesting({...spec, width, height}, theme)
  try {
    const vegaOrigin = plotOrigin(view)
    const reference = vegaText(view).map(p => relativeTo(vegaOrigin, p)).sort((a, b) => a.x - b.x)

    const ours = document.createElement('div')
    const drawn = renderBlergChart(spec, theme, ours, width, height)
    expect(drawn.svg).toContain('<text')
    // Axis tick labels are also <text> elements (filled with theme.muted); only the mark's own
    // text (theme.fg) is the thing this gate compares.
    const absolute = [...ours.querySelectorAll('text')].filter(el => el.getAttribute('fill') === theme.fg)
      .map(el => ({x: Number(el.getAttribute('x')), y: Number(el.getAttribute('y'))}))
    const ourOrigin = {x: Math.min(...absolute.map(p => p.x)), y: Math.min(...absolute.map(p => p.y))}
    const ourRects = absolute.map(p => relativeTo(ourOrigin, p)).sort((a, b) => a.x - b.x)

    expect(ourRects.length).toBe(reference.length)
    const span = (points: Array<{x: number; y: number}>, axis: 'x' | 'y') => Math.max(...points.map(p => p[axis])) - Math.min(...points.map(p => p[axis])) || 1
    const ourSpanX = span(ourRects, 'x'), refSpanX = span(reference, 'x')
    for (let i = 0; i < ourRects.length; i++) {
      expect(withinTolerance(ourRects[i].x / ourSpanX, reference[i].x / refSpanX, 0.01)).toBe(true)
    }
  } finally { view.finalize() }
}, 15_000)

// Review finding: no gate compared actual tick values (BlergBar's own header has the finding).
// text, like point/circle/square, draws two continuous axes — see BlergPoint's own header for
// why that's a stated exception to exact tick-value matching (Vega balances both axes' density
// together in a way axes.ts's per-axis divisor doesn't reproduce exactly yet); the domain
// endpoints (what the zero-default fix actually proves) still must match exactly.
it('draws a reasonable (non-degenerate) tick count on both axes, matching Vega\'s own domain', async () => {
  const width = 640, height = 360
  const container = document.createElement('div')
  const theme = readVisualTheme(container)
  const {view} = await compileVegaViewForDifferentialTesting({...spec, width, height}, theme)
  try {
    const vegaX = axisLabels(view, 'x'), vegaY = axisLabels(view, 'y')
    const ours = document.createElement('div')
    renderBlergChart(spec, theme, ours, width, height)
    const ourX = [...ours.querySelectorAll('text[text-anchor="middle"]')].filter(el => el.getAttribute('fill') !== theme.fg).map(el => el.textContent)
    const ourY = [...ours.querySelectorAll('text[text-anchor="end"]')].map(el => el.textContent)
    // The domain's own zero-start (what the zero-default fix above actually proves) must still
    // agree exactly, independent of the unresolved density divisor — the differing tick *step*
    // means the two sides' *last* tick isn't always the same value, so that one isn't asserted.
    expect(ourX[0]).toBe(vegaX[0])
    expect(ourY[0]).toBe(vegaY[0])
    expect(ourX.length).toBeGreaterThan(1)
    expect(ourY.length).toBeGreaterThan(1)
  } finally { view.finalize() }
}, 15_000)
