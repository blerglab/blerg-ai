// Real-app, real-browser proof for the blackboard recommendation card (BLG-454): the full
// SessionDetail page (its real CSS, dark theme, chat chrome around it), not a bare ChatView in an
// unstyled host — a stripped harness cannot show whether the card's shell, typography and button
// styles actually match the rest of the chat (channel recommendation, BLG-395). Mounted the way
// PopOutMessage.browser.test.tsx and SessionDetail.topbar.browser.test.tsx do.
import { act, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it } from 'vitest'
import { vi } from 'vitest'
import { cdp, page, userEvent } from 'vitest/browser'
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

const shotDir = (import.meta.env as Record<string, string | undefined>).VITE_BLACKBOARD_SHOTS_DIR
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })

const RECOMMENDATION = { id: 'rec-1', reason: '3 workers running at once', state: 'pending' as const }

beforeEach(() => {
  localStorage.clear()
  document.documentElement.setAttribute('data-theme', 'dark')
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ id: 'sess-a', kind: 'agent', status: 'running', title: 'Plan the release' })] })
  apiFetch.mockReset().mockImplementation(async (url: string) => {
    if (url.endsWith('/topics')) return json({ topics: [], unmarked: null, strokes: [] })
    if (url.includes('/live')) return json({ can_release_prompt: false })
    if (url.includes('/workspace?')) return json({ result: { ok: true, launch_generation: 'launch', generation: 1, entries: [], truncated: false }, members: [], runtime: 'host' })
    if (url.endsWith('/permissions')) return json({ can_grant: false, reason: '', grants: [], mcp_tools: [], boards: [] })
    if (url.includes('/blackboards')) return json({ blackboards: [] })
    return json({})
  })
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (typeof url === 'string' && url.includes('/blackboard-recommendation/')) return json(RECOMMENDATION)
    if (typeof url === 'string' && url.includes('/channel-recommendations/')) return json({ id: 'channel-1', topic: 'Plan the release notes', state: 'pending' })
    return json([])
  }))
})
afterEach(async () => { await emulateTouch(false); invalidateChatTransport(); vi.unstubAllGlobals(); vi.restoreAllMocks(); document.documentElement.removeAttribute('data-theme') })

async function emulateTouch(touch: boolean) {
  const client = cdp() as unknown as { send(method: string, params?: object): Promise<unknown> }
  await client.send('Emulation.setTouchEmulationEnabled', { enabled: touch, maxTouchPoints: 1 })
  await client.send('Emulation.setEmulatedMedia', { features: touch ? [{ name: 'hover', value: 'none' }, { name: 'pointer', value: 'coarse' }] : [] })
}

/** `width`/`height` is a pixel number for a narrow pane inside a wider window, or 'full' for the
 *  ordinary case of the app filling its real window (100vw/100vh), the same choice
 *  SessionDetail.topbar.browser.test.tsx uses and for the same reason: a fixed pixel width can
 *  leave the mounted tree short of the actual viewport. */
function mount(width: number | 'full') {
  const size = width === 'full' ? { width: '100vw', height: '100vh' } : { width, height: 900 }
  return render(
    <MemoryRouter initialEntries={['/sessions/sess-a']}>
      <CommandPaletteProvider>
        <div data-testid="recommendation-app-frame" style={size}>
          <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
        </div>
      </CommandPaletteProvider>
    </MemoryRouter>,
  )
}

function seed() {
  useTranscriptStore.setState({ sessions: {} })
  const transcript = useTranscriptStore.getState()
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'r1', seq: 1, ts: '2026-07-26T12:00:00Z', kind: 'blackboard_recommendation', payload: RECOMMENDATION } as AgentEvent)
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'c1', seq: 2, ts: '2026-07-26T12:00:00Z', kind: 'channel_recommendation', payload: { id: 'channel-1', topic: 'Plan the release notes', state: 'pending' } } as AgentEvent)
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'review-1', seq: 3, ts: '2026-07-26T12:00:00Z', kind: 'review_requested', payload: { request_id: 'review-1', artifact_id: 'artifact-1', artifact_name: 'Release plan.md', artifact_version: 1, state: 'pending', ask: 'Check the release plan before we begin.' } } as AgentEvent)
  transcript.ingestReplayDone('sess-a', { lastSeq: 3, hasMore: false })
}

const cases: Array<{ name: string; viewport: [number, number]; mount: number | 'full' }> = [
  { name: '390', viewport: [390, 844], mount: 'full' },
  { name: '1024-pane', viewport: [1920, 1080], mount: 1024 },
  { name: '3840', viewport: [3840, 2160], mount: 'full' },
]

function findButton(root: ParentNode, text: string): HTMLElement | null {
  return document.evaluate(`.//button[text()='${text}']`, root, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null).singleNodeValue as HTMLElement | null
}

