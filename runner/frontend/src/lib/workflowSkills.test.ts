import { expect, it } from 'vitest'
import { withWorkflowSkills } from './workflowSkills'

it('shows the applied inventory in the existing skills group without duplicate discovery rows', () => {
  const report = { seq: 2, ts: '', payload: { engine: 'codex', groups: [{ id: 'skills' as const, label: 'Skills', total: 1, items: [{ name: 'ux-design', status: 'discovered' }, { name: 'project-skill' }] }] } }
  const merged = withWorkflowSkills(report, { 'test-driven-development': true, 'ux-design': false, visualizing: false, coordinating: false })!
  expect(merged.payload.groups[0].items.map(item => [item.name, item.status])).toEqual([
    ['test-driven-development', 'enabled'], ['ux-design', 'disabled'], ['visualizing', 'disabled'], ['coordinating', 'disabled'], ['project-skill', undefined],
  ])
  expect(report.payload.groups[0].items[0].status).toBe('discovered')
})
it('retains the original report when no launch inventory was recorded', () => {
  expect(withWorkflowSkills(null, null)).toBeNull()
  expect(withWorkflowSkills(null, { 'test-driven-development': true, 'ux-design': true, visualizing: true, coordinating: true })?.payload.groups[0].items).toHaveLength(4)
})
