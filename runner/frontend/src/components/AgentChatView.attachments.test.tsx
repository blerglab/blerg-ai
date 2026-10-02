import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import AgentChatView from './AgentChatView'
import { useAgentTranscript } from '../hooks/useAgentTranscript'
import { resetKeptAttachments } from '../hooks/useAttachments'
import type { AgentEvent, SessionInfo } from '../types'

vi.mock('../ws', () => ({
  send: vi.fn(),
  onMessage: vi.fn(() => () => {}),
  onOpen: vi.fn(() => () => {}),
}))

import { send } from '../ws'

const apiFetch = vi.fn()
// What an upload reports while it runs; a test sets it to drive the progress bar.
const progress = vi.hoisted(() => ({ report: null as null | ((cb: (loaded: number, total: number) => void) => void) }))
vi.mock('../apiFetch', () => ({
  apiFetch: (...a: unknown[]) => apiFetch(...a),
  // An upload goes through apiUpload (it can report progress); the tests see it as the same call.
  apiUpload: (url: string, init: RequestInit, onProgress?: (loaded: number, total: number) => void) => {
    if (onProgress) progress.report?.(onProgress)
    return apiFetch(url, init)
  },
}))

const idle: SessionInfo = {
  id: 's1', daemon_id: 'd1', status: 'idle', project_path: '/r/proj', repo: 'proj', title: 'Test',
  model: 'claude-sonnet-5', started_at: '2026-09-30T00:00:00Z', kind: 'agent',
}
const running: SessionInfo = { ...idle, status: 'running' }

const NOTE = 'Attached files (fetch them with: blerg-runner fetch --all): '

let n = 0
function ev(seq: number, kind: AgentEvent['kind'], payload: unknown): AgentEvent {
  n++
  return { type: 'agent_event', session_id: 's1', client_event_id: `e${n}`, seq, ts: '2026-09-30T00:00:00Z', kind, payload }
}

const sentTexts = () => vi.mocked(send).mock.calls
  .map(c => c[0] as { type: string; text?: string })
  .filter(m => m.type === 'agent_user_message')
  .map(m => m.text)

function file(name: string, size = 10): File {
  return new File([new Uint8Array(Math.min(size, 16))], name, { type: 'application/pdf' })
}
function bigFile(name: string, size: number): File {
  const f = file(name)
  Object.defineProperty(f, 'size', { value: size })
  return f
}

let nextId = 0
let sizes: Record<string, number>
let uploadImpl: ((name: string, init: RequestInit) => Promise<Response>) | null

const uploads = () => apiFetch.mock.calls.filter(c => String(c[0]).endsWith('/uploads'))
const deletes = () => apiFetch.mock.calls.filter(c => (c[1] as RequestInit | undefined)?.method === 'DELETE')

function created(name: string): Response {
  nextId++
  const id = String(nextId).padStart(32, 'a')
  return new Response(JSON.stringify({ id, name, size: sizes[name] ?? 10, content_type: 'application/pdf', view: 'pdf', origin: 'user' }), { status: 201 })
}

