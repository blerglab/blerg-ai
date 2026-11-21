// BLG-467: charts and diagrams fill their space in a tab and in full screen, instead of sitting
// small in a corner of an otherwise empty one — the real app (SessionDetail, its real CSS and
// dark theme), the same harness PopOutMessage.browser.test.tsx uses, reached through the real
// "Open in a tab" button on a chart drawn in the chat, not a stand-in ArtifactViewer host.
import { act, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
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

const shots = import.meta.env.VITE_VISUAL_REFLOW_SHOTS_DIR
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })

// Explicit width/height, no strokeDash: classifies 'blerg' (classify.ts's SHIPPED_MARKS) — the
// small-chart-in-a-corner case the owner called out.
const BLERG_BAR = JSON.stringify({
  $schema: 'https://vega.github.io/schema/vega-lite/v6.json', width: 300, height: 180,
  data: { values: [{ month: 'Jan', count: 12 }, { month: 'Feb', count: 18 }, { month: 'Mar', count: 9 }] },
  mark: 'bar', encoding: { x: { field: 'month', type: 'nominal' }, y: { field: 'count', type: 'quantitative' } },
})
// BLG-468: a plain flowchart or sequence diagram like this now draws through Blerg's own
// renderers, not Mermaid's <img> — this test is specifically about Mermaid's own fill/scale
// behavior (mermaid.ts's MAX_FILL_SCALE), so it stays on a diagram kind outside both of BLG-468's
// declared subsets (slice 1's flowchart, slice 2's sequence).
const MERMAID = 'stateDiagram-v2\n  [*] --> Start\n  Start --> Review\n  Review --> [*]'

beforeEach(() => {
  localStorage.clear()
  document.documentElement.setAttribute('data-theme', 'dark')
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ id: 'sess-a', kind: 'agent', status: 'idle', title: 'Release chart' })] })
  apiFetch.mockReset().mockImplementation(async (url: string) => {
    if (url.endsWith('/topics')) return json({ topics: [], unmarked: null, strokes: [] })
    if (url.includes('/live')) return json({ can_release_prompt: false })
    if (url.includes('/workspace?')) return json({ result: { ok: true, launch_generation: 'launch', generation: 1, entries: [], truncated: false }, members: [], runtime: 'host' })
    if (url.endsWith('/permissions')) return json({ can_grant: false, reason: '', grants: [], mcp_tools: [], boards: [] })
    return json({})
  })
  vi.stubGlobal('fetch', vi.fn(async () => json([])))
})
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals(); vi.restoreAllMocks(); document.documentElement.removeAttribute('data-theme'); document.body.innerHTML = '' })

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

function seed(text: string) {
  useTranscriptStore.setState({ sessions: {} })
  const transcript = useTranscriptStore.getState()
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'u1', seq: 1, ts: '2026-11-03T12:00:00Z', kind: 'user_message', payload: { text: 'Show me the release chart', source: 'chat' } } as AgentEvent)
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'a1', seq: 2, ts: '2026-11-03T12:00:01Z', kind: 'assistant_text', payload: { text, done: true } } as AgentEvent)
  transcript.ingestReplayDone('sess-a', { lastSeq: 2, hasMore: false })
}

/** The content area a drawn visual has to itself: `.visual-surface`'s own box, minus its padding
 *  (matches Surface's own `available()`, VisualBlock.tsx) — not the wider tab/dialog chrome
 *  around it. */
function contentWidth(surface: Element): number {
  const style = getComputedStyle(surface)
  return surface.getBoundingClientRect().width - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight)
}

/** A tab opening (and the dialog/panel chrome around it settling) can itself resize
 *  `.visual-surface` a couple of times right after it first appears — each one clears and
 *  redraws the chart (Surface's own draw(), VisualBlock.tsx; each redraw's own resize-triggered
 *  follow-up lands a ResizeObserver tick later, not the same frame). Reads past all of that: the
 *  surface's own box, not a point-in-time query result a redraw already in flight could replace
 *  — stable only once the same reading holds for many consecutive frames in a row, not just one. */
