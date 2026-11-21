// BLG-512: real Runner process → socket delta → retained scalar nodes,
// mounted through SessionDetail with the production viewer and theme.
import { fireEvent, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it } from 'vitest'
import { vi } from 'vitest'
import { commands, page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import CommandPaletteProvider from './CommandPalette'
import * as wsMock from '../test/wsMock'
import type {ServerMessage} from '../types'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { invalidateChatTransport } from '../lib/chatTransport'
import '../index.css'
import '@blerglab/chat/blerg.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

const shotDir = (import.meta.env as Record<string, string | undefined>).VITE_LIVE_FILE_SHOTS_DIR
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })

const LIVE_ID = 'live00000000000000000000000001'
import dashboard from '../test/fixtures/charts/telemetry.dash.json'
const liveDashboard = {...dashboard, title: 'Live workspace telemetry', cards: dashboard.cards.map(card => card.id === 'workers'
  ? {...card, data: {kind: 'dataset', name: 'blerg.workers', parameters: {}}, view: {value: 'running', selection: 'latest'}}
  : card.id === 'disk'
    ? {...card, title: 'Gate wall time', data: {kind: 'dataset', name: 'blerg.gates.durations', parameters: {suite: 'total'}}, view: {value: 'seconds', selection: 'latest', unit: 's'}}
    : card.id === 'codex'
    ? {...card, data: {kind: 'dataset', name: 'blerg.engine.usage', parameters: {engine: 'codex'}}, view: {value: 'percent', selection: 'latest', unit: '%', min: 0, max: 100}}
    : card)}
const chartSource = JSON.stringify(liveDashboard)
let sessionId = 'sess-a'
const realFetch = globalThis.fetch.bind(globalThis)
let source = chartSource
const liveFile = { id: LIVE_ID, name: 'telemetry.dash.json', size: chartSource.length, content_type: 'application/json', view: 'json', origin: 'agent', version: 1, live: true, updated_at: new Date().toISOString() }

beforeEach(() => {
  source = chartSource
  liveFile.updated_at = new Date().toISOString()
  localStorage.clear()
  document.documentElement.setAttribute('data-theme', 'dark')
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ id: 'sess-a', kind: 'agent', status: 'idle', title: 'Release notes' })] })
  apiFetch.mockReset().mockImplementation(async (url: string) => {
    if (url.endsWith('/topics')) return json({ topics: [], unmarked: null, strokes: [] })
    if (url.includes('/live')) return json({ can_release_prompt: false })
    if (url.includes('/workspace?')) return json({ result: { ok: true, launch_generation: 'launch', generation: 1, entries: [], truncated: false }, members: [], runtime: 'host' })
    if (url.endsWith('/permissions')) return json({ can_grant: false, reason: '', grants: [], mcp_tools: [], boards: [] })
    return json({})
  })
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
    if (url.startsWith('http')) return realFetch(input, init)
    if (url.endsWith('/artifacts/' + LIVE_ID + '/raw')) return new Response(source)
    if (url.endsWith('/artifacts')) return json({ artifacts: [liveFile], limits: {}, can_delete: false })
    return json([])
  }))
})
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals(); vi.restoreAllMocks(); document.documentElement.removeAttribute('data-theme') })

