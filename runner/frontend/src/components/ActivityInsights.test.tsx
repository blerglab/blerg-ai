import { beforeEach, it, expect, vi } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import ActivityInsights from './ActivityInsights'
import { richActivity, emptyActivity } from '../test/activityFixture'
import { richInsights } from '../test/insightsFixture'
import { rememberInsights, insightsCacheKey, clearInsightsCache } from '../lib/insightsCache'
import { signedInAccount } from '../lib/liveBoardCard'
const apiFetch=vi.hoisted(()=>vi.fn())
vi.mock('../apiFetch',()=>({apiFetch}))
beforeEach(()=>{apiFetch.mockReset();apiFetch.mockResolvedValue({ok:true,json:async()=>richActivity})})
it('uses the requested cached range and its filter options after an initial read fails',async()=>{
 clearInsightsCache()
 const cached={...richActivity,sessions:richActivity.sessions.map(s=>({...s,engine:'cached-engine'}))}
 rememberInsights(insightsCacheKey(signedInAccount(),'activity||30d'),cached)
 apiFetch.mockResolvedValueOnce({ok:false,status:500}).mockImplementation(()=>new Promise(()=>{}))
 const view=render(<ActivityInsights section="flow" range="7d"/>)
 await screen.findByRole('alert')
 view.rerender(<ActivityInsights section="flow" range="30d"/>)
 expect(screen.getByRole('combobox',{name:'Engine slice'})).toHaveTextContent('cached-engine')
 expect(screen.getByRole('heading',{name:'Worker time'})).toBeInTheDocument()
 clearInsightsCache()
})
it('loads owner-scoped workspace data and renders measured gate and worker time',async()=>{
 render(<ActivityInsights section="flow" workspace="workspace-a" range="30d"/>)
 expect(await screen.findByText('Gate time')).toBeInTheDocument()
 expect(apiFetch).toHaveBeenCalledWith('/api/insights/activity?range=30d&workspace=workspace-a',expect.objectContaining({signal:expect.any(AbortSignal)}))
 expect(screen.getByText(/n 4 · median/)).toBeInTheDocument()
 expect(screen.getByText('Cards with unknown active time')).toBeInTheDocument()
 expect(screen.getByRole('heading',{name:'Worker time'})).toBeInTheDocument()
 expect(screen.getByText(/Dollar cost by phase is not recorded/)).toBeInTheDocument()
 fireEvent.change(screen.getByRole('combobox',{name:'Risk slice'}),{target:{value:'high'}})
 expect(screen.queryByText(/n 4 · median/)).toBeNull()
 expect(screen.getByText('No linked card active time is recorded in this range.')).toBeInTheDocument()
})
it('distinguishes unavailable reads from honest empty data and retries',async()=>{
 apiFetch.mockResolvedValueOnce({ok:false}).mockResolvedValue({ok:true,json:async()=>emptyActivity})
 render(<ActivityInsights section="reviews"/>)
 expect(await screen.findByRole('alert')).toHaveTextContent('Could not load')
 fireEvent.click(screen.getByRole('button',{name:'Retry'}))
 expect(await screen.findByText('No review records in this range.')).toBeInTheDocument()
 expect(screen.getByText('No failure records in this range.')).toBeInTheDocument()
})
it('keeps a workspace response from appearing after its scope changes',async()=>{
 let resolve!:(v:unknown)=>void
 apiFetch.mockReturnValueOnce(new Promise(r=>{resolve=r})).mockResolvedValue({ok:true,json:async()=>emptyActivity})
 const view=render(<ActivityInsights section="reviews" workspace="first"/>)
 view.rerender(<ActivityInsights section="reviews" workspace="second"/>)
 await screen.findByText('No review records in this range.')
 resolve({ok:true,json:async()=>richActivity})
 await waitFor(()=>expect(screen.getByText('0 reviews.')).toBeInTheDocument())
})
it('shows unknown gate coverage instead of zero bars for uninstrumented time',async()=>{
 apiFetch.mockResolvedValue({ok:true,json:async()=>({...emptyActivity,sessions:[{...richActivity.sessions[0],timing:{seconds:{unknown:100},phases:{gates:0},basis:'recorded',incomplete_tools:0}}]})})
 render(<ActivityInsights section="flow"/>)
 expect(await screen.findByText('No classified gate time is recorded for these conversations.')).toBeInTheDocument()
})
it('uses raw observations for small groups, including accessible descriptions',async()=>{
 render(<ActivityInsights section="flow"/>)
 const row=await screen.findByRole('img',{name:/M: n 4/})
 expect(row.querySelectorAll('.sb-dot')).toHaveLength(4)
 expect(row).not.toHaveAccessibleName(/25th|75th/)
})
it('uses title attributes without changing the document title',async()=>{
 document.title='Insights fixture'
 render(<ActivityInsights section="flow"/>)
 await screen.findByRole('heading',{name:'Worker time'})
 expect(document.title).toBe('Insights fixture')
 expect(document.querySelector('.activity-stack title')).toBeNull()
})
it('shows an owner-only state for a shared viewer and sign-in wording for 401',async()=>{
 apiFetch.mockResolvedValueOnce({ok:false,status:404}).mockResolvedValueOnce({ok:false,status:401})
 const v=render(<ActivityInsights section="flow" workspace="shared"/>)
 expect(await screen.findByText("Only the owner's activity is available.")).toBeInTheDocument()
 expect(screen.queryByRole('button',{name:'Retry'})).toBeNull()
 v.rerender(<ActivityInsights section="flow" workspace="other"/>)
 expect(await screen.findByText('Sign in to view work and review data.')).toBeInTheDocument()
})
it('aborts a pending read when unmounted and reuses a loaded report across section changes',async()=>{
 const view=render(<ActivityInsights section="flow"/>)
 await screen.findByRole('heading',{name:'Gate cost'})
 view.rerender(<ActivityInsights section="reviews"/>)
 expect(apiFetch).toHaveBeenCalledTimes(1)
 const signal=(apiFetch.mock.calls[0][1] as RequestInit).signal!
 view.unmount()
 expect(signal.aborted).toBe(true)
})

