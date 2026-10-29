import {useState} from 'react'
import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest'
import {act, fireEvent, render, waitFor, within} from '@testing-library/react'
import {page, userEvent} from 'vitest/browser'
import {ComposerChips, FreeSpaceButton, resetKeptAttachments, useAttachments, type ArtifactInfo, type FileSpace, type TransportFiles} from '@blerglab/chat'
import {attachmentQueue, attachmentQueueKey} from '../../../../packages/chat/src/model/attachmentQueue'
import FilesTab from './FilesTab'
import '@blerglab/chat/blerg.css'

function fixture(canDelete = true) {
  let artifacts: ArtifactInfo[] = [
    {id: 'newer', name: 'newer.txt', size: 4, content_type: 'text/plain', view: 'text', origin: 'user', created_at: '2026-10-03T00:00:00Z'},
    {id: 'published', name: 'published.md', size: 20, content_type: 'text/plain', view: 'text', origin: 'agent', created_at: '2026-10-01T00:00:00Z'},
    {id: 'old', name: 'old.txt', size: 3, content_type: 'text/plain', view: 'text', origin: 'user', created_at: '2026-10-02T00:00:00Z'},
  ]
  const space = async (): Promise<FileSpace> => ({artifacts: artifacts.map(a => ({...a, can_delete: canDelete})), limits: {max_session_upload_bytes: 8}, can_delete: canDelete})
  const upload = vi.fn(async (_sid: string, file: File) => {
    expect(await file.text()).toBe('abcde')
    const up = {id: file.name, name: file.name, size: file.size}
    artifacts = [...artifacts, {...up, origin: 'user', content_type: 'text/plain', view: 'text'}]
    return up
  })
  const remove = vi.fn(async (_sid: string, id: string) => {artifacts = artifacts.filter(a => a.id !== id)})
  const files: TransportFiles = {queueScope: () => 'fixture', list: async () => artifacts, space, upload, remove, raw: async () => new Blob(), download: async () => {}}
  return {files, upload, remove}
}
function Harness({files}: {files: TransportFiles}) {
  const attached = useAttachments('waiting-session', files)
  const [panel, setPanel] = useState(false)
  return <main style={{width: '100%', maxWidth: 1000}}>
    <button onClick={() => attached.addFiles([new File(['abcde'], 'queued.txt')])}>Attach fixture</button>
    <ComposerChips items={attached.items} onRemove={attached.remove} waitingActions={<FreeSpaceButton sessionId="waiting-session" files={files} waitingBytes={attached.waitingBytes} />} />
    <button onClick={() => setPanel(!panel)}>Files panel</button>
    {panel && <FilesTab sessionId="waiting-session" active files={{ops: files, items: [], status: 'ready', refresh: () => {}}} onOpenArtifact={() => {}} />}
  </main>
}
beforeEach(async () => {await resetKeptAttachments()})
afterEach(async () => {await resetKeptAttachments(); await page.viewport(1280, 800)})