async function waitForSettledSurface(viewer: Element): Promise<Element> {
  let last = ''
  let streak = 0
  const deadline = Date.now() + 15_000
  while (Date.now() < deadline) {
    const surface = viewer.querySelector('.visual-surface')
    const rect = surface?.getBoundingClientRect()
    const drawn = surface?.querySelector('[data-chart-renderer="blerg"], canvas, img.visual-diagram')
    const drawnRect = drawn?.getBoundingClientRect()
    // Rounded: sub-pixel float jitter between frames (layout rounding, not a real resize) must
    // not reset the streak forever on a loaded machine.
    const key = JSON.stringify([rect && Math.round(rect.width), rect && Math.round(rect.height), drawnRect && Math.round(drawnRect.width), drawnRect && Math.round(drawnRect.height), !!drawn])
    streak = key === last && drawn && drawnRect!.width > 0 ? streak + 1 : 0
    last = key
    if (streak >= 10) {
      // A frame-stable DOM can still be a frame or two ahead of what the compositor has
      // actually painted for a freshly attached tab; give it real wall-clock time, not just
      // more animation frames, before trusting a screenshot taken right after this returns.
      await new Promise(r => setTimeout(r, 1000))
      return surface!
    }
    await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))
  }
  throw new Error('the tab never settled on a stable size')
}

/** Whether a decoded screenshot shows real variation inside a specific region (the chart or
 *  diagram's own box, in page/CSS pixels — scaled here to the capture's device pixels) — not the
 *  rest of the page's chrome, which paints fine even when this is the part that's gone blank.
 *  Decoded in-browser (an Image onto a canvas) so this can retry before ever trusting a saved
 *  file: a Chromium/CDP quirk this test tripped on leaves a stale, blank compositor frame for a
 *  tab's own late-attached content even once its DOM and layout are already correct, and how
 *  long that takes to resolve is not consistent run to run. */
async function regionIsPainted(base64: string, rect: DOMRect): Promise<boolean> {
  const image = new Image()
  image.src = `data:image/png;base64,${base64}`
  await image.decode()
  const scale = image.naturalWidth / window.innerWidth
  const canvas = document.createElement('canvas')
  canvas.width = image.naturalWidth
  canvas.height = image.naturalHeight
  const context = canvas.getContext('2d')!
  context.drawImage(image, 0, 0)
  const x = Math.max(0, Math.round(rect.left * scale)), y = Math.max(0, Math.round(rect.top * scale))
  const w = Math.max(1, Math.min(canvas.width - x, Math.round(rect.width * scale)))
  const h = Math.max(1, Math.min(canvas.height - y, Math.round(rect.height * scale)))
  const { data } = context.getImageData(x, y, w, h)
  const [r0, g0, b0] = data
  for (let i = 0; i < data.length; i += 4 * 37) { // sampled, not every pixel: this region can still be large
    if (Math.abs(data[i] - r0) + Math.abs(data[i + 1] - g0) + Math.abs(data[i + 2] - b0) > 40) return true
  }
  return false
}

/** A real screenshot at the requested size (CONTRIBUTING.md's own verification step), retried
 *  (jiggling the viewport — same size, one pixel off and back, which forces a fresh composite)
 *  until the region `findRegion` locates actually shows something drawn, not just empty space
 *  the same color throughout. A function, not a captured element: the jiggle can itself trigger
 *  a redraw (Surface's own ResizeObserver, VisualBlock.tsx), replacing the element each time. */
async function screenshot(path: string, width: number, height: number, findRegion: () => Element | null): Promise<void> {
  for (let attempt = 0; attempt < 8; attempt++) {
    await page.viewport(width - 1, height)
    await page.viewport(width, height)
    await new Promise(r => setTimeout(r, 300))
    const result = await page.screenshot({ base64: true })
    const base64 = typeof result === 'string' ? result : result.base64
    const region = findRegion()
    if (region && await regionIsPainted(base64, region.getBoundingClientRect())) break
  }
  await page.screenshot({ path })
}

