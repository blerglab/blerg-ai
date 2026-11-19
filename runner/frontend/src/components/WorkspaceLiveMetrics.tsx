import { useEffect, useState } from 'react'
import { IconButton } from '@blerglab/chat'
import { apiFetch } from '../apiFetch'
import { formatDuration, formatPercent } from '../lib/insights'
import type { ActivitySession } from '../lib/insightsActivity'
import { SessionStateGlyph } from './SessionCard'

interface Run { id:string; session_id:string; suite:string; state:string; started_at:string; ended_at?:string|null }
interface Metrics {
 generated_at:string
 workers:{session_id:string;status:string;age_seconds:number;gate_fraction:number;turn_count:number;turns_truncated:boolean}[]
 runs:Run[]|null
 machine:{at:string;load_1:number;cpus:number;disk_free_bytes:number}|null
}

// The same owner-only read Main uses on demand; no new polling endpoint or
// inferred process list. Registered runs describe only explicitly reported work.
export default function WorkspaceLiveMetrics({workspace,sessions}:{workspace:string;sessions:ActivitySession[]}) {
 const [answer,setAnswer]=useState<{workspace:string;data:Metrics}|null>(null)
 const [failed,setFailed]=useState(false)
 const [retry,setRetry]=useState(0)
 useEffect(()=>{
  const controller=new AbortController()
  const read=()=>apiFetch(`/api/sessions/${encodeURIComponent(workspace)}/metrics`,{signal:controller.signal})
   .then(async r=>{if(!r.ok)throw new Error();return r.json() as Promise<Metrics>})
   .then(data=>{if(!controller.signal.aborted){setAnswer({workspace,data});setFailed(false)}})
   .catch(()=>{if(!controller.signal.aborted)setFailed(true)})
  void read()
  const timer=setInterval(()=>{if(document.visibilityState==='visible')void read()},30_000)
  return()=>{controller.abort();clearInterval(timer)}
 },[workspace,retry])
 const data=answer?.workspace===workspace?answer.data:null
 const names=new Map(sessions.map(s=>[s.id,s.title||'Untitled conversation']))
 const machine=data?.machine
 const fresh=!!machine&&!!data&&Date.parse(data.generated_at)-Date.parse(machine.at)<=300_000
 return <section><h2>Running work</h2>
  {failed&&<p role="alert" className="insights-note">Could not refresh live metrics. <IconButton label="Retry live metrics" onClick={()=>setRetry(n=>n+1)}/></p>}
  {!data&&!failed&&<p role="status" className="insights-note">Loading live metrics…</p>}
  {data&&<>
   <p className="insights-note">Updated {new Date(data.generated_at).toLocaleTimeString()}. Refreshes every 30 seconds while visible. Live state covers the whole workspace; cohort filters apply to recorded time and outcomes. Gate shares cover recorded turns.</p>
   <ul className="activity-workers">{(data.workers??[]).slice(0,50).map(w=><li key={w.session_id}><div className="activity-worker-heading"><SessionStateGlyph status={w.status==='blocked'?'waiting':w.status==='failed'?'error':w.status} label={stateLabel(w.status)}/><a href={`/sessions/${encodeURIComponent(w.session_id)}`}>{names.get(w.session_id)||'Conversation'}</a></div><span>Last status update {formatDuration(w.age_seconds)} ago · {w.turn_count?`${formatPercent(w.gate_fraction)} in gates across ${w.turn_count} recorded turns`:'Gate share not recorded'}</span></li>)}</ul>
   {(data.workers??[]).length>50&&<p className="insights-note">Showing 50 of {(data.workers??[]).length} conversations.</p>}
   <h3>Registered runs</h3>
   {(data.runs??[]).some(r=>r.state==='running')?<ul className="activity-records">{(data.runs??[]).filter(r=>r.state==='running').slice(0,50).map(r=>{
    const samples=(data.runs??[]).filter(p=>p.suite===r.suite&&p.state==='finished'&&p.ended_at).map(p=>(Date.parse(p.ended_at!)-Date.parse(p.started_at))/1000).filter(n=>Number.isFinite(n)&&n>=0).sort((a,b)=>a-b)
    const middle=(samples.length-1)/2,median=(samples[Math.floor(middle)]+samples[Math.ceil(middle)])/2
    return <li key={`${r.session_id}:${r.id}`}>{r.suite} · {names.get(r.session_id)||'Conversation'} · {formatDuration(Math.max(0,(Date.parse(data.generated_at)-Date.parse(r.started_at))/1000))} elapsed · {samples.length?`median ${formatDuration(median)}, n ${samples.length}${samples.length<5?' (small group)':''}`:'Usual duration not recorded'}</li>
   })}</ul>:<p className="insights-note">No runs are currently registered as running.</p>}
   <h3>Machine</h3>
   {fresh?<p className="insights-note">Load {machine.load_1.toFixed(1)} across {machine.cpus} CPUs · {(machine.disk_free_bytes/(1024**3)).toFixed(1)} GiB free · sampled {new Date(machine.at).toLocaleTimeString()}.</p>:<p className="insights-note">No recent machine sample is available.</p>}
  </>}
 </section>
}

function stateLabel(status:string):string {
 return ({running:'Working',idle:'Idle',blocked:'Waiting for an answer',waiting:'Waiting for an answer',failed:'Failed',error:'Failed',ended:'Ended',stopped:'Stopped',starting:'Starting',disconnected:'Paused'} as Record<string,string>)[status]||'State unavailable'
}
