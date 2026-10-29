import { splitAttachmentNote } from '../model/attachments'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import ChatView from './ChatView'
import { createFakeTransport, type FakeTransport } from '../test/fakeTransport'
import { SESSION, bigFile, ev as mk, file, meta, resetChatState, seed } from '../test/chat'
import { FileRequestError } from '../transport/files'
import type { SessionMeta } from '../transport/types'
import type { AgentEvent } from '../types'

const idle: SessionMeta = meta({ status: 'idle', started_at: '2026-09-30T00:00:00Z' })
const running: SessionMeta = { ...idle, status: 'running' }

const NOTE = 'Attached files (fetch them with: blerg-runner fetch --all): '

function ev(seq: number, kind: AgentEvent['kind'], payload: unknown): AgentEvent {
  return mk({ seq, kind, ts: '2026-09-30T00:00:00Z', payload })
}

let t: FakeTransport
const files = () => t.files!

beforeEach(() => {
  resetChatState()
  t = createFakeTransport()
  seed(
    ev(1, 'user_message', { text: 'start', source: 'chat' }),
    ev(2, 'turn_done', { stop_reason: 'end_turn', model: 'm', usage: { input_tokens: 1, output_tokens: 1, cache_read_tokens: 0, cache_write_tokens: 0 } }),
  )
})

afterEach(() => { vi.restoreAllMocks() })

function mount(m: SessionMeta = idle) {
  return render(<ChatView session={SESSION} transport={t.transport} meta={m} />)
}

function attach(...list: File[]) {
  fireEvent.change(screen.getByTestId('attach-input'), { target: { files: list } })
}
const box = () => screen.getByRole('textbox') as HTMLTextAreaElement
const sendBtn = () => screen.getByRole('button', { name: 'Send' })
const chips = () => screen.queryAllByTestId('attach-chip')
const send = () => act(async () => { fireEvent.click(sendBtn()) })

