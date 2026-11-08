import { afterEach, expect, it, vi } from 'vitest'
import { act, render, screen, fireEvent, within, waitFor } from '@testing-library/react'
import { page, commands } from 'vitest/browser'
import App from '../App'
import { useSessionStore } from '../hooks/useSessionStore'
import { useMessageStore } from '../hooks/useMessageStore'
import { useTranscriptStore } from '@blerglab/chat'
import { makeDaemon, makeSession } from '../test/fixtures'
import { reelReply } from '../test/reelFixture'
import { setTheme } from '../../../../packages/settings/theme'
import { invalidateChatTransport } from '../lib/chatTransport'
import '../index.css'
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async orig => ({ ...(await orig<typeof import('../authClient')>()), getAccessToken: () => 'tok' }))
vi.mock('../claims', async orig => ({ ...(await orig<typeof import('../claims')>()), tokenExpired: () => false }))
afterEach(() => { invalidateChatTransport(); window.history.replaceState({}, '', '/'); vi.unstubAllGlobals(); localStorage.clear() })
for (const theme of ['light', 'dark', 'colorblind-dark'] as const) it.each([390, 3840])(`opens a live Needs you tab in the app, ${theme}, %i px`, async width => {
 await page.viewport(width, width === 390 ? 844 : 2160)
 localStorage.clear(); setTheme(theme)
 const now = new Date().toISOString()
 const sessions = [makeSession({kind:'agent',title:'Prepare the release',runtime:'daemon',started_at:new Date(Date.now()-1800000).toISOString()}), ...['Review release notes','Check accessibility','Verify upgrade'].map((title,n)=>makeSession({id:`worker-${n}`,kind:'agent',title,parent_session_id:'sess-a',started_by:{kind:'session'},runtime:'daemon'}))]
 const messages = Array.from({length:8},(_,n)=>({id:`question-${n}`,session_id:sessions[n%4].id,kind:'ask' as const,status:'open' as const,answerable:true,no_wait:true,owner_only:true,body:`${n === 0 ? 'Choose the release approach?' : ['Keep the current wording?','Include the upgrade checklist?','Use the accessible chart palette?'][n%3]}\nOption A: keep the reviewed draft and its existing release checks.\nOption B: revise the draft before proceeding, preserving all feedback and verification notes.`,created_at:new Date(Date.now()-n*60000).toISOString()}))
 messages.push({...messages[1], id:'main-pending-worker',answerable:false,body:'Worker question waiting for Main'})
 const json=(value:unknown)=>new Response(JSON.stringify(value),{status:200,headers:{'Content-Type':'application/json'}})
 apiFetch.mockImplementation(async(url:string)=> {
  if(url==='/api/session-boards') return json({configured:true,boards:[]})
  if(url.startsWith('/api/models')) return json({models:[{id:'release-model',name:'Release model',section:'main',efforts:['low','high'],default_effort:'low'}],source:'live'})
  if(url.endsWith('/permissions')) return json({can_grant:true,reason:'',grants:[],mcp_tools:[],boards:[]})
  if(url.endsWith('/topics')) return json(reelReply())
  if(url.endsWith('/live')) { const own=messages.filter(m=>url.includes(`/sessions/${m.session_id}/`));return json({session_id:url.split('/sessions/')[1]?.split('/')[0],through_seq:1,generated_at:now,asks:own.map(m=>({event_id:m.id,message_id:m.id,question:m.body,asked_at:m.created_at,seq:1,no_wait:true,owner_only:true})),children:[],files:[],subagents:[],tasks:[],tool_changes:0,counts:{needs_you:own.length,running:0},can_release_prompt:true}) }
  if(url.includes('/proposals')) return json({proposals:[],count:0})
  if(url.startsWith('/api/messages/') && url.includes('/thread')) { const id=url.split('/messages/')[1].split('/')[0]; return json({question:useMessageStore.getState().messages.find(message=>message.id===id),entries:[],more:false}) }
  if(url.includes('/messages')||url.includes('/boards')||url.includes('/crons')) return json([])
  return json({})
 })
 vi.stubGlobal('fetch',vi.fn(async(url:string)=>json(String(url).endsWith('/artifacts')?{artifacts:[]}:[])))
 useSessionStore.setState({daemons:[makeDaemon()],sessions,statusChangedAt:{},serverVersion:'test'})
 useMessageStore.setState({messages})
 useTranscriptStore.getState().resetEvents('sess-a')
 useTranscriptStore.getState().ingest({type:'agent_event',session_id:'sess-a',client_event_id:'release-status',seq:1,kind:'assistant_text',payload:{text:'The release draft is ready. I’m checking the upgrade and accessibility notes while the open questions wait for your review.'},ts:now})
 useTranscriptStore.getState().ingestReplayDone('sess-a',{lastSeq:1,hasMore:false})
 window.history.replaceState({},'','/sessions/sess-a')
 render(<App />)
 if(width===390) fireEvent.click(await screen.findByTestId('rail-toggle'))
 const needs=await screen.findByTestId('rail-needs')
 await waitFor(()=>expect(within(needs).getByTestId('rail-needs-count')).toHaveTextContent('8'))
 expect(within(needs).getAllByRole('listitem')).toHaveLength(3)
 if(width===3840) expect(screen.getByTestId('card-attention-count')).toHaveTextContent('8 need you')
 setTheme(theme)
 if(import.meta.env.VITE_BLG497_SHOTS_DIR) await commands.needsYouScreenshot(width,theme,'panel')
 fireEvent.click(within(needs).getByRole('button',{name:'Open Needs you as tab'}))
 const tab=await screen.findByRole('tab',{name:/Needs you · 8/})
 expect(tab).toBeVisible()
 const region=await screen.findByRole('region',{name:'Needs you'})
 expect(within(region).queryByText('Worker question waiting for Main')).toBeNull()
 expect(within(region).getAllByRole('textbox')).toHaveLength(8)
 await waitFor(()=>expect(within(region).getAllByRole('log',{name:'Question discussion'})).toHaveLength(8))
 const composer=within(region).getAllByRole('textbox')[0]
 expect(composer.getBoundingClientRect().height).toBeLessThanOrEqual(44)
 expect(composer).not.toHaveFocus()
 fireEvent.focus(composer)
 composer.focus()
 expect(composer.getBoundingClientRect().height).toBeGreaterThanOrEqual(90)
 composer.blur()
 expect(composer.getBoundingClientRect().height).toBeLessThanOrEqual(44)
 expect(within(region).getByRole('heading',{name:'Check accessibility'})).toBeVisible()
 expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
 setTheme(theme)
 await waitFor(()=>expect(document.documentElement.getAttribute('data-theme')).toBe(theme))
 const shots=import.meta.env.VITE_BLG497_SHOTS_DIR
 if(shots) await commands.needsYouScreenshot(width,theme)
 fireEvent.change(within(region).getByLabelText('Worker'),{target:{value:'worker-0'}})
 expect(within(region).getAllByRole('textbox')).toHaveLength(2)
 act(()=>useMessageStore.setState(state=>({messages:state.messages.map(message=>message.id==='question-1'?{...message,status:'answered'}:message)})))
 await waitFor(()=>expect(within(region).getAllByRole('textbox')).toHaveLength(1))
 expect(within(region).getByRole('status')).toHaveTextContent('1 of 7 waiting')
 fireEvent.click(screen.getByTestId('rail-toggle'))
 expect(await screen.findByTestId('rail-needs-count')).toHaveTextContent('7')
 fireEvent.click(screen.getByTestId('rail-toggle'))
 if(width===3840) expect(screen.getByTestId('card-attention-count')).toHaveTextContent('7 need you')
 expect(screen.getByRole('tab',{name:/Needs you · 7/})).toBeVisible()
 fireEvent.click(screen.getByRole('button',{name:'Toggle explorer'}))
 const questions=await screen.findByRole('tab',{name:'Questions'})
 expect(questions).toHaveAttribute('aria-description','7 open questions')
 fireEvent.click(screen.getByRole('button',{name:'Toggle explorer'}))
 act(()=>useMessageStore.setState(state=>({messages:state.messages.map(message=>({...message,status:'answered'}))})))
 await within(region).findByText(/all caught up/)
 expect(within(region).queryByLabelText('Worker')).toBeNull()
 expect(within(region).queryByText(/0 waiting|Loading/)).toBeNull()
 expect(screen.getByRole('tab',{name:'Needs you'})).toBeVisible()
 if(shots) await commands.needsYouScreenshot(width,theme,'empty')
})
