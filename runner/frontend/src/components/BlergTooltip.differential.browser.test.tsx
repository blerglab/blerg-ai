import { expect, it } from 'vitest'
import { compileVegaViewForDifferentialTesting } from '../../../../packages/chat/src/visual/vegaRuntime'
import { readVisualTheme } from '../../../../packages/chat/src/visual/theme'
import { tooltipText } from '../../../../packages/chat/src/visual/blerg/tooltip'
import { drawBar } from '../../../../packages/chat/src/visual/blerg/marks/bar'
import { drawPoint } from '../../../../packages/chat/src/visual/blerg/marks/point'
import { drawLine } from '../../../../packages/chat/src/visual/blerg/marks/line'
import { drawText } from '../../../../packages/chat/src/visual/blerg/marks/text'
import { drawRectHeatmap } from '../../../../packages/chat/src/visual/blerg/marks/rect'
import { drawRule } from '../../../../packages/chat/src/visual/blerg/marks/rule'
import { drawArc } from '../../../../packages/chat/src/visual/blerg/marks/arc'

// BLG-452 design doc §3.10, §3.4: tooltips enter the differential gate like any other feature.
// Compares our own tooltipRegions[].text against Vega's own compiled item.tooltip, run through
// the exact same tooltipText() formatter both renderers share, for one representative spec per
// shipped mark family, across the tooltip channel's single-field, array, and aggregate forms.

interface SceneItem { items?: SceneItem[]; marktype?: string; role?: string; tooltip?: unknown }

async function vegaTooltips(spec: Record<string, unknown>): Promise<string[]> {
  const container = document.createElement('div')
  const theme = readVisualTheme(container)
  const {view} = await compileVegaViewForDifferentialTesting(spec, theme)
  try {
    const root = (view.scenegraph() as unknown as {root: SceneItem}).root
    const texts: string[] = []
    function visit(node: SceneItem) {
      if (node.role === 'mark') for (const item of node.items ?? []) if (item.tooltip !== undefined) texts.push(tooltipText(item.tooltip))
      for (const item of node.items ?? []) visit(item)
    }
    visit(root)
    return texts
  } finally { view.finalize() }
}

const theme = readVisualTheme(document.createElement('div'))

it('bar: matches Vega\'s tooltip text for an array tooltip channel', async () => {
  const values = [{x: 'A', y: 1}, {x: 'B', y: 2}]
  const spec = {data: {values}, mark: 'bar', encoding: {x: {field: 'x', type: 'nominal'}, y: {field: 'y', type: 'quantitative'}, tooltip: [{field: 'x', type: 'nominal'}, {field: 'y', type: 'quantitative'}]}}
  const reference = (await vegaTooltips(spec)).sort()
  const ours = drawBar(values, spec.encoding, {}, 400, 300, theme).tooltipRegions.map(r => r.text).sort()
  expect(ours).toEqual(reference)
})

it('bar: matches Vega\'s tooltip text for an aggregate tooltip field', async () => {
  const values = [{x: 'A', y: 1}, {x: 'A', y: 3}, {x: 'B', y: 5}]
  const spec = {data: {values}, mark: 'bar', encoding: {x: {field: 'x', type: 'nominal'}, y: {field: 'y', type: 'quantitative', aggregate: 'sum'}, tooltip: [{field: 'y', type: 'quantitative', aggregate: 'sum'}]}}
  const reference = (await vegaTooltips(spec)).sort()
  const ours = drawBar(values, spec.encoding, {}, 400, 300, theme).tooltipRegions.map(r => r.text).sort()
  expect(ours).toEqual(reference)
})

