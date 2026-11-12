import { render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import type { QuestionThreadPage } from '@blerglab/chat'
import '../index.css'
import { seedRichRail } from '../test/railFixture'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'
import { useMessageStore } from '../hooks/useMessageStore'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeSession } from '../test/fixtures'
import { invalidateChatTransport } from '../lib/chatTransport'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async orig => ({ ...(await orig<typeof import('../authClient')>()), getAccessToken: () => 'tok', redirectToRefresh: vi.fn() }))
vi.mock('../claims', async orig => ({ ...(await orig<typeof import('../claims')>()), tokenExpired: () => false }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
import { setTheme } from '../../../../packages/settings/theme'
import SessionDetail from './SessionDetail'

const shots=import.meta.env.VITE_QUESTION_SHOT_DIR
let data:QuestionThreadPage
beforeEach(()=>{
 setTheme('dark')
 localStorage.removeItem(RAIL_PREF_KEY);localStorage.setItem(RAIL_TAB_KEY,'live')
 data={question:{id:'thread-question',session_id:'child-worker',body:'Deploy this release to staging?',status:'open',answerable:true,question_turn:'person'},entries:[],more:false}
 const {live}=seedRichRail()
 live.can_release_prompt=true
 live.child_asks=[{message_id:'thread-question',session_id:'child-worker',child_title:'Release worker',question:data.question.body,asked_at:new Date().toISOString(),owner_only:true,no_wait:true}]
 useSessionStore.setState(state=>({sessions:[...state.sessions,makeSession({id:'child-worker',title:'Release worker',parent_session_id:'sess-a'})]}))
 useMessageStore.setState({messages:[{...data.question,kind:'ask',status:'open',created_at:new Date().toISOString(),no_wait:true,owner_only:true}]})
 const json=(body:unknown)=>Promise.resolve(new Response(JSON.stringify(body),{status:200,headers:{'Content-Type':'application/json'}}))
 apiFetch.mockImplementation((url:string,init?:RequestInit)=>{
  if(url.includes('/thread?') || url.endsWith('/thread')) {
   if(init?.method==='POST'){
    data.entries=[{seq:1,author:'person',kind:'reply',body:JSON.parse(init.body as string).reply,created_at:new Date().toISOString()},{seq:2,author:'main',kind:'reply',body:'No. Staging has its own database. Production stays on the current release.',created_at:new Date().toISOString()}]
   }
   return json(data)
  }
  if(url==='/api/messages/thread-question/reply' && init?.method==='POST') {data.question.status='answered';data.question.answer=JSON.parse(init.body as string).answer;return json({id:'thread-question'})}
  return json(url.endsWith('/live')?live:url.startsWith('/api/tool-changes')?{tool_changes:[]}: {})
 })
 vi.stubGlobal('fetch',vi.fn(()=>json([])))
})
afterEach(()=>{document.documentElement.removeAttribute('data-theme');vi.unstubAllGlobals();useMessageStore.setState({messages:[]});localStorage.removeItem(RAIL_PREF_KEY);localStorage.removeItem(RAIL_TAB_KEY);invalidateChatTransport()})
for(const width of [390,1920,3840]) it(`discusses and resolves the real workspace question tab at ${width}px`,async()=>{
 await page.viewport(width,1000)
 render(<div style={{height:'100vh'}}><MemoryRouter initialEntries={['/sessions/sess-a']}><Routes><Route path="/sessions/:id" element={<SessionDetail/>}/></Routes></MemoryRouter></div>)
 if(width<900) await userEvent.click(screen.getByTestId('rail-toggle'))
 const rail=await screen.findByTestId('session-rail')
 await userEvent.click(await within(rail).findByRole('button',{name:/Deploy this release/}))
 const dialog=screen.getAllByRole('dialog').at(-1)!
 await userEvent.fill(await within(dialog).findByRole('textbox',{name:'Your answer'}),'Will this affect the production database?')
 await userEvent.click(within(dialog).getByRole('button',{name:'Ask Release worker'}))
 await within(dialog).findByText('No. Staging has its own database. Production stays on the current release.')
 expect(within(dialog).queryByText('Your turn')).toBeNull()
 expect(dialog.scrollWidth).toBeLessThanOrEqual(dialog.clientWidth)
 if(shots) await page.screenshot({path:`${shots}/question-app-thread-${width}.png`})
 await userEvent.click(within(dialog).getByRole('button',{name:'Open as tab'}))
 const panel=await screen.findByRole('region',{name:'Question: Deploy this release to staging?'})
 expect(screen.getAllByRole('tab',{name:/Question: Deploy/})).toHaveLength(1)
 expect(panel.querySelector('.card-author')).toHaveTextContent('Release worker')
 await userEvent.fill(await within(panel).findByRole('textbox'),'Use staging')
 await userEvent.click(within(panel).getByRole('button',{name:'Answer'}))
 await within(panel).findByText('Answered: Use staging')
 expect(within(panel).queryByRole('textbox')).toBeNull()
 if(shots) await page.screenshot({path:`${shots}/question-app-outcome-${width}.png`})
})
