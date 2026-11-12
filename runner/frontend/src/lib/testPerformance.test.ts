import {expect,it} from 'vitest'
import {performanceOverview,suiteFindings,suiteTrend,type PerformanceRun} from './testPerformance'
it('caps the newest chart observations inside native policy limits',()=>{
 const runs=Array.from({length:210},(_,i)=>({workspace_id:'workspace',member_id:'project',run_id:String(i),commit:'01234567',finished_at:new Date(2026,0,1,0,i).toISOString(),seconds:i,complete:true,region:null,suites:[{suite:'unit',seconds:i,outcome:'pass',counts:null,slowest:[]}]} satisfies PerformanceRun))
 expect(suiteTrend(runs,'')).toHaveLength(200)
})

const run=(id:string,date:string,seconds:number,overrides:Partial<PerformanceRun>={}):PerformanceRun=>({workspace_id:'workspace',member_id:'project',run_id:id,commit:null,finished_at:date,seconds,complete:true,region:null,suites:[{suite:'unit',seconds,outcome:'pass',counts:null,slowest:[]}],...overrides})
it('summarizes run wall time and states a comparable suite finding',()=>{
 const old=run('old','2026-11-01T00:00:00Z',8)
 const latest=run('new','2026-11-11T00:00:00Z',12,{suites:[{suite:'unit',seconds:12,outcome:'pass',counts:null,slowest:[]},{suite:'integration',seconds:11,outcome:'pass',counts:null,slowest:[]}]})
 const overview=performanceOverview([old,latest],'')
 expect(overview.latestSeconds).toBe(12)
 expect(overview.deltaPercent).toBe(50)
 expect(overview.finding).toBe('unit is 50% slower than 10 days ago')
 expect(overview.slowest?.seconds).toBe(12)
 expect(overview.count).toBe(2)
})
it('keeps project, region and cache boundaries out of comparisons',()=>{
 const old=run('old','2026-11-01T00:00:00Z',8,{workspace_id:'other',region:'internal/unit'})
 const latest=run('new','2026-11-11T00:00:00Z',12,{region:'internal/unit'})
 expect(performanceOverview([old,latest],'').deltaPercent).toBeNull()
 expect(performanceOverview([old,latest],'').finding).not.toContain('slower')
 expect(performanceOverview([{...old,workspace_id:'workspace',region:'elsewhere'},latest],'').deltaPercent).toBeNull()
 expect(performanceOverview([{...old,workspace_id:'workspace',member_id:'other'},latest],'').deltaPercent).toBeNull()
 const partial={...latest,complete:false,cached_packages:1}
 const overview=performanceOverview([{...old,workspace_id:'workspace',region:'internal/unit'},partial],'')
 expect(overview.deltaPercent).toBeNull()
 expect(overview.partial).toBe(true)
 expect(overview.finding).not.toContain('slower')
})
it('keeps zero baselines and missing samples unknown',()=>{
 expect(performanceOverview([],'').latestSeconds).toBeNull()
 const overview=performanceOverview([run('old','2026-11-01T00:00:00Z',0),run('new','2026-11-11T00:00:00Z',12)],'unit')
 expect(overview.deltaPercent).toBeNull()
 expect(overview.finding).not.toMatch(/Infinity|NaN/)
})

it('does not call a failed suite faster than its successful baseline',()=>{
 const latest=run('new','2026-11-11T00:00:00Z',1,{suites:[{suite:'unit',seconds:1,outcome:'fail',counts:null,slowest:[]}]})
 const overview=performanceOverview([run('old','2026-11-01T00:00:00Z',8),latest],'')
 expect(overview.deltaPercent).toBeNull()
 expect(overview.finding).not.toContain('faster')
})

it('does not compare successful suites from an incomplete run',()=>{
 const old=run('old','2026-11-01T00:00:00Z',8,{complete:false})
 const latest=run('new','2026-11-11T00:00:00Z',12)
 expect(performanceOverview([old,latest],'').finding).not.toContain('slower')
 expect(performanceOverview([{...old,complete:true},{...latest,complete:false}],'').finding).not.toContain('slower')
})

it('finds each suite slowdown within its project, region, layer and complete passing history',()=>{
 const old=run('old','2026-11-01T00:00:00Z',8,{suites:[{suite:'unit',seconds:8,outcome:'pass',counts:null,slowest:[],layer:'2',budget_seconds:20}]})
 const latest=run('new','2026-11-11T00:00:00Z',12,{suites:[{...old.suites[0],seconds:12,budget_seconds:10}]})
 const row=(items:PerformanceRun[])=>suiteFindings(items,'unit').rows.find(r=>r.workspace==='workspace'&&r.member==='project'&&r.region===null)!
 expect(row([latest,old])).toMatchObject({seconds:12,budgetSeconds:10,overrunSeconds:2,deltaPercent:50})
 for(const foreign of [{...old,workspace_id:'other'},{...old,member_id:'other'},{...old,region:'other'},{...old,complete:false},{...old,cached_packages:1},{...old,suites:[{...old.suites[0],layer:'3b'}]},{...old,suites:[{...old.suites[0],outcome:'fail'}]}])expect(row([foreign,latest]).deltaPercent).toBeNull()
 expect(row([old,{...latest,complete:false}]).deltaPercent).toBeNull()
 expect(row([old,{...latest,suites:[{...latest.suites[0],budget_seconds:null}]}]).overrunSeconds).toBeNull()
 expect(row([{...old,suites:[{...old.suites[0],seconds:0}]},latest]).deltaPercent).toBeNull()
 expect(suiteFindings([old,latest],'other').rows).toHaveLength(0)
})
it('bounds suite findings without losing the coverage count',()=>{
 const runs=Array.from({length:55},(_,i)=>run(String(i),'2026-11-01T00:00:00Z',1,{member_id:`project${i}`}))
 expect(suiteFindings(runs,'')).toMatchObject({total:55})
 expect(suiteFindings(runs,'').rows).toHaveLength(50)
})

it('puts over-budget suites first without inventing budgets or flakes',()=>{
 const sample=run('new','2026-11-11T00:00:00Z',15,{suites:[{suite:'within',seconds:2,budget_seconds:10,outcome:'pass',counts:null,slowest:[]},{suite:'over',seconds:15,budget_seconds:10,outcome:'pass',counts:null,slowest:[]}]})
 expect(suiteFindings([sample],'').rows[0].suite).toBe('over')
})
