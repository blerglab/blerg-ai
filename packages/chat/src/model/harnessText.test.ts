import { describe, expect, it } from 'vitest'
import { HARNESS_MARKERS, isHarnessText } from './harnessText'
import { userMessageShape } from './userMessages'

// The Go reducer (runner/internal/topics) embeds this fixture; the chat must read the same markers, or a
// message hidden here would open a topic there (or the other way round).
// node:fs comes in by a dynamic import (the package tsconfig has no node types), and the path comes from
// vitest: import.meta.url is Vite's id here, not a file URL (tokens.test.ts does the same).
interface NodeFs { readFileSync(p: string, enc: 'utf8'): string }
const nodeModule = (name: string) => import(/* @vite-ignore */ name)
const FIXTURE = (expect.getState().testPath ?? '')
  .replace(/packages\/chat\/src\/model\/harnessText\.test\.ts$/, 'runner/internal/topics/testdata/harness_markers.json')

describe('harness markers', () => {
  it('are the ones the Go reducer reads', async () => {
    const { readFileSync } = (await nodeModule('node:fs')) as NodeFs
    const fixture = JSON.parse(readFileSync(FIXTURE, 'utf8')) as { prefixes: string[]; restart_note: string }
    expect([...HARNESS_MARKERS]).toEqual(fixture.prefixes)
    for (const p of fixture.prefixes) expect(isHarnessText(`  ${p} x`)).toBe(true)
    expect(userMessageShape({ text: `${fixture.restart_note}. Check your work.`, source: 'chat' }).as).toBe('notice')
  })
})
