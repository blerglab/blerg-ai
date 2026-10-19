import { describe, expect, it } from 'vitest'
import { composeReviewMessage } from './feedback'
import { emptyReview } from './review'

describe('code review instructions',()=>{
 it('asks for source changes and an updated patch through the existing reply flow',()=>{
  const review=emptyReview({name:'change.diff',artifactId:'d1'})
  review.requests.push({id:'r1',text:'Check conversion',status:'open',createdAt:'2026-10-17T00:00:00Z',anchor:{kind:'text',quote:'return int(x)',diff:{file:'src/file.go',side:'new',start:12,end:14,hunk:1,fileIndex:1}}})
  const message=composeReviewMessage(review,{name:'change.diff'})
  expect(message).toContain('source files named by its patch anchor')
  expect(message).toContain('updated diff with `blerg-runner diff <range>`')
  expect(message).toContain('review reply <id> done|declined')
  expect(message).not.toContain('in the file this was published from')
 })
})
