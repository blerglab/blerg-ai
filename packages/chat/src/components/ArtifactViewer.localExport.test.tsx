import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import ArtifactViewer from './ArtifactViewer'
import { createFakeFiles } from '../test/fakeTransport'

afterEach(() => vi.restoreAllMocks())
it('downloads already displayed documents from memory without another server request', async () => {
  const files = createFakeFiles()
  files.serve('# Ready')
  const save = vi.fn((_blob: Blob | MediaSource) => 'blob:local-export')
  vi.spyOn(URL, 'createObjectURL').mockImplementation(save)
  vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
  render(<ArtifactViewer files={files} sessionId="session" artifact={{ id: 'doc', name: 'ready.md', view: 'markdown', content_type: 'text/markdown', size: 7 }} onClose={() => {}} />)
  await screen.findByRole('heading', { name: 'Ready' })
  fireEvent.click(screen.getByRole('button', { name: 'Download' }))
  await waitFor(() => expect(save).toHaveBeenCalled())
  expect(files.downloads).toEqual([])
  expect(await (save.mock.calls[0][0] as Blob).text()).toBe('# Ready')
})
