import { expect, it } from 'vitest'
import { answerResult } from './answerResult'
const wrap = (result:unknown) => ({type:'blerg:answer',result})
it('accepts shared references that serialize as ordinary JSON',()=>{
 const item={value:'a'}
 expect(answerResult(wrap([item,item]))).toBe('[{"value":"a"},{"value":"a"}]')
})
it('rejects cycles, deep values, and aggregate oversize results',()=>{
 const cycle: unknown[]=[];cycle.push(cycle)
 expect(answerResult(wrap(cycle))).toBeNull()
 let deep: unknown=null;for(let i=0;i<18;i++)deep=[deep]
 expect(answerResult(wrap(deep))).toBeNull()
 expect(answerResult(wrap(Array(3).fill('x'.repeat(8000))))).toBeNull()
 expect(answerResult(wrap('😀'.repeat(5000)))).toBeNull()
})
