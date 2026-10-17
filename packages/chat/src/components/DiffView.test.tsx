import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import ArtifactViewer from './ArtifactViewer'
import DiffView, { DIFF_FILE_LINES } from './DiffView'
import { testing, type Token } from '../model/highlight'
import { createFakeFiles, type FakeFiles } from '../test/fakeTransport'

const PATCH = `commit ${'c'.repeat(40)}
Author: Tester
Date:   2026-10-17T08:00:00-04:00

    Retry the upload

diff --git a/src/up.go b/src/up.go
index 1111111..2222222 100644
--- a/src/up.go
+++ b/src/up.go
@@ -1,3 +1,4 @@ func upload()
 package up
-func a() {}
+func a() { retry() }
+// <img src=x onerror="window.__pwned=1">
 // end
diff --git a/img.png b/img.png
index 4444444..5555555 100644
Binary files a/img.png and b/img.png differ (binary, 5 -> 7 bytes)
`

function wide(on: boolean) {
  vi.stubGlobal('matchMedia', (q: string) => ({
    matches: on && q.includes('min-width'),
    addEventListener: () => {},
    removeEventListener: () => {},
  }))
}

beforeEach(() => {
  testing.reset(async () => ({ highlightTokens: (_l: string, text: string): Token[] => [{ cls: 'hljs-keyword', kids: [{ text }] }] }))
})

afterEach(() => {
  vi.unstubAllGlobals()
  testing.reset()
})

