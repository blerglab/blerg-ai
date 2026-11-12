export const OPEN_TEST_PERFORMANCE = 'blerg-open-test-performance'
export type PerformanceSuite = {flakes?:number|null;layer?:string;budget_seconds?:number|null;suite:string;seconds:number;outcome:string;counts:{pass:number;fail:number;skip:number}|null;slowest:{package:string;test:string;seconds:number;result:string}[]}
export type PerformanceRun = {workspace_id:string;member_id:string;run_id:string;commit:string|null;finished_at:string;seconds:number;complete:boolean;cached_packages?:number;region:string|null;suites:PerformanceSuite[]}
export type PerformanceProject={workspace_id:string;member_id:string;name?:string}
export type PerformanceResponse = {runs:PerformanceRun[];truncated:boolean;scan_limited?:boolean;next_cursor?:string|null;projects?:PerformanceProject[];projects_truncated?:boolean}
export function openTestPerformance(workspace?:string) {window.dispatchEvent(new CustomEvent(OPEN_TEST_PERFORMANCE,{detail:{workspace}}))}
export function suiteTrend(runs:PerformanceRun[],suite:string) {
 const points=runs.flatMap(r=>r.suites.filter(s=>!suite||s.suite===suite).map(s=>({date:r.finished_at,suite:s.suite,series:`${r.member_id} · ${r.workspace_id.slice(0,8)} · ${s.suite}`,seconds:s.seconds,commit:r.commit?.slice(0,8)??'Uncommitted',outcome:s.outcome,coverage:r.cached_packages?`Partial run · ${r.cached_packages} cached packages omitted`:r.complete?'Complete run':'Incomplete run'}))).sort((a,b)=>Date.parse(b.date)-Date.parse(a.date))
 const series=new Set<string>()
 return points.filter(p=>{if(!series.has(p.series)&&series.size>=8)return false;series.add(p.series);return true}).slice(0,200).reverse()
}

export function performanceOverview(runs:PerformanceRun[],suite:string) {
 const ordered=runs.filter(r=>!suite||r.suites.some(s=>s.suite===suite)).sort((a,b)=>Date.parse(b.finished_at)-Date.parse(a.finished_at))
 const latest=ordered[0]
 const value=(r:PerformanceRun)=>suite?r.suites.find(s=>s.suite===suite)?.seconds??null:r.seconds
 const slowest=latest?.suites.filter(s=>!suite||s.suite===suite).sort((a,b)=>b.seconds-a.seconds)[0]??null
 const result={latestSeconds:latest?value(latest):null,partial:latest?!latest.complete:false,deltaPercent:null as number|null,slowest:slowest?{suite:slowest.suite,seconds:slowest.seconds}:null,finding:'',count:runs.length}
 if(!latest)return result
 const passed=(r:PerformanceRun)=>r.suites.filter(s=>!suite||s.suite===suite).every(s=>s.outcome==='pass')
 const comparable=ordered.filter(r=>passed(r)&&r.workspace_id===latest.workspace_id&&r.member_id===latest.member_id&&r.region===latest.region&&r.complete&&!r.cached_packages&&Date.parse(r.finished_at)<Date.parse(latest.finished_at))
 const baseline=comparable.at(-1)
 const before=baseline?value(baseline):null
 if(passed(latest)&&latest.complete&&!latest.cached_packages&&before!==null&&before>0&&result.latestSeconds!==null)result.deltaPercent=(result.latestSeconds-before)/before*100
 if(latest.cached_packages){result.finding='Latest run has partial test coverage';return result}
 type Observation={run:PerformanceRun;suite:PerformanceSuite}
 const groups=new Map<string,Observation[]>()
 for(const r of ordered){
  if(!r.complete||r.cached_packages)continue
  for(const s of r.suites.filter(s=>(!suite||s.suite===suite)&&s.outcome==='pass')){
   const key=JSON.stringify([r.workspace_id,r.member_id,r.region,s.suite])
   groups.set(key,[...(groups.get(key)??[]),{run:r,suite:s}])
  }
 }
 const changes=[...groups.values()].flatMap(group=>{
  const newest=group[0],oldest=group.at(-1)!
  if(group.length<2||oldest.suite.seconds<=0||Date.parse(newest.run.finished_at)<=Date.parse(oldest.run.finished_at))return []
  return [{newest,oldest,percent:(newest.suite.seconds-oldest.suite.seconds)/oldest.suite.seconds*100}]
 }).filter(change=>change.newest.run.run_id===latest.run_id&&change.newest.run.workspace_id===latest.workspace_id&&change.newest.run.member_id===latest.member_id).sort((a,b)=>Math.abs(b.percent)-Math.abs(a.percent))
 const change=changes[0]
 if(change){
  const multiple=new Set(ordered.map(r=>`${r.workspace_id}/${r.member_id}`)).size>1
  const label=`${multiple?`${change.newest.run.member_id} · ${change.newest.run.workspace_id.slice(0,8)} · `:''}${change.newest.suite.suite}${change.newest.run.region?` (${change.newest.run.region})`:''}`
  const days=Math.round((Date.parse(change.newest.run.finished_at)-Date.parse(change.oldest.run.finished_at))/86400000)
  const ago=days>=1?`${days} ${days===1?'day':'days'} ago`:'earlier in this window'
  result.finding=Math.abs(change.percent)<1?`${label} changed less than 1% in this window`:`${label} is ${Math.round(Math.abs(change.percent))}% ${change.percent>0?'slower':'faster'} than ${ago}`
 }else result.finding=slowest?`${slowest.suite} took ${slowest.seconds.toFixed(1)} seconds in the latest run`:`Latest run took ${latest.seconds.toFixed(1)} seconds`
 return result
}


