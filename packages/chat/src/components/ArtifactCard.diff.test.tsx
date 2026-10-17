import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import ArtifactCard from './ArtifactCard'
import { ChatContext, type ChatContextValue } from './context'
import { clearPreviewCache, INLINE_TEXT_MAX } from '../model/artifactPreview'
import { createFakeFiles, type FakeFiles } from '../test/fakeTransport'
import type { ArtifactPayload } from '../types'

let files: FakeFiles
let chat: ChatContextValue

function patchOf(n: number): string {
  return Array.from({ length: n }, (_, i) =>
    `diff --git a/src/f${i}.go b/src/f${i}.go\n--- a/src/f${i}.go\n+++ b/src/f${i}.go\n@@ -1 +1,2 @@\n-a\n+b\n+c\n`).join('')
}

function card(payload: ArtifactPayload) {
  return render(<ChatContext.Provider value={chat}><ArtifactCard payload={payload} /></ChatContext.Provider>)
}

beforeEach(() => {
  clearPreviewCache()
  files = createFakeFiles()
  chat = {
    sessionId: 's1', files, view: vi.fn(), latestVersion: () => undefined, byName: () => undefined,
    submitFeedback: vi.fn(async () => true), reviewFor: vi.fn(async () => null),
  }
})

afterEach(() => vi.restoreAllMocks())

describe('ArtifactCard: a diff', () => {
  it('says how many files and lines changed, names the first files and opens the viewer', async () => {
    files.serve(patchOf(5))
    const payload = { id: 'd1', name: 'retry.diff', size: 400, content_type: 'text/x-diff', view: 'diff' }
    card(payload)
    const counts = await screen.findByTestId('artifact-preview-diff-counts')
    expect(counts.textContent).toBe('5 files · +10 −5')
    const tile = screen.getByTestId('artifact-preview-diff')
    expect(tile.textContent).toContain('src/f0.go')
    expect(tile.textContent).toContain('src/f2.go')
    expect(tile.textContent).not.toContain('src/f3.go')
    expect(tile.textContent).toContain('and 2 more')
    fireEvent.click(tile)
    expect(chat.view).toHaveBeenCalledWith(payload, { autoplay: false })
    expect(screen.getByRole('button', { name: 'View' })).toBeTruthy()
  })

  it('draws the hunks of a small one-file patch under the summary line, and keeps View', async () => {
    files.serve(patchOf(1))
    card({ id: 'd3', name: 'one.diff', size: 200, content_type: 'text/x-diff', view: 'diff' })
    const inline = await screen.findByTestId('artifact-preview-diff-inline')
    expect(inline.querySelector('[data-testid="artifact-preview-diff-counts"]')?.textContent).toBe('1 file · +2 −1')
    const rows = inline.querySelectorAll('tr.diff-row')
    expect(rows).toHaveLength(3)
    expect(inline.querySelectorAll('tr.diff-add')).toHaveLength(2)
    expect(inline.querySelectorAll('tr.diff-del')).toHaveLength(1)
    expect(inline.querySelector('.diff-file-head')).toBeNull()
    expect(screen.getByRole('button', { name: 'View' })).toBeTruthy()
  })

  it('keeps the summary card for several files, or for more than 20 changed lines', async () => {
    files.serve(patchOf(2))
    card({ id: 'd4', name: 'two.diff', size: 400, content_type: 'text/x-diff', view: 'diff' })
    await screen.findByTestId('artifact-preview-diff-counts')
    expect(screen.queryByTestId('artifact-preview-diff-inline')).toBeNull()
  })

  it('keeps the summary card for a one-file patch of 21 changed lines', async () => {
    const adds = Array.from({ length: 21 }, (_, i) => `+line ${i}`).join('\n')
    files.serve(`diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -0,0 +1,21 @@\n${adds}\n`)
    card({ id: 'd5', name: 'big.diff', size: 400, content_type: 'text/x-diff', view: 'diff' })
    await screen.findByTestId('artifact-preview-diff-counts')
    expect(screen.queryByTestId('artifact-preview-diff-inline')).toBeNull()
  })

  it('reads nothing for a patch over the preview size: the plain card', () => {
    card({ id: 'd2', name: 'huge.diff', size: INLINE_TEXT_MAX + 1, content_type: 'text/x-diff', view: 'diff' })
    expect(screen.queryByTestId('artifact-preview-diff')).toBeNull()
    expect(files.rawCalls).toEqual([])
    expect(screen.getByText('huge.diff')).toBeTruthy()
  })
})