for (const [width, height] of [[3840, 2160], [1024, 768], [390, 844]] as const) {
  it(`fills a chart's own tab at ${width}x${height}, never past the tab's content width`, async () => {
    await page.viewport(width, height)
    seed('## Release chart\n\nHere is this week\'s release count.\n\n```vega-lite\n' + BLERG_BAR + '\n```\n')
    mount()
    await waitFor(() => expect(document.querySelector('[data-chart-renderer="blerg"]')).not.toBeNull(), { timeout: 20_000 })
    const openButton = await waitFor(() => {
      const button = [...document.querySelectorAll<HTMLButtonElement>('.visual-block .visual-actions button')].find(b => b.getAttribute('aria-label') === 'Open in a tab')
      expect(button).not.toBeUndefined()
      return button!
    })
    await act(async () => { openButton.dispatchEvent(new MouseEvent('click', { bubbles: true })) })
    const viewer = await waitFor(() => { const el = document.querySelector('.hosted-view:not([hidden]) .artifact-body'); expect(el).not.toBeNull(); return el!; }, { timeout: 20_000 })
    await waitFor(() => expect(viewer.querySelector('[data-chart-renderer="blerg"]')).not.toBeNull(), { timeout: 20_000 })
    const surface = await waitForSettledSurface(viewer)
    const { chartWidth, chartBottom } = await waitFor(() => {
      const el = surface.querySelector<HTMLElement>('[data-chart-renderer="blerg"]')
      const rect = el?.getBoundingClientRect()
      expect(rect?.width ?? 0).toBeGreaterThan(0)
      return { chartWidth: rect!.width, chartBottom: rect!.bottom }
    })
    const available = contentWidth(surface)
    expect(chartWidth).toBeLessThanOrEqual(available + 1)
    if (width >= 1024) expect(chartWidth).toBeGreaterThanOrEqual(available * 0.9)
    // The data table disclosure sits below the chart, not squeezed beside it in a flex row.
    const table = surface.querySelector('.visual-blerg-table')
    if (table) expect(table.getBoundingClientRect().top).toBeGreaterThanOrEqual(chartBottom - 1)
    if (shots) await screenshot(`${shots}/tab-chart-${width}.png`, width, height, () => viewer.querySelector('[data-chart-renderer="blerg"]'))
  }, 45_000)

  it(`fills a diagram's own tab at ${width}x${height}, scaling its text up to at most 2x`, async () => {
    await page.viewport(width, height)
    seed('## Release flow\n\n```mermaid\n' + MERMAID + '\n```\n')
    mount()
    await waitFor(() => expect(document.querySelector('img.visual-diagram')).not.toBeNull(), { timeout: 20_000 })
    const openButton = await waitFor(() => {
      const button = [...document.querySelectorAll<HTMLButtonElement>('.visual-block .visual-actions button')].find(b => b.getAttribute('aria-label') === 'Open in a tab')
      expect(button).not.toBeUndefined()
      return button!
    })
    await act(async () => { openButton.dispatchEvent(new MouseEvent('click', { bubbles: true })) })
    const viewer = await waitFor(() => { const el = document.querySelector('.hosted-view:not([hidden]) .artifact-body'); expect(el).not.toBeNull(); return el!; }, { timeout: 20_000 })
    await waitFor(() => { const el = viewer.querySelector<HTMLImageElement>('img.visual-diagram'); expect(el?.naturalWidth).toBeGreaterThan(0) }, { timeout: 20_000 })
    const surface = await waitForSettledSurface(viewer)
    const { diagramWidth, naturalWidth } = await waitFor(() => {
      const el = surface.querySelector<HTMLImageElement>('img.visual-diagram')
      const width = el?.getBoundingClientRect().width ?? 0
      expect(width).toBeGreaterThan(0)
      return { diagramWidth: width, naturalWidth: el!.naturalWidth }
    })
    const available = contentWidth(surface)
    // Never wider than the natural 2x scale cap (mermaid.ts's own MAX_FILL_SCALE), and never
    // wider than the pane itself.
    expect(diagramWidth).toBeLessThanOrEqual(Math.min(available, naturalWidth * 2) + 1)
    // At 1024, the real app's own side panel can still leave less width than the diagram's
    // natural size — correctly shrinking it, not forcing growth past what's actually available.
    if (width >= 3840) expect(diagramWidth).toBeGreaterThan(naturalWidth) // it did grow to fill
    if (shots) await screenshot(`${shots}/tab-mermaid-${width}.png`, width, height, () => viewer.querySelector('img.visual-diagram'))
  }, 45_000)
}