describe('composer attachments', () => {
  it('has a keyboard-reachable paperclip with an accessible name', () => {
    mount()
    const btn = screen.getByRole('button', { name: 'Attach files' })
    expect(btn).not.toBeDisabled()
    const input = screen.getByTestId('attach-input') as HTMLInputElement
    expect(input.multiple).toBe(true)
    const click = vi.spyOn(input, 'click')
    fireEvent.click(btn)
    expect(click).toHaveBeenCalled()
  })

  it('uploads a chosen file at once through the transport and shows a chip with name and size', async () => {
    files().sizes['a b.pdf'] = 1258291
    mount()
    const f = file('a b.pdf')
    attach(f)
    await waitFor(() => expect(chips()).toHaveLength(1))
    expect(files().uploads).toHaveLength(1)
    expect(files().uploads[0].file).toBe(f)
    await waitFor(() => expect(chips()[0]).toHaveTextContent('1.2 MB'))
    expect(chips()[0]).toHaveTextContent('a b.pdf')
    expect(within(chips()[0]).getByRole('button', { name: 'Remove a b.pdf' })).toBeInTheDocument()
  })

  it('blocks Send while an upload is in flight and shows the state', async () => {
    let finish!: (r: { id: string; name: string; size: number }) => void
    files().uploadImpl = () => new Promise(res => { finish = res })
    mount()
    fireEvent.change(box(), { target: { value: 'compare' } })
    expect(sendBtn()).not.toBeDisabled()
    attach(file('a.pdf'))
    await waitFor(() => expect(chips()).toHaveLength(1))
    expect(chips()[0]).toHaveTextContent(/uploading/i)
    expect(sendBtn()).toBeDisabled()
    await act(async () => { fireEvent.keyDown(box(), { key: 'Enter' }) })
    expect(t.sent).toHaveLength(0)
    await act(async () => { finish(files().created('a.pdf')) })
    await waitFor(() => expect(sendBtn()).not.toBeDisabled())
    expect(chips()[0]).not.toHaveTextContent(/uploading/i)
  })

  it('shows a server refusal inline and leaves that file out of the message', async () => {
    files().uploadImpl = () => Promise.reject(new Error('this session already has 20 uploaded files; delete one first'))
    mount()
    attach(file('a.pdf'))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('already has 20 uploaded files')
    fireEvent.change(box(), { target: { value: 'hi' } })
    await send()
    expect(t.sent).toEqual(['hi'])
  })

  it('says a 413 is too large', async () => {
    files().uploadImpl = () => Promise.reject(new Error('This file is 130 MiB; the runner takes at most 100 MiB per file.'))
    mount()
    attach(file('a.pdf'))
    expect(await screen.findByRole('alert')).toHaveTextContent('the runner takes at most 100 MiB per file')
  })

  it('without an advertised limit refuses a file over 25 MiB in the browser without uploading it', async () => {
    mount()
    attach(bigFile('huge.iso', 25 * 1024 * 1024 + 1))
    expect(await screen.findByRole('alert')).toHaveTextContent(/huge\.iso.*25 MiB/)
    expect(files().uploads).toHaveLength(0)
  })

  it('takes the limit the host passes (the runner advertises it)', async () => {
    render(<ChatView session={SESSION} transport={t.transport} meta={idle} maxFileBytes={100 * 1024 * 1024} />)
    attach(bigFile('big.iso', 30 * 1024 * 1024))
    await waitFor(() => expect(files().uploads).toHaveLength(1))
    attach(bigFile('huge.iso', 100 * 1024 * 1024 + 1))
    expect(await screen.findByRole('alert')).toHaveTextContent('huge.iso is larger than 100 MiB and can’t be attached.')
    expect(files().uploads).toHaveLength(1)
  })

  it('takes the limit from the session meta when the host passes none', async () => {
    mount({ ...idle, limits: { max_file_bytes: 1024 } })
    attach(bigFile('k.bin', 2048))
    expect(await screen.findByRole('alert')).toHaveTextContent('k.bin is larger than 1.0 KB')
    expect(files().uploads).toHaveLength(0)
  })

  it('shows the server’s error id beside its sentence', async () => {
    files().uploadImpl = () => Promise.reject(new FileRequestError('The runner could not save the file because its disk is full; free some space.', 500, 'ab12cd34'))
    mount()
    attach(file('a.pdf'))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('a.pdf: The runner could not save the file because its disk is full; free some space. (error id ab12cd34)')
    expect(within(alert).getByTestId('error-id')).toHaveAttribute('title', expect.stringContaining('log'))
  })

  it('takes at most 10 files per message', async () => {
    mount()
    attach(...Array.from({ length: 11 }, (_, i) => file(`f${i}.pdf`)))
    await waitFor(() => expect(files().uploads).toHaveLength(10))
    expect(chips()).toHaveLength(10)
    expect(screen.getByTestId('attach-notice')).toHaveTextContent('up to 10 files')
  })

  it('removing a finished chip deletes the upload', async () => {
    mount()
    attach(file('a.pdf'))
    await waitFor(() => expect(chips()[0]).not.toHaveTextContent(/uploading/i))
    fireEvent.click(screen.getByRole('button', { name: 'Remove a.pdf' }))
    await waitFor(() => expect(chips()).toHaveLength(0))
    expect(files().removed).toEqual(['1'.padStart(32, 'a')])
  })

  it('cancelling an upload in flight aborts it', async () => {
    let signal: AbortSignal | undefined
    files().uploadImpl = (_f, ctl) => { signal = ctl.signal; return new Promise(() => {}) }
    mount()
    attach(file('a.pdf'))
    await waitFor(() => expect(chips()).toHaveLength(1))
    fireEvent.click(screen.getByRole('button', { name: 'Remove a.pdf' }))
    expect(signal?.aborted).toBe(true)
    await waitFor(() => expect(chips()).toHaveLength(0))
    expect(sendBtn()).toBeDisabled()
    expect(files().removed).toHaveLength(0)
  })

  it('appends the note exactly once, then clears the chips', async () => {
    files().sizes = { 'a.pdf': 1258291, 'b.pdf': 800 * 1024 }
    mount()
    attach(file('a.pdf'), file('b.pdf'))
    await waitFor(() => expect(chips()).toHaveLength(2))
    fireEvent.change(box(), { target: { value: 'compare these' } })
    await waitFor(() => expect(sendBtn()).not.toBeDisabled())
    await send()
    expect(t.sent).toHaveLength(1)
    expect(splitAttachmentNote(t.sent[0])).toEqual({ body: 'compare these', files: [{ id: '1'.padStart(32, 'a'), name: 'a.pdf', size: '1.2 MB' }, { id: '2'.padStart(32, 'a'), name: 'b.pdf', size: '800 KB' }] })
    expect(chips()).toHaveLength(0)
    fireEvent.change(box(), { target: { value: 'and then?' } })
    await send()
    expect(t.sent[1]).toBe('and then?')
  })

  it('a message of only files gets a generic line before the note', async () => {
    mount()
    expect(sendBtn()).toBeDisabled()
    attach(file('a.pdf'))
    await waitFor(() => expect(sendBtn()).not.toBeDisabled())
    await send()
    expect(t.sent).toHaveLength(1)
    expect(splitAttachmentNote(t.sent[0])).toEqual({ body: 'Please take a look at the attached files.', files: [{ id: '1'.padStart(32, 'a'), name: 'a.pdf', size: '10 B' }] })
  })

  it('keeps the chips when the message could not be sent', async () => {
    mount()
    attach(file('a.pdf'))
    await waitFor(() => expect(sendBtn()).not.toBeDisabled())
    act(() => t.setConnected(false))
    await send()
    expect(chips()).toHaveLength(1)
    expect(screen.getByTestId('send-notice')).toHaveTextContent('not sent')
    expect(t.sent).toHaveLength(0)
  })

  it('drag and drop adds files, announcing the drop zone', async () => {
    mount()
    const chat = screen.getByTestId('agent-chat')
    expect(screen.queryByTestId('drop-zone')).toBeNull()
    fireEvent.dragEnter(chat, { dataTransfer: { types: ['Files'], files: [] } })
    expect(screen.getByTestId('drop-zone')).toHaveTextContent('Drop files to attach')
    expect(screen.getByTestId('drop-zone')).toHaveAttribute('role', 'status')
    fireEvent.drop(chat, { dataTransfer: { types: ['Files'], files: [file('dropped.pdf')] } })
    await waitFor(() => expect(chips()).toHaveLength(1))
    expect(chips()[0]).toHaveTextContent('dropped.pdf')
    expect(screen.queryByTestId('drop-zone')).toBeNull()
  })

  it('ignores a drag that carries no files (text, links)', () => {
    mount()
    fireEvent.dragEnter(screen.getByTestId('agent-chat'), { dataTransfer: { types: ['text/plain'], files: [] } })
    expect(screen.queryByTestId('drop-zone')).toBeNull()
  })

  it('pasting a file adds it; pasting text does not', async () => {
    mount()
    fireEvent.paste(box(), { clipboardData: { files: [], types: ['text/plain'], getData: () => 'hello' } })
    expect(chips()).toHaveLength(0)
    fireEvent.paste(box(), { clipboardData: { files: [file('shot.png')], types: ['Files'], getData: () => '' } })
    await waitFor(() => expect(chips()).toHaveLength(1))
    expect(chips()[0]).toHaveTextContent('shot.png')
  })

  it('keeps the chips when you leave the session and come back', async () => {
    const { unmount } = mount()
    attach(file('a.pdf'))
    await waitFor(() => expect(chips()[0]).not.toHaveTextContent(/uploading/i))
    unmount()
    mount()
    expect(chips()).toHaveLength(1)
    expect(chips()[0]).toHaveTextContent('a.pdf')
    expect(sendBtn()).not.toBeDisabled()
  })

  it('disables the paperclip while the session is starting', () => {
    mount({ ...idle, status: 'starting' })
    expect(screen.getByRole('button', { name: 'Attach files' })).toBeDisabled()
  })
})

