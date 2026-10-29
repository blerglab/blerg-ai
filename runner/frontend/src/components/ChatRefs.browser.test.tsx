// Card #153, in real Chromium with a real mouse (CDP) and the hovercards' manual clock, at 390 px
// and 3840 px: card numbers and commit hashes in a session's messages, agent and person alike.
// "#1 priority" (no card #1 on the board) stays text; #147 (a card there) links in place, with the
// card's hovercard, and opens the ticket; a hex word that is no commit stays text; a real sha links
// and opens the commit viewer; nothing in a fenced code block changes; the lookups are batched.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { cdp, page } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { HOVERCARD, clearHovercardCache, resetRefStoresForTest, useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import '../index.css'
import { seedRichRail } from '../test/railFixture'
import { RAIL_PREF_KEY } from '../lib/sessionRail'
import { installHoverClock, type ManualHoverClock } from '../test/hoverClock'
import { resetSessionBoardsForTest } from '../lib/sessionBoard'
import { resetLiveBoardCardsForTest } from '../lib/liveBoardCard'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async (orig) => ({ ...(await orig<typeof import('../authClient')>()), getAccessToken: () => 'tok', redirectToRefresh: vi.fn() }))
vi.mock('../claims', async (orig) => ({ ...(await orig<typeof import('../claims')>()), tokenExpired: () => false }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

import SessionDetail from './SessionDetail'

const SHA = '08025f2c9d1e4b7a0c3f5e6d7a8b9c0d1e2f3a4b'
const PATCH = 'diff --git a/x.txt b/x.txt\n--- a/x.txt\n+++ b/x.txt\n@@ -1 +1 @@\n-old\n+new\n'
const json = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))

let commitReads: string[] = []
let cardReads: string[] = []

function serve() {
  const { live } = seedRichRail()
  apiFetch.mockImplementation((url: string) => {
    if (url.endsWith('/live')) return json({ ...live, can_release_prompt: true })
    if (url.endsWith('/permissions')) return json({ can_grant: true, reason: '', grants: [], mcp_tools: [], boards: [{ board_id: 'b1', board_name: 'Blerg', level: 'full' }] })
    const m = url.match(/^\/api\/board-cards\/b1\/(\d+)/)
    if (m) {
      cardReads.push(m[1])
      return ['147', '155', '156', '157'].includes(m[1])
        ? json({ card: { id: `c${m[1]}`, board_id: 'b1', number: Number(m[1]), title: m[1] === '147' ? 'Doc box and folder images' : `Range card ${m[1]}`, column_id: 'k', column_name: 'Review', columns: [{ id: 'k', name: 'Review' }] }, url: '/boards/b1/cards/147' })
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
      if (c[2]) return Promise.resolve(new Response(PATCH, { status: 200 }))
      return json({ sha: SHA, short: SHA.slice(0, 7), subject: 'Doc box and folder images', body: '', author: 'Dev', author_date: '2026-10-22T10:00:00Z', parents: [], files: [{ path: 'x.txt', adds: 1, dels: 1 }], adds: 1, dels: 1 })
    }
    return json([])
  }))
}

function seed() {
  const s = useTranscriptStore.getState()
  const ev = (seq: number, kind: string, payload: unknown) => ({ session_id: 'sess-a', client_event_id: `ref${seq}`, seq: 900 + seq, ts: new Date().toISOString(), kind, payload } as AgentEvent)
  s.ingest(ev(1, 'user_message', { text: 'Is #147 next? It is our #1 priority.', source: 'chat' }))
  s.ingest(ev(2, 'assistant_text', { text: 'Yes: #147 landed in 08025f2c, and cafe1234 is just a word.\n\n```\nfix #147 in 08025f2c\n```', done: true }))
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
  // Reference resolution and dialog teardown can reflow the transcript after
  // the chip appears. Aim again until real input registers, without advancing
  // the manual clock before that registration.
  await expect.poll(() => el.closest('.hc-anchor, [data-hc-anchor]')).not.toBeNull()
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
  localStorage.setItem(RAIL_PREF_KEY, 'false')
  commitReads = []
  cardReads = []
  resetRefStoresForTest()
  resetSessionBoardsForTest()
  resetLiveBoardCardsForTest()
  clearHovercardCache()
  clock = installHoverClock()
  mouse = cdp() as unknown as Mouse
  await moveTo(1, 1)
  serve()
})
afterEach(async () => {
  await moveTo(1, 1)
  clock.uninstall()
  vi.unstubAllGlobals()
  localStorage.removeItem(RAIL_PREF_KEY)
})

/** The Markdown block of the message whose text matches (references split it into elements). */
async function findMd(text: RegExp): Promise<HTMLElement> {
  return waitFor(() => {
    const md = [...document.querySelectorAll<HTMLElement>('.md')].find(el => text.test(el.textContent ?? ''))
    if (!md) throw new Error(`no message ${text}`)
    return md
  })
}

function renderPage() {
  render(<div style={{ height: '100vh' }}><MemoryRouter initialEntries={['/sessions/sess-a']}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></MemoryRouter></div>)
  seed()
}

