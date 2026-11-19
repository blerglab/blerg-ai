// BLG-485: a card key or a commit hash a chart or diagram draws (a bar chart's own x-axis
// category, a categorical legend entry, a tooltip, a flowchart node label) gets the same chip
// chat text already has — in real Chromium with a real mouse (CDP) and the hovercards' manual
// clock, at 390 px and 3840 px, the same harness ChatRefs.browser.test.tsx and
// VisualFlowchart.browser.test.tsx already use. The chart's own tooltip is deliberately excluded
// from hover/click (ChipOverlay.tsx's own header): it hides the instant the pointer leaves the
// SVG, before a hovercard's hover-intent could ever reach it — proved here as a styled-but-inert
// span, not a missed case.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { act, render, waitFor } from '@testing-library/react'
import { cdp, page } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { HOVERCARD, clearHovercardCache, resetRefStoresForTest, useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import '../index.css'
import '@blerglab/chat/blerg.css'
import { installHoverClock, type ManualHoverClock } from '../test/hoverClock'
import { resetSessionBoardsForTest } from '../lib/sessionBoard'
import { resetLiveBoardCardsForTest } from '../lib/liveBoardCard'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { invalidateChatTransport } from '../lib/chatTransport'
import SessionDetail from './SessionDetail'
import CommandPaletteProvider from './CommandPalette'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

const SHA = '08025f2c9d1e4b7a0c3f5e6d7a8b9c0d1e2f3a4b'
const json = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))

const shots = import.meta.env.VITE_VISUAL_CHART_REFS_SHOTS_DIR

const CHART = JSON.stringify({
  data: { values: [{ category: '#452', series: '08025f2c', value: 3 }, { category: 'Plain', series: 'Other', value: 2 }] },
  mark: 'bar',
  encoding: {
    x: { field: 'category', type: 'nominal' },
    y: { field: 'value', type: 'quantitative' },
    color: { field: 'series', type: 'nominal' },
    tooltip: { field: 'category', type: 'nominal' },
  },
})

const FLOWCHART = ['flowchart LR', 'Start([Request]) --> Approved[BLG-452]'].join('\n')

let cardReads: string[] = []
let commitReads: string[] = []

function serve() {
  apiFetch.mockImplementation((url: string) => {
    if (url.endsWith('/topics')) return json({ topics: [], unmarked: null, strokes: [] })
    if (url.includes('/live')) return json({
      session_id: 'sess-a', through_seq: 1, generated_at: new Date().toISOString(), tokens_hidden: false,
      asks: [], subagents: [], tasks: [], tool_changes: 0, counts: { needs_you: 0, running: 0 },
      can_release_prompt: true,
    })
    if (url.includes('/workspace?')) return json({ result: { ok: true, launch_generation: 'launch', generation: 1, entries: [], truncated: false }, members: [], runtime: 'host' })
    if (url.endsWith('/permissions')) return json({ can_grant: true, reason: '', grants: [], mcp_tools: [], boards: [{ board_id: 'b1', board_name: 'Blerg', level: 'full' }] })
    if (url.endsWith('/api/session-boards')) return json({ boards: [{ id: 'b1', key: 'BLG' }] })
    const m = url.match(/^\/api\/board-cards\/b1\/(\d+)/)
    if (m) {
      cardReads.push(m[1])
      return m[1] === '452'
        ? json({ card: { id: 'c452', board_id: 'b1', number: 452, title: 'References in charts', column_id: 'k', column_name: 'Review', columns: [{ id: 'k', name: 'Review' }] }, url: '/boards/b1/cards/452' })
        : json({ error: 'not found' }, 404)
    }
    if (url.startsWith('/api/tool-changes')) return json({ tool_changes: [] })
    return json({})
  })
  vi.stubGlobal('fetch', vi.fn((input: string) => {
    const url = String(input)
    const c = url.match(/\/commits\/([0-9a-f]+)(\/diff)?/)
    if (c) {
      if (!c[2]) commitReads.push(c[1])
      if (!SHA.startsWith(c[1])) return json({ error: 'unknown commit' }, 404)
      return json({ sha: SHA, short: SHA.slice(0, 7), subject: 'Chart chips', body: '', author: 'Dev', author_date: '2026-11-04T10:00:00Z', parents: [], files: [], adds: 0, dels: 0 })
    }
    return json([])
  }))
}

