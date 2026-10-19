import {afterEach,describe,expect,it,vi} from 'vitest'
import {render,waitFor,within} from '@testing-library/react'
import {userEvent} from 'vitest/browser'
import {ChatContext,ReviewMode,parseReviewFile,type ChatContextValue,type ReviewFile,type ReviewRequest} from '@blerglab/chat'
import '@blerglab/chat/blerg.css'
const source='# Threads\n\nResolved passage.\n\nRejected passage.\n\nOpen passage.\n'
const markdown={id:'thread-document',name:'threads.md',view:'markdown',content_type:'text/markdown',size:source.length,version:1}
const pdfArtifact={...markdown,id:'thread-pdf',name:'threads.pdf',view:'pdf',content_type:'application/pdf'}
const pdf=`%PDF-1.4
1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj
2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj
3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 400 200]/Contents 4 0 R/Resources<</Font<</F1 5 0 R>>>>>>endobj
4 0 obj<</Length 64>>stream
BT /F1 18 Tf 20 120 Td (The mean rose by twelve percent) Tj ET
endstream endobj
5 0 obj<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>endobj
trailer<</Root 1 0 R/Size 6>>
%%EOF`
const request=(id:string,status:ReviewRequest['status'],quote:string,text:string):ReviewRequest=>({id,status,text,createdAt:'2026-01-01T12:00:00Z',anchor:{kind:'text',quote,sourceArtifactId:markdown.id},...(status==='open'?{}:{reply:`Prior answer for ${id}`})})
const requests=()=>[request('done-id','done','Resolved passage.','Resolved request'),request('rejected-id','declined','Rejected passage.','Rejected request'),request('open-id','open','Open passage.','Existing open request')]
function mount(initial:ReviewRequest[],options:{kind?:'markdown'|'pdf';submit?:()=>Promise<boolean>}={}){
 const artifact=options.kind==='pdf'?pdfArtifact:markdown
 const review:ReviewFile={version:1,file:{name:artifact.name,artifactId:artifact.id},requests:initial}
 const sent:Array<{text:string;files:File[]}>=[]
 const context=(load:()=>Promise<ReviewFile|null>):ChatContextValue=>({sessionId:`threads-${artifact.id}`,files:null,view:vi.fn(),byName:()=>undefined,latestVersion:()=>undefined,reviewFor:load,submitFeedback:async(text,files)=>{sent.push({text,files});return options.submit?await options.submit():true}})
 const body=(value:ChatContextValue)=><div style={{display:'flex',width:960,height:640}}><ChatContext.Provider value={value}><ReviewMode artifact={artifact} text={options.kind==='pdf'?undefined:source} blob={options.kind==='pdf'?new Blob([pdf],{type:'application/pdf'}):undefined} onClose={()=>{}}/></ChatContext.Provider></div>
 const view=render(body(context(async()=>review)))
 return {...view,sent,reload:(load:()=>Promise<ReviewFile|null>)=>view.rerender(body(context(load)))}
}
function select(root:HTMLElement,quote:string){
 root.focus();const walk=document.createTreeWalker(root,NodeFilter.SHOW_TEXT)
 for(let node=walk.nextNode();node;node=walk.nextNode()){
  if(node.parentElement?.closest('[data-review-deletion]'))continue
  const at=(node as Text).data.indexOf(quote);if(at<0)continue
  const range=document.createRange();range.setStart(node,at);range.setEnd(node,at+quote.length)
  const selection=window.getSelection()!;selection.removeAllRanges();selection.addRange(range);return
 }
 throw new Error('Fixture passage unavailable')
}
afterEach(()=>localStorage.clear())
describe('document thread controls in Chromium',()=>{
 it('requires a nonblank explanation and Cancel leaves the thread settled without a pending submission',async()=>{
  const view=mount([requests()[0]])
  await userEvent.click(await view.findByRole('button',{name:'Reopen'}))
  expect(view.getByRole('button',{name:'Reopen with comment'})).toBeDisabled()
  await userEvent.fill(view.getByRole('textbox',{name:'Reason for reopening'}),'   \n  ')
  expect(view.getByRole('button',{name:'Reopen with comment'})).toBeDisabled()
  await userEvent.fill(view.getByRole('textbox',{name:'Reason for reopening'}),'Discard this explanation')
  await userEvent.click(view.getByRole('button',{name:'Cancel'}))
  expect(view.getByText('Resolved')).toBeInTheDocument()
  expect(view.queryByText('Pending review')).toBeNull()
  expect(view.getByRole('button',{name:'Submit'})).toBeDisabled()
  await userEvent.click(view.getByRole('button',{name:'Reopen'}))
  expect(view.getByRole('textbox',{name:'Reason for reopening'})).toHaveValue('')
  const explanation='Please check **this requirement**; <img src="https://invalid.test/reopen"> is text.'
  await userEvent.fill(view.getByRole('textbox',{name:'Reason for reopening'}),explanation)
  await userEvent.click(view.getByRole('button',{name:'Reopen with comment'}))
  expect(view.getByText('Person · reopened')).toBeInTheDocument()
  expect(view.container.querySelector('.review-reply img')).toBeNull()
  await userEvent.click(view.getByRole('button',{name:'Submit'}))
  await waitFor(()=>expect(view.sent).toHaveLength(1))
  const saved=parseReviewFile(await view.sent[0].files.at(-1)!.text())!
  expect(saved.requests[0]).toMatchObject({status:'open',revision:1,comments:[{text:explanation,createdAt:expect.any(String),revision:1}],replies:[expect.objectContaining({status:'done',revision:0})]})
  expect(view.sent[0].text).toContain(explanation)
  expect(view.sent[0].text).not.toContain('Discard this explanation')
 })
 it('collapses and hides settled threads without omitting them from submitted JSON or repeating them in feedback',async()=>{
  const view=mount(requests())
  const collapse=await view.findByRole('button',{name:/^Collapse thread: Resolved request$/})
  await userEvent.click(collapse)
  expect(view.getByRole('button',{name:/^Expand thread: Resolved request$/})).toHaveAttribute('aria-expanded','false')
  expect(view.queryByText('Prior answer for done-id')).toBeNull()
  await userEvent.click(view.getByRole('button',{name:/^Expand thread: Resolved request$/}))
  expect(view.getByText('Prior answer for done-id')).toBeInTheDocument()
  await userEvent.click(view.getByRole('checkbox',{name:'Hide resolved'}))
  expect(view.queryByText('Resolved request')).toBeNull();expect(view.queryByText('Rejected request')).toBeNull()
  expect(view.getByText('Existing open request')).toBeInTheDocument()
  const editor=view.getByRole('textbox',{name:'Document'});select(editor,'Open passage.')
  await userEvent.keyboard('{Control>}{Alt>}m{/Alt}{/Control}')
  await userEvent.fill(view.getByRole('textbox',{name:'Request'}),'Only this new request needs another answer')
  await userEvent.click(view.getByRole('button',{name:'Save'}));await userEvent.click(view.getByRole('button',{name:'Submit'}))
  await waitFor(()=>expect(view.sent).toHaveLength(1))
  const saved=parseReviewFile(await view.sent[0].files.at(-1)!.text())!
  expect(saved.requests).toHaveLength(4)
  expect(saved.requests.find(r=>r.id==='done-id')?.status).toBe('done');expect(saved.requests.find(r=>r.id==='rejected-id')?.status).toBe('declined')
  expect(view.sent[0].text).toContain('Only this new request needs another answer')
  for(const id of ['done-id','rejected-id','open-id'])expect(view.sent[0].text).not.toContain(`[${id}]`)
 })
 it('reopens an unavailable old-source anchor with history and survives an older remote reply after submission',async()=>{
  const prior={...request('old-answer','declined','Rejected passage.','Reconsider this answer'),revision:3,anchor:{kind:'text' as const,quote:'Rejected passage.',sourceArtifactId:'previous-source',unavailable:true}}
  const view=mount([prior]);await userEvent.click(await view.findByRole('button',{name:'Reopen'}))
  await userEvent.fill(view.getByRole('textbox',{name:'Reason for reopening'}),'The earlier answer missed this requirement.')
  await userEvent.click(view.getByRole('button',{name:'Reopen with comment'}))
  expect(view.getByText('Pending review')).toBeInTheDocument();expect(view.getByText('Prior answer for old-answer')).toBeInTheDocument()
  expect(view.getByRole('button',{name:'Original passage unavailable'})).toBeDisabled()
  await userEvent.click(view.getByRole('checkbox',{name:'Hide resolved'}));expect(view.getByText('Reconsider this answer')).toBeInTheDocument()
  await userEvent.click(view.getByRole('button',{name:'Submit'}));await waitFor(()=>expect(view.sent).toHaveLength(1))
  const saved=parseReviewFile(await view.sent[0].files.at(-1)!.text())!
  expect(saved.requests[0].comments?.[0]).toMatchObject({text:'The earlier answer missed this requirement.',revision:4})
  expect(view.sent[0].text).toContain('The earlier answer missed this requirement.')
  expect(saved.requests[0]).toMatchObject({id:prior.id,status:'open',revision:4,anchor:prior.anchor,replies:[expect.objectContaining({text:'Prior answer for old-answer',status:'declined',revision:3})]})
  expect(view.queryByRole('button',{name:/^Delete request:/})).toBeNull()
  const stale:ReviewFile={version:1,file:{name:markdown.name,artifactId:markdown.id},requests:[prior]}
  const load=vi.fn(async()=>stale);view.reload(load)
  await waitFor(()=>expect(load).toHaveBeenCalled());await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))
  expect(view.getByText('The earlier answer missed this requirement.')).toBeInTheDocument()
  expect(view.getByText('Open')).toBeInTheDocument();expect(view.queryByRole('button',{name:'Reopen'})).toBeNull()
  expect(view.getByRole('button',{name:'Original passage unavailable'})).toBeDisabled()
 })
 it('does not mutate other settled threads while a reopened review submission is in flight or fails',async()=>{
  let finish!:(ok:boolean)=>void;const result=new Promise<boolean>(resolve=>{finish=resolve})
  const view=mount(requests().slice(0,2),{submit:()=>result})
  const first=(await view.findByText('Resolved request')).closest('[data-testid="review-request"]') as HTMLElement
  await userEvent.click(within(first).getByRole('button',{name:'Reopen'}));await userEvent.fill(view.getByRole('textbox',{name:'Reason for reopening'}),'Please reconsider the failed requirement.');await userEvent.click(view.getByRole('button',{name:'Reopen with comment'}));await userEvent.click(view.getByRole('button',{name:'Submit'}))
  await waitFor(()=>expect(view.sent).toHaveLength(1))
  expect(view.getByRole('button',{name:'Reopen'})).toBeDisabled()
  await userEvent.click(view.getByRole('checkbox',{name:'Hide resolved'}));expect(view.queryByText('Rejected request')).toBeNull()
  finish(false)
  await view.findByRole('alert')
  expect(view.getByText('Pending review')).toBeInTheDocument();expect(view.getByRole('button',{name:'Submit'})).toBeEnabled()
  const saved=parseReviewFile(await view.sent[0].files.at(-1)!.text())!
  expect(saved.requests).toHaveLength(2);expect(saved.requests.find(r=>r.id==='rejected-id')?.status).toBe('declined')
 })
 it('does not reopen a thread beyond the supported revision cap',async()=>{
  const capped={...request('capped','done','Resolved passage.','Revision capped'),revision:1_000_000}
  const view=mount([capped]);expect(await view.findByRole('button',{name:'Reopen'})).toBeDisabled()
  expect(view.getByRole('button',{name:'Submit'})).toBeDisabled();expect(view.sent).toHaveLength(0)
 })
 it('shows the latest legacy answer beside existing history and preserves it through reopen',async()=>{
  const mixed={...request('legacy-latest','done','Resolved passage.','Legacy mixed history'),revision:5,reply:'Newest legacy answer',
   replies:[{status:'declined' as const,text:'Older archived answer',createdAt:'2026-01-01T10:00:00Z',revision:4}]}
  const view=mount([mixed])
  await view.findByText('Older archived answer')
  expect(await view.findByText('Newest legacy answer')).toBeInTheDocument()
  await userEvent.click(view.getByRole('button',{name:/^Collapse thread: Legacy mixed history$/}))
  expect(view.queryByText('Newest legacy answer')).toBeNull()
  await userEvent.click(view.getByRole('button',{name:/^Expand thread: Legacy mixed history$/}))
  expect(view.getByText('Newest legacy answer')).toBeInTheDocument()
  await userEvent.click(view.getByRole('button',{name:'Reopen'}))
  await userEvent.fill(view.getByRole('textbox',{name:'Reason for reopening'}),'The newest answer still needs clarification.')
  await userEvent.click(view.getByRole('button',{name:'Reopen with comment'}))
  expect(view.getAllByText('Newest legacy answer')).toHaveLength(1)
  await userEvent.click(view.getByRole('button',{name:'Submit'}));await waitFor(()=>expect(view.sent).toHaveLength(1))
  const saved=parseReviewFile(await view.sent[0].files.at(-1)!.text())!
  expect(saved.requests[0]).toMatchObject({status:'open',revision:6})
  expect(saved.requests[0].replies).toHaveLength(2)
  expect(saved.requests[0].replies?.filter(reply=>reply.text==='Newest legacy answer')).toHaveLength(1)
  expect(saved.requests[0].replies?.at(-1)).toMatchObject({status:'done',text:'Newest legacy answer',revision:5})
 })
 it('offers collapse hide and revision-aware reopen on a real rendered PDF',async()=>{
  const pdfRequests=[request('pdf-done','done','mean rose','PDF resolved comment'),request('pdf-rejected','declined','twelve percent','PDF rejected comment')].map(r=>({...r,anchor:{...r.anchor,page:1,sourceArtifactId:pdfArtifact.id}}))
  const view=mount(pdfRequests,{kind:'pdf'})
  await waitFor(()=>expect([...view.container.querySelectorAll('.textLayer span')].some(span=>span.textContent?.includes('mean rose'))).toBe(true),{timeout:12000})
  expect(view.container.querySelector('canvas')!.width).toBeGreaterThan(100)
  await userEvent.click(view.getByRole('button',{name:/^Collapse thread: PDF resolved comment$/}));expect(view.queryByText('Prior answer for pdf-done')).toBeNull()
  await userEvent.click(view.getByRole('button',{name:/^Expand thread: PDF resolved comment$/}));expect(view.getByText('Prior answer for pdf-done')).toBeInTheDocument()
  await userEvent.click(view.getByRole('checkbox',{name:'Hide resolved'}));expect(view.queryAllByTestId('review-request')).toHaveLength(0)
  await userEvent.click(view.getByRole('checkbox',{name:'Hide resolved'}))
  const card=view.getByText('PDF resolved comment').closest('[data-testid="review-request"]') as HTMLElement
  await userEvent.click(within(card).getByRole('button',{name:'Reopen'}));await userEvent.fill(view.getByRole('textbox',{name:'Reason for reopening'}),'The PDF conclusion needs its source.');await userEvent.click(view.getByRole('button',{name:'Reopen with comment'}));await userEvent.click(view.getByRole('checkbox',{name:'Hide resolved'}))
  expect(view.getByText('PDF resolved comment')).toBeInTheDocument();expect(view.queryByText('PDF rejected comment')).toBeNull()
  await userEvent.click(view.getByRole('button',{name:'Submit'}));await waitFor(()=>expect(view.sent).toHaveLength(1))
  const saved=parseReviewFile(await view.sent[0].files.at(-1)!.text())!
  expect(saved.requests).toHaveLength(2);expect(saved.requests[0]).toMatchObject({id:'pdf-done',status:'open',revision:1,anchor:{page:1,sourceArtifactId:pdfArtifact.id},replies:[expect.objectContaining({status:'done',revision:0})]})
  expect(view.sent[0].text).toContain('[pdf-done]');expect(view.sent[0].text).not.toContain('[pdf-rejected]')
 },20000)
})
