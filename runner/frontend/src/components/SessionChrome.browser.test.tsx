import { useEffect } from 'react'
import { beforeEach, afterEach, it, expect, vi } from 'vitest'
import { render, screen, waitFor, within, act } from '@testing-library/react'
import { page, userEvent, cdp } from 'vitest/browser'
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router-dom'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import { useSessionStore } from '../hooks/useSessionStore'
import { useToastStore } from '../hooks/useToastStore'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'
import { sessionTabsKey } from '../hooks/useSessionTabs'
import { explorerPrefKey } from '../hooks/useExplorerLayout'
import { makeSession, makeDaemon } from '../test/fixtures'
import { reelReply } from '../test/reelFixture'
import SessionDetail from './SessionDetail'
import ToastContainer from './ToastContainer'
import CommandPaletteProvider from './CommandPalette'
import { invalidateChatTransport } from '../lib/chatTransport'
import '../index.css'
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
const shellMode=vi.hoisted(()=>({restore:false, ids:['focus'], available:true}))
vi.mock('../shellTransport',async original=>{
 const actual=await original<typeof import('../shellTransport')>()
 const {FakeShell}=await import('../test/shellMock')
 return {...actual,restoreShells:async()=>shellMode.restore?shellMode.ids.map(id=>new FakeShell(id)):[]}
})
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('../lib/engineModels', async original => ({ ...await original<typeof import('../lib/engineModels')>(), useEngineModels: () => ({ models: [{ id: 'model-next', name: 'Next', efforts: ['low', 'high'], default_effort: 'low' }], loading: false, fallback: false, source: 'live', engineEfforts: ['low', 'high'] }) }))
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const wide = 'long-command-or-title-'.repeat(400)
const shotDir = import.meta.env.VITE_CHROME_SHOTS_DIR
beforeEach(() => {
 shellMode.restore=false; shellMode.ids=['focus']; shellMode.available=true
 localStorage.clear();document.documentElement.setAttribute('data-theme', 'dark')
 useTranscriptStore.setState({ sessions: {} });useToastStore.setState({ toasts: [] })
 useSessionStore.setState({ daemons: [makeDaemon()], sessions: ['sess-a', 'sess-b'].map(id => makeSession({ id, kind: 'agent', status: 'idle', title: id === 'sess-a' ? 'Build session chrome' : 'Check the release', runtime: 'daemon', model: 'model-next', effort: 'high' })) })
 for (const id of ['sess-a','sess-b']) {
  let seq=0;const state=useTranscriptStore.getState()
  const emit=(kind:string,payload:unknown)=>state.ingest({session_id:id,client_event_id:`chrome-${id}-${++seq}`,seq,ts:'2026-10-25T12:00:00Z',kind,payload} as AgentEvent)
  for(let i=0;i<40;i++)emit('user_message',{text:`Request ${i}`,source:'chat'})
  emit('tool_call',{tool:'Bash',call_id:'long-command',input:{command:wide}});emit('tool_result',{call_id:'long-command',output:'Done',is_error:false})
  emit('checklist',{items:[{id:'long-item',text:wide,done:false}]});emit('assistant_text',{text:'The session controls are in the panel. The chat keeps the available space.',done:true})
  state.ingestReplayDone(id,{lastSeq:seq,hasMore:false})
 }
 const reply=reelReply();reply.topics=[]
 reply.strokes=Array.from({length:2000},(_,i)=>({id:`stroke-${i}`,kind:'finding',source:'mark',text:i===0?wide:`Finding ${i}`,jump:null,seq:3000-i,at:new Date(Date.now()-i*60000).toISOString()}))
 apiFetch.mockReset().mockImplementation(async (url:string,opts?:RequestInit) => url.endsWith('/permissions') ? json({can_grant:true,reason:'',grants:[],boards:[],mcp_tools:[]}) : url.endsWith('/topics') ? json(reply) : url.endsWith('/shells') ? opts?.method==='POST'?json({code:'runtime_start_refused',cause:'helper_missing'},403):json([]) : url.includes('/workspace?') ? json({result:{ok:true,launch_generation:'launch',generation:1,entries:[{name:'README.md',path:'README.md',directory:false}],truncated:false},members:[],runtime:'host'}) : url.endsWith('/shell-capability')?json({available:shellMode.available}):json({}))
 vi.stubGlobal('fetch',vi.fn(async(url:string)=>String(url).includes('/workspace-file')?json({path:'README.md',view:'text',content_type:'text/plain',content_b64:btoa('Read the session notes.')}):json(String(url).endsWith('/artifacts')?{artifacts:[]}:[])))
})
afterEach(()=>{invalidateChatTransport();vi.unstubAllGlobals();document.documentElement.removeAttribute('data-theme')})
let navigateSession: ReturnType<typeof useNavigate>
function Switches(){const navigate=useNavigate();useEffect(()=>{navigateSession=navigate},[navigate]);return null}
const switchTo = (id: string) => act(() => navigateSession(`/sessions/${id}`))
function mount(){return render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider><div style={{height:'100vh',display:'flex',flexDirection:'column',overflow:'hidden'}}><Switches/><div style={{flex:1,minHeight:0,minWidth:0}}><Routes><Route path="/sessions/:id" element={<SessionDetail/>}/></Routes></div></div><ToastContainer/></CommandPaletteProvider></MemoryRouter>)}
const composer=()=>document.querySelector<HTMLTextAreaElement>('.agent-composer textarea')!
const panelToggle=()=>screen.getByTestId('rail-toggle')
async function wheel(element:HTMLElement) {
 const r=element.getBoundingClientRect(),f=window.frameElement?.getBoundingClientRect(),scale=f?f.width/innerWidth:1
 const x=(f?.left??0)+(r.left+Math.min(100,r.width/2))*scale,y=(f?.top??0)+(r.top+Math.min(100,r.height/2))*scale
 await cdp().send('Input.dispatchMouseEvent',{type:'mouseMoved',x,y})
 await cdp().send('Input.dispatchMouseEvent',{type:'mouseWheel',x,y,deltaY:600,deltaX:0})
}
it.each([1024,2860,3840])('contains long chat and 2,000 History strokes at %i px',async width=>{
 await page.viewport(width,1000);localStorage.setItem(RAIL_PREF_KEY,'1');localStorage.setItem(RAIL_TAB_KEY,'history');mount();await screen.findAllByTestId('stroke')
 const columns=screen.getByTestId('session-columns')
 console.log('chrome-width-walk',width,[...columns.querySelectorAll<HTMLElement>('*')].filter(e=>e.clientWidth>0&&e.scrollWidth>e.clientWidth+1).map(e=>[e.className,e.clientWidth,e.scrollWidth]).slice(0,20))
 expect(columns.scrollWidth).toBe(columns.clientWidth)
 expect(getComputedStyle(columns).overflowY).toBe('hidden')
 const header=screen.getByTestId('session-header-controls').parentElement!,top=header.getBoundingClientRect().top
 const body=screen.getByTestId('session-rail').querySelector<HTMLElement>('.rail-body')!
 console.log('chrome-height',width,columns.clientHeight,columns.scrollHeight,body.clientHeight,body.scrollHeight)
 await wheel(body);await waitFor(()=>expect(body.scrollTop).toBeGreaterThan(0))
 await wheel(document.querySelector<HTMLElement>('.agent-timeline')!)
 await waitFor(()=>expect(columns.scrollTop).toBe(0))
 expect(header.getBoundingClientRect().top).toBe(top)
 expect(composer().getBoundingClientRect().bottom).toBeLessThanOrEqual(innerHeight)
 expect(composer().getBoundingClientRect().top).toBeGreaterThan(header.getBoundingClientRect().bottom)
 expect(body.getBoundingClientRect().bottom).toBeLessThanOrEqual(innerHeight)
},30000)
it.each([1024,3840])('gives all the width back when the panel is collapsed at %i',async width=>{
 await page.viewport(width,1000);localStorage.setItem(RAIL_PREF_KEY,'0');mount();await waitFor(()=>expect(composer()).toBeTruthy())
 const columns=screen.getByTestId('session-columns'),chat=screen.getByTestId('session-chat-column')
 expect(screen.getAllByTestId('rail-toggle')).toHaveLength(1);expect(panelToggle().closest('nav')).toHaveAttribute('aria-label','Workspace')
 expect(chat.getBoundingClientRect().right).toBe(columns.getBoundingClientRect().right);expect(columns.querySelector('.pane-rail-right')).toBeNull()
 await userEvent.click(panelToggle());expect(screen.getByTestId('session-rail')).toBeVisible()
})
it('restores focus per session and never lets the mounted explorer steal it',async()=>{
 await page.viewport(1600,1000);localStorage.setItem(explorerPrefKey('person-a'),JSON.stringify({open:true,width:280}));mount();await waitFor(()=>expect(composer()).toHaveFocus())
 const search=await screen.findByRole('combobox',{name:'Search workspace files'});expect(search).not.toHaveFocus();await userEvent.click(search)
 switchTo('sess-b');await waitFor(()=>expect(composer()).toHaveFocus())
 switchTo('sess-a');await waitFor(()=>expect(screen.getByRole('combobox',{name:'Search workspace files'})).toHaveFocus())
 await userEvent.click(composer());switchTo('sess-b');switchTo('sess-a');await waitFor(()=>expect(composer()).toHaveFocus())
},30000)
it('does not raise the phone keyboard when switching sessions',async()=>{
 await page.viewport(390,844);mount();await waitFor(()=>expect(composer()).toBeTruthy());expect(composer()).not.toHaveFocus()
 await userEvent.click(composer());switchTo('sess-b');await waitFor(()=>expect(composer()).not.toHaveFocus())
})
it.each([390,3840])('shows header, Session settings, shell popover and toast at %i',async width=>{
 useTranscriptStore.setState({sessions:{}})
 const state=useTranscriptStore.getState();state.ingest({session_id:'sess-a',client_event_id:'review-chat',seq:1,kind:'assistant_text',ts:'2026-10-25T12:00:00Z',payload:{text:'The workspace is ready. Model and effort are available in the Session panel.',done:true}} as AgentEvent);state.ingestReplayDone('sess-a',{lastSeq:1,hasMore:false})
 await page.viewport(width,width===390?844:2160);localStorage.setItem(RAIL_PREF_KEY,'0');mount();await waitFor(()=>expect(composer()).toBeTruthy())
 expect(screen.getByRole('navigation',{name:width===390?'Session navigation':'Workspace'}).querySelector('button')).toHaveAccessibleName('Toggle explorer')
 expect(screen.queryByTestId('posture-banner')).toBeNull();expect(screen.getByRole('img',{name:/Runs unsandboxed/})).toBeVisible();expect(screen.queryByRole('button',{name:/0 open files/})).toBeNull()
 if(shotDir)await page.screenshot({path:`${shotDir}/session-chrome-header-${width}.png`})
 await userEvent.click(panelToggle());const panel=width===390?screen.getByRole('dialog',{name:'Session panel'}):screen.getByTestId('session-rail')
 await userEvent.click(within(panel).getByRole('tab',{name:/Live/}));const section=within(panel).getByTestId('rail-session');section.scrollIntoView({block:'end'})
 expect(within(section).getByRole('combobox',{name:'Model'})).toBeVisible();expect(within(section).getByRole('combobox',{name:'Effort'})).toBeVisible();expect(within(section).queryByRole('button',{name:'Rules'})).toBeNull()
 if(shotDir){await waitFor(()=>expect(panel.getAnimations().length).toBe(0));await page.screenshot({path:`${shotDir}/session-chrome-panel-${width}.png`})}
 if(width===390)await userEvent.keyboard('{Escape}');else await userEvent.click(panelToggle())
 await userEvent.click(await screen.findByRole('button',{name:'New shell'}));const error=await screen.findByRole('alertdialog',{name:"Shell didn't start"});expect(error).toHaveTextContent('executable or its working directory is missing')
 if(shotDir){await waitFor(()=>expect(getComputedStyle(error).opacity).toBe('1'));await page.screenshot({path:`${shotDir}/session-chrome-popover-${width}.png`})}
 await userEvent.click(within(error).getByRole('button',{name:'Dismiss'}));act(()=>useToastStore.getState().addToast({title:'Needs your attention',body:'The agent has a question.',ttlMs:60000}))
 const toast=await screen.findByTestId('toast-session');await waitFor(()=>expect(toast.getBoundingClientRect().bottom).toBeLessThan(composer().getBoundingClientRect().top))
 if(shotDir)await page.screenshot({path:`${shotDir}/session-chrome-toast-${width}.png`})
},30000)

