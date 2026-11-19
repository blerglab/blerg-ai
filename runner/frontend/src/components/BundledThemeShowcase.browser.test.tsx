// BLG-474: real-app evidence that the bundled open-source schemes render legibly across the
// runner chat, not just that the tokens pass their contrast math. Mounts the real SessionDetail
// page (same harness as PopOutMessage.browser.test.tsx/SessionDetail.graph.browser.test.tsx) with
// a transcript that has both a Vega-Lite chart and a Mermaid diagram, in three of the six bundled
// schemes, at phone and very-wide widths. Screenshots only when VITE_THEME_SHOTS_DIR is set (owner
// review evidence), same convention as PopOutMessage.browser.test.tsx.
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

const CHART = JSON.stringify({
  $schema: 'https://vega.github.io/schema/vega-lite/v6.json',
  width: 300,
  height: 180,
  data: { values: [{ category: 'Alpha', y: 2 }, { category: 'Beta', y: 4 }, { category: 'Gamma', y: 3 }] },
  mark: 'bar',
  encoding: { x: { field: 'category', type: 'nominal' }, y: { field: 'y', type: 'quantitative' } },
})
const DIAGRAM = 'flowchart LR\n  Build --> Test\n  Test --> Release'

beforeEach(() => {
  localStorage.clear()
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ id: 'sess-a', kind: 'agent', status: 'idle', title: 'Theme preview' })] })
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

function mount() {
  return render(
    <MemoryRouter initialEntries={['/sessions/sess-a']}>
      <CommandPaletteProvider>
        <div style={{ display: 'flex', height: '100vh' }}>
          <div style={{ flex: 1, minWidth: 0 }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div>
        </div>
      </CommandPaletteProvider>
    </MemoryRouter>,
  )
}

function seedChartAndDiagram() {
  useTranscriptStore.setState({ sessions: {} })
  const transcript = useTranscriptStore.getState()
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'u1', seq: 1, ts: '2026-11-03T12:00:00Z', kind: 'user_message', payload: { text: 'Show the release pipeline and this run\'s results.', source: 'chat' } } as AgentEvent)
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'a1', seq: 2, ts: '2026-11-03T12:00:01Z', kind: 'assistant_text', payload: { text: '## Release pipeline\n\n```mermaid\n' + DIAGRAM + '\n```', done: true } } as AgentEvent)
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'a2', seq: 3, ts: '2026-11-03T12:00:02Z', kind: 'assistant_text', payload: { text: 'Results by category:\n\n```vega-lite\n' + CHART + '\n```', done: true } } as AgentEvent)
  transcript.ingestReplayDone('sess-a', { lastSeq: 3, hasMore: false })
}

// Three of the six bundled schemes: one Catppuccin flavor, Nord and Tokyo Night — a light and two
// dark bundled themes, covering most of the mapping's variety without shooting all six.
const THEMES_TO_SHOW = ['catppuccin-mocha', 'nord', 'tokyo-night'] as const

for (const theme of THEMES_TO_SHOW) {
  for (const [width, height] of [[3840, 2160], [390, 844]] as const) {
    it(`renders the runner chat with a chart and a diagram readably in ${theme} at ${width}x${height}`, async () => {
      await page.viewport(width, height)
      document.documentElement.setAttribute('data-theme', theme)
      seedChartAndDiagram()
      const view = mount()
      await waitFor(() => expect(document.querySelectorAll('.agent-card.assistant')).toHaveLength(2))
      // Blerg draws this flowchart itself (BLG-468) as an inline SVG; Mermaid's fallback is an <img>.
      const image = await waitFor(() => {
        const node = document.querySelector<Element>('svg[data-diagram-renderer], img.visual-diagram')
        expect(node?.getBoundingClientRect().width ?? 0).toBeGreaterThan(20)
        return node!
      }, { timeout: 30_000 })
      expect(image).toBeTruthy()
      // The chart's own data-table disclosure only renders once Vega has finished drawing it.
      await waitFor(() => { expect(document.body.textContent).toContain('Data table') }, { timeout: 30_000 })
      if (shotDir) await page.screenshot({ path: `${shotDir}/runner-${theme}-${width}x${height}.png` })
      view.unmount()
    }, 45_000)
  }
}