describe('DiffView', () => {
  it('lists the files with their counts and draws the patch unified, with both line numbers', () => {
    wide(true)
    render(<DiffView text={PATCH} />)
    expect(screen.getByTestId('diff-summary').textContent).toBe('2 files · +2 −1 · 1 commit')
    const list = screen.getByTestId('diff-files')
    expect(within(list).getAllByRole('button').map(b => b.textContent)).toEqual(['Msrc/up.go+2 −1', 'Mimg.png+0 −0'])
    expect(screen.getByTestId('diff-commit').textContent).toContain('Retry the upload')
    const table = screen.getByTestId('diff-table-unified')
    const rows = Array.from(table.querySelectorAll('tr.diff-row'))
    const nums = rows.map(r => Array.from(r.querySelectorAll('.diff-num')).map(td => td.textContent))
    expect(nums).toEqual([['1', '1'], ['2', ''], ['', '2'], ['', '3'], ['3', '4']])
    expect(rows[1].className).toContain('diff-del')
    expect(rows[2].className).toContain('diff-add')
    expect(table.querySelector('.diff-hunk')?.textContent).toBe('@@ -1,3 +1,4 @@ func upload()')
    expect(screen.getByTestId('diff-binary').textContent).toContain('(binary, 5 -> 7 bytes)')
  })

  it('draws the patch as text, never markup', () => {
    wide(false)
    const { container } = render(<DiffView text={PATCH} />)
    expect(container.textContent).toContain('<img src=x onerror="window.__pwned=1">')
    expect(container.querySelector('img')).toBeNull()
    expect((window as unknown as { __pwned?: number }).__pwned).toBeUndefined()
  })

  it('offers side by side on a wide screen and pairs the changed lines', () => {
    wide(true)
    render(<DiffView text={PATCH} />)
    fireEvent.click(screen.getByRole('button', { name: 'Side by side' }))
    const table = screen.getByTestId('diff-table-split')
    const rows = Array.from(table.querySelectorAll('tr.diff-row'))
    expect(rows.map(r => Array.from(r.querySelectorAll('.diff-code')).map(td => td.textContent?.trim()))).toEqual([
      ['package up', 'package up'],
      ['-func a() {}', '+func a() { retry() }'],
      ['', '+// <img src=x onerror="window.__pwned=1">'],
      ['// end', '// end'],
    ])
    fireEvent.click(screen.getByRole('button', { name: 'Unified' }))
    expect(screen.getByTestId('diff-table-unified')).toBeTruthy()
  })

  it('has no side by side on a phone', () => {
    wide(false)
    render(<DiffView text={PATCH} />)
    expect(screen.queryByRole('button', { name: 'Side by side' })).toBeNull()
    expect(screen.getByTestId('diff-table-unified')).toBeTruthy()
    expect(screen.getByTestId('diff-files')).toBeTruthy()
  })

  it('colours the code by the file extension once the highlighter is in', async () => {
    wide(false)
    const { container } = render(<DiffView text={PATCH} />)
    await waitFor(() => expect(container.querySelector('.diff-code .hljs-keyword')).not.toBeNull())
    const add = container.querySelector('tr.diff-add .diff-code .hljs-keyword')
    expect(add?.textContent).toBe('func a() { retry() }')
    // A file with no known language stays plain.
    const plain = '--- a/x.unknownext\n+++ b/x.unknownext\n@@ -1 +1 @@\n-a\n+b\n'
    const { container: c2 } = render(<DiffView text={plain} />)
    await new Promise(r => setTimeout(r, 0))
    expect(c2.querySelector('.hljs-keyword')).toBeNull()
  })

  it('folds long unchanged context and opens it on a click', () => {
    wide(false)
    const ctx = Array.from({ length: 20 }, (_, i) => ` line ${i + 2}`).join('\n')
    const text = `diff --git a/f.txt b/f.txt\n--- a/f.txt\n+++ b/f.txt\n@@ -1,22 +1,22 @@\n-old\n+new\n${ctx}\n-tail\n+TAIL\n`
    render(<DiffView text={text} />)
    const fold = screen.getByRole('button', { name: /14 unchanged lines/ })
    expect(screen.queryByText('line 10')).toBeNull()
    fireEvent.click(fold)
    expect(screen.getByText('line 10')).toBeTruthy()
  })

  it('caps a huge file at DIFF_FILE_LINES lines with Show more', () => {
    wide(false)
    const n = DIFF_FILE_LINES + 50
    const adds = Array.from({ length: n }, (_, i) => `+l${i}`).join('\n')
    const { container } = render(<DiffView text={`diff --git a/big.txt b/big.txt\n--- /dev/null\n+++ b/big.txt\n@@ -0,0 +1,${n} @@\n${adds}\n`} />)
    expect(container.querySelectorAll('tr.diff-row').length).toBe(DIFF_FILE_LINES - 1)
    expect(screen.getByText(/Showing 5000 of 5051 lines/)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Show more' }))
    expect(container.querySelectorAll('tr.diff-row').length).toBe(n)
  }, 30_000)

  it('shows text that is not a patch as text', () => {
    render(<DiffView text={'just words\n'} />)
    expect(screen.getByTestId('artifact-text').textContent).toBe('just words\n')
  })
})

describe('ArtifactViewer: diff', () => {
  let files: FakeFiles
  beforeEach(() => { files = createFakeFiles() })

  it('opens a diff artifact in the diff viewer', async () => {
    wide(true)
    files.serve(PATCH)
    render(<ArtifactViewer sessionId="s1" files={files} onClose={vi.fn()}
      artifact={{ id: 'd1', name: 'retry-the-upload.diff', size: PATCH.length, content_type: 'text/x-diff; charset=utf-8', view: 'diff' }} />)
    expect(await screen.findByTestId('diff-view')).toBeTruthy()
    expect(screen.getByRole('dialog').className).toContain('is-diff')
    expect(document.querySelector('.artifact-meta')?.textContent).toMatch(/^Diff · /)
  })

  it('makes a patch over the size limit download-only', () => {
    render(<ArtifactViewer sessionId="s1" files={files} onClose={vi.fn()}
      artifact={{ id: 'd2', name: 'huge.diff', size: 3 * 1024 * 1024, content_type: 'text/x-diff', view: 'diff' }} />)
    expect(screen.getByTestId('artifact-no-preview').textContent).toContain('Too large to preview')
    expect(files.rawCalls).toEqual([])
  })
})
