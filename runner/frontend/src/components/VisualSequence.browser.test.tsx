// BLG-468 (slice 2): our own sequence renderer, in the real app (SessionDetail, its real CSS and
// dark theme) — mirrors VisualFlowchart.browser.test.tsx's own harness and assertions exactly,
// for the sequence grammar: draws through our own renderer (not Mermaid's <img>), hover/keyboard
// focus work, a card-key chip is a real focusable element, and the layout fills the space it's
// given in both chat and a tab.
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

const shots = import.meta.env.VITE_VISUAL_SEQUENCE_SHOTS_DIR

const SEQUENCE = [
  'sequenceDiagram',
  'participant Client as BLG-468',
  'participant API',
  'Client->>+API: Request',
  'API-->>-Client: Response',
  'Note right of API: Cache checked',
  'loop Retry',
  'Client->>API: Retry',
  'end',
].join('\n')

beforeEach(() => {
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

function seed() {
  useTranscriptStore.setState({ sessions: {} })
  const transcript = useTranscriptStore.getState()
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'u1', seq: 1, ts: '2026-11-03T12:00:00Z', kind: 'user_message', payload: { text: 'Show the call sequence', source: 'chat' } } as AgentEvent)
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'a1', seq: 2, ts: '2026-11-03T12:00:01Z', kind: 'assistant_text', payload: { text: '## Sequence\n\n```mermaid\n' + SEQUENCE + '\n```\n', done: true } } as AgentEvent)
  transcript.ingestReplayDone('sess-a', { lastSeq: 2, hasMore: false })
}

/** Same discipline as VisualFlowchart.browser.test.tsx's own helper, for the sequence renderer's
 *  own `data-diagram-renderer` value. */
