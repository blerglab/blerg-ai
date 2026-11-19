import { useTranscriptStore } from '@blerglab/chat'
import { invalidateChatTransport } from '../lib/chatTransport'
// Reach Insights through the shipped application, with realistic API-shaped data.
import { afterEach, expect, it, vi } from 'vitest'
import { act, render, screen, fireEvent, waitFor } from '@testing-library/react'
import { commands, page, userEvent } from 'vitest/browser'
import App from '../App'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { richActivity } from '../test/activityFixture'
import { richInsights, richPrices } from '../test/insightsFixture'
import '../index.css'
import { setTheme } from '../../../../packages/settings/theme'

// Claude's weekly usage-window samples (BLG-462): the current window has a sampling gap, starts
// at 0%, and is on pace to cross 100% before reset; the previous window is recorded too, so it
// shows faintly for comparison. Codex has none here, so it stays in dollars (a mixed account).
function usageBudgetFixture(now=Date.now()){
 const WEEK=7*24*3600_000,HOUR=3600_000
 const currentStart=now-2.5*24*3600_000, currentReset=currentStart+WEEK
 const previousStart=currentStart-WEEK, previousReset=currentStart
 const row=(atMs:number,windowStart:number,resetsAt:number,usedPercent:number)=>
  ({provider:'claude',window_id:'7d',used_percent:usedPercent,window_start:new Date(windowStart).toISOString(),resets_at:new Date(resetsAt).toISOString(),sampled_at:new Date(atMs).toISOString()})
 return [
  row(previousStart+6*HOUR,previousStart,previousReset,8),
  row(previousStart+2*24*3600_000,previousStart,previousReset,55),
  row(previousStart+5*24*3600_000,previousStart,previousReset,92),
  row(currentStart,currentStart,currentReset,0),
  row(currentStart+2*HOUR,currentStart,currentReset,9),
  // A sampling gap: the daemon stopped reporting for several hours.
  row(currentStart+14*HOUR,currentStart,currentReset,38),
  row(currentStart+2*24*3600_000,currentStart,currentReset,68),
 ]
}

