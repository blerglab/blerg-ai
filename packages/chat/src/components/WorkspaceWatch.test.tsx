import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import WorkspaceFileViewer from './WorkspaceFileViewer'
import { createFakeFiles } from '../test/fakeTransport'
import type { WorkspaceFile } from '../transport/types'

describe('workspace preview watch', () => {
  it('refuses type changes and oversized bytes without replacing the HTML frame', async () => {
    const files = createFakeFiles()
    let changed = false
    const file: WorkspaceFile = { path: 'index.html', view: 'html', content_type: 'text/html', size: 20, mtime: '1', blob: new Blob(['<p>safe</p>']) }
    files.workspaceFile = vi.fn(async () => changed ? { ...file, view: 'pdf', mtime: '2' } : file)
    const view = render(<WorkspaceFileViewer sessionId="watch-type" files={files} path="index.html" onClose={() => {}} embedded />)
    await waitFor(() => expect(view.container.querySelector('iframe')?.srcdoc).toContain('safe'))
    changed = true
    fireEvent.click(screen.getByRole('button', { name: 'Watch file' }))
    await screen.findByText('Could not update preview. The previous file is still shown.')
    expect(view.container.querySelector('iframe')?.srcdoc).toContain('safe')
    fireEvent.click(screen.getByRole('button', { name: 'Stop watching' }))
    files.workspaceFile = vi.fn(async () => ({ ...file, mtime: '3', blob: new Blob(['x'.repeat(2 * 1024 * 1024 + 1)]) }))
    fireEvent.click(screen.getByRole('button', { name: 'Watch file' }))
    await screen.findByText('Could not update preview. The previous file is still shown.')
    expect(view.container.querySelector('iframe')?.srcdoc).toContain('safe')
  })
  it('aborts an in-flight watch and ignores its late result', async () => {
    const files = createFakeFiles()
    const file: WorkspaceFile = { path: 'index.html', view: 'html', content_type: 'text/html', size: 20, mtime: '1', blob: new Blob(['<p>original</p>']) }
    let finish: (file: WorkspaceFile) => void = () => {}
    let signal: AbortSignal | undefined
    let calls = 0
    files.workspaceFile = vi.fn(async (_session, _path, incoming) => {
      if (++calls === 1) return file
      signal = incoming
      return new Promise<WorkspaceFile>(resolve => { finish = resolve })
    })
    const view = render(<WorkspaceFileViewer sessionId="watch-late" files={files} path="index.html" onClose={() => {}} embedded />)
    await waitFor(() => expect(view.container.querySelector('iframe')?.srcdoc).toContain('original'))
    fireEvent.click(screen.getByRole('button', { name: 'Watch file' }))
    await waitFor(() => expect(calls).toBe(2))
    fireEvent.click(screen.getByRole('button', { name: 'Stop watching' }))
    expect(signal?.aborted).toBe(true)
    await act(async () => { finish({ ...file, mtime: '2', blob: new Blob(['<p>late</p>']) }) })
    expect(view.container.querySelector('iframe')?.srcdoc).toContain('original')
  })
  it('requires explicit watch, keeps the prior preview on failure, and stops on pin', async () => {
    const files = createFakeFiles()
    let revision = 1
    let fail = false
    files.workspaceFile = vi.fn(async () => {
      if (fail) throw new Error('private server detail')
      const text = `<p>revision ${revision}</p>`
      return { path: 'viz/index.html', view: 'html', content_type: 'text/html', size: text.length, mtime: String(revision), blob: new Blob([text]) } as WorkspaceFile
    })
    const view = render(<WorkspaceFileViewer sessionId="watch" files={files} path="viz/index.html" onClose={() => {}} embedded />)
    const frame = () => view.container.querySelector('iframe')!
    await waitFor(() => expect(frame()?.srcdoc).toContain('revision 1'))
    revision = 2
    expect(files.workspaceFile).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole('button', { name: 'Watch file' }))
    await waitFor(() => expect(frame()?.srcdoc).toContain('revision 2'))
    fail = true
    await screen.findByText('Could not update preview. The previous file is still shown.', {}, { timeout: 4000 })
    expect(frame().srcdoc).toContain('revision 2')
    expect(view.container.textContent).not.toContain('private server detail')
    fireEvent.click(screen.getByRole('button', { name: 'Stop watching' }))
    const count = vi.mocked(files.workspaceFile).mock.calls.length
    await act(() => new Promise(resolve => setTimeout(resolve, 1200)))
    expect(files.workspaceFile).toHaveBeenCalledTimes(count)
  })
})
