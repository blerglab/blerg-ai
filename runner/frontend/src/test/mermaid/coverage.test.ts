import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import fixtures from './cases.json'

const require = createRequire(import.meta.url)
const bundle = readFileSync(require.resolve('mermaid'), 'utf8')
const manifest = JSON.parse(readFileSync(require.resolve('mermaid/package.json'), 'utf8')) as { version: string }

describe('Mermaid corpus inventory', () => {
  it('covers every installed detector and pins the baseline version', () => {
    expect(manifest.version, 'review fixtures and baselines when upgrading Mermaid').toBe('12.2.0')
    const registered = [...bundle.matchAll(/var id\d* = "([\w-]+)";/g)].map(m => m[1])
    expect(registered.length).toBeGreaterThan(30)
    const covered = new Set(fixtures.map(f => f.diagram))
    expect(registered.filter(id => !covered.has(id)), 'missing diagram families').toEqual([])
    expect([...covered].filter(id => !registered.includes(id) && !['policy', 'invalid'].includes(id)), 'stale diagram names').toEqual([])
  })

  it('gives every case a unique name, provenance, and an explicit expected outcome', () => {
    expect(new Set(fixtures.map(f => f.id)).size).toBe(fixtures.length)
    for (const fixture of fixtures) {
      expect(fixture.source.trim(), fixture.id).not.toBe('')
      expect(fixture.origin, fixture.id).not.toBe('')
      if (fixture.upstreamMissing) {
        expect(fixture.upstreamMissing.reason, fixture.id).not.toBe('')
        expect(fixture.upstreamMissing.label, fixture.id).not.toBe('')
      }
      if (fixture.upstream) {
        expect(fixture.upstream.reason, fixture.id).not.toBe('')
        expect(fixture.upstream.error, fixture.id).not.toBe('')
      }
    }
  })
})
