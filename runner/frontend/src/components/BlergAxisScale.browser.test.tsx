// BLG-467 review, round 2: a chart drawn far past its own declared size (a tab or full screen
// filling it, render.ts's chartSize) must still be *readable* — a left margin sized for the real
// drawn tick count (not a flat default), a tick count capped for a very long axis, and labels and
// strokes that scale up with the draw instead of staying dust-sized on a huge pane.
import { expect, it } from 'vitest'
import { readVisualTheme } from '../../../../packages/chat/src/visual/theme'
import { renderBlergChart } from '../../../../packages/chat/src/visual/blerg/render'
import { MAX_TICKS } from '../../../../packages/chat/src/visual/blerg/axes'

function mount(spec: Record<string, unknown>, width: number, height: number) {
  const container = document.createElement('div')
  document.body.append(container)
  const theme = readVisualTheme(container)
  renderBlergChart(spec, theme, container, width, height)
  const svg = container.querySelector('[data-chart-renderer="blerg"]') as SVGSVGElement
  return { container, svg }
}

const tallBar = {
  data: { values: [{ month: 'Jan', count: 12 }, { month: 'Feb', count: 18 }, { month: 'Mar', count: 9 }] },
  mark: 'bar', encoding: { x: { field: 'month', type: 'nominal' }, y: { field: 'count', type: 'quantitative' } },
}
const tallLine = {
  data: { values: Array.from({ length: 6 }, (_, i) => ({ x: i, y: i * 3 + 1 })) },
  mark: 'line', encoding: { x: { field: 'x', type: 'quantitative' }, y: { field: 'y', type: 'quantitative' } },
}
const tallRule = {
  data: { values: [{ cat: 'A', lo: 1, hi: 9 }, { cat: 'B', lo: 2, hi: 18 }] },
  mark: 'rule', encoding: { x: { field: 'cat', type: 'nominal' }, y: { field: 'lo', type: 'quantitative' }, y2: { field: 'hi' } },
}

for (const [name, spec] of [['bar', tallBar], ['line', tallLine], ['rule', tallRule]] as const) {
  it(`keeps every y-axis tick label inside the SVG when drawn tall (${name})`, () => {
    // A 2000px axis is exactly the shape of the owner's own report: a chart filled to a tall tab.
    const { container, svg } = mount(spec, 300, 2000)
    try {
      const svgBox = svg.getBoundingClientRect()
      const labels = [...svg.querySelectorAll('text[text-anchor="end"]')]
      expect(labels.length).toBeGreaterThan(1)
      for (const label of labels) {
        const box = label.getBoundingClientRect()
        expect(box.left, `"${label.textContent}" left edge`).toBeGreaterThanOrEqual(svgBox.left - 0.5)
      }
    } finally { container.remove() }
  })
}

it('caps the y-axis tick count on a very tall bar instead of drawing dozens of half-step ticks', () => {
  const { container, svg } = mount(tallBar, 300, 2000)
  try {
    const labels = [...svg.querySelectorAll('text[text-anchor="end"]')]
    // d3-array's ticks() can return one or two more/fewer than the exact target to land on a
    // "nice" step; MAX_TICKS + 2 keeps this a real regression guard, not a flaky exact count.
    expect(labels.length).toBeLessThanOrEqual(MAX_TICKS + 2)
  } finally { container.remove() }
})

it('keeps the usual small tick count on an ordinary, un-filled chart (the cap never bites there)', () => {
  const { container, svg } = mount(tallBar, 300, 180)
  try {
    const labels = [...svg.querySelectorAll('text[text-anchor="end"]')]
    expect(labels.length).toBeLessThan(MAX_TICKS)
  } finally { container.remove() }
})

it('caps axis labels and strokes at 1.2x on wide panes while preserving proportional growth below the cap', () => {
  const spec = { ...tallBar, width: 300, height: 180 }
  const natural = mount(spec, 300, 180)
  const filled = mount(spec, 3000, 180) // 10x the declared width — well past the 1.2x cap
  const grown = mount(spec, 330, 180) // 1.1x the declared width — below the label cap
  try {
    const naturalLabel = natural.svg.querySelector('text[text-anchor="end"]')!
    const filledLabel = filled.svg.querySelector('text[text-anchor="end"]')!
    const grownLabel = grown.svg.querySelector('text[text-anchor="end"]')!
    const naturalSize = Number(naturalLabel.getAttribute('font-size'))
    const filledSize = Number(filledLabel.getAttribute('font-size'))
    const grownSize = Number(grownLabel.getAttribute('font-size'))
    expect(naturalSize).toBeGreaterThan(0)
    expect(filledSize / naturalSize).toBeCloseTo(1.2, 5) // BLG-518: wide labels stay quiet, capped rather than 10x
    expect(grownSize / naturalSize).toBeCloseTo(1.1, 5)

    // Stroke width: a shared CSS rule (svg.ts's svgDocument), since no presentation attribute on
    // any individual line carries this today — computed style is where it actually takes effect.
    const naturalDomain = natural.svg.querySelector('line')!
    const filledDomain = filled.svg.querySelector('line')!
    expect(getComputedStyle(naturalDomain).strokeWidth).toBe('1px')
    expect(getComputedStyle(filledDomain).strokeWidth).toBe('1.2px')
  } finally {
    natural.container.remove(); filled.container.remove(); grown.container.remove()
  }
})
