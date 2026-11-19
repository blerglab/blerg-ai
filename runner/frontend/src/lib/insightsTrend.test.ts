import { expect, it } from 'vitest'
import { activityTrend, bucketSize, DAY, HOUR, periodAnswer, timeBuckets } from './insightsTrend'
import { emptyActivity } from '../test/activityFixture'

it('uses one time axis with explicit gaps and the requested aggregation',()=>{
 expect(bucketSize('2026-11-01','2026-11-02')).toBe(HOUR)
 expect(bucketSize('2026-10-26','2026-11-02')).toBe(DAY)
 expect(bucketSize('2026-08-04','2026-11-02')).toBe(7*DAY)
 expect(timeBuckets('2026-10-26','2026-11-02')).toHaveLength(7)
})
it('uses failure effective dates and retains the original attempt after a rerun',()=>{
 const d={...emptyActivity,from:'2026-10-26',to:'2026-11-02',failures:[{session_id:'a',board_id:'b',card:1,recorded_at:'2026-11-01',at:'2026-10-30',type:'test'},{session_id:'a',board_id:'b',card:1,recorded_at:'2026-11-01',type:'test',rerun_passed:true}]}
 const days=activityTrend(d,'health')
 expect(days.find(d=>d.at.startsWith('2026-10-30'))?.values).toEqual({test:1})
 expect(days.find(d=>d.at.startsWith('2026-11-01'))?.values).toEqual({})
})
it('does not invent dated work from lifetime totals',()=>{
 const s={id:'a',cards:['b#1'],parent_session_id:null,started_at:'2026-10-01',ended_at:null,shared_with_other_cards:false,timing:{seconds:{model:100},phases:{building:100},basis:'recorded',incomplete_tools:0}}
 const d={...emptyActivity,sessions:[s]}
 expect(activityTrend(d,'time').every(b=>Object.keys(b.values).length===0)).toBe(true)
 const hours:{at:string;phases:Record<string,number>;seconds:Record<string,number>}[]=[{at:'2026-10-25T23:00:00Z',phases:{building:30},seconds:{model:30}},{at:'2026-10-26T00:00:00Z',phases:{gates:70},seconds:{tests:70}}]
 const days=activityTrend({...d,sessions:[{...s,timing:{...s.timing,hours}}]},'time')
 expect(days.find(b=>b.at.startsWith('2026-10-25'))?.values).toEqual({building:30})
 expect(days.find(b=>b.at.startsWith('2026-10-26'))?.values).toEqual({gates:70})
})
it('compares equal periods in words and distinguishes unavailable evidence',()=>{
 const current=[{at:'2026-11-01',values:{gates:82}}],previous=[{at:'2026-10-25',values:{gates:100}}]
 expect(periodAnswer(current,previous,'Gate time','7 days',true)).toBe('Gate time is down 18% against the previous 7 days.')
 expect(periodAnswer(current,[],'Gate time','7 days',false)).toContain('comparison is unavailable')
 expect(periodAnswer(current,[],'Gate time','7 days',true)).toContain('no earlier activity')
})

it('counts only hours within each period for a conversation spanning both',()=>{
 const from='2026-10-26T15:00:00Z',to='2026-11-02T15:00:00Z'
 const hours=Array.from({length:24*16},(_,i)=>({at:new Date(Date.parse(to)-(i+1)*HOUR).toISOString(),seconds:{model:3600},phases:{building:3600}}))
 const session={...emptyActivity.sessions[0],id:'steady',cards:['b#1'],parent_session_id:null,started_at:hours.at(-1)!.at,ended_at:null,shared_with_other_cards:false,timing:{hours,seconds:{model:hours.length*3600},phases:{building:hours.length*3600},basis:'recorded',incomplete_tools:0}}
 const d={...emptyActivity,from,to,sessions:[session],history:{from:new Date(Date.parse(from)-7*DAY).toISOString(),sessions:[session],failures:[]}}
 for(const view of ['time','cost'] as const){
  const current=activityTrend(d,view),previous=activityTrend(d,view,d.history.from,from)
  expect(periodAnswer(current,previous,'Recorded time','7 days',true)).toContain('unchanged')
  expect(current.reduce((n,b)=>n+Object.values(b.values).reduce((a,v)=>a+v,0),0)).toBe(7*24*3600)
 }
 expect(activityTrend({...d,sessions:[{...session,native_active_time_unknown:true}]},'cost').every(b=>Object.keys(b.values).length===0)).toBe(true)
})
