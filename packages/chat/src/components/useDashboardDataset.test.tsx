import {useLayoutEffect} from 'react'
import {act, render} from '@testing-library/react'
import {afterEach, expect, it, vi} from 'vitest'
import {useDashboardDataset} from './useDashboardDataset'
import type {DashboardCard} from '../visual/dashboard'
import type {TransportFiles} from '../transport/types'
import type {DatasetSample, DatasetRow} from '../visual/datasets'

afterEach(() => {vi.useRealTimers();vi.unstubAllGlobals();vi.restoreAllMocks()})
it('retains an offscreen sample, uses the usage cadence, and purges invalidated authority', () => {
 vi.useFakeTimers();vi.setSystemTime(new Date('2026-11-04T12:10:00Z'))
 let intersect: (entries: {isIntersecting:boolean}[]) => void = () => {}
 vi.stubGlobal('IntersectionObserver', class {
  constructor(callback: typeof intersect) {intersect=callback}
  observe() {} disconnect() {}
 })
 let receive: (sample: DatasetSample & {rows:DatasetRow[]}) => void = () => {}
 let purge = () => {}
 const off=vi.fn()
 const files={subscribeDataset:vi.fn((_s,_d,callback) => {receive=callback;return off}),onDatasetInvalidated:(callback:()=>void)=>{purge=callback;return()=>{}}} as unknown as TransportFiles
 const card:DashboardCard={id:'usage',kind:'gauge',title:'Usage',span:1,data:{kind:'dataset',name:'blerg.engine.usage',parameters:{engine:'codex'}},view:{value:'percent',selection:'latest',min:0,max:100}}
 function View() {
  const {ref,rows,status}=useDashboardDataset(card,{sessionId:'session',files})
  return <article ref={ref}><span>{String(rows[0]?.percent ?? 'Unknown')}</span><p>{status}</p></article>
 }
 const view=render(<View />)
 act(()=>intersect([{isIntersecting:true}]))
 act(()=>receive({type:'dataset_delta',subscription_id:'id',epoch:'epoch',base_sequence:0,sequence:1,snapshot:true,upserts:[],removals:[],observed_at:'2026-11-04T12:01:00Z',status:'ready',rows:[{key:'codex',percent:40}]}))
 expect(view.container).toHaveTextContent('40Live')
 act(()=>intersect([{isIntersecting:false}]))
 expect(off).toHaveBeenCalledOnce()
 expect(view.container).toHaveTextContent('40')
 act(()=>intersect([{isIntersecting:true}]))
 expect(view.container).toHaveTextContent('40')
 act(()=>vi.advanceTimersByTime(3*60_000))
 expect(view.container).toHaveTextContent('Delayed')
 act(()=>receive({type:'dataset_delta',subscription_id:'new',epoch:'new',base_sequence:0,sequence:1,snapshot:true,upserts:[],removals:[],observed_at:'',status:'unavailable',rows:[]}))
 expect(view.container).toHaveTextContent('40Source unavailable')
 act(()=>purge())
 expect(view.container).toHaveTextContent('Unknown')
})

it('purges an offscreen MCP card at its deadline and never restores it from an unavailable sample', () => {
 vi.useFakeTimers();vi.setSystemTime(new Date('2026-11-05T12:00:00Z'))
 let intersect: (entries: {isIntersecting:boolean}[]) => void = () => {}
 vi.stubGlobal('IntersectionObserver', class {
  constructor(callback: typeof intersect) {intersect=callback}
  observe() {} disconnect() {}
 })
 let receive: (sample: DatasetSample & {rows:DatasetRow[]}) => void = () => {}
 const files={subscribeDataset:vi.fn((_s,_d,callback) => {receive=callback;return()=>{}})} as unknown as TransportFiles
 const card:DashboardCard={id:'quote',kind:'stat',title:'Quote',span:1,data:{kind:'dataset',name:'blerg.mcp',parameters:{binding:'00000000-0000-4000-8000-000000000514'}},view:{value:'value',selection:'latest'}}
 function View() {
  const {ref,rows,status}=useDashboardDataset(card,{sessionId:'session',files})
  return <article ref={ref}><span>{String(rows[0]?.value ?? 'Unknown')}</span><p>{status}</p></article>
 }
 const view=render(<View />)
 act(()=>intersect([{isIntersecting:true}]))
 const sample:DatasetSample & {rows:DatasetRow[]}={type:'dataset_delta',subscription_id:'id',epoch:'epoch',base_sequence:0,sequence:1,snapshot:true,upserts:[],removals:[],observed_at:'2026-11-05T12:00:00Z',valid_until:'2026-11-05T12:00:02Z',status:'ready',rows:[{key:'quote',value:185.25}]}
 act(()=>receive(sample))
 expect(view.container).toHaveTextContent('185.25Live')
 act(()=>receive({...sample,sequence:2,status:'unavailable',rows:[]}))
 expect(view.container).not.toHaveTextContent('185.25')
 act(()=>receive({...sample,sequence:3}))
 act(()=>intersect([{isIntersecting:false}]))
 act(()=>vi.advanceTimersByTime(2001))
 expect(view.container).not.toHaveTextContent('185.25')
 expect(view.container).toHaveTextContent('UnknownData expired')
 act(()=>receive({...sample,sequence:2,status:'unavailable',rows:[]}))
 expect(view.container).not.toHaveTextContent('185.25')
})

