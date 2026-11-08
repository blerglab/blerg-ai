import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { ChatContext, type ChatContextValue } from '../context'
import WorkspaceFileViewer from '../WorkspaceFileViewer'
import WorkspaceReview from './WorkspaceReview'
import type { TransportFiles, WorkspaceFile } from '../../transport/types'
import type { ReviewFile } from '../../model/review'

it('batches lines from two workspace tabs through the existing feedback path, retaining a failed send', async () => {
  const submitFeedback = vi.fn(async () => false)
  const source: WorkspaceFile = {path:'src/one.ts',size:7,view:'text',content_type:'text/plain',mtime:'now',blob:new Blob(['one\ntwo'])}
  const files = {workspaceFile:vi.fn(async (_session:string,path:string)=>({...source,path})),upload:vi.fn()} as unknown as TransportFiles
  const ctx = {sessionId:'workspace-review-batch',files,reviewFor:async()=>null,submitFeedback} as unknown as ChatContextValue
  const show = (path:string) => <ChatContext.Provider value={ctx}><WorkspaceFileViewer sessionId={ctx.sessionId} files={files} path={path} onClose={()=>{}} embedded /></ChatContext.Provider>
  const tab = render(show('src/one.ts'))
  await screen.findByTestId('workspace-code')
  fireEvent.click(screen.getByRole('button',{name:'Review'}))
  fireEvent.click(screen.getByRole('button',{name:'Select line 1'}))
  fireEvent.click(screen.getByRole('button',{name:'Select line 2'}),{shiftKey:true})
  fireEvent.change(screen.getByRole('textbox',{name:'Request'}),{target:{value:'Fix the first file'}})
  fireEvent.click(screen.getByRole('button',{name:'Save request'}))
  await screen.findByRole('button',{name:'Send 1 request'})
  tab.unmount()
  render(show('src/two.ts'))
  await screen.findByTestId('workspace-code')
  fireEvent.click(screen.getByRole('button',{name:'Review'}))
  fireEvent.click(screen.getByRole('button',{name:'Select line 2'}))
  fireEvent.change(screen.getByRole('textbox',{name:'Request'}),{target:{value:'Fix the second file'}})
  fireEvent.click(screen.getByRole('button',{name:'Save request'}))
  fireEvent.click(await screen.findByRole('button',{name:'Send 2 requests'}))
  await screen.findByText('Could not send. Your requests are still here.')
  expect(screen.getByRole('button',{name:'Send 2 requests'})).toBeEnabled()
  submitFeedback.mockResolvedValue(true)
  fireEvent.click(await screen.findByRole('button',{name:'Send 2 requests'}))
  await waitFor(()=>expect(submitFeedback).toHaveBeenCalledTimes(2))
  const [message, uploads] = submitFeedback.mock.calls[1] as unknown as [string, File[]]
  expect(message).toContain('src/one.ts')
  expect(message).toContain('src/two.ts')
  expect(uploads).toHaveLength(1)
  const review = JSON.parse(await uploads[0].text()) as ReviewFile
  expect(review.requests.map(r=>r.anchor.workspace)).toEqual([
    expect.objectContaining({path:'src/one.ts',start:1,end:2}),
    expect.objectContaining({path:'src/two.ts',start:2,end:2}),
  ])
  expect(files.upload).not.toHaveBeenCalled()
})

it('shows agent replies and moved source without moving a changed passage onto unrelated code', async () => {
  const {createWorkspaceAnchor} = await import('../../model/workspaceAnchor')
  const anchor = await createWorkspaceAnchor('app.ts','one\ntwo',2,2)
  const review:ReviewFile = {version:1,file:{name:'Workspace files',artifactId:'workspace'},requests:[
    {id:'done',anchor,text:'Fix two',status:'done',reply:'Fixed it.',createdAt:'now'},
    {id:'outdated',anchor:{...anchor,quote:'missing'},text:'Old code',status:'declined',reply:'Code was removed.',createdAt:'now'},
  ]}
  const source = 'intro\none\ntwo'
  const files = {workspaceFile:async()=>({path:'app.ts',size:source.length,view:'text',content_type:'text/plain',mtime:'new',blob:new Blob([source])})} as unknown as TransportFiles
  const ctx = {sessionId:'workspace-replies',files,reviewFor:async()=>review,submitFeedback:vi.fn()} as unknown as ChatContextValue
  render(<ChatContext.Provider value={ctx}><WorkspaceFileViewer sessionId={ctx.sessionId} files={files} path="app.ts" onClose={()=>{}} embedded /></ChatContext.Provider>)
  fireEvent.click(await screen.findByRole('button',{name:'Review'}))
  await screen.findByText('Fixed it.')
  await screen.findByText('· Moved')
  expect(screen.getByText('app.ts, lines 3–3')).toBeInTheDocument()
  await screen.findByText('· Outdated: selected lines changed or are ambiguous')
  expect(screen.getByText('Code was removed.')).toBeInTheDocument()
})

