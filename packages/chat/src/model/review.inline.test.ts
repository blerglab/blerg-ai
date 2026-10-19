import {describe,expect,it} from 'vitest'
import {emptyReview,mergeReviews,parseReviewFile,type ReviewFile,type ReviewRequest} from './review'
import {parsePatch} from './patch'
import {projectReviewAnchor} from './reviewProjection'
const request:ReviewRequest={id:'r1',anchor:{kind:'text',quote:'after',diff:{file:'main.ts',side:'new',start:1,end:1,hunk:1,fileIndex:1,artifactId:'old'}},text:'Change this',status:'open',createdAt:'2026-01-01T00:00:00Z'}
const review=(r:ReviewRequest):ReviewFile=>({...emptyReview({name:'change.diff',artifactId:'old'}),requests:[r]})
describe('inline review model',()=>{
 it('roundtrips general comments and new-side code suggestions',()=>{
  const value={...review({...request,suggestion:{original:'after',replacement:'better'}}),requests:[{...request,suggestion:{original:'after',replacement:'better'}},{...request,id:'general',anchor:{kind:'general'}}]}
  expect(parseReviewFile(JSON.stringify(value))).toEqual(value)
 })
 it('refuses suggestions without an exact new-side code anchor',()=>{
  for(const anchor of [{kind:'general'},{...request.anchor,diff:{...request.anchor.diff,side:'old'}},{...request.anchor,quote:'other'}]) expect(parseReviewFile(JSON.stringify(review({...request,anchor,suggestion:{original:'after',replacement:'better'}} as ReviewRequest)))).toBeNull()
 })
 it('keeps a reopened revision open until the agent replies to that revision',()=>{
  const person=review({...request,status:'open',revision:1})
  const old=review({...request,status:'done',reply:'Fixed',revision:0})
  expect(mergeReviews(person,old)?.requests[0].status).toBe('open')
  expect(mergeReviews(person,review({...old.requests[0],revision:1}))?.requests[0].status).toBe('done')
 })
 it('projects unique same-file quotes across revisions and refuses ambiguous or changed code',()=>{
  const patch=parsePatch('diff --git a/main.ts b/main.ts\n--- a/main.ts\n+++ b/main.ts\n@@ -10,1 +10,1 @@\n-before\n+after\n')
  expect(projectReviewAnchor(patch,request.anchor,'current')?.diff).toMatchObject({start:10,end:10,artifactId:'current'})
  expect(projectReviewAnchor(parsePatch('diff --git a/main.ts b/main.ts\n--- a/main.ts\n+++ b/main.ts\n@@ -1,1 +1,2 @@\n-before\n+after\n+after\n'),request.anchor,'current')).toBeNull()
  expect(projectReviewAnchor(parsePatch('diff --git a/main.ts b/main.ts\n--- a/main.ts\n+++ b/main.ts\n@@ -1,1 +1,1 @@\n-before\n+changed\n'),request.anchor,'current')).toBeNull()
 })
})
