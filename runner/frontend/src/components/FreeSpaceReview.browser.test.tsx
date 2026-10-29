// #231 after review: two tabs never send the same waiting file twice, the waiting actions show once
// (with Increase storage for an administrator), the Files tab offers Delete only where the person
// may delete, and stale or discarded waiting files leave this device. Real Chromium: IndexedDB
// and Web Locks are the browser's own.
import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest'
import {act, render, waitFor, within} from '@testing-library/react'
import {page, userEvent} from 'vitest/browser'
import {ComposerChips, WaitingSpaceActions, forgetSessionAttachments, resetKeptAttachments, useAttachments, type ArtifactInfo, type FileSpace, type TransportFiles} from '@blerglab/chat'
import {attachmentQueue, attachmentQueueKey, QUEUE_MAX_AGE_MS, setAttachmentQueueClock} from '../../../../packages/chat/src/model/attachmentQueue'
import FilesTab from './FilesTab'
import '@blerglab/chat/blerg.css'
import './SessionRail.css'

const SID = 'review-session'
function fixture(opts: {canIncrease?: boolean} = {}) {
  let limit = 8
  let artifacts: ArtifactInfo[] = [
    {id: 'old', name: 'old.txt', size: 6, content_type: 'text/plain', view: 'text', origin: 'user', created_at: '2026-10-02T00:00:00Z', can_delete: true},
  ]
  const space = vi.fn(async (): Promise<FileSpace> => ({artifacts, limits: {max_session_upload_bytes: limit, max_session_upload_cap_bytes: 64}, can_delete: true, can_increase: opts.canIncrease === true && limit < 64}))
  const upload = vi.fn(async (_sid: string, file: File) => {
    await new Promise(r => setTimeout(r, 30))
    const up = {id: `up-${upload.mock.calls.length}`, name: file.name, size: file.size}
    artifacts = [...artifacts, {...up, origin: 'user', content_type: 'text/plain', view: 'text', can_delete: true}]
    return up
  })
  const increaseStorage = vi.fn(async () => {limit = 64})
  const files: TransportFiles = {queueScope: () => 'review', list: async () => artifacts, space, upload, increaseStorage, remove: async () => {}, raw: async () => new Blob(), download: async () => {}}
  return {files, upload, space, increaseStorage, makeRoom: () => {limit = 64}}
}
function Composer({files, label}: {files: TransportFiles; label: string}) {
  const attached = useAttachments(SID, files)
  return <section aria-label={label} style={{width: '100%', maxWidth: 1000}}>
    <button onClick={() => attached.addFiles([new File(['abcde'], 'queued.txt')])}>Attach in {label}</button>
    <ComposerChips items={attached.items} onRemove={attached.remove} waitingActions={<WaitingSpaceActions sessionId={SID} files={files}
      waitingBytes={attached.waitingBytes} waitingForSpace={attached.waitingForSpace} space={attached.space} />} />
  </section>
}
beforeEach(async () => {await resetKeptAttachments()})
afterEach(async () => {setAttachmentQueueClock(null); await resetKeptAttachments(); await page.viewport(1280, 800)})

