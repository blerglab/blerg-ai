import { render, within, waitFor } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { page, userEvent } from 'vitest/browser'
import { ChatContext, WorkspaceFileViewer, type ChatContextValue, type ReviewFile, type TransportFiles, type WorkspaceFile } from '@blerglab/chat'
import '@blerglab/chat/blerg.css'
import '../index.css'

for (const width of [390,3840]) {
  it(`sends a workspace review beside chat with contained controls at ${width}px`,async()=>{
    await page.viewport(width,900)
    const submitFeedback = vi.fn(async (_message:string,_files:File[])=>true)
    const source = 'const first = 1\nconst second = 2\n' + 'long source '.repeat(100)
    const files = {workspaceFile:async (_session:string,path:string):Promise<WorkspaceFile>=>({path,size:source.length,view:'text',content_type:'text/plain',mtime:'now',blob:new Blob([source])})} as TransportFiles
    const ctx = {sessionId:`workspace-review-${width}`,files,reviewFor:async()=>null,submitFeedback} as unknown as ChatContextValue
    const {container,getByRole} = render(<ChatContext.Provider value={ctx}><div style={{display:'grid',gridTemplateColumns:width<600?'minmax(0,1fr)':'minmax(0,1fr) 320px',height:'100vh',minWidth:0}}>
      <WorkspaceFileViewer sessionId={ctx.sessionId} files={files} path="src/app.ts" onClose={()=>{}} embedded />
      {width>=600 && <aside aria-label="Chat beside code">Chat remains beside the code</aside>}
    </div></ChatContext.Provider>)
    await userEvent.click(await within(container).findByRole('button',{name:'Review'}))
    await userEvent.click(getByRole('button',{name:'Select line 1'}))
    await userEvent.click(getByRole('button',{name:'Select line 2'}),{modifiers:['Shift']})
    await userEvent.fill(getByRole('textbox',{name:'Request'}),'Please explain both constants.')
    await userEvent.click(getByRole('button',{name:'Save request'}))
    const send = await within(container).findByRole('button',{name:'Send 1 request'})
    for (const control of [send,getByRole('textbox',{name:'Request'}),getByRole('button',{name:'Back to file'})]) {
      const box = control.getBoundingClientRect()
      expect(box.width).toBeGreaterThan(0)
      expect(box.left).toBeGreaterThanOrEqual(0)
      expect(box.right).toBeLessThanOrEqual(width)
    }
    expect(container.scrollWidth).toBeLessThanOrEqual(width)
    await userEvent.click(send)
    await waitFor(()=>expect(submitFeedback).toHaveBeenCalledTimes(1))
    const [message,uploads] = submitFeedback.mock.calls[0]
    expect(message).toContain('src/app.ts, lines 1–2')
    const review = JSON.parse(await uploads[0].text()) as ReviewFile
    expect(review.requests[0].anchor.workspace).toMatchObject({path:'src/app.ts',start:1,end:2})
    expect(review.requests[0].anchor.workspace?.contentHash).toMatch(/^[a-f0-9]{64}$/)
    if(width>=600) expect(getByRole('complementary',{name:'Chat beside code'}).getBoundingClientRect().width).toBe(320)
  })
  it(`shares a batch across two split file panes at ${width}px`,async()=>{
    await page.viewport(width,900)
    const submitFeedback = vi.fn(async (_message:string,_files:File[])=>true)
    const files = {workspaceFile:async (_session:string,path:string):Promise<WorkspaceFile>=>({path,size:3,view:'text',content_type:'text/plain',mtime:'now',blob:new Blob(['one'])})} as TransportFiles
    const ctx = {sessionId:`split-workspace-review-${width}`,files,reviewFor:async()=>null,submitFeedback} as unknown as ChatContextValue
    const {container} = render(<ChatContext.Provider value={ctx}><div style={{display:'grid',gridTemplateColumns:'minmax(0,1fr) minmax(0,1fr)',height:'100vh'}}>
      {['one.ts','two.ts'].map(path=><div key={path} data-testid={path} style={{minWidth:0,minHeight:0,display:'flex'}}><WorkspaceFileViewer sessionId={ctx.sessionId} files={files} path={path} onClose={()=>{}} embedded /></div>)}
    </div></ChatContext.Provider>)
    for (const path of ['one.ts','two.ts']) {
      const pane = page.getByTestId(path)
      await pane.getByRole('button',{name:'Review',exact:true}).click()
      await pane.getByRole('button',{name:'Select line 1',exact:true}).click()
      await pane.getByRole('textbox',{name:'Request',exact:true}).fill(`Fix ${path}`)
      await pane.getByRole('button',{name:'Save request',exact:true}).click()
      await waitFor(()=>expect(within(within(container).getByTestId(path)).getByText(`Fix ${path}`)).toBeInTheDocument())
    }
    await page.getByTestId('one.ts').getByRole('button',{name:'Send 2 requests',exact:true}).click()
    await waitFor(()=>expect(submitFeedback).toHaveBeenCalledTimes(1))
    expect(submitFeedback.mock.calls[0][0]).toContain('2 requests')
    expect(submitFeedback.mock.calls[0][0]).toContain('two.ts')
    expect(container.scrollWidth).toBeLessThanOrEqual(width)
  })
}
