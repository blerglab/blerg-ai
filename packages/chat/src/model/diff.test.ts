import { describe, expect, it } from 'vitest'
import { unifiedDiff, wordDiff } from './diff'

describe('unifiedDiff', () => {
  it('is empty for equal texts', () => {
    expect(unifiedDiff('a\nb\n', 'a\nb\n', 'x.md')).toBe('')
    expect(unifiedDiff('', '', 'x.md')).toBe('')
  })

  it('writes one hunk with three lines of context for a small change', () => {
    const a = ['l1', 'l2', 'l3', 'l4', 'We sampled weekly.', 'l6', 'l7', 'l8', 'l9'].join('\n') + '\n'
    const b = ['l1', 'l2', 'l3', 'l4', 'We sampled weekly for six weeks.', 'l6', 'l7', 'l8', 'l9'].join('\n') + '\n'
    expect(unifiedDiff(a, b, 'report.md')).toBe([
      '--- a/report.md',
      '+++ b/report.md',
      '@@ -2,7 +2,7 @@',
      ' l2',
      ' l3',
      ' l4',
      '-We sampled weekly.',
      '+We sampled weekly for six weeks.',
      ' l6',
      ' l7',
      ' l8',
      '',
    ].join('\n'))
  })

  it('writes two hunks when changes are far apart, and merges them when their context touches', () => {
    const lines = Array.from({ length: 20 }, (_, i) => `l${i + 1}`)
    const far = [...lines]
    far[1] = 'L2'
    far[17] = 'L18'
    const two = unifiedDiff(lines.join('\n') + '\n', far.join('\n') + '\n', 'f')
    expect(two.match(/^@@/gm)).toHaveLength(2)
    expect(two).toContain('@@ -1,5 +1,5 @@\n')
    expect(two).toContain('@@ -15,6 +15,6 @@\n')
    const near = [...lines]
    near[4] = 'L5'
    near[10] = 'L11'
    const one = unifiedDiff(lines.join('\n') + '\n', near.join('\n') + '\n', 'f')
    expect(one.match(/^@@/gm)).toHaveLength(1)
    expect(one).toContain('@@ -2,13 +2,13 @@\n')
  })

  it('handles insertions, deletions, and single-line counts', () => {
    expect(unifiedDiff('a\n', 'a\nb\n', 'f')).toBe('--- a/f\n+++ b/f\n@@ -1 +1,2 @@\n a\n+b\n')
    expect(unifiedDiff('a\nb\n', 'a\n', 'f')).toBe('--- a/f\n+++ b/f\n@@ -1,2 +1 @@\n a\n-b\n')
    expect(unifiedDiff('', 'new\n', 'f')).toBe('--- a/f\n+++ b/f\n@@ -0,0 +1 @@\n+new\n')
    expect(unifiedDiff('old\n', '', 'f')).toBe('--- a/f\n+++ b/f\n@@ -1 +0,0 @@\n-old\n')
  })

  it('marks a missing newline at the end of a file', () => {
    expect(unifiedDiff('a\nb', 'a\nc', 'f')).toBe('--- a/f\n+++ b/f\n@@ -1,2 +1,2 @@\n a\n-b\n\\ No newline at end of file\n+c\n\\ No newline at end of file\n')
    expect(unifiedDiff('a', 'a\n', 'f')).toBe('--- a/f\n+++ b/f\n@@ -1 +1 @@\n-a\n\\ No newline at end of file\n+a\n')
  })
})

describe('wordDiff', () => {
  it('is one same part for equal texts, and nothing for two empty ones', () => {
    expect(wordDiff('a b\nc', 'a b\nc')).toEqual([{ kind: 'same', text: 'a b\nc' }])
    expect(wordDiff('', '')).toEqual([])
  })

  it('marks words inside a changed line, keeping the rest same', () => {
    expect(wordDiff('one\nWe sampled weekly.\nthree\n', 'one\nWe sampled weekly for six weeks.\nthree\n')).toEqual([
      { kind: 'same', text: 'one\nWe sampled ' },
      { kind: 'del', text: 'weekly.' },
      { kind: 'ins', text: 'weekly for six weeks.' },
      { kind: 'same', text: '\nthree\n' },
    ])
  })

  it('marks whole lines that were only added or only removed', () => {
    expect(wordDiff('a\nb\n', 'a\nb\nc\n')).toEqual([{ kind: 'same', text: 'a\nb\n' }, { kind: 'ins', text: 'c\n' }])
    expect(wordDiff('a\nb\nc\n', 'a\nc\n')).toEqual([{ kind: 'same', text: 'a\n' }, { kind: 'del', text: 'b\n' }, { kind: 'same', text: 'c\n' }])
  })

  it('merges adjacent parts of one kind and reassembles both texts', () => {
    const a = 'x y z\nq\n'
    const b = 'x Y z\nQ\n'
    const parts = wordDiff(a, b)
    for (let i = 1; i < parts.length; i++) expect(parts[i].kind).not.toBe(parts[i - 1].kind)
    expect(parts.filter(p => p.kind !== 'ins').map(p => p.text).join('')).toBe(a)
    expect(parts.filter(p => p.kind !== 'del').map(p => p.text).join('')).toBe(b)
  })

  it('replaces everything when nothing is shared', () => {
    expect(wordDiff('alpha', 'beta')).toEqual([{ kind: 'del', text: 'alpha' }, { kind: 'ins', text: 'beta' }])
  })
})
