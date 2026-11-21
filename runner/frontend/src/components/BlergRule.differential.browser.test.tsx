import { expect, it } from 'vitest'
import { compileVegaViewForDifferentialTesting } from '../../../../packages/chat/src/visual/vegaRuntime'
import { readVisualTheme } from '../../../../packages/chat/src/visual/theme'
import { axisLabels, relativeTo, withinTolerance } from '../../../../packages/chat/src/visual/blerg/differential'
import { renderBlergChart } from '../../../../packages/chat/src/visual/blerg/render'

// BLG-452 design doc §3.10, step 3: rule enters classify.ts's SHIPPED_MARKS only once its corpus
// passes here (the x-band range form — see marks/rule.ts's header for the layered-annotation
// form's deferral). Same margin-tolerance convention as bar's gate (band x scale).

interface SceneItem { items?: SceneItem[]; marktype?: string; role?: string; x?: number; y?: number; x2?: number; y2?: number }

function vegaRules(view: import('vega').View): Array<{x: number; y: number}> {
  const root = (view.scenegraph() as unknown as {root: SceneItem}).root
  const points: Array<{x: number; y: number}> = []
  function visit(node: SceneItem, x: number, y: number) {
    const nx = x + (node.x ?? 0), ny = y + (node.y ?? 0)
    if (node.role === 'mark' && node.marktype === 'rule') {
      // Each rule item encodes both endpoints (x/y and x2/y2), not one item per endpoint.
      for (const item of node.items ?? []) {
        points.push({x: nx + (item.x ?? 0), y: ny + (item.y ?? 0)})
        points.push({x: nx + (item.x2 ?? item.x ?? 0), y: ny + (item.y2 ?? item.y ?? 0)})
      }
    }
    for (const item of node.items ?? []) visit(item, nx, ny)
  }
  visit(root, 0, 0)
  return points
}

const noTitle = {axis: {title: null}}
const spec = {
  data: {values: [{team: 'A', lo: 1, hi: 5}, {team: 'B', lo: 2, hi: 8}, {team: 'C', lo: 0, hi: 3}]},
  mark: 'rule', encoding: {x: {field: 'team', type: 'nominal', ...noTitle}, y: {field: 'lo', type: 'quantitative', ...noTitle}, y2: {field: 'hi'}},
}

const MARGIN_TOLERANCE_PX = 20

it('matches Vega\'s rule geometry within tolerance', async () => {
  const width = 640, height = 360
  const container = document.createElement('div')
  const theme = readVisualTheme(container)

  const {view} = await compileVegaViewForDifferentialTesting({...spec, width, height}, theme)
  try {
    // A rule's bounding box spans both its endpoints (x2/y2, not just x/y), which the shared
    // plotOrigin() helper doesn't know about (it was written for single-anchor marks); derive
    // the origin from the same both-endpoints point list used for the comparison itself instead.
    const vegaRulePoints = vegaRules(view)
    const vegaOrigin = {x: Math.min(...vegaRulePoints.map(p => p.x)), y: Math.min(...vegaRulePoints.map(p => p.y))}
    const reference = vegaRulePoints.map(p => relativeTo(vegaOrigin, p)).sort((a, b) => a.x - b.x || a.y - b.y)

    const ours = document.createElement('div')
    renderBlergChart(spec, theme, ours, width, height)
    const absolute = [...ours.querySelectorAll('line')]
      .filter(el => el.getAttribute('stroke') === theme.accent)
      .flatMap(el => [{x: Number(el.getAttribute('x1')), y: Number(el.getAttribute('y1'))}, {x: Number(el.getAttribute('x2')), y: Number(el.getAttribute('y2'))}])
    const ourOrigin = {x: Math.min(...absolute.map(p => p.x)), y: Math.min(...absolute.map(p => p.y))}
    const ourRects = absolute.map(p => relativeTo(ourOrigin, p)).sort((a, b) => a.x - b.x || a.y - b.y)

    expect(ourRects.length).toBe(reference.length)
    for (let i = 0; i < ourRects.length; i++) {
      expect(withinTolerance(ourRects[i].x, reference[i].x, MARGIN_TOLERANCE_PX)).toBe(true)
      expect(withinTolerance(ourRects[i].y, reference[i].y, MARGIN_TOLERANCE_PX)).toBe(true)
    }
  } finally { view.finalize() }
}, 15_000)

// Review finding: no gate compared actual tick values (BlergBar's own header has the finding).
// rule's own x is a band scale (category ticks, not continuous), so only y applies here.
it('matches Vega\'s y-axis tick values exactly', async () => {
  const width = 640, height = 360
  const container = document.createElement('div')
  const theme = readVisualTheme(container)
  const {view} = await compileVegaViewForDifferentialTesting({...spec, width, height}, theme)
  try {
    const vegaY = axisLabels(view, 'y')
    const ours = document.createElement('div')
    renderBlergChart(spec, theme, ours, width, height)
    const ourY = [...ours.querySelectorAll('text[text-anchor="end"]')].map(el => el.textContent)
    expect(ourY).toEqual(vegaY)
  } finally { view.finalize() }
}, 15_000)