it.each([
 ['execution_unavailable', 'Gateway unavailable'],
 ['authority_unavailable', 'Authority unavailable'],
 ['attempt_store_unavailable', 'Audit unavailable'],
 ['clock_skew', 'Clock skew'],
 ['connection_needs_sign_in', 'Connection needs sign-in'],
 ['provider_rate_limited', 'Rate limited'],
 ['mapping_failed', 'Invalid sample'],
 ['toString', 'Source unavailable'],
 ['provider-body-canary', 'Source unavailable'],
] as const)('shows safe %s state without changing last-good rows or time', (cause, label) => {
 vi.useFakeTimers(); vi.setSystemTime(new Date('2026-11-05T12:00:00Z'))
 let receive: (sample: DatasetSample & {rows: DatasetRow[]}) => void = () => {}
 let intersect: (entries: {isIntersecting:boolean}[]) => void = () => {}
 vi.stubGlobal('IntersectionObserver', class {constructor(callback: typeof intersect) {intersect=callback} observe() {} disconnect() {}})
 const files = {subscribeDataset:vi.fn((_s,_d,callback) => {receive=callback;return()=>{}})} as unknown as TransportFiles
 const card: DashboardCard = {id:'quote',kind:'stat',title:'Quote',span:1,data:{kind:'dataset',name:'blerg.mcp',parameters:{binding:'00000000-0000-4000-8000-000000000514'}},view:{value:'value',selection:'latest'}}
 function View() {const {ref,rows,status,observed}=useDashboardDataset(card,{sessionId:'session',files});return <article ref={ref}>{rows[0]?.value}<p>{status}</p><time>{observed}</time></article>}
 const view=render(<View />)
 act(()=>intersect([{isIntersecting:true}]))
 act(()=>receive({type:'dataset_delta',subscription_id:'id',epoch:'epoch',base_sequence:1,sequence:2,snapshot:false,upserts:[],removals:[],observed_at:'2026-11-05T12:00:00Z',valid_until:'2026-11-05T13:00:00Z',status:'unavailable',cause,rows:[{key:'quote',value:185.25}]}))
 expect(view.container).toHaveTextContent(label)
 expect(view.container).toHaveTextContent('185.25')
 expect(view.container.querySelector('time')).toHaveTextContent('2026-11-05T12:00:00Z')
})

it.each(['text','chart','sparkline'] as const)('creates no MCP demand for unsupported %s cards', kind => {
 const subscribeDataset=vi.fn()
 let intersect: (entries: {isIntersecting:boolean}[]) => void = () => {}
 vi.stubGlobal('IntersectionObserver', class {constructor(callback: typeof intersect) {intersect=callback} observe() {} disconnect() {}})
 const card: DashboardCard={id:'unsupported',kind:kind==='sparkline'?'stat':kind,title:'Unsupported',span:1,data:{kind:'dataset',name:'blerg.mcp',parameters:{binding:'00000000-0000-4000-8000-000000000514'}},view:{value:'value',selection:'latest',...(kind==='sparkline'?{sparkline:'value'}:{})},...(kind==='chart'?{spec:{mark:'bar'}}:{})}
 function View() {const {ref,status}=useDashboardDataset(card,{sessionId:'session',files:{subscribeDataset} as unknown as TransportFiles});return <article ref={ref}>{status}</article>}
 const view=render(<View />)
 act(()=>intersect([{isIntersecting:true}]))
 expect(view.container).toHaveTextContent('unsupported-live')
 expect(view.container).toHaveTextContent('Use native stat or gauge without sparklines')
 expect(subscribeDataset).not.toHaveBeenCalled()
})

