// Real-app, real-browser proof for the pop-out's line rule (more than 4 rendered lines by default): the full SessionDetail page
// (its real CSS, dark theme, tab bar), not a bare component in an unstyled host — jsdom cannot
// compute layout at all, and a stripped-down harness cannot show the owner where the rule actually
// lands against the real chrome. Mounted the way ExplorerDock.browser.test.tsx and
// SessionTabBar.browser.test.tsx do. Also the owner's pre-merge screenshots.
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

const shotDir = (import.meta.env as Record<string, string | undefined>).VITE_POPOUT_SHOTS_DIR
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })

beforeEach(() => {
  localStorage.clear()
  document.documentElement.setAttribute('data-theme', 'dark')
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ id: 'sess-a', kind: 'agent', status: 'idle', title: 'Release notes' })] })
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

// A one-line reply: under the rule at any width.
const UNDER_TEXT = 'All good — nothing to report.'
// The shape of a typical status reply the owner wanted offered (2026-11-03): a paragraph, three
// bullets and a closing paragraph. At least five rendered lines at any width, so it is over.
const OVER_TEXT = 'Our chart library is approved and merges after the current gate.\n\n- Ticks match Vega exactly for every chart type it supports.\n- Some line charts still go to Vega for now.\n- Next: matching Vega\'s margins to within a pixel.\n\nThe gate is now on the lint and type checks, all passing so far.'
const TABLE = '\n\n| Item | Amount |\n| --- | --- |\n| **Launch** | 42 |\n| Formula | =1+2 |'
const BASE = '## Release notes\n\nWhat shipped and why.\n\n- Added the workspace explorer\n- Fixed a flaky resize observer\n- Polished empty states\n\n```ts\nexport function summarize(n: number) {\n  return `${n} items`\n}\n```\n\n'
function filler(n: number): string {
  return Array.from({ length: n }, (_, i) => `Detail ${i + 1}: enough prose to wrap across the pane and add some height here.`).join('\n\n')
}
const LONG_TEXT = `${BASE}${filler(80)}`

/** A phone has no mouse: emulate `hover: none` / `pointer: coarse` through CDP (page.viewport
 *  alone only resizes pixels, it does not change these media features) so the screenshots show
 *  what a real phone renders — Copy hidden, the pop-out alone in its place — not a desktop hover
 *  state at a narrow width. */
async function emulateTouch(touch: boolean) {
  const client = cdp() as unknown as { send(method: string, params?: object): Promise<unknown> }
  await client.send('Emulation.setEmulatedMedia', { features: touch ? [{ name: 'hover', value: 'none' }, { name: 'pointer', value: 'coarse' }] : [] })
}

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

function seedThree(overText: string) {
  useTranscriptStore.setState({ sessions: {} })
  const transcript = useTranscriptStore.getState()
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'u1', seq: 1, ts: '2026-07-26T12:00:00Z', kind: 'user_message', payload: { text: 'How did the release go?', source: 'chat' } } as AgentEvent)
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'a1', seq: 2, ts: '2026-07-26T12:00:01Z', kind: 'assistant_text', payload: { text: UNDER_TEXT, done: true } } as AgentEvent)
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'a2', seq: 3, ts: '2026-07-26T12:00:02Z', kind: 'assistant_text', payload: { text: overText, done: true } } as AgentEvent)
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'a3', seq: 4, ts: '2026-07-26T12:00:03Z', kind: 'assistant_text', payload: { text: LONG_TEXT, done: true } } as AgentEvent)
  transcript.ingestReplayDone('sess-a', { lastSeq: 4, hasMore: false })
}

for (const [width, height] of [[3840, 2160], [390, 844]] as const) {
  it(`shows the pop-out once a message runs past 4 lines in the real page at ${width}x${height}`, async () => {
    await page.viewport(width, height)
    await emulateTouch(width === 390)

    seedThree(OVER_TEXT + TABLE)
    const view = mount()
    await waitFor(() => expect(document.querySelectorAll('.agent-card.assistant')).toHaveLength(3))
    // PopOutMessage's own visibility check runs off a ResizeObserver callback, which fires on
    // its own schedule rather than inside this commit: give it a frame to settle before reading
    // the result it produced.
    await act(async () => { await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))) })
    const cards = [...document.querySelectorAll<HTMLElement>('.agent-card.assistant')]
    const popouts = cards.map(c => c.querySelector('[data-testid="popout-message"]'))
    expect(popouts[0]).toBeNull() // one line: hidden
    expect(popouts[1]).not.toBeNull() // a short status reply of five-plus lines: shown
    expect(popouts[2]).not.toBeNull() // very long: shown
    // The icons sit in a reserved corner: never over the message's own text (they used to hide
    // the end of the first line, and a reference chip there could not be hovered).
    for (const i of [1, 2]) {
      const button = popouts[i]!.getBoundingClientRect()
      const walker = document.createTreeWalker(cards[i].querySelector('.md')!, NodeFilter.SHOW_TEXT)
      for (let node = walker.nextNode(); node; node = walker.nextNode()) {
        const range = document.createRange()
        range.selectNodeContents(node)
        for (const r of range.getClientRects()) {
          const overlaps = r.left < button.right && r.right > button.left && r.top < button.bottom && r.bottom > button.top
          expect(overlaps, `pop-out overlaps "${node.textContent?.slice(0, 40)}"`).toBe(false)
        }
      }
    }

    const names = ['one-line', 'status-reply', 'very-long']
    for (let i = 0; i < cards.length; i += 1) {
      cards[i].scrollIntoView({ block: 'start' })
      // On a touch screen the pop-out (once shown) is always visible, never hover-revealed: Copy
      // itself is hidden there (a touch screen copies by long-press instead), so there is nothing
      // to hover and no reason to simulate one.
      if (width !== 390) await userEvent.hover(cards[i])
      if (shotDir) await page.screenshot({ path: `${shotDir}/${names[i]}-${width}x${height}.png` })
    }

    await act(async () => { popouts[1]!.dispatchEvent(new MouseEvent('click', { bubbles: true })) })
    await waitFor(() => expect(document.querySelector('.hosted-view:not([hidden]) .artifact-body')).toBeTruthy())
    if (shotDir) await page.screenshot({ path: `${shotDir}/opened-tab-${width}x${height}.png` })
    const active = document.querySelector<HTMLElement>('.hosted-view:not([hidden])')!
    await userEvent.click(active.querySelector<HTMLButtonElement>('[aria-label="Table actions"]')!)
    expect(active.querySelector('[aria-label="Download CSV"]')).not.toBeNull()
    const toolbar = active.querySelector('.viewer-toolbar')!
    expect(toolbar.querySelector('[aria-label="Download Markdown"]')).not.toBeNull()
    expect(toolbar.querySelector('[aria-label="Print"]')).not.toBeNull()

    view.unmount()
    await emulateTouch(false)
  })
}
