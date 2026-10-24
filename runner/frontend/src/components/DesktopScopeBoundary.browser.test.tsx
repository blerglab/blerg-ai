import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { page } from 'vitest/browser'
import { useTranscriptStore } from '@blerglab/chat'
import DesktopScopeBoundary from './DesktopScopeBoundary'
import { useCapabilities } from '../hooks/useCapabilities'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import * as wsMock from '../test/wsMock'
vi.mock('../ws', () => import('../test/wsMock'))
const noEvents: never[] = []
function Reader() {
  const events = useTranscriptStore(state => state.sessions.cached?.events) ?? noEvents
  const report = useCapabilities('cached', events)
  return <><output>{report?.payload.cwd ?? 'Source unavailable'}</output><p>{(events.find(e => e.kind === 'user_message')?.payload as { text?: string } | undefined)?.text}</p></>
}
afterEach(() => { cleanup(); vi.unstubAllGlobals() })
for (const type of ['desktop_ownership_changed', 'desktop_inventory_invalidated'] as const) {
  it.each([390, 3840])(`remounts real capability reader safely on ${type} at %ipx`, async width => {
    await page.viewport(width, 900)
    useTranscriptStore.setState({ sessions: {} })
    useSessionStore.setState({ daemons: [makeDaemon({ id: 'desktop-a' })], sessions: [makeSession({ id: 'cached', daemon_id: 'desktop-a' })] })
    const base = { type: 'agent_event' as const, session_id: 'cached', ts: '2026-01-01T00:00:00Z' }
    useTranscriptStore.getState().ingest({ ...base, client_event_id: 'user', seq: 1, kind: 'user_message', payload: { text: 'Keep my shared prose' } })
    const capabilities = { cwd: '/private-old-owner', engine: 'codex', groups: [] }
    useTranscriptStore.getState().ingest({ ...base, client_event_id: 'caps', seq: 2, kind: 'capabilities', payload: capabilities })
    const fetcher = vi.fn(async () => new Response(JSON.stringify({ capabilities, seq: 3 })))
    vi.stubGlobal('fetch', fetcher)
    render(<DesktopScopeBoundary><Reader /></DesktopScopeBoundary>)
    await waitFor(() => expect(fetcher).toHaveBeenCalledOnce())
    expect(screen.getByText('/private-old-owner')).toBeTruthy()
    fetcher.mockImplementation(async () => new Response(JSON.stringify({ capabilities: null })))
    act(() => wsMock.emit(type === 'desktop_ownership_changed' ? { type, desktop_id: 'desktop-a', ownership_revision: 2 } : { type }))
    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2))
    expect(screen.queryByText('/private-old-owner')).toBeNull()
    expect(screen.getByText('Source unavailable')).toBeTruthy()
    expect(screen.getByText('Keep my shared prose')).toBeTruthy()
  })
}
