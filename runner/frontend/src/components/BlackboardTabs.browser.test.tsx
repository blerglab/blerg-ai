import { BlackboardChart } from './BlackboardTabs'
import blackboardMockup from '../../../../docs/design/blackboard-mockup.html?raw'
import { emit, flushOpen } from '../test/wsMock'
import { installHoverClock } from '../test/hoverClock'
import { clearBoardPageCache } from '../lib/boardPageCache'
import { resetLiveBoardCardsForTest } from '../lib/liveBoardCard'
import { resetBoardKeyMapsForTest } from '../lib/cardRefs'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { resetBlackboardReadsForTest } from '../lib/blackboardReads'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { page } from 'vitest/browser'
import { setTheme } from '../../../../packages/settings/theme'
import { useTranscriptStore } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import SessionsHome from './SessionsHome'
import InsightsSkeleton from './InsightsSkeleton'
import './Insights.css'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { invalidateChatTransport } from '../lib/chatTransport'
import { resetSessionBoardLinksForTest } from '../lib/sessionBoardLinks'
import { resetBoardOriginForTest } from '../lib/boardLink'
import type { BoardAccess } from '../lib/sessionPermissions'
import '../index.css'
import '@blerglab/chat/blerg.css'

vi.mock('../shellTransport', async original => {
  const actual = await original<typeof import('../shellTransport')>()
  const { FakeShell } = await import('../test/shellMock')
  return { ...actual, openShell: vi.fn(async () => new FakeShell('tab-shell')), restoreShells: vi.fn(async () => []) }
})
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
const boards: BoardAccess[] = []
let malformedEvents = false
let released = false
let revoked = false
let committed: {selected:string[];revision:number} | null = null
const proposals = [{id:'proposal',actor:{id:'sess-a',kind:'agent'},milestone_id:'release',revision:2,status:'pending',changes:[{id:'change-a',operation:'add',item:{board_id:'b-rel',card_number:1,reason:'Bug'}},{id:'change-b',operation:'remove',item:{board_id:'b-rel',card_number:2,reason:'Move to the next release'}}]}]
const names = ['Restore a workspace after restart', 'Review changes beside the conversation', 'Keep questions visible while working', 'Open repositories from Main', 'Read release history in one place']
const cards = Array.from({length: 150}, (_, i) => ({card: { id: 'card-' + i, board_id: 'b-rel', number: i + 1, reference: 'DEMO-' + (i+1), title: names[i % names.length] + (i < 5 ? '' : ' · ' + Math.floor(i / 5 + 1)), tags: [], type: 'feature', size: 'M', risk: 'low', complexity: 'medium' }, membership: { thrust_id: 'push-' + (i % 6), state: i % 7 === 0 ? 'unplanned' : 'planned' }, stage: ['done','to_test','in_work','next','needs_you'][i % 5], drift: false, deleted: false }))
const milestones: {id:string;name:string;target:string|null;frozen_at:string|null;revision:number;board_id:string;release_id:string;stage:string;shipped_at?:string;cut_receipt_id?:string}[] = [{id: 'release', name: '0.6.0', target: '2026-11-05T00:00:00Z', frozen_at: null, revision: 2, board_id: 'b-rel', release_id: 'release', stage: ''}, {id: 'past', name: '0.5.0', frozen_at: '2026-10-28T00:00:00Z', target: null, revision: 1, board_id: 'b-rel', release_id: 'past-release', shipped_at:'2026-10-28T00:00:00Z', cut_receipt_id:'receipt-past', stage: 'Final'}]
const record = {name: 'Release work', vision: 'An IDE for AI collaboration', plan_revision: 0, milestones, goals: [{id:'ux',name:'Delightful, intuitive UX'},{id:'foundation',name:'Sessions you can rely on'}], thrusts: ['Recover work', 'Review in context', 'Questions that help', 'Workspaces from Main', 'Release confidence', 'Clear navigation'].map((name,i)=>({id:'push-'+i,name,goal_id:i<3?'ux':'foundation',milestone_id:'release'})), snapshots: [0,1,3,4].map((d)=>({milestone_id:'release',day:new Date(Date.UTC(2026,9,29+d)).toISOString().slice(0,10),complete:true,counts:{done: d*10,to_test:d*5,in_work:20,next:100-d*15,needs_you:30}})),events:[{id:'e1',milestone_id:'release',kind:'opened',source:'agent',recorded_at:'2026-10-29T00:00:00Z',payload:{}},{id:'e2',milestone_id:'release',kind:'alpha',source:'agent',recorded_at:'2026-11-02T00:00:00Z',payload:{number:1}}]}
let cardColumn = 'todo'
let milestoneStage = ''
let milestoneFrozen: string | null = null
let finalCalled: { revision: number; next_release_id: string } | null = null
let abortCalled: { revision: number } | null = null
let smallWorkspace = false
let smallGoals: { id: string; name: string }[] = [{ id: 'write', name: 'Write the thing' }]
let planForbidden = false
let planRevision = 0
let planItems: { card: { id: string; title: string }; membership: { id: string; goal_id?: string; state: string }; stage: string }[] = []
let planCalled: { revision: number; changes: { operation: string; item: Record<string, unknown> }[] } | null = null
let releasesServed: { id: string; name: string; status: string }[] = []
let noBlackboard = false
let createBody: Record<string, unknown> | null = null
let createForbidden = false
let requestSetupWait: Promise<void> | null = null
let requestSetupForbidden = false
let draftPlan: { name: string; vision: string; goals: string[]; milestone: { name: string; target?: string }; board_id?: string; thrusts: { name: string; goal_index: number; reason: string }[] } | null = null
let draftDiscardForbidden = false
let draftCreateForbidden = false
beforeEach(() => {
 resetBlackboardReadsForTest()
 revoked = false
 malformedEvents = false
 released = false
 committed = null
 proposals[0].status='pending'
 milestoneStage = ''
 milestoneFrozen = null
 finalCalled = null
 abortCalled = null
 smallWorkspace = false
 smallGoals = [{ id: 'write', name: 'Write the thing' }]
 planForbidden = false
 planRevision = 0
 planItems = [{ card: { id: 'item-1', title: 'Write the first chapter' }, membership: { id: 'item-1', goal_id: 'write', state: 'planned' }, stage: 'next' }]
 planCalled = null
 releasesServed = [{ id: 'r1', name: '0.6.0', status: 'in progress' }]
 noBlackboard = false
 createBody = null
 createForbidden = false
 requestSetupWait = null
 requestSetupForbidden = false
 draftPlan = null
 draftDiscardForbidden = false
 draftCreateForbidden = false
 clearBoardPageCache()
  localStorage.clear()
  cardColumn = 'todo'
  resetLiveBoardCardsForTest()
  resetBoardKeyMapsForTest()
  resetSessionBoardLinksForTest()
  resetBoardOriginForTest()
  document.documentElement.setAttribute('data-theme', 'dark')
  useTranscriptStore.setState({ sessions: {} })
  useTranscriptStore.getState().ingestReplayDone('sess-a', { lastSeq: 0, hasMore: false })
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ kind: 'agent', runtime: 'daemon', status: 'idle', title: 'Plan the release' })] })
  apiFetch.mockReset().mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.includes('/plan/draft') && init?.method === 'POST') {
      draftPlan = samplePlan
      emit({ type: 'blackboard_draft_changed', session_id: 'sess-a' })
      return json({ status: 'draft_pending' })
    }
    if (url.includes('/plan/request-setup')) {
      await requestSetupWait
      if (requestSetupForbidden) return new Response(JSON.stringify({error:'Sign in as a person.'}), {status:403, headers:{'Content-Type':'application/json'}})
      return json({status:'requested', session:'main-session'})
    }
    if (url.startsWith('/api/blackboards')) {
      if (/^\/api\/blackboards\/draft\/create\?/.test(url)) {
        if (draftCreateForbidden) return new Response(JSON.stringify({error:'Only the workspace owner can create a blackboard.'}), {status:403, headers:{'Content-Type':'application/json'}})
        const plan = draftPlan
        createBody = plan ? { name: plan.name, vision: plan.vision, goals: plan.goals, milestone_name: plan.milestone.name, board_id: plan.board_id } : null
        const milestones = plan?.milestone.name ? [{ id: 'new-milestone', name: plan.milestone.name, target: null, frozen_at: null, stage: '', revision: 1, board_id: plan.board_id ?? '', release_id: '' }] : []
        draftPlan = null
        return json({ id: 'new-record', name: plan?.name ?? '', vision: plan?.vision ?? '', milestones })
      }
      if (/^\/api\/blackboards\/draft\?/.test(url) && (!init?.method || init.method === 'GET')) {
        return json({ draft: draftPlan ? { created_at: '2026-11-01T00:00:00Z', created_by: 'sess-a', untrusted_data: draftPlan } : null })
      }
      if (/^\/api\/blackboards\/draft\?/.test(url) && init?.method === 'DELETE') {
        if (draftDiscardForbidden) return new Response(JSON.stringify({error:'Only the workspace owner can discard a draft.'}), {status:403, headers:{'Content-Type':'application/json'}})
        draftPlan = null
        emit({ type: 'blackboard_draft_changed', session_id: 'sess-a' })
        return json({ status: 'discarded' })
      }
      if (init?.method === 'POST' && /^\/api\/blackboards\?/.test(url)) {
        const body = JSON.parse(String(init.body)) as Record<string, unknown>
        createBody = body
        if (createForbidden) return new Response(JSON.stringify({error:'Only the workspace owner can set up a blackboard.'}), {status:403, headers:{'Content-Type':'application/json'}})
        const milestones = body.milestone_name ? [{ id: 'new-milestone', name: body.milestone_name, target: null, frozen_at: null, stage: '', revision: 1, board_id: body.board_id ?? '', release_id: body.release_id ?? '' }] : []
        return new Response(JSON.stringify({ id: 'new-record', name: body.name, vision: body.vision ?? '', milestones }), { status: 201, headers: { 'Content-Type': 'application/json' } })
      }
      if (revoked) return new Response('{}', {status:403})
      // A plain read of the blackboard the "Set up myself" form just created (no cursor, no
      // sub-path): the fixture above only knows the 'record' id by coincidence of its name
      // containing "/record?"; a freshly created blackboard needs its own minimal record shape.
      if (/^\/api\/blackboards\/new-record\?/.test(url)) {
        const milestones = createBody?.milestone_name ? [{ id: 'new-milestone', name: createBody.milestone_name, target: null, frozen_at: null, stage: '', revision: 1, board_id: createBody.board_id ?? '', release_id: createBody.release_id ?? '' }] : []
        return json({ record: { name: createBody?.name ?? '', vision: createBody?.vision ?? '', plan_revision: 0, milestones, goals: [], thrusts: [], snapshots: [], events: [], proposals: [], version: 1 }, pending_proposals: 0, next_cursor: '' })
      }
      if (url.includes('/plan?')) { const body=JSON.parse(String(init?.body)); planCalled=body; if (planForbidden) return new Response('{}',{status:403}); if (body.revision!==planRevision) return new Response('{}',{status:409}); for (const c of body.changes) { if (c.operation==='add') { const id=String(c.item.id||'item-'+(planItems.length+1)); const idx=planItems.findIndex(i=>i.membership.id===id); const next={card:{id,title:String(c.item.title)},membership:{id,goal_id:c.item.goal_id as string|undefined,state:'planned'},stage:String(c.item.progress||'next')}; if (idx>=0) planItems[idx]=next; else planItems.push(next) } else if (c.operation==='remove') { const idx=planItems.findIndex(i=>i.membership.id===c.item.id); if (idx>=0) planItems[idx]={...planItems[idx],membership:{...planItems[idx].membership,state:'moved_out'},stage:'moved_out'} } } planRevision++; return json({status:'committed'}) }
      if (url.includes('/commit?')) {committed=JSON.parse(String(init?.body));proposals[0].status='committed';return json({status:'committed'})}
      if (url.includes('/slice')) { if (!url.includes('/milestones/')) { return json({cards:planItems,next_cursor:'',complete:true,frozen:false}) } const rows = (url.includes('/past/') || url.includes('/future/')) ? [] : released ? cards.map((c,i)=>({...c,deleted:i===0,stage:i<5?'moved_out':'done',membership:{...c.membership,state:i<5?'moved_out':c.membership.state,reason:'Deferred to the next milestone'}})) : milestoneStage==='Final' ? cards.map((c,i)=>i===0?{...c,drift:true}:c) : cards; const cursor=Number(new URL(url,'https://example.test').searchParams.get('cursor')??0); return json({cards:rows.slice(cursor,cursor+50),next_cursor:cursor+50<rows.length?String(cursor+50):'',complete:true,frozen:released||milestoneStage==='Final'||url.includes('/past/')}) }
      if (url.includes('/record?')) {
        if (smallWorkspace) return json({record:{name:'Solo project',vision:'Ship something useful',plan_revision:planRevision,milestones:[],goals:smallGoals,thrusts:[],snapshots:[],events:[],proposals:[]},pending_proposals:0,next_cursor:''})
        const body = {record:{...record,proposals,milestones:milestones.map(m=>({...m,frozen_at:(released||milestoneFrozen)&&m.id==='release'?(milestoneFrozen??'2026-11-02T12:00:00Z'):m.frozen_at,shipped_at:released&&m.id==='release'?'2026-11-02T12:00:00Z':('shipped_at' in m?m.shipped_at:null),cut_receipt_id:released&&m.id==='release'?'receipt-release':('cut_receipt_id' in m?m.cut_receipt_id:undefined),stage:m.id==='release'?milestoneStage:m.stage})),events:[...record.events,...(malformedEvents?[{id:'empty-decision',milestone_id:'release',kind:'proposal_rejected',source:'person',recorded_at:'2026-11-02T11:00:00Z',payload:null},{id:'assertion',milestone_id:'release',kind:'decision',source:'agent',recorded_at:'2026-11-02T11:30:00Z',payload:{reason:{toString:'not callable'},gate_result:{approved:true}}}]:[])]},pending_proposals:proposals[0].status==='pending'?1:0}; const cursor=Number(new URL(url,'https://example.test').searchParams.get('cursor')??0); return json({...body,record:{...Object.fromEntries(Object.entries(body.record).map(([key,value])=>[key,Array.isArray(value)?value.slice(cursor,cursor+2):value])),version:1},next_cursor:cursor+2<Math.max(...Object.values(body.record).filter(Array.isArray).map(value=>value.length))?String(cursor+2):''}) }
      return json({blackboards: noBlackboard ? [] : [{id:'record',name:smallWorkspace?'Solo project':'Release work',milestones:smallWorkspace?[]:milestones}]})
    }
    if (/\/board-cards\/b-rel\/\d+\?/.test(url)) {
      const number=Number(url.match(/b-rel\/(\d+)/)?.[1]);return json({card:{...(cards[number-1]?.card ?? { id: 'owner-card-' + number, board_id: 'b-rel', number, reference: 'BLG-' + number, title: 'Evidence for the next iteration' }),version:1,rank:'a',column_id:'todo',column_name:'In work',body:'Keep release evidence beside the work.\n\nAcceptance: a person can trace scope and decisions without leaving the workspace.',columns:[{id:'todo',name:'In work'}]},columns:[{id:'todo',name:'In work'}]})
    }
    if (url.includes('/board-cards/b-rel/view')) return json({ columns: [{ id: 'todo', name: 'To do' }, { id: 'done', name: 'Done' }], cards: [{ id: 'card-1', board_id: 'b-rel', number: 1, reference: 'REL-1', title: 'Ship the release', tags: ['to-test'], version: 4, rank: 'a', column_id: cardColumn }], releases: releasesServed })
    if (url.includes('/board-cards/b-rel/1/move')) { cardColumn = 'done'; return json({ moved: true }) }
    if (url.includes('/board-cards/b-rel/1')) return json({ card: { id: 'card-1', board_id: 'b-rel', number: 1, title: 'Ship the release', version: 4, rank: 'a', column_id: cardColumn, columns: [{ id: 'todo', name: 'To do' }, { id: 'done', name: 'Done' }] } })
    if (url.startsWith('/api/models/')) return json({ source: 'live', models: [{ id: 'claude-sonnet-4-6', name: 'Sonnet', section: 'main', efforts: ['low', 'high'], default_effort: 'high' }] })
    if (url.includes('shell-capability')) return json({ available: true })
    if (url.includes('/live')) return json({ session_id: 'sess-a', through_seq: 0, generated_at: new Date().toISOString(), tokens_hidden: true, asks: [], subagents: [], tasks: [], tool_changes: 0, counts: { needs_you: 0, running: 0 }, can_release_prompt: true, children: [{ id: 'worker-a', title: 'Release worker', engine: 'codex', model: '', status: 'running', started_at: '2026-10-27T00:00:00Z', asking: false, tokens: null, ticket: { source: 'blerg-board', board_id: 'b-rel', key: '#1', title: 'Ship the release', status: 'To do' } }] })
    if (url.endsWith('/permissions')) return json({ can_grant: true, reason: '', grants: [], mcp_tools: [], boards })
    if (url === '/api/site-board') return json({ board_url: 'https://board.example.com' })
    if (url === '/api/session-boards') return json({ configured: true, boards: [{ id: 'b-rel', key: 'REL', key_aliases: ['BLG'], name: boards.find(b => b.board_id === 'b-rel')?.board_name ?? 'Release planning' }] })
    return json({})
  })
  vi.stubGlobal('fetch', vi.fn(async () => json([])))
})
afterEach(() => { window.history.replaceState({}, '', '/'); invalidateChatTransport(); vi.unstubAllGlobals(); document.documentElement.removeAttribute('data-theme'); localStorage.removeItem('blerg-theme') })

