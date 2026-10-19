import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import ReviewMode from './ReviewMode'
import { ChatContext, type ChatContextValue } from '../context'
import type { Anchor, ReviewFile } from '../../model/review'

vi.mock('../DiffView', () => ({default: ({onSelection}: {onSelection?: (anchor: Anchor) => void}) => <div>
  {[1, 2].map(line => <button key={line} onClick={() => onSelection?.({kind:'text',quote:`code ${line}`,diff:{file:'file.go',side:'new',start:line,end:line,hunk:1,fileIndex:1}})}>Select fixture {line}</button>)}
</div>}))
const artifact = {id:'diff1',name:'change.diff',size:100,content_type:'text/x-diff',view:'diff',version:1}
function show(reviewFor = async (): Promise<ReviewFile | null> => null, submitFeedback = vi.fn(async (_text: string, _files: File[]) => true)) {
  const ctx = {sessionId:'s1',files:null,view:vi.fn(),latestVersion:()=>undefined,byName:()=>undefined,reviewFor,submitFeedback} as ChatContextValue
  render(<ChatContext.Provider value={ctx}><ReviewMode artifact={artifact} text="diff source" onClose={()=>{}} /></ChatContext.Provider>)
  return submitFeedback
}
function request(line: number, text: string) {
  fireEvent.click(screen.getByRole('button',{name:`Select fixture ${line}`}))
  fireEvent.click(screen.getByRole('button',{name:'Request change'}))
  fireEvent.change(screen.getByRole('textbox',{name:'Request'}),{target:{value:text}})
  fireEvent.click(screen.getByRole('button',{name:'Save'}))
}
function read(file: File): Promise<string> {
 return new Promise((resolve,reject)=>{const r=new FileReader();r.onload=()=>resolve(String(r.result));r.onerror=()=>reject(r.error);r.readAsText(file)})
}
describe('diff review uses the existing feedback path',()=>{
 it('collects multiple requests into one version-1 review upload and one message',async()=>{
  const send=show()
  expect(screen.queryByRole('textbox',{name:'Source'})).toBeNull()
  request(1,'Fix first');request(2,'Fix second')
  fireEvent.click(screen.getByRole('button',{name:'Submit'}))
  await waitFor(()=>expect(send).toHaveBeenCalledTimes(1))
  const [message,files]=send.mock.calls[0]
  expect(message).toContain('2 requests')
  expect(message).toContain('file.go')
  expect(files).toHaveLength(1)
  expect(files[0].name).toBe('change.diff.review.json')
  const review=JSON.parse(await read(files[0])) as ReviewFile
  expect(review.version).toBe(1)
  expect(review.requests).toHaveLength(2)
  expect(review.requests[0].anchor.diff).toMatchObject({file:'file.go',start:1,end:1,artifactId:'diff1'})
  await waitFor(()=>expect(screen.getByRole('button',{name:'Submit'})).toBeDisabled())
 })
 it('retains an unsent request when the existing review arrives late',async()=>{
  let finish!: (review:ReviewFile|null)=>void
  show(()=>new Promise(resolve=>{finish=resolve}))
  request(1,'Keep local')
  await act(async()=>{finish({version:1,file:{name:'change.diff',artifactId:'diff1'},requests:[]})})
  expect(screen.getByText('Keep local')).toBeInTheDocument()
  expect(screen.getByRole('button',{name:'Submit'})).toBeEnabled()
 })
 it('keeps requests saved during submission for the next submit',async()=>{
  let finish!: (value:boolean)=>void
  show(async()=>null,vi.fn(()=>new Promise<boolean>(resolve=>{finish=resolve})))
  request(1,'First batch')
  fireEvent.click(screen.getByRole('button',{name:'Submit'}))
  request(2,'Next batch')
  await act(async()=>{finish(true)})
  expect(screen.getByRole('button',{name:'Submit'})).toBeEnabled()
  expect(screen.getByRole('button',{name:'Delete request: Next batch'})).toBeInTheDocument()
  expect(screen.queryByRole('button',{name:'Delete request: First batch'})).toBeNull()
 })
})
