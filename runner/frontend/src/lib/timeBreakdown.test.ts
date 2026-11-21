import { expect,it } from 'vitest'
import { richActivity,emptyActivity } from '../test/activityFixture'
import { phaseSeconds,timeBreakdown } from './timeBreakdown'

const sum=(values:Record<string,number>)=>Object.values(values).reduce((a,n)=>a+n,0)
it.each(['gates','building'] as const)('reconciles %s by kind, worker, engine attribution and card',phase=>{
 const d=richActivity,result=timeBreakdown(d,phase,d.from,d.to)
 const parent=d.sessions.reduce((n,s)=>n+(s.timing?.hours||[]).reduce((n,h)=>n+phaseSeconds(h,phase),0),0)
 expect(result.total).toBeCloseTo(parent)
 expect(result.buckets.reduce((n,b)=>n+sum(b.values),0)).toBeCloseTo(parent)
 expect(result.cards.reduce((n,c)=>n+c.seconds,0)).toBeCloseTo(parent)
 for(const w of result.workers){expect(sum(w.parts)).toBeCloseTo(w.seconds);if(phase==='building')expect(w.first+w.rework+w.stageUnknown).toBeCloseTo(w.seconds)}
})
it.each(['gates','building'] as const)('keeps an empty %s period empty',phase=>{
 const result=timeBreakdown(emptyActivity,phase,emptyActivity.from,emptyActivity.to)
 expect(result.total).toBe(0);expect(result.workers).toEqual([]);expect(result.suites).toEqual([])
})
it('keeps all-unknown and single-kind periods honest',()=>{
 const session=richActivity.sessions[0],hour={at:'2026-10-25T10:00:00Z',seconds:{tests:100},phases:{gates:100,building:80},detail:undefined}
 const d={...emptyActivity,sessions:[{...session,timing:{...session.timing!,hours:[hour],gate_runs:undefined}}]}
 const unknown=timeBreakdown(d,'gates',d.from,d.to)
 expect(unknown.buckets.reduce((n,b)=>n+(b.values.unknown||0),0)).toBe(100)
 expect(unknown.suites).toEqual([]);expect(unknown.runTimingGap).toBe(100)
 const known=timeBreakdown({...d,sessions:[{...d.sessions[0],timing:{...d.sessions[0].timing!,hours:[{...hour,detail:{'gates.tests':100}}]}}]},'gates',d.from,d.to)
 expect(known.buckets.reduce((n,b)=>n+(b.values.tests||0),0)).toBe(100)
 expect(known.buckets.reduce((n,b)=>n+(b.values.unknown||0),0)).toBe(0)
 const building=timeBreakdown(d,'building',d.from,d.to)
 expect(building.workers[0].stageUnknown).toBe(80)
})
