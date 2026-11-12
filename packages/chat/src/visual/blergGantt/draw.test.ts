import { describe, expect, it } from 'vitest'
import { parseGantt } from './parse'
import { layoutGantt, DEFAULT_PX_PER_DAY } from './layout'
import { drawGantt } from './draw'

const THEME = {
  fg: '#f0f3f7', muted: '#bfc7d2', surface: '#171b22', surface2: '#1f242d',
  border: '#313a48', accent: '#ff6a3d', fontFamily: 'sans-serif', fontSize: 14,
  palette: ['#b69cf0'], dark: true,
}

const NOW = new Date('2025-01-09T00:00:00Z')
const SOURCE = 'gantt\n title Release plan\n dateFormat YYYY-MM-DD\n section Design\n Plan :done, a, 2025-01-06, 2d\n Build :crit, active, b, after a, 3d\n Ship :milestone, after b, 0d'

function draw(source: string, fontSize = 14) {
  const graph = parseGantt(source)
  if (!graph) throw new Error('fixture must parse')
  const layout = layoutGantt(graph, DEFAULT_PX_PER_DAY, fontSize, NOW)
  return drawGantt(graph, layout, { ...THEME, fontSize })
}

describe('drawGantt', () => {
  it('draws a valid, non-empty SVG with a positive width and height', () => {
    const out = draw(SOURCE)
    expect(out.svg).toContain('<svg')
    expect(out.width).toBeGreaterThan(0)
    expect(out.height).toBeGreaterThan(0)
  })

  it('tags every task bar and lane with its own data attribute', () => {
    const out = draw(SOURCE)
    expect(out.svg).toContain('data-blerg-gantt-task="a"')
    expect(out.svg).toContain('data-blerg-gantt-task="b"')
    expect(out.svg).toContain('data-blerg-gantt-depends="a"')
    expect(out.svg).toContain('data-blerg-gantt-lane-column')
  })

  it('draws a milestone as a diamond (polygon), not a rect', () => {
    const out = draw(SOURCE)
    const root = new DOMParser().parseFromString(out.svg, 'image/svg+xml').documentElement
    expect(root.querySelectorAll('polygon').length).toBeGreaterThan(0)
  })

  it('draws a dependency line between a task and each of its own `after` dependencies', () => {
    const out = draw(SOURCE)
    expect(out.svg).toContain('data-blerg-gantt-dep-line="a->b"')
  })

  it('draws a today line only when the layout places one', () => {
    const withToday = draw(SOURCE)
    expect(withToday.svg).toContain('data-blerg-gantt-today')
    const graph = parseGantt(SOURCE)!
    const farLayout = layoutGantt(graph, DEFAULT_PX_PER_DAY, 14, new Date('2020-01-01'))
    const noToday = drawGantt(graph, farLayout, THEME)
    expect(noToday.svg).not.toContain('data-blerg-gantt-today')
  })

  it('a bar naming a card key carries a chip attribute and role=button', () => {
    const out = draw('gantt\n section S\n BLG-468 : t1, 2024-01-01, 1d')
    expect(out.svg).toContain('data-blerg-chip-key="card:BLG:468"')
    expect(out.svg).toContain('role="button"')
  })

  it('a plain bar carries no chip attribute', () => {
    const out = draw('gantt\n section S\n Plain task : t1, 2024-01-01, 1d')
    const root = new DOMParser().parseFromString(out.svg, 'image/svg+xml').documentElement
    const group = root.querySelector('[data-blerg-gantt-task="t1"]')!
    expect(group.getAttribute('data-blerg-chip-key')).toBeNull()
    expect(group.getAttribute('role')).toBe('group')
  })

  it('never emits a literal color — every fill/stroke traces back to a theme token', () => {
    const out = draw(SOURCE)
    const literalHex = /#[0-9a-f]{3,8}(?![0-9a-f])/gi
    const used = new Set((out.svg.match(literalHex) ?? []).map(h => h.toLowerCase()))
    const tokens = new Set([THEME.fg, THEME.muted, THEME.surface, THEME.surface2, THEME.border, THEME.accent].map(c => c.toLowerCase()))
    for (const hex of used) expect(tokens.has(hex), `unexpected literal color ${hex}`).toBe(true)
  })

  it('a critical task is outlined with the accent token, thicker than a plain bar', () => {
    const out = draw(SOURCE)
    const root = new DOMParser().parseFromString(out.svg, 'image/svg+xml').documentElement
    const crit = root.querySelector('[data-blerg-gantt-bar-shape="b"]')!
    const plain = root.querySelector('[data-blerg-gantt-bar-shape="a"]')!
    expect(crit.getAttribute('stroke')).toBe(THEME.accent)
    expect(Number(crit.getAttribute('stroke-width'))).toBeGreaterThan(Number(plain.getAttribute('stroke-width')))
  })

  it('scales stroke width up with a bigger fontSize', () => {
    const small = draw(SOURCE, 14)
    const big = draw(SOURCE, 28)
    const strokeOf = (svg: string) => Number(/stroke-width="([\d.]+)"/.exec(svg)![1])
    expect(strokeOf(big.svg)).toBeGreaterThan(strokeOf(small.svg) * 1.5)
  })

  it('a milestone is always filled with the accent token, whether or not it is also crit', () => {
    const out = draw(SOURCE) // "Ship" (task id "task-2") is a plain, non-crit milestone
    const root = new DOMParser().parseFromString(out.svg, 'image/svg+xml').documentElement
    const milestone = root.querySelector('polygon')!
    expect(milestone.getAttribute('fill')).toBe(THEME.accent)
  })

  it('a milestone draws larger than a plain bar\'s own height', () => {
    const out = draw(SOURCE)
    const root = new DOMParser().parseFromString(out.svg, 'image/svg+xml').documentElement
    const milestone = root.querySelector('polygon')!
    const points = milestone.getAttribute('points')!.split(/\s+/).map(pair => pair.split(',').map(Number))
    const milestoneHeight = Math.max(...points.map(p => p[1])) - Math.min(...points.map(p => p[1]))
    const plainBar = root.querySelector('[data-blerg-gantt-bar-shape="a"]')!
    expect(milestoneHeight).toBeGreaterThan(Number(plainBar.getAttribute('height')))
  })

  it('the today label is a full, bold accent label with a background, not the tiny tick-label size', () => {
    const out = draw(SOURCE)
    const root = new DOMParser().parseFromString(out.svg, 'image/svg+xml').documentElement
    const today = root.querySelector('[data-blerg-gantt-today]')!
    const label = today.querySelector('text')!
    expect(Number(label.getAttribute('font-size'))).toBe(14)
    expect(label.getAttribute('font-weight')).toBe('bold')
    expect(label.getAttribute('fill')).toBe(THEME.accent)
    expect(today.querySelector('rect')).not.toBeNull()
  })
})
