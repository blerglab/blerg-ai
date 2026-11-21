import { expect, it } from 'vitest'
import { blackboardEventLabel, blackboardEventPayload } from './blackboardStyle'
it('renders a declined batch whose server event has no payload',()=>{
 expect(blackboardEventLabel({kind:'proposal_rejected',payload:null})).toBe('Proposal declined')
 expect(blackboardEventPayload(null)).toEqual({})
})
it('treats nested event assertions as data without coercing them into text',()=>{
 const hostile={reason:{toString:'not callable'},gate_result:{approved:true},number:{toString:'not callable'}}
 expect(blackboardEventPayload(hostile)).toEqual({reason:undefined,gate_result:undefined,number:undefined})
 expect(blackboardEventLabel({kind:'decision',payload:hostile})).toBe('Decision')
})
