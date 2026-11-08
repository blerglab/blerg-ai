import { expect, it } from 'vitest'
import { readVisualTheme } from '../../../../packages/chat/src/visual/theme'
import { renderBlergChart } from '../../../../packages/chat/src/visual/blerg/render'

// A real usability bug found while reviewing BLG-452 screenshots at 390px: a quantitative x-axis
// always labeled 10 ticks regardless of available width, so a narrow chart's tick labels
// overlapped into an illegible smear — a chart this renderer draws must be at least as usable as
// Vega's (the task's own framing), and an unreadable axis fails that even though it's not a
// pixel-geometry mismatch a differential-against-Vega test would catch on its own. First fixed
// with a measureText-based fit, later replaced by axes.ts's densityTickCount (Vega's own ~1-tick-
// per-40px rule, a later review finding) — this guards the regression directly either way: real
// SVG text width (a real browser only — jsdom estimates) proves no two tick labels overlap at a
// narrow width, for a mark family (line) exercised at a known-bad width in the screenshot round.

it('a quantitative x-axis draws no overlapping tick labels at a narrow (390px) width', async () => {
  const width = 390, height = 300
  const container = document.createElement('div')
  const theme = readVisualTheme(container)
  const values = [{t: 1, v: 10}, {t: 2, v: 15}, {t: 3, v: 12}, {t: 4, v: 18}]
  const spec = {data: {values}, mark: 'line', encoding: {x: {field: 't', type: 'quantitative'}, y: {field: 'v', type: 'quantitative'}}}
  renderBlergChart(spec, theme, container, width, height)

  const svg = container.querySelector('[data-chart-renderer="blerg"]')!
  // text-anchor="middle" is specific to drawLinearAxisBottom's own labels (axes.ts); the left
  // axis's labels use text-anchor="end" — this is how the two sets are told apart.
  const labels = [...svg.querySelectorAll('text[text-anchor="middle"]')].filter(el => /^-?\d+(\.\d+)?$/.test(el.textContent ?? ''))
  expect(labels.length).toBeGreaterThan(1)
  const boxes = labels.map(el => el.getBoundingClientRect())
  const sorted = boxes.slice().sort((a, b) => a.left - b.left)
  for (let i = 1; i < sorted.length; i++) {
    expect(sorted[i].left).toBeGreaterThanOrEqual(sorted[i - 1].right)
  }
}, 15_000)