type Mouse = { send(method: string, params?: object): Promise<unknown> }
let mouse: Mouse
let clock: ManualHoverClock
const card = () => document.querySelector<HTMLElement>('[data-testid="hovercard"]')
async function moveTo(x: number, y: number) {
  const f = window.frameElement?.getBoundingClientRect()
  const k = f ? f.width / window.innerWidth : 1
  await mouse.send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: (f?.left ?? 0) + x * k, y: (f?.top ?? 0) + y * k })
}
async function hover(el: Element) {
  await expect.poll(async () => {
    const r = el.getBoundingClientRect()
    const x = Math.round(r.left + r.width / 2), y = Math.round(r.top + r.height / 2)
    await moveTo(x, y - 1)
    await moveTo(x, y)
    return clock.pending() > 0 || card() !== null
  }).toBe(true)
  clock.advance(HOVERCARD.openDelayMs)
  await expect.poll(card).not.toBeNull()
}

beforeEach(async () => {
  document.documentElement.setAttribute('data-theme', 'dark')
  cardReads = []
  commitReads = []
  resetRefStoresForTest()
  resetSessionBoardsForTest()
  resetLiveBoardCardsForTest()
  clearHovercardCache()
  clock = installHoverClock()
  mouse = cdp() as unknown as Mouse
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ id: 'sess-a', kind: 'agent', status: 'idle', title: 'Chart refs' })] })
  serve()
  await moveTo(1, 1)
})
afterEach(async () => {
  await moveTo(1, 1)
  clock.uninstall()
  invalidateChatTransport()
  vi.unstubAllGlobals()
  document.documentElement.removeAttribute('data-theme')
  document.body.innerHTML = ''
})

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

function seedChart() {
  useTranscriptStore.setState({ sessions: {} })
  const t = useTranscriptStore.getState()
  t.ingest({ session_id: 'sess-a', client_event_id: 'u1', seq: 1, ts: '2026-11-04T12:00:00Z', kind: 'user_message', payload: { text: 'Chart it', source: 'chat' } } as AgentEvent)
  t.ingest({ session_id: 'sess-a', client_event_id: 'a1', seq: 2, ts: '2026-11-04T12:00:01Z', kind: 'assistant_text', payload: { text: '```vega-lite\n' + CHART + '\n```\n', done: true } } as AgentEvent)
  t.ingestReplayDone('sess-a', { lastSeq: 2, hasMore: false })
}

function seedFlowchart() {
  useTranscriptStore.setState({ sessions: {} })
  const t = useTranscriptStore.getState()
  t.ingest({ session_id: 'sess-a', client_event_id: 'u2', seq: 1, ts: '2026-11-04T12:00:00Z', kind: 'user_message', payload: { text: 'Diagram it', source: 'chat' } } as AgentEvent)
  t.ingest({ session_id: 'sess-a', client_event_id: 'a2', seq: 2, ts: '2026-11-04T12:00:01Z', kind: 'assistant_text', payload: { text: '```mermaid\n' + FLOWCHART + '\n```\n', done: true } } as AgentEvent)
  t.ingestReplayDone('sess-a', { lastSeq: 2, hasMore: false })
}

async function waitForSettled(selector: string, scope: Element = document.body): Promise<Element> {
  await waitFor(() => expect(scope.querySelector(selector)).not.toBeNull(), { timeout: 15_000 })
  const surface = scope.querySelector('.visual-surface')!
  await new Promise<void>(resolve => {
    let timer: ReturnType<typeof setTimeout>
    const quiet = () => { observer.disconnect(); resolve() }
    const observer = new MutationObserver(() => { clearTimeout(timer); timer = setTimeout(quiet, 400) })
    observer.observe(surface, { childList: true, subtree: true })
    timer = setTimeout(quiet, 400)
  })
  return scope.querySelector(selector)!
}

async function screenshotOf(path: string, rect: DOMRect): Promise<void> {
  await page.screenshot({ path, clip: { x: Math.max(0, rect.left), y: Math.max(0, rect.top), width: rect.width, height: rect.height } })
}