describe('messages with attachments in the transcript', () => {
  it('shows a sent note as file chips, not raw text', async () => {
    seed(ev(3, 'user_message', { text: `compare these\n\n${NOTE}a.pdf (1.2 MB), b.pdf (800 KB)`, source: 'chat' }))
    mount()
    const att = await screen.findByTestId('message-attachments')
    expect(within(att).getAllByRole('listitem').map(li => li.textContent)).toEqual([
      expect.stringContaining('a.pdf'), expect.stringContaining('b.pdf'),
    ])
    expect(att).toHaveTextContent('1.2 MB')
    expect(screen.getByText('compare these')).toBeInTheDocument()
    expect(screen.queryByText(/blerg-runner fetch --all/)).toBeNull()
  })

  it('leaves an ordinary message alone', () => {
    seed(ev(3, 'user_message', { text: 'Attached files are great', source: 'chat' }))
    mount()
    expect(screen.getByText('Attached files are great')).toBeInTheDocument()
    expect(screen.queryByTestId('message-attachments')).toBeNull()
  })

  it('a queued message (session running) shows its attachments, and Esc does not interrupt', async () => {
    seed(ev(3, 'user_message', { text: 'go', source: 'chat' }))
    mount(running)
    attach(file('a.pdf'))
    await waitFor(() => expect(chips()[0]).not.toHaveTextContent(/uploading/i))
    fireEvent.change(box(), { target: { value: 'read this' } })
    await send()
    const q = await screen.findByTestId('queued-message')
    expect(within(q).getByTestId('message-attachments')).toHaveTextContent('a.pdf')
    expect(q).toHaveTextContent('read this')
    expect(q).toHaveTextContent(/Queued/)

    // The real event replaces the dimmed bubble once the turn picks the message up.
    act(() => t.emit(ev(9, 'user_message', { text: t.sent[0], source: 'chat' })))
    await waitFor(() => expect(screen.queryByTestId('queued-message')).toBeNull())

    fireEvent.keyDown(box(), { key: 'Escape' })
    expect(t.interrupts).toBe(0)
  })

  it('shows upload progress on the chip as it goes, then Processing, then the size', async () => {
    let finish!: (r: { id: string; name: string; size: number }) => void
    let report!: (loaded: number, total: number) => void
    files().uploadImpl = (_f, ctl) => { report = ctl.onProgress!; return new Promise(res => { finish = res }) }
    files().sizes['big.pdf'] = 10 * 1024 * 1024
    mount()
    attach(bigFile('big.pdf', 10 * 1024 * 1024))
    await waitFor(() => expect(chips()).toHaveLength(1))
    expect(within(chips()[0]).getByRole('progressbar', { name: 'Uploading big.pdf' })).toHaveAttribute('aria-valuenow', '0')

    act(() => report(2.5 * 1024 * 1024, 10 * 1024 * 1024))
    await waitFor(() => expect(chips()[0]).toHaveTextContent('Uploading 25% · 2.5 MB of 10.0 MB'))
    expect(within(chips()[0]).getByRole('progressbar')).toHaveAttribute('aria-valuenow', '25')

    act(() => report(10 * 1024 * 1024, 10 * 1024 * 1024))
    await waitFor(() => expect(chips()[0]).toHaveTextContent('Processing'))

    await act(async () => { finish(files().created('big.pdf')) })
    await waitFor(() => expect(chips()[0]).toHaveTextContent('10.0 MB'))
    expect(within(chips()[0]).queryByRole('progressbar')).toBeNull()
  })

  it('reads the Files list again when an upload finishes and when the panel opens', async () => {
    const listCalls = () => files().listCalls
    mount()
    await waitFor(() => expect(listCalls()).toBeGreaterThan(0))
    const before = listCalls()

    // an upload leaves no transcript event: the list must be read again once it is done
    attach(file('a.pdf'))
    await waitFor(() => expect(chips()[0]).toHaveTextContent('10 B'))
    await waitFor(() => expect(listCalls()).toBeGreaterThan(before))
    const afterUpload = listCalls()
    await screen.findByRole('button', { name: /Files \(1\)/ })

    // opening the panel reads it again too
    fireEvent.click(screen.getByRole('button', { name: /Files/ }))
    await waitFor(() => expect(listCalls()).toBeGreaterThan(afterUpload))
    expect(await screen.findByText('you')).toBeInTheDocument()
  })
})