function expectProportionalTracking(heading: Element) {
 const style = getComputedStyle(heading)
 expect(parseFloat(style.letterSpacing)).toBeCloseTo(-0.03 * parseFloat(style.fontSize), 2)
}

for (const width of [390,1920]) it('keeps heading tracking proportional across other global consumers at ' + width + 'px', async () => {
 await page.viewport(width, 900)
 render(<div style={{ background: 'var(--basalt)', color: 'var(--chalk)', padding: 20 }}>
  <SessionsHome onNewSession={() => {}} />
  <InsightsSkeleton />
  <section className="insights"><h1>Workspace insights</h1><div className="insights-question"><h1>Am I on pace for my limits?</h1></div></section>
  <h1>Display heading</h1>
 </div>)
 const home = getComputedStyle(screen.getByRole('heading', { name: 'No workspace selected' }))
 expect(parseFloat(home.letterSpacing)).toBeCloseTo(0.14 * parseFloat(home.fontSize), 2)
 for (const heading of document.querySelectorAll('h1')) {
  if (heading.textContent !== 'No workspace selected') expectProportionalTracking(heading)
 }
 const shots = import.meta.env.VITE_BLACKBOARD_SHOTS_DIR
 if (shots) await page.screenshot({ path: shots + '/heading-consumers-' + width + '.png' })
})