describe('files waiting for space, after review', () => {
  it('two tabs that restored the same waiting file send it once (finding 3)', async () => {
    const {files, upload, makeRoom} = fixture()
    const locks = vi.spyOn(navigator.locks, 'request')
    let ui = render(<Composer files={files} label="first" />)
    await userEvent.click(ui.getByRole('button', {name: 'Attach in first'}))
    await waitFor(() => expect(ui.getByTestId('attach-chip')).toHaveTextContent('Waiting for space'))
    await waitFor(async () => expect((await attachmentQueue.read(attachmentQueueKey(SID, files)!))?.[0]?.file?.size).toBe(5))
    ui.unmount()
    await resetKeptAttachments(false)
    // Two tabs open on the same session, each restoring the stored waiting file.
    ui = render(<><Composer files={files} label="tab one" /><Composer files={files} label="tab two" /></>)
    const one = ui.getByRole('region', {name: 'tab one'}), two = ui.getByRole('region', {name: 'tab two'})
    await waitFor(() => {
      expect(within(one).getByTestId('attach-chip')).toHaveTextContent('Waiting for space')
      expect(within(two).getByTestId('attach-chip')).toHaveTextContent('Waiting for space')
    })
    makeRoom()
    await act(async () => {window.dispatchEvent(new Event('blerg-files-changed'))})
    await waitFor(() => {
      expect(within(one).getByTestId('attach-chip')).toHaveClass('done')
      expect(within(two).getByTestId('attach-chip')).toHaveClass('done')
    }, {timeout: 10000})
    expect(upload).toHaveBeenCalledTimes(1)
    // Across real tabs the same queue is a Web Lock on this session's key.
    expect(locks.mock.calls.some(([name]) => String(name).startsWith('blerg-attach:'))).toBe(true)
  })

  for (const width of [390, 3840]) it(`shows the waiting actions once, tells the person to delete older attachments, and lets an admin increase storage at ${width}px`, async () => {
    await page.viewport(width, 1000)
    const {files, upload, increaseStorage} = fixture({canIncrease: true})
    const ui = render(<Composer files={files} label="admin" />)
    await userEvent.click(ui.getByRole('button', {name: 'Attach in admin'}))
    await userEvent.click(ui.getByRole('button', {name: 'Attach in admin'}))
    await waitFor(() => expect(ui.getAllByTestId('attach-chip').filter(c => c.textContent?.includes('Waiting for space'))).toHaveLength(2))
    const panel = ui.getByTestId('attach-space-panel')
    expect(within(panel).getAllByRole('button', {name: 'Free up space…'})).toHaveLength(1)
    expect(panel).toHaveTextContent('You can delete older attachments to make room')
    expect(ui.getByTestId('attach-waiting-status')).toHaveTextContent('2 files are waiting for space')
    const bounds = panel.getBoundingClientRect()
    expect(bounds.left).toBeGreaterThanOrEqual(0)
    expect(bounds.right).toBeLessThanOrEqual(width)
    expect(panel.scrollWidth).toBeLessThanOrEqual(panel.clientWidth + 1)
    const shots = import.meta.env.VITE_FILE_TAB_SHOTS_DIR
    if (shots) await page.screenshot({path: `${shots}/waiting-actions-${width}.png`})
    await userEvent.click(await within(panel).findByRole('button', {name: 'Increase storage'}))
    await waitFor(() => expect(upload).toHaveBeenCalledTimes(2), {timeout: 10000})
    expect(increaseStorage).toHaveBeenCalledTimes(1)
    await waitFor(() => expect(ui.queryByTestId('attach-space-panel')).toBeNull())
  })

  it('tells a person who is not an admin whom to ask', async () => {
    const {files} = fixture()
    const ui = render(<Composer files={files} label="member" />)
    await userEvent.click(ui.getByRole('button', {name: 'Attach in member'}))
    await waitFor(() => expect(ui.getByTestId('attach-space-panel')).toHaveTextContent('Ask an admin to increase this session’s storage.'))
    expect(ui.queryByRole('button', {name: 'Increase storage'})).toBeNull()
  })

  for (const width of [390, 3840]) it(`offers Delete in the Files tab only on files the person may delete at ${width}px`, async () => {
    await page.viewport(width, 1000)
    const items: ArtifactInfo[] = [
      {id: 'mine', name: 'mine.txt', size: 4, content_type: 'text/plain', view: 'text', origin: 'user', created_at: '2026-10-03T00:00:00Z', can_delete: true},
      {id: 'theirs', name: 'theirs.txt', size: 4, content_type: 'text/plain', view: 'text', origin: 'user', created_at: '2026-10-02T00:00:00Z', can_delete: false},
    ]
    const remove = vi.fn(async () => {})
    const files: TransportFiles = {list: async () => items, remove, upload: async () => {throw new Error('unused')}, raw: async () => new Blob(), download: async () => {}}
    const refresh = vi.fn()
    const ui = render(<FilesTab sessionId={SID} active files={{ops: files, items, status: 'ready', refresh}} onOpenArtifact={() => {}} />)
    const rows = ui.getAllByTestId('artifact-row')
    const mine = rows.find(r => r.textContent?.includes('mine.txt'))!, theirs = rows.find(r => r.textContent?.includes('theirs.txt'))!
    expect(within(theirs).queryByRole('button', {name: 'Delete'})).toBeNull()
    expect(within(theirs).getByRole('button', {name: 'Download'})).toBeTruthy()
    const changed = vi.fn()
    window.addEventListener('blerg-files-changed', changed)
    await userEvent.click(within(mine).getByRole('button', {name: 'Delete'}))
    await userEvent.click(within(mine).getByRole('button', {name: 'Yes, delete'}))
    await waitFor(() => expect(remove).toHaveBeenCalledWith(SID, 'mine'))
    expect(refresh).toHaveBeenCalled()
    expect(changed).toHaveBeenCalled()
    window.removeEventListener('blerg-files-changed', changed)
    // Rows bleed 6px into the panel's padding for their hover background, never past the screen.
    expect(mine.getBoundingClientRect().right).toBeLessThanOrEqual(width)
    expect(mine.scrollWidth).toBeLessThanOrEqual(mine.clientWidth + 1)
  })

  it('drops waiting files older than a week, and a discarded session’s waiting files (finding 7)', async () => {
    let now = Date.parse('2026-10-01T00:00:00Z')
    setAttachmentQueueClock(() => now)
    const item = {key: 'k', name: 'stale.txt', size: 1, status: 'waiting' as const, file: new File(['x'], 'stale.txt')}
    await attachmentQueue.writeForTest('scope\0stale-session', [item])
    await attachmentQueue.writeForTest('scope\0discarded-session', [item])
    await attachmentQueue.writeForTest('other\0discarded-session', [item])
    now += QUEUE_MAX_AGE_MS + 1
    await attachmentQueue.writeForTest('scope\0fresh-session', [item])
    expect(await attachmentQueue.read('scope\0stale-session')).toBeUndefined()
    await attachmentQueue.prune()
    await forgetSessionAttachments('discarded-session')
    expect(await attachmentQueue.read('scope\0discarded-session')).toBeUndefined()
    expect(await attachmentQueue.read('other\0discarded-session')).toBeUndefined()
    expect((await attachmentQueue.read('scope\0fresh-session'))?.[0]?.name).toBe('stale.txt')
    await resetKeptAttachments()
    expect(await attachmentQueue.read('scope\0fresh-session')).toBeUndefined()
  })
})
