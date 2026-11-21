import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { render, waitFor } from '@testing-library/react'
import { page } from 'vitest/browser'
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

const shotDir = (import.meta.env as Record<string, string | undefined>).VITE_THEME_SHOTS_DIR
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })

beforeEach(() => {
  localStorage.clear()
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ id: 'sess-a', kind: 'agent', status: 'idle', title: 'Release work' })] })
  apiFetch.mockReset().mockImplementation(async (url: string) => {
    if (url.endsWith('/topics')) return json({ topics: [], unmarked: null, strokes: [] })
    if (url.includes('/live')) return json({ can_release_prompt: false })
    if (url.includes('/workspace?')) return json({ result: { ok: true, launch_generation: 'launch', generation: 1, entries: [], truncated: false }, members: [], runtime: 'host' })
    if (url.endsWith('/permissions')) return json({ can_grant: false, reason: '', grants: [], mcp_tools: [], boards: [] })
    return json({})
  })
  vi.stubGlobal('fetch', vi.fn(async () => json([])))
})
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals(); vi.restoreAllMocks(); document.documentElement.removeAttribute('data-theme') })

function mount(paneWidth: number) {
  return render(
    <MemoryRouter initialEntries={['/sessions/sess-a']}>
      <CommandPaletteProvider>
        <div style={{ display: 'flex', height: '100vh', width: paneWidth }}>
          <div style={{ flex: 1, minWidth: 0 }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div>
        </div>
      </CommandPaletteProvider>
    </MemoryRouter>,
  )
}

const TABLES = `Release work:

| Piece | Description | Effort |
| --- | --- | --- |
| Import | Import the existing records and validate the complete release workflow before saving changes. | Small |
| Screens | Review the narrow screen layout and confirm every control remains reachable. | Medium |
| Process | Check the remaining release steps with realistic populated data. | Small |

| Link | Destination |
| --- | --- |
| Source | https://example.com/${'abcdef'.repeat(40)} |
| Hash | \`${'abcdef'.repeat(40)}\` |

| Alpha | Bravo | Charlie | Delta | Echo | Foxtrot | Golf | Hotel | India | Juliet |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Ready | Ready | Ready | Ready | Ready | Ready | Ready | Ready | Ready | Ready |`

function lines(element: Element) {
  const range = document.createRange()
  range.selectNodeContents(element)
  return new Set(Array.from(range.getClientRects(), rect => Math.round(rect.top))).size
}

for (const { width, paneWidth } of [{ width: 390, paneWidth: 390 }, { width: 3840, paneWidth: 3840 }, { width: 3840, paneWidth: 390 }]) {
  for (const theme of ['dark', 'light']) {
    it(`keeps table labels whole and overflow local at ${width}px in a ${paneWidth}px pane (${theme})`, async () => {
      await page.viewport(width, width === 390 ? 844 : 2160)
      document.documentElement.dataset.theme = theme
      useTranscriptStore.setState({ sessions: {} })
      useTranscriptStore.getState().ingest({ session_id: 'sess-a', client_event_id: 'a1', seq: 1, ts: '2026-11-03T12:00:00Z', kind: 'assistant_text', payload: { text: TABLES, done: true } } as AgentEvent)
      useTranscriptStore.getState().ingestReplayDone('sess-a', { lastSeq: 1, hasMore: false })
      mount(paneWidth)
      await waitFor(() => expect(document.querySelectorAll('.md-table-wrap')).toHaveLength(3))
      const wraps = Array.from(document.querySelectorAll<HTMLElement>('.md-table-wrap'))
      if (shotDir) {
        const timeline = document.querySelector<HTMLElement>('.agent-timeline')!
        timeline.scrollTop = 0
        await waitFor(() => expect(timeline.scrollTop).toBe(0))
      }
      if (shotDir) {
        await page.screenshot({ path: `${shotDir}/tables-${width}-pane-${paneWidth}-${theme}.png` })
        if (paneWidth === 390) {
          wraps[1].scrollIntoView({ block: 'start' })
          await page.screenshot({ path: `${shotDir}/table-tokens-${width}-pane-${paneWidth}-${theme}.png` })
        }
      }
      for (const cell of wraps[0].querySelectorAll('th:first-child, td:first-child, td:last-child')) {
        expect(lines(cell), cell.textContent ?? '').toBe(1)
      }
      for (const wrap of wraps) {
        const table = wrap.querySelector('table')!
        const actions = wrap.querySelector<HTMLElement>('.md-export-actions')!
        expect(table.getBoundingClientRect().top - wrap.getBoundingClientRect().top).toBeCloseTo(getComputedStyle(actions).position === 'absolute' ? 0 : actions.getBoundingClientRect().height, 0)
        expect(wrap.getBoundingClientRect().right).toBeLessThanOrEqual(width)
      }
      expect(wraps[0].scrollWidth).toBeLessThanOrEqual(wraps[0].clientWidth + 1)
      expect(wraps[1].querySelector('table')!.getBoundingClientRect().width).toBeLessThanOrEqual(wraps[1].clientWidth + 1)
      expect(wraps[1].querySelector('td:first-child')!.textContent).toBe('Source')
      expect(lines(wraps[1].querySelector('td:first-child')!)).toBe(1)
      expect(wraps[2].querySelector<HTMLElement>('.md-table-scroll')!.scrollWidth > wraps[2].querySelector<HTMLElement>('.md-table-scroll')!.clientWidth).toBe(paneWidth === 390)
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    })
  }
}