it('labels assignment bias and requires a risk slice for engine observations',async()=>{
 render(<ActivityInsights section="flow"/>)
 expect(await screen.findByText(/Mission-critical work goes to one engine/)).toBeInTheDocument()
 expect(screen.getByText('Choose a risk to compare engines; task risk affects review needs.')).toBeInTheDocument()
 fireEvent.change(screen.getByRole('combobox',{name:'Risk slice'}),{target:{value:'medium'}})
 expect(screen.getByRole('img',{name:/claude: n 1, observed values 1 review round/})).toBeInTheDocument()
})
it('does not refetch after a loaded report is hidden and shown again',async()=>{
 const view=render(<ActivityInsights section="flow"/>)
 await screen.findByRole('heading',{name:'Gate cost'})
 view.rerender(<ActivityInsights section="usage" enabled={false}/>)
 view.rerender(<ActivityInsights section="flow" enabled/>)
 expect(apiFetch).toHaveBeenCalledTimes(1)
})

it('caps gate and worker rows instead of rendering a thousand bars, with a way to see them all',async()=>{
 apiFetch.mockResolvedValue({ok:true,json:async()=>({...richActivity,sessions:Array.from({length:100},(_,i)=>({...richActivity.sessions[0],id:`s-${i}`}))})})
 const view=render(<ActivityInsights section="flow"/>)
 await screen.findByRole('heading',{name:'Worker time'})
 expect(view.container.querySelectorAll('.activity-workers>li')).toHaveLength(50)
 expect(screen.getByRole('heading',{name:'Gate cost'}).closest('section')!.querySelectorAll('.activity-bars>li')).toHaveLength(50)
 const workerSection=screen.getByRole('heading',{name:'Worker time'}).closest('section')!
 const showAll=within(workerSection).getByRole('button',{name:'See all 100'})
 const list=workerSection.querySelector('.activity-workers')!
 expect(showAll).toHaveAttribute('aria-expanded','false')
 expect(showAll.getAttribute('aria-controls')).toBe(list.id)
 fireEvent.click(showAll)
 expect(view.container.querySelectorAll('.activity-workers>li')).toHaveLength(100)
 const showFewer=within(workerSection).getByRole('button',{name:'Show top 50'})
 expect(showFewer).toHaveAttribute('aria-expanded','true')
 expect(showFewer.getAttribute('aria-controls')).toBe(list.id)
})
it('drops the cached owner report when the sign-in identity changes',async()=>{
 apiFetch.mockResolvedValueOnce({ok:true,json:async()=>richActivity}).mockReturnValue(new Promise(()=>{}))
 render(<ActivityInsights section="flow"/>)
 await screen.findByRole('heading',{name:'Worker time'})
 fireEvent(window,new Event('blerg-identity-changed'))
 await waitFor(()=>expect(screen.queryByRole('heading',{name:'Worker time'})).toBeNull())
 expect(apiFetch).toHaveBeenCalledTimes(2)
 expect((apiFetch.mock.calls[0][1] as RequestInit).signal!.aborted).toBe(true)
})

it('filters review evidence and questions by size, builder and subagent use without another read',async()=>{
 render(<ActivityInsights section="reviews"/>)
 await screen.findByText('Questions asked')
 expect(screen.getByText('Questions asked').nextElementSibling).toHaveTextContent('6')
 expect(screen.getByText('Reached the person').nextElementSibling).toHaveTextContent('1')
 expect(screen.getByRole('heading',{name:'Review outcomes'})).toBeInTheDocument()
 fireEvent.change(screen.getByRole('combobox',{name:'Subagents used slice'}),{target:{value:'yes'}})
 expect(screen.getByText('Questions asked').nextElementSibling).toHaveTextContent('4')
 fireEvent.change(screen.getByRole('combobox',{name:'Engine slice'}),{target:{value:'codex'}})
 expect(screen.getByText('Questions asked').nextElementSibling).toHaveTextContent('0')
 expect(screen.getByText('No review records in this range.')).toBeInTheDocument()
 expect(apiFetch).toHaveBeenCalledTimes(1)
})

