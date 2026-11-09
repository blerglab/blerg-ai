import { describe, expect, it } from 'vitest'
import { versionPatch } from './versionPatch'
import { parsePatch } from './patch'

describe('version patches', () => {
  it('retains unchanged lines between edits and escapes file names', () => {
    const patch = parsePatch(versionPatch('a\nb\nc\nd\n', 'a\nB\nc\nD\n', 'notes with spaces.md'))
    expect(patch.files).toHaveLength(1)
    expect(patch.files[0].path).toBe('notes with spaces.md')
    expect(patch.files[0].hunks[0].lines.map(l => [l.kind, l.text])).toEqual([
      ['ctx', 'a'], ['del', 'b'], ['add', 'B'], ['ctx', 'c'], ['del', 'd'], ['add', 'D'],
    ])
  })
  it('detects final newline changes and empty files', () => {
    expect(versionPatch('same', 'same\n', 'x')).toContain('\\ No newline at end of file')
    expect(versionPatch('', '', 'x')).toBe('')
    expect(parsePatch(versionPatch('', 'new\n', 'x')).files[0].hunks[0].lines).toEqual([{ kind: 'add', text: 'new', new: 1 }])
  })
})

it('preserves context between separated edits in a long document', () => {
  const before = Array.from({ length: 2000 }, (_, i) => `Paragraph ${i}\n`).join('')
  const after = before.replace('Paragraph 0\n', 'Changed opening\n').replace('Paragraph 1999\n', 'Changed ending\n')
  const lines = parsePatch(versionPatch(before, after, 'long.md')).files[0].hunks[0].lines
  expect(lines.filter(l => l.kind === 'ctx')).toHaveLength(1998)
  expect(lines.filter(l => l.kind === 'add')).toHaveLength(2)
  expect(lines.filter(l => l.kind === 'del')).toHaveLength(2)
})
