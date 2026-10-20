import { describe, it, expect } from 'vitest'
import { diffArtifacts, hasPlanTab, planList, planSummary, planTabLabel } from './railPlan'

const item = (text: string, done: boolean) => ({ text, done })
const art = (id: string, view: string, at: string, extra = {}) => ({ id, name: `${id}.diff`, size: 1, content_type: 'text/x-diff', view, created_at: at, version: 1, latest_version: 1, ...extra })

describe('railPlan', () => {
  it('summarises all explicitly doing items before next', () => {
    const l = planList({items:[{text:'Next',done:false},{text:'Build',done:false,in_progress:true},{text:'Review',done:false,in_progress:true}]} as never,null)!
    expect(planSummary(l)).toBe('Checklist · 0 of 3 · Doing: Build; Review')
  })
  it('summarises a checklist with its next item', () => {
    const l = planList({ items: [item('a', true), item('b', false), item('c', false)] }, null)!
    expect(planSummary(l)).toBe('Checklist · 1 of 3 · Next: b')
    expect(planTabLabel(l, 0, 0)).toBe('1/3')
  })
  it('says all done', () => {
    const l = planList({ items: [item('a', true), item('b', true)] }, null)!
    expect(planSummary(l)).toBe('Checklist · all 2 done')
  })
  it('uses the in-progress plan item active text', () => {
    const l = planList(null, { items: [{ text: 'Build', status: 'completed' }, { text: 'Test it', active: 'Testing it', status: 'in_progress' }, { text: 'Ship', status: 'pending' }] } as never)!
    expect(planSummary(l)).toBe('Plan · 1 of 3 · Next: Testing it')
  })
  it('counts tickets and diffs when there is no list', () => {
    expect(planTabLabel(null, 1, 2)).toBe('3')
    expect(planTabLabel(null, 0, 0)).toBeNull()
    expect(hasPlanTab(null, 0, 0)).toBe(false)
    expect(hasPlanTab(null, 0, 1)).toBe(true)
  })
  it('lists diffs newest first, skipping other files and missing ones', () => {
    const out = diffArtifacts([art('old', 'diff', '2026-10-01T10:00:00Z'), art('doc', 'markdown', '2026-10-03T10:00:00Z'), art('new', 'diff', '2026-10-02T10:00:00Z'), art('gone', 'diff', '2026-10-04T10:00:00Z', { missing: true })])
    expect(out.map(a => a.id)).toEqual(['new', 'old'])
  })
})
