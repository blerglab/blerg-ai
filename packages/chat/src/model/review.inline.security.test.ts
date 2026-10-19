import {describe,expect,it,vi} from 'vitest'
import {mergeReviews,parseReviewFile,type ReviewFile} from './review'
import {parsePatch} from './patch'
import {projectReviewAnchor} from './reviewProjection'
import {createDiffAnchor} from './diffSelection'

vi.mock('./diffSelection',async importOriginal=>{
 const actual=await importOriginal<typeof import('./diffSelection')>()
 return {...actual,createDiffAnchor:vi.fn(actual.createDiffAnchor)}
})

const file=():ReviewFile=>({version:1,file:{name:'changes.diff',artifactId:'patch'},requests:[{
 id:'one',text:'Check this',status:'open',revision:2,createdAt:'2026-01-01T12:00:00Z',
 anchor:{kind:'text',quote:'before',diff:{file:'src/example.ts',side:'new',start:1,end:1,hunk:1,fileIndex:1}},
}]})

describe('hostile inline review metadata',()=>{
 it.each<{status:unknown}>([{toString:null},{toString:'done'},{valueOf:'done'},['done'],null,{},42,true].map(status=>({status})))('rejects nonstring reply status without throwing: $status',({status})=>{
  const value=file() as unknown as {requests:Record<string,unknown>[]}
  value.requests[0].replies=[{status,text:'Earlier answer',createdAt:'2026-01-01T12:00:00Z'}]
  let parsed:unknown
  expect(()=>{parsed=parseReviewFile(JSON.stringify(value))}).not.toThrow()
  expect(parsed).toBeNull()
 })
 it('rejects a NUL in the suggested original even when replacement is ordinary text',()=>{
  const value=file()
  value.requests[0].anchor.quote='before\0code'
  value.requests[0].suggestion={original:'before\0code',replacement:'after'}
  expect(parseReviewFile(JSON.stringify(value))).toBeNull()
 })
 it('rejects general comments carrying a hidden patch locator',()=>{
  const value=file()
  value.requests[0].anchor.kind='general'
  expect(parseReviewFile(JSON.stringify(value))).toBeNull()
 })
 it('keeps the newer person answer and exact provenance against a stale agent copy',()=>{
  const person=file()
  person.requests[0].reply='Current answer'
  const agent=file()
  agent.requests[0]={...agent.requests[0],revision:1,status:'done',reply:'Stale answer',anchor:{kind:'general'}}
  const before=JSON.stringify([person,agent])
  const merged=mergeReviews(person,agent)!
  expect(merged.requests[0]).toMatchObject({revision:2,status:'open',reply:'Current answer',anchor:person.requests[0].anchor})
  expect(JSON.stringify([person,agent])).toBe(before)
 })
 it('bounds anchor construction while rejecting repeated first lines without a full quote match',()=>{
  const count=3000
  const patch=parsePatch('--- /dev/null\n+++ b/src/example.ts\n@@ -0,0 +1,'+count+' @@\n'+Array.from({length:count},()=>'+repeated').join('\n'))
  const anchor=file().requests[0].anchor
  anchor.quote='repeated\nmissing'
  anchor.diff={...anchor.diff!,start:1,end:2,artifactId:'old'}
  vi.mocked(createDiffAnchor).mockClear()
  expect(projectReviewAnchor(patch,anchor,'new')).toBeNull()
  expect(vi.mocked(createDiffAnchor).mock.calls.length).toBeLessThanOrEqual(2)
 })

})
