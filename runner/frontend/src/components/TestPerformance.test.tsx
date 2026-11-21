import {act,cleanup,render,screen,waitFor,fireEvent} from '@testing-library/react'
import {afterEach,expect,it,vi} from 'vitest'
import TestPerformance from './TestPerformance'
import {classifyChart} from '../../../../packages/chat/src/visual/blerg/classify'
const state=vi.hoisted(()=>({account:'alice',apiFetch:vi.fn()}))
vi.mock('../apiFetch',()=>({apiFetch:state.apiFetch}))
vi.mock('../lib/liveBoardCard',()=>({signedInAccount:()=>state.account}))
vi.mock('@blerglab/chat',()=>({VisualBlock:({source}:{source:string})=><div data-testid="chart" data-source={source}/>}))
afterEach(()=>{cleanup();state.account='alice';state.apiFetch.mockReset()})
it('purges the previous account in the first render after identity rebinding',async()=>{
 const runs=[{workspace_id:'workspace',member_id:'api',run_id:'run',commit:'01234567',finished_at:new Date().toISOString(),seconds:10,complete:true,region:null,suites:[{suite:'unit',seconds:10,outcome:'pass',counts:null,slowest:[]}]}]
 state.apiFetch.mockResolvedValueOnce(new Response(JSON.stringify({runs,truncated:false}))).mockImplementation(()=>new Promise(()=>{}))
 render(<TestPerformance/>)
 await screen.findByText('1 recorded run · Wall time per suite; parallel suites can overlap.')
 expect(screen.getByRole('heading',{name:'unit took 10.0 seconds in the latest run'})).toBeVisible()
 expect(screen.getByRole('group',{name:'Test run overview'})).toHaveTextContent('Latest run10.0 s')
 const source=JSON.parse(screen.getByTestId('chart').getAttribute('data-source')!) as Record<string,unknown>
 expect(classifyChart(source)).toBe('blerg')
 act(()=>{state.account='bob';window.dispatchEvent(new Event('blerg-identity-changed'))})
 expect(screen.queryByTestId('chart')).toBeNull()
 expect(screen.getByLabelText('Fetching test history')).toBeVisible()
 await waitFor(()=>expect(state.apiFetch).toHaveBeenCalledTimes(2))
})
it('lets a quiet project be selected independently of the current run page',async()=>{
 state.apiFetch.mockImplementation(async()=>new Response(JSON.stringify({runs:[],truncated:false,projects:[{workspace_id:'quiet',member_id:'repository',name:'Repository'}]})))
 render(<TestPerformance/>)
 expect(await screen.findByRole('option',{name:'Repository · quiet'})).toBeVisible()
 fireEvent.change(screen.getByRole('combobox',{name:'Project'}),{target:{value:'quiet/repository'}})
 await waitFor(()=>expect(state.apiFetch).toHaveBeenLastCalledWith('/api/test-performance?workspace=quiet&member=repository'))
})

it('fetches an earlier page without replacing the latest run',async()=>{
 const run=(id:string)=>({workspace_id:'workspace',member_id:'api',run_id:id,commit:null,finished_at:new Date().toISOString(),seconds:10,complete:true,region:null,suites:[{suite:'unit',seconds:10,outcome:'pass',counts:null,slowest:[]}]})
 state.apiFetch.mockImplementation(async(url:string)=>new Response(JSON.stringify({runs:[run(url.includes('cursor=earlier')?'old':'new')],truncated:!url.includes('cursor=earlier'),next_cursor:url.includes('cursor=earlier')?null:'earlier'})))
 render(<TestPerformance/>)
 fireEvent.click(await screen.findByRole('button',{name:'Earlier runs'}))
 await screen.findByText('2 recorded runs · Wall time per suite; parallel suites can overlap.')
 expect(screen.queryByRole('button',{name:'Earlier runs'})).toBeNull()
 const source=JSON.parse(screen.getByTestId('chart').getAttribute('data-source')!) as {data:{values:{commit:string}[]}}
 expect(source.data.values.every(row=>row.commit==='Uncommitted')).toBe(true)
})

it('keeps first-page projects while paging through a bounded scan',async()=>{
 state.apiFetch.mockImplementation(async(url:string)=>new Response(JSON.stringify(url.includes('cursor=')?{runs:[],next_cursor:null}:{runs:[],next_cursor:'scan',scan_limited:true,projects_truncated:true,projects:[{workspace_id:'quiet',member_id:'repository',name:'Repository'}]})))
 render(<TestPerformance/>)
 const more=await screen.findByRole('button',{name:'Load more'})
 expect(screen.queryByText('Give your tests a history')).toBeNull()
 expect(screen.getByText('More runs may follow. Load more to continue checking your history.')).toBeVisible()
 fireEvent.click(more)
 await waitFor(()=>expect(screen.queryByRole('button',{name:'Load more'})).toBeNull())
 expect(screen.getByRole('option',{name:'Repository · quiet'})).toBeVisible()
 expect(screen.getByText('The project inventory is incomplete. Open a workspace to narrow it.')).toBeVisible()
})

it('shows project budgets, overruns and per-suite slowdowns without inventing unknowns',async()=>{
 const now=Date.now()
 const run=(id:string,age:number,seconds:number,budget:number|null)=>({workspace_id:'workspace',member_id:'api',run_id:id,commit:null,finished_at:new Date(now-age*86400000).toISOString(),seconds,complete:true,region:null,suites:[{suite:'unit',seconds,outcome:'pass',counts:null,slowest:[],layer:'2',budget_seconds:budget,flakes:1},{suite:'integration',seconds:5,outcome:'pass',counts:null,slowest:[]}]})
 state.apiFetch.mockResolvedValue(new Response(JSON.stringify({runs:[run('new',0,12,10),run('old',10,8,20)],truncated:false})))
 render(<TestPerformance/>)
 const findings=await screen.findByRole('region',{name:'Suite findings'})
 expect(findings).toHaveTextContent('unit')
 expect(findings).toHaveTextContent('Reported flakes1')
 expect(findings).toHaveTextContent('2.0 s over')
 expect(findings).toHaveTextContent('+50.0%')
 expect(findings).toHaveTextContent('Unknown')
 fireEvent.change(screen.getByRole('combobox',{name:'Suite'}),{target:{value:'integration'}})
 expect(findings).not.toHaveTextContent('unit')
 expect(findings).not.toHaveTextContent('2.0 s over')
})
