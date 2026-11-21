import {expect, it, vi} from 'vitest'
import {subscribeDataset} from './datasets'
import type {BlergSocket} from './blerg'
import type {DatasetSample} from '../visual/datasets'

it('reconciles keyed deltas, requests a snapshot on gaps, and purges revoked data', () => {
  vi.useFakeTimers()
  let handler: (sample: never) => void = () => {}
  let close = () => {}
  const send = vi.fn<BlergSocket['send']>(() => true), receive = vi.fn()
  const socket: BlergSocket = {
    send, onMessage: (_, fn) => {handler = fn; return () => {}},
    onOpen: fn => {fn(); return () => {}}, onClose: fn => {close = fn; return () => {}},
  }
  const off = subscribeDataset(socket, 'session', {kind: 'dataset', name: 'blerg.workers', parameters: {}}, receive)
  const id = send.mock.calls.find(([command]) => command.type === 'subscribe_dataset')?.[0] as unknown as {subscription_id: string}
  const sample = {type: 'dataset_delta', subscription_id: id.subscription_id, epoch: 'epoch', snapshot: true, base_sequence: 0, sequence: 1, upserts: [{key: 'workers', running: 1}], removals: [], observed_at: '2026-11-04T12:00:00Z', status: 'ready'} as DatasetSample
  const emit = (patch: Partial<DatasetSample>) => handler({...sample, ...patch} as never)
  emit({})
  expect(receive.mock.lastCall?.[0].rows).toEqual([{key: 'workers', running: 1}])
  emit({snapshot: false, base_sequence: 1, sequence: 2, upserts: [{key: 'workers', running: 3}]})
  expect(receive.mock.lastCall?.[0].rows).toEqual([{key: 'workers', running: 3}])
  emit({snapshot: false, base_sequence: 2, sequence: 3, status: 'unavailable', upserts: [], removals: []})
  expect(receive.mock.lastCall?.[0].rows).toEqual([{key: 'workers', running: 3}])
  const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
  emit({snapshot: false, base_sequence: 4, sequence: 5})
  expect(send.mock.lastCall?.[0]).toMatchObject({type: 'subscribe_dataset'})
  expect(receive.mock.lastCall?.[0].rows[0].running).toBe(3)
  close()
  expect(receive.mock.lastCall?.[0].status).toBe('disconnected')
  emit({status: 'access_removed'})
  expect(receive.mock.lastCall?.[0].rows).toEqual([])
  off()
  expect(send.mock.lastCall?.[0]).toMatchObject({type: 'unsubscribe_dataset'})
  expect(vi.getTimerCount()).toBe(0)
  warn.mockRestore(); vi.useRealTimers()
})

