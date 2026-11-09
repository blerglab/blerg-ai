import { expect, it } from 'vitest'
import { blackboardEventLabel } from '../lib/blackboardStyle'
it('uses the blackboard style for timeline labels', () => {
 const event = {id:'event',milestone_id:'milestone',kind:'shipped',recorded_at:'2026-11-02',source:'person',payload:{}}
 expect(blackboardEventLabel(event,{name:'Continuous delivery',stages:[{key:'shipped',label:'Published'}]})).toBe('Published')
 expect(blackboardEventLabel({...event,kind:'alpha',payload:{number:2}})).toBe('Alpha 2')
})
