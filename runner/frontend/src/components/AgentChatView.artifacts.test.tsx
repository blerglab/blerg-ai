import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import AgentChatView from './AgentChatView'
import { useAgentTranscript } from '../hooks/useAgentTranscript'
import { buildTimeline, isVisibleEvent } from '../lib/toolGroups'
import type { AgentEvent, SessionInfo } from '../types'

vi.mock('../ws', () => ({
  send: vi.fn(),
  onMessage: vi.fn(() => () => {}),
  onOpen: vi.fn(() => () => {}),
}))

const apiFetch = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

const session: SessionInfo = {
  id: 's1', daemon_id: 'd1', status: 'idle', project_path: '/r/proj', repo: 'proj', title: 'Test',
  model: 'claude-sonnet-5', started_at: '2026-07-26T00:00:00Z', kind: 'agent',
}

type Listed = { id: string; name: string; size: number; content_type: string; view: string; version?: number; latest_version?: number; created_at?: string }
const report: Listed = { id: 'a1', name: 'report.md', size: 2048, content_type: 'text/markdown', view: 'markdown' }
const memo: Listed = { id: 'a2', name: 'memo.docx', size: 245760, content_type: 'application/x', view: 'none' }

function artifactEvent(payload: Listed, seq: number): AgentEvent {
  return {
    type: 'agent_event', session_id: 's1', client_event_id: `e${seq}`, seq, ts: '2026-07-26T00:00:00Z',
    kind: 'artifact', payload,
  }
}

let listed: Listed[]
let rawBody: string

beforeEach(() => {
  vi.clearAllMocks()
  window.history.replaceState(null, '', '/sessions/s1')
  useAgentTranscript.setState({ sessions: {} })
  listed = []
  rawBody = '# Report\n\nall good'
  apiFetch.mockReset()
  apiFetch.mockImplementation((url: string) => {
    if (url === '/api/sessions/s1/artifacts') return Promise.resolve(new Response(JSON.stringify({ artifacts: listed })))
    if (url.endsWith('/raw')) return Promise.resolve(new Response(rawBody))
    if (url.endsWith('/download')) return Promise.resolve(new Response('bytes'))
    return Promise.resolve(new Response('{}', { status: 404 }))
  })
  Object.assign(URL, { createObjectURL: vi.fn(() => 'blob:x'), revokeObjectURL: vi.fn() })
})

afterEach(() => {
  window.history.replaceState(null, '', '/')
  vi.restoreAllMocks()
})

function ingest(...evs: AgentEvent[]) {
  act(() => { for (const e of evs) useAgentTranscript.getState().ingest(e) })
}

