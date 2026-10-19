import {afterEach,describe,expect,it,vi} from 'vitest'
import {render,waitFor} from '@testing-library/react'
import {cdp,userEvent} from 'vitest/browser'
import type {CDPSession} from '@vitest/browser-playwright'
import {ChatContext,ReviewMode,parseReviewFile,type ChatContextValue,type ReviewFile} from '@blerglab/chat'
import '@blerglab/chat/blerg.css'
const source='# Review\n\nSame passage.\n\nSame passage.\n'
const artifact={id:'security-current',name:'security.md',view:'markdown',content_type:'text/markdown',size:source.length,version:2}
const review=(artifactId=artifact.id):ReviewFile=>({version:1,file:{name:artifact.name,artifactId},requests:[{id:'original-first',text:'Comment belongs to the first original passage',status:'open',createdAt:'2026-01-01T00:00:00Z',anchor:{kind:'text',quote:'Same passage.',occurrence:1}}]})
function mount(text=source,load:()=>Promise<ReviewFile|null>=async()=>null){
 const sent:File[][]=[]
 const context:ChatContextValue={sessionId:'document-security',files:null,view:vi.fn(),byName:()=>undefined,latestVersion:()=>undefined,reviewFor:load,submitFeedback:async(_message,files)=>{sent.push(files);return true}}
 return {...render(<ChatContext.Provider value={context}><ReviewMode artifact={artifact} text={text} onClose={()=>{}}/></ChatContext.Provider>),sent}
}
function select(root:HTMLElement,quote:string){
 root.focus();const walk=document.createTreeWalker(root,NodeFilter.SHOW_TEXT)
 for(let n=walk.nextNode();n;n=walk.nextNode()){
  if(n.parentElement?.closest('[data-review-deletion]'))continue
  const at=(n as Text).data.indexOf(quote);if(at<0)continue
  const range=document.createRange();range.setStart(n,at);range.setEnd(n,at+quote.length)
  const selection=window.getSelection()!;selection.removeAllRanges();selection.addRange(range);return
 }
 throw new Error('Fixture passage unavailable')
}
afterEach(()=>localStorage.clear())
describe('document review adversarial Chromium cases',()=>{
 it('does not attach a delayed original comment to the surviving duplicate after deletion',async()=>{
  let resolve!:(value:ReviewFile)=>void
  const pending=new Promise<ReviewFile>(done=>{resolve=done})
  const view=mount(source,()=>pending);const editor=await view.findByRole('textbox',{name:'Document'})
  select(editor,'Same passage.');await userEvent.keyboard('{Backspace}');resolve(review())
  await waitFor(()=>expect(view.getByRole('button',{name:'Original passage unavailable'})).toBeDisabled())
  expect(editor).toHaveTextContent('Same passage.');expect(view.queryByRole('button',{name:'Show passage'})).toBeNull()
 })
 it('keeps older artifact comments visible without attaching them to identical current text',async()=>{
  const view=mount(source,async()=>review('security-previous'))
  await view.findByText('Comment belongs to the first original passage')
  await waitFor(()=>expect(view.getByRole('button',{name:'Original passage unavailable'})).toBeDisabled())
  expect(view.queryByRole('button',{name:'Show passage'})).toBeNull()
  await userEvent.click(view.getByRole('button',{name:/^Source$/}))
  await userEvent.fill(view.getByRole('textbox',{name:'Source'}),source+'New ending.\n')
  await userEvent.click(view.getByRole('button',{name:'Submit'}))
  await waitFor(()=>expect(view.sent).toHaveLength(1))
  const saved=parseReviewFile(await view.sent[0].at(-1)!.text())!
  expect(saved).not.toBeNull()
  expect(saved.file.artifactId).toBe(artifact.id)
  expect(saved.requests[0].anchor).toMatchObject({sourceArtifactId:'security-previous',unavailable:true})
  view.unmount()
  const reopened=mount(source,async()=>saved)
  await waitFor(()=>expect(reopened.getByRole('button',{name:'Original passage unavailable'})).toBeDisabled())
 })
 it('does not map an existing duplicate comment after the source fallback is edited',async()=>{
  const view=mount(source,async()=>review())
  await waitFor(()=>expect(view.getByRole('button',{name:'Show passage'})).toBeEnabled())
  await userEvent.click(view.getByRole('button',{name:/^Source$/}))
  const input=view.getByRole('textbox',{name:'Source'})
  await userEvent.fill(input,'# Review\n\nSame passage.\n')
  await waitFor(()=>expect(view.getByRole('button',{name:'Original passage unavailable'})).toBeDisabled())
  expect(view.queryByRole('button',{name:'Show passage'})).toBeNull()
 })
 it('preserves a dirty draft but never moves it to a duplicate when its original passage is deleted',async()=>{
  const view=mount(source)
  const editor=await view.findByRole('textbox',{name:'Document'})
  select(editor,'Same passage.')
  await userEvent.keyboard('{Control>}{Alt>}m{/Alt}{/Control}')
  const comment=view.getByRole('textbox',{name:'Request'})
  await userEvent.fill(comment,'Only change the original first passage')
  select(editor,'Same passage.')
  await userEvent.keyboard('{Backspace}')
  expect(comment).toHaveValue('Only change the original first passage')
  await userEvent.click(view.getByRole('button',{name:'Save'}))
  await waitFor(()=>expect(view.getByRole('button',{name:'Original passage unavailable'})).toBeDisabled())
  expect(view.queryByRole('button',{name:'Show passage'})).toBeNull()
  expect(view.getByText('Only change the original first passage')).toBeInTheDocument()
  await userEvent.click(view.getByRole('button',{name:'Submit'}))
  await waitFor(()=>expect(view.sent).toHaveLength(1))
  await waitFor(()=>expect(view.getByRole('button',{name:'Original passage unavailable'})).toBeDisabled())
  await userEvent.click(view.getByRole('button',{name:/^Source$/}))
  await userEvent.click(view.getByRole('button',{name:/^Document$/}))
  await waitFor(()=>expect(view.getByRole('button',{name:'Original passage unavailable'})).toBeDisabled())
  const saved=parseReviewFile(await view.sent[0].at(-1)!.text())!
  expect(saved).not.toBeNull()
  expect(saved.requests[0].anchor).toMatchObject({sourceArtifactId:artifact.id,unavailable:true})
  view.unmount()
  const reopened=mount(source,async()=>saved)
  await waitFor(()=>expect(reopened.getByRole('button',{name:'Original passage unavailable'})).toBeDisabled())
  expect(reopened.queryByRole('button',{name:'Show passage'})).toBeNull()
 })
 it('does not rebind an unsent comment after inserting an earlier duplicate and remounting the editor',async()=>{
  const view=mount(source)
  const editor=await view.findByRole('textbox',{name:'Document'})
  select(editor,'Same passage.');await userEvent.keyboard('{Control>}{Alt>}m{/Alt}{/Control}')
  await userEvent.fill(view.getByRole('textbox',{name:'Request'}),'Keep this on the original paragraph')
  await userEvent.click(view.getByRole('button',{name:'Save'}))
  await waitFor(()=>expect(view.getByRole('button',{name:'Show passage'})).toBeEnabled())
  await userEvent.click(editor);await userEvent.keyboard('{Control>}{Home}{/Control}Same passage. ')
  await userEvent.click(view.getByRole('button',{name:/^Source$/}))
  await userEvent.click(view.getByRole('button',{name:/^Document$/}))
  await waitFor(()=>expect(view.getByRole('button',{name:'Original passage unavailable'})).toBeDisabled())
  expect(view.getByText('Keep this on the original paragraph')).toBeInTheDocument()
 })
 it('does not rebind an existing comment when submission advances the edited source baseline',async()=>{
  const view=mount(source,async()=>review())
  const editor=await view.findByRole('textbox',{name:'Document'})
  await waitFor(()=>expect(view.getByRole('button',{name:'Show passage'})).toBeEnabled())
  await userEvent.click(editor);await userEvent.keyboard('{Control>}{Home}{/Control}Same passage. ')
  await userEvent.click(view.getByRole('button',{name:'Submit'}))
  await waitFor(()=>expect(view.sent).toHaveLength(1))
  await waitFor(()=>expect(view.getByRole('button',{name:'Original passage unavailable'})).toBeDisabled())
  expect(view.getByText('Comment belongs to the first original passage')).toBeInTheDocument()
 })
 it.each(['<?xml version="1.0"?>','<? 0?>','<?processing data?>'])('preserves processing-instruction source without rich parsing: %s',async instruction=>{
  const text=instruction+'\n\nKeep **all** bytes.\n';const view=mount(text)
  expect(await view.findByRole('textbox',{name:'Source'})).toHaveValue(text)
  expect(view.queryByRole('textbox',{name:'Document'})).toBeNull();expect(view.getByRole('button',{name:'Submit'})).toBeDisabled()
 })
 it('keeps untouched Markdown bytes exact across source and document mode toggles',async()=>{
  const text='# Heading\n\nA **bold** word and _italic_ word.\n\n- first\n- second\n';const view=mount(text)
  await view.findByRole('textbox',{name:'Document'});await userEvent.click(view.getByRole('button',{name:/^Source$/}))
  expect(view.getByRole('textbox',{name:'Source'})).toHaveValue(text)
  await userEvent.click(view.getByRole('button',{name:/^Document$/}));await view.findByRole('textbox',{name:'Document'})
  await userEvent.click(view.getByRole('button',{name:/^Source$/}));expect(view.getByRole('textbox',{name:'Source'})).toHaveValue(text)
  expect(view.getByRole('button',{name:'Submit'})).toBeDisabled();expect(view.sent).toHaveLength(0)
 })
 it('consumes only plaintext clipboard content before HTML parsing and makes no resource requests',async()=>{
  const session=cdp() as CDPSession;const requests:string[]=[]
  const received=(event:{request:{url:string}})=>{if(event.request.url.includes('document-security.invalid'))requests.push(event.request.url)}
  await session.send('Network.enable')
  // Vitest's CDP on() returns synchronously while backend subscription uses an RPC.
  // A subsequent acknowledged CDP command fences listener installation before the probe.
  session.on('Network.requestWillBeSent',received)
  await session.send('Network.setBlockedURLs',{urls:['*://document-security.invalid/*']})
  try{
   await fetch('https://document-security.invalid/probe').catch(()=>{});await waitFor(()=>expect(requests.some(url=>url.endsWith('/probe'))).toBe(true),{timeout:5000});requests.length=0
   const view=mount('Safe paragraph.\n');const editor=await view.findByRole('textbox',{name:'Document'})
   await userEvent.click(editor);await userEvent.keyboard('{Control>}a{/Control}')
   const clipboardData=new DataTransfer();clipboardData.setData('text/plain','Plaintext paragraph.')
   clipboardData.setData('text/html','<img src="https://document-security.invalid/image"><svg onload="alert(1)"><image href="https://document-security.invalid/svg"/></svg><iframe src="https://document-security.invalid/frame"></iframe><style>@import "https://document-security.invalid/css"</style><a href="javascript:alert(1)">unsafe</a>')
   editor.dispatchEvent(new ClipboardEvent('paste',{clipboardData,bubbles:true,cancelable:true}));await waitFor(()=>{const current=editor.cloneNode(true) as HTMLElement;current.querySelectorAll('[data-review-deletion]').forEach(node=>node.remove());expect(current).toHaveTextContent('Plaintext paragraph.')})
   // Inspect document content; the trusted formatting toolbar now contains SVG icons.
   expect(editor.querySelector('img,iframe,script,svg,style')).toBeNull();await userEvent.click(view.getByRole('button',{name:'Submit'}));await waitFor(()=>expect(view.sent).toHaveLength(1))
   const uploaded=await view.sent[0][0].text();expect(uploaded).toContain('Plaintext paragraph.');expect(uploaded).not.toContain('document-security.invalid')
   const htmlOnly=new DataTransfer();htmlOnly.setData('text/html','<img src="https://document-security.invalid/html-only">')
   const latestEditor=await view.findByRole('textbox',{name:'Document'});latestEditor.dispatchEvent(new ClipboardEvent('paste',{clipboardData:htmlOnly,bubbles:true,cancelable:true}));await new Promise(resolve=>setTimeout(resolve,100))
   expect(requests).toEqual([]);expect(latestEditor.querySelector('img,iframe,script,svg,style')).toBeNull()
  }finally{await session.off('Network.requestWillBeSent',received);await session.send('Network.setBlockedURLs',{urls:[]});await session.send('Network.disable')}
 },15000)
})
