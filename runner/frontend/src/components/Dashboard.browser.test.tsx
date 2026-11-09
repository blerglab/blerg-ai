// BLG-470/506: real chart cards and quiet live timestamps, mounted through the real
// SessionDetail page the way PopOutMessage.browser.test.tsx does (the
// real SessionDetail page, its real CSS and dark theme), not a bare component in an unstyled host.
import { fireEvent, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it } from 'vitest'
import { vi } from 'vitest'
import { page } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import {clearViews} from '../lib/sessionViewCache'
import {clearViewCache} from '../../../../packages/chat/src/model/viewCache'
import CommandPaletteProvider from './CommandPalette'
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
const chartSource = JSON.stringify(dashboard)
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
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
    if (url.endsWith('/artifacts/' + LIVE_ID + '/raw')) return new Response(source)
    if (url.endsWith('/artifacts')) return json({ artifacts: [liveFile], limits: {}, can_delete: false })
    return json([])
  }))
})
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals(); vi.restoreAllMocks(); document.documentElement.removeAttribute('data-theme') })

function mount(pane: number) {
  return render(
    <MemoryRouter initialEntries={['/sessions/sess-a']}>
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
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'f1', seq: 1, ts: '2026-11-03T00:00:00Z', kind: 'artifact', payload: liveFile } as AgentEvent)
  transcript.ingestReplayDone('sess-a', { lastSeq: 1, hasMore: false })
}

