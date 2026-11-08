import { act, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { HOVERCARD, useTranscriptStore } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import ToastContainer from './ToastContainer'
import { useToastStore } from '../hooks/useToastStore'
import CommandPaletteProvider from './CommandPalette'
import { useMessageStore } from '../hooks/useMessageStore'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { invalidateChatTransport } from '../lib/chatTransport'
import { openInbox, refreshInbox, type InboxItem } from '../hooks/useInbox'
import { installHoverClock } from '../test/hoverClock'
import { resetSessionBoardsForTest } from '../lib/sessionBoard'
import { resetLiveBoardCardsForTest } from '../lib/liveBoardCard'
import { resetBoardKeyMapsForTest } from '../lib/cardRefs'
import '../index.css'
import '@blerglab/chat/blerg.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
const shotDir = (import.meta.env as Record<string, string | undefined>).VITE_INBOX_SHOTS_DIR
const json = (data: unknown) => new Response(JSON.stringify(data), { headers: { 'Content-Type': 'application/json' } })
let items: InboxItem[]
beforeEach(() => {
 resetSessionBoardsForTest(); resetLiveBoardCardsForTest(); resetBoardKeyMapsForTest()
 useToastStore.setState({toasts:[]})
 localStorage.clear()
 useMessageStore.setState({ messages: [] })
 useTranscriptStore.setState({ sessions: {} })
 useTranscriptStore.getState().ingest({ type: 'agent_event', session_id: 'sess-a', client_event_id: 'greeting', seq: 1, ts: '2026-11-01T12:00:00Z', kind: 'assistant_text', payload: { text: 'Your important updates are ready to read.', done: true } })
 useTranscriptStore.getState().ingestReplayDone('sess-a', { lastSeq: 1, hasMore: false })
 document.documentElement.setAttribute('data-theme', 'dark')
 items = [
  { id: 'summary', session_id: 'sess-a', subject: 'Your morning summary', kind: 'summary', preview: 'The release is ready. Two decisions need your attention.', body: '## Ready for your day\n\nThe release is ready to try. Your workspace kept its place overnight.\n\n### What shipped\n\n- Inbox items stay private to your account.\n- Reading on one device updates the others.\n- Important updates have a place to land.\n\n### Worth a look\n\nThe chart review is complete. Open the workspace to explore the new reading pane.\n\n| Work | State |\n| --- | --- |\n| Release notes | Ready |\n| Chart review | Complete |\n\nNothing here needs an answer. Read when you have a moment.', created_at: '2026-11-04T12:00:00Z', read: false },
  { id: 'release', session_id: 'sess-a', subject: 'The release is ready to try', kind: 'release', preview: 'The gate passed and the release notes are ready.', body: '## Release ready\n\nThe gate passed. Your release notes are ready to read.', created_at: '2026-11-03T18:00:00Z', read: false },
  { id: 'review', session_id: 'sess-a', subject: 'Chart review complete', kind: 'review', preview: 'All review findings are resolved.', body: 'All review findings are resolved.', created_at: '2026-11-02T18:00:00Z', read: true },
 ]
 useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ id: 'sess-a', kind: 'agent', status: 'idle', title: 'Release workspace' })] })
 apiFetch.mockReset().mockImplementation(async (url: string, init?: RequestInit) => {
  if (url === '/api/inbox') return json({ items, unread: items.filter(i => !i.read).length, next: '' })
  if (url.startsWith('/api/inbox/')) {
   const id = url.split('/')[3], item = items.find(i => i.id === id)
   if (!item) return new Response('{}', { status: 404 })
   if (init?.method === 'DELETE') { items = items.filter(i => i.id !== id); return json({deleted: true}) }
   if (init?.method === 'PUT') { item.read = JSON.parse(String(init.body)).read }
   return json(item)
  }
  if (url.endsWith('/topics')) return json({ topics: [], unmarked: null, strokes: [] })
  if (url.includes('/live')) return json({ can_release_prompt: false })
  if (url.includes('/workspace?')) return json({ result: { ok: true, launch_generation: 'launch', generation: 1, entries: [], truncated: false }, members: [], runtime: 'host' })
  if (url.endsWith('/permissions')) return json({ can_grant: false, reason: '', grants: [], mcp_tools: [], boards: [] })
  return json({})
 })
 vi.stubGlobal('fetch', vi.fn(async () => json([])))
})
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals(); vi.restoreAllMocks(); document.documentElement.removeAttribute('data-theme') })
function mount(pane?: number, search = '') {
 return render(<MemoryRouter initialEntries={['/sessions/sess-a' + search]}><CommandPaletteProvider><ToastContainer/><div style={{ display: 'flex', width: pane ?? '100%', height: '100vh', minWidth: 0 }}><div style={{ flex: 1, minWidth: 0 }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div></div></CommandPaletteProvider></MemoryRouter>)
}
for (const spec of [{ width: 390, height: 844, theme: 'dark' }, { width: 1920, height: 1080, pane: 1024, theme: 'dark' }, { width: 3840, height: 2160, theme: 'dark' }, { width: 1920, height: 1080, pane: 1024, theme: 'colorblind-dark' }]) {
 it(`reads privately in one reused Inbox tab at ${spec.width}, ${spec.pane ?? 'full'} pane, ${spec.theme}`, async () => {
  await page.viewport(spec.width, spec.height)
  document.documentElement.setAttribute('data-theme', spec.theme)
  const view = mount(spec.pane)
  await act(async () => { await refreshInbox() })
  await waitFor(() => expect(screen.getAllByRole('button', { name: 'Open inbox' }).length).toBe(3))
  await userEvent.click(screen.getByRole('button', { name: 'Toggle explorer' }))
  await userEvent.click(await screen.findByRole('tab', { name: 'Inbox' }))
  if (shotDir) await page.screenshot({ path: `${shotDir}/arrival-${spec.width}-${spec.pane ?? 'full'}-${spec.theme}.png` })
  await userEvent.click(screen.getByRole('button', { name: 'Your morning summary, unread' }))
  await screen.findByRole('heading', { name: 'Your morning summary' })
  await waitFor(() => expect(items[0].read).toBe(true))
  expect(document.querySelectorAll('.session-tab [data-tab-id="inbox-reader"]')).toHaveLength(1)
  if (spec.pane) {
   const frame = view.container.firstElementChild as HTMLElement
   const host = document.querySelector('.inbox-host')!
   frame.style.width = `${frame.clientWidth + spec.pane - host.clientWidth}px`
   await waitFor(() => expect(Math.abs(host.clientWidth - spec.pane!)).toBeLessThanOrEqual(1))
  }
  await userEvent.click(screen.getByRole('button', { name: 'Mark unread' }))
  await waitFor(() => expect(items[0].read).toBe(false))
  if (shotDir) await page.screenshot({ path: `${shotDir}/reading-${spec.width}-${spec.pane ?? 'full'}-${spec.theme}.png` })
  // On a phone the list is the explorer's Inbox tab (the reader tab has no list or back arrow).
  if (spec.width === 390) { await userEvent.click(screen.getByRole('button', { name: 'Toggle explorer' })); await userEvent.click(await waitFor(() => document.getElementById('session-tab-inbox')!)) }
  const row = screen.getAllByRole('button', { name: 'Your morning summary, unread' }).find(el => el.getBoundingClientRect().width > 0)!
  await userEvent.hover(row)
  expect(row.closest("li")!.querySelector('.inbox-row-delete')).toBeVisible()
  if (shotDir) await page.screenshot({ path: `${shotDir}/row-hover-${spec.width}-${spec.pane ?? 'full'}-${spec.theme}.png` })
  await act(async () => openInbox('release'))
  await screen.findByRole('heading', { name: 'The release is ready to try' })
  expect(document.querySelectorAll('.session-tab [data-tab-id="inbox-reader"]')).toHaveLength(1)
  // The reader tab carries no back arrow; the list is the explorer's Inbox tab.
  expect(screen.queryByRole('button', { name: 'Back to inbox list' })).toBeNull()
  const host = document.querySelector('.inbox-host')!
  expect(host.scrollWidth).toBeLessThanOrEqual(host.clientWidth + 1)
  view.unmount()
 })
}

it('opens Inbox through collapsed counts, heading, palette and shortcut', async () => {
 await page.viewport(1920,1080)
 useMessageStore.setState({ messages: [{ id: 'question', session_id: 'sess-a', kind: 'ask', status: 'open', answerable: true, body: 'Ready to review?', created_at: '2026-11-04T13:00:00Z' }] })
 const view=mount()
 await act(async()=>{await refreshInbox()})
 await screen.findByRole('button',{name:'2 unread inbox items'})
 if (shotDir) await page.screenshot({ path: `${shotDir}/collapsed-counts-1920-dark.png` })
 await userEvent.click(screen.getByRole('button',{name:'1 open questions'}))
 await waitFor(()=>expect(screen.getByRole('tab',{name:'Questions'})).toHaveAttribute('aria-selected','true'))
 await userEvent.click(screen.getByRole('button',{name:'Toggle explorer'}))
 await userEvent.click(await screen.findByRole('button',{name:'2 unread inbox items'}))
 await waitFor(()=>expect(document.querySelector('[data-tab-id="inbox-reader"]')).toHaveAttribute('aria-selected','true'))
 await userEvent.click(screen.getByRole('button',{name:'Close Inbox'}))
 await userEvent.click(screen.getByRole('button',{name:'Toggle explorer'}))
 await userEvent.click(screen.getByRole('tab',{name:'Inbox'}))
 // Owner 2026-11-05: no 'open as tab' button; a row opens the reader tab.
 expect(screen.queryByRole('button',{name:'Open inbox as tab'})).toBeNull()
 await userEvent.click(screen.getAllByRole('button',{name:/, unread$/})[0])
 expect(document.querySelectorAll('[data-tab-id="inbox-reader"]')).toHaveLength(1)
 await userEvent.click(screen.getByRole('button',{name:'Close Inbox'}))
 await act(async()=>window.dispatchEvent(new KeyboardEvent('keydown',{key:'i',ctrlKey:true,altKey:true,bubbles:true})))
 await waitFor(()=>expect(document.querySelector('[data-tab-id="inbox-reader"]')).toBeTruthy())
 await userEvent.click(screen.getByRole('button',{name:'Search commands and files'}))
 await userEvent.fill(screen.getByRole('combobox', { name: 'Search commands, workspaces, cards and files' }),'Open inbox')
 await userEvent.click(await screen.findByRole('option',{name:/Open inbox/}))
 expect(document.querySelectorAll('[data-tab-id="inbox-reader"]')).toHaveLength(1)
 view.unmount()
})

it('opens a push deep link at its item and marks it read', async () => {
 await page.viewport(1920,1080)
 const view=mount(undefined,'?inbox=summary')
 await screen.findByRole('heading',{name:'Your morning summary'})
 await waitFor(()=>expect(items[0].read).toBe(true))
 expect(apiFetch.mock.calls.filter(([url,init])=>url==='/api/inbox/summary/read' && init?.method==='PUT')).toHaveLength(1)
 // Owner 2026-11-05: no 'Open conversation' in the reader (conversations are long and not threaded).
 expect(screen.queryByRole('link',{name:'Open conversation'})).toBeNull()
 view.unmount()
})

it('hides zero counts and opens an empty Inbox', async () => {
 await page.viewport(1920,1080)
 items=[]
 const view=mount()
 await act(async()=>{await refreshInbox()})
 expect(screen.queryByRole('button',{name:/unread inbox items/})).toBeNull()
 await act(async()=>openInbox())
 await screen.findByText('Nothing to read yet.')
 view.unmount()
})

it('reads beside the dock list (the tab has no list of its own), one-line rows and wide document columns', async () => {
 await page.viewport(3840,2160)
 items[0].subject='Morning summary: the release is ready for review, deployment checks are complete, and the workspace has important updates worth reading before the next planning session starts'
 items[0].body=Array.from({length:12},(_,i)=>`### Update ${i+1}\n\nThe release is ready to try. Review the changes and keep the workspace ready for the next day. This reading pane makes important work easy to find.`).join('\n\n')
 const view=mount()
 await act(async()=>{await refreshInbox()})
 await userEvent.click(screen.getByRole('button',{name:'Toggle explorer'}))
 await userEvent.click(within(screen.getByRole('region',{name:'Explorer'})).getByRole('tab',{name:'Inbox'}))
 await act(async()=>openInbox('summary'))
 await screen.findByRole('heading',{name:items[0].subject})
 expect(document.querySelector('.inbox-list')).toBeNull()
 const subject=document.querySelector<HTMLElement>('.inbox-compact .inbox-card-subject')!
 // Owner 2026-11-05: one line per item in the dock list (details live in the row's hovercard).
 expect(getComputedStyle(subject).whiteSpace).toBe('nowrap')
 expect(getComputedStyle(subject).textOverflow).toBe('ellipsis')
 expect(subject.getBoundingClientRect().height).toBeLessThanOrEqual(parseFloat(getComputedStyle(subject).lineHeight) + 1)
 const documentBody=document.querySelector('.inbox-document > .md')!
 expect(getComputedStyle(documentBody).marginTop).toBe('24px')
 expect(getComputedStyle(documentBody).columnWidth).toBe('720px')
 view.unmount()
})

it('marks the dock row of the item the Inbox tab is showing, and moves the mark with the reader', async () => {
 await page.viewport(1920,1080)
 const view=mount(undefined,'?inbox=summary')
 await screen.findByRole('heading',{name:'Your morning summary'})
 if(!document.getElementById('session-tab-inbox')) await userEvent.click(screen.getByRole('button',{name:'Toggle explorer'}))
 await userEvent.click(await waitFor(()=>document.getElementById('session-tab-inbox')!))
 const row=(subject:string)=>document.querySelector<HTMLElement>(`.inbox-compact .inbox-card[aria-label^="${subject},"]`)
 await waitFor(()=>expect(row('Your morning summary')).toHaveAttribute('aria-current','true'))
 expect(document.querySelectorAll('.inbox-compact .inbox-card[aria-current="true"]')).toHaveLength(1)
 await userEvent.click(row('The release is ready to try')!)
 await screen.findByRole('heading',{name:'The release is ready to try'})
 await waitFor(()=>expect(row('The release is ready to try')).toHaveAttribute('aria-current','true'))
 expect(row('Your morning summary')).not.toHaveAttribute('aria-current')
 await userEvent.click(screen.getByRole('button',{name:'Delete'}))
 await waitFor(()=>expect(document.querySelectorAll('.inbox-compact .inbox-card[aria-current="true"]')).toHaveLength(0))
 expect(screen.queryByRole('heading',{name:'The release is ready to try'})).toBeNull()
 await userEvent.click(screen.getByRole('button',{name:'Undo'}))
 await screen.findByRole('heading',{name:'The release is ready to try'})
 await waitFor(()=>expect(row('The release is ready to try')).toHaveAttribute('aria-current','true'))
 await userEvent.click(screen.getByRole('button',{name:'Close Inbox'}))
 await waitFor(()=>expect(document.querySelectorAll('.inbox-compact .inbox-card[aria-current="true"]')).toHaveLength(0))
 view.unmount()
})

it('resolves a BLG key from the source session, shows its hovercard and opens its ticket', async()=>{
 await page.viewport(1920,1080)
 items[0].session_id='source-other'
 items[0].body='Review BLG-123 and #124 before the release.'
 const original=apiFetch.getMockImplementation()!
 apiFetch.mockImplementation(async(url:string,init?:RequestInit)=>{
  if(url==='/api/session-boards')return json({boards:[{id:'inbox-board',key:'BLG'}]})
  if(url==='/api/sessions/source-other/permissions')return json({can_grant:false,grants:[],mcp_tools:[],boards:[{board_id:'inbox-board',level:'read'}]})
  if(url.startsWith('/api/board-cards/inbox-board/124'))return json({card:{id:'card-124',board_id:'inbox-board',number:124,title:'Source board item'},url:'/boards/inbox-board/cards/124'})
  if(url.startsWith('/api/board-cards/inbox-board/123'))return json({card:{id:'card-123',board_id:'inbox-board',reference:'BLG-123',number:123,title:'Release checklist',body:'Ready for review',column_name:'Review',columns:[]},url:'/boards/inbox-board/cards/123'})
  return original(url,init)
 })
 const clock=installHoverClock()
 const view=mount(undefined,'?inbox=summary')
 try{
  await screen.findByRole('heading',{name:'Your morning summary'})
  const ref=await screen.findByRole('button',{name:'Open card BLG-123: Release checklist'})
  await userEvent.hover(ref)
  clock.advance(HOVERCARD.openDelayMs)
  await waitFor(()=>expect(document.querySelector('[data-testid="hovercard"]')).toHaveTextContent('Release checklist'))
  await userEvent.click(ref)
  await waitFor(()=>expect(screen.getByRole('dialog')).toHaveTextContent('Release checklist'))
  expect(apiFetch).toHaveBeenCalledWith('/api/sessions/source-other/permissions')
 }finally{clock.uninstall();view.unmount()}
})

it('deletes a dock row without opening it, and Undo restores its arrival card', async()=>{
 await page.viewport(1920,1080)
 const view=mount()
 await act(async()=>{await refreshInbox()})
 await waitFor(()=>expect(screen.getAllByRole('button',{name:'Open inbox'})).toHaveLength(3))
 await userEvent.click(screen.getByRole('button',{name:'Toggle explorer'}))
 await userEvent.click(await screen.findByRole('tab',{name:'Inbox'}))
 const row=screen.getByRole('button',{name:'Your morning summary, unread'})
 await userEvent.hover(row)
 await userEvent.click(row.closest("li")!.querySelector<HTMLButtonElement>('.inbox-row-delete')!)
 await waitFor(()=>expect(screen.getAllByRole('button',{name:'Open inbox'})).toHaveLength(2))
 expect(screen.queryByRole('heading',{name:'Your morning summary'})).toBeNull()
 expect(document.querySelector('[data-tab-id="inbox-reader"]')).toBeNull()
 await userEvent.click(screen.getByRole('button',{name:'Undo'}))
 await screen.findByRole('button',{name:'Your morning summary, unread'})
 await waitFor(()=>expect(screen.getAllByRole('button',{name:'Open inbox'})).toHaveLength(3))
 view.unmount()
})
