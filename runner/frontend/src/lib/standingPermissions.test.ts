import { userMessageShape } from '@blerglab/chat'
import { describe, expect, it } from 'vitest'
import { inactiveLabel, type ProposalGrant } from './sessionPermissions'

describe('standing permission state copy', () => {
  const grant: ProposalGrant = { id: 'g', kind: 'board_file', created_via: 'panel', created_at: '' }
  it('explains automatic restoration without Renew', () => {
    const text = inactiveLabel({ ...grant, inactive: 'suspended' })
    expect(text).toContain('automatically')
    expect(text).not.toMatch(/renew|allow it again/i)
  })
  it('explains how to repair an ownerless board', () => {
    const text = inactiveLabel({ ...grant, inactive: 'board_owner_missing' })
    expect(text).toContain('no recorded owner')
    expect(text).toMatch(/owner or an administrator.*assign an owner/i)
    expect(text).not.toMatch(/retry|expired|sign in/i)
  })
  it('keeps outages separate from expired authority', () => {
    expect(inactiveLabel({ ...grant, inactive: 'unavailable' })).toContain('could not be verified')
    expect(inactiveLabel({ ...grant, inactive: 'revoked' })).toContain('expired')
  })
})

it('renders permission updates as an existing system notice, never as the person', () => {
  expect(userMessageShape({ source: 'system', text: '[system] Ticket board permission is suspended.' }))
    .toEqual({ as: 'notice', text: 'Ticket board permission is suspended.' })
})