it.each([
  ['blerg.gates.durations', {suite: 'total'}, {key: 'run:total', seconds: 0, suite: 'total', outcome: 'pass'}],
  ['blerg.cards.time', {board: 'board-a', card: '7'}, {key: 'board-a:7', seconds: 0}],
  ['blerg.reviews.pairs', {board: 'board-a', card: '7'}, {key: 'unknown:codex', author_engine: 'unknown', reviewer_engine: 'codex', reviews: 1, high: 0}],
  ['blerg.gates.bins', {width: '60'}, {key: '0', start: 0, end: 60, count: 1}],
  ['blerg.cards.merged.heatmap', {bucket: 'hour', timezone: 'UTC'}, {key: '2026-11-01:00', day: '2026-11-01', hour: 0, count: 0}],
  ['blerg.workers.timeline', {}, {key: 'worker:start', worker: 'worker', start: '2026-11-01T00:00:00Z', end: '2026-11-01T00:01:00Z', status: 'unknown'}],
] as const)('preserves %s rows through a socket fake and removes corrected keys', (name, parameters, row) => {
  vi.useFakeTimers()
  let handler: (sample: never) => void = () => {}
  const send = vi.fn<BlergSocket['send']>(() => true), receive = vi.fn()
  const socket: BlergSocket = {
    send, onMessage: (_, fn) => {handler = fn; return () => {}},
    onOpen: fn => {fn(); return () => {}}, onClose: () => () => {},
  }
  const off = subscribeDataset(socket, 'session', {kind: 'dataset', name, parameters}, receive)
  try {
    const command = send.mock.calls.find(([command]) => command.type === 'subscribe_dataset')?.[0] as unknown as {subscription_id: string}
    expect(send.mock.calls.find(([command]) => command.type === 'subscribe_dataset')?.[0]).toMatchObject({data: {name, parameters}})
    const sample: DatasetSample = {type: 'dataset_delta', subscription_id: command.subscription_id, epoch: 'epoch', snapshot: true, base_sequence: 0, sequence: 1, upserts: [row], removals: [], observed_at: '2026-11-01T00:01:00Z', status: 'ready'}
    handler(JSON.parse(JSON.stringify(sample)) as never)
    expect(receive.mock.lastCall?.[0].rows).toEqual([row])
    handler({...sample, snapshot: false, base_sequence: 1, sequence: 2, upserts: [], removals: [row.key]} as never)
    expect(receive.mock.lastCall?.[0].rows).toEqual([])
  } finally {off(); vi.useRealTimers()}
})

it('orders hashed gate keys by end time for latest scalar selection', () => {
  vi.useFakeTimers()
  let handler: (sample: never) => void = () => {}
  const send = vi.fn<BlergSocket['send']>(() => true), receive = vi.fn()
  const socket: BlergSocket = {send, onMessage: (_, fn) => {handler = fn; return () => {}}, onOpen: fn => {fn(); return () => {}}}
  const off = subscribeDataset(socket, 'session', {kind: 'dataset', name: 'blerg.gates.durations', parameters: {suite: 'total'}}, receive)
  try {
    const id = send.mock.calls.find(([command]) => command.type === 'subscribe_dataset')?.[0] as unknown as {subscription_id: string}
    handler({type: 'dataset_delta', subscription_id: id.subscription_id, epoch: 'epoch', snapshot: true, base_sequence: 0, sequence: 1,
      upserts: [{key: 'aaaa', end: '2026-11-02T00:00:00Z', seconds: 2}, {key: 'zzzz', end: '2026-11-01T00:00:00Z', seconds: 1}], removals: [], observed_at: '2026-11-02T00:00:00Z', status: 'ready'} as never)
    expect(receive.mock.lastCall?.[0].rows.map((row: {seconds: number}) => row.seconds)).toEqual([1, 2])
  } finally {off(); vi.useRealTimers()}
})

it('expires an MCP sample while disconnected without accepting a late heartbeat', () => {
  vi.useFakeTimers(); vi.setSystemTime(new Date('2026-11-05T12:00:00Z'))
  let handler: (sample: never) => void = () => {}, close = () => {}
  const send = vi.fn<BlergSocket['send']>(() => true), receive = vi.fn()
  const socket: BlergSocket = {send, onMessage: (_, fn) => {handler = fn; return () => {}}, onOpen: fn => {fn(); return () => {}}, onClose: fn => {close = fn; return () => {}}}
  const off = subscribeDataset(socket, 'session', {kind: 'dataset', name: 'blerg.mcp', parameters: {binding: '00000000-0000-4000-8000-000000000514'}}, receive)
  try {
    const id = send.mock.calls.find(([command]) => command.type === 'subscribe_dataset')?.[0] as unknown as {subscription_id: string}
    const sample = {type: 'dataset_delta', subscription_id: id.subscription_id, epoch: 'epoch', snapshot: true, base_sequence: 0, sequence: 1,
      upserts: [{key: 'quote', value: 185.25}], removals: [], observed_at: '2026-11-05T12:00:00Z', valid_until: '2026-11-05T12:00:02Z', status: 'ready'}
    handler(sample as never)
    expect(receive.mock.lastCall?.[0].rows).toHaveLength(1)
    close()
    expect(receive.mock.lastCall?.[0]).toMatchObject({status: 'disconnected', rows: [{key: 'quote', value: 185.25}]})
    vi.advanceTimersByTime(2001)
    expect(receive.mock.lastCall?.[0]).toMatchObject({status: 'access_removed', cause: 'sample_expired', rows: []})
    handler({...sample, status: 'unavailable', snapshot: false, base_sequence: 1, sequence: 2, upserts: []} as never)
    expect(receive.mock.lastCall?.[0].rows).toEqual([])
  } finally {off(); vi.useRealTimers()}
})

