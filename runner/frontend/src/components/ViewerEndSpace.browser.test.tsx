import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import CommandPaletteProvider from './CommandPalette'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { sessionTabsKey } from '../hooks/useSessionTabs'
import { RAIL_PREF_KEY } from '../lib/sessionRail'
import { invalidateChatTransport } from '../lib/chatTransport'
import { openSessionFile } from '../lib/openSessionFile'
import '../index.css'
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
vi.mock('../shellTransport', async original => ({ ...await original<typeof import('../shellTransport')>(), restoreShells: async () => [] }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('../lib/engineModels', async original => ({ ...await original<typeof import('../lib/engineModels')>(), useEngineModels: () => ({ models: [], loading: false, fallback: false, source: 'live', engineEfforts: [] }) }))
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
const marker = 'EOF_SENTINEL'
const code = [...Array.from({ length: 200 }, (_, i) => `const line${i} = ${i}`), `// ${marker}`].join('\n')
const markdown = '# Notes\n\n' + Array.from({ length: 100 }, (_, i) => `Paragraph ${i}.\n\n`).join('') + marker
const patch = 'diff --git a/main.ts b/main.ts\n--- a/main.ts\n+++ b/main.ts\n@@ -0,0 +1,201 @@\n' + code.split('\n').map(line => '+' + line).join('\n') + '\n'
const shotDir = import.meta.env.VITE_FILE_TAB_SHOTS_DIR
beforeEach(() => {
  localStorage.clear(); localStorage.setItem(RAIL_PREF_KEY, '0')
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ kind: 'agent', status: 'idle', title: 'Read the last line comfortably' })] })
  useTranscriptStore.setState({ sessions: {} })
  const store = useTranscriptStore.getState()
  store.ingest({ session_id: 'sess-a', seq: 1, client_event_id: 'eof', ts: '2026-10-25T12:00:00Z', kind: 'assistant_text', payload: { text: 'The viewer keeps room after the final line.', done: true } } as AgentEvent)
  store.ingestReplayDone('sess-a', { lastSeq: 1, hasMore: false })
  apiFetch.mockReset().mockImplementation(async (url: string) => url.includes('/live') ? json({ can_release_prompt: true }) : url.endsWith('/shells') ? json([]) : url.endsWith('/shell-capability') ? json({ available: false }) : json({}))
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (String(url).includes('/workspace-file')) {
      const path = new URL(String(url), location.origin).searchParams.get('path')!
      return json({ path, view: path.endsWith('.md') ? 'markdown' : path.endsWith('.diff') ? 'diff' : 'text', content_type: 'text/plain', content_b64: btoa(path.endsWith('.md') ? markdown : path.endsWith('.diff') ? patch : code) })
    }
    if (String(url).endsWith('/raw')) return new Response(code)
    return json(String(url).endsWith('/artifacts') ? { artifacts: [] } : [])
  }))
})
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals() })
function mount() {
  return render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider><div style={{ height: '100vh' }}><Routes><Route path="/sessions/:id" element={<SessionDetail/>}/></Routes></div></CommandPaletteProvider></MemoryRouter>)
}
function markerRange(element: Element) {
  const index = element.textContent?.lastIndexOf(marker) ?? -1
  if (index < 0) throw new Error('The final line has not rendered')
  const range = document.createRange(), walker = document.createTreeWalker(element, NodeFilter.SHOW_TEXT)
  let offset = 0, started = false
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const length = node.textContent?.length ?? 0
    if (!started && index < offset + length) { range.setStart(node, index - offset); started = true }
    if (started && index + marker.length <= offset + length) { range.setEnd(node, index + marker.length - offset); return range }
    offset += length
  }
  throw new Error('The final line has not rendered')
}
function sessionIsContained() {
  const columns = screen.getByTestId('session-columns'), root = document.querySelector<HTMLElement>('[data-session-root]')!
  expect(columns.scrollHeight).toBe(columns.clientHeight); expect(columns.scrollWidth).toBe(columns.clientWidth)
  expect(root.scrollHeight).toBe(root.clientHeight); expect(root.scrollWidth).toBe(root.clientWidth)
  expect(columns.scrollTop).toBe(0); expect(root.scrollTop).toBe(0)
}
for (const width of [390, 3840]) {
  for (const mode of ['code', 'raw', 'diff', 'markdown', 'artifact-code', 'artifact-text'] as const) {
    it(`scrolls beyond the last ${mode} line inside its viewer at ${width} px`, async () => {
      await page.viewport(width, width === 390 ? 844 : 2160)
      const path = mode === 'code' || mode === 'artifact-code' ? 'main.ts' : mode === 'artifact-text' ? 'plain.txt' : mode === 'diff' ? 'changes.diff' : 'notes.md'
      if (mode.startsWith('artifact-')) {
        const artifact = { id: 'source', name: path, view: 'text', size: code.length, content_type: 'text/plain' }
        localStorage.setItem(sessionTabsKey('person-a', 'sess-a'), JSON.stringify({ tabs: [{ id: 'artifact:source', view: { kind: 'artifact', artifact }, preview: false }], active: 'artifact:source' }))
      }
      mount()
      if (!mode.startsWith('artifact-')) act(() => { openSessionFile({ session: 'sess-a', account: 'person-a', path }) })
      const viewer = await screen.findByRole('region', { name: path })
      if (mode === 'raw') await userEvent.click(await within(viewer).findByRole('button', { name: 'Raw' }))
      await waitFor(() => expect(markerRange(viewer)).toBeTruthy())
      // Published plain text is numbered like code since #265: the same scroller.
      const scroller = viewer.querySelector<HTMLElement>(mode === 'diff' ? '.diff-main' : mode === 'markdown' ? '.artifact-body' : '.code-viewer-scroll')!
      await waitFor(() => expect(scroller.clientHeight).toBeGreaterThan(200))
      scroller.scrollTop = scroller.scrollHeight
      await waitFor(() => {
        const lastLine = markerRange(viewer).getBoundingClientRect(), box = scroller.getBoundingClientRect()
        const gap = box.bottom - lastLine.bottom
        expect(lastLine.top).toBeGreaterThanOrEqual(box.top - 1)
        expect(gap).toBeGreaterThanOrEqual(mode === 'markdown' ? 32 : scroller.clientHeight / 3 - 1)
      })
      sessionIsContained()
      // A nested split resizes the viewport: the EOF space must follow the viewer, not window height.
      if (width === 3840 && mode !== 'markdown') {
        await userEvent.keyboard('{Control>}k{/Control}')
        await userEvent.fill(screen.getByRole('combobox', { name: /^Search commands/ }), '>Split tab group down')
        await userEvent.keyboard('{Enter}')
        await waitFor(() => expect(document.querySelectorAll('.session-group')).toHaveLength(2))
        scroller.scrollTop = scroller.scrollHeight
        await waitFor(() => expect(scroller.getBoundingClientRect().bottom - markerRange(viewer).getBoundingClientRect().bottom).toBeGreaterThanOrEqual(Math.max(scroller.clientHeight / 3, 6 * 18) - 1))
        const divider = screen.getByRole('separator', { name: /Resize tab groups/ })
        await userEvent.click(divider); await userEvent.keyboard('{End}')
        await waitFor(() => expect(scroller.clientHeight).toBeLessThan(6 * 18 * 3))
        scroller.scrollTop = scroller.scrollHeight
        await waitFor(() => expect(scroller.getBoundingClientRect().bottom - markerRange(viewer).getBoundingClientRect().bottom).toBeGreaterThanOrEqual(6 * 18 - 1))
        sessionIsContained()
        await userEvent.dblClick(divider)
        await waitFor(() => expect(scroller.clientHeight).toBeGreaterThan(500))
        scroller.scrollTop = scroller.scrollHeight
      }
      if (shotDir) await page.screenshot({ path: `${shotDir}/viewer-eof-${mode}-${width}.png` })
    })
  }
}