export function suiteFindings(runs:PerformanceRun[],suite:string) {
 type Sample={run:PerformanceRun;suite:PerformanceSuite}
 const groups=new Map<string,Sample[]>()
 const ordered=[...runs].sort((a,b)=>Date.parse(b.finished_at)-Date.parse(a.finished_at))
 for(const run of ordered)for(const sample of run.suites){
  if(suite&&sample.suite!==suite)continue
  const key=JSON.stringify([run.workspace_id,run.member_id,run.region,sample.suite])
  const group=groups.get(key)??[];group.push({run,suite:sample});groups.set(key,group)
 }
 const rows=[...groups.entries()].map(([key,group])=>{
  const latest=group[0],s=latest.suite,r=latest.run
  const usable=(sample:Sample)=>sample.run.complete&&!sample.run.cached_packages&&sample.suite.outcome==='pass'&&(sample.suite.layer??'')===(s.layer??'')
  const baseline=group.filter(sample=>usable(sample)&&Date.parse(sample.run.finished_at)<Date.parse(r.finished_at)).at(-1)
  const delta=usable(latest)&&baseline&&baseline.suite.seconds>0?(s.seconds-baseline.suite.seconds)/baseline.suite.seconds*100:null
  const budget=s.budget_seconds!==null&&s.budget_seconds!==undefined&&Number.isFinite(s.budget_seconds)&&s.budget_seconds>0?s.budget_seconds:null
  return {flakes:s.flakes??null,key,workspace:r.workspace_id,member:r.member_id,region:r.region,suite:s.suite,layer:s.layer??null,seconds:s.seconds,budgetSeconds:budget,overrunSeconds:budget===null?null:Math.max(0,s.seconds-budget),deltaPercent:delta,baselineAt:baseline?.run.finished_at??null,partial:!r.complete||!!r.cached_packages,outcome:s.outcome}
 })
 rows.sort((a,b)=>(b.overrunSeconds??0)-(a.overrunSeconds??0))
 return {rows:rows.slice(0,50),total:rows.length}
}