it('refuses an MCP sample without an authority deadline', () => {
  let handler: (sample: never) => void = () => {}
  const send = vi.fn<BlergSocket['send']>(() => true), receive = vi.fn()
  const socket: BlergSocket = {send, onMessage: (_, fn) => {handler = fn; return () => {}}, onOpen: fn => {fn(); return () => {}}}
  const off = subscribeDataset(socket, 'session', {kind: 'dataset', name: 'blerg.mcp', parameters: {binding: '00000000-0000-4000-8000-000000000514'}}, receive)
  try {
    const id = send.mock.calls.find(([command]) => command.type === 'subscribe_dataset')?.[0] as unknown as {subscription_id: string}
    handler({type: 'dataset_delta', subscription_id: id.subscription_id, epoch: 'epoch', snapshot: true, base_sequence: 0, sequence: 1,
      upserts: [{key: 'quote', value: 185.25}], removals: [], observed_at: new Date().toISOString(), status: 'ready'} as never)
    expect(receive.mock.lastCall?.[0].rows).toEqual([])
  } finally {off()}
})

it('caps browser retention at 24 hours even when an authority deadline is later', () => {
 vi.useFakeTimers();vi.setSystemTime(new Date('2026-11-05T12:00:00Z'))
 let handler: (sample:never)=>void=()=>{}
 const send=vi.fn<BlergSocket['send']>(()=>true),receive=vi.fn()
 const socket:BlergSocket={send,onMessage:(_,fn)=>{handler=fn;return()=>{}},onOpen:fn=>{fn();return()=>{}}}
 const off=subscribeDataset(socket,'session',{kind:'dataset',name:'blerg.mcp',parameters:{binding:'00000000-0000-4000-8000-000000000514'}},receive)
 try {
  const id=send.mock.calls.find(([command]) => command.type === 'subscribe_dataset')?.[0] as unknown as {subscription_id:string}
  handler({type:'dataset_delta',subscription_id:id.subscription_id,epoch:'epoch',snapshot:true,base_sequence:0,sequence:1,upserts:[{key:'quote',value:185.25}],removals:[],observed_at:'2026-11-05T12:00:00Z',valid_until:'2026-11-07T12:00:00Z',status:'ready'} as never)
  vi.advanceTimersByTime(24*60*60_000+1)
  expect(receive.mock.lastCall?.[0].rows).toEqual([])
 }finally{off();vi.useRealTimers()}
})