it('uses conversation titles for gate links and worker rows and retains complete phase names',async()=>{
 render(<ActivityInsights section="flow"/>)
 await screen.findByRole('heading',{name:'Time by phase'})
 expect(screen.getAllByRole('link',{name:'Measure gate time'})).toHaveLength(2)
 expect(screen.getAllByRole('link',{name:'Measure gate time'})[0]).toHaveAttribute('href','/sessions/worker-3')
 expect(screen.queryByText('work…er-3')).toBeNull()
 expect(screen.getByText('Building')).toBeInTheDocument()
})

it('distinguishes unavailable question counts from a measured zero',async()=>{
 apiFetch.mockResolvedValue({ok:true,json:async()=>({...emptyActivity,questions:undefined})})
 render(<ActivityInsights section="reviews"/>)
 expect(await screen.findByText('Question counts are not available from this server.')).toBeInTheDocument()
 expect(screen.queryByText('Questions asked')).toBeNull()
})

it('uses the same chart for single categories and keeps definitions in measurement details',async()=>{
 render(<ActivityInsights section="reviews"/>)
 await screen.findByText('Questions asked')
 expect(screen.getByText('1 review.')).toBeInTheDocument()
 expect(screen.getByText('1 failure · 1 successful rerun.')).toBeInTheDocument()
 expect(screen.getByRole('button',{name:'Show Changes requested'})).toBeInTheDocument()
 expect(screen.getByText(/Reviewer names come from/).closest('details')).not.toHaveAttribute('open')
 expect(screen.queryByText(/Your work only/)).toBeNull()
})

it('drills from active time into exact cards in place and returns to the chart',async()=>{
 render(<ActivityInsights section="flow"/>)
 await screen.findByRole('heading',{name:'Active time per card by size'})
 fireEvent.click(screen.getByRole('button',{name:/n 4 · median/}))
 const list=screen.getByRole('list',{name:'Records for M'})
 expect(list).toHaveTextContent('fixture#1')
 expect(list).toHaveTextContent('fixture#4')
 expect(list).not.toHaveTextContent('fixture#7')
 fireEvent.click(screen.getByRole('button',{name:'Show all'}))
 expect(screen.queryByRole('list',{name:'Records for M'})).toBeNull()
})
it('drills from review rounds and outcomes into their records with a way back',async()=>{
 render(<ActivityInsights section="reviews"/>)
 await screen.findByRole('heading',{name:'Review outcomes'})
 fireEvent.click(screen.getByRole('button',{name:'Show Cards with 1 review round'}))
 expect(screen.getByRole('list',{name:'Records for Cards with 1 review round'})).toHaveTextContent('fixture#1')
 fireEvent.click(screen.getByRole('button',{name:'Show all'}))
 fireEvent.click(screen.getByRole('button',{name:'Show Changes requested'}))
 expect(screen.getByRole('list',{name:'Records for Changes requested'})).toHaveTextContent('#1')
 fireEvent.click(screen.getByRole('button',{name:'Show all'}))
 expect(screen.queryByRole('list',{name:'Records for Changes requested'})).toBeNull()
})

it('distinguishes the same card number on different boards in drill-down records',async()=>{
 apiFetch.mockResolvedValue({ok:true,json:async()=>({...richActivity,cards:[{...richActivity.cards[0],reference:'BLG-1'},{...richActivity.cards[0],key:'other-board#1',reference:'OPS-1'}],reviews:[richActivity.reviews[0],{...richActivity.reviews[0],board_id:'other-board'}]})})
 render(<ActivityInsights section="reviews"/>)
 await screen.findByRole('heading',{name:'Review outcomes'})
 fireEvent.click(screen.getByRole('button',{name:'Show Changes requested'}))
 const records=screen.getByRole('list',{name:'Records for Changes requested'})
 expect(records).toHaveTextContent('BLG-1')
 expect(records).toHaveTextContent('OPS-1')
 expect(records).not.toHaveTextContent('fixture#1')
 expect(records).not.toHaveTextContent('other-board#1')
})

// Context shows only context content, the same opt-out 'skills' already uses: the generic flow
// diagnostics are not about context and would only repeat the Time tab underneath it.
it('shows only context content, not the generic flow diagnostics (like skills)',async()=>{
 render(<MemoryRouter><ActivityInsights section="flow" questionView="context" summary={richInsights}/></MemoryRouter>)
 await screen.findByRole('figure',{name:'Context fill'})
 expect(screen.queryByRole('heading',{name:'Worker time'})).toBeNull()
 expect(screen.queryByRole('heading',{name:'Gate cost'})).toBeNull()
 expect(screen.queryByRole('heading',{name:'Active time per card by size'})).toBeNull()
 expect(screen.queryByRole('heading',{name:'Reviews by building engine'})).toBeNull()
 expect(screen.queryByText('How this is measured')).toBeNull()
})
