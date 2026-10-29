import { expect, it } from 'vitest'

// node:fs comes in by a dynamic import (the package tsconfig has no node types; ?raw CSS imports are
// empty under this test setup), and the directory from vitest, as harnessText.test.ts does.
interface NodeFs { readFileSync(p: string, enc: 'utf8'): string; readdirSync(p: string): string[] }
const nodeModule = (name: string) => import(/* @vite-ignore */ name)
const DIR = (expect.getState().testPath ?? '').replace(/\/cssKeyframes\.test\.ts$/, '')

// An app that embeds the chat (the board) loads only the chat's own CSS: every animation the chat
// names must be defined there, or it silently does not run (the start card's spinner did not spin).
// Each shorthand keyword fills one slot once; a keyword whose slot is already taken is the name
// (`animation: ease 1s ease` names an animation "ease"). CSS-wide keywords are never names.
const SLOTS: Record<string, string> = {
  linear: 'timing', ease: 'timing', 'ease-in': 'timing', 'ease-out': 'timing', 'ease-in-out': 'timing',
  'step-start': 'timing', 'step-end': 'timing', infinite: 'count',
  normal: 'direction', reverse: 'direction', alternate: 'direction', 'alternate-reverse': 'direction',
  none: 'fill', forwards: 'fill', backwards: 'fill', both: 'fill', running: 'state', paused: 'state',
}
const WIDE = new Set(['initial', 'inherit', 'unset', 'revert', 'revert-layer'])

/** Split on top-level commas: never inside a function's parentheses or a string. */
function splitTop(value: string): string[] {
  const out: string[] = []; let depth = 0, quote = '', cur = ''
  for (const ch of value) {
    if (quote) { cur += ch; if (ch === quote) quote = ''; continue }
    if (ch === '"' || ch === "'") { quote = ch; cur += ch; continue }
    if (ch === '(') depth++
    if (ch === ')') depth--
    if (ch === ',' && depth === 0) { out.push(cur); cur = ''; continue }
    cur += ch
  }
  return [...out, cur].map(s => s.trim()).filter(Boolean)
}

/** The tokens of one animation: a function call or a string stays one token. */
function tokens(value: string): string[] {
  return value.match(/[\w-]+\([^()]*(?:\([^()]*\)[^()]*)*\)|"[^"]*"|'[^']*'|[^\s]+/g) ?? []
}

/** The keyframe names an animation / animation-name declaration refers to. */
export function animationNames(property: string, value: string): string[] {
  const names: string[] = []
  const clean = value.replace(/!\s*important\s*$/i, '').trim()
  if (WIDE.has(clean)) return names
  for (const part of splitTop(clean)) {
    const used = new Set<string>()
    for (const token of tokens(part)) {
      if (/^["']/.test(token)) { names.push(token.slice(1, -1)); continue }
      if (/^[\w-]+\(/.test(token)) { if (/^(steps|cubic-bezier|linear)\(/.test(token)) used.add('timing'); continue }
      if (property === 'animation-name') { if (token !== 'none' && !WIDE.has(token)) names.push(token); continue }
      if (/^-?[\d.]+$/.test(token)) { used.add('count'); continue }
      if (/^-?[\d.]+m?s$/.test(token) || WIDE.has(token)) continue
      const slot = SLOTS[token]
      if (slot && !used.has(slot)) { used.add(slot); continue }
      if (token !== 'none') names.push(token)
    }
  }
  return names
}

/** The names @keyframes rules define, quoted or not. */
export function keyframeNames(css: string): string[] {
  return [...css.matchAll(/@keyframes\s+(?:"([^"]+)"|'([^']+)'|([\w-]+))/g)].map(m => m[1] ?? m[2] ?? m[3])
}

it('reads animation names the way CSS does', () => {
  expect(animationNames('animation', 'spin 1s steps(4, jump-none) infinite alternate-reverse')).toEqual(['spin'])
  expect(animationNames('animation', '"quoted" 1s, other 2s ease-in-out initial')).toEqual(['quoted', 'other'])
  expect(animationNames('animation-name', 'ease, none')).toEqual(['ease'])
  expect(animationNames('animation', 'var(--x, missing) 1s')).toEqual([])
  expect(animationNames('animation-name', 'initial')).toEqual([])
  expect(animationNames('animation', 'inherit')).toEqual([])
  expect(animationNames('animation', 'ease 1s ease')).toEqual(['ease'])
  expect(animationNames('animation', 'spin 1s linear infinite !important')).toEqual(['spin'])
  expect(animationNames('animation', '1s 2 infinite')).toEqual(['infinite'])
  expect(animationNames('animation', '1s linear(0, 1) ease')).toEqual(['ease'])
  expect(keyframeNames('@keyframes "quoted" {} @keyframes plain {}')).toEqual(['quoted', 'plain'])
})

it('defines every animation its CSS uses', async () => {
  const { readFileSync, readdirSync } = (await nodeModule('node:fs')) as NodeFs
  const files = readdirSync(DIR).filter(f => f.endsWith('.css'))
  expect(files.length).toBeGreaterThan(5)
  const css = files.map(f => readFileSync(`${DIR}/${f}`, 'utf8')).join('\n')
  const defined = new Set(keyframeNames(css))
  const used = new Set<string>()
  for (const m of css.matchAll(/(?<![\w-])(animation|animation-name)\s*:\s*([^;}]+)/g)) {
    for (const name of animationNames(m[1], m[2])) used.add(name)
  }
  expect(used.size).toBeGreaterThan(5)
  expect([...used].filter(name => !defined.has(name))).toEqual([])
})
