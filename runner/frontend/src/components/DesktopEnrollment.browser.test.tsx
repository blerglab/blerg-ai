import { act, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { page } from 'vitest/browser'
import { ChatView, useTranscriptStore } from '@blerglab/chat'
import DesktopScopeBoundary from './DesktopScopeBoundary'
import { useSessionStore } from '../hooks/useSessionStore'
import { blergTransport, invalidateChatTransport } from '../lib/chatTransport'
import { makeDaemon, makeSession } from '../test/fixtures'
import * as ws from '../test/wsMock'
import '../index.css'
import '@blerglab/chat/blerg.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => null, ensureFreshToken: async () => true }))
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals() })

it.each([390, 3840])('keeps an enrolled chat mounted without skeleton flashes at %i px, but clears reassigned sources', async width => {
  await page.viewport(width, 900)
  ws.send.mockReturnValue(true)
  vi.stubGlobal('fetch', vi.fn(async () => new Response('[]')))
  useTranscriptStore.setState({ sessions: {} })
  const daemon = makeDaemon({ compatibility: true })
  const session = makeSession({ kind: 'agent', daemon_id: daemon.id, desktop_compatibility: true })
  useSessionStore.setState({ daemons: [daemon], sessions: [session] })
  function Chat() {
    const current = useSessionStore(state => state.sessions[0])
    return <ChatView session={current.id} transport={blergTransport()} meta={current} />
  }
  const { container } = render(<div style={{ height: '100vh' }}><DesktopScopeBoundary><Chat /></DesktopScopeBoundary></div>)
  act(() => {
    ws.emit({ type: 'agent_event', session_id: session.id, client_event_id: 'prose', seq: 1, ts: '2026-01-01T00:00:00Z', kind: 'user_message', payload: { text: 'Keep this conversation visible', source: 'chat' } })
    ws.emit({ type: 'agent_event', session_id: session.id, client_event_id: 'caps', seq: 2, ts: '2026-01-01T00:00:01Z', kind: 'capabilities', payload: { cwd: '/workspace', engine: 'codex', groups: [] } })
    ws.emit({ type: 'agent_events_replay_done', session_id: session.id, last_seq: 2, has_more: false })
  })
  await waitFor(() => expect(screen.queryByTestId('history-loading')).toBeNull())
  const prose = screen.getByText('Keep this conversation visible')
  const transport = blergTransport()
  const transcript = useTranscriptStore.getState().sessions[session.id]
  // Observe additions as well as the final DOM, to catch a transient skeleton.
  const flashes: Node[] = []
  const observer = new MutationObserver(records => {
    for (const record of records) for (const node of record.addedNodes) {
      if (node instanceof Element && (node.matches('[data-testid="history-loading"]') || node.querySelector('[data-testid="history-loading"]'))) flashes.push(node)
    }
  })
  observer.observe(container, { childList: true, subtree: true })
  try {
    await act(async () => ws.emit({ type: 'desktop_sources_changed', desktop_id: daemon.id }))
    await act(async () => ws.emit({ type: 'initial_state', daemons: [{ ...daemon, compatibility: false }], sessions: [{ ...session, desktop_compatibility: false }] }))
    await act(async () => ws.emit({ type: 'session_started', session: { ...session, desktop_compatibility: false } }))
    await act(async () => ws.emit({ type: 'daemon_connected', daemon: { ...daemon, compatibility: false } }))
    expect(flashes).toEqual([])
    expect(screen.getByText('Keep this conversation visible')).toBe(prose)
    expect(blergTransport()).toBe(transport)
    expect(useTranscriptStore.getState().sessions[session.id]).toBe(transcript)
  } finally { observer.disconnect() }
  act(() => ws.emit({ type: 'desktop_ownership_changed', desktop_id: daemon.id, ownership_revision: 3 }))
  expect(useSessionStore.getState().sessions[0].project_path).toBe('')
  expect(useTranscriptStore.getState().sessions[session.id].events.map(event => event.kind)).not.toContain('capabilities')
  expect(blergTransport()).not.toBe(transport)
})