it('recovers MCP gaps and reconnects with a fresh subscription and rejects obsolete frames', () => {
  vi.useFakeTimers(); vi.setSystemTime(new Date('2026-11-05T12:00:00Z'))
  let handler: (sample: never) => void = () => {}, open = () => {}
  const send = vi.fn<BlergSocket['send']>(() => true), receive = vi.fn()
  const socket: BlergSocket = {send, onMessage: (_, fn) => {handler = fn; return () => {}}, onOpen: fn => {open = fn; fn(); return () => {}}}
  const off = subscribeDataset(socket, 'session', {kind: 'dataset', name: 'blerg.mcp', parameters: {binding: '00000000-0000-4000-8000-000000000514'}}, receive)
  const currentId = () => (send.mock.calls.filter(([command]) => command.type === 'subscribe_dataset').at(-1)![0] as unknown as {subscription_id: string}).subscription_id
  const emit = (subscription_id: string, epoch: string, sequence: number, snapshot = true, value = 185.25) => handler({type: 'dataset_delta', subscription_id, epoch, sequence, base_sequence: sequence - 1, snapshot, upserts: [{key: 'quote', value}], removals: [], observed_at: '2026-11-05T12:00:00Z', valid_until: '2026-11-05T13:00:00Z', status: 'ready'} as never)
  try {
    const first = currentId()
    emit(first, 'old', 1)
    emit(first, 'old', 3, false)
    const recovered = currentId()
    expect(recovered).not.toBe(first)
    expect(send.mock.calls.some(([command]) => command.type === 'unsubscribe_dataset' && command.subscription_id === first)).toBe(true)
    emit(recovered, 'new', 1, true, 189.75)
    emit(first, 'old', 4, true, 1)
    emit(recovered, 'new', 1, false, 2)
    expect(receive.mock.lastCall?.[0].rows).toEqual([{key: 'quote', value: 189.75}])
    emit(recovered, 'obsolete-epoch', 2, true, 666)
    expect(receive.mock.lastCall?.[0].rows).toEqual([{key: 'quote', value: 189.75}])
    open()
    const reconnected = currentId()
    expect(reconnected).not.toBe(recovered)
    emit(recovered, 'new', 5, true, 3)
    emit(reconnected, 'reconnect', 1, true, 190)
    expect(receive.mock.lastCall?.[0].rows).toEqual([{key: 'quote', value: 190}])
    handler({type: 'dataset_delta', subscription_id: reconnected, epoch: 'reconnect', sequence: 2, base_sequence: 1, snapshot: true, upserts: [], removals: [], status: 'access_removed'} as never)
    emit(reconnected, 'reconnect', 1)
    expect(receive.mock.lastCall?.[0].rows).toEqual([])
  } finally {off(); vi.useRealTimers()}
})

it('purges poisoned access-removed frames even when they carry a valid future payload',()=>{
 vi.useFakeTimers();vi.setSystemTime(new Date('2026-11-05T12:00:00Z'))
 let handler:(sample:never)=>void=()=>{}
 const send=vi.fn<BlergSocket['send']>(()=>true),receive=vi.fn()
 const socket:BlergSocket={send,onMessage:(_,fn)=>{handler=fn;return()=>{}},onOpen:fn=>{fn();return()=>{}}}
 const off=subscribeDataset(socket,'session',{kind:'dataset',name:'blerg.mcp',parameters:{binding:'00000000-0000-4000-8000-000000000514'}},receive)
 try{
 const id=(send.mock.calls.find(([command])=>command.type==='subscribe_dataset')![0] as unknown as {subscription_id:string}).subscription_id
 const sample={type:'dataset_delta',subscription_id:id,epoch:'epoch',snapshot:true,base_sequence:0,sequence:1,upserts:[{key:'quote',value:185.25}],removals:[],observed_at:'2026-11-05T12:00:00Z',valid_until:'2026-11-05T13:00:00Z',status:'ready'}
 handler(sample as never)
 handler({...sample,sequence:2,status:'access_removed'} as never)
 expect(receive.mock.lastCall?.[0].rows).toEqual([])
 handler({...sample,sequence:3,base_sequence:2,snapshot:false,status:'unavailable',upserts:[]} as never)
 expect(receive.mock.lastCall?.[0].rows).toEqual([])
 }finally{off();vi.useRealTimers()}
})


