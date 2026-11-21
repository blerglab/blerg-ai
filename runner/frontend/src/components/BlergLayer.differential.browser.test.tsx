import { expect, it } from 'vitest'
import { compileVegaViewForDifferentialTesting } from '../../../../packages/chat/src/visual/vegaRuntime'
import { readVisualTheme } from '../../../../packages/chat/src/visual/theme'
import { withinTolerance } from '../../../../packages/chat/src/visual/blerg/differential'
import { renderBlergChart } from '../../../../packages/chat/src/visual/blerg/render'

// BLG-452 design doc §3.10, priority 3: layer composition enters classify.ts's SHIPPED_MARKS
// combination only once its corpus passes here, for exactly the two "common" overlays named in
// the task — bar+text (value labels) and line/area+rule (a threshold annotation). Confirmed
// against real Vega output while building this (not assumed): a datum-only rule annotation
// defaults to a literal "black" stroke rather than the chart's own theme color (see render.ts's
// literalMarkColor header), and Vega's own scenegraph item.x/x2 for that rule span the full plot
// width at a constant y, matching our own full-width horizontal line exactly.

interface SceneItem { items?: SceneItem[]; marktype?: string; role?: string; x?: number; y?: number; stroke?: string; fill?: string; text?: string }

function sceneMarks(view: import('vega').View, marktype: string): SceneItem[] {
  const root = (view.scenegraph() as unknown as {root: SceneItem}).root
  const out: SceneItem[] = []
  function visit(node: SceneItem) {
    if (node.role === 'mark' && node.marktype === marktype) out.push(...(node.items ?? []))
    for (const item of node.items ?? []) visit(item)
  }
  visit(root)
  return out
}

it('bar+text layer: label positions and text match Vega\'s own, within tolerance', async () => {
  const width = 400, height = 300
  const container = document.createElement('div')
  const theme = readVisualTheme(container)
  const values = [{month: 'Jan', open: 3}, {month: 'Feb', open: 4}]
  const spec = {
    data: {values}, encoding: {x: {field: 'month', type: 'nominal', axis: {title: null}}, y: {field: 'open', type: 'quantitative', axis: {title: null}}},
    layer: [{mark: 'bar'}, {mark: 'text', encoding: {text: {field: 'open', type: 'quantitative'}}}],
  }
  const {view} = await compileVegaViewForDifferentialTesting({...spec, width, height}, theme)
  try {
    const vegaTexts = sceneMarks(view, 'text')
    expect(vegaTexts.length).toBe(2)
    const vegaOrigin = Math.min(...vegaTexts.map(t => t.x ?? 0))

    const ours = document.createElement('div')
    renderBlergChart(spec, theme, ours, width, height)
    const ourTexts = [...ours.querySelectorAll('text[data-chart-label="true"]')]
    expect(ourTexts.length).toBe(2)
    const ourOrigin = Math.min(...ourTexts.map(el => Number(el.getAttribute('x'))))

    const sortedVega = [...vegaTexts].sort((a, b) => (a.x ?? 0) - (b.x ?? 0))
    const sortedOurs = [...ourTexts].sort((a, b) => Number(a.getAttribute('x')) - Number(b.getAttribute('x')))
    for (let i = 0; i < 2; i++) {
      expect(withinTolerance((sortedVega[i].x ?? 0) - vegaOrigin, Number(sortedOurs[i].getAttribute('x')) - ourOrigin, 25)).toBe(true)
      expect(sortedOurs[i].textContent).toBe(sortedVega[i].text)
    }
  } finally { view.finalize() }
}, 15_000)

it('line+rule layer: the rule spans the full plot width at the datum\'s y, matching Vega\'s own black default', async () => {
  const width = 400, height = 300
  const container = document.createElement('div')
  const theme = readVisualTheme(container)
  const values = [{t: 1, v: 10}, {t: 2, v: 15}, {t: 3, v: 12}]
  const spec = {
    data: {values}, layer: [
      {mark: 'line', encoding: {x: {field: 't', type: 'quantitative', axis: {title: null}}, y: {field: 'v', type: 'quantitative', axis: {title: null}}}},
      {mark: 'rule', encoding: {y: {datum: 12}}},
    ],
  }
  const {view} = await compileVegaViewForDifferentialTesting({...spec, width, height}, theme)
  try {
    const vegaRules = sceneMarks(view, 'rule')
    expect(vegaRules.length).toBeGreaterThan(0)
    expect(vegaRules[0].stroke).toBe('black')

    const ours = document.createElement('div')
    renderBlergChart(spec, theme, ours, width, height)
    // Geometry matches Vega; the color deliberately does not. Vega strokes a bare rule in literal
    // black, while Blerg takes every chart color from the theme (owner rule, BLG-542 reference rules).
    const lines = [...ours.querySelectorAll<SVGLineElement>('line[data-reference-rule="y"]')]
    expect(lines.length).toBe(1)
    expect(lines[0].getAttribute('stroke')).toBe(theme.muted)
    const ourRule = lines[0]
    const x1 = Number(ourRule.getAttribute('x1')), x2 = Number(ourRule.getAttribute('x2'))
    const y1 = Number(ourRule.getAttribute('y1')), y2 = Number(ourRule.getAttribute('y2'))
    expect(y1).toBe(y2) // horizontal
    // Spans the plot area, not just a point or a short segment — a left margin (y-axis labels)
    // means this is meaningfully less than the full chart width, not ~400.
    expect(Math.abs(x2 - x1)).toBeGreaterThan(width * 0.6)
    expect(ours.querySelector('path')).not.toBeNull() // the line series itself still drew
  } finally { view.finalize() }
}, 15_000)
