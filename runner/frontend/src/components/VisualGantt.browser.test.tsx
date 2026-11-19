// BLG-471: our own gantt renderer, in the real app (SessionDetail, its real CSS and dark theme) —
// mirrors VisualFlowchart.browser.test.tsx's and VisualSequence.browser.test.tsx's own harness,
// for the gantt grammar's own chrome: draws through our own renderer (not Mermaid's <img>), the
// chart scrolls horizontally with its lane names pinned rather than shrinking to fit, zoom
// buttons rerun layout, hover/keyboard focus announce a task's dates and highlight its
// dependency, a card-key task is a real focusable chip, and a today line is present.
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

const shots = import.meta.env.VITE_VISUAL_GANTT_SHOTS_DIR

// A realistic plan: sections, durations, dependencies, a milestone and a critical path — close
// to the real 0.6.0 Mermaid gantt the owner drew by hand (BLG-471's own motivation).
const GANTT = [
  'gantt',
  'title 0.6.0 release plan',
  'dateFormat YYYY-MM-DD',
  'axisFormat %m-%d',
  'section Design',
  'Spec review :done, spec, 2026-10-20, 3d',
  'BLG-468 :crit, active, blg468, after spec, 5d',
  'section Build',
  'Workers epic :crit, workers, after blg468, 7d',
  'Perf pass :perfpass, after workers, 3d',
  'section Release',
  'Cut 0.6.0 :milestone, cut, after perfpass, 0d',
].join('\n')

beforeEach(() => {
  // Keep today inside the fixed release plan without faking animation/poll timers.
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date('2026-10-28T12:00:00Z'))
  localStorage.clear()
  document.documentElement.setAttribute('data-theme', 'dark')
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ id: 'sess-a', kind: 'agent', status: 'idle', title: 'Diagram renderer' })] })
  apiFetch.mockReset().mockImplementation(async (url: string) => {
    if (url.endsWith('/topics')) return new Response(JSON.stringify({ topics: [], unmarked: null, strokes: [] }), { headers: { 'Content-Type': 'application/json' } })
    if (url.includes('/live')) return new Response(JSON.stringify({ can_release_prompt: false }), { headers: { 'Content-Type': 'application/json' } })
    if (url.includes('/workspace?')) return new Response(JSON.stringify({ result: { ok: true, launch_generation: 'launch', generation: 1, entries: [], truncated: false }, members: [], runtime: 'host' }), { headers: { 'Content-Type': 'application/json' } })
    if (url.endsWith('/permissions')) return new Response(JSON.stringify({ can_grant: false, reason: '', grants: [], mcp_tools: [], boards: [] }), { headers: { 'Content-Type': 'application/json' } })
    return new Response(JSON.stringify({}), { headers: { 'Content-Type': 'application/json' } })
  })
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify([]), { headers: { 'Content-Type': 'application/json' } })))
})
afterEach(() => { vi.useRealTimers(); invalidateChatTransport(); vi.unstubAllGlobals(); vi.restoreAllMocks(); document.documentElement.removeAttribute('data-theme'); document.body.innerHTML = '' })

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

function seed() {
  useTranscriptStore.setState({ sessions: {} })
  const transcript = useTranscriptStore.getState()
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'u1', seq: 1, ts: '2026-11-03T12:00:00Z', kind: 'user_message', payload: { text: 'Show the release plan', source: 'chat' } } as AgentEvent)
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'a1', seq: 2, ts: '2026-11-03T12:00:01Z', kind: 'assistant_text', payload: { text: '## Release plan\n\n```mermaid\n' + GANTT + '\n```\n', done: true } } as AgentEvent)
  transcript.ingestReplayDone('sess-a', { lastSeq: 2, hasMore: false })
}

/** Same discipline as VisualFlowchart.browser.test.tsx's own helper, for the gantt renderer's own
 *  `data-diagram-renderer` value. */
async function waitForSettled(scope: Element = document.body): Promise<SVGSVGElement> {
  await waitFor(() => expect(scope.querySelector('svg[data-diagram-renderer="blerg-gantt"]')).not.toBeNull(), { timeout: 15_000 })
  const surface = scope.querySelector('.visual-surface')!
  await new Promise<void>(resolve => {
    let timer: ReturnType<typeof setTimeout>
    const quiet = () => { observer.disconnect(); resolve() }
    const observer = new MutationObserver(() => { clearTimeout(timer); timer = setTimeout(quiet, 400) })
    observer.observe(surface, { childList: true, subtree: true })
    timer = setTimeout(quiet, 400)
  })
  return scope.querySelector<SVGSVGElement>('svg[data-diagram-renderer="blerg-gantt"]')!
}

