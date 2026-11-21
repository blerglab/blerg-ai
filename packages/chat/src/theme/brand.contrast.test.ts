// BLG-472 round 2: every theme in packages/settings/theme.ts must define the full chart series
// (readVisualTheme's token-driven choice only helps if the tokens are actually there), and each
// series color must read against that theme's own chart surface at WCAG's 3:1 non-text minimum —
// not just the one theme (Color-blind light) the browser test exercises.
import { describe, expect, it } from 'vitest'
import { THEMES } from '../../../settings/theme'

interface Fs { readFileSync(path: string, encoding: 'utf8'): string }
const fsName = 'node:fs'
const fs = (await import(/* @vite-ignore */ fsName)) as Fs
const SRC = (expect.getState().testPath ?? '').replace(/\/theme\/brand\.contrast\.test\.ts$/, '/')
const css = fs.readFileSync(`${SRC}theme/brand.css`, 'utf8')

const SERIES = ['tests', 'build', 'git', 'read', 'edit', 'other', 'model', 'waiting'] as const

/** The first block for `selector`, by brace depth (brand.css blocks never nest). */
function block(selector: string): string {
  const start = css.indexOf(selector)
  if (start < 0) throw new Error(`selector not found in brand.css: ${selector}`)
  const open = css.indexOf('{', start)
  let depth = 0
  for (let i = open; i < css.length; i++) {
    if (css[i] === '{') depth++
    else if (css[i] === '}' && --depth === 0) return css.slice(open + 1, i)
  }
  throw new Error(`unterminated block: ${selector}`)
}

/** name -> raw declared value, for simple `--name: value;` declarations in a flat block. */
function declarations(body: string): Map<string, string> {
  const map = new Map<string, string>()
  for (const m of body.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) map.set(m[1]!, m[2]!.trim())
  return map
}

/** Resolves `var(--x)` / `var(--x, fallback)` against the same block's own declarations. */
function resolve(map: Map<string, string>, value: string, seen = new Set<string>()): string {
  const m = /^var\((--[\w-]+)(?:\s*,\s*(.+))?\)$/.exec(value)
  if (!m) return value
  const [, name, fallback] = m
  if (seen.has(name!)) throw new Error(`circular var(): ${name}`)
  const declared = map.get(name!)
  if (declared === undefined) {
    if (fallback !== undefined) return resolve(map, fallback, seen)
    throw new Error(`undefined custom property: ${name}`)
  }
  return resolve(map, declared, new Set(seen).add(name!))
}

function lin(c: number): number { const v = c / 255; return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4 }
function luminance(hex: string): number {
  const h = hex.replace('#', '')
  const [r, g, b] = [0, 2, 4].map(i => parseInt(h.slice(i, i + 2), 16))
  return 0.2126 * lin(r!) + 0.7152 * lin(g!) + 0.0722 * lin(b!)
}
function contrast(a: string, b: string): number {
  const [l1, l2] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return (l1! + 0.05) / (l2! + 0.05)
}

// One CSS block per THEMES entry (packages/settings/theme.ts): "system" has no data-theme
// attribute, so the OS can land it on either of the two unlabeled blocks below. Every id past
// high-contrast is a bundled scheme (BLG-474, scripts/themeimport) whose block is the same plain
// `:root[data-theme="<id>"] {` shape, so those are derived from THEMES directly rather than
// hand-listed — a theme added or dropped there needs no edit here.
const HAND_WRITTEN_BLOCKS: Record<string, string[]> = {
  system: [':root {', ':root:not([data-theme]) {'],
  dark: [':root[data-theme="dark"] {'],
  light: [':root[data-theme="light"] {'],
  'colorblind-dark': [':root[data-theme="colorblind-dark"] {'],
  'colorblind-light': [':root[data-theme="colorblind-light"] {'],
  'high-contrast': [':root[data-theme="high-contrast"] {'],
}
const THEME_BLOCKS: Record<string, string[]> = Object.fromEntries(THEMES.map(({ id }) =>
  [id, HAND_WRITTEN_BLOCKS[id] ?? [`:root[data-theme="${id}"] {`]]))

it('covers every theme packages/settings/theme.ts lists', () => {
  expect(Object.keys(THEME_BLOCKS).sort()).toEqual(THEMES.map(t => t.id).sort())
})

describe.each(THEMES.map(t => t.id))('%s', id => {
  const selectors = THEME_BLOCKS[id]!
  it.each(selectors)('defines the full chart series, readable against every surface it can be shown on (selector %s)', selector => {
    const map = declarations(block(selector))
    // BLG-474 review: a chart mark can be drawn against the app background or a card surface too,
    // not only --scree — check all three, not just the one the browser test happens to exercise.
    const surfaces = ['--basalt', '--scree', '--scree-2'].map(name => {
      const resolved = resolve(map, map.get(name)!)
      expect(resolved, `${id}: ${name}`).toMatch(/^#[0-9a-f]{6}$/i)
      return resolved
    })
    for (const name of SERIES) {
      const raw = map.get(`--chart-${name}`)
      expect(raw, `${id}: --chart-${name} is not defined`).toBeDefined()
      const color = resolve(map, raw!)
      expect(color, `${id}: --chart-${name} resolves to ${color}, not a hex color`).toMatch(/^#[0-9a-f]{6}$/i)
      for (const surface of surfaces) {
        expect(contrast(color, surface), `${id}: --chart-${name} ${color} on ${surface}`).toBeGreaterThanOrEqual(3)
      }
    }
  })
})