function mount() {
  return render(<MemoryRouter initialEntries={['/sessions/sess-a']}><div style={{ height: '100vh' }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div></MemoryRouter>)
}

// Open blackboard is a header control at a normal width, and a menu item inside the header's
// collapsed Open menu below a narrow width (card BLG-450). It is never disabled (round 3 review):
// it opens the empty-state screen until its own fetch of this workspace's blackboards resolves
// with a real one, then opens that instead.
async function openBlackboard() {
  const menuTrigger = screen.queryByTestId('open-tab-menu')
  if (menuTrigger) fireEvent.click(menuTrigger)
  const open = await screen.findByRole(menuTrigger ? 'menuitem' : 'button', { name: 'Open blackboard' })
  // Let the (mocked, immediately-resolving) blackboard-list fetch land before clicking, so the
  // view captured at click time is this workspace's real blackboard, not the empty-state
  // sentinel — the button itself is never disabled while that fetch is in flight (round 3 review).
  await act(async () => {})
  fireEvent.click(open)
  return open
}

for (const width of [390, 3840]) it('navigates the real blackboard tab at ' + width + 'px', async () => {
 await page.viewport(width, width === 390 ? 844 : 1200)
 mount()
 const shots = import.meta.env.VITE_BLACKBOARD_SHOTS_DIR
 if (shots) await page.screenshot({path: shots + '/before-' + width + '.png'})
 await openBlackboard()
 await screen.findByRole('navigation', {name: 'Blackboard levels'})
 await screen.findByRole('button', {name:/Recover work/})
 expectProportionalTracking(screen.getByRole('heading',{name:'0.6.0',level:1}))
 // Structure renders before paginated card details; wait for completion, not a count.
 await waitFor(() => expect(screen.queryByText('Loading card details…')).toBeNull())
 expect(screen.getByText('Cut Nov 5 · 150 cards')).toBeTruthy()
 expect(screen.getByText('Main proposes 2 plan changes.')).toBeTruthy()
 expect(screen.queryByText('Today')).toBeNull()
 expect(screen.queryByText(/Daily observations ·/)).toBeNull()
 const svg = document.querySelector<SVGSVGElement>('.blackboard-chart svg')!
 for (const rect of svg.querySelectorAll<SVGRectElement>('.blackboard-day rect')) expect(rect.width.baseVal.value).toBeLessThanOrEqual(48)
 expect(svg.querySelectorAll('.blackboard-today')).toHaveLength(1)
 const today = svg.querySelector<SVGLineElement>('.blackboard-today')!
 expect(today.y1.baseVal.value).toBe(4)
 expect(today.y2.baseVal.value).toBe(80)
 for (const rect of svg.querySelectorAll<SVGRectElement>('.blackboard-day > g rect')) expect(today.x1.baseVal.value < rect.x.baseVal.value || today.x1.baseVal.value > rect.x.baseVal.value + rect.width.baseVal.value).toBe(true)
 expect(svg.querySelector('.blackboard-latest')?.textContent).not.toMatch(/\d{4}-\d{2}-\d{2}/)
 const refresh = screen.getByRole('button',{name:'Refresh blackboard'})
 expect(refresh.getBoundingClientRect().right).toBeGreaterThan(document.querySelector('.blackboard-nav')!.getBoundingClientRect().right-4)
 for(const button of document.querySelectorAll('.blackboard-pill')) expect(getComputedStyle(button).borderWidth).toBe('0px')
 await waitFor(() => { const svg=document.querySelector<SVGSVGElement>('.blackboard-chart svg')!; expect(Math.abs(svg.viewBox.baseVal.width-svg.getBoundingClientRect().width)).toBeLessThan(2); for(const text of svg.querySelectorAll('text')) expect(Number(text.getAttribute('x'))).toBeLessThanOrEqual(svg.viewBox.baseVal.width); const ticks=[...svg.querySelectorAll<SVGTextElement>('text[y="108"]')]; for(let i=1;i<ticks.length;i++) expect(ticks[i].getBBox().x-ticks[i-1].getBBox().x-ticks[i-1].getBBox().width).toBeGreaterThanOrEqual(8) })
 expect(document.querySelector('.blackboard')!.scrollWidth).toBeLessThanOrEqual(document.querySelector('.blackboard')!.clientWidth + 1)
 const tiles=[...document.querySelectorAll<HTMLElement>('.blackboard-grid')][0]
 const tileButtons=[...tiles.querySelectorAll<HTMLElement>('.blackboard-tile')]
 expect(getComputedStyle(tileButtons[0]).borderWidth).toBe('0px')
 expect(Math.max(...tileButtons.map(b=>b.getBoundingClientRect().width))).toBeLessThanOrEqual(document.querySelector('.blackboard')!.clientWidth)
 if (shots) await page.screenshot({path: shots + '/slice-' + width + '.png'})
 {
  const clock=installHoverClock()
  try {
   const event=document.querySelector('.blackboard-chart-event')!
   fireEvent.pointerEnter(event,{pointerType:'mouse'}); act(()=>clock.advance(1000))
   const hover=await screen.findByTestId('hovercard')
   expect(hover.textContent).toContain('Recorded Oct 29')
   expect(hover.textContent).not.toMatch(/2026-10-29/)
   if(shots)await page.screenshot({path:shots+'/event-hover-'+width+'.png'})
   fireEvent.pointerLeave(event,{pointerType:'mouse'});act(()=>clock.advance(1000))
  } finally {clock.uninstall()}
 }
 fireEvent.click(screen.getByRole('button', {name:/Recover work/}))
 const filters = screen.getByRole('group', {name:'Card filters'})
 expect(within(filters).getByRole('button',{name:/Active/}).getAttribute('aria-pressed')).toBe('true')
 expect(document.querySelectorAll('.blackboard .shared-board-card').length).toBe(15)
 expect(screen.getByRole('tab',{name:'Blackboard'})).toBeTruthy()
 if (shots) await page.screenshot({path: shots + '/thrust-' + width + '.png'})
 const firstCard = document.querySelector<HTMLButtonElement>('.blackboard .shared-board-card')!
 fireEvent.click(firstCard)
 await screen.findByTestId('board-card-live')
 fireEvent(window, new Event('focus'))
 await waitFor(() => expect(screen.getByTestId('board-card-live')).toBeTruthy())
 if (shots) await page.screenshot({path: shots + '/card-' + width + '.png'})
 fireEvent.click(screen.getByRole('button', {name:'Close'}))
 fireEvent.change(screen.getByRole('combobox',{name:'Sort'}), {target:{value:'title'}})
 expect(document.querySelector('.blackboard .shared-board-card .num')?.textContent).toBe('DEMO-73')
 fireEvent.click(within(filters).getByRole('button',{name:/All/}))
 expect(document.querySelectorAll('.blackboard .shared-board-card').length).toBe(25)
 fireEvent.change(screen.getByRole('combobox',{name:'Sort'}),{target:{value:'number'}})
 expect(document.querySelector('.blackboard .shared-board-card .num')?.textContent).toBe('DEMO-1')
 fireEvent.click(within(screen.getByRole('navigation',{name:'Blackboard levels'})).getByRole('button',{name:'Project'}))
 await screen.findByText('An IDE for AI collaboration')
 expect(document.querySelectorAll('.blackboard .shared-board-card').length).toBe(0)
 if (shots) await page.screenshot({path: shots + '/project-' + width + '.png'})
 {
  const clock = installHoverClock()
  try {
   const tile = document.querySelector('.blackboard-tile')!
   fireEvent.pointerEnter(tile, {pointerType:'mouse'})
   act(() => clock.advance(1000))
   const hover = await screen.findByTestId('hovercard')
   expect(hover.textContent).not.toContain(cards[0].card.title)
   expect(hover.textContent).not.toMatch(/DEMO-/)
   if (shots) await page.screenshot({path: shots + '/project-hover-' + width + '.png'})
   fireEvent.pointerLeave(tile, {pointerType:'mouse'})
   act(() => clock.advance(1000))
   const shipped = within(screen.getByRole('group',{name:'Project filters'})).getByRole('button',{name:/Shipped/})
   fireEvent.pointerEnter(shipped,{pointerType:'mouse'})
   act(() => clock.advance(1000))
   const stats=await screen.findByTestId('hovercard')
   expect(stats.textContent).toContain('1 release')
   expect(stats.textContent).not.toMatch(/DEMO-/)
   if(shots)await page.screenshot({path:shots+'/shipped-hover-'+width+'.png'})
   fireEvent.pointerLeave(shipped,{pointerType:'mouse'})
   act(() => clock.advance(1000))
  } finally {clock.uninstall()}
 }
 fireEvent.click(within(screen.getByRole('group',{name:'Project filters'})).getByRole('button',{name:/Shipped/}))
 fireEvent.click(screen.getAllByRole('button',{name:/0.5.0/})[0])
 await screen.findByText('This release predates the record. No daily history is available.')
 if (shots) await page.screenshot({path: shots + '/release-' + width + '.png'})
 fireEvent.click(screen.getByRole('button',{name:'Review'}))
 const review = await screen.findByRole('dialog',{name:'Plan changes for 0.6.0'})
 expect(within(review).getByRole('heading',{name:'Plan changes · 0.6.0'})).toBeTruthy()
 expect(within(review).queryByRole('button',{name:'Commit selected changes'})).toBeNull()
 expect(review.getBoundingClientRect().right).toBeLessThanOrEqual(innerWidth + 1)
 expect(within(review).queryAllByRole('checkbox')).toHaveLength(0)
 if (shots) await page.screenshot({path: shots + '/proposal-' + width + '.png'})
 fireEvent.click(within(review).getByRole('button',{name:'Close plan review'}))
 expect(committed).toBeNull()
 await screen.findByRole('navigation',{name:'Blackboard levels'})
 const sourceTab = screen.getByRole('tab', {name:'Blackboard'})
 fireEvent.contextMenu(sourceTab)
 fireEvent.click(await screen.findByRole('menuitem',{name:'Duplicate'}))
 await waitFor(() => expect(screen.getAllByRole('tab',{name:'Blackboard'})).toHaveLength(2))
 await screen.findByText('This release predates the record. No daily history is available.')
 revoked = true
 fireEvent.click(screen.getByRole('button',{name:'Refresh blackboard'}))
 await screen.findByText('This workspace cannot read this blackboard.')
 expect(screen.queryByText('An IDE for AI collaboration')).toBeNull()
})

for (const width of [390,3840]) it('keeps timeline assertions readable at ' + width + 'px', async()=>{
 malformedEvents=true
 await page.viewport(width,width===390?844:1200)
 mount()
 await openBlackboard()
 await screen.findByRole('img',{name:/Proposal declined/})
 expect(screen.queryByText('[object Object]')).toBeNull()
 const shots=import.meta.env.VITE_BLACKBOARD_SHOTS_DIR
 if(shots)await page.screenshot({path:shots+'/evidence-'+width+'.png'})
})

for (const width of [390,3840]) it('shows frozen release scope at ' + width + 'px',async()=>{
 released=true
 await page.viewport(width,width===390?844:1200)
 mount()
 await openBlackboard()
 await screen.findByRole('button',{name:/Shipped 145/})
 const filters=screen.getByRole('group',{name:'Card filters'})
 expect(within(filters).getAllByRole('button').map(b=>b.textContent)).toEqual(['All 150','Shipped 145','Moved out 5','Unplanned 21'])
 const shots=import.meta.env.VITE_BLACKBOARD_SHOTS_DIR
 if(shots)await page.screenshot({path:shots+'/frozen-release-'+width+'.png'})
 fireEvent.click(within(filters).getByRole('button',{name:/Moved out/}))
 expect(document.querySelectorAll('.blackboard-tile').length).toBe(5)
 fireEvent.click(screen.getByRole('button',{name:/Recover work/}))
 const deleted=[...document.querySelectorAll<HTMLButtonElement>('.blackboard .shared-board-card')].find(b=>b.querySelector('.num')?.textContent==='DEMO-1')!
 fireEvent.click(deleted)
 await screen.findByText('This card was deleted from the board. Showing the title preserved for 0.6.0.')
 expect(screen.queryByText(/agent.s copy/)).toBeNull()
 if(shots)await page.screenshot({path:shots+'/deleted-card-'+width+'.png'})
})

for (const width of [390,3840]) it('shows a small workspace plan without mutation controls at ' + width + 'px', async () => {
 smallWorkspace = true
 await page.viewport(width, width === 390 ? 844 : 1200)
 mount()
 await openBlackboard()
 await screen.findByRole('heading', {name:'Solo project', level:1})
 expect(within(screen.getByRole('navigation',{name:'Blackboard levels'})).getAllByRole('button',{name:'Project'})).toHaveLength(1)
 expect(screen.queryByRole('group', {name:'Project filters'})).toBeNull()
 expect(screen.queryByRole('group', {name:'Card filters'})).toBeNull()
 expect(document.querySelector('.blackboard-chart')).toBeNull()
 expect(document.querySelector('.blackboard-bar')).toBeNull()
 await screen.findByText('Write the first chapter')
 const shots = import.meta.env.VITE_BLACKBOARD_SHOTS_DIR
 if (shots) await page.screenshot({path: shots + '/small-workspace-' + width + '.png'})
 expect(screen.queryByRole('textbox', {name:"What's next"})).toBeNull()
 expect(screen.queryByRole('button', {name:'Add'})).toBeNull()
 expect(within(document.querySelector<HTMLElement>('.blackboard')!).queryAllByRole('checkbox')).toHaveLength(0)
 expect(planCalled).toBeNull()
})

for (const width of [390,3840]) it('keeps cut decisions out of the blackboard at ' + width + 'px', async () => {
 milestoneStage = 'Finalizing'
 await page.viewport(width, width === 390 ? 844 : 1200)
 mount()
 await openBlackboard()
 await screen.findByText('Released on board; evidence incomplete')
 for (const name of ['Final','Retry Final','Abort','Approve','Reject','Create','Edit','Discard']) expect(screen.queryByRole('button', {name})).toBeNull()
 expect(screen.queryByRole('combobox', {name:/release/i})).toBeNull()
 expect(finalCalled).toBeNull()
 expect(abortCalled).toBeNull()
})

for (const width of [390,3840]) it('keeps plain items visible once a milestone exists at ' + width + 'px', async () => {
 await page.viewport(width, width === 390 ? 844 : 1200)
 mount()
 await openBlackboard()
 const nav = await screen.findByRole('navigation', {name:'Blackboard levels'})
 fireEvent.click(within(nav).getByRole('button',{name:'Project'}))
 await screen.findByText('An IDE for AI collaboration')
 await screen.findByText('Write the first chapter')
 expect(screen.getByText('No goal')).toBeTruthy()
 const shots = import.meta.env.VITE_BLACKBOARD_SHOTS_DIR
 if (shots) await page.screenshot({path: shots + '/plain-with-milestone-' + width + '.png'})
})

it('never lists card titles in a thrust tile hovercard', async () => {
 await page.viewport(3840, 1200)
 mount()
 await openBlackboard()
 await screen.findByRole('navigation', {name:'Blackboard levels'})
 const clock = installHoverClock()
 try {
  const thrustTile = document.querySelector<HTMLButtonElement>('.blackboard-tile')!
  fireEvent.pointerEnter(thrustTile, {pointerType:'mouse'})
  act(() => clock.advance(1000))
  const hover = await screen.findByTestId('hovercard')
  for (const name of names) expect(hover.textContent).not.toContain(name)
  fireEvent.pointerLeave(thrustTile, {pointerType:'mouse'})
  act(() => clock.advance(1000))
 } finally { clock.uninstall() }
})

for (const width of [390,3840]) it('groups plain items by goal, keeps an empty goal visible, and buckets a stale goal under No goal at ' + width + 'px', async () => {
 smallWorkspace = true
 smallGoals = [{ id: 'write', name: 'Write the thing' }, { id: 'edit', name: 'Edit the thing' }]
 planItems = [
  { card: { id: 'item-1', title: 'Write the first chapter' }, membership: { id: 'item-1', goal_id: 'write', state: 'planned' }, stage: 'next' },
  { card: { id: 'item-2', title: 'Ask a reader' }, membership: { id: 'item-2', goal_id: 'ghost', state: 'planned' }, stage: 'next' },
 ]
 await page.viewport(width, width === 390 ? 844 : 1200)
 mount()
 await openBlackboard()
 await screen.findByText('Write the thing')
 await screen.findByText('Edit the thing')
 await screen.findByText('No goal')
 expect(screen.getByText('Write the first chapter')).toBeTruthy()
 expect(screen.getByText('Ask a reader')).toBeTruthy()
 expect(screen.getByText('Nothing planned yet.')).toBeTruthy()
 const shots = import.meta.env.VITE_BLACKBOARD_SHOTS_DIR
 if (shots) await page.screenshot({path: shots + '/small-workspace-goals-' + width + '.png'})
})

// Card BLG-450 phase 2 / round 3 review: the empty blackboard screen. The opener is never
// disabled — with no blackboard yet it opens this screen instead of trying to load one that
// doesn't exist (fix #3). It explains what a blackboard is in plain words and offers the two ways
// to start one, both visible, neither hidden in a menu.
for (const width of [390,1024,1920,3840]) it('keeps the empty blackboard read-only at ' + width + 'px', async () => {
 noBlackboard = true
 await page.viewport(width,900)
 mount()
 await openBlackboard()
 await screen.findByRole('heading',{name:'Give your next idea a clear path'})
 const pane = document.querySelector<HTMLElement>('.blackboard')!
 expect(within(pane).queryAllByRole('button')).toHaveLength(0)
 expect(within(pane).queryAllByRole('textbox')).toHaveLength(0)
 expect(pane.scrollWidth).toBeLessThanOrEqual(pane.clientWidth+1)
})

const samplePlan = { name: 'Workspace plan', vision: 'Keep planning clear.', goals: ['First goal'], milestone: { name: 'v0.1' }, board_id: 'b-rel', thrusts: [{ name: 'Initial work', goal_index: 0, reason: 'Keep progress visible.' }] }

it('shows the pending draft instead of the empty state when one is stored', async () => {
 noBlackboard = true
 draftPlan = samplePlan
 await page.viewport(1440, 900)
 mount()
 await openBlackboard()
 await screen.findByText('Workspace plan')
 expect(screen.queryByRole('heading', { name: "Give your next idea a clear path" })).toBeNull()
 expect(screen.queryByRole('button', { name: 'Create' })).toBeNull()
 expect(screen.queryByRole('button', { name: 'Edit' })).toBeNull()
 expect(screen.queryByRole('button', { name: 'Discard' })).toBeNull()
})

it('marks the draft\'s own content as agent-derived and not yet committed', async () => {
 noBlackboard = true
 draftPlan = samplePlan
 await page.viewport(1440, 900)
 mount()
 await openBlackboard()
 const heading = await screen.findByText('Workspace plan')
 expectProportionalTracking(heading)
 expect(getComputedStyle(heading).fontStyle).toBe('normal')
 expect(getComputedStyle(screen.getByText(samplePlan.goals[0]))).toMatchObject({ textTransform: 'none', fontStyle: 'normal' })
 expect(screen.getByText('Draft from Main, not yet created. Decide in chat or Needs you.')).toBeVisible()

})

it('lays the draft out as goal-grouped tiles, like the real blackboard', async () => {
 noBlackboard = true
 draftPlan = samplePlan
 await page.viewport(1440, 900)
 mount()
 await openBlackboard()
 await screen.findByText('Workspace plan')
 expect(screen.queryByRole('navigation', { name: 'Blackboard levels' })).toBeNull()
 expect(document.querySelector('.blackboard-heading h1')?.textContent).toBe('Workspace plan')
 expect(document.querySelector('.blackboard-empty-card')).toBeNull()
 const grid = document.querySelector<HTMLElement>('.blackboard-draft-groups')!
 expect(grid).toBeTruthy()
 const tile = grid.querySelector<HTMLElement>('.blackboard-draft-thrust')!
 expect(tile.textContent).toContain(samplePlan.thrusts[0].name)
 expect(tile.textContent).toContain(samplePlan.thrusts[0].reason)
 expect(tile.querySelector('.blackboard-bar')).toBeNull()
})

// Owner's real draft; thrust names/reasons are representative because only their shape was supplied.
const ownerPlan = {
 name: 'Blerg',
 vision: 'A tighter loop for any iterative work: consistent, any provider, on your own machine, and steered by what actually happened.',
 goals: [
  'Tight loops: fast and calm, never makes you wait; agent work yields to you.',
  'Consistent and cross-provider: the same experience and rules whichever engine does the work.',
  'Local and yours: runs on your machine or your cluster; your data stays where you put it.',
  'Closed-loop control: every turn leaves deterministic evidence (gates, reviews, failures, timings) that steers the next.',
  'Clear and joyful: you always know what is happening, and it is pleasant to use.',
 ],
 milestone: { name: '0.6.0', target: '2026-11-06' }, board_id: 'b-rel',
 thrusts: [
  { name: 'Keep the loop moving', goal_index: 0, reason: 'BLG-494 keeps the live draft current without making you wait or losing your place.' },
  { name: 'One experience across engines', goal_index: 1, reason: 'BLG-450 gives every provider the same blackboard and planning rules.' },
  { name: 'Keep work on your machine', goal_index: 2, reason: 'BLG-447 keeps unattended work predictable on your own machine or cluster.' },
  { name: 'Let evidence steer the next turn', goal_index: 3, reason: 'BLG-453 brings deterministic review and gate evidence back into the loop.' },
  { name: 'Make the plan easy to read', goal_index: 4, reason: 'BLG-495 keeps goals, thrusts and their reasons together at every pane size.' },
 ],
}
for (const theme of ['dark', 'colorblind-dark'] as const) for (const width of [390, 1024, 3840]) for (const committedView of [false, true]) {
 it('captures owner plan ' + (committedView ? 'committed' : 'draft') + ' ' + theme + ' at ' + width + 'px', async () => {
  await page.viewport(width === 1024 ? 1440 : width, 1600)
  setTheme(theme)
  const cardThrusts = cards.map(c => c.membership.thrust_id)
  const saved = { goals: record.goals, thrusts: record.thrusts, name: record.name, vision: record.vision, target: milestones[0].target }
  try {
   if (committedView) {
    cards.forEach((c, i) => { c.membership.thrust_id = 'push-' + (i % 5) })
    record.name = ownerPlan.name
    record.vision = ownerPlan.vision
    record.goals = ownerPlan.goals.map((name, i) => ({ id: 'owner-' + i, name }))
    record.thrusts = ownerPlan.thrusts.map((t, i) => ({ id: 'push-' + i, name: t.name, goal_id: 'owner-' + i, milestone_id: 'release' }))
    milestones[0].target = ownerPlan.milestone.target
   } else { noBlackboard = true; draftPlan = ownerPlan }
   mount()
   await openBlackboard()
   if (committedView) await screen.findByRole('navigation', { name: 'Blackboard levels' })
   else { await screen.findByRole('heading', { name: ownerPlan.name }); await waitFor(() => expect(document.querySelectorAll('.blackboard-draft-thrust .plan-ref')).toHaveLength(5)) }
   const pane = document.querySelector<HTMLElement>('.blackboard')!
   if (width === 1024) {
    const host = pane.closest<HTMLElement>('.hosted-view') ?? pane.parentElement!
    host.style.width = '1024px'; host.style.maxWidth = '1024px'; host.style.flex = '0 0 1024px'
   }
   await act(async () => { await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))) })
   if (width === 1024) expect(pane.getBoundingClientRect().width).toBe(1024)
   expect(pane.scrollWidth).toBeLessThanOrEqual(pane.clientWidth + 1)
   for (const h of pane.querySelectorAll('h2')) expect(getComputedStyle(h).textTransform).toBe('none')
   for (const tile of pane.querySelectorAll('.blackboard-tile, .blackboard-draft-thrust')) expect(tile.getBoundingClientRect().width).toBeLessThanOrEqual(pane.clientWidth)
   const shots = import.meta.env.VITE_BLACKBOARD_SHOTS_DIR
   if (shots) await page.screenshot({ path: shots + '/owner-' + (committedView ? 'committed' : 'draft') + '-' + theme + '-' + width + '.png' })
  } finally { cards.forEach((c, i) => { c.membership.thrust_id = cardThrusts[i] }); Object.assign(record, { goals: saved.goals, thrusts: saved.thrusts, name: saved.name, vision: saved.vision }); milestones[0].target = saved.target }
 })
}