describe('artifact cards in the chat', () => {
  it('draws a card: name, size, Download and (for a viewable type) View', async () => {
    ingest(artifactEvent(report, 1))
    render(<AgentChatView session={session} />)
    const card = await screen.findByTestId('artifact-card')
    expect(card).toHaveTextContent('📎')
    expect(card).toHaveTextContent('report.md')
    expect(card).toHaveTextContent('2.0 KB')
    expect(within(card).getByRole('button', { name: 'Download' })).toBeInTheDocument()
    expect(within(card).getByRole('button', { name: 'View' })).toBeInTheDocument()
  })

  it('has no View for a download-only file', async () => {
    ingest(artifactEvent(memo, 1))
    render(<AgentChatView session={session} />)
    const card = await screen.findByTestId('artifact-card')
    expect(within(card).getByRole('button', { name: 'Download' })).toBeInTheDocument()
    expect(within(card).queryByRole('button', { name: 'View' })).toBeNull()
  })

  it('draws a card in the full (not compact) view too', async () => {
    localStorage.setItem('blerg.agent.compactTools', '0')
    ingest(artifactEvent(report, 1))
    render(<AgentChatView session={session} />)
    expect(await screen.findByTestId('artifact-card')).toBeInTheDocument()
    localStorage.removeItem('blerg.agent.compactTools')
  })

  it('View opens the viewer on that file', async () => {
    ingest(artifactEvent(report, 1))
    render(<AgentChatView session={session} />)
    const card = await screen.findByTestId('artifact-card')
    fireEvent.click(within(card).getByRole('button', { name: 'View' }))
    const dialog = await screen.findByRole('dialog', { name: /report\.md/ })
    expect(await within(dialog).findByRole('heading', { name: 'Report' })).toBeInTheDocument()
    expect(apiFetch).toHaveBeenCalledWith('/api/sessions/s1/artifacts/a1/raw')
  })

  it('Download fetches the download route with apiFetch', async () => {
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    ingest(artifactEvent(report, 1))
    render(<AgentChatView session={session} />)
    const card = await screen.findByTestId('artifact-card')
    fireEvent.click(within(card).getByRole('button', { name: 'Download' }))
    await waitFor(() => expect(click).toHaveBeenCalled())
    expect(apiFetch).toHaveBeenCalledWith('/api/sessions/s1/artifacts/a1/download')
  })

  it('a file deleted since says so instead of failing silently', async () => {
    apiFetch.mockImplementation((url: string) =>
      Promise.resolve(url === '/api/sessions/s1/artifacts'
        ? new Response(JSON.stringify({ artifacts: [] }))
        : new Response('{}', { status: 404 })))
    ingest(artifactEvent(report, 1))
    render(<AgentChatView session={session} />)
    const card = await screen.findByTestId('artifact-card')
    fireEvent.click(within(card).getByRole('button', { name: 'Download' }))
    expect(await within(card).findByRole('alert')).toHaveTextContent('no longer available')
  })

  it('is a card kind, so it shows up in the compact timeline', () => {
    const ev = artifactEvent(report, 1)
    expect(isVisibleEvent(ev)).toBe(true)
    expect(buildTimeline([ev])).toEqual([{ type: 'event', ev }])
  })
})

describe('the Files panel', () => {
  it('counts the session’s files on the toolbar button', async () => {
    listed = [report, memo]
    render(<AgentChatView session={session} />)
    const btn = await screen.findByRole('button', { name: /Files \(2\)/ })
    expect(btn).toBeInTheDocument()
  })

  it('shows Files (0) and an empty state when there are none', async () => {
    render(<AgentChatView session={session} />)
    fireEvent.click(await screen.findByRole('button', { name: /Files \(0\)/ }))
    expect(await screen.findByTestId('artifacts-empty')).toBeInTheDocument()
  })

  it('opens a list, newest first, and opens a file from it', async () => {
    listed = [report, memo]
    render(<AgentChatView session={session} />)
    fireEvent.click(await screen.findByRole('button', { name: /Files \(2\)/ }))
    const panel = await screen.findByRole('dialog', { name: /Files/ })
    expect(within(panel).getAllByTestId('artifact-name').map(n => n.textContent)).toEqual(['report.md', 'memo.docx'])
    fireEvent.click(within(within(panel).getAllByTestId('artifact-row')[0]).getByRole('button', { name: 'View' }))
    expect(await screen.findByRole('dialog', { name: /report\.md/ })).toBeInTheDocument()
  })

  it('reloads the list when a new artifact event arrives', async () => {
    render(<AgentChatView session={session} />)
    await screen.findByRole('button', { name: /Files \(0\)/ })
    listed = [report]
    ingest(artifactEvent(report, 1))
    expect(await screen.findByRole('button', { name: /Files \(1\)/ })).toBeInTheDocument()
  })
})

