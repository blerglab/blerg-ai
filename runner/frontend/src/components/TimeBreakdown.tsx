import type { ActivityReport, GateRunBucket } from '../lib/insightsActivity'
import { formatDuration, formatTick, partialReportNote } from '../lib/insights'
import { HOUR, bucketSize, periodAnswer } from '../lib/insightsTrend'
import { PARTS, timeBreakdown, type TimePhase } from '../lib/timeBreakdown'
import TimeChart, { type TimeEvent } from './charts/TimeChart'

const ACTIONS:Record<string,string>={tests:'Review the suite scope and repeated full runs.',build_lint:'Check build setup and whether outputs can be reused.',commit_hooks:'Inspect hooks that repeat checks. This includes Git commit time; hook time is not measured separately.',model:'Compare model time across engines and workers before changing assignments.',tools:'Open the worker to inspect repeated commands and file work.',waiting:'Check whether workers or background work can start earlier. Wait tools include deliberate pauses; the wait target may not be recorded.',unknown:'Earlier timing did not record this split. New evidence will fill the gap.'}
const WORKER_ACTIONS:Record<string,string>={tests:'Scope repeated suites to the files this worker changes.',build_lint:'Check whether this worker can reuse build outputs.',commit_hooks:'Check whether this worker’s hooks repeat completed checks.',model:'Review the model choice for this worker’s tasks.',tools:'Check this worker for repeated commands before adding parallel work.',waiting:'Start dependencies before this worker reaches its wait.',unknown:'Capture tool and status evidence before changing this worker.'}
const cardName=(key:string)=>key.includes('#')?`#${key.split('#').at(-1)}`:key
const engineName=(key:string)=>key==='claude'?'Claude':key==='codex'?'Codex':key||'Engine not recorded'

