import { describe, expect, it } from 'vitest'
import { proposalExpiring, proposalOverdue, proposalsWaiting, serverClockOffset, PROPOSAL_WARNING_MS } from './proposalAttention'
import type { ProposalInfo } from '../types'

describe('proposal expiry warnings', () => {
  it('warns only before a pending deadline, including the start of the last day', () => {
    const now = Date.now()
    const p = (delta: number, state = 'pending') => ({ state, expires_at: new Date(now + delta).toISOString() }) as ProposalInfo
    expect(proposalExpiring(p(PROPOSAL_WARNING_MS), now)).toBe(true)
    expect(proposalExpiring(p(PROPOSAL_WARNING_MS + 1), now)).toBe(false)
    expect(proposalExpiring(p(1), now)).toBe(true)
    expect(proposalExpiring(p(0), now)).toBe(false)
    expect(proposalExpiring(p(-1), now)).toBe(false)
    expect(proposalExpiring(p(1000, 'done'), now)).toBe(false)
    expect(proposalExpiring({ state: 'pending', expires_at: 'invalid' } as ProposalInfo, now)).toBe(false)
  })
})

describe('proposals past their deadline', () => {
  const now = Date.parse('2026-10-21T12:00:00Z')
  const p = (id: string, delta: number, state = 'pending') => ({ id, state, expires_at: new Date(now + delta).toISOString() }) as ProposalInfo
  it('are overdue only while pending, from the deadline on', () => {
    expect(proposalOverdue(p('a', -1), now)).toBe(true)
    expect(proposalOverdue(p('b', 0), now)).toBe(true)
    expect(proposalOverdue(p('c', 1), now)).toBe(false)
    expect(proposalOverdue(p('d', -1, 'executing'), now)).toBe(false)
    expect(proposalOverdue({ state: 'pending', expires_at: 'invalid' } as ProposalInfo, now)).toBe(false)
  })
  it('are left out of what waits on the person', () => {
    expect(proposalsWaiting([p('a', -60_000), p('b', 60_000), p('c', 86400_000)], now).map(x => x.id)).toEqual(['b', 'c'])
  })
})

describe('the server clock offset', () => {
  it('is this client\'s clock minus the server\'s, and 0 without a readable server time', () => {
    const client = Date.parse('2026-10-21T12:10:00Z')
    expect(serverClockOffset('2026-10-21T12:00:00Z', client)).toBe(10 * 60_000)
    expect(serverClockOffset(undefined, client)).toBe(0)
    expect(serverClockOffset('soon', client)).toBe(0)
  })
})