async function screenshot(path: string, rect: DOMRect): Promise<void> {
  for (let attempt = 0; attempt < 8; attempt++) {
    const result = await page.screenshot({ base64: true })
    const base64 = typeof result === 'string' ? result : result.base64
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
    let painted = false
    for (let i = 0; i < data.length; i += 4 * 37) {
      if (Math.abs(data[i] - r0) + Math.abs(data[i + 1] - g0) + Math.abs(data[i + 2] - b0) > 40) { painted = true; break }
    }
    if (painted) break
    await new Promise(r => setTimeout(r, 300))
  }
  await page.screenshot({ path })
}

for (const [width, height] of [[3840, 2160], [1024, 768], [390, 844]] as const) {
  it(`draws in chat, scrollable with pinned lanes, at ${width}x${height}`, async () => {
    await page.viewport(width, height)
    seed()
    mount()
    const svg = await waitForSettled()
    const scrollHost = svg.closest<HTMLElement>('.visual-blerg-gantt-scroll')!
    // Our own chrome, not Mermaid's <img>: a scrollable host with the lane column pinned inside
    // its own SVG (draw.ts), zoom buttons, and the chart's own natural width (never shrunk — the
    // whole reason it scrolls instead, ticket's own "a long range scrolls horizontally" line).
    expect(getComputedStyle(scrollHost).overflowX).toBe('auto')
    expect(svg.querySelector('[data-blerg-gantt-lane-column]')).not.toBeNull()
    expect(svg.querySelector('[data-blerg-gantt-today]')).not.toBeNull()
    const surface = svg.closest<HTMLElement>('.visual-surface')!
    // The zoom toolbar lives in VisualBlock's own `.visual-actions` row (the app's existing
    // chrome, a sibling of `.visual-surface`), never floating over the drawing itself.
    const block = svg.closest<HTMLElement>('.visual-block')!
    expect(block.querySelector('.visual-actions button[aria-label="Zoom in"]')).not.toBeNull()
    expect(block.querySelector('.visual-actions button[aria-label="Zoom out"]')).not.toBeNull()
    expect(block.querySelector('.visual-actions button[aria-label="Fit to width"]')).not.toBeNull()

    // Hover/keyboard focus: a task announces its name/dates/duration into the live region, and
    // highlights its own dependency's bar — asserted immediately after the event, not through
    // waitFor (VisualFlowchart.browser.test.tsx's own round-4 flakiness lesson).
    const live = svg.closest('.visual-blerg-gantt')!.querySelector('[aria-live="polite"]')!
    const blg468 = svg.querySelector<SVGElement>('[data-blerg-gantt-task="blg468"]')!
    await act(async () => { (blg468 as unknown as HTMLElement).focus() })
    expect(live.textContent).toContain('BLG-468')
    expect(live.textContent).toContain('critical')
    const specShape = svg.querySelector('[data-blerg-gantt-bar-shape="spec"]')!
    expect(specShape.getAttribute('stroke')).toBe(specShape.getAttribute('data-blerg-gantt-stroke-highlight'))

    // A bar naming a card key is a real, keyboard-reachable chip.
    const chip = svg.querySelector<SVGElement>('[data-blerg-chip-key]')!
    expect(chip.getAttribute('role')).toBe('button')
    expect(chip.getAttribute('data-blerg-gantt-task')).toBe('blg468')

    // Every task actually drew (not a silent empty chart).
    expect(svg.querySelectorAll('[data-blerg-gantt-task]')).toHaveLength(5)
    expect(svg.querySelectorAll('polygon').length).toBeGreaterThan(0) // the milestone

    if (shots) await screenshot(`${shots}/gantt-chat-${width}.png`, surface.getBoundingClientRect())
  }, 45_000)

  it(`fills its own tab, at ${width}x${height}`, async () => {
    await page.viewport(width, height)
    seed()
    mount()
    await waitForSettled()
    const openButton = await waitFor(() => {
      const button = [...document.querySelectorAll<HTMLButtonElement>('.visual-block .visual-actions button')].find(b => b.getAttribute('aria-label') === 'Open in a tab')
      expect(button).not.toBeUndefined()
      return button!
    })
    await act(async () => { openButton.dispatchEvent(new MouseEvent('click', { bubbles: true })) })
    const viewer = await waitFor(() => { const el = document.querySelector('.hosted-view:not([hidden]) .artifact-body'); expect(el).not.toBeNull(); return el!; }, { timeout: 20_000 })
    const svg = await waitForSettled(viewer)
    const surface = svg.closest<HTMLElement>('.visual-surface')!
    const scrollHost = svg.closest<HTMLElement>('.visual-blerg-gantt-scroll')!
    expect(getComputedStyle(scrollHost).overflowX).toBe('auto')
    // Fit-to-width on open (render.ts): the drawn chart is close to the tab's own available
    // width, not left at its natural chat-sized width inside a much bigger pane.
    const availableWidth = surface.getBoundingClientRect().width
    expect(Number(svg.getAttribute('width'))).toBeLessThanOrEqual(availableWidth + 2)
    expect(Number(svg.getAttribute('width'))).toBeGreaterThanOrEqual(availableWidth * 0.6)

    // Text/rows actually grew with the tab (review round 2's own "stays at natural size" finding)
    // — never past MAX_FILL_SCALE (2x), bounded by the available height.
    const barLabel = svg.querySelector('[data-blerg-gantt-task] text')!
    const tabFontSize = Number(barLabel.getAttribute('font-size'))
    const CHAT_FONT_SIZE = 14 * 0.9 // draw.ts's own task-label ratio, at the fixture's 14px theme
    expect(tabFontSize).toBeGreaterThan(CHAT_FONT_SIZE)
    expect(tabFontSize).toBeLessThanOrEqual(CHAT_FONT_SIZE * 2 * 1.1)
    expect(svg.getBoundingClientRect().height).toBeLessThanOrEqual(surface.getBoundingClientRect().height + 2)

    // The toolbar lives in the tab's own ViewerToolbar (ViewerActionsContext), not floating over
    // the drawing and not duplicated in VisualBlock's own `.visual-actions` row while hosted.
    const dialog = viewer.closest('.artifact-dialog')!
    expect(dialog.querySelector('.viewer-toolbar button[aria-label="Zoom in"]')).not.toBeNull()
    expect(dialog.querySelector('.visual-actions button[aria-label="Zoom in"]')).toBeNull()

    if (shots) await screenshot(`${shots}/gantt-tab-${width}.png`, surface.getBoundingClientRect())
  }, 45_000)
}