for (const theme of ['dark', 'colorblind-dark']) for (const c of cases) it(`renders the blackboard recommendation card in the real chat at ${c.name} in ${theme}`, async () => {
  await page.viewport(...c.viewport)
  await emulateTouch(c.viewport[0] === 390)
  expect(matchMedia('(pointer: coarse)').matches).toBe(c.viewport[0] === 390)
  document.documentElement.setAttribute('data-theme', theme)
  seed()
  const view = mount(c.mount)
  // The card itself (heading + reason) renders before its three actions, which wait on the
  // card's own load() round trip: wait for the actions, not just the card shell.
  await waitFor(() => expect(findButton(document, 'Set it up with Main')).not.toBeNull())
  expect(document.querySelector('[data-session-root]')!.getBoundingClientRect().width).toBe(c.mount === 'full' ? c.viewport[0] : c.mount)
  const card = document.querySelector<HTMLElement>('[data-testid="blackboard-recommendation"]')!
  // No overflow, and real theme tokens resolved (not the unstyled fallback a stripped harness
  // would show): a real border color, not transparent/initial.
  expect(card.scrollWidth).toBeLessThanOrEqual(card.clientWidth)
  const cardBorder = getComputedStyle(card).borderColor
  expect(cardBorder).not.toBe('')
  expect(cardBorder).not.toBe('rgba(0, 0, 0, 0)')
  const primary = findButton(card, 'Set it up with Main')!
  const quiet = findButton(card, 'Not now')!
  expect(primary).toBeTruthy()
  expect(quiet).toBeTruthy()
  expect(card).toHaveClass('agent-card', 'delegation-card')
  expect(primary.getBoundingClientRect().height).toBeGreaterThanOrEqual(c.viewport[0] === 390 ? 44 : 20)
  if (c.viewport[0] !== 390) expect(primary.getBoundingClientRect().height).toBeLessThan(44)
  await waitFor(() => expect(document.querySelector('[data-testid="review-request-card"]')).not.toBeNull())
  // The one primary action reads differently from the two quiet ones (BLG-395 button language,
  // BLG-454 review): a real, resolved color difference, not just a class name.
  expect(getComputedStyle(primary).color).not.toBe(getComputedStyle(quiet).color)
  if (shotDir) await page.screenshot({ path: `${shotDir}/recommendations-pending-${theme}-${c.name}.png` })
  vi.mocked(fetch).mockImplementation(async (url, init) => {
    if (String(url).includes('/blackboard-recommendation/')) return json(init?.method === 'POST' ? { ...RECOMMENDATION, state: 'not_now' } : RECOMMENDATION)
    if (String(url).includes('/channel-recommendations/')) return json({ id: 'channel-1', topic: 'Plan the release notes', state: init?.method === 'POST' ? 'kept' : 'pending' })
    return json([])
  })
  await act(async () => { await userEvent.click(findButton(document, 'Keep in this thread')!) })
  await waitFor(() => expect(document.body.textContent).toContain('Kept in this thread'))
  await act(async () => { await userEvent.click(findButton(card, 'Not now')!) })
  await waitFor(() => expect(card.textContent).toContain('Put off for later.'))
  expect(findButton(card, 'Open blackboard')).not.toBeNull()
  if (shotDir) await page.screenshot({ path: `${shotDir}/recommendations-decided-${theme}-${c.name}.png` })
  await act(async () => { await userEvent.click(findButton(card, 'Open blackboard')!) })
  await waitFor(() => expect(document.querySelector('[aria-label="Blackboard"]')).not.toBeNull())
  view.unmount()
})

it('View blackboard is keyboard-operable and opens the blackboard tab', async () => {
  await page.viewport(1440, 900)
  seed()
  mount('full')
  await waitFor(() => expect(findButton(document, 'View blackboard')).not.toBeNull())
  const setupMyself = findButton(document, 'View blackboard')!
  setupMyself.focus()
  await waitFor(() => expect(document.activeElement).toBe(setupMyself))
  await act(async () => { await userEvent.keyboard('{Enter}') })
  await waitFor(() => expect(document.querySelector('[aria-label="Blackboard"]')).not.toBeNull())
})


it('preserves desktop delegation and proposal action sizes at 1024 px', async () => {
  await page.viewport(1024, 900)
  await emulateTouch(false)
  useTranscriptStore.setState({ sessions: {} })
  const transcript = useTranscriptStore.getState()
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'delegation-1', seq: 1, ts: '2026-07-26T12:00:00Z', kind: 'delegation_request', payload: { request_id: 'delegation-1', engine: 'claude', runtime: 'docker', title: 'Check the release notes', prompt: 'Review the release notes for accuracy.', offered: ['once', 'deny'], expires_at: '2099-01-01T00:00:00Z' } } as AgentEvent)
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'proposal-1', seq: 2, ts: '2026-07-26T12:00:00Z', kind: 'proposal_request', payload: { proposal_id: 'proposal-1', kind: 'board_file' } } as AgentEvent)
  transcript.ingestReplayDone('sess-a', { lastSeq: 2, hasMore: false })
  vi.mocked(fetch).mockImplementation(async (url) => {
    if (String(url).includes('/api/proposals/')) return json({ id: 'proposal-1', kind: 'board_file', state: 'pending', arguments_raw: '{}', arguments: { board_name: 'Release', title: 'Verify the release', body: 'Check the release notes before publishing.' } })
    return json([])
  })
  const view = mount('full')
  await waitFor(() => expect(document.querySelector('[data-testid="proposal-actions"] button')).not.toBeNull())
  const buttons = [...document.querySelectorAll<HTMLElement>('[data-testid="delegation-card"] .delegation-actions button, [data-testid="proposal-actions"] button')]
  expect(buttons.length).toBeGreaterThanOrEqual(4)
  // Pre-BLG-489 baseline: these actions had no minimum height. Capture that baseline in the
  // real app, then compare the actual stylesheet to it, without touching the sibling sources.
  const baseline = document.createElement('style')
  baseline.textContent = '.delegation-actions .delegation-btn { min-height: 0; }'
  document.head.append(baseline)
  let original: number[]
  try {
    original = buttons.map(button => button.getBoundingClientRect().height)
    if (shotDir) await page.screenshot({ path: `${shotDir}/siblings-desktop-1024-before-BLG-489.png` })
  } finally { baseline.remove() }
  const phase = import.meta.env.VITE_RECOMMENDATION_REVIEW_PHASE || 'after'
  if (shotDir) await page.screenshot({ path: `${shotDir}/siblings-desktop-1024-${phase}.png` })
  expect(buttons.map(button => button.getBoundingClientRect().height)).toEqual(original)
  expect(Math.max(...original)).toBeLessThan(44)
  view.unmount()
})