describe('files waiting for space (#231)', () => {
  const full = (over: Partial<import('../transport/types').FileSpace> = {}) => ({
    artifacts: [{id: 'old', name: 'old.txt', size: 8, origin: 'user' as const, content_type: 'text/plain', view: 'text' as const, can_delete: true}],
    limits: {max_session_upload_bytes: 8, max_session_upload_cap_bytes: 16}, can_delete: true, can_increase: false, ...over,
  })

  it('keeps Send enabled for a typed message, sends it without the waiting file, and keeps the file waiting', async () => {
    files().space = async () => full()
    mount()
    attach(file('waiting.txt'))
    await waitFor(() => expect(chips()[0]).toHaveTextContent('Waiting for space'))
    fireEvent.change(box(), {target: {value: 'an answer meanwhile'}})
    expect(sendBtn()).not.toBeDisabled()
    await send()
    expect(t.sent).toEqual(['an answer meanwhile'])
    expect(chips()).toHaveLength(1)
    expect(chips()[0]).toHaveTextContent('Waiting for space')
    expect(files().uploads).toHaveLength(0)
  })

  it('shows the waiting actions once for several waiting files, says older attachments can be deleted, and announces it', async () => {
    files().space = async () => full()
    mount()
    attach(file('one.txt'), file('two.txt'), file('three.txt'))
    await waitFor(() => expect(chips().filter(c => c.textContent?.includes('Waiting for space'))).toHaveLength(3))
    expect(screen.getAllByRole('button', {name: 'Free up space…'})).toHaveLength(1)
    expect(screen.getByTestId('attach-space-panel')).toHaveTextContent('You can delete older attachments to make room')
    expect(screen.getByTestId('attach-waiting-status')).toHaveTextContent('3 files are waiting for space')
    expect(screen.queryByRole('button', {name: /Ask the session/})).toBeNull()
    // Not an administrator: the person is told whom to ask.
    expect(screen.getByTestId('attach-space-panel')).toHaveTextContent('Ask an admin to increase this session’s storage.')
    expect(screen.queryByRole('button', {name: 'Increase storage'})).toBeNull()
  })

  it('lets an administrator increase storage once, and the waiting file then uploads', async () => {
    let raised = false
    let calls = 0
    files().space = async () => raised ? full({limits: {max_session_upload_bytes: 32, max_session_upload_cap_bytes: 32}, can_increase: false}) : full({can_increase: true})
    files().increaseStorage = async () => {calls++; await new Promise(r => setTimeout(r, 20)); raised = true}
    mount()
    attach(file('waiting.txt'))
    const button = await screen.findByRole('button', {name: 'Increase storage'})
    fireEvent.click(button); fireEvent.click(button)
    await waitFor(() => expect(files().uploads).toHaveLength(1))
    expect(calls).toBe(1)
    await waitFor(() => expect(chips()[0]).not.toHaveTextContent('Waiting'))
  })

  it('says the server cannot be reached instead of claiming there is no space', async () => {
    files().space = async () => {throw new TypeError('Failed to fetch')}
    mount()
    attach(file('waiting.txt'))
    await waitFor(() => expect(chips()[0]).toHaveTextContent('Waiting to reach the server'))
    expect(screen.getByTestId('attach-space-panel')).toHaveTextContent('Can’t reach the server')
    expect(screen.queryByText(/attachments are full/)).toBeNull()
  })

  it('stops waiting when the session ends: the file fails and nothing polls', async () => {
    let checks = 0
    files().space = async () => {checks++; return full()}
    const view = mount()
    attach(file('waiting.txt'))
    await waitFor(() => expect(chips()[0]).toHaveTextContent('Waiting for space'))
    view.rerender(<ChatView session={SESSION} transport={t.transport} meta={{...idle, status: 'stopped'}} />)
    act(() => t.setStatus('stopped'))
    await waitFor(() => expect(chips()[0]).toHaveTextContent('session has ended'))
    const after = checks
    await act(async () => {window.dispatchEvent(new Event('focus')); await new Promise(r => setTimeout(r, 30))})
    expect(checks).toBe(after)
  })
})
