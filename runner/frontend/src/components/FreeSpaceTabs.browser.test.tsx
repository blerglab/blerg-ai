// #231: files queued in one tab, seen from another, in real Chromium (IndexedDB and Web Locks are
// the browser's own). A second tab is a second instance of the attachments module, with its own
// tab id, upload queue, remembered chips and liveness lock, sharing only what real tabs share.
// (Vite serves one extra instance per file here, so a third tab reuses the first instance under a
// new tab id once the first tab has closed.)
import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest'
import {act, render, waitFor, within} from '@testing-library/react'
import {page, userEvent} from 'vitest/browser'
import {ComposerChips, type ArtifactInfo, type FileSpace, type TransportFiles} from '@blerglab/chat'
import * as tabOne from '../../../../packages/chat/src/hooks/useAttachments'
import {attachmentQueue, attachmentQueueKey} from '../../../../packages/chat/src/model/attachmentQueue'
import '@blerglab/chat/blerg.css'

type Hooks = typeof tabOne
const SID = 'tabs-session'
function fixture() {
  let limit = 8
  let artifacts: ArtifactInfo[] = [{id: 'old', name: 'old.txt', size: 6, content_type: 'text/plain', view: 'text', origin: 'user', created_at: '2026-10-02T00:00:00Z', can_delete: true}]
  const space = vi.fn(async (): Promise<FileSpace> => ({artifacts, limits: {max_session_upload_bytes: limit}, can_delete: true}))
  const upload = vi.fn(async (_sid: string, file: File) => {
    await new Promise(r => setTimeout(r, 20))
    const up = {id: `up-${upload.mock.calls.length}`, name: file.name, size: file.size}
    artifacts = [...artifacts, {...up, origin: 'user', content_type: 'text/plain', view: 'text', can_delete: true}]
    return up
  })
  const files: TransportFiles = {queueScope: () => 'tabs', list: async () => artifacts, space, upload, remove: async () => {}, raw: async () => new Blob(), download: async () => {}}
  // Each tab has its own transport (and so its own upload calls) over the same server.
  const tab = () => {const own = vi.fn((sid: string, file: File) => upload(sid, file)); return {files: {...files, upload: own} as TransportFiles, upload: own}}
  return {files, upload, tab, makeRoom: () => {limit = 64}}
}
const chip = (region: HTMLElement) => within(region).getByTestId('attach-chip')
function Tab({hooks, files, label, name = 'private.txt'}: {hooks: Hooks; files: TransportFiles; label: string; name?: string}) {
  const attached = hooks.useAttachments(SID, files)
  return <section aria-label={label}>
    <button onClick={() => attached.addFiles([new File(['abcde'], name)])}>Attach in {label}</button>
    <button onClick={() => void attached.retryWaiting()}>Retry in {label}</button>
    <ComposerChips items={attached.items} onRemove={attached.remove} />
  </section>
}
const stored = (files: TransportFiles) => attachmentQueue.read(attachmentQueueKey(SID, files)!)
async function secondTab(id = 'tab-two'): Promise<Hooks> {
  // The same module under another URL: Vite serves a separate instance, as another tab loads it.
  const url = new URL(`../../../../packages/chat/src/hooks/useAttachments.ts?${id}`, import.meta.url).href
  const hooks = await import(/* @vite-ignore */ url) as Hooks
  await hooks.setAttachmentTabForTest(id)
  return hooks
}
beforeEach(async () => {
  await tabOne.setAttachmentTabForTest('tab-one')
  await tabOne.resetKeptAttachments()
  // No five-second polls: each tab acts only when told, so interleavings are exact. (Only the
  // hook's poll is held back; the test library's own intervals still run.)
  window.setInterval = ((fn: TimerHandler, ms?: number, ...args: unknown[]) => ms === tabOne.WAITING_RETRY_MS ? 0 : realInterval(fn, ms, ...args)) as typeof window.setInterval
})
const realInterval = window.setInterval.bind(window)
afterEach(async () => {window.setInterval = realInterval as typeof window.setInterval; await tabOne.resetKeptAttachments(); await page.viewport(1280, 800)})

