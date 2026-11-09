// Real SessionDetail + Runner process + owner socket. Provider data is simulated.
import {fireEvent, render, waitFor} from '@testing-library/react'
import {afterEach, beforeEach, expect, it, vi} from 'vitest'
import {commands, page} from 'vitest/browser'
import {MemoryRouter, Route, Routes} from 'react-router-dom'
import {useTranscriptStore, type AgentEvent, type DatasetSample} from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import CommandPaletteProvider from './CommandPalette'
import * as wsMock from '../test/wsMock'
import type {ServerMessage} from '../types'
import {useSessionStore} from '../hooks/useSessionStore'
import {makeDaemon, makeSession} from '../test/fixtures'
import {invalidateChatTransport} from '../lib/chatTransport'
import '../index.css'
import '@blerglab/chat/blerg.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({sub: 'person-a', exp: 9999999999}))}.x`, ensureFreshToken: async () => true}))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({apiFetch}))
const realFetch = globalThis.fetch.bind(globalThis)
const json = (body: unknown) => new Response(JSON.stringify(body), {headers: {'Content-Type': 'application/json'}})
const binding = {kind: 'dataset', name: 'blerg.mcp', parameters: {binding: '00000000-0000-4000-8000-000000000514'}}
const source = JSON.stringify({format: 'blerg-dashboard', version: 1, title: 'Market quotes · simulated', cards: [
  {id: 'price', kind: 'stat', title: 'NVDA latest quote', span: 1, data: binding, view: {value: 'value', selection: 'latest', unit: 'USD'}},
  {id: 'unsupported', kind: 'stat', title: 'Unsupported live trend', span: 1, data: binding, view: {value: 'value', selection: 'latest', sparkline: 'value'}},
  {id: 'band', kind: 'gauge', title: 'NVDA price band', span: 1, data: binding, view: {value: 'value', selection: 'latest', unit: 'USD', min: 0, max: 300}},
]})
const liveId = 'live00000000000000000000000514'
const liveFile = {id: liveId, name: 'market.dash.json', size: source.length, content_type: 'application/json', view: 'json', origin: 'agent', version: 1, live: true, updated_at: '2026-11-05T12:00:00Z'}
const shotDir = (import.meta.env as Record<string, string | undefined>).VITE_LIVE_FILE_SHOTS_DIR
beforeEach(() => {
  localStorage.clear()
  useSessionStore.setState({daemons: [makeDaemon()], sessions: []})
  apiFetch.mockReset().mockImplementation(async (url: string) => {
    if (url.endsWith('/topics')) return json({topics: [], unmarked: null, strokes: []})
    if (url.includes('/live')) return json({can_release_prompt: false})
    if (url.includes('/workspace?')) return json({result: {ok: true, launch_generation: 'launch', generation: 1, entries: [], truncated: false}, members: [], runtime: 'host'})
    if (url.endsWith('/permissions')) return json({can_grant: false, reason: '', grants: [], mcp_tools: [], boards: []})
    return json({})
  })
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
    if (url.startsWith('http')) return realFetch(input, init)
    if (url.endsWith('/artifacts/' + liveId + '/raw')) return new Response(source)
    if (url.endsWith('/artifacts')) return json({artifacts: [liveFile], limits: {}, can_delete: false})
    return json([])
  }))
})
afterEach(() => {invalidateChatTransport(); vi.unstubAllGlobals(); vi.restoreAllMocks(); document.documentElement.removeAttribute('data-theme')})

