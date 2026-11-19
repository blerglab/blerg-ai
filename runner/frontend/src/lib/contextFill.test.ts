import { expect, it } from 'vitest'
import { contextFillSeries } from './contextFill'

const from='2026-10-25T00:00:00Z', to='2026-10-26T00:00:00Z'

it('draws one series per conversation instead of mixing every conversation together',()=>{
 const series=contextFillSeries([
  {session_id:'main-1',title:'Improve ticket flow',role:'main',at:'2026-10-25T03:10:00Z',percent:20,compacted:false},
  {session_id:'worker-1',title:'Check phone layout',role:'worker',at:'2026-10-25T03:40:00Z',percent:80,compacted:false},
 ],from,to)
 expect(series).toHaveLength(2)
 const main=series.find(s=>s.sessionId==='main-1')!
 expect(main.role).toBe('main')
 expect(main.points).toEqual([{at:'2026-10-25T03:10:00Z',percent:20,compacted:false}])
 const worker=series.find(s=>s.sessionId==='worker-1')!
 expect(worker.title).toBe('Check phone layout')
 expect(worker.points).toEqual([{at:'2026-10-25T03:40:00Z',percent:80,compacted:false}])
})

it('keeps every real reading for a conversation, not a bucketed maximum',()=>{
 const series=contextFillSeries([
  {session_id:'a',title:'',role:'worker',at:'2026-10-25T03:10:00Z',percent:20,compacted:false},
  {session_id:'a',title:'',role:'worker',at:'2026-10-25T03:40:00Z',percent:80,compacted:false},
  {session_id:'a',title:'',role:'worker',at:'2026-10-25T05:00:00Z',percent:50,compacted:true},
 ],from,to)
 expect(series).toHaveLength(1)
 expect(series[0].points).toHaveLength(3)
 expect(series[0].points[1].percent).toBe(80)
 expect(series[0].points[2].compacted).toBe(true)
})

it('sorts a conversation\'s points chronologically regardless of input order',()=>{
 const series=contextFillSeries([
  {session_id:'a',title:'',role:'worker',at:'2026-10-25T05:00:00Z',percent:50,compacted:false},
  {session_id:'a',title:'',role:'worker',at:'2026-10-25T03:00:00Z',percent:10,compacted:false},
 ],from,to)
 expect(series[0].points.map(p=>p.percent)).toEqual([10,50])
})

it('clamps an out-of-range percent (a window misreport) to 0-100',()=>{
 const series=contextFillSeries([{session_id:'a',title:'',role:'worker',at:'2026-10-25T03:10:00Z',percent:140,compacted:false}],from,to)
 expect(series[0].points[0].percent).toBe(100)
})

it('drops a reading outside the requested range',()=>{
 const series=contextFillSeries([{session_id:'a',title:'',role:'worker',at:'2026-10-27T03:10:00Z',percent:50,compacted:false}],from,to)
 expect(series).toHaveLength(0)
})
