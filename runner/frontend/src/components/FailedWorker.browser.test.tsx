import { afterEach, expect, it, vi } from 'vitest'
import { act, render, screen, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { useTranscriptStore } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import SessionList from './SessionList'
import CommandPaletteProvider from './CommandPalette'
import { useSessionStore } from '../hooks/useSessionStore'
import { useSessionListFilters } from '../lib/sessionSelection'
import { makeDaemon, makeSession } from '../test/fixtures'
import { invalidateChatTransport } from '../lib/chatTransport'
import { resetSessionBoardLinksForTest } from '../lib/sessionBoardLinks'
import { setChildrenExpanded } from '../lib/sidebarPrefs'
import { resetBoardOriginForTest } from '../lib/boardLink'
import * as wsMock from '../test/wsMock'
import '../index.css'
import '@blerglab/chat/blerg.css'
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({sub:'person-a',exp:9999999999}))}.x`, ensureFreshToken: async () => true }))
afterEach(() => { invalidateChatTransport(); resetSessionBoardLinksForTest(); resetBoardOriginForTest(); vi.unstubAllGlobals(); localStorage.clear() })

for (const theme of ['dark','colorblind-dark']) for (const width of [390, 3840]) it(`keeps a failed worker and its actions separate from Main at ${width}px in ${theme}`, async () => {
 await page.viewport(width, width === 390 ? 844 : 2160)
 document.documentElement.setAttribute('data-theme',theme)
 wsMock.resetWsMock()
 const main = makeSession({id:'main',title:'Release coordination',kind:'agent',status:'idle'})
 const worker = makeSession({id:'worker',title:'Review widget',kind:'agent',runtime:'cluster',status:'error',parent_session_id:'main',started_by:{kind:'session'},repo:'example-org/widget',ended_at:new Date().toISOString(),end_reason:'start_failed'})
 const reason = 'Repository “example-org/widget”: the GitHub read-only worker token for “example-org” cannot see this repository. Check the repository name and its token’s repository access in Settings → Integrations → Read-only tokens for workers. Your workspace and Main are still available.'
 useSessionStore.setState({sessions:[main,{...worker,error_reason:reason},{...worker,id:'old-worker',title:'Old daemon failure',runtime:'daemon',ended_at:new Date(Date.now()-25*3600000).toISOString(),end_reason:'process_failed'}],daemons:[makeDaemon()]})
 useSessionListFilters.setState({query:'',focus:null})
 useTranscriptStore.setState({sessions:{}})
 const store = useTranscriptStore.getState()
 store.ingest({type:'agent_event',session_id:'worker',client_event_id:'plan',seq:1,ts:'2026-11-14T12:00:00Z',kind:'start_stage',payload:{plan:true,runtime:'cluster',stages:[{id:'credentials',label:'Checking repository access',state:'active'},{id:'plugins',label:'Installing plugins',state:'warning',detail:'skipped: started by another session'}]}})
 store.ingest({type:'agent_event',session_id:'worker',client_event_id:'failure',seq:2,ts:'2026-11-14T12:00:01Z',kind:'start_stage',payload:{stages:[{id:'credentials',state:'failed',detail:reason}]}})
 store.ingestReplayDone('worker',{lastSeq:2,hasMore:false})
 const mutations: string[] = []
 vi.stubGlobal('fetch',vi.fn(async (input:RequestInfo|URL,init?:RequestInit) => {
  const path = new URL(String(input),location.origin).pathname
  if (init?.method === 'POST' || init?.method === 'DELETE') mutations.push(`${init.method} ${path}`)
  if (path.endsWith('/live')) return Response.json({session_id:path.split('/')[3],through_seq:2,generated_at:new Date().toISOString(),tokens_hidden:true,asks:[],subagents:[],tasks:[],tool_changes:0,counts:{needs_you:0,running:0},can_release_prompt:true,children:[]})
  if (path === '/api/sessions/worker') return Response.json({...worker,error_reason:reason})
  return Response.json({})
 }))
 const view = render(<MemoryRouter initialEntries={['/sessions/worker']}><CommandPaletteProvider><div style={{width:'100vw',height:'100vh',display:'flex'}}><Routes><Route path="/sessions/:id" element={<SessionDetail/>}/></Routes></div></CommandPaletteProvider></MemoryRouter>)
 const panel = await screen.findByTestId('start-progress')
 expect(panel).toHaveTextContent("Couldn't start this worker: Review widget")
 expect(within(panel).queryByRole('button',{name:'End workspace'})).toBeNull()
 expect(within(panel).queryByRole('button',{name:'Retry'})).toBeNull()
 expect(within(panel).queryByText('Installing plugins')).toBeNull()
 expect(panel.scrollWidth).toBeLessThanOrEqual(panel.clientWidth)
 const discard = await within(panel).findByRole('button',{name:'Discard worker'})
 const shots = import.meta.env.VITE_CHROME_SHOTS_DIR
 if (shots) await page.screenshot({path:`${shots}/failed-worker-card-${theme}-${width}.png`})
 await userEvent.click(discard)
 await userEvent.click(within(screen.getByRole('alertdialog')).getByRole('button',{name:'Discard worker'}))
 expect(mutations).toEqual(['POST /api/sessions/main/children/worker/discard'])
 expect(wsMock.send.mock.calls.some(([message]) => message.type === 'agent_user_message' && message.session_id === 'main')).toBe(false)
 view.unmount()
 render(<MemoryRouter><div style={{width:width === 390 ? '100vw':420,height:'100vh'}}><SessionList variant="sidebar"/></div></MemoryRouter>)
 const root = document.querySelector<HTMLElement>('[data-session-branch="main"]')!
 const own = root.firstElementChild as HTMLElement
 expect(within(own).getByTestId('status-dot')).toHaveAccessibleName('idle')
 expect(within(own).getByTestId('card-attention-count')).toHaveTextContent('1 failed')
 expect(screen.getByText('Review widget')).toBeVisible()
 expect(screen.getAllByTestId('status-dot').filter(dot=>dot.getAttribute('title')==='error')).toHaveLength(1)
 if (shots) await page.screenshot({path:`${shots}/failed-worker-sidebar-${theme}-${width}.png`})
 act(()=>setChildrenExpanded('main',false))
 expect(screen.queryByText('Review widget')).toBeNull()
 expect(within(own).getByTestId('card-attention-count')).toHaveTextContent('2 failed')
 if (shots) await page.screenshot({path:`${shots}/failed-worker-sidebar-collapsed-${theme}-${width}.png`})
 act(()=>setChildrenExpanded('main',true))
 act(()=>useSessionStore.getState().updateSessionStatus('worker','idle'))
 expect(screen.queryByText('Review widget')).toBeNull()
})

for (const theme of ['dark','colorblind-dark']) for (const width of [390,3840]) it(`ends the failed channel alone at ${width}px in ${theme}`, async()=>{
 await page.viewport(width,width===390?844:2160)
 document.documentElement.setAttribute('data-theme',theme)
 wsMock.resetWsMock()
 const main=makeSession({id:'main',title:'Release coordination',kind:'agent',status:'idle'})
 const channel=makeSession({id:'channel',title:'Release notes',kind:'agent',runtime:'cluster',status:'error',parent_session_id:'main',started_by:null,ended_at:new Date().toISOString(),end_reason:'start_failed'})
 useSessionStore.setState({sessions:[main,channel],daemons:[makeDaemon()]})
 useTranscriptStore.setState({sessions:{}})
 const store=useTranscriptStore.getState()
 store.ingest({type:'agent_event',session_id:'channel',client_event_id:'plan',seq:1,ts:new Date().toISOString(),kind:'start_stage',payload:{plan:true,runtime:'cluster',stages:[{id:'engine',label:'Starting agent',state:'active'}]}})
 store.ingest({type:'agent_event',session_id:'channel',client_event_id:'failure',seq:2,ts:new Date().toISOString(),kind:'start_stage',payload:{stages:[{id:'engine',state:'failed',detail:'The agent could not start.'}]}})
 store.ingestReplayDone('channel',{lastSeq:2,hasMore:false})
 const mutations:string[]=[]
 vi.stubGlobal('fetch',vi.fn(async(input:RequestInfo|URL,init?:RequestInit)=>{
  const path=new URL(String(input),location.origin).pathname
  if(init?.method==='DELETE'||init?.method==='POST') mutations.push(`${init.method} ${path}`)
  if(path.endsWith('/live'))return Response.json({session_id:path.split('/')[3],through_seq:2,generated_at:new Date().toISOString(),tokens_hidden:true,asks:[],subagents:[],tasks:[],tool_changes:0,counts:{needs_you:0,running:0},can_release_prompt:true,children:[]})
  if(path==='/api/sessions/channel')return Response.json(channel)
  return Response.json({})
 }))
 render(<MemoryRouter initialEntries={['/sessions/channel']}><CommandPaletteProvider><div style={{width:'100vw',height:'100vh',display:'flex'}}><Routes><Route path="/sessions/:id" element={<SessionDetail/>}/></Routes></div></CommandPaletteProvider></MemoryRouter>)
 const panel=await screen.findByTestId('start-progress')
 expect(panel).toHaveTextContent("Couldn't start this channel: Release notes")
 expect(within(panel).queryByRole('button',{name:/workspace/i})).toBeNull()
 const end=within(panel).getByRole('button',{name:'End channel'})
 expect(end).toHaveClass('sp-stop')
 expect(panel.scrollWidth).toBeLessThanOrEqual(panel.clientWidth)
 const shots=import.meta.env.VITE_CHROME_SHOTS_DIR
 if(shots)await page.screenshot({path:`${shots}/failed-channel-${theme}-${width}.png`})
 await userEvent.click(end)
 await userEvent.click(within(screen.getByRole('alertdialog',{name:'End this channel?'})).getByRole('button',{name:'End channel'}))
 expect(mutations).toEqual(['DELETE /api/sessions/channel'])
 expect(useSessionStore.getState().sessions.find(s=>s.id==='main')?.status).toBe('idle')
})