async function waitForSettled(scope: Element = document.body): Promise<SVGSVGElement> {
  await waitFor(() => expect(scope.querySelector('svg[data-diagram-renderer="blerg-sequence"]')).not.toBeNull(), { timeout: 15_000 })
  const surface = scope.querySelector('.visual-surface')!
  await new Promise<void>(resolve => {
    let timer: ReturnType<typeof setTimeout>
    const quiet = () => { observer.disconnect(); resolve() }
    const observer = new MutationObserver(() => { clearTimeout(timer); timer = setTimeout(quiet, 400) })
    observer.observe(surface, { childList: true, subtree: true })
    timer = setTimeout(quiet, 400)
  })
  return scope.querySelector<SVGSVGElement>('svg[data-diagram-renderer="blerg-sequence"]')!
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
  it(`fills the message's own width in chat, no inner scrollbar, at ${width}x${height}`, async () => {
    await page.viewport(width, height)
    seed()
    mount()
    const svg = await waitForSettled()
    const surface = svg.closest<HTMLElement>('.visual-surface')!
    const surfaceStyle = getComputedStyle(surface)
    const availableWidth = surface.clientWidth - Number.parseFloat(surfaceStyle.paddingLeft) - Number.parseFloat(surfaceStyle.paddingRight)
    // Review (post-slice-2): chat shrinks to the available width when the diagram's own natural
    // size (its viewBox, fixed regardless of fit) is wider than it, but never grows past natural
    // the other way round — the exact bug this review round found, on this renderer's own
    // chat-3840 screenshot: a two-participant diagram blown up ~6x in a very wide chat message.
    const viewBoxWidth = Number(svg.getAttribute('viewBox')!.split(' ')[2])
    const expectedWidth = Math.min(viewBoxWidth, availableWidth)
    expect(svg.getBoundingClientRect().width).toBeGreaterThanOrEqual(expectedWidth - 2)
    expect(svg.getBoundingClientRect().width).toBeLessThanOrEqual(expectedWidth + 2)
    expect(svg.getBoundingClientRect().width).toBeLessThanOrEqual(viewBoxWidth + 2)
    expect(surfaceStyle.maxHeight).toBe('none')
    expect(surface.scrollHeight).toBeLessThanOrEqual(surface.clientHeight + 1)

    // Hover and keyboard focus: a participant announces into the aria-live region on focus —
    // asserted immediately after focus, not through waitFor (VisualFlowchart.browser.test.tsx's
    // own round-4 flakiness lesson: a later, unrelated mouseenter could otherwise overwrite it
    // before a polling assertion ever reads the correct value).
    const live = svg.parentElement!.querySelector('[aria-live="polite"]')!
    const chip = svg.querySelector<SVGElement>('[data-blerg-chip-key]')!
    expect(chip.getAttribute('role')).toBe('button')
    await act(async () => { (chip as unknown as HTMLElement).focus() })
    expect(live.textContent).toBe('Open BLG-468')

    // Both messages and the activation bar actually drew (not a silent empty diagram).
    expect(svg.querySelectorAll('[data-blerg-seq-message]')).toHaveLength(3)
    expect(svg.querySelector('[data-blerg-seq-block="loop"]')).not.toBeNull()

    if (shots) {
      const finalSvg = document.querySelector<SVGSVGElement>('svg[data-diagram-renderer="blerg-sequence"]')!
      await screenshot(`${shots}/sequence-chat-${width}.png`, finalSvg.getBoundingClientRect())
    }
  }, 45_000)

  it(`fills its own tab, at ${width}x${height}`, async () => {
    await page.viewport(width, height)
    seed()
    mount()
    const chatSvg = await waitForSettled()
    const chatWidth = chatSvg.getBoundingClientRect().width
    const naturalWidth = Number(chatSvg.getAttribute('viewBox')!.split(' ')[2])
    const naturalHeight = Number(chatSvg.getAttribute('viewBox')!.split(' ')[3])
    const openButton = await waitFor(() => {
      const button = [...document.querySelectorAll<HTMLButtonElement>('.visual-block .visual-actions button')].find(b => b.getAttribute('aria-label') === 'Open in a tab')
      expect(button).not.toBeUndefined()
      return button!
    })
    await act(async () => { openButton.dispatchEvent(new MouseEvent('click', { bubbles: true })) })
    const viewer = await waitFor(() => { const el = document.querySelector('.hosted-view:not([hidden]) .artifact-body'); expect(el).not.toBeNull(); return el!; }, { timeout: 20_000 })
    const svg = await waitForSettled(viewer)
    const surface = svg.closest<HTMLElement>('.visual-surface')!
    const tabWidth = svg.getBoundingClientRect().width
    const tabHeight = svg.getBoundingClientRect().height
    const availableWidth = surface.getBoundingClientRect().width
    const availableHeight = surface.getBoundingClientRect().height
    expect(tabWidth).toBeLessThanOrEqual(availableWidth + 1)
    expect(tabHeight).toBeLessThanOrEqual(availableHeight + 1)
    const rect = svg.getBoundingClientRect(), surfaceRect = surface.getBoundingClientRect()
    expect(Math.abs((rect.left - surfaceRect.left) - (surfaceRect.right - rect.right))).toBeLessThanOrEqual(20)
    // The tab always grows at least as large as chat's own (never-larger-than-natural) size —
    // within the bounded refinement loop's own ~2% convergence tolerance, not exact.
    expect(tabWidth).toBeGreaterThanOrEqual(chatWidth * 0.95)
    // Capped at MAX_FILL_SCALE (2x) — reinstated now that chat itself never grows past 1x
    // (render.ts's own header), so a tab no longer needs to out-grow an uncapped chat. Checked on
    // whichever dimension is naturally the one with the most room: a sequence diagram's own
    // timeline-of-messages shape is tall even with few participants, so height can be the tighter
    // bound at a narrow viewport, while width has the most room at the widest one.
    const grewToWidthCap = tabWidth <= naturalWidth * 2 * 1.1 && tabWidth >= Math.min(naturalWidth * 1.5, availableWidth * 0.9)
    const grewToHeightCap = tabHeight <= naturalHeight * 2 * 1.1 && tabHeight >= Math.min(naturalHeight * 1.5, availableHeight * 0.9)
    expect(grewToWidthCap || grewToHeightCap, `width ${tabWidth} (natural ${naturalWidth}), height ${tabHeight} (natural ${naturalHeight})`).toBe(true)
    expect(surface.scrollHeight).toBeLessThanOrEqual(surface.clientHeight + 8)

    if (shots) await screenshot(`${shots}/sequence-tab-${width}.png`, svg.getBoundingClientRect())
  }, 45_000)
}

it('in chat at 3840, a participant label\'s rendered font size never exceeds its natural size', async () => {
  // The exact bug this review round found: a two-participant diagram like this fixture, in a
  // very wide chat message, had its text blown up several times over and ran off the message's
  // bottom. Same measurement as VisualFlowchart.browser.test.tsx's own equivalent test.
  await page.viewport(3840, 2160)
  seed()
  mount()
  const svg = await waitForSettled()
  const viewBoxWidth = Number(svg.getAttribute('viewBox')!.split(' ')[2])
  const renderScale = svg.getBoundingClientRect().width / viewBoxWidth
  expect(renderScale).toBeLessThanOrEqual(1.01)
  const label = svg.querySelector('[data-blerg-seq-participant] text')!
  const naturalFontSize = Number(label.getAttribute('font-size'))
  const renderedFontSize = naturalFontSize * renderScale
  expect(renderedFontSize).toBeLessThanOrEqual(naturalFontSize * 1.01)
}, 45_000)