it('keeps draft markup literal, including reasons', async () => {
 noBlackboard = true
 draftPlan = { ...samplePlan, vision: '<b>literal vision</b> **plain**', thrusts: [{ name: '<em>name</em>', goal_index: 0, reason: '<img src=x> **reason**' }] }
 mount()
 await openBlackboard()
 expect(await screen.findByText('<b>literal vision</b> **plain**')).toBeVisible()
 expect(screen.getByText('<img src=x> **reason**')).toBeVisible()
 expect(document.querySelector('.blackboard img, .blackboard em, .blackboard b')).toBeNull()
})

// Deliberately different sizes and stage mixes make goal rollups distinguishable from thrusts.
const committedThrusts = [
 { goal: 0, name: 'Keep the loop moving', counts: [9, 4, 3, 2, 0] },
 { goal: 0, name: 'Yield promptly to you', counts: [3, 5, 2, 1, 1] },
 { goal: 0, name: 'Refresh without losing your place', counts: [1, 2, 3, 2, 1] },
 { goal: 0, name: 'Keep background work calm', counts: [0, 1, 3, 1, 1] },
 { goal: 1, name: 'One experience across engines', counts: [8, 4, 2, 2, 0] },
 { goal: 1, name: 'Share the same planning rules', counts: [2, 3, 4, 1, 1] },
 { goal: 1, name: 'Explain provider state clearly', counts: [0, 2, 3, 2, 1] },
 { goal: 2, name: 'Keep work on your machine', counts: [12, 3, 2, 2, 1] },
 { goal: 2, name: 'Recover local work reliably', counts: [1, 2, 4, 2, 1] },
 { goal: 3, name: 'Let evidence steer the next turn', counts: [2, 3, 7, 4, 1] },
 { goal: 3, name: 'Keep review evidence traceable', counts: [1, 1, 5, 4, 2] },
 { goal: 4, name: 'Make the plan easy to read', counts: [2, 1, 3, 3, 1] },
]