it.each([390,3840])('shows open files with a count and restores file focus at %i',async width=>{
 await page.viewport(width,width===390?844:2160)
 const tabs=['README.md','notes.md'].map(path=>({id:`workspace:${path}`,view:{kind:'workspace',path},preview:false}))
 for(const session of ['sess-a','sess-b'])localStorage.setItem(sessionTabsKey('person-a',session),JSON.stringify({tabs,active:session==='sess-a'?'workspace:README.md':'chat'}))
 localStorage.setItem(RAIL_PREF_KEY,'0');mount()
 const file=width===390?screen.getByRole('button',{name:'2 open files'}):screen.getByRole('tab',{name:/README.md/})
 expect(file).toBeVisible()
 if(width===390){
  expect(file).toHaveAttribute('title','2 open files');expect(file.querySelector('svg')).not.toBeNull()
  if(shotDir)await page.screenshot({path:`${shotDir}/session-chrome-files-${width}.png`})
  await userEvent.click(file);expect(screen.getByRole('menuitem',{name:'README.md'})).toBeVisible();await userEvent.keyboard('{Escape}')
 }else{
  await userEvent.click(file);expect(file).toHaveFocus();switchTo('sess-b');await waitFor(()=>expect(composer()).toHaveFocus());switchTo('sess-a');await waitFor(()=>expect(screen.getByRole('tab',{name:/README.md/})).toHaveFocus())
  if(shotDir)await page.screenshot({path:`${shotDir}/session-chrome-files-${width}.png`})
 }
})

