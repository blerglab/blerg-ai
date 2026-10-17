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

  it('reads nothing for a patch over the preview size: the plain card', () => {
    card({ id: 'd2', name: 'huge.diff', size: INLINE_TEXT_MAX + 1, content_type: 'text/x-diff', view: 'diff' })
    expect(screen.queryByTestId('artifact-preview-diff')).toBeNull()
    expect(files.rawCalls).toEqual([])
    expect(screen.getByText('huge.diff')).toBeTruthy()
  })
})