it.each(['binding','session','unsupported'] as const)('never shows retained MCP rows after changing %s', changed => {
 let intersect: (entries: {isIntersecting:boolean}[])=>void=()=>{}
 vi.stubGlobal('IntersectionObserver',class {constructor(callback:typeof intersect){intersect=callback} observe(){} disconnect(){}})
 let receive:(sample:DatasetSample & {rows:DatasetRow[]})=>void=()=>{}
 const files={subscribeDataset:vi.fn((_s,_d,callback)=>{receive=callback;return()=>{}})} as unknown as TransportFiles
 const card:DashboardCard={id:'quote',kind:'stat',title:'Quote',span:1,data:{kind:'dataset',name:'blerg.mcp',parameters:{binding:'00000000-0000-4000-8000-000000000514'}},view:{value:'value',selection:'latest'}}
 function View({sessionId,selected}:{sessionId:string;selected:DashboardCard}){const {ref,rows}=useDashboardDataset(selected,{sessionId,files});return <article ref={ref}>{rows[0]?.value ?? 'Unknown'}</article>}
 vi.useFakeTimers();vi.setSystemTime(new Date('2026-11-05T12:00:00Z'))
 const view=render(<View sessionId="first" selected={card}/>)
 act(()=>intersect([{isIntersecting:true}]))
 const sample:DatasetSample & {rows:DatasetRow[]}={type:'dataset_delta',subscription_id:'id',epoch:'epoch',base_sequence:0,sequence:1,snapshot:true,upserts:[],removals:[],observed_at:'2026-11-05T12:00:00Z',valid_until:'2026-11-05T13:00:00Z',status:'ready',rows:[{key:'quote',value:185.25}]}
 act(()=>receive(sample));expect(view.container).toHaveTextContent('185.25')
 const oldReceive=receive
 const selected:DashboardCard=changed==='binding'?{...card,data:{kind:'dataset',name:'blerg.mcp',parameters:{binding:'00000000-0000-4000-8000-000000000515'}}}:changed==='unsupported'?{...card,view:{value:'value',selection:'latest',sparkline:'value'}}:card
 view.rerender(<View sessionId={changed==='session'?'second':'first'} selected={selected}/>)
 expect(view.container).not.toHaveTextContent('185.25')
 // A callback already queued before unsubscribe must retain its original tag.
 act(()=>oldReceive(sample));expect(view.container).not.toHaveTextContent('185.25')
})

it('demands MCP only while intersecting, visible and outside hidden ancestors',()=>{
 let intersect:(entries:{isIntersecting:boolean}[])=>void=()=>{},mutate=()=>{}
 vi.stubGlobal('IntersectionObserver',class {constructor(callback:typeof intersect){intersect=callback} observe(){} disconnect(){}})
 vi.stubGlobal('MutationObserver',class {constructor(callback:()=>void){mutate=callback} observe(){} disconnect(){}})
 const visibility=vi.spyOn(document,'visibilityState','get').mockReturnValue('visible')
 const off=vi.fn(),subscribe=vi.fn(()=>off)
 const card:DashboardCard={id:'quote',kind:'stat',title:'Quote',span:1,data:{kind:'dataset',name:'blerg.mcp',parameters:{binding:'00000000-0000-4000-8000-000000000514'}},view:{value:'value',selection:'latest'}}
 const files={subscribeDataset:subscribe} as unknown as TransportFiles
 function View(){const {ref}=useDashboardDataset(card,{sessionId:'session',files});return <article ref={ref}/>}
 const view=render(<View/>);expect(subscribe).not.toHaveBeenCalled()
 act(()=>intersect([{isIntersecting:true}]));expect(subscribe).toHaveBeenCalledTimes(1)
 act(()=>{visibility.mockReturnValue('hidden');document.dispatchEvent(new Event('visibilitychange'))});expect(off).toHaveBeenCalledTimes(1)
 act(()=>{visibility.mockReturnValue('visible');document.dispatchEvent(new Event('visibilitychange'))});expect(subscribe).toHaveBeenCalledTimes(2)
 act(()=>{view.container.setAttribute('hidden','');mutate()});expect(off).toHaveBeenCalledTimes(2)
 act(()=>{view.container.removeAttribute('hidden');mutate()});expect(subscribe).toHaveBeenCalledTimes(3)
 act(()=>intersect([{isIntersecting:false}]));expect(off).toHaveBeenCalledTimes(3)
 visibility.mockRestore()
})