it('does not restore terminal input focus when returning to its session',async()=>{
 await page.viewport(1600,1000);shellMode.restore=true;localStorage.setItem(RAIL_PREF_KEY,'0');mount()
 const shell=await waitFor(()=>{const input=document.querySelector<HTMLTextAreaElement>('.shell-terminal .xterm-helper-textarea');expect(input).not.toBeNull();return input!})
 await userEvent.click(screen.getByRole('tab', { name: 'Shell' })); act(()=>shell.focus());expect(shell).toHaveFocus();switchTo('sess-b');await waitFor(()=>expect(composer()).toHaveFocus());switchTo('sess-a')
 await waitFor(()=>expect(document.querySelector('.shell-terminal .xterm-helper-textarea')).not.toBeNull())
 expect(document.activeElement?.closest('.shell-terminal')).toBeNull()
})

it('returns keyboard focus to the panel toggle after Escape', async () => {
 await page.viewport(1600, 1000); localStorage.setItem(RAIL_PREF_KEY, '0'); mount()
 await userEvent.click(panelToggle())
 await userEvent.click(within(screen.getByTestId('session-rail')).getByRole('tab', { name: /Live/ }))
 await userEvent.keyboard('{Escape}')
 expect(panelToggle()).toHaveAttribute('aria-expanded', 'false')
 expect(panelToggle()).toHaveFocus()
})
it.each(['present', 'ended', 'all-ended', 'unavailable'])('retains shell selection without restoring terminal input focus (%s)', async state => {
 await page.viewport(1600, 1000); shellMode.restore=true; shellMode.ids=['focus', 'second']
 localStorage.setItem('shell-dock:person-a', 'right'); localStorage.setItem(RAIL_PREF_KEY, '0'); mount()
 const shellInput = (id: string) => document.querySelector<HTMLTextAreaElement>(`[data-shell-id="${id}"] .xterm-helper-textarea`)
 await waitFor(() => expect(shellInput('second')).not.toBeNull())
 await userEvent.click(screen.getByRole('tab', { name: 'Shell 2' }))
 await waitFor(() => expect(shellInput('second')).toHaveFocus())
 switchTo('sess-b'); await waitFor(() => expect(composer()).toHaveFocus())
 shellMode.ids = state === 'all-ended' ? [] : state === 'ended' ? ['focus'] : ['focus', 'second']
 shellMode.available = state !== 'unavailable'
 switchTo('sess-a')
 await waitFor(() => expect(document.querySelector('[data-shells-restored]')).toHaveAttribute('data-shells-restored', 'true'))
 if (state === 'present' || state === 'ended') await waitFor(() => expect(shellInput(state === 'present' ? 'second' : 'focus')).not.toBeNull())
 expect(document.activeElement?.closest('.shell-terminal')).toBeNull()
 if (state === 'present') {
  expect(screen.getByRole('tab', { name: 'Shell 2' })).toHaveAttribute('aria-selected', 'true')
  switchTo('sess-b'); await waitFor(() => expect(composer()).toHaveFocus())
  shellMode.ids = ['second', 'focus']; switchTo('sess-a')
  await waitFor(() => expect(shellInput('second')).not.toBeNull())
  expect(shellInput('second')).not.toHaveFocus()
 }
})
