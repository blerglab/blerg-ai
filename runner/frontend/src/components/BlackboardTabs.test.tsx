import { expect, it } from 'vitest'
import { tabLabel, viewId, type SessionView } from '../hooks/useSessionTabs'
it('keeps Blackboard as the tab name at every level', () => {
 const project = { kind: 'blackboard', blackboardId: 'record', path: { level: 'project' }, label: 'Project' } as SessionView
 const thrust = { ...project, path: { level: 'thrust', milestoneId: 'release', thrustId: 'push' }, label: 'Reliable sessions' } as SessionView
 expect(viewId(project)).toBe(viewId(thrust))
 expect(tabLabel(project)).toBe('Blackboard')
 expect(tabLabel(thrust)).toBe('Blackboard')
})

it('duplicates preserve the path with an independent tab identity', () => {
 const view = { kind:'blackboard',blackboardId:'record',path:{level:'thrust',milestoneId:'m',thrustId:'t'},label:'Recover work' } as SessionView
 const copy = {...view, instance:'second'}
 expect(viewId(copy)).not.toBe(viewId(view))
 expect(tabLabel(copy)).toBe('Blackboard')
})