it('anchors a nontext file by its bytes without inventing line numbers', async () => {
  const send = vi.fn(async (_message:string,_files:File[])=>true)
  const file = {path:'asset.bin',size:3,view:'download',content_type:'application/octet-stream',mtime:'now',blob:new Blob([new Uint8Array([0,1,2])])}
  const files = {workspaceFile:async()=>file} as unknown as TransportFiles
  const ctx = {sessionId:'workspace-binary',files,reviewFor:async()=>null,submitFeedback:send} as unknown as ChatContextValue
  render(<ChatContext.Provider value={ctx}><WorkspaceFileViewer sessionId={ctx.sessionId} files={files} path="asset.bin" onClose={()=>{}} embedded /></ChatContext.Provider>)
  fireEvent.click(await screen.findByRole('button',{name:'Review'}))
  fireEvent.change(screen.getByRole('textbox',{name:'Request'}),{target:{value:'Replace the asset'}})
  fireEvent.click(screen.getByRole('button',{name:'Save request'}))
  fireEvent.click(await screen.findByRole('button',{name:'Send 1 request'}))
  await waitFor(()=>expect(send).toHaveBeenCalledTimes(1))
  const parsed = JSON.parse(await send.mock.calls[0][1][0].text()) as ReviewFile
  expect(parsed.requests[0].anchor.workspace).toMatchObject({path:'asset.bin',start:0,end:0})
  expect(send.mock.calls[0][0]).toContain('whole file')
})

it('sends a shared batch only once when two panes submit before either rerenders', async () => {
  let finish!: (ok:boolean)=>void
  const send = vi.fn(()=>new Promise<boolean>(resolve=>{finish=resolve}))
  const files = {} as TransportFiles
  const ctx = {sessionId:'workspace-simultaneous-send',files,reviewFor:async()=>null,submitFeedback:send} as unknown as ChatContextValue
  const {container} = render(<ChatContext.Provider value={ctx}>{['first','second'].map(id=><div key={id} data-testid={id}><WorkspaceReview sessionId={ctx.sessionId} files={files} path="app.ts" text="one" blob={new Blob(['one'])} onClose={()=>{}} /></div>)}</ChatContext.Provider>)
  const first = within(within(container).getByTestId('first'))
  fireEvent.click(first.getByRole('button',{name:'Select line 1'}))
  fireEvent.change(first.getByRole('textbox',{name:'Request'}),{target:{value:'Fix once'}})
  fireEvent.click(first.getByRole('button',{name:'Save request'}))
  await first.findByRole('button',{name:'Send 1 request'})
  const sendButtons = screen.getAllByRole('button',{name:'Send 1 request'})
  act(()=>{fireEvent.click(sendButtons[0]);fireEvent.click(sendButtons[1])})
  expect(send).toHaveBeenCalledTimes(1)
  await act(async()=>{finish(true)})
})

it('drops the visible draft batch when the account namespace changes on a reused transport', async () => {
  let account = 'first'
  const files = {queueScope:()=>account} as TransportFiles
  const ctx = {sessionId:'workspace-account-switch',files,reviewFor:async()=>null,submitFeedback:vi.fn()} as unknown as ChatContextValue
  const show = ()=> <ChatContext.Provider value={ctx}><WorkspaceReview sessionId={ctx.sessionId} files={files} path="app.ts" text="one" blob={new Blob(['one'])} onClose={()=>{}} /></ChatContext.Provider>
  const tab = render(show())
  fireEvent.click(screen.getByRole('button',{name:'Select line 1'}))
  fireEvent.change(screen.getByRole('textbox',{name:'Request'}),{target:{value:'First account draft'}})
  fireEvent.click(screen.getByRole('button',{name:'Save request'}))
  await screen.findByRole('button',{name:'Send 1 request'})
  fireEvent.change(screen.getByRole('textbox',{name:'Request'}),{target:{value:'Private unsaved note'}})
  account = 'second'
  tab.rerender(show())
  expect(screen.queryByText('First account draft')).toBeNull()
  expect(screen.getByRole('textbox',{name:'Request'})).toHaveValue('')
  expect(screen.getByRole('button',{name:'Send 0 requests'})).toBeDisabled()
})
