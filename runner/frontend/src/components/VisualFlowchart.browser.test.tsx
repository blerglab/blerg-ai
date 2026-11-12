// BLG-468: our own flowchart renderer, in the real app (SessionDetail, its real CSS and dark
// theme) — the same harness PopOutMessage.browser.test.tsx and VisualReflow.browser.test.tsx
// already use. Proves, in a real browser, both in a chat message and in a tab: the diagram draws
// through our own renderer (not Mermaid's <img>), hover/keyboard focus work, a card-key chip is a
// real focusable element, and the layout fills the space it's given — reflowing (a real
// re-layout, not a CSS scale) to top-to-bottom only when the authored direction doesn't fit.
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

const shots = import.meta.env.VITE_VISUAL_FLOWCHART_SHOTS_DIR

const FLOWCHART = [
  'flowchart LR',
  'Start([Request]) --> Review{Review}',
  'Review -->|approve| Approved[BLG-468]',
  'Review -->|reject| Start',
  'subgraph pipeline[Pipeline]',
  'Approved --> Build[[Build]]',
  'Build --> Ship((Ship))',
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
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'u1', seq: 1, ts: '2026-11-03T12:00:00Z', kind: 'user_message', payload: { text: 'Show the pipeline', source: 'chat' } } as AgentEvent)
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'a1', seq: 2, ts: '2026-11-03T12:00:01Z', kind: 'assistant_text', payload: { text: '## Pipeline\n\n```mermaid\n' + FLOWCHART + '\n```\n', done: true } } as AgentEvent)
  transcript.ingestReplayDone('sess-a', { lastSeq: 2, hasMore: false })
}

/** Live resize-redraw (VisualBlock.tsx) replaces the host div (and the SVG inside it) entirely on
 *  every redraw, including a few right after first mount as the surrounding page settles — so a
 *  reference taken mid-settle can go stale moments later. Waits for an actual quiet period (no
 *  DOM mutation under `scope` for a full 500ms), not just one matching poll, then returns a fresh
 *  query — the only reference the caller should treat as stable. */
async function waitForSettled(scope: Element = document.body): Promise<SVGSVGElement> {
  await waitFor(() => expect(scope.querySelector('svg[data-diagram-renderer="blerg-flow"]')).not.toBeNull(), { timeout: 15_000 })
  // Scoped to .visual-surface itself (not the whole page, which has unrelated churn — a live
  // timestamp, focus-visible toggles) and to childList only: a redraw replaces Surface's own
  // `host` div wholesale (VisualBlock.tsx), which childList:subtree on the stable .visual-surface
  // ancestor catches without needing to also watch attributes.
  const surface = scope.querySelector('.visual-surface')!
  await new Promise<void>(resolve => {
    let timer: ReturnType<typeof setTimeout>
    const quiet = () => { observer.disconnect(); resolve() }
    const observer = new MutationObserver(() => { clearTimeout(timer); timer = setTimeout(quiet, 400) })
    observer.observe(surface, { childList: true, subtree: true })
    timer = setTimeout(quiet, 400)
  })
  return scope.querySelector<SVGSVGElement>('svg[data-diagram-renderer="blerg-flow"]')!
}

