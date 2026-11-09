import type { TimeBucket } from '../components/charts/TimeChart'
import type { ActivityReport } from './insightsActivity'

export const HOUR=3600_000, DAY=24*HOUR
export function bucketSize(from:string,to:string) { const span=Date.parse(to)-Date.parse(from);return span<DAY+HOUR?HOUR:span>=89*DAY?7*DAY:DAY }
export function timeBuckets(from:string,to:string, size=bucketSize(from,to)):TimeBucket[] {
 const origin=size===7*DAY?4*DAY:0
 const start=Math.floor((Date.parse(from)-origin)/size)*size+origin, end=Date.parse(to)
 if(!Number.isFinite(start)||!Number.isFinite(end)||end<start)return []
 return Array.from({length:Math.min(2200,Math.ceil((end-start)/size))},(_,i)=>({at:new Date(start+i*size).toISOString(),values:{}}))
}
export function addValue(buckets:TimeBucket[],at:string,key:string,value:number,size:number) {
 if(!Number.isFinite(value)||value<=0)return
 const bucket=buckets.find(b=>Date.parse(at)>=Date.parse(b.at)&&Date.parse(at)<Date.parse(b.at)+size)
 if(bucket)bucket.values[key]=(bucket.values[key]||0)+value
}
export function activityTrend(d:ActivityReport,view:'health'|'cost'|'time',from=d.from,to=d.to) {
 const size=bucketSize(from,to), buckets=timeBuckets(from,to,size)
 const past=Date.parse(from)<Date.parse(d.from)
 const start=Date.parse(from),end=Date.parse(to)
 if(view==='health')for(const f of (past?d.history?.failures||[]:d.failures))if(!f.rerun_passed&&Date.parse(f.at||f.recorded_at)>=start&&Date.parse(f.at||f.recorded_at)<end)addValue(buckets,f.at||f.recorded_at,f.type||'unknown',1,size)
 if(view!=='health')for(const s of (past?d.history?.sessions||[]:d.sessions)){
  if(view==='cost'&&(s.cards.length!==1||s.shared_with_other_cards||s.card_attribution_unknown||s.native_active_time_unknown))continue
  const card=d.cards.find(c=>c.key===s.cards[0])
  for(const h of s.timing?.hours||[]){
   if(Date.parse(h.at)<start||Date.parse(h.at)>=end)continue
   if(view==='time')for(const [phase,value]of Object.entries(h.phases))addValue(buckets,h.at,phase==='fixing'||phase==='waiting'?'building':phase,value,size)
   else for(const key of ['tests','build_lint','git','read','edit','other','model','turn_end'])addValue(buckets,h.at,card?.size||'Unknown size',h.seconds[key]||0,size)
  }
 }
 return buckets
}
export function periodAnswer(current:TimeBucket[],previous:TimeBucket[],subject:string,period:string,hasPrevious:boolean) {
 const sum=(rows:TimeBucket[])=>rows.reduce((n,b)=>n+Object.values(b.values).reduce((a,v)=>a+v,0),0)
 const now=sum(current),before=sum(previous)
 if(!hasPrevious)return `${subject}: the previous ${period} have not been recorded, so a comparison is unavailable.`
 if(!before)return now?`${subject}: no earlier activity was recorded for comparison.`:`No ${subject.toLowerCase()} in either period.`
 const change=Math.round((now-before)/before*100)
 const verb=subject==='Failures'?'are':'is'
 return change===0?`${subject} ${verb} unchanged against the previous ${period}.`:`${subject} ${verb} ${change>0?'up':'down'} ${Math.abs(change)}% against the previous ${period}.`
}
