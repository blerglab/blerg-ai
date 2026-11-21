import { act, cleanup, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { loadMoreInbox, refreshInbox, useInbox } from './useInbox'
vi.mock('../ws',()=>import('../test/wsMock'))
vi.mock('../authClient',()=>({getAccessToken:()=>`x.${btoa(JSON.stringify({sub:'race-person'}))}.x`}))
const apiFetch=vi.hoisted(()=>vi.fn())
vi.mock('../apiFetch',()=>({apiFetch}))
function deferred<T>() {
 let resolve!: (value:T)=>void
 const promise=new Promise<T>(done=>{resolve=done})
 return {promise,resolve}
}
afterEach(cleanup)
const item=(id:string)=>({id,session_id:'s',subject:id,kind:'summary',created_at:'2026-11-05T12:00:00Z',read:false})
beforeEach(async()=>{apiFetch.mockReset().mockImplementation(()=>Promise.resolve(Response.json({items:[item('first')],unread:2,next:'older'})));await refreshInbox()})
it('discards an older page when refresh supersedes it and releases loading',async()=>{
 const view=renderHook(()=>useInbox());await act(async()=>{await refreshInbox()})
 const page=deferred<Response>()
 apiFetch.mockImplementation((url:string)=>url.includes('?before=')?page.promise:Promise.resolve(Response.json({items:[item('new')],unread:9,next:''})))
 let pending:Promise<void>;act(()=>{pending=loadMoreInbox()})
 await act(async()=>{await refreshInbox()})
 await act(async()=>{page.resolve(Response.json({items:[item('stale')],unread:1,next:''}));await pending!})
 expect(view.result.current.items.map(i=>i.id)).toEqual(['new']);expect(view.result.current.unread).toBe(9);expect(view.result.current.loading).toBe(false)
})
it('discards a stale refresh when a newer page load owns the revision',async()=>{
 const view=renderHook(()=>useInbox());await act(async()=>{await refreshInbox()})
 const fresh=deferred<Response>()
 apiFetch.mockImplementation((url:string)=>url.includes('?before=')?Promise.resolve(Response.json({items:[item('older')],unread:2,next:''})):fresh.promise)
 let pending:Promise<void>;act(()=>{pending=refreshInbox()})
 await act(async()=>{await loadMoreInbox()})
 await act(async()=>{fresh.resolve(Response.json({items:[item('stale')],unread:99,next:''}));await pending!})
 expect(view.result.current.items.map(i=>i.id)).toEqual(['first','older']);expect(view.result.current.unread).toBe(2);expect(view.result.current.loading).toBe(false)
})
it('releases loading after a page request fails',async()=>{
 const view=renderHook(()=>useInbox());await act(async()=>{await refreshInbox()})
 apiFetch.mockImplementation((url:string)=>url.includes('?before=')?Promise.reject(new TypeError('network unavailable')):Promise.resolve(Response.json({items:[item('first')],unread:2,next:'older'})))
 await act(async()=>{await loadMoreInbox()})
 expect(view.result.current.loading).toBe(false)
 expect(view.result.current.error).toContain('Couldn’t load older items')
})

it('keeps pending deletions hidden across refresh, commits after five seconds, and removes arrival cards',async()=>{
 const {deleteInboxItem,useInboxEvents}=await import('./useInbox')
 apiFetch.mockImplementation(async()=>Response.json({items:[item('committed')],unread:1,next:''}))
 const view=renderHook(()=>({inbox:useInbox(),events:useInboxEvents('s')}))
 await act(async()=>{await refreshInbox()})
 vi.useFakeTimers()
 try {
  act(()=>deleteInboxItem('committed'))
  expect(view.result.current.inbox.items).toEqual([])
  expect(view.result.current.events).toEqual([])
  await act(async()=>{await refreshInbox()})
  expect(view.result.current.inbox.items).toEqual([])
  apiFetch.mockImplementation(async(_url:string,init?:RequestInit)=>{
   if(init?.method==='DELETE')return Response.json({deleted:true})
   return Response.json({items:[],unread:0,next:''})
  })
  await act(async()=>{await vi.advanceTimersByTimeAsync(5000)})
  expect(apiFetch).toHaveBeenCalledWith('/api/inbox/committed',{method:'DELETE'})
  expect(view.result.current.inbox.items).toEqual([])
 }finally{vi.useRealTimers()}
})
it('restores the item and reports a failed delete',async()=>{
 const {deleteInboxItem}=await import('./useInbox')
 const {useToastStore}=await import('./useToastStore')
 apiFetch.mockImplementation(async()=>Response.json({items:[item('first')],unread:1,next:''}))
 const view=renderHook(()=>useInbox())
 await act(async()=>{await refreshInbox()})
 vi.useFakeTimers()
 try{
  act(()=>deleteInboxItem('first'))
  apiFetch.mockImplementation(async()=>new Response('{}',{status:500}))
  await act(async()=>{await vi.advanceTimersByTimeAsync(5000)})
  expect(view.result.current.items.map(i=>i.id)).toEqual(['first'])
  expect(useToastStore.getState().toasts.some(t=>t.title==='Couldn’t delete item')).toBe(true)
 }finally{vi.useRealTimers()}
})

it('scopes requests and unread counts to each workspace, including mutations', async()=>{
 const {setInboxRead, deleteInboxItem}=await import('./useInbox')
 apiFetch.mockImplementation(async(url:string)=>Response.json({items:[item(url.includes('workspace=second')?'second':'first')],unread:url.includes('workspace=second')?7:1,next:''}))
 const first=renderHook(()=>useInbox('first'))
 const second=renderHook(()=>useInbox('second'))
 await act(async()=>{await refreshInbox('first');await refreshInbox('second')})
 expect(first.result.current.items.map(i=>i.id)).toEqual(['first'])
 expect(second.result.current.items.map(i=>i.id)).toEqual(['second'])
 expect(first.result.current.unread).toBe(1)
 expect(second.result.current.unread).toBe(7)
 await act(async()=>{await setInboxRead('first',true,'first')})
 expect(apiFetch).toHaveBeenCalledWith('/api/inbox/first/read?workspace=first',expect.objectContaining({method:'PUT'}))
 vi.useFakeTimers()
 try {
  act(()=>deleteInboxItem('first','first'))
  await act(async()=>{await vi.advanceTimersByTimeAsync(5000)})
  expect(apiFetch).toHaveBeenCalledWith('/api/inbox/first?workspace=first',{method:'DELETE'})
 }finally{vi.useRealTimers()}
})

it('hides the previous workspace immediately and ignores its late response',async()=>{
 const view=renderHook(({workspace})=>useInbox(workspace),{initialProps:{workspace:'old-workspace'}})
 await act(async()=>{await refreshInbox('old-workspace')})
 const late=deferred<Response>()
 apiFetch.mockImplementation((url:string)=>url.includes('workspace=old-workspace')?late.promise:Promise.resolve(Response.json({items:[item('new-workspace')],unread:1,next:''})))
 let pending:Promise<void>
 act(()=>{pending=refreshInbox('old-workspace')})
 view.rerender({workspace:'new-workspace'})
 expect(view.result.current.items).toEqual([])
 await act(async()=>{await refreshInbox('new-workspace')})
 await act(async()=>{late.resolve(Response.json({items:[item('late-foreign')],unread:99,next:''}));await pending!})
 expect(view.result.current.items.map(i=>i.id)).toEqual(['new-workspace'])
 expect(view.result.current.unread).toBe(1)
})
