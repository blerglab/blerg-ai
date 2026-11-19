import { describe, expect, it } from 'vitest'
import { classifyChart, SHIPPED_MARKS } from './classify'
import { renderBlergChart } from './render'
import { readVisualTheme } from '../theme'
import { ALL_DECLARATIONS, generateCases } from './generatedCorpus'

// BLG-452 review follow-up (#2, tightened): a fast (jsdom) smoke pass over the generated corpus
// (generatedCorpus.ts) — proves every case at least classifies 'blerg' and draws without
// throwing, on every CI run, before the slower real-browser differential gate
// (GeneratedCorpus.differential.browser.test.tsx) gets to it. This is NOT the coverage gate by
// itself: per the review, "every generated case is compared against Vega ... or it isn't counted
// as covered" — the differential gate is what actually counts a declared option as covered; this
// file only catches an obviously-broken case (a throw, a classify mismatch) cheaply and early.

const theme = readVisualTheme(document.createElement('div'))

describe('generated corpus smoke pass: every declared mark option classifies and draws', () => {
  const cases = generateCases()
  it('generated at least one case per shipped mark\'s declaration (the generator itself isn\'t empty)', () => {
    const coveredMarks = new Set(cases.map(c => c.mark))
    for (const mark of SHIPPED_MARKS) expect(coveredMarks.has(mark), `no generated case touches "${mark}"`).toBe(true)
  })

  // Review finding (#2): the check above is per *mark*, coarse enough that a generation-loop bug
  // skipping an entire declared channel/type/aggregate/sort combination (as generateCases()
  // itself did before this review round — see its own comments) would never be caught, since
  // some *other* option on the same mark still "touched" it. This one is per declared *option*
  // instead: every channel type, aggregate and sort value in every mark's own declaration — built
  // independently from ALL_DECLARATIONS here, not read back from generateCases()'s own output —
  // must have at least one generated case whose label names it, or the build fails and says
  // exactly which option has none. tooltip is excluded (review finding #4): its cases are
  // structurally the base chart with an extra channel, not a capability of their own — fine to
  // keep differential-testing them, just not counted here.
  it('every declared channel type, aggregate and sort option has at least one generated case', () => {
    const generatedLabels = new Set(cases.map(c => c.label))
    const missing: string[] = []
    for (const declaration of ALL_DECLARATIONS) {
      for (const [channel, channelDeclaration] of Object.entries(declaration.channels)) {
        if (!channelDeclaration || channel === 'tooltip') continue
        for (const type of channelDeclaration.types) {
          const label = `${declaration.mark}.${channel}:${type}`
          if (!generatedLabels.has(label)) missing.push(label)
        }
        if (channelDeclaration.aggregate) {
          for (const aggregate of channelDeclaration.aggregate) {
            const label = `${declaration.mark}.${channel}.aggregate:${aggregate}`
            if (!generatedLabels.has(label)) missing.push(label)
          }
        }
        if (channelDeclaration.sort) {
          for (const variant of ['descending', 'none', 'array']) {
            const label = `${declaration.mark}.${channel}.sort:${variant}`
            if (!generatedLabels.has(label)) missing.push(label)
          }
        }
      }
    }
    expect(missing, 'declared options with zero generated cases').toEqual([])
  })

  it.each(cases.map(c => [c.label, c.spec] as const))('%s', (_label, spec) => {
    expect(classifyChart(spec)).toBe('blerg')
    const container = document.createElement('div')
    expect(() => renderBlergChart(spec, theme, container, 400, 300)).not.toThrow()
  })
})
