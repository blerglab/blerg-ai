import { describe, expect, it } from 'vitest'
import { parsePatch } from './patch'
import { createDiffAnchor, diffAnchorContains } from './diffSelection'

const SHA = 'a'.repeat(40)
const PATCH = `commit ${SHA}\n\n    Rename\ndiff --git a/old.ts b/new.ts\nrename from old.ts\nrename to new.ts\n--- a/old.ts\n+++ b/new.ts\n@@ -4,3 +4,3 @@\n same\n-old <img src=x>\n+new <img src=x>\n tail\n@@ -20 +20 @@\n-later\n+LATER\n`

describe('diff line anchors', () => {
  it('keeps side-specific rename paths, complete commit and exact selected quote', () => {
    const patch = parsePatch(PATCH)
    expect(createDiffAnchor(patch, {fileIndex: 1, hunk: 1, side: 'old', line: 4}, 6)).toEqual({
      kind: 'text', quote: 'same\nold <img src=x>\ntail',
      diff: {file: 'old.ts', fileIndex: 1, hunk: 1, side: 'old', start: 4, end: 6, commit: SHA},
    })
    expect(createDiffAnchor(patch, {fileIndex: 1, hunk: 1, side: 'new', line: 5})?.diff?.file).toBe('new.ts')
  })
  it('does not cross hunk boundaries or select absent side lines', () => {
    const patch = parsePatch(PATCH)
    expect(createDiffAnchor(patch, {fileIndex: 1, hunk: 1, side: 'new', line: 4}, 20)).toBeNull()
    expect(createDiffAnchor(patch, {fileIndex: 2, hunk: 1, side: 'old', line: 4})).toBeNull()
    expect(createDiffAnchor(patch, {fileIndex: 1, hunk: 1, side: 'old', line: 0})).toBeNull()
  })
  it('bounds range length and quote size and handles added/deleted files', () => {
    const added = parsePatch('--- /dev/null\n+++ b/a.txt\n@@ -0,0 +1,201 @@\n' + Array.from({length: 201}, (_, i) => '+' + i).join('\n'))
    expect(createDiffAnchor(added, {fileIndex: 1, hunk: 1, side: 'old', line: 1})).toBeNull()
    expect(createDiffAnchor(added, {fileIndex: 1, hunk: 1, side: 'new', line: 1}, 200)?.diff?.end).toBe(200)
    expect(createDiffAnchor(added, {fileIndex: 1, hunk: 1, side: 'new', line: 1}, 201)).toBeNull()
    const deleted = parsePatch('--- a/a.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n')
    expect(createDiffAnchor(deleted, {fileIndex: 1, hunk: 1, side: 'old', line: 1})?.quote).toBe('old')
    expect(createDiffAnchor(deleted, {fileIndex: 1, hunk: 1, side: 'new', line: 1})).toBeNull()
    const huge = parsePatch('--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-a\n+' + 'x'.repeat(32769))
    expect(createDiffAnchor(huge, {fileIndex: 1, hunk: 1, side: 'new', line: 1})).toBeNull()
  })
  it('matches exact file occurrence, hunk, commit, side, path and range', () => {
    const patch = parsePatch(PATCH + PATCH.replace(SHA, 'b'.repeat(40)))
    const anchor = createDiffAnchor(patch, {fileIndex: 1, hunk: 1, side: 'old', line: 5})!
    expect(diffAnchorContains(patch, anchor, {fileIndex: 1, hunk: 1, side: 'old', line: 5})).toBe(true)
    for (const point of [{fileIndex: 2, hunk: 1, side: 'old' as const, line: 5},
      {fileIndex: 1, hunk: 2, side: 'old' as const, line: 20},
      {fileIndex: 1, hunk: 1, side: 'new' as const, line: 5}]) {
      expect(diffAnchorContains(patch, anchor, point)).toBe(false)
    }
    expect(diffAnchorContains(patch, {...anchor, diff: {...anchor.diff!, commit: 'b'.repeat(40)}}, {fileIndex: 1, hunk: 1, side: 'old', line: 5})).toBe(false)
  })
})