for (const width of [390, 1024, 3840]) for (const level of ['project', 'milestone'] as const) {
 it('compares the live ' + level + ' with the rendered design at ' + width + 'px', async () => {
  await page.viewport(width === 1024 ? 1440 : width, (width === 390 ? 2000 : 1600))
  const saved = { name: record.name, vision: record.vision, goals: record.goals, thrusts: record.thrusts, milestones: [...milestones], cards: [...cards], snapshots: record.snapshots }
  planItems = []
  record.name = ownerPlan.name
  record.vision = ownerPlan.vision
  record.goals = ownerPlan.goals.map((name, i) => ({ id: 'owner-' + i, name }))
  record.thrusts = committedThrusts.map((t, i) => ({ id: 'push-' + i, name: t.name, goal_id: 'owner-' + t.goal, milestone_id: 'release' }))
  milestones.splice(0, milestones.length, { ...milestones[0], target: ownerPlan.milestone.target, frozen_at: null })
  let nextCard = 0
  const seeded = committedThrusts.flatMap((t, thrust) => t.counts.flatMap((n, stage) => Array.from({ length: n }, () => {
   const original = saved.cards[nextCard++]
   return { ...original, membership: { ...original.membership, thrust_id: 'push-' + thrust }, stage: ['done', 'to_test', 'in_work', 'next', 'needs_you'][stage] }
  })))
  cards.splice(0, cards.length, ...seeded)
  const totals = { done: 0, to_test: 0, in_work: 0, next: 0, needs_you: 0 }
  for (const c of cards) totals[c.stage as keyof typeof totals]++
  record.snapshots = [...record.snapshots, { milestone_id: 'release', day: '2026-11-04', complete: true, counts: totals }]
  try {
  const app = mount()
  await openBlackboard()
  await screen.findByRole('heading', { name: '0.6.0' })
  if (level === 'project') {
   fireEvent.click(within(screen.getByRole('navigation', { name: 'Blackboard levels' })).getByRole('button', { name: 'Project' }))
   await screen.findByRole('heading', { name: record.name })
  }
  const pane = document.querySelector<HTMLElement>('.blackboard')!
  if (width === 1024) {
   const host = pane.closest<HTMLElement>('.hosted-view') ?? pane.parentElement!
   host.style.width = '1024px'; host.style.maxWidth = '1024px'; host.style.flex = '1 1 auto'
  }
  await act(async () => { await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))) })
  expect(screen.getByRole('tab', { name: 'Blackboard' })).toHaveAttribute('aria-selected', 'true')
  if (width === 1024) expect(pane.getBoundingClientRect().width).toBe(1024)
  expect(pane.scrollWidth).toBeLessThanOrEqual(pane.clientWidth + 1)
  if (level === 'project') {
   expect(document.querySelector('.blackboard-chart')).toBeNull()
   expect(screen.getByRole('button', { name: /All 1 milestone/ })).toBeVisible()
   expect(screen.getByRole('button', { name: /0.6.0 12 thrusts/ })).toBeVisible()
   expect(pane.querySelectorAll('.blackboard-thrust')).toHaveLength(12)
   expect(pane.querySelectorAll('.blackboard-goal-caption')).toHaveLength(12)
   expect(pane.querySelector('.blackboard-history')).toBeTruthy()
  } else {
   const grids = [...pane.querySelectorAll<HTMLElement>('.blackboard-committed-groups .blackboard-grid')]
   const first = [...grids[0].querySelectorAll<HTMLElement>('.blackboard-tile')].map(c => c.getBoundingClientRect())
   expect(first).toHaveLength(4)
   expect(new Set(first.map(c => Math.round(c.top))).size).toBeGreaterThanOrEqual(1)
   expect(Math.max(...first.map(c => c.width))).toBeLessThanOrEqual(document.querySelector('.blackboard')!.clientWidth)
   const counts = [...pane.querySelectorAll('.blackboard-tile small')].map(n => n.textContent)
   expect(counts).toHaveLength(12)
  }
  const shots = import.meta.env.VITE_BLACKBOARD_SHOTS_DIR
  if (shots) await page.screenshot({ path: shots + '/round3-live-' + level + '-' + width + '.png' })
  if (shots && width === 390 && level === 'project') {
   pane.scrollTop = pane.scrollHeight
   await act(async () => { await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))) })
   await page.screenshot({ path: shots + '/round3-live-project-390-bottom.png' })
  }
  app.unmount()
  const mock = render(<iframe title="Blackboard design reference" srcDoc={blackboardMockup} style={{ display: 'block', width, height: (width === 390 ? 2000 : 1600), border: 0 }} />)
  const frame = screen.getByTitle('Blackboard design reference') as HTMLIFrameElement
  await waitFor(() => expect(frame.contentDocument?.querySelector('#m h1')?.textContent).toBe('0.6.0'))
  if (level === 'project') {
   act(() => frame.contentDocument!.querySelector<HTMLButtonElement>('[data-nav="project"]')!.click())
   await waitFor(() => expect(frame.contentDocument?.querySelector('#m h1')?.textContent).toBe('Blerg'))
  }
  if (shots) await page.screenshot({ path: shots + '/round3-mockup-' + level + '-' + width + '.png' })
  mock.unmount()
  } finally {
   Object.assign(record, { name: saved.name, vision: saved.vision, goals: saved.goals, thrusts: saved.thrusts })
   milestones.splice(0, milestones.length, ...saved.milestones)
   cards.splice(0, cards.length, ...saved.cards)
   record.snapshots = saved.snapshots
  }
 })
}