const apiFetch=vi.hoisted(()=>vi.fn())
vi.mock('../apiFetch',()=>({apiFetch}))
vi.mock('../ws',()=>import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
afterEach(()=>{window.history.replaceState({},'', '/');invalidateChatTransport();vi.unstubAllGlobals();vi.restoreAllMocks()})

it.each([390,3840])('opens five question views, details and an invisible refresh through the app at %i px',async width=>{
 await page.viewport(width,width===390?844:2160)
 setTheme('dark')
 const shots=import.meta.env.VITE_INSIGHTS_SHOTS_DIR
 const capture=async(name:string,viewport=width)=>{
  const match=import.meta.env.VITE_INSIGHTS_SHOTS_MATCH
  if(!shots||(match&&!name.includes(match)))return
  const frame=document.createElement('div')
  frame.style.cssText='position:fixed;inset:0;width:100vw;height:100vh;pointer-events:none'
  document.body.append(frame)
  expect(frame.getBoundingClientRect().width).toBe(viewport)
  try{await page.screenshot({element:frame,path:`${shots}/${name}-${viewport}.png`})}finally{frame.remove()}
 }
 const refreshers:(()=>void)[]=[]
 const interval=globalThis.setInterval.bind(globalThis)
 vi.spyOn(window,'setInterval').mockImplementation((handler,delay,...args)=>{
  if(delay===30_000&&typeof handler==='function')refreshers.push(()=>handler(...args))
  return interval(handler,delay,...args)
 })
 let pending=true
 let skillsUnavailable=false
 let skillsRich=false
 let timingPending=false
 const completions:(()=>void)[]=[]
 // BLG-464 round 2: show 5+ skills in mixed verdict states together (owner's review ask). Three
 // synthetic skills, each on its own cards/sessions so the other tabs' totals stay untouched;
 // 'managing-tickets' and 'coordinating' already have display names (skillNames).
 const skillsDemo=(()=>{
  const session=(id:string,card:string)=>({id,parent_session_id:null,started_at:'2026-10-25T00:00:00Z',ended_at:'2026-10-25T01:00:00Z',shared_with_other_cards:false,cards:[card],timing:null})
  // The earlier Card cost step leaves the Risk filter on 'medium'; give these cards that risk so
  // activitySlice (insightsActivity.ts) doesn't filter their sessions back out.
  const card=(key:string)=>({key,size:'M',risk:'medium',complexity:'medium',size_first_set_at:'2026-10-24T00:00:00Z',flow:null})
  const load=(session_id:string,skill:string,version:string,at:string)=>({session_id,role:'worker',engine:'claude',skill,version,mode:'on_demand' as const,bytes:3000,basis:'native_skill_success',at})
  const review=(n:number,round:number,findings:string[])=>({session_id:'demo',board_id:'fixture',card:n,round,verdict:'changes',findings,recorded_at:'2026-10-26T00:00:00Z'})
  return {
   cards:[20,21,22,23,24,25].map(n=>card(`fixture#${n}`)),
   sessions:[
    session('skill-demo-mt-1','fixture#20'),session('skill-demo-mt-2','fixture#21'),
    session('skill-demo-co-1','fixture#22'),session('skill-demo-co-2','fixture#23'),
    session('skill-demo-pl-1','fixture#24'),session('skill-demo-pl-2','fixture#25'),
   ],
   reviews:[
    review(20,1,['high','medium','low','low']), // managing-tickets v1: 4 findings / 1 review
    review(21,1,['low']),                        // managing-tickets v2: 1 / 1 -> fewer (Helping)
    review(22,1,['low']),                        // coordinating v1: 1 / 1
    review(23,1,['high','medium','low']),        // coordinating v2: 3 / 1 -> more (Worse)
    review(24,1,['medium','medium']),            // planning v1: 2 / 1
    review(25,1,['medium','medium']),review(25,2,['medium','medium']), // planning v2: 4 / 2 -> same
   ],
   loads:[
    load('skill-demo-mt-1','managing-tickets','1'.repeat(64),'2026-10-24T02:00:00Z'),
    load('skill-demo-mt-2','managing-tickets','2'.repeat(64),'2026-10-27T02:00:00Z'),
    load('skill-demo-co-1','coordinating','3'.repeat(64),'2026-10-24T02:00:00Z'),
    load('skill-demo-co-2','coordinating','4'.repeat(64),'2026-10-27T02:00:00Z'),
    load('skill-demo-pl-1','planning','5'.repeat(64),'2026-10-24T02:00:00Z'),
    load('skill-demo-pl-2','planning','6'.repeat(64),'2026-10-27T02:00:00Z'),
   ],
  }
 })()
 const richActivityWithSkillsDemo=()=>({...richActivity,cards:[...richActivity.cards,...skillsDemo.cards],sessions:[...richActivity.sessions,...skillsDemo.sessions],reviews:[...richActivity.reviews,...skillsDemo.reviews],skills:{...richActivity.skills,loads:[...richActivity.skills!.loads,...skillsDemo.loads]}})
 apiFetch.mockImplementation(async(url:string)=>{
  let body:unknown={}
  if(url.startsWith('/api/insights/activity'))body=skillsUnavailable?{...richActivity,skills:{...richActivity.skills,unavailable:true}}:timingPending?{...richActivity,pending_sessions:12,sessions:[...richActivity.sessions,...Array.from({length:12},(_,i)=>({...richActivity.sessions[0],id:`queued-timing-${i}`,title:`Verify required checks ${i+1}`,timing:null}))]}:skillsRich?richActivityWithSkillsDemo():richActivity
  else if(url.startsWith('/api/insights/prices'))body=richPrices
  else if(url.startsWith('/api/insights?'))body={...richInsights,workspace_name:'Improve ticket flow',cluster:null,usage_budget:usageBudgetFixture(),tokens:{...richInsights.tokens,top_sessions:richInsights.tokens.top_sessions.map((s,i)=>({...s,session_id:`worker-${i+1}`}))}}
  else if(url.endsWith('/metrics'))body={generated_at:new Date().toISOString(),workers:[],runs:[],machine:null}
  else if(url.endsWith('/permissions'))body={can_grant:true,reason:'',grants:[],boards:[],mcp_tools:[]}
  else if(url.startsWith('/api/models/'))body={source:'live',models:[{id:'claude-sonnet-4-6',name:'Sonnet',section:'main',efforts:['low','high'],default_effort:'high'}]}
  else if(url.endsWith('/live'))body={session_id:'sess-a',through_seq:2,generated_at:new Date().toISOString(),tokens_hidden:false,asks:[],subagents:[],tasks:[],tool_changes:0,counts:{needs_you:0,running:0},can_release_prompt:true,children:[]}
  else if(url.endsWith('/shell-capability'))body={available:false}
  else if(url.endsWith('/topics'))body={topics:[],strokes:[]}
  else if(url.endsWith('/children')||url.endsWith('/messages')||url.endsWith('/shells')||url.includes('/boards'))body=[]
  if(pending&&url.startsWith('/api/insights'))await new Promise<void>(resolve=>completions.push(resolve))
  return {ok:true,status:200,json:async()=>body,text:async()=>''}
 })
 vi.stubGlobal('fetch',vi.fn(async()=>new Response('[]',{status:200})))
 useTranscriptStore.setState({sessions:{}})
 const transcript=useTranscriptStore.getState()
 transcript.ingest({type:'agent_event',session_id:'sess-a',client_event_id:'question',seq:1,ts:'2026-10-31T19:00:00Z',kind:'user_message',payload:{text:'Improve ticket flow and check the narrow-panel layout.',source:'chat'}})
 transcript.ingest({type:'agent_event',session_id:'sess-a',client_event_id:'answer',seq:2,ts:'2026-10-31T19:01:00Z',kind:'assistant_text',payload:{text:'The layout changes are ready. I verified the compact toolbar and the stacked tables.',done:true}})
 transcript.ingestReplayDone('sess-a',{lastSeq:2,hasMore:false})
 useSessionStore.setState({daemons:[makeDaemon({usage_limits:[{provider:'claude',fetched_at:new Date().toISOString(),windows:[{id:'5h',used_percent:63,resets_at:new Date(Date.now()+3600_000).toISOString()}]}]})],sessions:[makeSession({kind:'agent',engine:'claude',model:'claude-sonnet-4-6',runtime:'daemon',started_at:'2026-10-31T19:00:00Z',status:'idle',title:'Improve ticket flow'})],statusChangedAt:{}})
 window.history.replaceState({},'', '/sessions/sess-a')
 render(<App/> )
 if(width===390){
  // On a phone (review round 2 of card BLG-450), Search joins Star inside the collapsed Open menu.
  await page.getByTestId('open-tab-menu').click()
  await page.getByRole('menuitem',{name:'Search commands and files',exact:true}).click()
  await page.getByRole('combobox',{name:/Search commands/}).fill('Open Insights')
  await page.getByRole('option',{name:/Open Insights/}).click()
 }else fireEvent.click(screen.getByTestId('insights-nav'))
 await screen.findByText('Loading insights…')
 await commands.setUsageReducedMotion(true)
 try {
  const skeleton=document.querySelector('.insights-skeleton .initial-skeleton-value')!
  expect(getComputedStyle(skeleton,'::after').animationName).toBe('none')
  await capture('initial-skeleton')
 }finally{await commands.setUsageReducedMotion(false)}
 pending=false
 await act(async()=>completions.forEach(f=>f()))
 await screen.findByTestId('question-health')
 expect(window.location.pathname).toBe('/sessions/sess-a')
 expect(screen.getAllByRole('tab',{name:/Insights/})).toHaveLength(1)
 expect(screen.queryByRole('link',{name:'Back to conversation'})).toBeNull()
 expect(screen.queryByText('Improve ticket flow',{selector:'.insights-sub'})).toBeNull()
 const choose=async(label:string,id:string)=>{
  if(screen.getByLabelText('Insights view').getClientRects().length)await userEvent.selectOptions(screen.getByLabelText('Insights view'),id)
  else await page.getByRole('button',{name:label,exact:true}).click()
  await screen.findByTestId(`question-${id}`)
 }
 await capture('health-before-refresh')
 const chart=screen.getByTestId('question-health')
 const markup=chart.outerHTML
 const bounds=chart.getBoundingClientRect()
 const shell=document.querySelector<HTMLElement>('.insights-tab')!
 const scroll=shell.scrollTop
 pending=true
 await act(async()=>{refreshers.forEach(f=>f())})
 expect(completions.length).toBeGreaterThan(0)
 expect(screen.getByTestId('question-health')).toBe(chart)
 expect(chart.outerHTML).toBe(markup)
 expect(chart.getBoundingClientRect().toJSON()).toEqual(bounds.toJSON())
 expect(shell.scrollTop).toBe(scroll)
 expect(screen.queryByText('Updating…')).toBeNull()
 await capture('health-mid-refresh')
 pending=false
 await act(async()=>completions.forEach(f=>f()))
 for(const [label,id] of [['Card cost','cost'],['Time','time'],['Spending','spending'],['Skills','skills'],['Context','context']]){
  await choose(label,id)
  if(id==='skills'){
   skillsRich=true
   await act(async()=>{refreshers.forEach(f=>f())})
   await screen.findByTestId('skill-card-planning')
  }
  expect(shell.scrollWidth).toBeLessThanOrEqual(shell.clientWidth)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  await capture(id)
  if(id==='time'||id==='cost'){
   await capture(`${id}-timing-complete-before`)
   timingPending=true
   await act(async()=>{refreshers.forEach(f=>f())})
   await screen.findByText(/timing for 12 completed conversations/i)
   expect(screen.getByTestId(`question-${id}`)).toHaveTextContent('is incomplete: timing for 12 completed conversations')
   await capture(`${id}-timing-pending-after`)
   timingPending=false
   await act(async()=>{refreshers.forEach(f=>f())})
   await waitFor(()=>expect(screen.queryByText(/timing for 12 completed conversations/i)).toBeNull())
  }
  if(id==='skills'){
   for(const summary of screen.getAllByText('Evidence by version'))fireEvent.click(summary)
   await capture('skills-evidence')
   expect(shell.scrollWidth).toBeLessThanOrEqual(shell.clientWidth)
   skillsUnavailable=true
   await act(async()=>{refreshers.forEach(f=>f())})
   await screen.findByText(/Skill evidence is temporarily unavailable/)
   await capture('skills-unavailable')
   skillsUnavailable=false
   await act(async()=>{refreshers.forEach(f=>f())})
   await screen.findByTestId('skill-card-planning')
  }
  if(id==='cost'){
   await page.getByRole('button',{name:/n 4 · median/}).click()
   expect(screen.getByRole('list',{name:'Records for M'})).toHaveTextContent('fixture#1')
   await capture('cost-drilldown')
   await page.getByRole('button',{name:'Show all',exact:true}).click()
   await page.getByText('Filter',{exact:true}).click()
   await userEvent.selectOptions(screen.getByRole('combobox',{name:'Risk slice'}),'medium')
   await userEvent.keyboard('{Escape}')
   await capture('cost-reviews')
   screen.getByRole('heading',{name:'Rework by building engine'}).scrollIntoView({block:'start'})
   await capture('cost-rework')
  }
  if(id==='time'){
   await page.getByRole('button',{name:'Gates',exact:true}).click()
   expect(screen.getByRole('button',{name:'Gates'})).toHaveAttribute('aria-pressed','false')
   await capture('time-gates-hidden')
   const timeSVG=screen.getByTestId('question-time').querySelector('svg')!
   for(const event of timeSVG.querySelectorAll('circle'))for(const label of timeSVG.querySelectorAll('text')){
    const a=event.getBoundingClientRect(),b=label.getBoundingClientRect()
    expect(a.left<b.right&&a.right>b.left&&a.top<b.bottom&&a.bottom>b.top,`Event marker overlaps ${label.textContent}`).toBe(false)
   }
   await page.getByRole('button',{name:'Gates',exact:true}).click()
   await page.getByRole('button',{name:'Show Gates breakdown',exact:true}).click()
   await screen.findByTestId('time-breakdown-gates')
   await capture('time-gates-selected')
   screen.getByRole('heading',{name:'Gate time'}).scrollIntoView({block:'start'})
   await capture('time-gates')
   screen.getByRole('heading',{name:'Slowest suites and regions'}).scrollIntoView({block:'start'})
   await capture('time-gate-runs')
   await page.getByRole('button',{name:'Show Building breakdown',exact:true}).click()
   await screen.findByTestId('time-breakdown-building')
   screen.getByRole('heading',{name:'Building time'}).scrollIntoView({block:'start'})
   await capture('time-building')
   screen.getByRole('heading',{name:'First build and rework by card'}).scrollIntoView({block:'start'})
   await capture('time-rework')
   await page.getByRole('button',{name:'Hide Building breakdown',exact:true}).click()
   await page.getByRole('button',{name:'Show Gates',exact:true}).click()
   expect(screen.getByRole('list',{name:'Records for Gates'})).toHaveTextContent('Improve ticket flow')
   await capture('time-drilldown')
  }
 }
 await choose('Agent health','health')
 await screen.findByTestId('card-cost')
 expect(screen.getByTestId('card-cost')).toHaveTextContent('—')
 expect(screen.getByTestId('card-cost')).not.toHaveTextContent('claude-opus')
 if(width===390)await screen.getByRole('heading',{name:'Pod startup'}).scrollIntoView({block:'start'})
 // Keep hovercards out of the refresh-stability comparison after scrolling.
 await userEvent.hover(screen.getByRole('heading',{name:'Pod startup'}))
 await capture('details')
 const diagnostics=screen.getByRole('heading',{name:'Pod startup'}).closest('.insights-report')!
 const detailMarkup=diagnostics.textContent, detailScroll=shell.scrollTop
 if(width===390)expect(detailScroll).toBeGreaterThan(0)
 pending=true
 await act(async()=>{refreshers.forEach(f=>f())})
 expect(diagnostics.textContent).toBe(detailMarkup)
 expect(shell.scrollTop).toBe(detailScroll)
 await capture('details-mid-refresh')
 pending=false
 await act(async()=>completions.forEach(f=>f()))
 expect(shell.scrollWidth).toBeLessThanOrEqual(shell.clientWidth)
 if(width===3840){
  const source=screen.getByRole('tab',{name:/Insights/}).closest<HTMLElement>('.session-tab')!
  const group=document.querySelector<HTMLElement>('[data-group-id]')!
  const box=group.getBoundingClientRect()
  await userEvent.dragAndDrop(source,group,{targetPosition:{x:box.width-12,y:box.height/2}})
  await waitFor(()=>expect(document.querySelectorAll('[data-group-id]')).toHaveLength(2))
  await page.viewport(1024,900)
  // The tab's container, rather than the window, chooses stacked diagnostics.
  await screen.findByTestId('startup-stack')
  await screen.findByTestId('tokens-stack')
  const panel=document.querySelector<HTMLElement>('.insights-tab')!
  expect(panel.scrollWidth).toBeLessThanOrEqual(panel.clientWidth)
  screen.getByRole('heading',{name:'Pod startup'}).scrollIntoView({block:'start'})
  await capture('narrow-split-startup',1024)
  screen.getByRole('heading',{name:'Tokens and cost'}).scrollIntoView({block:'start'})
  await capture('narrow-split-tokens',1024)
  await choose('Time','time')
  await capture('narrow-timing-complete',1024)
  timingPending=true
  await act(async()=>{refreshers.forEach(f=>f())})
  await screen.findByText(/timing for 12 completed conversations/i)
  await capture('narrow-timing-pending',1024)
  timingPending=false
  await act(async()=>{refreshers.forEach(f=>f())})
  await waitFor(()=>expect(screen.queryByText(/timing for 12 completed conversations/i)).toBeNull())
  await page.getByRole('button',{name:'Show Gates breakdown',exact:true}).click()
  await screen.findByTestId('time-breakdown-gates')
  screen.getByRole('heading',{name:'Gate time'}).scrollIntoView({block:'start'})
  await capture('narrow-split-gates',1024)
  await page.getByRole('button',{name:'Show Building breakdown',exact:true}).click()
  await screen.findByTestId('time-breakdown-building')
  screen.getByRole('heading',{name:'Building time'}).scrollIntoView({block:'start'})
  await capture('narrow-split-building',1024)
  await choose('Skills','skills')
  await screen.findByTestId('skill-card-ux-design')
  expect(panel.scrollWidth).toBeLessThanOrEqual(panel.clientWidth)
  await capture('narrow-split-skills',1024)
  await choose('Context','context')
  await capture('narrow-split-context',1024)
 }
})
