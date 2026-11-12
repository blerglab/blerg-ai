import { describe, expect, it } from 'vitest'
import { newestOpenMilestone } from './blackboardMilestones'

describe('newestOpenMilestone', () => {
 it('defaults to the newest open milestone and skips terminal stages', () => {
  const milestones = [
   { id: 'old', frozen_at: null }, { id: 'new', frozen_at: null },
   { id: 'cutting', stage: 'Finalizing' }, { id: 'shipped', stage: 'Final' },
   { id: 'frozen', frozen_at: '2027-01-01' },
  ]
  expect(newestOpenMilestone(milestones)?.id).toBe('new')
  expect(milestones[0].id).toBe('old')
 })
 it('has no open selection when all milestones are closed or missing', () => {
  expect(newestOpenMilestone([])).toBeUndefined()
  expect(newestOpenMilestone([{ stage: 'Final' }, { frozen_at: '2027-01-01' }])).toBeUndefined()
 })
})