it('point: matches Vega\'s tooltip text for a single tooltip field', async () => {
  const values = [{x: 1, y: 2}, {x: 3, y: 4}]
  const spec = {data: {values}, mark: 'point', encoding: {x: {field: 'x', type: 'quantitative'}, y: {field: 'y', type: 'quantitative'}, tooltip: {field: 'y', type: 'quantitative'}}}
  const reference = (await vegaTooltips(spec)).sort()
  const ours = drawPoint('point', values, spec.encoding, {}, 400, 300, theme).tooltipRegions.map(r => r.text).sort()
  expect(ours).toEqual(reference)
})

it('line: matches Vega\'s tooltip text for an array tooltip channel', async () => {
  const values = [{x: 1, y: 2}, {x: 3, y: 4}]
  const spec = {data: {values}, mark: 'line', encoding: {x: {field: 'x', type: 'quantitative'}, y: {field: 'y', type: 'quantitative'}, tooltip: [{field: 'x', type: 'quantitative'}, {field: 'y', type: 'quantitative'}]}}
  const reference = (await vegaTooltips(spec)).sort()
  const ours = drawLine('line', values, spec.encoding, {}, 400, 300, theme).tooltipRegions.map(r => r.text).sort()
  expect(ours).toEqual(reference)
})

it('text: matches Vega\'s tooltip text for an array tooltip channel', async () => {
  const values = [{x: 1, y: 2, label: 'A'}, {x: 3, y: 4, label: 'B'}]
  const spec = {data: {values}, mark: 'text', encoding: {x: {field: 'x', type: 'quantitative'}, y: {field: 'y', type: 'quantitative'}, text: {field: 'label'}, tooltip: [{field: 'label', type: 'nominal'}]}}
  const reference = (await vegaTooltips(spec)).sort()
  const ours = drawText(values, spec.encoding, {}, 400, 300, theme).tooltipRegions.map(r => r.text).sort()
  expect(ours).toEqual(reference)
})

it('rect: matches Vega\'s tooltip text for an array tooltip channel', async () => {
  const values = [{x: 'A', y: '1', v: 3}, {x: 'B', y: '1', v: 7}]
  const spec = {data: {values}, mark: 'rect', encoding: {x: {field: 'x', type: 'ordinal'}, y: {field: 'y', type: 'ordinal'}, color: {field: 'v', type: 'quantitative'}, tooltip: [{field: 'x', type: 'ordinal'}, {field: 'v', type: 'quantitative'}]}}
  const reference = (await vegaTooltips(spec)).sort()
  const ours = drawRectHeatmap(values, spec.encoding, 400, 300, theme).tooltipRegions.map(r => r.text).sort()
  expect(ours).toEqual(reference)
})

it('rule: matches Vega\'s tooltip text for an array tooltip channel', async () => {
  const values = [{team: 'A', lo: 1, hi: 5}, {team: 'B', lo: 2, hi: 8}]
  const spec = {data: {values}, mark: 'rule', encoding: {x: {field: 'team', type: 'nominal'}, y: {field: 'lo', type: 'quantitative'}, y2: {field: 'hi'}, tooltip: [{field: 'team', type: 'nominal'}, {field: 'lo', type: 'quantitative'}, {field: 'hi', type: 'quantitative'}]}}
  const reference = (await vegaTooltips(spec)).sort()
  const ours = drawRule(values, spec.encoding, {}, 400, 300, theme).tooltipRegions.map(r => r.text).sort()
  expect(ours).toEqual(reference)
})

it('arc: matches Vega\'s tooltip text for an array tooltip channel', async () => {
  const values = [{category: 'A', value: 10}, {category: 'B', value: 30}]
  const spec = {data: {values}, mark: 'arc', encoding: {theta: {field: 'value', type: 'quantitative'}, color: {field: 'category', type: 'nominal'}, tooltip: [{field: 'category', type: 'nominal'}, {field: 'value', type: 'quantitative'}]}}
  const reference = (await vegaTooltips(spec)).sort()
  const ours = drawArc(values, spec.encoding, {}, 400, 300, theme).tooltipRegions.map(r => r.text).sort()
  expect(ours).toEqual(reference)
})
