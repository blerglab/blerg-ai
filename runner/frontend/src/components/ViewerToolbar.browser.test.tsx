import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { localStorageChatStorage, useTranscriptStore, type AgentEvent, type ArtifactInfo } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import CommandPaletteProvider from './CommandPalette'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { sessionTabsKey, viewId } from '../hooks/useSessionTabs'
import { RAIL_PREF_KEY } from '../lib/sessionRail'
import { invalidateChatTransport } from '../lib/chatTransport'
import '../index.css'
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
vi.mock('../shellTransport', async original => ({ ...await original<typeof import('../shellTransport')>(), restoreShells: async () => [] }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('../lib/engineModels', async original => ({ ...await original<typeof import('../lib/engineModels')>(), useEngineModels: () => ({ models: [], loading: false, fallback: false, source: 'live', engineEfforts: [] }) }))
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
const shotDir = import.meta.env.VITE_FILE_TAB_SHOTS_DIR
beforeEach(() => {
  document.documentElement.dataset.theme = 'dark'
  localStorage.clear(); localStorage.setItem(RAIL_PREF_KEY, '0')
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ kind: 'agent', status: 'idle', title: 'Prepare the launch notes' })] })
  useTranscriptStore.setState({ sessions: {} })
  const store = useTranscriptStore.getState()
  store.ingest({ session_id: 'sess-a', seq: 1, client_event_id: 'eof', ts: '2026-10-25T12:00:00Z', kind: 'assistant_text', payload: { text: 'I prepared the launch notes and a preview for review.', done: true } } as AgentEvent)
  store.ingestReplayDone('sess-a', { lastSeq: 1, hasMore: false })
  apiFetch.mockReset().mockImplementation(async (url: string) => url.includes('/live') ? json({ can_release_prompt: true }) : url.endsWith('/shells') ? json([]) : url.endsWith('/shell-capability') ? json({ available: false }) : json({}))

})
afterEach(() => { delete document.documentElement.dataset.theme; invalidateChatTransport(); vi.unstubAllGlobals() })
function mount() {
  return render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider><div style={{ height: '100vh' }}><Routes><Route path="/sessions/:id" element={<SessionDetail/>}/></Routes></div></CommandPaletteProvider></MemoryRouter>)
}

for (const width of [390, 3840]) {
  for (const kind of ['artifact', 'workspace', 'diff', 'live', 'image'] as const) {
    it(`shows the ${kind} viewer in the session at ${width}px`, async () => {
      await page.viewport(width, width === 390 ? 844 : 2160)
      const artifact = { id: 'notes-v2', name: kind === 'image' ? 'launch.svg' : kind === 'diff' ? 'changes.diff' : kind === 'live' ? 'preview.html' : 'notes.md', view: kind === 'image' ? 'image' : kind === 'diff' ? 'diff' : kind === 'live' ? 'html' : 'markdown', size: 120, content_type: kind === 'image' ? 'image/svg+xml' : 'text/plain', version: 2, latest_version: 2 }
      const artifacts = [{ ...artifact, id: 'notes-v1', version: 1 }, artifact]
      vi.stubGlobal('fetch', vi.fn(async (url: string) => {
        if (String(url).includes('/workspace-file')) return json({ path: 'notes.md', view: 'markdown', content_type: 'text/plain', content_b64: btoa('# Launch notes\n\nThe release is ready for review.') })
        if (String(url).endsWith('/raw')) return new Response(kind === 'image' ? '<svg xmlns="http://www.w3.org/2000/svg" width="960" height="600"><rect width="960" height="600" fill="#172536"/><rect x="80" y="80" width="800" height="440" rx="24" fill="#27486a"/><text x="140" y="300" fill="#eef4fa" font-family="sans-serif" font-size="64">Launch preview</text></svg>' : kind === 'diff' ? 'diff --git a/notes.md b/notes.md\n--- a/notes.md\n+++ b/notes.md\n@@ -1 +1 @@\n-Draft\n+Ready for review\n' : kind === 'live' ? '<h1>Launch preview</h1><p>Ready for review</p>' : String(url).includes('notes-v1') ? '# Launch notes\n\nDraft.' : '# Launch notes\n\nThe release is ready for review.')
        return json(String(url).endsWith('/artifacts') ? { artifacts } : [])
      }))
      const view = kind === 'workspace' ? { kind: 'workspace', path: 'notes.md' } : { kind: 'artifact', artifact }
      localStorage.setItem(sessionTabsKey('person-a', 'sess-a'), JSON.stringify({ tabs: [{ id: viewId(view as Parameters<typeof viewId>[0]), view, preview: false }], active: 'chat' }))
      mount()
      if (width === 390) { await userEvent.click(screen.getByRole('button', { name: '1 open file' })); await userEvent.click(screen.getByRole('menuitem', { name: artifact.name === 'notes.md' ? 'notes.md' : artifact.name })) }
      else await userEvent.click(screen.getByRole('tab', { name: new RegExp(artifact.name) }))
      const viewer = await screen.findByRole('region', { name: new RegExp(`^${artifact.name}(?: ·|$)`) })
      await waitFor(() => expect(viewer.querySelector('.artifact-body')?.textContent || viewer.querySelector('iframe') || viewer.querySelector('img')?.naturalWidth).toBeTruthy())
      if (kind === 'image') {
        await waitFor(() => expect(viewer.querySelector('img')?.naturalWidth).toBe(960))
        if (shotDir) await page.screenshot({ path: `${shotDir}/${kind}-${width}.png` })
      }
      if (kind === 'artifact') await within(viewer).findByRole('button', { name: 'Changes since v1' })
      const toolbar = within(viewer).getByRole('toolbar', { name: 'File actions' })
      expect(viewer.querySelectorAll('[role="toolbar"]')).toHaveLength(1)
      await waitFor(() => expect(toolbar.querySelector('button[aria-label="Download"]')).toBeTruthy())
      const more = toolbar.clientWidth < 600 ? await within(toolbar).findByRole('button', { name: 'More' }) : null
      if (more) await userEvent.click(more)
      expect(within(toolbar).getByRole('button', { name: 'Print' })).toHaveAttribute('title', 'Print')
      expect(within(toolbar).getByRole('button', { name: 'Download' })).toBeInTheDocument()
      // The tab owns a plain filename; the utility bar must not repeat it.
      expect(toolbar.querySelector('h2')).toBeNull()
      expect(viewer.querySelector('.artifact-comparison-controls')).toBeNull()
      for (const button of toolbar.querySelectorAll<HTMLButtonElement>('.viewer-action')) {
        expect(button).toHaveAccessibleName()
        expect(button.title).toBeTruthy()
        expect(getComputedStyle(button).borderWidth).toBe('0px')
        expect(button.textContent).toBe('')
        const box = button.getBoundingClientRect()
        expect(box.left).toBeGreaterThanOrEqual(0)
        expect(box.right).toBeLessThanOrEqual(width)
      }
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
      if (shotDir) await page.screenshot({ path: `${shotDir}/${kind}-${width}.png` })
      if (kind === 'artifact' || kind === 'workspace') {
        await userEvent.click(within(viewer).getByRole('button', { name: 'Raw' }))
        const raw = within(viewer).getByRole('button', { name: 'Rendered' })
        expect(raw).toHaveAttribute('aria-pressed', 'true')
        expect(getComputedStyle(raw).borderWidth).toBe('0px')
        if (shotDir) await page.screenshot({ path: `${shotDir}/${kind}-raw-${width}.png` })
        await userEvent.click(raw)
      }
      if (kind === 'artifact') {
        await userEvent.click(within(viewer).getByRole('button', { name: 'Changes since v1' }))
        await within(viewer).findByRole('button', { name: 'Document' })
        await waitFor(() => expect(viewer.querySelector('.diff-table-wrap')).not.toBeNull())
        expect(viewer.querySelector('.artifact-comparison-controls')).toBeNull()
        for (const button of toolbar.querySelectorAll<HTMLButtonElement>('.viewer-action')) {
          expect(button.getBoundingClientRect().right).toBeLessThanOrEqual(width)
          expect(getComputedStyle(button).borderWidth).toBe('0px')
        }
        if (shotDir) await page.screenshot({ path: `${shotDir}/comparison-${width}.png` })
      }
    })
  }
}