describe('waiting for session space', () => {
  for (const width of [390, 3840]) it(`preselects oldest attachments, deletes, and resumes automatically at ${width}px`, async () => {
    await page.viewport(width, 1000)
    const {files, upload, remove} = fixture()
    const ui = render(<Harness files={files} />)
    await userEvent.click(ui.getByRole('button', {name: 'Attach fixture'}))
    await waitFor(() => expect(ui.getByTestId('attach-chip')).toHaveTextContent('Waiting for space'))
    expect(upload).not.toHaveBeenCalled()
    await userEvent.click(ui.getByRole('button', {name: 'Free up space…'}))
    const dialog = ui.getByRole('dialog', {name: 'Free up space'})
    await waitFor(() => expect(within(dialog).getAllByRole('checkbox')).toHaveLength(3))
    const boxes = within(dialog).getAllByRole('checkbox') as HTMLInputElement[]
    expect(boxes.map(b => b.checked)).toEqual([false, true, true])
    expect(boxes.map(b => b.closest('label')!.textContent)).toEqual([expect.stringContaining('published.md'), expect.stringContaining('old.txt'), expect.stringContaining('newer.txt')])
    const bounds = dialog.getBoundingClientRect()
    expect(bounds.left).toBeGreaterThanOrEqual(0)
    expect(bounds.right).toBeLessThanOrEqual(width)
    expect(dialog.scrollWidth).toBeLessThanOrEqual(dialog.clientWidth)
    const shots = import.meta.env.VITE_FILE_TAB_SHOTS_DIR
    if (shots) await page.screenshot({path: `${shots}/free-space-${width}.png`})
    await userEvent.click(within(dialog).getByRole('button', {name: 'Delete selected files'}))
    await waitFor(() => expect(ui.getByTestId('attach-chip')).not.toHaveTextContent('Waiting for space'))
    expect(remove.mock.calls.map(c => c[1])).toEqual(['old', 'newer'])
    expect(upload).toHaveBeenCalledTimes(1)
  })
  it('restores waiting bytes and metadata from IndexedDB after losing page memory', async () => {
    const {files, upload} = fixture()
    let ui = render(<Harness files={files} />)
    await userEvent.click(ui.getByRole('button', {name: 'Attach fixture'}))
    await waitFor(() => expect(ui.getByTestId('attach-chip')).toHaveTextContent('Waiting for space'))
    await waitFor(async () => expect((await attachmentQueue.read(attachmentQueueKey('waiting-session', files)!))?.[0]?.file?.size).toBe(5))
    ui.unmount()
    await resetKeptAttachments(false)
    ui = render(<Harness files={files} />)
    await waitFor(() => expect(ui.getByTestId('attach-chip')).toHaveTextContent('Waiting for space'))
    expect(ui.getByTestId('attach-chip')).toHaveTextContent('queued.txt')
    expect(upload).not.toHaveBeenCalled()
    await userEvent.click(ui.getByRole('button', {name: 'Files panel'}))
    await userEvent.click(within(ui.getByTestId('rail-files')).getByRole('button', {name: 'Free up space…'}))
    const cleanup = ui.getByRole('dialog', {name: 'Free up space'})
    await waitFor(() => expect(within(cleanup).getAllByRole('checkbox').map(box => (box as HTMLInputElement).checked)).toEqual([false, true, true]))
    await userEvent.click(within(cleanup).getByRole('button', {name: 'Cancel'}))
    await act(async () => {await files.remove('waiting-session', 'old'); await files.remove('waiting-session', 'newer'); window.dispatchEvent(new Event('blerg-files-changed'))})
    await waitFor(() => expect(upload).toHaveBeenCalledTimes(1), {timeout: 10000})
    await waitFor(() => expect(ui.getByTestId('attach-chip')).toHaveClass('done'))
    ui.unmount()
    await resetKeptAttachments(false)
    ui = render(<Harness files={files} />)
    await waitFor(() => expect(ui.getByTestId('attach-chip')).toHaveClass('done'))
    expect(upload).toHaveBeenCalledTimes(1)
    await userEvent.click(ui.getByRole('button', {name: 'Remove queued.txt'}))
    await waitFor(async () => expect(await attachmentQueue.read(attachmentQueueKey('waiting-session', files)!)).toBeUndefined())
  })
  it('keeps durable waiting bytes private when another account opens the same session', async () => {
    const {files} = fixture()
    let viewer = 'first-account'
    files.queueScope = () => viewer
    let ui = render(<Harness files={files} />)
    await userEvent.click(ui.getByRole('button', {name: 'Attach fixture'}))
    await waitFor(() => expect(ui.getByTestId('attach-chip')).toHaveTextContent('Waiting for space'))
    const firstKey = attachmentQueueKey('waiting-session', files)!
    await waitFor(async () => expect((await attachmentQueue.read(firstKey))?.[0]?.file?.size).toBe(5))
    ui.unmount(); await resetKeptAttachments(false)
    viewer = 'second-account'
    ui = render(<Harness files={files} />)
    await act(async () => {await attachmentQueue.read(attachmentQueueKey('waiting-session', files)!)})
    expect(ui.queryByTestId('attach-chip')).toBeNull()
    expect((await attachmentQueue.read(firstKey))?.[0]?.name).toBe('queued.txt')
    ui.unmount(); await resetKeptAttachments(false)
    viewer = 'first-account'
    ui = render(<Harness files={files} />)
    await waitFor(() => expect(ui.getByTestId('attach-chip')).toHaveTextContent('queued.txt'))
  })
  it('opens the same cleanup from the session Files panel and prevents a viewer from deleting', async () => {
    const {files, remove} = fixture(false)
    const ui = render(<Harness files={files} />)
    fireEvent.click(ui.getByRole('button', {name: 'Files panel'}))
    fireEvent.click(ui.getByRole('button', {name: 'Free up space…'}))
    const dialog = ui.getByRole('dialog', {name: 'Free up space'})
    await waitFor(() => expect(within(dialog).getByRole('status')).toHaveTextContent('Only the session’s owner'))
    expect(within(dialog).getByRole('button', {name: 'Delete selected files'})).toBeDisabled()
    for (const box of within(dialog).getAllByRole('checkbox')) expect(box).toBeDisabled()
    expect(remove).not.toHaveBeenCalled()
    fireEvent.keyDown(dialog, {key: 'Escape'})
    expect(ui.queryByRole('dialog')).toBeNull()
  })
})