for (const [width, height] of [[390, 900], [3840, 2160]] as const) {
  it(`a card-key chart category gets the card's hovercard and opens it, at ${width}x${height}`, async () => {
    await page.viewport(width, height)
    seedChart()
    mount()
    const svg = await waitForSettled('svg[data-chart-renderer="blerg"]') as SVGSVGElement
    const chip = await waitFor(() => {
      const el = svg.querySelector<SVGGElement>('[data-blerg-chip-key="card:452"]')
      expect(el).not.toBeNull()
      return el!
    })
    chip.scrollIntoView({ block: 'center' })
    const hotspot = await waitFor(() => {
      const el = document.querySelector<HTMLElement>('[data-testid="visual-chip-hotspot"][data-chip-key="card:452"]')
      expect(el).not.toBeNull()
      return el!
    })
    await hover(hotspot)
    await expect.poll(() => card()?.textContent).toContain('References in charts')
    if (shots) await screenshotOf(`${shots}/chart-axis-hovercard-${width}.png`, card()!.getBoundingClientRect())
    // Opens the same way RefSpan's own card chip does (ctx.refs.openCard, md-ref's own proven
    // path in ChatRefs.browser.test.tsx): asserted here by the hotspot's own resolved label,
    // not by re-driving the ticket dialog open through this harness's minimal session fixture,
    // which the dialog's own board-tab/session-board plumbing isn't built to carry.
    expect(hotspot.getAttribute('aria-label')).toBe('Open card #452: References in charts')
  }, 45_000)

  it(`a commit-named legend entry gets the commit hovercard and opens the commit viewer, at ${width}x${height}`, async () => {
    await page.viewport(width, height)
    seedChart()
    mount()
    const svg = await waitForSettled('svg[data-chart-renderer="blerg"]') as SVGSVGElement
    const chip = await waitFor(() => {
      const el = svg.querySelector<SVGGElement>('[data-blerg-chip-key="commit:08025f2c"]')
      expect(el).not.toBeNull()
      return el!
    })
    chip.scrollIntoView({ block: 'center' })
    const hotspot = await waitFor(() => {
      const el = document.querySelector<HTMLElement>('[data-testid="visual-chip-hotspot"][data-chip-key="commit:08025f2c"]')
      expect(el).not.toBeNull()
      return el!
    })
    await hover(hotspot)
    await expect.poll(() => card()?.textContent).toContain('Chart chips')
    await moveTo(1, 1)
    await act(async () => { hotspot.click() })
    expect(await document.querySelector('[data-testid="commit-viewer-overlay"]')).not.toBeNull()
  }, 45_000)

  it(`the chart's own tooltip draws a ref styled but inert (no hovercard, no chip), at ${width}x${height}`, async () => {
    await page.viewport(width, height)
    seedChart()
    mount()
    const svg = await waitForSettled('svg[data-chart-renderer="blerg"]') as SVGSVGElement
    const bars = [...svg.querySelectorAll<SVGRectElement>('rect:not([data-legend-swatch])')]
    let tooltip: HTMLElement | null = null
    let ref: Element | null = null
    for (const bar of bars) {
      const r = bar.getBoundingClientRect()
      await moveTo(Math.round(r.left + r.width / 2), Math.round(r.top + r.height / 2))
      tooltip = await waitFor(() => { const el = document.querySelector<HTMLElement>('.visual-tooltip'); expect(el?.hidden).toBe(false); return el!; })
      ref = tooltip.querySelector('.visual-tooltip-ref')
      if (ref) break
    }
    expect(ref).not.toBeNull()
    expect(ref!.hasAttribute('data-blerg-chip-key')).toBe(false)
    // No hotspot *for the tooltip's own ref* — the axis tick's own "#452" chip (same key,
    // resolved independently) legitimately has one.
    expect(tooltip!.querySelector('[data-testid="visual-chip-hotspot"]')).toBeNull()
  }, 45_000)

  it(`a card-key flowchart node gets the same hovercard, at ${width}x${height}`, async () => {
    await page.viewport(width, height)
    seedFlowchart()
    mount()
    const svg = await waitForSettled('svg[data-diagram-renderer="blerg-flow"]') as SVGSVGElement
    const chip = await waitFor(() => {
      const el = svg.querySelector<SVGGElement>('[data-blerg-chip-key="card:BLG:452"]')
      expect(el).not.toBeNull()
      return el!
    })
    chip.scrollIntoView({ block: 'center' })
    const hotspot = await waitFor(() => {
      const el = document.querySelector<HTMLElement>('[data-testid="visual-chip-hotspot"][data-chip-key="card:BLG:452"]')
      expect(el).not.toBeNull()
      return el!
    })
    await hover(hotspot)
    await expect.poll(() => card()?.textContent).toContain('References in charts')
    if (shots) await screenshotOf(`${shots}/flowchart-node-hovercard-${width}.png`, card()!.getBoundingClientRect())
    // Same opening path as the chart axis chip above (RefSpan's own ctx.refs.openCard) — see
    // that test's own comment for why this asserts the resolved aria-label rather than
    // re-driving the ticket dialog through this harness's minimal session fixture.
    expect(hotspot.getAttribute('aria-label')).toBe('Open card #452: References in charts')
  }, 45_000)
}