it('keeps Project distinct when thrusts have no cards and restores the stable tab after navigating away', async () => {
 const saved = [...cards]
 cards.length = 0
 try {
  mount()
  await openBlackboard()
  await screen.findByRole('heading', { name: '0.6.0' })
  fireEvent.click(within(screen.getByRole('navigation', { name: 'Blackboard levels' })).getByRole('button', { name: 'Project' }))
  await screen.findByRole('heading', { name: record.name })
  expect(screen.queryAllByText(/0 of 0/)).toHaveLength(0)
  expect(screen.getByText('Milestone history')).toBeTruthy()
  expect(screen.getByRole('button', { name: /Recover work/ })).toBeVisible()
  fireEvent.click(screen.getByRole('tab', { name: 'Main' }))
  fireEvent.click(screen.getByRole('tab', { name: 'Blackboard' }))
  expect(await screen.findByRole('heading', { name: record.name })).toBeVisible()
  fireEvent.click(within(screen.getByRole('group', { name: 'Project filters' })).getByRole('button', { name: 'Icebox' }))
  expect(screen.getByText('Icebox counts are not available in this record.')).toBeVisible()
 } finally { cards.push(...saved) }
})

it('renders the JS design reference for both blackboard levels', async () => {
 await page.viewport(1024, 1200)
 render(<iframe title="Rendered design" srcDoc={blackboardMockup} style={{ width: '100%', height: 1200, border: 0 }} />)
 const frame = screen.getByTitle('Rendered design') as HTMLIFrameElement
 await waitFor(() => expect(frame.contentDocument?.querySelector('#m h1')?.textContent).toBe('0.6.0'))
 const shots = import.meta.env.VITE_BLACKBOARD_SHOTS_DIR
 if (shots) await page.screenshot({ path: shots + '/reference-milestone.png' })
 act(() => frame.contentDocument!.querySelector<HTMLButtonElement>('[data-nav="project"]')!.click())
 await waitFor(() => expect(frame.contentDocument?.querySelector('#m h1')?.textContent).toBe('Blerg'))
 if (shots) await page.screenshot({ path: shots + '/reference-project.png' })
})

