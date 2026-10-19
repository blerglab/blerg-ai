import {afterEach, beforeEach, expect, it, vi} from 'vitest'
import {act, fireEvent, render, screen, waitFor} from '@testing-library/react'
import ReviewMode from './ReviewMode'
import {ChatContext, type ChatContextValue} from '../context'
import type {ReviewFile} from '../../model/review'

const original='# Report\n\nOriginal body.\n'
beforeEach(()=>localStorage.clear())
afterEach(()=>{vi.restoreAllMocks();window.getSelection()?.removeAllRanges()})
function fileText(file:File):Promise<string>{return new Promise((resolve,reject)=>{const reader=new FileReader();reader.onload=()=>resolve(String(reader.result));reader.onerror=reject;reader.readAsText(file)})}
function setup(existing:ReviewFile|null=null){
 let finish!:(ok:boolean)=>void
 const sent:File[][]=[]
 const chat:ChatContextValue={sessionId:'review-lifecycle',files:null,view:vi.fn(),latestVersion:()=>undefined,byName:()=>undefined,reviewFor:async()=>existing,submitFeedback:async(_text,files)=>{sent.push(files);return new Promise<boolean>(resolve=>{finish=resolve})}}
 const view=render(<ChatContext.Provider value={chat}><ReviewMode artifact={{id:'document-lifecycle',name:'report.md',size:original.length,view:'markdown',content_type:'text/markdown'}} text={original} onClose={vi.fn()}/></ChatContext.Provider>)
 fireEvent.click(screen.getByRole('button',{name:'Source'}))
 const source=screen.getByRole('textbox',{name:'Source'})
 return {sent,source,finish:(ok:boolean)=>finish(ok),unmount:view.unmount}
}

it('retains a source edit made during an earlier submission and measures its next diff against the sent snapshot',async()=>{
 const {source,sent,finish}=setup()
 const first=original+'First change.\n',second=first+'Second change.\n'
 fireEvent.change(source,{target:{value:first}})
 fireEvent.click(screen.getByRole('button',{name:'Submit'}))
 await waitFor(()=>expect(sent).toHaveLength(1))
 fireEvent.change(source,{target:{value:second}})
 await act(async()=>finish(true))
 expect(source).toHaveValue(second)
 expect(localStorage.getItem('chat.review.edit.document-lifecycle')).toBe(second)
 expect(screen.getByRole('button',{name:'Submit'})).toBeEnabled()
 expect(await fileText(sent[0][0])).toBe(first)
 fireEvent.click(screen.getByRole('button',{name:'Submit'}))
 await waitFor(()=>expect(sent).toHaveLength(2))
 const review=JSON.parse(await fileText(sent[1][1])) as ReviewFile
 expect(review.edit?.diff).toContain('+Second change.')
 expect(review.edit?.diff).not.toContain('+First change.')
 await act(async()=>finish(false))
})

it('keeps newer source locally after the in-flight submission fails',async()=>{
 const {source,sent,finish}=setup()
 fireEvent.change(source,{target:{value:original+'Sent candidate.\n'}})
 fireEvent.click(screen.getByRole('button',{name:'Submit'}))
 await waitFor(()=>expect(sent).toHaveLength(1))
 const later=original+'Keep this later draft.\n'
 fireEvent.change(source,{target:{value:later}})
 await act(async()=>finish(false))
 expect(source).toHaveValue(later)
 expect(localStorage.getItem('chat.review.edit.document-lifecycle')).toBe(later)
 expect(screen.getByRole('button',{name:'Submit'})).toBeEnabled()
})

it('preserves a comment saved while an earlier source snapshot is being sent',async()=>{
 const {source,sent,finish}=setup()
 fireEvent.change(source,{target:{value:original+'Sent candidate.\n'}})
 fireEvent.click(screen.getByRole('button',{name:'Submit'}))
 await waitFor(()=>expect(sent).toHaveLength(1))
 const page=screen.getByTestId('review-page')
 const walker=document.createTreeWalker(page,NodeFilter.SHOW_TEXT)
 let node:Node|null
 while((node=walker.nextNode())){const offset=node.textContent?.indexOf('Original body.')??-1;if(offset<0)continue;const range=document.createRange();range.setStart(node,offset);range.setEnd(node,offset+14);const selection=window.getSelection()!;selection.removeAllRanges();selection.addRange(range);break}
 fireEvent.click(screen.getByRole('button',{name:'Add comment'}))
 fireEvent.change(screen.getByRole('textbox',{name:'Request'}),{target:{value:'Do not lose this concurrent comment'}})
 fireEvent.click(screen.getByRole('button',{name:'Save'}))
 await act(async()=>finish(true))
 expect(screen.getByRole('button',{name:'Submit'})).toBeEnabled()
 expect(screen.getByTestId('review-request')).toHaveTextContent('Do not lose this concurrent comment')
 fireEvent.click(screen.getByRole('button',{name:'Submit'}))
 await waitFor(()=>expect(sent).toHaveLength(2))
 expect(sent[1]).toHaveLength(1)
 const review=JSON.parse(await fileText(sent[1][0])) as ReviewFile
 expect(review.requests).toHaveLength(1)
 expect(review.requests[0].text).toBe('Do not lose this concurrent comment')
 await act(async()=>finish(true))
})

it('does not erase a newer reopened draft when an abandoned submission finishes',async()=>{
 const {source,sent,finish,unmount}=setup()
 fireEvent.change(source,{target:{value:original+'Previous sitting.\n'}})
 fireEvent.click(screen.getByRole('button',{name:'Submit'}))
 await waitFor(()=>expect(sent).toHaveLength(1))
 unmount()
 const newer=original+'New sitting draft.\n'
 localStorage.setItem('chat.review.edit.document-lifecycle',newer)
 await act(async()=>finish(true))
 expect(localStorage.getItem('chat.review.edit.document-lifecycle')).toBe(newer)
})

it('preserves non-document anchors without adding document-only provenance fields',async()=>{
 const anchors=[
  {kind:'general' as const},
  {kind:'region' as const,page:1,rect:{x:0,y:0,w:.5,h:.5}},
  {kind:'text' as const,quote:'old code',diff:{file:'file.ts',side:'new' as const,start:1,end:1,hunk:1,fileIndex:1}},
 ]
 const existing:ReviewFile={version:1,file:{name:'report.md',artifactId:'older'},requests:anchors.map((anchor,index)=>({id:`legacy${index}`,anchor,text:`Old comment ${index}`,status:'open',createdAt:'2026-10-18T00:00:00Z'}))}
 const {source,sent,finish}=setup(existing)
 await screen.findByText('Old comment 0')
 fireEvent.change(source,{target:{value:original+'New body.\n'}})
 fireEvent.click(screen.getByRole('button',{name:'Submit'}))
 await waitFor(()=>expect(sent).toHaveLength(1))
 const review=JSON.parse(await fileText(sent[0][1])) as ReviewFile
 expect(review.requests.map(request=>request.anchor)).toEqual(anchors)
 await act(async()=>finish(true))
})