it('clicking Zoom in reruns layout at a wider pxPerDay, in the real app', async () => {
  await page.viewport(1024, 768)
  seed()
  mount()
  const svg = await waitForSettled()
  const before = Number(svg.getAttribute('width'))
  const surface = svg.closest<HTMLElement>('.visual-surface')!
  const block = svg.closest<HTMLElement>('.visual-block')!
  const zoomIn = block.querySelector<HTMLButtonElement>('.visual-actions button[aria-label="Zoom in"]')!
  await act(async () => { zoomIn.dispatchEvent(new MouseEvent('click', { bubbles: true })) })
  const after = Number(surface.querySelector<SVGSVGElement>('svg[data-diagram-renderer="blerg-gantt"]')!.getAttribute('width'))
  expect(after).toBeGreaterThan(before)
}, 45_000)

it('side by side with Mermaid\'s own render of the same source (real <img>, no chrome)', async () => {
  await page.viewport(1600, 1000)
  seed()
  mount()
  const ours = await waitForSettled()
  const oursSurface = ours.closest<HTMLElement>('.visual-surface')!
  const mermaidHost = document.createElement('div')
  mermaidHost.style.cssText = 'margin-top:24px;border-top:1px dashed #666;padding-top:16px'
  document.body.append(mermaidHost)
  const { renderMermaid } = await import('../../../../packages/chat/src/visual/mermaid')
  const { readVisualTheme } = await import('../../../../packages/chat/src/visual/theme')
  await renderMermaid(GANTT, mermaidHost, readVisualTheme(oursSurface), { available: { width: oursSurface.clientWidth, height: 0 }, fill: false })
  const img = await waitFor(() => { const el = mermaidHost.querySelector('img'); expect(el).not.toBeNull(); return el!; }, { timeout: 15_000 })
  await waitFor(() => expect(img.naturalWidth).toBeGreaterThan(0), { timeout: 15_000 })
  if (import.meta.env.VITE_VISUAL_GANTT_SHOTS_DIR) {
    const dir = import.meta.env.VITE_VISUAL_GANTT_SHOTS_DIR
    // The app's own root is a fixed 100vh flex layout (mount(), above) — appended after it,
    // mermaidHost sits below the fold rather than inside that flow. Two same-width shots (ours,
    // then Mermaid's own, scrolled into view) side by side on disk, rather than one combined
    // capture racing the page's own scroll position.
    await screenshot(`${dir}/gantt-side-by-side-ours.png`, oursSurface.getBoundingClientRect())
    mermaidHost.scrollIntoView()
    await new Promise(r => setTimeout(r, 50))
    await screenshot(`${dir}/gantt-side-by-side-mermaid.png`, mermaidHost.getBoundingClientRect())
  }
  mermaidHost.remove()
}, 45_000)