it('does not resurrect expired keys when a later MCP delta arrives',()=>{
 vi.useFakeTimers();vi.setSystemTime(new Date('2026-11-05T12:00:00Z'))
 let handler:(sample:never)=>void=()=>{}
 const send=vi.fn<BlergSocket['send']>(()=>true),receive=vi.fn()
 const socket:BlergSocket={send,onMessage:(_,fn)=>{handler=fn;return()=>{}},onOpen:fn=>{fn();return()=>{}}}
 const off=subscribeDataset(socket,'session',{kind:'dataset',name:'blerg.mcp',parameters:{binding:'00000000-0000-4000-8000-000000000514'}},receive)
 try{
 const id=(send.mock.calls.find(([command])=>command.type==='subscribe_dataset')![0] as unknown as {subscription_id:string}).subscription_id
 const sample={type:'dataset_delta',subscription_id:id,epoch:'epoch',snapshot:true,base_sequence:0,sequence:1,upserts:[{key:'old',value:185.25}],removals:[],observed_at:'2026-11-05T12:00:00Z',valid_until:'2026-11-05T12:00:02Z',status:'ready'}
 handler(sample as never);vi.advanceTimersByTime(2001);expect(receive.mock.lastCall?.[0].rows).toEqual([])
 handler({...sample,snapshot:false,base_sequence:1,sequence:2,upserts:[{key:'new',value:189.75}],observed_at:'2026-11-05T12:00:02Z',valid_until:'2026-11-05T13:00:00Z'} as never)
 expect(receive.mock.lastCall?.[0].rows).toEqual([{key:'new',value:189.75}])
 }finally{off();vi.useRealTimers()}
})

it('refuses callbacks already queued when an MCP subscription is retired',()=>{
 vi.useFakeTimers();vi.setSystemTime(new Date('2026-11-05T12:00:00Z'))
 let handler:(sample:never)=>void=()=>{}
 const send=vi.fn<BlergSocket['send']>(()=>true),receive=vi.fn()
 const socket:BlergSocket={send,onMessage:(_,fn)=>{handler=fn;return()=>{}},onOpen:fn=>{fn();return()=>{}}}
 const off=subscribeDataset(socket,'session',{kind:'dataset',name:'blerg.mcp',parameters:{binding:'00000000-0000-4000-8000-000000000514'}},receive)
 try{
 const id=(send.mock.calls.find(([command])=>command.type==='subscribe_dataset')![0] as unknown as {subscription_id:string}).subscription_id
 const sample={type:'dataset_delta',subscription_id:id,epoch:'epoch',snapshot:true,base_sequence:0,sequence:1,upserts:[{key:'quote',value:185.25}],removals:[],observed_at:'2026-11-05T12:00:00Z',valid_until:'2026-11-05T13:00:00Z',status:'ready'}
 handler(sample as never);off();const count=receive.mock.calls.length
 handler({...sample,sequence:2} as never)
 expect(receive).toHaveBeenCalledTimes(count)
 expect(vi.getTimerCount()).toBe(0)
 }finally{off();vi.useRealTimers()}
})


it.each([
 ['fractional sequence',{sequence:2.5}],
 ['unsafe sequence',{sequence:Number.MAX_SAFE_INTEGER+1}],
 ['nonfinite value',{upserts:[{key:'bad',value:Infinity}]}],
 ['long value',{upserts:[{key:'bad',value:'x'.repeat(257)}]}],
 ['long key',{upserts:[{key:'x'.repeat(129),value:1}]}],
 ['long removal',{removals:['x'.repeat(129)]}],
 ['too many fields',{upserts:[{key:'bad',...Object.fromEntries(Array.from({length:16},(_,i)=>['v'+i,i]))}]}],
 ['too many rows',{upserts:Array.from({length:10001},(_,i)=>({key:'v'+i,value:i}))}],
] as const)('refuses the entire %s frame and retains the authorized good sample',(_name,patch)=>{
 vi.useFakeTimers();vi.setSystemTime(new Date('2026-11-05T12:00:00Z'))
 let handler:(sample:never)=>void=()=>{}
 const send=vi.fn<BlergSocket['send']>(()=>true),receive=vi.fn()
 const socket:BlergSocket={send,onMessage:(_,fn)=>{handler=fn;return()=>{}},onOpen:fn=>{fn();return()=>{}}}
 const off=subscribeDataset(socket,'session',{kind:'dataset',name:'blerg.mcp',parameters:{binding:'00000000-0000-4000-8000-000000000514'}},receive)
 try{
 const id=(send.mock.calls.find(([command])=>command.type==='subscribe_dataset')![0] as unknown as {subscription_id:string}).subscription_id
 const sample={type:'dataset_delta',subscription_id:id,epoch:'epoch',snapshot:true,base_sequence:0,sequence:1,upserts:[{key:'quote',value:185.25}],removals:[],observed_at:'2026-11-05T12:00:00Z',valid_until:'2026-11-05T13:00:00Z',status:'ready'}
 handler(sample as never)
 handler({...sample,snapshot:false,base_sequence:1,sequence:2,upserts:[{key:'fresh',value:189.75}],...patch} as never)
 expect(receive).toHaveBeenCalledTimes(1)
 expect(receive.mock.lastCall?.[0].rows).toEqual([{key:'quote',value:185.25}])
 }finally{off();vi.useRealTimers()}
})