for (const [width, height] of [[390, 800], [3840, 2000]] as const) {
  describe(`references in messages at ${width} px`, () => {
    beforeEach(async () => { await page.viewport(width, height) })

    it('links #147 and the real sha in place; #1, the hex word and the code block stay text', async () => {
      renderPage()
      const user = await findMd(/Is #147 next/)
      const agent = await findMd(/landed in/)
      // Confirmed: the person's #147 and the agent's #147 and sha.
      await waitFor(() => expect(within(user).getAllByTestId('md-ref').map(r => r.textContent)).toEqual(['#147']))
      await waitFor(() => expect(within(agent).getAllByTestId('md-ref').map(r => r.textContent)).toEqual(['#147', '08025f2c']))
      // Not confirmed: plain text, exactly as written.
      expect(user.textContent).toContain('our #1 priority')
      // #1 asked and answered no: the text as it was, no link.
      await waitFor(() => expect(user.querySelector('[data-ref-candidate="card:1"]')!.getAttribute('data-ref-state')).toBe('none'))
      expect(user.querySelector('[data-ref-candidate="card:1"]')!.closest('button, a')).toBeNull()
      const md = agent
      expect(md.querySelector('[data-ref-candidate="commit:cafe1234"]')!.getAttribute('data-ref-state')).toBe('none')
      // The fenced block is untouched: no candidates, no links.
      const pre = md.querySelector('pre')!
      expect(pre.textContent).toContain('fix #147 in 08025f2c')
      expect(pre.querySelector('[data-ref-candidate], [data-testid="md-ref"]')).toBeNull()
      // Batched and deduped: each card and each word asked once.
      expect([...cardReads].sort()).toEqual(['1', '147'])
      expect([...commitReads].sort()).toEqual(['08025f2c', 'cafe1234'])
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    })

    it('expands ranges in both roles with a separate hovercard and dialog for every card', async () => {
      renderPage()
      const store = useTranscriptStore.getState()
      for (const [seq, kind, payload] of [
        [903, 'user_message', { text: 'Person range: (#155–157), next.', source: 'chat' }],
        [904, 'assistant_text', { text: 'Agent range: #155-#157.\n\n`#155–157`\n\n```\n#155–157\n```', done: true }],
      ] as const) store.ingest({ session_id: 'sess-a', client_event_id: `range${seq}`, seq, ts: new Date().toISOString(), kind, payload } as AgentEvent)
      const person = await findMd(/Person range/)
      const agent = await findMd(/Agent range/)
      for (const md of [person, agent]) {
        await waitFor(() => expect(within(md).getAllByTestId('md-ref').map(r => r.textContent)).toEqual(['#155', '#156', '#157']))
      }
      expect(person.textContent).toBe('Person range: (#155, #156, #157), next.')
      expect(agent.querySelector('pre')!.textContent).toContain('#155–157')
      expect(agent.querySelector('code')!.textContent).toBe('#155–157')
      for (const ref of within(agent).getAllByTestId('md-ref')) {
        ref.scrollIntoView({ block: 'center' })
        await hover(ref)
        await expect.poll(() => card()?.textContent).toContain(`Range card ${ref.textContent!.slice(1)}`)
        await moveTo(1, 1)
        ref.click()
        const dialog = await screen.findByRole('dialog')
        await waitFor(() => expect(dialog.textContent).toContain(`Range card ${ref.textContent!.slice(1)}`))
        within(dialog).getByRole('button', { name: /close/i }).click()
        await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
      }
      expect(cardReads.filter(n => ['155', '156', '157'].includes(n)).sort()).toEqual(['155', '156', '157'])
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    })

    it('resolves a board created mid-session and opens explicit references inside Blerg', async () => {
      const original = apiFetch.getMockImplementation()!
      apiFetch.mockImplementation((url: string) => {
        if (url.startsWith('/api/board-cards/new-board/7')) return json({ card: { id: 'new-card', board_id: 'new-board', number: 7, reference: 'NEW-7', title: 'New board work', column_name: 'Ready', columns: [] }, url: '/boards/new-board/cards/7' })
        return original(url)
      })
      renderPage()
      await findMd(/landed in/)
      const store = useTranscriptStore.getState()
      const add = (seq: number, kind: string, payload: unknown) => store.ingest({ session_id: 'sess-a', client_event_id: `created${seq}`, seq, ts: new Date().toISOString(), kind, payload } as AgentEvent)
      add(903, 'proposal_decided', { proposal_id: 'new-board', kind: 'board_create', state: 'done', board: { board_id: 'new-board', board_name: 'New board', access: 'full' } })
      add(904, 'assistant_text', { text: 'Created cards: #7 and board:new-board#7.', done: true })
      const md = await findMd(/Created cards:/)
      await waitFor(() => expect(within(md).getAllByTestId('md-ref')).toHaveLength(2))
      const refs = within(md).getAllByTestId('md-ref')
      expect(refs.map(r => r.textContent)).toEqual(['NEW-7', 'NEW-7'])
      const explicit = refs[1]
      explicit.scrollIntoView({ block: 'center' })
      await hover(explicit)
      await expect.poll(() => card()?.textContent).toContain('New board work')
      await moveTo(1, 1)
      explicit.click()
      const dialog = await screen.findByRole('dialog')
      expect(dialog.textContent).toContain('New board work')
      expect(md.querySelector('a')).toBeNull()
      within(dialog).getByRole('button', { name: /close/i }).click()
    })

    it('#147 has the card\'s hovercard; the sha opens the commit viewer', async () => {
      renderPage()
      const agent = await findMd(/landed in/)
      await waitFor(() => expect(within(agent).getAllByTestId('md-ref')).toHaveLength(2))
      const [cardRef, shaRef] = within(agent).getAllByTestId('md-ref')
      // The link takes the text's own place: same line, same size as the words around it.
      const line = parseFloat(getComputedStyle(cardRef).lineHeight)
      expect(cardRef.getBoundingClientRect().height).toBeLessThanOrEqual(line + 2)
      await hover(cardRef)
      await expect.poll(() => card()!.textContent).toContain('Doc box and folder images')
      expect(card()!.textContent).toContain('#147')
      await moveTo(1, 1)
      shaRef.click()
      expect(await screen.findByTestId('commit-viewer-overlay')).toBeInTheDocument()
    })
  })
}