function mount(pane: number) {
  return render(
    <MemoryRouter initialEntries={['/sessions/' + sessionId]}>
      <CommandPaletteProvider>
        <div style={{ display: 'flex', height: '100vh', width: pane, maxWidth: '100%', transform: 'translateZ(0)' }}>
          <div style={{ flex: 1, minWidth: 0 }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div>
        </div>
      </CommandPaletteProvider>
    </MemoryRouter>,
  )
}

function seedLiveArtifact() {
  useTranscriptStore.setState({ sessions: {} })
  const transcript = useTranscriptStore.getState()
  transcript.ingest({ session_id: sessionId, client_event_id: 'f1', seq: 1, ts: '2026-11-03T00:00:00Z', kind: 'artifact', payload: liveFile } as AgentEvent)
  transcript.ingestReplayDone(sessionId, { lastSeq: 1, hasMore: false })
}


for (const [width, pane, theme] of [[390, 390, 'dark'], [520, 520, 'dark'], [3840, 1024, 'dark'], [3840, 3840, 'dark'], [3840, 3840, 'colorblind-dark']] as const) {
  it(`updates live scalars through a real Runner process at ${width}px pane ${pane} ${theme}`, async () => {
    await page.viewport(width, width === 390 ? 844 : 2160)
    await commands.setUsageReducedMotion(false)
    const fixture = await commands.startDatasetFixture(location.origin)
    sessionId = fixture.sessionId
    useSessionStore.setState({sessions: [makeSession({id: sessionId, kind: 'agent', status: 'idle', title: 'Live telemetry'})]})
    const socket = new WebSocket(fixture.url.replace(/^http/, 'ws') + '/ws/browser', ['bearer', fixture.token])
    const deltas: {snapshot: boolean; base_sequence: number; sequence: number}[] = []
    socket.onmessage = event => {
      const message = JSON.parse(String(event.data))
      if (message.type === 'dataset_delta') {
        deltas.push(message)
        wsMock.emit(message as ServerMessage)
      }
    }
    await new Promise<void>((resolve, reject) => {socket.onopen = () => resolve(); socket.onerror = () => reject(new Error('fixture socket failed'))})
    wsMock.send.mockImplementation(message => {
      if (['subscribe_dataset', 'unsubscribe_dataset', 'dataset_heartbeat', 'dataset_diagnostic'].includes(message.type)) socket.send(JSON.stringify(message))
      return true
    })
    const capture = async (name: string) => {
      if (!shotDir || (name !== 'dashboard' && pane !== 1024)) return
      const frame = document.createElement('div')
      frame.style.cssText = 'position:fixed;inset:0;width:100vw;height:100vh;pointer-events:none'
      document.body.append(frame)
      try {await page.screenshot({element: frame, path: `${shotDir}/live-${name}-${width}-pane-${pane}-${theme}.png`})}
      finally {frame.remove()}
    }
    let view: ReturnType<typeof mount> | undefined
    try {
      document.documentElement.setAttribute('data-theme', theme)
      seedLiveArtifact()
      view = mount(pane)
      const rail = view.container.querySelector('[data-testid="rail-toggle"][aria-expanded="true"]')
      if (rail) fireEvent.click(rail)
      const card = await waitFor(() => {
        const el = document.querySelector('[data-testid="artifact-card"]')
        if (!el) throw new Error('no artifact card')
        return el as HTMLElement
      })
      fireEvent.click([...card.querySelectorAll('button')].find(b => b.textContent === 'View')!)
      const tab = await waitFor(() => {
        const el = document.querySelector('.hosted-view:not([hidden]) .dashboard-block')
        if (!el) throw new Error('no dashboard tab')
        if (el.querySelectorAll('.dashboard-card').length !== 9) throw new Error('dashboard not ready')
        return el as HTMLElement
      })
      const worker = tab.querySelector('[aria-label="Workers running"]')!
      const gate = tab.querySelector('[aria-label="Gate wall time"]')!
      await waitFor(() => expect(gate.querySelector('.dashboard-number')).toHaveTextContent('60'), {timeout: 5000})
      const gateValue = gate.querySelector('.dashboard-number')
      const gauge = tab.querySelector('[aria-label="Codex weekly usage"]')!
      expect(worker).toBeTruthy()
      expect(gauge, [...tab.querySelectorAll('.dashboard-card')].map(el => el.getAttribute('aria-label')).join(',')).toBeTruthy()
      await waitFor(() => expect(worker.querySelector('.dashboard-number')).toHaveTextContent('0'), {timeout: 5000})
      const body = tab.closest('.artifact-body') as HTMLElement
      body.scrollTop = gauge.getBoundingClientRect().top - body.getBoundingClientRect().top - 160
      await waitFor(() => expect(gauge.querySelector('[role="meter"]')).toHaveAttribute('aria-valuenow', '40'), {timeout: 5000})
      const meter = gauge.querySelector('[role="meter"]'), fill = gauge.querySelector('.dashboard-meter-fill'), value = gauge.querySelector('.dashboard-number')
      await waitFor(() => expect(tab.querySelectorAll('.dashboard-chart .visual-surface[data-scene-state="exact"] svg, .dashboard-chart .visual-surface[data-scene-state="exact"] canvas')).toHaveLength(3), {timeout: 30_000})
      const drawings = [...tab.querySelectorAll('.dashboard-chart .visual-surface svg, .dashboard-chart .visual-surface canvas')]
      await userEvent.hover(gauge)
      expect(gauge.matches(':hover')).toBe(true)
      const scrollBefore = body.scrollTop
      await capture('before')
      const response = await fetch(fixture.url + '/fixture/change', {method: 'POST', headers: {Authorization: 'Bearer ' + fixture.token}})
      expect(response.ok).toBe(true)
      await waitFor(() => expect(gauge.querySelector('[role="meter"]')).toHaveAttribute('aria-valuenow', '75'), {timeout: 5000})
      await capture('changing')
      await waitFor(() => expect(gauge.querySelector('.dashboard-number')).toHaveTextContent('75'))
      expect(body.scrollTop).toBe(scrollBefore)
      expect(gauge.matches(':hover')).toBe(true)
      body.scrollTop = 0
      await waitFor(() => expect(worker.querySelector('.dashboard-number')).toHaveTextContent('1'), {timeout: 5000})
      expect(tab.querySelector('[aria-label="Workers running"]')).toBe(worker)
      await waitFor(() => expect(gate.querySelector('.dashboard-number')).toHaveTextContent('45'), {timeout: 5000})
      expect(gate.querySelector('.dashboard-number')).toBe(gateValue)
      const retained = gauge.querySelector('.dashboard-number')!.textContent
      body.scrollTop = body.scrollHeight
      await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      expect(gauge.querySelector('.dashboard-number')!.textContent).toBe(retained)
      body.scrollTop = 0
      expect(tab.querySelector('[aria-label="Codex weekly usage"]')).toBe(gauge)
      expect(gauge.querySelector('[role="meter"]')).toBe(meter)
      expect(gauge.querySelector('.dashboard-meter-fill')).toBe(fill)
      expect(gauge.querySelector('.dashboard-number')).toBe(value)
      expect([...tab.querySelectorAll('.dashboard-chart .visual-surface svg, .dashboard-chart .visual-surface canvas')]).toEqual(drawings)
      expect(deltas.some(d => !d.snapshot && d.base_sequence > 0)).toBe(true)
      await capture('after')
      body.scrollTop = 0
      await waitFor(() => expect(tab.querySelectorAll('.dashboard-chart .visual-surface[data-scene-state="exact"] svg, .dashboard-chart .visual-surface[data-scene-state="exact"] canvas')).toHaveLength(3), {timeout: 30_000})
      await capture('dashboard')
      expect(tab.scrollWidth).toBeLessThanOrEqual(tab.clientWidth)
      await commands.setUsageReducedMotion(true)
      expect(getComputedStyle(fill!).transitionDuration).toBe('0s')
    } finally {
      view?.unmount()
      socket.close()
      wsMock.send.mockReset()
      await commands.stopDatasetFixture(fixture.token)
      await commands.setUsageReducedMotion(false)
    }
  }, 120_000)
}
