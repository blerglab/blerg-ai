import { describe, expect, it } from 'vitest'
import { readReviewRequest, reviewOutcomeLine, reviewRequestStates } from './reviewRequests'
import type { AgentEvent } from '../types'

const base = { request_id: 'r1', artifact_id: 'a1', artifact_name: 'plan.md', artifact_version: 1, artifact_view: 'markdown', ask: 'OK?' }
const ev = (kind: string, payload: object, seq: number) => ({ kind, payload, seq, client_event_id: `e${seq}` }) as unknown as AgentEvent

describe('a dismissed review request (card #155)', () => {
  it('reads as settled and says it was dismissed, not approved', () => {
    const r = readReviewRequest({ ...base, state: 'dismissed' })
    expect(r?.state).toBe('dismissed')
    expect(reviewOutcomeLine(r!)).toBe('Dismissed (maybe answered in the chat)')
  })

  it('settles the pending request in the transcript', () => {
    const states = reviewRequestStates([ev('review_requested', { ...base, state: 'pending' }, 1), ev('review_decided', { ...base, state: 'dismissed' }, 2)])
    expect(states.get('r1')?.state).toBe('dismissed')
  })
})