for (const [width, pane, theme] of [[390, 390, 'dark'], [3840, 1024, 'dark'], [3840, 3840, 'dark'], [3840, 3840, 'colorblind-dark']] as const) {
  it(`updates and purges owner-private MCP scalars at ${width}px pane ${pane} ${theme}`, async () => {
    await page.viewport(width, width === 390 ? 844 : 2160)
    const fixture = await commands.startDatasetFixture(location.origin, true)
    const sid = fixture.sessionId
    document.documentElement.setAttribute('data-theme', theme)
    useSessionStore.setState({sessions: [makeSession({id: sid, kind: 'agent', status: 'idle', title: 'Market dashboard'})]})
    const socket = new WebSocket(fixture.url.replace(/^http/, 'ws') + '/ws/browser', ['bearer', fixture.token])
    const received: string[] = []
    const frames: DatasetSample[] = []
    socket.onmessage = event => {const message = JSON.parse(String(event.data)); received.push(message.type + ':' + (message.cause ?? '')); if (message.type === 'dataset_delta') {frames.push(message as DatasetSample); wsMock.emit(message as ServerMessage)}}
    await new Promise<void>((resolve, reject) => {socket.onopen = () => resolve(); socket.onerror = () => reject(new Error('fixture socket failed'))})
    wsMock.send.mockImplementation(message => {if (['subscribe_dataset', 'unsubscribe_dataset', 'dataset_heartbeat', 'dataset_diagnostic'].includes(message.type)) socket.send(JSON.stringify(message)); return true})
    useTranscriptStore.setState({sessions: {}})
    useTranscriptStore.getState().ingest({session_id: sid, client_event_id: 'mcp-artifact', seq: 1, ts: '2026-11-05T12:00:00Z', kind: 'artifact', payload: liveFile} as AgentEvent)
    useTranscriptStore.getState().ingestReplayDone(sid, {lastSeq: 1, hasMore: false})
    const view = render(<MemoryRouter initialEntries={['/sessions/' + sid]}><CommandPaletteProvider><div style={{display: 'flex', height: '100vh', width: pane, maxWidth: '100%', transform: 'translateZ(0)'}}><div style={{flex: 1, minWidth: 0}}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div></div></CommandPaletteProvider></MemoryRouter>)
    try {
      const rail = view.container.querySelector('[data-testid="rail-toggle"][aria-expanded="true"]')
      if (rail) fireEvent.click(rail)
      const card = await waitFor(() => {const el = document.querySelector('[data-testid="artifact-card"]'); if (!el) throw new Error('artifact missing'); return el})
      fireEvent.click([...card.querySelectorAll('button')].find(button => button.textContent === 'View')!)
      const tab = await waitFor(() => {const el = document.querySelector('.hosted-view:not([hidden]) .dashboard-block'); if (!el) throw new Error('dashboard missing'); return el})
      const stat = tab.querySelector('[aria-label="NVDA latest quote"]')!
      const gauge = tab.querySelector('[aria-label="NVDA price band"]')!
      await waitFor(() => expect(stat.querySelector('.dashboard-number'), JSON.stringify({sent: wsMock.send.mock.calls.map(c => c[0].type), received})).toHaveTextContent('185.25'), {timeout: 10000})
      expect(tab.querySelector('[aria-label="Unsupported live trend"]')).toHaveTextContent('unsupported-live · Use native stat or gauge without sparklines')
      expect(wsMock.send.mock.calls.filter(([message]) => message.type === 'subscribe_dataset')).toHaveLength(2)
      expect(view.container).toHaveTextContent('Dashboard data exports are disabled.')
      expect(view.container.querySelector('[aria-label="Download SVG"]')).toBeNull()
      expect(view.container.querySelector('[aria-label="Download PNG"]')).toBeNull()
      expect(JSON.parse(source).cards[0].data).toEqual(binding)
      expect(source).not.toContain('185.25')
      const number = stat.querySelector('.dashboard-number'), meter = gauge.querySelector('[role="meter"]')
      await realFetch(fixture.url + '/fixture/mcp/change', {method: 'POST', headers: {Authorization: 'Bearer ' + fixture.token}})
      await waitFor(() => expect(number).toHaveTextContent('189.75'), {timeout: 10000})
      expect(stat.querySelector('.dashboard-number')).toBe(number)
      expect(gauge.querySelector('[role="meter"]')).toBe(meter)
      // Inject a gap in a real Runner frame; recovery goes through the shipped
      // transport, authenticated socket and current-authority full snapshot.
      const old = frames.at(-1)!
      const beforeRecovery = frames.length
      wsMock.emit({...old, snapshot: false, base_sequence: old.sequence + 1, sequence: old.sequence + 2} as unknown as ServerMessage)
      await waitFor(() => expect(frames.slice(beforeRecovery).some(frame => frame.type === 'dataset_delta' && frame.snapshot && frame.subscription_id !== old.subscription_id)).toBe(true), {timeout: 10000})
      wsMock.emit({...old, snapshot: true, sequence: old.sequence + 10, upserts: [{key: 'quote', value: 999}]} as unknown as ServerMessage)
      expect(number).toHaveTextContent('189.75')
      wsMock.setSocketOpen(false)
      await waitFor(() => expect(stat).toHaveTextContent('Disconnected'))
      const beforeReconnect = frames.length
      wsMock.setSocketOpen(true); wsMock.flushOpen()
      await waitFor(() => expect(frames.length).toBeGreaterThan(beforeReconnect), {timeout: 10000})
      await waitFor(() => expect(stat).toHaveTextContent('Live'))
      expect(stat.querySelector('.dashboard-number')).toBe(number)
      expect(tab.scrollWidth).toBeLessThanOrEqual(tab.clientWidth)
      const fetchedAt = stat.querySelector('time')?.getAttribute('datetime')
      for (const [cause, label] of [['provider_rate_limited', 'Rate limited'], ['mapping_failed', 'Invalid sample'], ['execution_unavailable', 'Gateway unavailable'], ['authority_unavailable', 'Authority unavailable']]) {
        expect((await realFetch(fixture.url + '/fixture/mcp/failure?cause=' + cause, {method: 'POST', headers: {Authorization: 'Bearer ' + fixture.token}})).ok).toBe(true)
        await waitFor(() => expect(stat).toHaveTextContent(label), {timeout: 10000})
        expect(number).toHaveTextContent('189.75')
        expect(stat).toHaveTextContent('Market closed')
        expect(stat).toHaveTextContent('Entitlement unknown')
        expect(stat).toHaveTextContent('Retry')
        expect(stat.querySelector('time')).toHaveAttribute('datetime', fetchedAt!)
      }
      if (shotDir) {
        const frame = document.createElement('div'); frame.style.cssText = 'position:fixed;inset:0;width:100vw;height:100vh;pointer-events:none'; document.body.append(frame)
        try {await page.screenshot({element: frame, path: `${shotDir}/mcp-${width}-pane-${pane}-${theme}.png`})} finally {frame.remove()}
      }
      expect((await realFetch(fixture.url + '/fixture/mcp/expire', {method: 'POST', headers: {Authorization: 'Bearer ' + fixture.token}})).ok).toBe(true)
      await waitFor(() => expect(tab.textContent).not.toContain('189.75'), {timeout: 10000})
      await waitFor(() => expect(stat).toHaveTextContent('Data expired'), {timeout: 10000})
      expect(gauge.querySelector('[role="meter"]')).not.toHaveAttribute('aria-valuenow')
      if (shotDir) {
        const frame = document.createElement('div'); frame.style.cssText = 'position:fixed;inset:0;width:100vw;height:100vh;pointer-events:none'; document.body.append(frame)
        try {await page.screenshot({element: frame, path: `${shotDir}/mcp-expired-${width}-pane-${pane}-${theme}.png`})} finally {frame.remove()}
      }
      expect((await realFetch(fixture.url + '/fixture/mcp/recover', {method: 'POST', headers: {Authorization: 'Bearer ' + fixture.token}})).ok).toBe(true)
      await waitFor(() => expect(number).toHaveTextContent('189.75'), {timeout: 10000})
      await realFetch(fixture.url + '/fixture/mcp/revoke', {method: 'POST', headers: {Authorization: 'Bearer ' + fixture.token}})
      await waitFor(() => expect(tab.textContent).not.toContain('189.75'), {timeout: 10000})
    } finally {view.unmount(); socket.close(); wsMock.send.mockReset(); await commands.stopDatasetFixture(fixture.token)}
  }, 120_000)
}
