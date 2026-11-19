import type { CapabilitiesReport } from './capabilities'

export type WorkflowPolicy = Record<'test-driven-development' | 'ux-design' | 'visualizing' | 'coordinating', boolean>
export const workflowNames = ['test-driven-development', 'ux-design', 'visualizing', 'coordinating'] as const

export function withWorkflowSkills(report: CapabilitiesReport | null, policy: WorkflowPolicy | null): CapabilitiesReport | null {
  if (!policy) return report
  const payload = report?.payload ?? { engine: 'unknown', engine_name: 'Blerg session guide', groups: [] }
  const previous = payload.groups.find(group => group.id === 'skills')
  const items = workflowNames.map(name => ({ name, source: 'Blerg', status: policy[name] ? 'enabled' : 'disabled' }))
  const others = previous?.items.filter(item => !workflowNames.some(name => name === item.name)) ?? []
  return { seq: report?.seq ?? 0, ts: report?.ts ?? '', payload: { ...payload, groups: [
    { ...previous, id: 'skills', label: 'Skills', total: undefined, items: [...items, ...others] },
    ...payload.groups.filter(group => group.id !== 'skills'),
  ] } }
}