describe('files queued in another tab', () => {
  for (const width of [390, 3840]) it(`only the tab that chose a file uploads it; the other shows it read-only (rereview 2, finding 1) at ${width}px`, async () => {
    await page.viewport(width, 900)
    const tabTwo = await secondTab()
    const {tab, upload, makeRoom} = fixture()
    const t1 = tab(), t2 = tab()
    // Tab one chooses private.txt and stays open with it waiting.
    const ui = render(<Tab hooks={tabOne} files={t1.files} label="one" />)
    const one = ui.getByRole('region', {name: 'one'})
    await userEvent.click(within(one).getByRole('button', {name: 'Attach in one'}))
    await waitFor(() => expect(chip(one)).toHaveTextContent('Waiting for space'))
    await waitFor(async () => expect((await stored(t1.files))?.[0]).toMatchObject({name: 'private.txt', owner: 'tab-one'}))
    // Tab two opens the session: the file is there, read-only, with no remove button.
    const ui2 = render(<Tab hooks={tabTwo} files={t2.files} label="two" />)
    const two = ui2.getByRole('region', {name: 'two'})
    await waitFor(() => expect(chip(two)).toHaveTextContent('Waiting in another tab'))
    expect(within(chip(two)).queryByRole('button', {name: /Remove/})).toBeNull()
    expect(chip(two).getBoundingClientRect().right).toBeLessThanOrEqual(width)
    // Room appears. Tab two retries: it uploads nothing. Tab one does.
    makeRoom()
    await userEvent.click(within(two).getByRole('button', {name: 'Retry in two'}))
    await userEvent.click(within(one).getByRole('button', {name: 'Retry in one'}))
    await waitFor(() => expect(chip(one)).toHaveClass('done'))
    // Tab two follows: attached in the other tab, never sent from here.
    await userEvent.click(within(two).getByRole('button', {name: 'Retry in two'}))
    await waitFor(() => expect(chip(two)).toHaveTextContent('Attached in another tab'))
    // The reviewer's sequence: the chooser retries again later; nothing is sent twice.
    await userEvent.click(within(one).getByRole('button', {name: 'Retry in one'}))
    await act(async () => {await attachmentQueue.flush(); await new Promise(r => setTimeout(r, 50))})
    expect(t2.upload).not.toHaveBeenCalled()
    expect(t1.upload).toHaveBeenCalledTimes(1)
    expect(upload).toHaveBeenCalledTimes(1)
    ui2.unmount(); ui.unmount()
  })

  it('takes over a file whose tab closed, once, and keeps it across a reload of the same tab', async () => {
    const {tab, upload, makeRoom} = fixture()
    const t1 = tab(), t2 = tab(), t3 = tab()
    let ui = render(<Tab hooks={tabOne} files={t1.files} label="one" />)
    await userEvent.click(ui.getByRole('button', {name: 'Attach in one'}))
    await waitFor(() => expect(chip(ui.getByRole('region', {name: 'one'}))).toHaveTextContent('Waiting for space'))
    await waitFor(async () => expect((await stored(t1.files))?.[0]?.owner).toBe('tab-one'))
    // A reload of the same tab (same id) is still the owner: its own chip, not another tab's.
    ui.unmount(); await tabOne.resetKeptAttachments(false)
    ui = render(<Tab hooks={tabOne} files={t1.files} label="one" />)
    await waitFor(() => expect(chip(ui.getByRole('region', {name: 'one'}))).toHaveTextContent('Waiting for space'))
    // Tab one closes: its lock goes.
    ui.unmount(); await tabOne.setAttachmentTabForTest(null)
    // Tabs two and three open and find the owner gone: exactly one takes the file over.
    // (Tab three is the first module instance again, under a new tab id and with no memory.)
    const tabTwo = await secondTab('tab-two')
    await tabOne.resetKeptAttachments(false); await tabOne.setAttachmentTabForTest('tab-three')
    const tabThree = tabOne
    expect(tabThree).not.toBe(tabTwo)
    makeRoom()
    const both = render(<><Tab hooks={tabTwo} files={t2.files} label="two" /><Tab hooks={tabThree} files={t3.files} label="three" /></>)
    await waitFor(async () => expect(['tab-two', 'tab-three']).toContain((await stored(t1.files))?.[0]?.owner), {timeout: 10000})
    const owner = (await stored(t1.files))![0]!.owner!
    const [mine, theirs] = owner === 'tab-two' ? ['two', 'three'] : ['three', 'two']
    await userEvent.click(both.getByRole('button', {name: `Retry in ${mine}`}))
    await waitFor(() => expect(chip(both.getByRole('region', {name: mine}))).toHaveClass('done'), {timeout: 10000})
    await userEvent.click(both.getByRole('button', {name: `Retry in ${theirs}`}))
    await waitFor(() => expect(chip(both.getByRole('region', {name: theirs}))).toHaveTextContent('Attached in another tab'))
    expect(upload).toHaveBeenCalledTimes(1)
    expect((owner === 'tab-two' ? t3 : t2).upload).not.toHaveBeenCalled()
    both.unmount()
  })

  for (const how of ['sign-out', 'discard', 'unannounced'] as const) it(`${how} in one tab: the owning tab’s late completion and unmount never write the file back (rereview 2, finding 2)`, async () => {
    const tabTwo = await secondTab()
    const {files} = fixture()
    let finish!: () => void
    const late = vi.fn(async (_sid: string, file: File) => {await new Promise<void>(r => {finish = r}); return {id: 'late', name: file.name, size: file.size}})
    files.upload = late
    files.space = async () => ({artifacts: [], limits: {max_session_upload_bytes: 64}, can_delete: true})
    // Tab two owns a file and is uploading it; tab one shows it read-only.
    const two = render(<Tab hooks={tabTwo} files={files} label="two" />)
    await userEvent.click(within(two.getByRole('region', {name: 'two'})).getByRole('button', {name: 'Attach in two'}))
    await waitFor(() => expect(late).toHaveBeenCalled())
    await waitFor(async () => expect((await stored(files))?.[0]?.owner).toBe('tab-two'))
    const one = render(<Tab hooks={tabOne} files={files} label="one" />)
    await waitFor(() => expect(chip(one.getByRole('region', {name: 'one'}))).toHaveTextContent('in another tab'))
    // Cleanup in tab one. "unannounced": the record is deleted before any message reaches tab
    // two (another tab's cleanup whose broadcast has not arrived yet).
    if (how === 'sign-out') await tabOne.resetKeptAttachments()
    else if (how === 'discard') await tabOne.forgetSessionAttachments(SID)
    else await attachmentQueue.writeForTest(attachmentQueueKey(SID, files)!, [])
    expect(await stored(files)).toBeUndefined()
    // Tab two's upload finishes late, then it closes: nothing comes back.
    finish()
    two.unmount(); one.unmount()
    await attachmentQueue.flush(); await new Promise(r => setTimeout(r, 50)); await attachmentQueue.flush()
    expect(await stored(files)).toBeUndefined()
  })
})
