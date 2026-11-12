import { useEffect } from 'react'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { page, userEvent } from 'vitest/browser'
import { AskReply, QuestionThreadsProvider, type QuestionThreadPage, type QuestionThreadsClient } from '@blerglab/chat'
import { useSessionTabs, tabLabel } from '../hooks/useSessionTabs'
import { useSessionStore } from '../hooks/useSessionStore'
import { useMessageStore } from '../hooks/useMessageStore'
import { makeSession } from '../test/fixtures'
import { questionThreads } from '../lib/questionThreads'
import { setTheme } from '../../../../packages/settings/theme'
import QuestionTabs from './QuestionTabs'
import QuestionRow from './QuestionRow'
import '../index.css'
import '@blerglab/chat/blerg.css'
import './SessionRail.css'
import './SessionTabBar.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async orig => ({ ...(await orig<typeof import('../authClient')>()), getAccessToken: () => 'tok', redirectToRefresh: vi.fn() }))
vi.mock('../claims', async orig => ({ ...(await orig<typeof import('../claims')>()), tokenExpired: () => false }))

const shots = import.meta.env.VITE_QUESTION_SHOT_DIR
function Harness({ client }: { client: QuestionThreadsClient }) {
 const tabs = useSessionTabs('thread-owner', 'main', false)
 useEffect(() => {
  const target = tabs.tabs.find(tab => tab.id === tabs.active)
  if (target) document.getElementById('question-target')!.appendChild(tabs.targetFor(target.id))
 }, [tabs])
 return <QuestionThreadsProvider client={client}>
  <aside className="session-rail" style={{width:'min(320px, 100%)',padding:16}}><h2>Needs you</h2><QuestionRow id="q1" question="Deploy this release to staging?" audience="you"><AskReply messageId="q1" answer={text => client.answer('q1',text)} /></QuestionRow></aside>
  <div className="session-tab-bar" role="tablist" aria-label="Workspace tabs"><button role="tab" aria-selected={tabs.active==='chat'} onClick={()=>tabs.select('chat')}>Chat</button>{tabs.tabs.map(tab=><button role="tab" aria-selected={tabs.active===tab.id} key={tab.id} onClick={()=>tabs.select(tab.id)}>{tabLabel(tab.view)}</button>)}</div>
  <div id="question-target" style={{height:600}} />
  <QuestionTabs tabs={tabs} sessionId="main" />
 </QuestionThreadsProvider>
}
afterEach(() => { localStorage.clear(); document.documentElement.removeAttribute('data-theme') })
for (const scenario of [{width:390,pane:390,theme:'dark' as const},{width:1920,pane:1920,theme:'dark' as const},{width:3840,pane:3840,theme:'dark' as const},{width:3840,pane:480,theme:'colorblind-dark' as const}]) {
 it(`keeps a question thread in one tab and retains its outcome at ${scenario.width}px, pane ${scenario.pane}`,async()=>{
  await page.viewport(scenario.width,1000)
  setTheme(scenario.theme)
  useSessionStore.setState({sessions:[makeSession({id:'main',title:'Main'})]})
  useMessageStore.setState({messages:[{id:'q1',session_id:'main',body:'Deploy this release to staging?',kind:'ask',status:'open',created_at:'2026-11-05T12:00:00Z',question_turn:'person',answerable:true}]})
  const data:QuestionThreadPage={question:{id:'q1',session_id:'main',body:'Deploy this release to staging?',status:'open',answerable:true,question_turn:'person'},entries:[],more:false}
  const client:QuestionThreadsClient={...questionThreads,
   load:vi.fn(async()=>structuredClone(data)),
   reply:vi.fn(async(_id,text)=>{data.entries.push({seq:1,author:'person',kind:'reply',body:text,created_at:'2026-11-05T12:01:00Z'});data.question.question_turn='main'}),
   answer:vi.fn(async(_id,text)=>{data.question.status='answered';data.question.answer=text;return true}),
   dismiss:vi.fn(async()=>true),
  }
  render(<div style={{width:scenario.pane,maxWidth:'100%'}}><Harness client={client}/></div>)
  expect(screen.queryByRole('tab',{name:/Question:/})).toBeNull()
  await userEvent.click(screen.getByRole('button',{name:/Deploy this release/}))
  let dialog=screen.getByRole('dialog')
  await userEvent.fill(await within(dialog).findByRole('textbox',{name:'Your answer'}),'Will this affect the production database?')
  await userEvent.click(within(dialog).getByRole('button',{name:'Ask Main'}))
  await within(dialog).findByText('Will this affect the production database?')
  expect(client.answer).not.toHaveBeenCalled()
  await act(async()=>{
   data.entries.push({seq:2,author:'main',kind:'reply',body:'No. Staging has its own database. Production stays on the current release.',created_at:'2026-11-05T12:02:00Z'})
   data.question.question_turn='person'
   useMessageStore.setState(state=>({messages:state.messages.map(message=>({...message,question_turn:'person'}))}))
  })
  await within(dialog).findByText('No. Staging has its own database. Production stays on the current release.')
  expect(dialog.scrollWidth).toBeLessThanOrEqual(dialog.clientWidth)
  if(shots) await page.screenshot({path:`${shots}/question-thread-${scenario.width}-pane-${scenario.pane}.png`})
  await userEvent.click(within(dialog).getByRole('button',{name:'Open as tab'}))
  expect(screen.queryByRole('dialog')).toBeNull()
  const tab=await screen.findByRole('tab',{name:'Question: Deploy this release to staging?'})
  expect(tab).toHaveAttribute('aria-selected','true')
  await screen.findByRole('region',{name:'Question: Deploy this release to staging?'})
  await userEvent.click(screen.getByRole('button',{name:/Deploy this release/}))
  dialog=screen.getByRole('dialog')
  await userEvent.click(await within(dialog).findByRole('button',{name:'Open as tab'}))
  expect(screen.getAllByRole('tab',{name:/Question:/})).toHaveLength(1)
  act(()=>{
   data.question.body='Deploy only to staging?'
   data.question.question_revision=3
   useMessageStore.setState(state=>({messages:state.messages.map(message=>({...message,body:data.question.body,question_revision:3}))}))
  })
  await screen.findByRole('tab',{name:'Question: Deploy only to staging?'})
  const panel=await screen.findByRole('region',{name:'Question: Deploy only to staging?'})
  expect(within(panel).getAllByRole('heading',{name:'Deploy only to staging?'})).toHaveLength(1)
  expect(within(panel).getAllByText('Deploy only to staging?')).toHaveLength(1)
  await userEvent.fill(within(panel).getByRole('textbox'),'Use staging')
  await userEvent.click(within(panel).getByRole('button',{name:'Answer'}))
  await within(panel).findByText('Answered: Use staging')
  expect(within(panel).queryByRole('textbox')).toBeNull()
  expect(screen.getByRole('tab',{name:/Question:/})).toBeTruthy()
  await waitFor(()=>expect(panel.scrollWidth).toBeLessThanOrEqual(panel.clientWidth))
  if(shots) await page.screenshot({path:`${shots}/question-outcome-${scenario.width}-pane-${scenario.pane}.png`})
 })
}