for (const width of [390, 3840]) it(`follows chat publications and offers a pinned update without reloading at ${width}px`, async () => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  localStorageChatStorage.set('blerg.viewer.follow.sess-a.agent.launch.md', null)
  const first: ArtifactInfo = { id: 'launch-v1', name: 'launch.md', view: 'markdown', size: 120, content_type: 'text/markdown', version: 1, latest_version: 1 }
  const artifacts = [first]
  const publish = (version: number) => {
    const artifact = { ...first, id: `launch-v${version}`, version, latest_version: version }
    for (const item of artifacts) item.latest_version = version
    if (version > 1) artifacts.unshift(artifact)
    act(() => useTranscriptStore.getState().ingest({ type: 'agent_event', session_id: 'sess-a', seq: version + 1, client_event_id: `publish-${version}`, ts: '2026-10-25T12:00:00Z', kind: 'artifact', payload: artifact }))
  }
  vi.stubGlobal('fetch', vi.fn(async (url: string) => String(url).endsWith('/raw') ? new Response(`# Launch notes\n\nRelease ${String(url).match(/launch-v(\d+)/)?.[1]} is ready.`) : json(String(url).endsWith('/artifacts') ? { artifacts } : [])))
  publish(1)
  mount()
  const card = await screen.findByTestId('artifact-card')
  await userEvent.click(within(card).getByRole('button', { name: 'View' }))
  const viewer = await screen.findByRole('region', { name: /launch.md/ })
  await within(viewer).findByText('Release 1 is ready.')
  expect(screen.getByRole('button', { name: 'Pin version' })).toHaveAttribute('aria-pressed', 'true')
  publish(2)
  await waitFor(() => expect(within(screen.getByRole('region', { name: /launch.md/ })).getByText('Release 2 is ready.')).toBeVisible())
  await userEvent.click(screen.getByRole('button', { name: 'Pin version' }))
  publish(3)
  const update = await screen.findByRole('button', { name: 'v3 is available' })
  expect(update).toHaveAttribute('title', 'v3 is available')
  expect(within(screen.getByRole('region', { name: /launch.md/ })).getByText('Release 2 is ready.')).toBeVisible()
  expect(update.closest('[role="toolbar"]')).toBeTruthy()
  expect(update.querySelector('.viewer-version-dot')).not.toBeNull()
  expect(update.closest('[role="toolbar"]')?.querySelector('h2')).toBeNull()
  expect(within(screen.getByRole('region', { name: /launch.md/ })).queryByText('Release 3 is ready.')).toBeNull()
  if (shotDir) await page.screenshot({ path: `${shotDir}/pinned-update-${width}.png` })
  await userEvent.click(update)
  await waitFor(() => expect(within(screen.getByRole('region', { name: /launch.md/ })).getByText('Release 3 is ready.')).toBeVisible())
  expect(screen.queryByRole('button', { name: 'v3 is available' })).toBeNull()
  expect(screen.getByRole('button', { name: 'Follow latest' })).toHaveAttribute('aria-pressed', 'false')
})