describe('the ?artifact= link the CLI prints', () => {
  it('opens that artifact when the session page loads', async () => {
    window.history.replaceState(null, '', '/sessions/s1?artifact=a1')
    listed = [report]
    render(<AgentChatView session={session} />)
    const dialog = await screen.findByRole('dialog', { name: /report\.md/ })
    expect(await within(dialog).findByRole('heading', { name: 'Report' })).toBeInTheDocument()
    expect(apiFetch).toHaveBeenCalledWith('/api/sessions/s1/artifacts/a1/raw')
  })

  it('takes the parameter out of the address when the viewer is closed', async () => {
    window.history.replaceState(null, '', '/sessions/s1?artifact=a1&x=1')
    listed = [report]
    render(<AgentChatView session={session} />)
    const dialog = await screen.findByRole('dialog', { name: /report\.md/ })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Close' }))
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(window.location.search).toBe('?x=1')
  })

  it('says so when the artifact no longer exists', async () => {
    window.history.replaceState(null, '', '/sessions/s1?artifact=gone')
    listed = [report]
    render(<AgentChatView session={session} />)
    expect(await screen.findByTestId('artifact-missing')).toHaveTextContent(/no longer exists/)
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('does nothing without the parameter', async () => {
    listed = [report]
    render(<AgentChatView session={session} />)
    await screen.findByRole('button', { name: /Files \(1\)/ })
    expect(screen.queryByRole('dialog')).toBeNull()
  })
})

describe('versions in the chat', () => {
  const v = (id: string, version: number, latest = 3): Listed => ({ ...report, id, version, latest_version: latest, created_at: `2026-09-30T1${version}:00:00Z` })

  it('puts a version badge on the card of a later version', async () => {
    ingest(artifactEvent({ ...report, version: 3 }, 1))
    render(<AgentChatView session={session} />)
    const card = await screen.findByTestId('artifact-card')
    expect(card).toHaveTextContent('report.md · v3 · 2.0 KB')
  })

  it('leaves the badge off a version 1 whose name has no other versions', async () => {
    ingest(artifactEvent({ ...report, version: 1 }, 1))
    listed = [{ ...report, version: 1, latest_version: 1 }]
    render(<AgentChatView session={session} />)
    const card = await screen.findByTestId('artifact-card')
    await screen.findByRole('button', { name: /Files \(1\)/ })
    expect(card).not.toHaveTextContent(/\bv1\b/)
    expect(card.querySelector('.artifact-card-version')).toBeNull()
  })

  it('shows the badge on a version 1 card once its file has later versions', async () => {
    ingest(artifactEvent({ ...report, version: 1 }, 1))
    listed = [v('a2', 2, 2), { ...report, version: 1, latest_version: 2 }]
    render(<AgentChatView session={session} />)
    const card = await screen.findByTestId('artifact-card')
    await waitFor(() => expect(card).toHaveTextContent('report.md · v1 · 2.0 KB'))
  })

  it('the toolbar counts files, not versions, and the viewer of an older version links to the latest', async () => {
    listed = [v('a3', 3), v('a2', 2), v('a1', 1)]
    window.history.replaceState(null, '', '/sessions/s1?artifact=a2')
    render(<AgentChatView session={session} />)
    expect(await screen.findByRole('button', { name: /Files \(1\)/ })).toBeInTheDocument()
    const dialog = await screen.findByRole('dialog', { name: 'report.md · v2 of 3' })
    expect(within(dialog).getByTestId('artifact-older')).toBeInTheDocument()
    expect(apiFetch).toHaveBeenCalledWith('/api/sessions/s1/artifacts/a2/raw')
    fireEvent.click(within(dialog).getByRole('button', { name: 'View latest' }))
    expect(await screen.findByRole('dialog', { name: 'report.md · v3 of 3' })).toBeInTheDocument()
    expect(apiFetch).toHaveBeenCalledWith('/api/sessions/s1/artifacts/a3/raw')
  })

  it('the ?artifact= link works for the latest version too', async () => {
    listed = [v('a3', 3), v('a2', 2)]
    window.history.replaceState(null, '', '/sessions/s1?artifact=a3')
    render(<AgentChatView session={session} />)
    expect(await screen.findByRole('dialog', { name: 'report.md · v3 of 3' })).toBeInTheDocument()
    expect(screen.queryByTestId('artifact-older')).toBeNull()
  })
})
