import { describe, expect, it } from 'vitest'
import { NUMBER_ONLY, STRING_ENUMS } from './vegaLiteStrings.generated'

// The chart policy refuses a string wherever Vega-Lite's own schema never takes a free string
// (BLG-329): a number-only property, or one that takes only a few fixed words. Those values can be
// written straight into generated expression source. This test derives both lists from the schema
// of the installed vega-lite, so an upgrade that changes them fails here.
// Regenerate with UPDATE_VEGA_LITE_STRINGS=1 npx vitest run src/visual/vegaLiteStrings.test.ts

// node:fs comes in by a dynamic import (the package tsconfig has no node types); paths come from
// vitest's test path, as in harnessText.test.ts.
interface NodeFs { readFileSync(p: string, enc: 'utf8'): string; writeFileSync(p: string, data: string): void }
const nodeModule = (name: string) => import(/* @vite-ignore */ name)
const HERE = expect.getState().testPath ?? ''
const SCHEMA = HERE.replace(/packages\/chat\/src\/visual\/vegaLiteStrings\.test\.ts$/, 'node_modules/vega-lite/build/vega-lite-schema.json')
const GENERATED = HERE.replace(/\.test\.ts$/, '.generated.ts')
const env = (globalThis as {process?: {env: Record<string, string | undefined>}}).process?.env ?? {}

type Node = Record<string, unknown>
const IGNORED = new Set(['#/definitions/ExprRef', '#/definitions/SignalRef']) // refused by key anyway
const FREE = '\u0000free'

function derive(schema: {definitions: Record<string, Node>}) {
  const memo = new Map<string, Set<string>>()
  const strings = (node: unknown, seen: Set<string>): Set<string> => {
    const out = new Set<string>()
    if (!node || typeof node !== 'object') return out
    const n = node as Node
    if (typeof n.$ref === 'string' && !IGNORED.has(n.$ref) && !seen.has(n.$ref)) {
      const name = n.$ref.split('/').pop()!
      if (!memo.has(name)) memo.set(name, strings(schema.definitions[name], new Set([...seen, n.$ref])))
      for (const s of memo.get(name)!) out.add(s)
    }
    for (const key of ['anyOf', 'oneOf', 'allOf']) for (const child of (n[key] as unknown[] | undefined) ?? []) for (const s of strings(child, seen)) out.add(s)
    for (const value of (n.enum as unknown[] | undefined) ?? []) if (typeof value === 'string') out.add(value)
    if (typeof n.const === 'string') out.add(n.const)
    const types = Array.isArray(n.type) ? n.type : [n.type]
    if (types.includes('string') && !('enum' in n) && !('const' in n)) out.add(FREE)
    if (types.includes('array') && n.items) for (const s of strings(n.items, seen)) out.add(s)
    return out
  }
  const byKey = new Map<string, Set<string>>()
  for (const definition of Object.values(schema.definitions)) {
    for (const [key, property] of Object.entries((definition.properties as Record<string, unknown> | undefined) ?? {})) {
      const set = byKey.get(key) ?? new Set<string>()
      for (const s of strings(property, new Set())) set.add(s)
      byKey.set(key, set)
    }
  }
  const numberOnly: string[] = []
  const enums: Record<string, string[]> = {}
  for (const [key, set] of [...byKey].sort(([a], [b]) => a.localeCompare(b))) {
    if (set.has(FREE)) continue
    if (set.size === 0) numberOnly.push(key)
    else enums[key] = [...set].sort()
  }
  return {numberOnly, enums}
}

describe('Vega-Lite string rules', () => {
  it('match the schema of the installed vega-lite', async () => {
    const fs = (await nodeModule('node:fs')) as NodeFs
    const {numberOnly, enums} = derive(JSON.parse(fs.readFileSync(SCHEMA, 'utf8')))
    if (env.UPDATE_VEGA_LITE_STRINGS === '1') {
      fs.writeFileSync(GENERATED, `// Generated from vega-lite's JSON schema by vegaLiteStrings.test.ts; do not edit by hand.
// Properties that never take a free string: number-only ones, and ones that take only these words.
export const NUMBER_ONLY: ReadonlySet<string> = new Set(${JSON.stringify(numberOnly)})
export const STRING_ENUMS: Readonly<Record<string, readonly string[]>> = ${JSON.stringify(enums)}
`)
    }
    expect([...NUMBER_ONLY]).toEqual(numberOnly)
    expect(STRING_ENUMS).toEqual(enums)
  })

  it('cover the properties named in review', () => {
    for (const key of ['bandPosition', 'binSpacing', 'cornerRadius', 'strokeWidth', 'angle', 'radius', 'theta', 'thickness', 'padding', 'paddingInner', 'paddingOuter', 'zero', 'clamp']) {
      expect(NUMBER_ONLY.has(key) || Object.hasOwn(STRING_ENUMS, key), key).toBe(true)
    }
    expect(STRING_ENUMS.nice).toContain('month')
  })
})
