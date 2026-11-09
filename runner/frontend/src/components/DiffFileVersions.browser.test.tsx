import { it, expect, vi, describe } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { page } from 'vitest/browser'
import '@blerglab/chat/blerg.css'
import '../index.css'
import { DiffView, type TransportFiles } from '@blerglab/chat'

const sha = 'abcdef0123456789abcdef0123456789abcdef01'
const patch = `commit ${sha}\n\ndiff --git a/old.txt b/new.txt\nsimilarity index 80%\nrename from old.txt\nrename to new.txt\n--- a/old.txt\n+++ b/new.txt\n@@ -1 +1 @@\n-old\n+new\n`
for (const width of [390, 3840]) describe(`Diff files at ${width}`, () => {
it('opens full changed and original blobs at the named commit through the file viewer', async () => {
  await page.viewport(width, 900)
  const commitFile = vi.fn(async (_s, _sha, path) => ({ path, size: 30, view: 'text', content_type: 'text/plain', mtime: '', blob: new Blob(['complete file including context']) }))
  const files = { commitFile } as unknown as TransportFiles
  render(<DiffView text={patch} fileContext={{ sessionId: 'session', files }} />)
  fireEvent.click(screen.getByRole('button', { name: 'Open changed' }))
  await waitFor(() => expect(screen.getByRole('dialog').textContent).toContain('complete file including context'))
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  expect(commitFile).toHaveBeenCalledWith('session', sha, 'new.txt', 'changed', expect.any(AbortSignal), undefined)
  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
  fireEvent.click(screen.getByRole('button', { name: 'Open original' }))
  await waitFor(() => expect(commitFile).toHaveBeenCalledWith('session', sha, 'old.txt', 'original', expect.any(AbortSignal), undefined))
})
it('explains absent sides for added and deleted files', () => {
  const added = `commit ${sha}\n\ndiff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+new\n`
  const deleted = 'diff --git a/old.txt b/old.txt\ndeleted file mode 100644\n--- a/old.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n'
  render(<DiffView text={added + deleted} fileContext={{ sessionId: 'session', files: { commitFile: vi.fn() } as unknown as TransportFiles }} />)
  expect(screen.getByRole('button', { name: 'No original (file added)' })).toBeDisabled()
  expect(screen.getByRole('button', { name: 'No changed (file deleted)' })).toBeDisabled()
})

})