export default function TimeBreakdown({activity:d,phase,from,to,previousFrom,period,events,workspace,onClose}:{activity:ActivityReport;phase:TimePhase;from:string;to:string;previousFrom:string;period:string;events:TimeEvent[];workspace?:string;onClose:()=>void}){
 const current=timeBreakdown(d,phase,from,to),previous=timeBreakdown(d,phase,previousFrom,from)
 const label=phase==='gates'?'Gate time':'Building time'
 const totals:Record<string,number>={}
 for(const b of current.buckets)for(const [key,n]of Object.entries(b.values))totals[key]=(totals[key]||0)+n
 const engines=new Map<string,{seconds:number;parts:Record<string,number>}>()
 for(const w of current.workers){const key=w.session.engine||'',row=engines.get(key)||{seconds:0,parts:{}};row.seconds+=w.seconds;for(const [k,n]of Object.entries(w.parts))row.parts[k]=(row.parts[k]||0)+n;engines.set(key,row)}
 const parts=(values:Record<string,number>)=>Object.entries(PARTS[phase]).filter(([key])=>values[key]>0).map(([key,label])=>`${label}: ${formatDuration(values[key])}`).join(' · ')
 const largestPart=(values:Record<string,number>)=>Object.entries(values).sort((a,b)=>b[1]-a[1])[0]?.[0]||'unknown'
 return <section className="insights-phase-breakdown" data-testid={`time-breakdown-${phase}`} aria-label={`${label} breakdown`}>
  <div style={{display:'flex',alignItems:'baseline',gap:12}}><h2>{label}</h2><button className="insights-sort" aria-label="Close breakdown" title="Close breakdown" onClick={onClose}><span aria-hidden="true">✕</span></button></div>
  <p className="insights-answer">{d.pending_sessions?`${label} comparison is incomplete while older timing is being prepared.`:d.truncated||d.omitted_cards?`${label} cannot be compared reliably. ${partialReportNote(d.truncated,d.omitted_cards,workspace)}`:periodAnswer(current.buckets,previous.buckets,label,period,!!d.history)}</p>
  <TimeChart buckets={current.buckets} series={Object.entries(PARTS[phase]).map(([key,label])=>({key,label,description:ACTIONS[key]}))} label={`${label} breakdown`} events={events} format={formatTick} now={d.to} hourly={bucketSize(d.from,d.to)===HOUR}/>
  <p className="insights-note">{formatDuration(current.total)} in total. Every breakdown includes unrecorded time as a gap, so it adds up to the selected total.</p>
  {!current.total?(!d.pending_sessions&&<p className="insights-note">No dated {phase==='gates'?'gate':'building'} time was recorded in this period.</p>):<>
   <ul className="activity-records">{Object.entries(PARTS[phase]).filter(([key])=>totals[key]>0).map(([key,label])=><li key={key}><strong>{label} · {formatDuration(totals[key])}</strong><p className="insights-note">{ACTIONS[key]}</p></li>)}</ul>
   {phase==='gates'&&<>
    <h3>Slowest suites and regions</h3>
    <p className="insights-note">Runs are counted when they start in this period. Outcomes and repeats need explicit evidence; an unknown outcome is not a pass. Repeats use the same recorded command in one conversation; a rerun follows a failed run.</p>
    {current.suites.length>0&&<ol className="stack">{current.suites.slice(0,10).map((r,i)=><GateSuiteRow key={i} run={r}/>)}</ol>}
    {current.runTimingGap>1e-6&&<p className="insights-note">{formatDuration(current.runTimingGap)} has no run record. Suite, outcome and repeat counts for that time are unavailable; no run count is estimated.</p>}
    {current.suites.length>10&&<p className="insights-note">Showing the 10 slowest of {current.suites.length} suites and regions.</p>}
   </>}
   {phase==='building'&&<><h3>By engine</h3><ul className="activity-records">{[...engines].sort((a,b)=>b[1].seconds-a[1].seconds).map(([engine,row])=><li key={engine}><strong>{engineName(engine)} · {formatDuration(row.seconds)}</strong><p>{parts(row.parts)}</p><p className="insights-note">Compare the largest part before changing the worker or model assignment.</p></li>)}</ul><p className="insights-note">Building includes recorded rework and wait tools. Model time includes engine overhead; work without its own telemetry stays unclassified. Idle time and waiting on you remain separate.</p></>}
   <h3>Workers spending the most {phase==='gates'?'gate':'building'} time</h3>
   <ol className="activity-records">{current.workers.slice(0,10).map(w=><li key={w.session.id}><a href={`/sessions/${encodeURIComponent(w.session.id)}`}>{w.session.title||'Untitled conversation'}</a> · {formatDuration(w.seconds)} · {engineName(w.session.engine||'')}<p>{parts(w.parts)}</p><p className="insights-note">{WORKER_ACTIONS[largestPart(w.parts)]}</p></li>)}</ol>
   <h3>{phase==='building'?'First build and rework by card':'Cards spending the most gate time'}</h3>
   {phase==='building'&&<p className="insights-note">First build includes test-first red runs. Rework follows unresolved requested changes or failed checks after an edit to source code outside tests. A passing run of the same kind in the same region clears failures in its scope, including narrower runs. Missing earlier context is not inferred.</p>}
   <ol className="stack">{current.cards.slice(0,10).map(c=><li key={c.key}><div className="stack-head"><strong>{[d.cards.find(card=>card.key===c.key)?.reference||cardName(c.key),d.cards.find(card=>card.key===c.key)?.title].filter(Boolean).join(" · ")}</strong><span>{formatDuration(c.seconds)}</span></div>{phase==='building'&&<dl className="stack-figs"><div><dt>First build</dt><dd>{formatDuration(c.first)}</dd></div><div><dt>Rework</dt><dd>{formatDuration(c.rework)}</dd></div><div><dt>Stage not recorded</dt><dd>{formatDuration(c.unknown)}</dd></div></dl>}<p className="insights-note">{c.key==='Card not attributable'?'Time shared across cards or without a readable link cannot be allocated to one card.':phase==='building'&&c.rework>c.first?'Review the changes requested and failed checks before repeating the build.':'Inspect the workers above for checks or work this card repeats.'}</p></li>)}</ol>
  </>}
 </section>
}

function GateSuiteRow({run:r}:{run:GateRunBucket}){
 const stats=[['Runs started',r.runs],['Passed / failed',`${r.passed} / ${r.failed}`],['Outcome not recorded',r.unknown_outcome],['Repeats / reruns',`${r.repeated} / ${r.reruns}${r.repeat_unknown?` · ${r.repeat_unknown} not recorded`:''}`]]
 return <li><div className="stack-head"><strong>{r.suite}</strong><span>{formatDuration(r.seconds)}</span></div>
  <dl className="stack-figs">{stats.map(([label,value])=><div key={label}><dt>{label}</dt><dd>{r.count_unknown?'Not recorded':value}</dd></div>)}<div><dt>Time on failed runs</dt><dd>{formatDuration(r.failed_seconds)}</dd></div></dl>
  <p className="insights-note">{r.count_unknown?'Overlapping tool and suite records have no correlation, so their run counts cannot be separated.':r.failed_seconds>0?'Start with the failing runs; this time may be recoverable after a fix.':r.repeated>0?'Check whether repeated runs could share one final check.':'Review this suite’s scope and setup cost.'}{r.suite==='Suite not recorded'?' Earlier commands did not capture a suite or region.':''}</p>
 </li>
}
