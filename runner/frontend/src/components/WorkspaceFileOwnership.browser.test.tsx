import { act, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { page, userEvent } from 'vitest/browser'
import { ChatView, useTranscriptStore } from '@blerglab/chat'
import { blergTransport, invalidateChatTransport } from '../lib/chatTransport'
import { makeSession } from '../test/fixtures'
import * as ws from '../test/wsMock'
import '../index.css'
import '@blerglab/chat/blerg.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => null, ensureFreshToken: async () => true }))
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals() })

for (const width of [390, 3840]) {
  it.each([
    ['desktop_ownership', 'Your desktops'],
    ['session_ownership', 'Ask the session owner'],
  ])(`explains %s in the file viewer at ${width} px`, async (code, guidance) => {
    await page.viewport(width, 900)
    localStorage.clear()
    ws.send.mockReturnValue(true)
    useTranscriptStore.setState({ sessions: {} })
    vi.stubGlobal('fetch', vi.fn(async (url: string) => String(url).includes('/workspace-file?')
      ? new Response(JSON.stringify({ code, error: 'private internal detail', error_id: 'ownership-file' }), { status: 403 })
      : new Response('[]')))
    const session = makeSession({ kind: 'agent', status: 'idle' })
    render(<div style={{ height: '100vh' }}><ChatView session={session.id} transport={blergTransport()} meta={session} canOpenWorkspaceFiles /></div>)
    act(() => {
      ws.emit({ type: 'agent_event', session_id: session.id, client_event_id: 'report', seq: 1, ts: '2026-01-01T00:00:00Z', kind: 'assistant_text', payload: { text: 'Open `.agent-notes/report.md`.', done: true } })
      ws.emit({ type: 'agent_events_replay_done', session_id: session.id, last_seq: 1, has_more: false })
    })
    await waitFor(() => expect(screen.queryByTestId('history-loading')).toBeNull())
    await userEvent.click(screen.getByTitle('Open from the session’s folder'))
    const error = await screen.findByTestId('workspace-error')
    expect(error.textContent).toContain(guidance)
    expect(error.textContent).toContain('ownership-file')
    expect(error.textContent).not.toContain('Not found')
    expect(error.textContent).not.toContain('private internal detail')
    const rect = error.getBoundingClientRect()
    expect(rect.width).toBeGreaterThan(0)
    expect(rect.left).toBeGreaterThanOrEqual(0)
    expect(rect.right).toBeLessThanOrEqual(width)
  })
}
