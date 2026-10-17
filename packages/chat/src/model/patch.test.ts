import { describe, expect, it } from 'vitest'
import { foldContext, parsePatch, patchSummary, splitRows, patchSummaryText, unquotePath, type PatchLine } from './patch'

const TWO_COMMITS = `commit ${'a'.repeat(40)}
Author: Tester
Date:   2026-10-17T08:00:00-04:00

    Retry the upload

    With a body line.

diff --git a/src/up.go b/src/up.go
index 1111111..2222222 100644
--- a/src/up.go
+++ b/src/up.go
@@ -1,4 +1,5 @@ func upload()
 package up
-func a() {}
+func a() { retry() }
+func retry() {}
 // end
 // tail
\\ No newline at end of file
commit ${'b'.repeat(40)}
Author: Tester
Date:   2026-10-17T09:00:00-04:00

    Add docs

diff --git a/docs/new file.md b/docs/new file.md
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/docs/new file.md\t
@@ -0,0 +1,2 @@
+# Title
+text
diff --git a/img.png b/img.png
index 4444444..5555555 100644
Binary files a/img.png and b/img.png differ (binary, 5 -> 7 bytes)
diff --git a/old.txt b/old.txt
deleted file mode 100644
index 6666666..0000000
--- a/old.txt
+++ /dev/null
@@ -1 +0,0 @@
-gone
diff --git a/a.txt b/b.txt
similarity index 90%
rename from a.txt
rename to b.txt
`

describe('parsePatch', () => {
  it('reads commits, files, statuses and the numbers of both sides', () => {
    const p = parsePatch(TWO_COMMITS)
    expect(p.commits.map(c => c.subject)).toEqual(['Retry the upload', 'Add docs'])
    expect(p.files.map(f => [f.path, f.status, f.commit])).toEqual([
      ['src/up.go', 'modified', 0],
      ['docs/new file.md', 'added', 1],
      ['img.png', 'modified', 1],
      ['old.txt', 'deleted', 1],
      ['b.txt', 'renamed', 1],
    ])
    const up = p.files[0]
    expect(up.adds).toBe(2)
    expect(up.dels).toBe(1)
    const lines = up.hunks[0].lines
    expect(lines[0]).toEqual({ kind: 'ctx', text: 'package up', old: 1, new: 1 })
    expect(lines[1]).toEqual({ kind: 'del', text: 'func a() {}', old: 2 })
    expect(lines[3]).toEqual({ kind: 'add', text: 'func retry() {}', new: 3 })
    expect(lines[lines.length - 1]).toEqual({ kind: 'note', text: 'No newline at end of file' })
    expect(up.hunks[0].header).toBe('@@ -1,4 +1,5 @@ func upload()')
    expect(p.files[2].binary).toContain('(binary, 5 -> 7 bytes)')
    expect(p.files[4].oldPath).toBe('a.txt')
    expect(p.adds).toBe(4)
    expect(p.dels).toBe(2)
  })

  it('reads quoted paths and a plain diff -u patch', () => {
    const q = parsePatch('diff --git "a/t\\303\\251st\\tx" "b/t\\303\\251st\\tx"\n--- "a/t\\303\\251st\\tx"\n+++ "b/t\\303\\251st\\tx"\n@@ -1 +1 @@\n-a\n+b\n')
    expect(q.files[0].path).toBe('tést\tx')
    const u = parsePatch('--- old/f.c\t2026-01-01\n+++ new/f.c\t2026-01-02\n@@ -1 +1 @@\n-x\n+y\n--- old/g.c\n+++ new/g.c\n@@ -1 +1 @@\n-p\n+q\n')
    expect(u.files.map(f => f.path)).toEqual(['new/f.c', 'new/g.c'])
    expect(u.adds).toBe(2)
  })

  it('takes nothing a line pretends to be inside a hunk', () => {
    // A deleted line that reads like a header is still a deletion.
    const p = parsePatch('diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1,2 +1 @@\n--- a/evil\n-diff --git a/y b/y\n+ok\n')
    expect(p.files).toHaveLength(1)
    expect(p.files[0].dels).toBe(2)
    expect(p.files[0].hunks[0].lines.map(l => l.text)).toEqual(['-- a/evil', 'diff --git a/y b/y', 'ok'])
  })

  it('skips a GIT binary patch body', () => {
    const p = parsePatch('diff --git a/b.bin b/b.bin\nindex 1..2\nGIT binary patch\nliteral 5\nMcmZ?\n\nliteral 0\n\ndiff --git a/c b/c\n--- a/c\n+++ b/c\n@@ -1 +1 @@\n-1\n+2\n')
    expect(p.files.map(f => f.path)).toEqual(['b.bin', 'c'])
    expect(p.files[0].binary).toBeTruthy()
    expect(p.files[0].hunks).toHaveLength(0)
  })

  it('is empty for text that is not a patch', () => {
    expect(parsePatch('hello\nworld\n').files).toEqual([])
    expect(parsePatch('').files).toEqual([])
  })
})

describe('patchSummary', () => {
  it('counts each path once and names the first files', () => {
    const s = patchSummary(TWO_COMMITS)
    expect(s).toEqual({ files: 5, adds: 4, dels: 2, names: ['src/up.go', 'docs/new file.md', 'img.png'] })
    expect(patchSummaryText(s)).toBe('5 files · +4 −2')
    expect(patchSummaryText({ files: 1, adds: 0, dels: 3 })).toBe('1 file · +0 −3')
  })
})

describe('unquotePath', () => {
  it('leaves a plain path and decodes octal UTF-8', () => {
    expect(unquotePath('a/b c')).toBe('a/b c')
    expect(unquotePath('"\\346\\227\\245.txt"')).toBe('日.txt')
    expect(unquotePath('"a\\"b\\\\c"')).toBe('a"b\\c')
  })
})

const ctx = (n: number, from = 1): PatchLine[] => Array.from({ length: n }, (_, i) => ({ kind: 'ctx', text: `c${from + i}`, old: from + i, new: from + i }))

describe('splitRows', () => {
  it('pairs each run of deletions with the additions after it', () => {
    const rows = splitRows([
      ...ctx(1),
      { kind: 'del', text: 'a', old: 2 }, { kind: 'del', text: 'b', old: 3 },
      { kind: 'add', text: 'A', new: 2 },
      { kind: 'note', text: 'No newline at end of file' },
    ])
    expect(rows.map(r => [r.left?.text, r.right?.text, r.note])).toEqual([
      ['c1', 'c1', undefined], ['a', 'A', undefined], ['b', undefined, undefined], [undefined, undefined, 'No newline at end of file'],
    ])
  })
})

describe('foldContext', () => {
  it('folds a long run of context between changes, keeping three lines each side', () => {
    const lines: PatchLine[] = [{ kind: 'add', text: 'x', new: 1 }, ...ctx(20, 2), { kind: 'del', text: 'y', old: 22 }]
    const pieces = foldContext(lines)
    expect(pieces.map(p => [p.kind, p.lines.length])).toEqual([['lines', 4], ['fold', 14], ['lines', 4]])
  })

  it('keeps short context and folds the outer side of a hunk edge', () => {
    expect(foldContext([...ctx(3), { kind: 'add', text: 'x', new: 4 }]).map(p => p.kind)).toEqual(['lines'])
    const lead = foldContext([...ctx(12), { kind: 'add', text: 'x', new: 13 }])
    expect(lead.map(p => [p.kind, p.lines.length])).toEqual([['fold', 9], ['lines', 4]])
  })
})