it('never paints a previously cached binding after its deadline when selected again',()=>{
 vi.useFakeTimers();vi.setSystemTime(new Date('2026-11-05T12:00:00Z'))
 let intersect:(entries:{isIntersecting:boolean}[])=>void=()=>{}
 vi.stubGlobal('IntersectionObserver',class {constructor(callback:typeof intersect){intersect=callback} observe(){} disconnect(){}})
 let receive:(sample:DatasetSample & {rows:DatasetRow[]})=>void=()=>{}
 const files={subscribeDataset:vi.fn((_s,_d,callback)=>{receive=callback;return()=>{}})} as unknown as TransportFiles
 const first:DashboardCard={id:'quote',kind:'stat',title:'Quote',span:1,data:{kind:'dataset',name:'blerg.mcp',parameters:{binding:'00000000-0000-4000-8000-000000000514'}},view:{value:'value',selection:'latest'}}
 const second:DashboardCard={...first,data:{kind:'dataset',name:'blerg.mcp',parameters:{binding:'00000000-0000-4000-8000-000000000515'}}}
 const painted:unknown[]=[]
 function View({card}:{card:DashboardCard}){const {ref,rows}=useDashboardDataset(card,{sessionId:'session',files});useLayoutEffect(()=>{painted.push(rows[0]?.value)});return <article ref={ref}>{rows[0]?.value ?? 'Unknown'}</article>}
 const view=render(<View card={first}/>);act(()=>intersect([{isIntersecting:true}]))
 act(()=>receive({type:'dataset_delta',subscription_id:'id',epoch:'epoch',base_sequence:0,sequence:1,snapshot:true,upserts:[],removals:[],observed_at:'2026-11-05T12:00:00Z',valid_until:'2026-11-05T12:00:02Z',status:'ready',rows:[{key:'quote',value:185.25}]}))
 expect(view.container).toHaveTextContent('185.25')
 view.rerender(<View card={second}/>);painted.length=0
 // The old sample timer was canceled by the binding change; the 15-second
 // display-cadence timer is not a substitute for checking time during render.
 vi.setSystemTime(new Date('2026-11-05T12:00:03Z'))
 view.rerender(<View card={first}/>)
 expect(painted).not.toContain(185.25)
})


it.each(['binding','session'] as const)('cannot carry prior private rows through an unavailable frame after changing %s',changed=>{
 vi.useFakeTimers();vi.setSystemTime(new Date('2026-11-05T12:00:00Z'))
 let intersect:(entries:{isIntersecting:boolean}[])=>void=()=>{}
 vi.stubGlobal('IntersectionObserver',class {constructor(callback:typeof intersect){intersect=callback} observe(){} disconnect(){}})
 let receive:(sample:DatasetSample & {rows:DatasetRow[]})=>void=()=>{}
 const files={subscribeDataset:vi.fn((_s,_d,callback)=>{receive=callback;return()=>{}})} as unknown as TransportFiles
 const usage={kind:'dataset',name:'blerg.engine.usage',parameters:{engine:'codex'}} as const
 const card:DashboardCard={id:'quote',kind:'stat',title:'Quote',span:1,data:changed==='binding'?{kind:'dataset',name:'blerg.mcp',parameters:{binding:'00000000-0000-4000-8000-000000000514'}}:usage,view:{value:'value',selection:'latest'}}
 function View({selected,sessionId}:{selected:DashboardCard;sessionId:string}){const {ref,rows}=useDashboardDataset(selected,{sessionId,files});return <article ref={ref}>{rows[0]?.value ?? 'Unknown'}</article>}
 const view=render(<View selected={card} sessionId="first"/>);act(()=>intersect([{isIntersecting:true}]))
 const sample:DatasetSample & {rows:DatasetRow[]}={type:'dataset_delta',subscription_id:'id',epoch:'epoch',base_sequence:0,sequence:1,snapshot:true,upserts:[],removals:[],observed_at:'2026-11-05T12:00:00Z',valid_until:'2026-11-05T13:00:00Z',status:'ready',rows:[{key:'quote',value:185.25}]}
 act(()=>receive(sample));expect(view.container).toHaveTextContent('185.25')
 view.rerender(<View selected={{...card,data:usage}} sessionId={changed==='session'?'second':'first'}/>);act(()=>intersect([{isIntersecting:true}]))
 act(()=>receive({...sample,sequence:2,status:'unavailable',rows:[]}))
 expect(view.container).not.toHaveTextContent('185.25')
})
