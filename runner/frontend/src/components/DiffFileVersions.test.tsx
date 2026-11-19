import { it, expect, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { DiffView, CommitViewer, ArtifactViewer, ChatContext, type ChatContextValue, type TransportFiles } from '@blerglab/chat'

const sha = 'abcdef0123456789abcdef0123456789abcdef01'
const patch = `commit ${sha}\n\ndiff --git a/old.txt b/new.txt\nsimilarity index 80%\nrename from old.txt\nrename to new.txt\n--- a/old.txt\n+++ b/new.txt\n@@ -1 +1 @@\n-old\n+new\n`
it('opens full changed and original blobs at the named commit through the file viewer', async () => {
  const commitFile = vi.fn(async (_s, _sha, path) => ({ path, size: 30, view: 'text', content_type: 'text/plain', mtime: '', blob: new Blob(['complete file including context']) }))
  const files = { commitFile } as unknown as TransportFiles
  render(<DiffView text={patch} fileContext={{ sessionId: 'session', files }} />)
  fireEvent.click(screen.getByRole('button', { name: 'Open changed' }))
  await waitFor(() => expect(screen.getByRole('dialog').textContent).toContain('complete file including context'))
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
it('keeps an explicit repository member on the full-file read', async () => {
  const member = { member_id: 'second', launch_generation: 'launch', generation: 2 }
  const commitFile = vi.fn(async (_s, _sha, path) => ({ path, size: 5, view: 'text', content_type: 'text/plain', mtime: '', blob: new Blob(['whole']) }))
  const files = { commitFile, commitRepositories: async () => [{ name: 'Second repository', ref: member }] } as unknown as TransportFiles
  render(<DiffView text={patch} fileContext={{ sessionId: 'session', files }} />)
  const picker = await screen.findByLabelText('File repository')
  expect(screen.getByRole('button', { name: 'Open changed' })).toBeDisabled()
  fireEvent.change(picker, { target: { value: 'second' } })
  fireEvent.click(screen.getByRole('button', { name: 'Open changed' }))
  await waitFor(() => expect(commitFile).toHaveBeenCalledWith('session', sha, 'new.txt', 'changed', expect.any(AbortSignal), member))
})

it('offers full-file actions from a posted diff in the existing artifact viewer', async () => {
  const commitFile = vi.fn(async (_s, _sha, path) => ({ path, size: 5, view: 'text', content_type: 'text/plain', mtime: '', blob: new Blob(['whole']) }))
  const files = { commitFile, raw: async () => new Blob([patch]), list: async () => [] } as unknown as TransportFiles
  const context: ChatContextValue = { sessionId: 'session', files, view: vi.fn(), latestVersion: () => undefined, byName: () => undefined, reviewFor: async () => null, submitFeedback: async () => true }
  render(<ChatContext.Provider value={context}><ArtifactViewer sessionId="session" files={files} artifact={{ id: 'diff', name: 'changes.patch', size: patch.length, view: 'diff', content_type: 'text/plain' }} onClose={() => {}} /></ChatContext.Provider>)
  fireEvent.click(await screen.findByRole('tab', { name: 'Files' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Open changed' }))
  await waitFor(() => expect(commitFile).toHaveBeenCalledWith('session', sha, 'new.txt', 'changed', expect.any(AbortSignal), undefined))
})

it('does not offer file actions or repository reads without a per-file commit', () => {
  const commitRepositories = vi.fn(async () => [])
  const files = { commitFile: vi.fn(), commitRepositories } as unknown as TransportFiles
  render(<DiffView text={patch.replace(`commit ${sha}\n\n`, '')} commit={sha} fileContext={{ sessionId: 'no-commit', files }} />)
  expect(screen.queryByRole('button', { name: 'Open changed' })).toBeNull()
  expect(commitRepositories).not.toHaveBeenCalled()
})
it('does not read repositories when the transport cannot open commit files', () => {
  const commitRepositories = vi.fn(async () => [])
  render(<DiffView text={patch} fileContext={{ sessionId: 'read-only', files: { commitRepositories } as unknown as TransportFiles }} />)
  expect(commitRepositories).not.toHaveBeenCalled()
})
it('opens a Markdown blob through CommitViewer without saving or resolving relative files', async () => {
  const markdown = patch.replaceAll('old.txt', 'old.md').replaceAll('new.txt', 'new.md')
  const commitFile = vi.fn(async (_s, _sha, path) => ({ path, size: 40, view: 'markdown', content_type: 'text/markdown', mtime: '', blob: new Blob(['[Sibling](sibling.md)\n![Image](image.png)']) }))
  const files = { commitDiff: async () => ({ text: markdown }), commitFile } as unknown as TransportFiles
  render(<CommitViewer sessionId="markdown-commit" sha={sha} files={files} onClose={() => {}} />)
  fireEvent.click(await screen.findByRole('button', { name: 'Open changed' }))
  await screen.findByText('Sibling')
  expect(screen.queryByRole('button', { name: 'Save a copy to Files' })).toBeNull()
  expect(screen.queryByRole('link', { name: 'Sibling' })).toBeNull()
  expect(commitFile).toHaveBeenCalledTimes(1)
})
