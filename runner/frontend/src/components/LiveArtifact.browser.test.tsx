// BLG-470/506: real chart cards and quiet live timestamps, mounted through the real
// SessionDetail page the way PopOutMessage.browser.test.tsx does (the
// real SessionDetail page, its real CSS and dark theme), not a bare component in an unstyled host.
import { fireEvent, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it } from 'vitest'
import { vi } from 'vitest'
import { userEvent, page } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
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
// A native live shape: authored Vega axis options intentionally select static unsupported-live.
const chartSource = JSON.stringify({ title: 'Completed reviews by day', data: { values: [{ day: 'Mon', count: 4 }, { day: 'Tue', count: 7 }, { day: 'Wed', count: 9 }] }, mark: 'bar', encoding: { x: { field: 'day', type: 'nominal' }, y: { field: 'count', type: 'quantitative' } } })
let source = chartSource
const liveFile = { id: LIVE_ID, name: 'completed-reviews.vl.json', size: chartSource.length, content_type: 'application/json', view: 'json', origin: 'agent', version: 1, live: true, updated_at: new Date().toISOString() }

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
        <div style={{ display: 'flex', height: '100vh', width: pane, maxWidth: '100%' }}>
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

for (const [width, height, pane, theme] of [[390, 844, 390, 'dark'], [3840, 2160, 1024, 'dark'], [3840, 2160, 3840, 'dark'], [1024, 800, 1024, 'colorblind-dark']] as const) {
  it(`shows a live file's card with a quiet relative timestamp and a Snapshot tab at ${width}x${height} pane ${pane} ${theme}`, async () => {
    await page.viewport(width, height)
    document.documentElement.setAttribute('data-theme', theme)
    seedLiveArtifact()
    const view = mount(pane)

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
    await waitFor(() => expect(preview.querySelector('.visual-surface svg, .visual-surface canvas')).toBeTruthy(), { timeout: 30_000 })
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
    expect(timestamp.getBoundingClientRect().left).toBeGreaterThan(snapshot.getBoundingClientRect().right)
    const toolbar = tab.querySelector('.viewer-toolbar') as HTMLElement
    expect(toolbar.getBoundingClientRect().height).toBeLessThanOrEqual(40)
    if (width === 390) {
      const more = toolbar.querySelector<HTMLButtonElement>('button[aria-label="More"]')!
      expect(more.title).toBe('More')
      await userEvent.click(more)
      expect(toolbar.querySelector('button[aria-label="Download source"]')).toBeVisible()
      more.focus()
      await userEvent.keyboard('{Escape}')
      expect(more).toHaveFocus()
      expect(tab).toBeVisible()
      expect(toolbar.querySelector('button[aria-label="Download source"]')).not.toBeVisible()
    }
    expect(toolbar.getBoundingClientRect().right - timestamp.getBoundingClientRect().right).toBeLessThan(12)
    await waitFor(() => expect(tab.querySelector('.visual-surface svg, .visual-surface canvas')).toBeTruthy(), { timeout: 30_000 })
    await capture('tab')

    // Close the retained file tab before remounting the transcript refusal fixture.
    fireEvent.click(document.querySelector('button[aria-label="Close completed-reviews.vl.json"]')!)
    view.unmount()
    liveFile.updated_at = new Date().toISOString()
    source = JSON.stringify({ data: { url: 'https://example.com/private.json' }, mark: 'bar' })
    seedLiveArtifact()
    const refusedView = mount(pane)
    const refused = await waitFor(() => {
      const el = document.querySelector('[data-testid="visual-failed"]')
      if (!el) throw new Error('no refusal')
      return el as HTMLElement
    })
    expect(refused.textContent).toContain('Chart refused')
    expect(refused.querySelector('pre')).toBeNull()
    await capture('refused')
    fireEvent.click(refused.querySelector('button[aria-label="Show source"]')!)
    await waitFor(() => expect(refused.querySelector('pre')).toBeTruthy())
    refusedView.unmount()
  }, 40_000)
}