it('bounds the accumulated keyed store independently of each delta size',()=>{
 vi.useFakeTimers();vi.setSystemTime(new Date('2026-11-05T12:00:00Z'))
 let handler:(sample:never)=>void=()=>{}
 const send=vi.fn<BlergSocket['send']>(()=>true),receive=vi.fn()
 const socket:BlergSocket={send,onMessage:(_,fn)=>{handler=fn;return()=>{}},onOpen:fn=>{fn();return()=>{}}}
 const off=subscribeDataset(socket,'session',{kind:'dataset',name:'blerg.mcp',parameters:{binding:'00000000-0000-4000-8000-000000000514'}},receive)
 try{
 const id=(send.mock.calls.find(([command])=>command.type==='subscribe_dataset')![0] as unknown as {subscription_id:string}).subscription_id
 const sample={type:'dataset_delta',subscription_id:id,epoch:'epoch',snapshot:true,base_sequence:0,sequence:1,upserts:[{key:'quote',value:185.25}],removals:[],observed_at:'2026-11-05T12:00:00Z',valid_until:'2026-11-05T13:00:00Z',status:'ready'}
 handler(sample as never)
 handler({...sample,snapshot:false,base_sequence:1,sequence:2,upserts:Array.from({length:10000},(_,i)=>({key:'v'+i,value:i}))} as never)
 expect(receive).toHaveBeenCalledTimes(1)
 expect(receive.mock.lastCall?.[0].rows).toEqual([{key:'quote',value:185.25}])
 }finally{off();vi.useRealTimers()}
})

it('preserves the safe unavailable cause before any authorized good sample exists',()=>{
 vi.useFakeTimers();vi.setSystemTime(new Date('2026-11-05T12:00:00Z'))
 let handler:(sample:never)=>void=()=>{}
 const send=vi.fn<BlergSocket['send']>(()=>true),receive=vi.fn()
 const socket:BlergSocket={send,onMessage:(_,fn)=>{handler=fn;return()=>{}},onOpen:fn=>{fn();return()=>{}}}
 const off=subscribeDataset(socket,'session',{kind:'dataset',name:'blerg.mcp',parameters:{binding:'00000000-0000-4000-8000-000000000514'}},receive)
 try{
 const id=(send.mock.calls.find(([command])=>command.type==='subscribe_dataset')![0] as unknown as {subscription_id:string}).subscription_id
 handler({type:'dataset_delta',subscription_id:id,epoch:'epoch',snapshot:true,base_sequence:0,sequence:1,upserts:[],removals:[],observed_at:'',status:'unavailable',cause:'gateway_unavailable'} as never)
 expect(receive.mock.lastCall?.[0]).toMatchObject({status:'unavailable',cause:'gateway_unavailable',rows:[]})
 }finally{off();vi.useRealTimers()}
})
