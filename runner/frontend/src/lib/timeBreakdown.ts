import type { ActivityHour, ActivityReport, ActivitySession, GateRunBucket } from './insightsActivity'
import { addValue, bucketSize, timeBuckets } from './insightsTrend'

export type TimePhase='gates'|'building'
export const PARTS={gates:{tests:'Tests',build_lint:'Build and lint',commit_hooks:'Commit and hooks',unknown:'Kind not recorded'},building:{model:'Model time',tools:'Tool time',waiting:'Recorded waiting',unknown:'Split not recorded'}}
export function phaseSeconds(h:ActivityHour,phase:TimePhase){return phase==='gates'?(h.phases.gates||0):(h.phases.building||0)+(h.phases.fixing||0)+(h.phases.waiting||0)}
const within=(at:string,from:string,to:string)=>Date.parse(at)>=Date.parse(from)&&Date.parse(at)<Date.parse(to)
export function hourParts(h:ActivityHour,phase:TimePhase){
 const parent=phaseSeconds(h,phase),values:Record<string,number>={}
 let known=0
 for(const key of Object.keys(PARTS[phase]).filter(k=>k!=='unknown')){const value=h.detail?.[`${phase}.${key}`]||0;values[key]=Math.max(0,value);known+=values[key]}
 // Missing joint timing is a gap, never inferred from unrelated tool totals.
 if(known>parent+1e-6)return {unknown:parent}
 values.unknown=Math.max(0,parent-known)
 return values
}
export function timeBreakdown(d:ActivityReport,phase:TimePhase,from:string,to:string){
 const sessions=Date.parse(from)<Date.parse(d.from)?d.history?.sessions||[]:d.sessions
 const size=bucketSize(d.from,d.to),buckets=timeBuckets(from,to,size)
 const workers=sessions.map(s=>{
  const hours=(s.timing?.hours||[]).filter(h=>within(h.at,from,to)),parts:Record<string,number>={}
  let seconds=0,first=0,rework=0
  for(const h of hours){seconds+=phaseSeconds(h,phase);first+=h.detail?.['building.first']||0;rework+=h.detail?.['building.rework']||0;for(const [key,value]of Object.entries(hourParts(h,phase))){parts[key]=(parts[key]||0)+value;addValue(buckets,h.at,key,value,size)}}
  if(first+rework>seconds+1e-6){first=0;rework=0}
  return {session:s,seconds,parts,first,rework,stageUnknown:Math.max(0,seconds-first-rework)}
 }).filter(w=>w.seconds>0).sort((a,b)=>b.seconds-a.seconds)
 const runs=sessions.flatMap(s=>(s.timing?.gate_runs||[]).filter(r=>within(r.at,from,to)))
 const suites=new Map<string,GateRunBucket>()
 for(const run of runs){const key=[run.region,run.suite||'Suite not recorded'].filter(Boolean).join(' · ');const row=suites.get(key);if(!row){suites.set(key,{...run,suite:key});continue}for(const field of ['seconds','runs','passed','failed','unknown_outcome','repeated','reruns','repeat_unknown','failed_seconds'] as const)row[field]+=run[field]||0;row.count_unknown ||= run.count_unknown}
 const total=workers.reduce((n,w)=>n+w.seconds,0),attributed=runs.reduce((n,r)=>n+r.seconds,0)
 const cards=new Map<string,{key:string;seconds:number;first:number;rework:number;unknown:number}>()
 for(const w of workers){const s=w.session,key=attributableCard(s)||'Card not attributable';const row=cards.get(key)||{key,seconds:0,first:0,rework:0,unknown:0};row.seconds+=w.seconds;row.first+=w.first;row.rework+=w.rework;row.unknown+=w.stageUnknown;cards.set(key,row)}
 return {buckets,workers,total,suites:[...suites.values()].sort((a,b)=>b.seconds-a.seconds),runTimingGap:Math.max(0,total-attributed),cards:[...cards.values()].sort((a,b)=>b.seconds-a.seconds)}
}
export function attributableCard(s:ActivitySession){return s.cards.length===1&&!s.shared_with_other_cards&&!s.card_attribution_unknown?s.cards[0]:null}