it('shows a goal path through shipped and future milestones without inventing their card history', async () => {
 await page.viewport(1440, 1200)
 setTheme('colorblind-dark')
 planItems = []
 const saved = { milestones: [...milestones], thrusts: record.thrusts }
 try {
  milestones.push({ ...milestones[0], id: 'future', name: '0.7.0', target: '2026-12-01', frozen_at: null })
  record.thrusts = [...record.thrusts,
   { id: 'past-ux', name: 'Earlier UX work', goal_id: 'ux', milestone_id: 'past' },
   { id: 'future-ux', name: 'Next UX work', goal_id: 'ux', milestone_id: 'future' },
  ]
  mount()
  await openBlackboard()
  await screen.findByRole('heading', { name: '0.6.0' })
  fireEvent.click(within(screen.getByRole('navigation', { name: 'Blackboard levels' })).getByRole('button', { name: 'Project' }))
  fireEvent.click(screen.getByText('Milestone history'))
  const goal = (await screen.findByRole('heading', { name: 'Delightful, intuitive UX' })).parentElement!
  const path = within(goal)
  await waitFor(() => expect(screen.queryByText('Loading card details…')).toBeNull())
  expect(path.getByRole('button', { name: /Shipped 1 release/ })).toBeVisible()
  expect(path.getByRole('button', { name: /0.6.0.*40% built/ })).toBeVisible()
  expect(path.getByRole('button', { name: /0.7.0 Planned/ })).toBeVisible()
  expect(path.getByText(/1 thrust · target Dec 1/)).toBeVisible()
  const shots = import.meta.env.VITE_BLACKBOARD_SHOTS_DIR
  if (shots) await page.screenshot({ path: shots + '/round2-project-path-colorblind.png' })
  fireEvent.click(path.getByRole('button', { name: /Shipped 1 release/ }))
  expect(screen.getByRole('heading', { name: '0.5.x' })).toBeVisible()
  expect(screen.getByRole('button', { name: /0.5.0 No card history/ })).toBeVisible()
 } finally { milestones.splice(0, milestones.length, ...saved.milestones); record.thrusts = saved.thrusts }
})

for (const width of [390, 1024, 3840]) it('updates an open draft live at ' + width + 'px', async () => {
 noBlackboard = true
 await page.viewport(width === 1024 ? 3840 : width, width === 390 ? 844 : 1200)
 mount()
 if (width === 1024) {
  const root = document.querySelector<HTMLElement>('[data-session-root]')!
  root.style.right = 'auto'
  root.style.width = '1024px'
  expect(root.getBoundingClientRect().width).toBe(1024)
 }
 await openBlackboard()
 await screen.findByRole('heading', { name: 'Give your next idea a clear path' })
 await act(async () => {
  await apiFetch('/api/sessions/sess-a/plan/draft', { method: 'POST' })
 })
 expect(await screen.findByRole('heading', { name: samplePlan.name })).toBeVisible()
 const shots = import.meta.env.VITE_BLACKBOARD_SHOTS_DIR
 if (shots) {
  const frame = document.createElement('div')
  frame.style.cssText = 'position:fixed;inset:0;width:100vw;height:100vh;pointer-events:none'
  document.body.append(frame)
  try {
   if (width === 1024) frame.style.width = '1024px'
   await page.screenshot({ element: frame, path: shots + '/blackboard-live-' + width + '.png' })
   if (width === 390) {
    setTheme('colorblind-dark')
    await page.screenshot({ element: frame, path: shots + '/blackboard-live-colorblind-390.png' })
   }
  } finally { frame.remove() }
 }
 await act(async () => {
  draftPlan = null
  emit({ type: 'blackboard_draft_changed', session_id: 'sess-a' })
 })
 expect(await screen.findByRole('heading', { name: 'Give your next idea a clear path' })).toBeVisible()
})

it('opens a blackboard created on another device without reopening the empty tab', async () => {
 noBlackboard = true
 await page.viewport(1440, 900)
 mount()
 await openBlackboard()
 await screen.findByRole('heading', { name: 'Give your next idea a clear path' })
 await act(async () => {
  noBlackboard = false
  emit({ type: 'blackboard_draft_changed', session_id: 'sess-a' })
 })
 expect(await screen.findByRole('heading', { name: '0.6.0' })).toBeVisible()
})

