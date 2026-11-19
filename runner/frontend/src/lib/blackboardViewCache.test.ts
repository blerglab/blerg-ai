import { expect, it } from 'vitest'
import { blackboardViewKey, blackboardViewGeneration, cacheBlackboardView, cachedBlackboardView, invalidateBlackboardViews } from './blackboardViewCache'
import type { Data } from '../components/BlackboardTabs'
const data = {record:{name:'Plan'},slices:{},plain:{cards:[],complete:true}} as unknown as Data
it('keeps queries and blackboards separate and bounds retained views', () => {
 for(let i=0;i<21;i++) cacheBlackboardView(blackboardViewKey('b'+i,'?session=s'),data,blackboardViewGeneration())
 expect(cachedBlackboardView(blackboardViewKey('b0','?session=s'))).toBeNull()
 expect(cachedBlackboardView(blackboardViewKey('b20','?session=s'))).toBe(data)
 expect(cachedBlackboardView(blackboardViewKey('b20','?session=other'))).toBeNull()
})
it('prevents an in-flight read from restoring invalidated data', () => {
 const generation=blackboardViewGeneration(), key=blackboardViewKey('b','?session=s')
 cacheBlackboardView(key,data,generation)
 invalidateBlackboardViews(['s'])
 cacheBlackboardView(key,data,generation)
 expect(cachedBlackboardView(key)).toBeNull()
})
