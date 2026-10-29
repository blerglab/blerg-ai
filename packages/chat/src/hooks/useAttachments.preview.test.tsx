import {act,renderHook,waitFor} from '@testing-library/react'
import {afterEach,beforeEach,describe,expect,it,vi} from 'vitest'
import {resetKeptAttachments,useAttachments} from './useAttachments'
import type {AttachmentPreview} from '../model/attachmentPreview'
import type {TransportFiles} from '../transport/types'
const preview=vi.hoisted(()=>vi.fn())
vi.mock('../model/attachmentPreview',()=>({createAttachmentPreview:preview}))
const uploaded=(file:File)=>({id:file.name,name:file.name,size:file.size})
function files():TransportFiles{return{list:async()=>[],raw:async()=>new Blob(),download:async()=>{},remove:vi.fn(async()=>{}),upload:vi.fn(async(_id,file)=>uploaded(file))}}
beforeEach(()=>{resetKeptAttachments();preview.mockReset();preview.mockResolvedValue({kind:'text',text:'A local excerpt'})})
afterEach(()=>{resetKeptAttachments();vi.restoreAllMocks()})
describe('attachment preview lifetime',()=>{
 it('keeps completed previews when the same mounted hook switches away and back',async()=>{
  const transport=files();const{result,rerender}=renderHook(({sid})=>useAttachments(sid,transport),{initialProps:{sid:'A'}})
  act(()=>result.current.addFiles([new File(['abc'],'notes.md')]))
  await waitFor(()=>expect(result.current.items[0]).toMatchObject({status:'done',preview:{kind:'text',text:'A local excerpt'}}))
  rerender({sid:'B'});expect(result.current.items).toEqual([])
  act(()=>result.current.addFiles([new File(['def'],'other.md')]))
  await waitFor(()=>expect(result.current.items[0]?.status).toBe('done'))
  rerender({sid:'A'});expect(result.current.items[0]).toMatchObject({name:'notes.md',preview:{text:'A local excerpt'}})
  expect(result.current.done[0]).not.toHaveProperty('preview')
 })
 it('aborts removed and cleared previews and discards late results',async()=>{
  const pending:Array<{signal:AbortSignal;finish:(value:AttachmentPreview)=>void}>=[]
  preview.mockImplementation((_file,signal)=>new Promise(resolve=>pending.push({signal,finish:resolve})))
  const{result}=renderHook(()=>useAttachments('A',files()))
  act(()=>result.current.addFiles([new File(['a'],'one.md'),new File(['b'],'two.md')]))
  await waitFor(()=>expect(result.current.done).toHaveLength(2))
  act(()=>result.current.remove(result.current.items[0]!.key))
  expect(pending[0]!.signal.aborted).toBe(true)
  act(()=>result.current.clear())
  expect(pending[1]!.signal.aborted).toBe(true)
  await act(async()=>{for(const job of pending)job.finish({kind:'text',text:'late'})})
  expect(result.current.items).toEqual([])
 })
 it('does not let the old upload or preview appear in a newly selected session',async()=>{
  let finishUpload:((up:ReturnType<typeof uploaded>)=>void)|undefined
  let finishPreview!:(value:AttachmentPreview)=>void
  let signal!:AbortSignal
  const transport=files();transport.upload=vi.fn(()=>new Promise<ReturnType<typeof uploaded>>(resolve=>{finishUpload=resolve}))
  preview.mockImplementation((_file,abortSignal)=>{signal=abortSignal;return new Promise(resolve=>{finishPreview=resolve})})
  const{result,rerender}=renderHook(({sid})=>useAttachments(sid,transport),{initialProps:{sid:'A'}})
  const file=new File(['abc'],'private-A.md')
  act(()=>result.current.addFiles([file]))
  rerender({sid:'B'});expect(result.current.items).toEqual([]);expect(signal.aborted).toBe(true)
  // Uploads start from the session's queue: one abandoned before it began never reaches the transport.
  await act(async()=>{finishUpload?.(uploaded(file));finishPreview({kind:'text',text:'A content'})})
  expect(result.current.items).toEqual([])
  // Back in A, the cut-off upload waits to be sent again (#231 review, finding 8), without the
  // preview that was abandoned.
  rerender({sid:'A'});expect(result.current.items).toMatchObject([{name:'private-A.md',status:'waiting'}]);expect(result.current.items[0]?.preview).toBeUndefined()
 })
 it('evicts only preview data while preserving unsent attachment metadata across many sessions',async()=>{
  const transport=files();const{result,rerender}=renderHook(({sid})=>useAttachments(sid,transport),{initialProps:{sid:'session-0'}})
  for(let n=0;n<18;n++){
   rerender({sid:`session-${n}`})
   act(()=>result.current.addFiles([new File(['x'],`file-${n}.txt`)]))
   await waitFor(()=>expect(result.current.items[0]?.status).toBe('done'))
  }
  rerender({sid:'session-0'});expect(result.current.items[0]).toMatchObject({name:'file-0.txt',id:'file-0.txt',status:'done'});expect(result.current.items[0]?.preview).toBeUndefined()
  rerender({sid:'session-17'});expect(result.current.items[0]?.name).toBe('file-17.txt')
 })
})
