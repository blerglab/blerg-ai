import { describe, expect, it } from 'vitest'
import { versionPatch } from './versionPatch'
import { focusVersionPatch } from './versionFocus'
import { parsePatch } from './patch'

describe('requested passage comparison', () => {
  it('keeps source line numbers when narrowing a later passage', () => {
    const before = Array.from({ length: 40 }, (_, i) => `line ${i + 1}`).join('\n')
    const patch = versionPatch(before, before.replace('line 30', 'updated 30'), 'notes.md')
    const focused = focusVersionPatch(patch, 'line 30')!
    const hunk = parsePatch(focused).files[0].hunks[0]
    expect(hunk.oldStart).toBe(25)
    expect(hunk.newStart).toBe(25)
    expect(focused).toContain('+updated 30')
    expect(focused).not.toContain('line 1\n')
    expect(focusVersionPatch(patch, 'missing quote')).toBeNull()
  })
})

it('finds a quote selected from rendered Markdown across emphasis and links', () => {
  const before = Array.from({ length: 30 }, (_, i) => i === 20 ? 'Read **the updated** [guide](https://example.com/guide).' : 'line ' + i).join('\n')
  const focused = focusVersionPatch(versionPatch(before, before.replace('updated', 'revised'), 'notes.md'), 'Read the updated guide.')
  expect(focused).not.toBeNull()
  expect(focused).toContain('revised')
  expect(focused).not.toContain('line 0\n')
})
