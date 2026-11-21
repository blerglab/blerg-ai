// BLG-484: Ctrl/Cmd+A in the real session page scopes to what has focus or the last click — a
// message, a code block inside one, or the transcript — never the sidebar or the toolbar, and
// never the whole page. Mounted the way PopOutMessage.browser.test.tsx does: the real SessionDetail
// page, real CSS, beside a sidebar stand-in so the bug this fixes ("the entire app" selected) would
// show up as a failing assertion rather than passing by omission. Real clicks and keystrokes
// (userEvent, not dispatchEvent) so the browser's own focus-on-click behavior is in play too.
import { render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { page, userEvent } from 'vitest/browser'
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

const CODE = 'func main() {\n\tprintln("hi")\n}'
const SIDEBAR_TEXT = 'Sidebar stand-in: Release notes session'

function mount() {
  return render(
    <MemoryRouter initialEntries={['/sessions/sess-a']}>
      <CommandPaletteProvider>
        <div style={{ display: 'flex', height: '100vh' }}>
          <nav aria-label="Sidebar stand-in" style={{ width: 200 }}>{SIDEBAR_TEXT}</nav>
          <div style={{ flex: 1, minWidth: 0 }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div>
        </div>
      </CommandPaletteProvider>
    </MemoryRouter>,
  )
}

function seed() {
  useTranscriptStore.setState({ sessions: {} })
  const transcript = useTranscriptStore.getState()
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'u1', seq: 1, ts: '2026-07-26T12:00:00Z', kind: 'user_message', payload: { text: 'First message', source: 'chat' } } as AgentEvent)
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'a1', seq: 2, ts: '2026-07-26T12:00:01Z', kind: 'assistant_text', payload: { text: `Reply with code\n\n\`\`\`go\n${CODE}\n\`\`\``, done: true } } as AgentEvent)
  transcript.ingestReplayDone('sess-a', { lastSeq: 2, hasMore: false })
}

const selectedText = () => window.getSelection()?.toString() ?? ''
const pressSelectAll = () => userEvent.keyboard('{Control>}a{/Control}')
const shotDir = (import.meta.env as Record<string, string | undefined>).VITE_SELECTALL_SHOTS_DIR
const shot = (name: string) => shotDir && page.screenshot({ path: `${shotDir}/${name}.png` })

async function ready() {
  seed()
  const view = mount()
  await waitFor(() => expect(document.querySelectorAll('.agent-card')).toHaveLength(3))
  return view
}

for (const [width, height] of [[390, 844], [3840, 2160]] as const) {
  it(`selects just the clicked message, never the sidebar or the other message, at ${width}px`, async () => {
    await page.viewport(width, height)
    const view = await ready()
    const prose = document.querySelector('.agent-card.user p, .agent-card.user .md') as HTMLElement
    await userEvent.click(prose)
    await pressSelectAll()
    const text = selectedText()
    expect(text).toContain('First message')
    expect(text).not.toContain(SIDEBAR_TEXT)
    expect(text).not.toContain('Reply with code')
    await shot(`message-${width}x${height}`)
    view.unmount()
  })

  it(`selects just the code, not the message prose or the copy button, at ${width}px`, async () => {
    await page.viewport(width, height)
    const view = await ready()
    const pre = document.querySelector('.agent-card.assistant .md-codeblock pre') as HTMLElement
    await userEvent.click(pre)
    await pressSelectAll()
    const text = selectedText()
    expect(text).toContain('println')
    expect(text).not.toContain('Reply with code')
    expect(text).not.toContain('Copy')
    await shot(`codeblock-${width}x${height}`)
    view.unmount()
  })

  it(`falls back to the transcript (never the sidebar or toolbar) when the pane itself has focus, at ${width}px`, async () => {
    await page.viewport(width, height)
    const view = await ready()
    const toolbar = document.querySelector('.agent-toolbar') as HTMLElement
    ;(document.activeElement as HTMLElement | null)?.blur()
    toolbar.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    await pressSelectAll()
    const text = selectedText()
    expect(text).toContain('First message')
    expect(text).toContain('Reply with code')
    expect(text).not.toContain(SIDEBAR_TEXT)
    await shot(`transcript-fallback-${width}x${height}`)
    view.unmount()
  })

  it(`leaves the composer its own select-all, at ${width}px`, async () => {
    await page.viewport(width, height)
    const view = await ready()
    const composer = document.querySelector('.agent-composer textarea') as HTMLTextAreaElement
    await userEvent.click(composer)
    await userEvent.clear(composer)
    await userEvent.type(composer, 'draft text')
    await pressSelectAll()
    // The browser's own select-all ran inside the field (window selection sees the field's own
    // content, never the rest of the page), never the chat pane's scoping.
    expect(selectedText()).toBe('draft text')
    expect(selectedText()).not.toContain('First message')
    view.unmount()
  })

  it(`copy after selecting a message yields clean text, consistent with the Copy button, at ${width}px`, async () => {
    await page.viewport(width, height)
    const view = await ready()
    const prose = document.querySelector('.agent-card.user p, .agent-card.user .md') as HTMLElement
    await userEvent.click(prose)
    await pressSelectAll()
    const data = new DataTransfer()
    const ev = new ClipboardEvent('copy', { bubbles: true, cancelable: true, clipboardData: data })
    // DataTransfer only answers getData synchronously, inside a listener still handling the
    // event: read the written plain text from a listener of our own, not after dispatch returns.
    let plain = ''
    document.addEventListener('copy', e => { plain = e.clipboardData?.getData('text/plain') ?? '' }, { once: true })
    prose.closest('.agent-card')!.dispatchEvent(ev)
    expect(plain).toContain('First message')
    expect(plain).not.toContain('Copy message')
    view.unmount()
  })
}

// A narrow pane inside a wide window (the window is 1920px; the chat pane is pinned to 1024px by
// a sidebar) scopes the same as a narrow window does.
it('scopes to the message in a 1024px pane inside a wide window', async () => {
  await page.viewport(1920, 1080)
  seed()
  const view = render(
    <MemoryRouter initialEntries={['/sessions/sess-a']}>
      <CommandPaletteProvider>
        <div style={{ display: 'flex', height: '100vh' }}>
          <nav aria-label="Sidebar stand-in" style={{ width: 200 }}>{SIDEBAR_TEXT}</nav>
          <div style={{ width: 1024, flexShrink: 0 }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div>
          <div style={{ flex: 1 }} />
        </div>
      </CommandPaletteProvider>
    </MemoryRouter>,
  )
  await waitFor(() => expect(document.querySelectorAll('.agent-card')).toHaveLength(3))
  const prose = document.querySelector('.agent-card.user p, .agent-card.user .md') as HTMLElement
  await userEvent.click(prose)
  await pressSelectAll()
  const text = selectedText()
  expect(text).toContain('First message')
  expect(text).not.toContain(SIDEBAR_TEXT)
  expect(text).not.toContain('Reply with code')
  await shot('message-pane-1024')
  view.unmount()
})