for (const [width, height, pane, theme] of [[390, 844, 390, 'dark'], [520, 1000, 520, 'dark'], [3840, 2160, 1024, 'dark'], [3840, 2160, 3840, 'dark'], [3840, 2160, 3840, 'colorblind-dark']] as const) {
  it(`renders dashboard cards, responsive grid and source refusal at ${width}x${height} pane ${pane} ${theme}`, async () => {
    await page.viewport(width, height)
    document.documentElement.setAttribute('data-theme', theme)
    seedLiveArtifact()
    const view = mount(pane)

    const rail = view.container.querySelector('[data-testid="rail-toggle"][aria-expanded="true"]')
    if (rail) fireEvent.click(rail)
    const card = await waitFor(() => {
      const el = document.querySelector('[data-testid="artifact-card"]')
      if (!el) throw new Error('no artifact card yet')
      return el as HTMLElement
    })
    expect(card.querySelector('[data-testid="artifact-card-version"]')).toBeNull()
    const cardTime = card.querySelector('[data-testid="artifact-card-updated"]') as HTMLElement
    expect(cardTime).not.toHaveTextContent(/Updated/)
    expect(cardTime).toHaveClass('card-time', 'card-time-end')
    expect(cardTime).toBe(cardTime.parentElement?.lastElementChild)
    expect(cardTime.querySelector('time')?.getAttribute('title')).toBeTruthy()
    expect(getComputedStyle(cardTime).opacity).toBe('0.6')
    expect(getComputedStyle(cardTime).fontSize).toBe('10.5px')
    const preview = card.querySelector('[data-testid="artifact-preview-diagram"]') as HTMLElement
    await waitFor(() => expect(preview.querySelectorAll('.dashboard-card')).toHaveLength(9), { timeout: 30_000 })
    expect(preview.getBoundingClientRect().height).toBe(260)
    expect(card.querySelector('[data-testid="artifact-preview-excerpt"]')).toBeNull()
    const capture = async (state: string) => {
      if (!shotDir) return
      const frame = document.createElement('div')
      frame.style.cssText = 'position:fixed;inset:0;width:100vw;height:100vh;pointer-events:none'
      document.body.append(frame)
      try { await page.screenshot({ element: frame, path: `${shotDir}/${state}-${width}-pane-${pane}-${theme}.png` }) } finally { frame.remove() }
    }
    await capture('card')

    const viewButton = [...card.querySelectorAll('button')].find(b => b.textContent === 'View')
    if (!viewButton) throw new Error('no View button on the live card')
    fireEvent.click(viewButton)
    await waitFor(() => expect(document.querySelector('.hosted-view:not([hidden]) .artifact-body')).toBeTruthy())
    const tab = document.querySelector('.hosted-view:not([hidden])') as HTMLElement
    await waitFor(() => expect(tab.querySelector('[data-testid="viewer-live-updated"]')).toBeTruthy())
    expect(tab.querySelector('.viewer-version-select')).toBeNull()
    expect(tab.querySelector('button[aria-label="Snapshot"]')).toBeTruthy()
    const timestamp = tab.querySelector('[data-testid="viewer-live-updated"]') as HTMLElement
    expect(timestamp.textContent).not.toContain('Updated')
    expect(timestamp.querySelector('time')?.getAttribute('title')).toBeTruthy()
    const snapshot = tab.querySelector('button[aria-label="Snapshot"]') as HTMLElement
    // The static-snapshot explanation may wrap below the controls in a narrow pane.
    // The time itself still belongs at the toolbar's trailing edge.
    expect(timestamp.querySelector('time')!.getBoundingClientRect().left).toBeGreaterThan(snapshot.getBoundingClientRect().right)
    const toolbar = tab.querySelector('.viewer-toolbar') as HTMLElement
    expect(toolbar.getBoundingClientRect().right - timestamp.getBoundingClientRect().right).toBeLessThan(12)
    await waitFor(() => expect(tab.querySelectorAll('.dashboard-chart .visual-surface[data-scene-state="exact"] svg, .dashboard-chart .visual-surface[data-scene-state="exact"] canvas')).toHaveLength(3), { timeout: 30_000 })
    for (const drawing of tab.querySelectorAll('.dashboard-chart .visual-surface[data-scene-state="exact"] svg, .dashboard-chart .visual-surface[data-scene-state="exact"] canvas')) {
      const surface = drawing.closest('.visual-surface') as HTMLElement
      expect(drawing.getBoundingClientRect().width).toBeGreaterThan(surface.clientWidth - 50)
      expect(drawing.getBoundingClientRect().height).toBeGreaterThan(180)
    }
    const grid = tab.querySelector('.dashboard-grid') as HTMLElement
    const block = tab.querySelector('.dashboard-block') as HTMLElement
    const contentWidth = block.clientWidth - 24
    const columns = contentWidth >= 1000 ? 4 : contentWidth >= 496 ? 2 : 1
    expect(getComputedStyle(grid).gridTemplateColumns.split(' ').length).toBe(columns)
    expect(grid.scrollWidth).toBeLessThanOrEqual(grid.clientWidth + 1)
    expect(tab.querySelector('[data-kind="gauge"] [role="meter"]')).toHaveAttribute('aria-valuenow', '71')
    expect(tab.querySelector('[data-kind="text"]')).toHaveTextContent('Local snapshot')
    expect(tab.querySelector('.dashboard-sparkline')).toBeTruthy()
    expect(block.getBoundingClientRect().width).toBeLessThanOrEqual(pane)
    await capture('tab')
    if (width <= 520) {
      const body = tab.querySelector('.artifact-body') as HTMLElement
      body.scrollTop = body.scrollHeight
      await capture('trends')
      body.scrollTop = 0
    }

    view.unmount()
    // This branch proves first-open refusal, independent of a retained valid tab.
    localStorage.clear()
    clearViews()
    clearViewCache()
    liveFile.updated_at = new Date().toISOString()
    source = JSON.stringify({...dashboard, version: 0})
    seedLiveArtifact()
    const refusedView = mount(pane)
    const refused = await waitFor(() => {
      const el = document.querySelector('.dashboard-block:has([role="status"])')
      if (!el) throw new Error('no refusal')
      return el as HTMLElement
    })
    expect(refused.textContent).toContain('Dashboard refused')
    expect(refused.querySelector('pre')).toBeNull()
    await capture('refused')
    fireEvent.click(refused.querySelector('button[aria-label="Show source"]')!)
    await waitFor(() => expect(refused.querySelector('pre')).toBeTruthy())
    refusedView.unmount()
  }, 40_000)
}