/** A real screenshot (CONTRIBUTING.md's own verification step) once the diagram has settled. */
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
  it(`never draws larger than its own natural size in chat, no inner scrollbar, at ${width}x${height}`, async () => {
    await page.viewport(width, height)
    seed()
    mount()
    const svg = await waitForSettled()
    const surface = svg.closest<HTMLElement>('.visual-surface')!
    const surfaceStyle = getComputedStyle(surface)
    const availableWidth = surface.clientWidth - Number.parseFloat(surfaceStyle.paddingLeft) - Number.parseFloat(surfaceStyle.paddingRight)
    // Review (post-slice-2, applied back to flowchart): chat shrinks to the available width when
    // the diagram's own natural size (its viewBox, fixed regardless of fit) is wider than it, but
    // never grows past natural the other way round — a wide message must not blow a small
    // diagram's text up past 1x. `expectedWidth` below is exactly render.ts's own chatSize rule:
    // min(natural, available).
    const viewBoxWidth = Number(svg.getAttribute('viewBox')!.split(' ')[2])
    const expectedWidth = Math.min(viewBoxWidth, availableWidth)
    expect(svg.getBoundingClientRect().width).toBeGreaterThanOrEqual(expectedWidth - 2)
    expect(svg.getBoundingClientRect().width).toBeLessThanOrEqual(expectedWidth + 2)
    // Never past natural size regardless of the above — the direct check for "text at most 1x":
    // displayed/viewBox is the SVG's own uniform scale factor (width/height attrs are set to the
    // same aspect ratio as the viewBox, chatSize.ts), so this bounds the rendered size of every
    // glyph inside it, not just the SVG's own outer box.
    expect(svg.getBoundingClientRect().width).toBeLessThanOrEqual(viewBoxWidth + 2)
    // No inner scrollbar: natural height, nothing clipped or scrolled inside the card.
    expect(surfaceStyle.maxHeight).toBe('none')
    expect(surface.scrollHeight).toBeLessThanOrEqual(surface.clientHeight + 1)

    // Hover and keyboard focus (card BLG-468's own acceptance): a node announces into the
    // aria-live region on focus. Asserted immediately after focus, not through waitFor: the
    // focus handler (accessibility.ts) is a synchronous DOM listener, already run by the time
    // `act` resolves — polling here instead would tolerate a later, unrelated event (`focus()`
    // can auto-scroll the element into view, which can in turn fire a stray `mouseenter` on
    // whatever now sits under a stationary virtual mouse pointer) overwriting the live region
    // again before the assertion runs, flaky under heavier parallel load (measured).
    const live = svg.parentElement!.querySelector('[aria-live="polite"]')!
    const chip = svg.querySelector<SVGElement>('[data-blerg-chip-key]')!
    expect(chip.getAttribute('role')).toBe('button')
    await act(async () => { (chip as unknown as HTMLElement).focus() })
    expect(live.textContent).toBe('Open BLG-468')

    // Back edges (Review -- reject --> Start) bow apart instead of overlapping the forward edge.
    const edgePaths = [...svg.querySelectorAll('[data-blerg-flow-edge] path')].map(p => p.getAttribute('d'))
    expect(new Set(edgePaths).size).toBe(edgePaths.length) // no two edges share an identical path

    if (shots) {
      const finalSvg = document.querySelector<SVGSVGElement>('svg[data-diagram-renderer="blerg-flow"]')!
      await screenshot(`${shots}/flowchart-chat-${width}.png`, finalSvg.getBoundingClientRect())
    }
  }, 45_000)

  it(`fills its own tab (review round 3 §1), at ${width}x${height}`, async () => {
    await page.viewport(width, height)
    seed()
    mount()
    const chatSvg = await waitForSettled()
    const chatWidth = chatSvg.getBoundingClientRect().width
    const naturalWidth = Number(chatSvg.getAttribute('viewBox')!.split(' ')[2])
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
    // Never past the pane itself.
    expect(tabWidth).toBeLessThanOrEqual(availableWidth + 1)
    expect(tabHeight).toBeLessThanOrEqual(availableHeight + 1)
    // Centered: equal margins on both sides (not stuck in a corner, the review's other finding).
    const rect = svg.getBoundingClientRect(), surfaceRect = surface.getBoundingClientRect()
    expect(Math.abs((rect.left - surfaceRect.left) - (surfaceRect.right - rect.right))).toBeLessThanOrEqual(20)
    // The tab always grows at least as large as chat's own (never-larger-than-natural) size —
    // within the bounded refinement loop's own ~2% convergence tolerance (render.ts), not exact.
    expect(tabWidth).toBeGreaterThanOrEqual(chatWidth * 0.95)
    if (width === 3840) {
      // At the widest viewport, available space is far bigger than MAX_FILL_SCALE (2x) could
      // ever use — the tab reaches its own cap rather than the pane's own width (review, applied
      // post-slice-2: an uncapped fill here is exactly the "scales up without bound" behavior
      // chat itself was just fixed to never do; a tab keeps the bounded 2x affordance, not an
      // unbounded one). At a narrower viewport (1024, 390) the pane itself is already tighter
      // than 2x natural, so the available-space bound (not the cap) is what's being exercised —
      // scoped here to the one viewport where the cap is actually the binding constraint.
      expect(tabWidth).toBeLessThanOrEqual(naturalWidth * 2 * 1.1)
      expect(tabWidth).toBeGreaterThanOrEqual(naturalWidth * 1.5)
    }
    // Never needs to scroll: the fill size is bounded by the available height (render.ts) — a
    // few px tolerance for the card's own chrome (header/actions) sharing this box, not a real
    // scroll.
    expect(surface.scrollHeight).toBeLessThanOrEqual(surface.clientHeight + 8)

    if (shots) await screenshot(`${shots}/flowchart-tab-${width}.png`, svg.getBoundingClientRect())
  }, 45_000)
}

it('in chat at 3840, a node label\'s rendered font size never exceeds its natural size', async () => {
  // A very wide chat message is exactly the case that surfaced the bug this guards: a small
  // diagram's text blown up several times over because the SVG's own width/height attrs were set
  // far past its viewBox. The SVG's own declared font-size attribute is fixed at natural size
  // (draw.ts never scales it for chat); what actually changes the glyph's *rendered* pixel size
  // is the ratio between the SVG's displayed box and its own viewBox — read directly off a real
  // text element rather than assumed from the SVG's own outer box alone.
  await page.viewport(3840, 2160)
  seed()
  mount()
  const svg = await waitForSettled()
  const viewBoxWidth = Number(svg.getAttribute('viewBox')!.split(' ')[2])
  const renderScale = svg.getBoundingClientRect().width / viewBoxWidth
  expect(renderScale).toBeLessThanOrEqual(1.01)
  const label = svg.querySelector('[data-blerg-flow-node] text')!
  const naturalFontSize = Number(label.getAttribute('font-size'))
  const renderedFontSize = naturalFontSize * renderScale
  expect(renderedFontSize).toBeLessThanOrEqual(naturalFontSize * 1.01)
}, 45_000)
