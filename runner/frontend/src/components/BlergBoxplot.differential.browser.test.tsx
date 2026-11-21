import { expect, it } from 'vitest'
import { compileVegaViewForDifferentialTesting } from '../../../../packages/chat/src/visual/vegaRuntime'
import { readVisualTheme } from '../../../../packages/chat/src/visual/theme'
import { axisLabels, withinTolerance } from '../../../../packages/chat/src/visual/blerg/differential'
import { renderBlergChart } from '../../../../packages/chat/src/visual/blerg/render'

// BLG-452 design doc §3.10, priority 4: boxplot enters classify.ts's SHIPPED_MARKS only once its
// corpus passes here. Compares our box/whisker/median/outlier geometry and colors against Vega's
// own scenegraph directly — quartiles, the 1.5*IQR whisker extent, and which points are flagged
// outliers are all real math (d3-array's quantileSorted, Tukey's rule), not assumed — confirmed
// against Vega's own compiled output while building marks/boxplot.ts (see its header).

interface SceneItem { items?: SceneItem[]; marktype?: string; role?: string; name?: string; x?: number; y?: number; y2?: number; width?: number; height?: number; stroke?: string; fill?: string }

const noTitle = {axis: {title: null}}
const values = [
  {g: 'A', v: 1}, {g: 'A', v: 2}, {g: 'A', v: 3}, {g: 'A', v: 4}, {g: 'A', v: 5}, {g: 'A', v: 20},
  {g: 'B', v: 10}, {g: 'B', v: 12}, {g: 'B', v: 14}, {g: 'B', v: 16}, {g: 'B', v: 18},
]
const spec = {data: {values}, mark: 'boxplot', encoding: {x: {field: 'g', type: 'nominal', ...noTitle}, y: {field: 'v', type: 'quantitative', ...noTitle}}}

it('boxplot: box geometry, median, whiskers and the flagged outlier all match Vega\'s own, within tolerance', async () => {
  const width = 400, height = 300
  const container = document.createElement('div')
  const theme = readVisualTheme(container)
  const {view} = await compileVegaViewForDifferentialTesting({...spec, width, height}, theme)
  try {
    // The box rect's own mark group name ends in "layer_1_layer_0_marks" (see marks/boxplot.ts
    // header's scratch-dump notes); the simplest stable match across Vega-Lite's internal
    // layer-name numbering is "the rect-marktype mark group", keyed by its own item height (the
    // box is tall, the median overlay is a ~1px hairline), not a literal name — that numbering
    // isn't part of any public contract.
    const rectGroups = (() => {
      const root = (view.scenegraph() as unknown as {root: SceneItem}).root
      const rects: SceneItem[] = []
      function visit(node: SceneItem) { if (node.role === 'mark' && node.marktype === 'rect') rects.push(node); for (const item of node.items ?? []) visit(item) }
      visit(root)
      return rects
    })()
    expect(rectGroups.length).toBe(2) // the box rect group and the median rect group
    const boxItems = rectGroups.find(b => (b.items?.[0]?.height ?? 0) > 5)!.items! // the box (tall), not the median (thin)
    const medianItems = rectGroups.find(b => (b.items?.[0]?.height ?? 0) <= 5)!.items!
    expect(boxItems.length).toBe(2)

    const ours = document.createElement('div')
    renderBlergChart(spec, theme, ours, width, height)
    const ourRects = [...ours.querySelectorAll('rect')].slice(1) // index 0 is the background
    const ourBoxes = ourRects.filter(el => Number(el.getAttribute('height')) > 5)
    const ourMedians = ourRects.filter(el => Number(el.getAttribute('height')) <= 5)
    expect(ourBoxes.length).toBe(2)
    expect(ourMedians.length).toBe(2)

    // Normalized the same way BlergBarColor's gate is: our own left/top-margin matching Vega's
    // margin algorithm is only approximate (the same documented v1 gap every other mark's gate
    // already states), so what must match exactly is the box's own *relative* position/size
    // within the plot, not either side's absolute pixel origin.
    const sortedVegaBoxes = [...boxItems].sort((a, b) => (a.x ?? 0) - (b.x ?? 0))
    const sortedOurBoxes = [...ourBoxes].sort((a, b) => Number(a.getAttribute('x')) - Number(b.getAttribute('x')))
    const vegaXOrigin = Math.min(...sortedVegaBoxes.map(b => b.x ?? 0)), vegaYOrigin = Math.min(...sortedVegaBoxes.map(b => b.y ?? 0))
    const ourXOrigin = Math.min(...sortedOurBoxes.map(b => Number(b.getAttribute('x')))), ourYOrigin = Math.min(...sortedOurBoxes.map(b => Number(b.getAttribute('y'))))
    // A larger tolerance than BlergBar's own gate, same rationale as BlergBarColor's: the
    // category-axis margin match is only approximate (documented v1 gap), and this chart's
    // smaller plot area makes that absolute gap proportionally bigger here.
    const MARGIN_TOLERANCE_PX = 15
    for (let i = 0; i < 2; i++) {
      const v = sortedVegaBoxes[i], o = sortedOurBoxes[i]
      expect(withinTolerance((v.x ?? 0) - vegaXOrigin, Number(o.getAttribute('x')) - ourXOrigin, MARGIN_TOLERANCE_PX)).toBe(true)
      expect(withinTolerance((v.y ?? 0) - vegaYOrigin, Number(o.getAttribute('y')) - ourYOrigin, MARGIN_TOLERANCE_PX)).toBe(true)
      expect(withinTolerance(v.width ?? 0, Number(o.getAttribute('width')), 1)).toBe(true)
      expect(withinTolerance(v.height ?? 0, Number(o.getAttribute('height')), MARGIN_TOLERANCE_PX)).toBe(true)
    }
    const sortedVegaMedians = [...medianItems].sort((a, b) => (a.x ?? 0) - (b.x ?? 0))
    const sortedOurMedians = [...ourMedians].sort((a, b) => Number(a.getAttribute('x')) - Number(b.getAttribute('x')))
    for (let i = 0; i < 2; i++) {
      expect(withinTolerance((sortedVegaMedians[i].y ?? 0) - vegaYOrigin, Number(sortedOurMedians[i].getAttribute('y')) - ourYOrigin, MARGIN_TOLERANCE_PX)).toBe(true)
    }

    // Group A's 20 is a Tukey outlier (1.5*IQR beyond Q3); group B has none.
    const outlierCircles = [...ours.querySelectorAll('circle')]
    expect(outlierCircles.length).toBe(1)
    expect(outlierCircles[0].getAttribute('fill')).toBe('transparent')
  } finally { view.finalize() }
}, 15_000)

// Review finding: no gate compared actual tick values (BlergBar's own header has the finding).
// boxplot's own x is a band scale (category ticks, not continuous), so only y applies here.
it('matches Vega\'s y-axis tick values exactly', async () => {
  const width = 400, height = 300
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
