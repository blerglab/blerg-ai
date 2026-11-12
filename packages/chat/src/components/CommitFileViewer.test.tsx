import { render, screen, waitFor } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { createFakeFiles } from '../test/fakeTransport'
import CommitFileViewer from './CommitFileViewer'

const member = { member_id: 'repo', launch_generation: 'launch', generation: 2 }
const sha = 'abcdef1234567890abcdef1234567890abcdef12'
it('reads qualified immutable versions independently without reading the working tree', async () => {
  const workspaceFile = vi.fn()
  const commitFile = vi.fn(async (_session: string, _sha: string, path: string, side: string) => ({ path, size: 20, view: 'text', content_type: 'text/plain', mtime: '', blob: new Blob([`${side} file contents`]) }))
  const files = { ...createFakeFiles(), workspaceFile, commitFile }
  render(<>{(['original', 'changed'] as const).map(side => <CommitFileViewer key={side} sessionId="main" files={files} path="app.ts" version={{ sha, side, sessionId: 'channel', member }} onClose={() => {}} />)}</>)
  await screen.findByText('original file contents')
  await screen.findByText('changed file contents')
  expect(commitFile).toHaveBeenCalledWith('channel', sha, 'app.ts', 'original', expect.any(AbortSignal), member)
  expect(commitFile).toHaveBeenCalledWith('channel', sha, 'app.ts', 'changed', expect.any(AbortSignal), member)
  expect(workspaceFile).not.toHaveBeenCalled()
  expect(screen.queryByRole('button', { name: 'Save a copy to Files' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Watch file' })).toBeNull()
})
it('fails closed when immutable commit reads are unavailable', async () => {
  const workspaceFile = vi.fn()
  render(<CommitFileViewer sessionId="channel" files={{ ...createFakeFiles(), workspaceFile }} path="app.ts" version={{ sha, side: 'changed' }} onClose={() => {}} />)
  await waitFor(() => expect(screen.getByTestId('workspace-error')).toHaveTextContent('Couldn’t reach the server to open that file. Try again'))
  expect(workspaceFile).not.toHaveBeenCalled()
})