beforeEach(() => {
  vi.clearAllMocks()
  progress.report = null
  nextId = 0
  resetKeptAttachments()
  sizes = {}
  uploadImpl = null
  useAgentTranscript.setState({ sessions: {} })
  window.localStorage.clear()
  apiFetch.mockReset()
  apiFetch.mockImplementation((url: string, init?: RequestInit) => {
    if (url === '/api/sessions/s1/artifacts') return Promise.resolve(new Response(JSON.stringify({ artifacts: [] })))
    if (url === '/api/sessions/s1/uploads') {
      const name = decodeURIComponent((init?.headers as Record<string, string>)['X-Artifact-Name'])
      return uploadImpl ? uploadImpl(name, init!) : Promise.resolve(created(name))
    }
    if (init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
    return Promise.resolve(new Response('{}', { status: 404 }))
  })
  const ingest = useAgentTranscript.getState().ingest
  ingest(ev(1, 'user_message', { text: 'start', source: 'chat' }))
  ingest(ev(2, 'turn_done', { stop_reason: 'end_turn', model: 'm', usage: { input_tokens: 1, output_tokens: 1, cache_read_tokens: 0, cache_write_tokens: 0 } }))
})

afterEach(() => { vi.restoreAllMocks() })

function attach(...files: File[]) {
  fireEvent.change(screen.getByTestId('attach-input'), { target: { files } })
}
const box = () => screen.getByRole('textbox') as HTMLTextAreaElement
const sendBtn = () => screen.getByRole('button', { name: 'Send' })
const chips = () => screen.queryAllByTestId('attach-chip')

describe('composer attachments', () => {
  it('has a keyboard-reachable paperclip with an accessible name', () => {
    render(<AgentChatView session={idle} />)
    const btn = screen.getByRole('button', { name: 'Attach files' })
    expect(btn).not.toBeDisabled()
    const input = screen.getByTestId('attach-input') as HTMLInputElement
    expect(input.multiple).toBe(true)
    const click = vi.spyOn(input, 'click')
    fireEvent.click(btn)
    expect(click).toHaveBeenCalled()
  })

  it('uploads a chosen file at once as raw bytes and shows a chip with name and size', async () => {
    sizes['a b.pdf'] = 1258291
    render(<AgentChatView session={idle} />)
    const f = file('a b.pdf')
    attach(f)
    await waitFor(() => expect(chips()).toHaveLength(1))
    const call = uploads()[0]
    const init = call[1] as RequestInit
    expect(init.method).toBe('POST')
    expect(init.body).toBe(f)
    expect((init.headers as Record<string, string>)['X-Artifact-Name']).toBe('a%20b.pdf')
    await waitFor(() => expect(chips()[0]).toHaveTextContent('1.2 MB'))
    expect(chips()[0]).toHaveTextContent('a b.pdf')
    expect(within(chips()[0]).getByRole('button', { name: 'Remove a b.pdf' })).toBeInTheDocument()
  })

  it('blocks Send while an upload is in flight and shows the state', async () => {
    let finish!: (r: Response) => void
    uploadImpl = () => new Promise<Response>(res => { finish = res })
    render(<AgentChatView session={idle} />)
    fireEvent.change(box(), { target: { value: 'compare' } })
    expect(sendBtn()).not.toBeDisabled()
    attach(file('a.pdf'))
    await waitFor(() => expect(chips()).toHaveLength(1))
    expect(chips()[0]).toHaveTextContent(/uploading/i)
    expect(sendBtn()).toBeDisabled()
    fireEvent.keyDown(box(), { key: 'Enter' })
    expect(sentTexts()).toHaveLength(0)
    await act(async () => { finish(created('a.pdf')) })
    await waitFor(() => expect(sendBtn()).not.toBeDisabled())
    expect(chips()[0]).not.toHaveTextContent(/uploading/i)
  })

  it('shows a server refusal inline and leaves that file out of the message', async () => {
    uploadImpl = () => Promise.resolve(new Response(JSON.stringify({ error: 'this session already has 20 uploaded files; delete one first' }), { status: 409 }))
    render(<AgentChatView session={idle} />)
    attach(file('a.pdf'))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('already has 20 uploaded files')
    fireEvent.change(box(), { target: { value: 'hi' } })
    fireEvent.click(sendBtn())
    expect(sentTexts()).toEqual(['hi'])
  })

  it('says a 413 is too large', async () => {
    uploadImpl = () => Promise.resolve(new Response(JSON.stringify({ error: 'file is larger than 25 MiB' }), { status: 413 }))
    render(<AgentChatView session={idle} />)
    attach(file('a.pdf'))
    expect(await screen.findByRole('alert')).toHaveTextContent('larger than 25 MiB')
  })

  it('refuses a file over 25 MiB in the browser without uploading it', async () => {
    render(<AgentChatView session={idle} />)
    attach(bigFile('huge.iso', 25 * 1024 * 1024 + 1))
    expect(await screen.findByRole('alert')).toHaveTextContent(/huge\.iso.*25 MiB/)
    expect(uploads()).toHaveLength(0)
  })

  it('takes at most 10 files per message', async () => {
    render(<AgentChatView session={idle} />)
    attach(...Array.from({ length: 11 }, (_, i) => file(`f${i}.pdf`)))
    await waitFor(() => expect(uploads()).toHaveLength(10))
    expect(chips()).toHaveLength(10)
    expect(screen.getByTestId('attach-notice')).toHaveTextContent('up to 10 files')
  })

  it('removing a finished chip deletes the upload', async () => {
    render(<AgentChatView session={idle} />)
    attach(file('a.pdf'))
    await waitFor(() => expect(chips()[0]).not.toHaveTextContent(/uploading/i))
    fireEvent.click(screen.getByRole('button', { name: 'Remove a.pdf' }))
    await waitFor(() => expect(chips()).toHaveLength(0))
    expect(deletes()).toHaveLength(1)
    expect(deletes()[0][0]).toBe(`/api/sessions/s1/artifacts/${'1'.padStart(32, 'a')}`)
  })

  it('cancelling an upload in flight aborts it', async () => {
    let signal: AbortSignal | null | undefined
    uploadImpl = (_n, init) => { signal = init.signal; return new Promise<Response>(() => {}) }
    render(<AgentChatView session={idle} />)
    attach(file('a.pdf'))
    await waitFor(() => expect(chips()).toHaveLength(1))
    fireEvent.click(screen.getByRole('button', { name: 'Remove a.pdf' }))
    expect(signal?.aborted).toBe(true)
    await waitFor(() => expect(chips()).toHaveLength(0))
    expect(sendBtn()).toBeDisabled()
  })

  it('appends the note exactly once, then clears the chips', async () => {
    sizes = { 'a.pdf': 1258291, 'b.pdf': 800 * 1024 }
    render(<AgentChatView session={idle} />)
    attach(file('a.pdf'), file('b.pdf'))
    await waitFor(() => expect(chips()).toHaveLength(2))
    fireEvent.change(box(), { target: { value: 'compare these' } })
    await waitFor(() => expect(sendBtn()).not.toBeDisabled())
    fireEvent.click(sendBtn())
    expect(sentTexts()).toEqual([`compare these\n\n${NOTE}a.pdf (1.2 MB), b.pdf (800 KB)`])
    expect(chips()).toHaveLength(0)
    fireEvent.change(box(), { target: { value: 'and then?' } })
    fireEvent.click(sendBtn())
    expect(sentTexts()[1]).toBe('and then?')
  })

  it('a message of only files gets a generic line before the note', async () => {
    render(<AgentChatView session={idle} />)
    expect(sendBtn()).toBeDisabled()
    attach(file('a.pdf'))
    await waitFor(() => expect(sendBtn()).not.toBeDisabled())
    fireEvent.click(sendBtn())
    expect(sentTexts()).toEqual([`Please take a look at the attached files.\n\n${NOTE}a.pdf (10 B)`])
  })

  it('keeps the chips when the message could not be sent', async () => {
    vi.mocked(send).mockReturnValueOnce(false)
    render(<AgentChatView session={idle} />)
    attach(file('a.pdf'))
    await waitFor(() => expect(sendBtn()).not.toBeDisabled())
    fireEvent.click(sendBtn())
    expect(chips()).toHaveLength(1)
    expect(screen.getByTestId('send-notice')).toHaveTextContent('not sent')
  })

  it('drag and drop adds files, announcing the drop zone', async () => {
    render(<AgentChatView session={idle} />)
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
    render(<AgentChatView session={idle} />)
    fireEvent.dragEnter(screen.getByTestId('agent-chat'), { dataTransfer: { types: ['text/plain'], files: [] } })
    expect(screen.queryByTestId('drop-zone')).toBeNull()
  })

  it('pasting a file adds it; pasting text does not', async () => {
    render(<AgentChatView session={idle} />)
    fireEvent.paste(box(), { clipboardData: { files: [], types: ['text/plain'], getData: () => 'hello' } })
    expect(chips()).toHaveLength(0)
    fireEvent.paste(box(), { clipboardData: { files: [file('shot.png')], types: ['Files'], getData: () => '' } })
    await waitFor(() => expect(chips()).toHaveLength(1))
    expect(chips()[0]).toHaveTextContent('shot.png')
  })

  it('keeps the chips when you leave the session and come back', async () => {
    const { unmount } = render(<AgentChatView session={idle} />)
    attach(file('a.pdf'))
    await waitFor(() => expect(chips()[0]).not.toHaveTextContent(/uploading/i))
    unmount()
    render(<AgentChatView session={idle} />)
    expect(chips()).toHaveLength(1)
    expect(chips()[0]).toHaveTextContent('a.pdf')
    expect(sendBtn()).not.toBeDisabled()
  })

  it('disables the paperclip while the session is starting', () => {
    render(<AgentChatView session={{ ...idle, status: 'starting' }} />)
    expect(screen.getByRole('button', { name: 'Attach files' })).toBeDisabled()
  })
})

describe('messages with attachments in the transcript', () => {
  it('shows a sent note as file chips, not raw text', async () => {
    useAgentTranscript.getState().ingest(ev(3, 'user_message', {
      text: `compare these\n\n${NOTE}a.pdf (1.2 MB), b.pdf (800 KB)`, source: 'chat',
    }))
    render(<AgentChatView session={idle} />)
    const att = await screen.findByTestId('message-attachments')
    expect(within(att).getAllByRole('listitem').map(li => li.textContent)).toEqual([
      expect.stringContaining('a.pdf'), expect.stringContaining('b.pdf'),
    ])
    expect(att).toHaveTextContent('1.2 MB')
    expect(screen.getByText('compare these')).toBeInTheDocument()
    expect(screen.queryByText(/blerg-runner fetch --all/)).toBeNull()
  })

  it('leaves an ordinary message alone', () => {
    useAgentTranscript.getState().ingest(ev(3, 'user_message', { text: 'Attached files are great', source: 'chat' }))
    render(<AgentChatView session={idle} />)
    expect(screen.getByText('Attached files are great')).toBeInTheDocument()
    expect(screen.queryByTestId('message-attachments')).toBeNull()
  })

  it('a queued message (session running) shows its attachments, and Esc still interrupts', async () => {
    useAgentTranscript.getState().ingest(ev(3, 'user_message', { text: 'go', source: 'chat' }))
    render(<AgentChatView session={running} />)
    attach(file('a.pdf'))
    await waitFor(() => expect(chips()[0]).not.toHaveTextContent(/uploading/i))
    fireEvent.change(box(), { target: { value: 'read this' } })
    fireEvent.click(sendBtn())
    const q = await screen.findByTestId('queued-message')
    expect(within(q).getByTestId('message-attachments')).toHaveTextContent('a.pdf')
    expect(q).toHaveTextContent('read this')
    expect(q).toHaveTextContent(/Queued/)

    // The real event replaces the dimmed bubble once the turn picks the message up.
    act(() => {
      useAgentTranscript.getState().ingest(ev(9, 'user_message', { text: sentTexts()[0], source: 'chat' }))
    })
    await waitFor(() => expect(screen.queryByTestId('queued-message')).toBeNull())

    fireEvent.keyDown(box(), { key: 'Escape' })
    expect(vi.mocked(send).mock.calls.some(c => (c[0] as { type: string }).type === 'interrupt_session')).toBe(true)
  })

  it('shows upload progress on the chip as it goes, then Processing, then the size', async () => {
    let finish!: (r: Response) => void
    let report!: (loaded: number, total: number) => void
    progress.report = cb => { report = cb }
    uploadImpl = () => new Promise<Response>(res => { finish = res })
    sizes['big.pdf'] = 10 * 1024 * 1024
    render(<AgentChatView session={idle} />)
    attach(bigFile('big.pdf', 10 * 1024 * 1024))
    await waitFor(() => expect(chips()).toHaveLength(1))
    expect(within(chips()[0]).getByRole('progressbar', { name: 'Uploading big.pdf' })).toHaveAttribute('aria-valuenow', '0')

    act(() => report(2.5 * 1024 * 1024, 10 * 1024 * 1024))
    await waitFor(() => expect(chips()[0]).toHaveTextContent('Uploading 25% · 2.5 MB of 10.0 MB'))
    expect(within(chips()[0]).getByRole('progressbar')).toHaveAttribute('aria-valuenow', '25')

    act(() => report(10 * 1024 * 1024, 10 * 1024 * 1024))
    await waitFor(() => expect(chips()[0]).toHaveTextContent('Processing'))

    await act(async () => { finish(created('big.pdf')) })
    await waitFor(() => expect(chips()[0]).toHaveTextContent('10.0 MB'))
    expect(within(chips()[0]).queryByRole('progressbar')).toBeNull()
  })

  it('reads the Files list again when an upload finishes and when the panel opens', async () => {
    const listCalls = () => apiFetch.mock.calls.filter(c => c[0] === '/api/sessions/s1/artifacts').length
    render(<AgentChatView session={idle} />)
    await waitFor(() => expect(listCalls()).toBeGreaterThan(0))
    const before = listCalls()

    // an upload leaves no transcript event: the list must be read again once it is done
    attach(file('a.pdf'))
    await waitFor(() => expect(chips()[0]).toHaveTextContent('10 B'))
    await waitFor(() => expect(listCalls()).toBeGreaterThan(before))
    const afterUpload = listCalls()

    // opening the panel reads it again too
    fireEvent.click(screen.getByRole('button', { name: /Files/ }))
    await waitFor(() => expect(listCalls()).toBeGreaterThan(afterUpload))
  })
})