it('refreshes a replaced draft and catches up after a browser reconnect', async () => {
 noBlackboard = true
 draftPlan = samplePlan
 await page.viewport(1440, 900)
 mount()
 await openBlackboard()
 await screen.findByRole('heading', { name: samplePlan.name })
 await act(async () => {
  draftPlan = { ...samplePlan, name: 'Revised plan' }
  emit({ type: 'blackboard_draft_changed', session_id: 'sess-a' })
 })
 expect(await screen.findByRole('heading', { name: 'Revised plan' })).toBeVisible()
 await act(async () => { draftPlan = null; flushOpen() })
 expect(await screen.findByRole('heading', { name: 'Give your next idea a clear path' })).toBeVisible()
})

it('keeps a visible draft and offers read retry when refresh fails',async()=>{
 noBlackboard=true
 draftPlan=samplePlan
 const original=apiFetch.getMockImplementation()!
 const log=vi.spyOn(console,'error').mockImplementation(()=>{})
 let fail=false
 apiFetch.mockImplementation(async(url:string,init?:RequestInit)=> fail && url.startsWith('/api/blackboards/draft?') ? new Response('{}',{status:503}) : original(url,init))
 try {
 mount();await openBlackboard();await screen.findByRole('heading',{name:samplePlan.name})
 fail=true;act(()=>emit({type:'blackboard_draft_changed',session_id:'sess-a'}))
 await screen.findByRole('alert')
 expect(screen.getByRole('heading',{name:samplePlan.name})).toBeVisible()
 fail=false;draftPlan={...samplePlan,name:'Revised draft'}
 fireEvent.click(screen.getByRole('button',{name:'Retry blackboard'}))
 await screen.findByRole('heading',{name:'Revised draft'})
 } finally {log.mockRestore()}
})

for (const width of [390, 1024, 3840]) it('reads board link decisions in the real app at ' + width + 'px', async () => {
 await page.viewport(width, width === 3840 ? 2160 : 900)
 const original = proposals[0].changes
 try {
  for (const operation of ['link', 'unlink']) {
   proposals[0].changes = [{id:'board-link',operation,item:{board_id:'b-rel',card_number:0,reason:'Keep the release work visible'}}]
   // Each operation is a new server fixture; no socket revision is emitted by this loop.
   resetBlackboardReadsForTest()
   const mounted = mount()
   await openBlackboard()
   fireEvent.click(await screen.findByRole('button', {name:'Review'}))
   const review = await screen.findByRole('dialog', {name:'Plan changes for 0.6.0'})
   expect(within(review).getByText(operation === 'link' ? 'Link board · b-rel' : 'Unlink board · b-rel', {exact:false})).toBeVisible()
   expect(within(review).queryByText(/Card #0/)).toBeNull()
   expect(review.getBoundingClientRect().right).toBeLessThanOrEqual(width + 1)
   const shots = import.meta.env.VITE_BLACKBOARD_SHOTS_DIR
   if (shots) await page.screenshot({path:shots + '/board-' + operation + '-' + width + '.png'})
   fireEvent.click(within(review).getByRole('button',{name:'Close plan review'}))
   if (width === 390 && operation === 'link') {
    setTheme('colorblind-dark')
    fireEvent.click(screen.getByRole('button',{name:'Review'}))
    await screen.findByRole('dialog',{name:'Plan changes for 0.6.0'})
    if (shots) await page.screenshot({path:shots + '/board-link-colorblind-390.png'})
    fireEvent.click(screen.getByRole('button',{name:'Close plan review'}))
    setTheme('dark')
   }
   mounted.unmount()
  }
 } finally {proposals[0].changes = original}
})


it('hides a chart until two real observation days exist', () => {
 const { container, rerender } = render(<BlackboardChart snapshots={[]} events={record.events} target={null} frozen={false} />)
 expect(container.querySelector('figure')).toBeNull()
 rerender(<BlackboardChart snapshots={record.snapshots.slice(0,1)} events={record.events} target={null} frozen={false} />)
 expect(container.querySelector('figure')).toBeNull()
 rerender(<BlackboardChart snapshots={record.snapshots.slice(0,2)} events={record.events} target={null} frozen={false} />)
 expect(container.querySelector('figure')).toBeTruthy()
})

it('collapses identical daily plan events into one counted marker', () => {
 const event = {...record.events[0], kind:'plan_set'}
 render(<BlackboardChart snapshots={record.snapshots} events={[event,{...event,id:'repeat'}]} target={null} frozen={false} />)
 expect(document.querySelector('.blackboard-chart-event')?.textContent).toBe('Plan set ×2')
})


for (const width of [390,1920,3840]) it('compacts 5 goals, 5 thrusts and 237 cards at '+width+'px', async () => {
 const original = {goals:record.goals,thrusts:record.thrusts,snapshots:record.snapshots,events:record.events,cards:[...cards]}
 try {
  record.goals = ['Keep work safe across restarts and interruptions','Make review feel natural beside the conversation','Keep the next useful action clear','Give teams a dependable workspace','Ship confidently with an honest record'].map((name,i)=>({id:'goal-'+i,name}))
  record.thrusts = ['Recover work','Review in context','Questions that help','Workspaces from Main','Release confidence'].map((name,i)=>({id:'push-'+i,name,goal_id:'goal-'+i,milestone_id:'release'}))
  cards.splice(0,cards.length,...Array.from({length:237},(_,i)=>({...original.cards[i%original.cards.length],card:{...original.cards[i%original.cards.length].card,id:'fixture-'+i,number:i+1,reference:'DEMO-'+(i+1)},membership:{thrust_id:'push-'+i%5,state:'planned'},stage:i<150?'done':i<221?'to_test':i<229?'in_work':i<235?'next':'needs_you'})))
  record.snapshots = []
  await page.viewport(width,width===390?1100:1000)
  mount(); await openBlackboard()
  await screen.findByRole('button',{name:/Recover work/})
  await waitFor(() => expect(screen.queryByText('Loading card details…')).toBeNull())
  expect(document.querySelector('.blackboard-chart')).toBeNull()
  const filters=screen.getByRole('group',{name:'Card filters'})
  expect(within(filters).getByRole('button',{name:'To test 71'})).toBeTruthy()
  expect(within(filters).getByRole('button',{name:'Needs you 2'})).toBeTruthy()
  expect(document.querySelectorAll('.blackboard-thrust')).toHaveLength(5)
  expect(document.querySelector('.blackboard-goal-caption')?.textContent).toBe(record.goals[0].name)
  expect(document.querySelector('.blackboard-tile-counts')?.textContent).toBe('3 open') // the header already says '45 of 48 shipped'
  const board=document.querySelector<HTMLElement>('.blackboard')!
  expect(board.scrollWidth).toBeLessThanOrEqual(board.clientWidth+1)
  const tiles=[...document.querySelectorAll<HTMLElement>('.blackboard-thrust')]
  const columns=tiles.filter(tile=>Math.abs(tile.getBoundingClientRect().top-tiles[0].getBoundingClientRect().top)<2).length
  expect(columns).toBe(width===390?1:width===1920?2:5)
  const shots=import.meta.env.VITE_BLACKBOARD_SHOTS_DIR
  if(shots) await page.screenshot({path:shots+'/blackboard-539-'+width+'.png'})
  setTheme('colorblind-dark')
  if(shots) await page.screenshot({path:shots+'/blackboard-539-colorblind-'+width+'.png'})
  record.snapshots = [0,1].map(d=>({milestone_id:'release',day:`2026-11-0${3+d}`,complete:true,counts:{done:150,to_test:71,in_work:8,next:6,needs_you:2}}))
  record.events = Array.from({length:20},(_,i)=>({id:'plan-'+i,milestone_id:'release',kind:'plan_set',source:'agent',recorded_at:'2026-11-03T00:00:00Z',payload:{}}))
  fireEvent.click(screen.getByRole('button',{name:'Refresh blackboard'}))
  await screen.findByRole('img',{name:/Plan set ×20/})
  expect(document.querySelector('.blackboard-chart svg')!.getBoundingClientRect().height).toBe(120)
  if(shots) await page.screenshot({path:shots+'/blackboard-539-chart-colorblind-'+width+'.png'})

 } finally {
  record.goals=original.goals; record.thrusts=original.thrusts; record.snapshots=original.snapshots; record.events=original.events
  cards.splice(0,cards.length,...original.cards);setTheme('dark')
 }
})
