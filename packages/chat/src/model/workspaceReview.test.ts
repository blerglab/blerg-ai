import { expect, it } from 'vitest'
import { workspaceReviewStore } from './workspaceReview'
import { clearViewCache } from './viewCache'
import type { TransportFiles } from '../transport/types'
import type { ReviewRequest } from './review'

const request:ReviewRequest = {id:'r',anchor:{kind:'text',quote:'one',workspace:{path:'app.ts',start:1,end:1,contentHash:'a'.repeat(64)}},text:'Change one',status:'open',createdAt:'now'}
it('shares drafts only within the same account, files transport and session',()=>{
  let account = 'first'
  const files = {queueScope:()=>account} as TransportFiles
  const store = workspaceReviewStore(files,'s')
  store.add(request)
  expect(workspaceReviewStore(files,'s')).toBe(store)
  expect(workspaceReviewStore(files,'other').snapshot().requests).toEqual([])
  account = 'second'
  expect(workspaceReviewStore(files,'s').snapshot().requests).toEqual([])
  expect(workspaceReviewStore({} as TransportFiles,'s').snapshot().requests).toEqual([])
  clearViewCache()
  account = 'first'
  expect(workspaceReviewStore(files,'s').snapshot().requests).toEqual([])
})
it('keeps local pending requests when the server replies and accepts replies after sending',()=>{
  const store = workspaceReviewStore({} as TransportFiles,'s')
  store.add(request)
  const review = {version:1 as const,file:{name:'Workspace files',artifactId:'workspace'},requests:[{...request,status:'done' as const,reply:'Fixed.'}]}
  store.merge(review)
  expect(store.snapshot().requests[0].status).toBe('open')
  store.update({pending:new Set()})
  store.merge(review)
  expect(store.snapshot().requests[0]).toMatchObject({status:'done',reply:'Fixed.'})
})
