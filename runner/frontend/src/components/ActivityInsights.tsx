import { signedInAccount } from '../lib/liveBoardCard'
import { BarChart, SpreadChart } from './InsightsCharts'
import '../../../../packages/board/board.css'
import QuestionInsights from './QuestionInsights'
import type { QuestionView } from '../lib/insightsQuestions'
import type { InsightsResponse } from '../lib/insights'
import InsightsSkeleton from './InsightsSkeleton'
import { cachedInsights, clearInsightsCache, insightsCacheKey, rememberInsights } from '../lib/insightsCache'
import { createPortal } from 'react-dom'
import { useInsightsPreference } from '../hooks/useInsightsPreferences'
import { IconButton } from '@blerglab/chat'
import { useEffect, useMemo, useRef, useState } from 'react'
import { apiFetch } from '../apiFetch'
import { activeSeconds, knownGateSeconds, engineReviewSpreads, activitySlice, cardActiveTime, markCardKey, counts, flowSpreads, reviewRounds, type ActivityReport, type RiskSlice, type ActivityFilters, type ActivitySession } from '../lib/insightsActivity'
import { formatDuration, formatPercent, partialReportNote, type RangeLabel } from '../lib/insights'
import type { InsightsSection } from './Insights'
import './ActivityInsights.css'
import WorkspaceLiveMetrics from './WorkspaceLiveMetrics'
import { ShowAllButton } from './InsightsExpand'
import { useExpand } from '../hooks/useExpand'

export default function ActivityInsights({ section, workspace, range, enabled=true, filterTarget, questionView, summary, diagnostics=false, detailsContent }: { detailsContent?:React.ReactNode; diagnostics?: boolean; questionView?: QuestionView; summary?: InsightsResponse; filterTarget?: HTMLElement | null; section: InsightsSection; workspace?: string; range?: RangeLabel; enabled?: boolean }) {
 const cacheKey=insightsCacheKey(signedInAccount(), `activity|${workspace || ''}|${range??(workspace?'all':'7d')}`)
 const [answer,setAnswer]=useState<{key:string;data:ActivityReport}|null>(()=>{const data=cachedInsights<ActivityReport>(cacheKey);return data?{key:`0|${workspace || ''}|${range??(workspace?'all':'7d')}`,data}:null})
 const [failed,setFailed]=useState<{key:string;status:number}|null>(null)
 const loadedKey=useRef<string|null>(null)
 const [retry,setRetry]=useState(0)
 const [risk,setRisk]=useInsightsPreference<RiskSlice>('risk', 'all')
 const [filters,setFilters]=useInsightsPreference<ActivityFilters>('activity', {})
 const [measureTarget,setMeasureTarget]=useState<HTMLDivElement|null>(null)
 const [workerSort,setWorkerSort]=useState<'name'|'time'>('name')
 const [reviewSort,setReviewSort]=useState<'card'|'round'|'reviewer'>('card')
 const [failureSort,setFailureSort]=useState<'suite'|'type'|'severity'>('suite')
 const [identityVersion,setIdentityVersion]=useState(0)
 useEffect(()=>{if(!enabled)return;const timer=setInterval(()=>{if(document.visibilityState==='visible')setRetry(n=>n+1)},30_000);return()=>clearInterval(timer)},[enabled,section])
 useEffect(()=>{const changed=()=>{loadedKey.current=null;setAnswer(null);setIdentityVersion(v=>v+1)};window.addEventListener('blerg-identity-changed',changed);return()=>window.removeEventListener('blerg-identity-changed',changed)},[])
 const requestedRange=range??(workspace?'all':'7d')
 const scopeKey=`${identityVersion}|${workspace || ''}|${requestedRange}`
 const key=`${scopeKey}|${retry}`
 useEffect(()=>{
  if(!enabled||loadedKey.current===key)return
  let cancelled=false
  const controller=new AbortController()
  const qs=new URLSearchParams({range:requestedRange});if(workspace)qs.set('workspace',workspace)
  apiFetch(`/api/insights/activity?${qs}`,{signal:controller.signal})
   .then(r=>r.ok?r.json() as Promise<ActivityReport>:Promise.reject({status:r.status}))
   .then(data=>{if(!cancelled){loadedKey.current=key;rememberInsights(cacheKey,data);setAnswer({key:scopeKey,data})}})
   .catch((e: {status?:number})=>{if(!cancelled){if(e.status&&[401,403,404].includes(e.status)){clearInsightsCache();setAnswer(null)};setFailed({key,status:e.status??0})}})
  return()=>{cancelled=true;controller.abort()}
 },[workspace,requestedRange,key,scopeKey,enabled,cacheKey])
 const d=useMemo(()=>{const current=answer?.key===scopeKey?answer.data:cachedInsights<ActivityReport>(cacheKey);return current?(questionView==='spending'?current:activitySlice(current,risk,filters)):null},[answer,scopeKey,cacheKey,risk,filters,questionView])
 const workersExpand=useExpand(d?.sessions.filter(s=>s.timing).length??0,50)
 const marksExpand=useExpand(d?.reviews.length??0,50)
 const marksRecordsExpand=useExpand(d?.reviews.length??0,50)
 const failuresExpand=useExpand(d?.failures.length??0,50)
 const failuresRecordsExpand=useExpand(d?.failures.length??0,50)
 if(!enabled&&!d)return null
 if(!d&&failed?.key===key&&workspace&&[403,404].includes(failed.status))return <p className="insights-note">Only the owner's activity is available.</p>
 if(!d&&failed?.key===key&&failed.status===401)return <p className="insights-note">Sign in to view work and review data.</p>
 if(!d&&failed?.key===key)return <section className="activity-report"><p role="alert">Could not load work and review data. <IconButton label="Retry" labeled onClick={()=>setRetry(v=>v+1)} /></p></section>
 if(!d||(questionView&&!summary))return <InsightsSkeleton />
 const flow=flowSpreads(d)
 const unknownCards=d.cards.filter(c=>cardActiveTime(d,c.key)===null)
 const measured=d.sessions.filter(s=>s.timing).sort((a,b)=>workerSort==='name'?sessionName(a).localeCompare(sessionName(b)):activeSeconds(b)-activeSeconds(a))
 const gates=measured.filter(s=>knownGateSeconds(s)!==null)
 const gateSeconds=gates.reduce((n,s)=>n+knownGateSeconds(s)!,0)
 const measuredSeconds=gates.reduce((n,s)=>n+activeSeconds(s),0)
 const namedMark=(m:ActivityReport['reviews'][number])=>({...m,card_ref:m.card_ref||d.cards.find(c=>c.key===markCardKey(m))?.reference})
 const failureRecords=d.failures.map(namedMark)
 const failures=failureRecords.filter(f=>!f.rerun_passed)
 const reruns=failureRecords.filter(f=>f.rerun_passed)
 const marks=d.reviews.map(namedMark).sort((a,b)=>reviewSort==='round'?(a.round??0)-(b.round??0):reviewSort==='reviewer'?(a.by??'').localeCompare(b.by??''):(a.card??0)-(b.card??0))
 const severities=counts(marks.flatMap(m=>m.findings??[]))
 const source=answer?.key===scopeKey?answer.data:cachedInsights<ActivityReport>(cacheKey)!
 const engines=[...new Set(source.sessions.map(s=>s.engine||'unknown'))].sort()
 const reviewers=[...new Set(source.reviews.map(m=>m.by||'unknown'))].sort()
 const filterCount=(risk==='all'?0:1)+Object.values(filters).filter(v=>v&&v!=='all').length
 const cohortFiltered=risk!=='all'||Object.entries(filters).some(([k,v])=>k!=='reviewer'&&v!=='all')
 const questions=d.questions
 const phases=Object.entries(PHASE_LABELS).map(([key,label])=>({key:label,count:measured.reduce((n,s)=>n+(s.timing!.phases[key]??0),0)}))
 return <div className="activity-report" data-testid="activity-report">
  {!diagnostics && questionView!=='spending' && (filterTarget ? createPortal(<details className="insights-filter-menu" onKeyDown={e=>{if(e.key==='Escape'){e.preventDefault();e.stopPropagation();e.currentTarget.open=false;e.currentTarget.querySelector('summary')?.focus()}}}><summary className="btn small">Filter{filterCount>0&&<span className="filter-count">{filterCount}</span>}</summary><div className="filter-pop"><div className="activity-controls"><label>Risk <select className="insights-select" aria-label="Risk slice" value={risk} onChange={e=>setRisk(e.target.value as RiskSlice)}>{['all','low','medium','high','unknown'].map(r=><option key={r} value={r}>{r==='all'?'All risks':r}</option>)}</select></label>
   <Slice label="Size" value={filters.size} options={['S','M','L','XL','unknown']} onChange={size=>setFilters(f=>({...f,size}))}/>
   <Slice label="Engine" value={filters.engine} options={engines} onChange={engine=>setFilters(f=>({...f,engine}))}/>
   <Slice label="Subagents used" value={filters.subagents} options={['yes','no']} onChange={subagents=>setFilters(f=>({...f,subagents:subagents as ActivityFilters['subagents']}))}/>
   {<Slice label="Reviewer" value={filters.reviewer} options={reviewers} onChange={reviewer=>setFilters(f=>({...f,reviewer}))}/>}
   {!questionView&&<span>{d.cards.length} cards · {d.sessions.length} conversations · Updated {new Date(d.generated_at).toLocaleTimeString()}</span>}<div ref={setMeasureTarget}/></div></div></details>, filterTarget) : <><div className="activity-controls"><label>Risk <select className="insights-select" aria-label="Risk slice" value={risk} onChange={e=>setRisk(e.target.value as RiskSlice)}>{['all','low','medium','high','unknown'].map(r=><option key={r} value={r}>{r==='all'?'All risks':r}</option>)}</select></label>
   <Slice label="Size" value={filters.size} options={['S','M','L','XL','unknown']} onChange={size=>setFilters(f=>({...f,size}))}/>
   <Slice label="Engine" value={filters.engine} options={engines} onChange={engine=>setFilters(f=>({...f,engine}))}/>
   <Slice label="Subagents used" value={filters.subagents} options={['yes','no']} onChange={subagents=>setFilters(f=>({...f,subagents:subagents as ActivityFilters['subagents']}))}/>
   {<Slice label="Reviewer" value={filters.reviewer} options={reviewers} onChange={reviewer=>setFilters(f=>({...f,reviewer}))}/>}
   {!questionView&&<span>{d.cards.length} cards · {d.sessions.length} conversations · Updated {new Date(d.generated_at).toLocaleTimeString()}</span>}</div></>)}
  {questionView && summary && <QuestionInsights view={questionView} activity={d} summary={summary} risk={risk} filterTarget={measureTarget} workspace={workspace} />}
  {questionView!=='skills'&&questionView!=='context'&&<>
  {detailsContent}
  <details className="activity-method insights-how"><summary>How this is measured</summary>
   <p className="insights-note">{workspace?'Time covers each whole conversation in this workspace.':'Time covers each whole conversation started in this range.'} Reviews and failures are counted within the selected range. Conversation time shared across cards is counted once.</p>
   <p className="insights-note">Active time per card sums recorded active time across linked workers and subagents, including turn work. Idle and waiting on a person are excluded. Missing telemetry and ambiguous or absent worker links remain unknown. Native subagents without their own active-time telemetry also make card time unknown; their elapsed duration is not used as active time.</p>
   <p className="insights-note">Reviews by building engine counts distinct review rounds per card, using only cards with one known building engine and numbered reviews. Cards built by several engines or with missing round numbers are excluded. Reviewer names come from the review author; they do not identify the builder. No findings means none were reported, not that the work was checked and found clean.</p>
   <p className="insights-note">Mission-critical work goes to one engine. Engine assignment is not random; compare within a risk tag. These observations cannot establish which engine produces better work.</p>
   {cohortFiltered&&<p className="insights-note">Filters omit conversations linked to several cards or cards you cannot read. Building engine and subagent filters select cards with measured building or fixing time; their other conversations remain included. Subagents include native agents and launched workers; missing observations remain unknown.</p>}
   <p className="insights-note">Gate time includes tests, build/lint and commit hooks classified from commands. Dollar cost by phase is not recorded. {d.sessions.length-gates.length} conversations lack complete gate timing. Gate share excludes idle, waiting and unknown time. Time adds across workers; it is not workspace elapsed time. Waiting on a person cannot yet be subtracted from card time.</p>
   <p className="insights-note">Questions include nonblocking questions. Reached the person means sent to their inbox, not read or answered. Failure alerts are excluded. A question is counted once; questions linked to several cards are excluded when filtering cards.</p>
   <p className="insights-note">Failure entries can describe the same attempt. A successful rerun does not erase the original failure.</p>
   <p className="insights-note">Worker time: disjoint tool/status intervals partition each conversation. Model time includes running time outside tools and engine overhead; unknown time stays unknown. Rows include Main, workers and channels. Test shares use total recorded conversation time. Review rounds count each numbered round once per card; reviews without a round number are excluded. {measured.reduce((n,s)=>n+s.timing!.incomplete_tools,0)} unfinished tools were clipped to the read time or conversation end.</p>
  </details>
  {!questionView&&!!d.pending_sessions&&<p role="status" className="insights-note">Timing for {d.pending_sessions} completed conversations is being prepared in the background. This report is incomplete and will update automatically.</p>}
  {(d.truncated||d.omitted_cards>0)&&<p role="status" className="insights-note">Partial report: {partialReportNote(d.truncated,d.omitted_cards,workspace)}</p>}
  {section==='flow'&&<>
   <section><h2>Rework by building engine</h2><BarChart rows={Array.from(new Set(measured.map(s=>s.engine||'Engine not recorded'))).map(engine=>({key:engine,count:measured.filter(s=>(s.engine||'Engine not recorded')===engine).reduce((n,s)=>n+(s.timing!.phases.fixing??0),0)}))} duration records={engine=>measured.filter(s=>(s.engine||'Engine not recorded')===engine&&(s.timing!.phases.fixing??0)>0).map(s=>`${sessionName(s)} · ${formatDuration(s.timing!.phases.fixing)}`)}/></section>
   <section><h2>Active time per card by size</h2>
    <Metric label="Cards with unknown active time" value={String(unknownCards.length)} records={unknownCards.map(c=>c.key)}/>
    {flow.length?<SpreadChart rows={flow} records={size=>d.cards.filter(c=>(c.size||'Unknown size')===size&&cardActiveTime(d,c.key)!==null).map(c=>`${c.key} · ${formatDuration(cardActiveTime(d,c.key)!)}`)}/>:<Empty>No linked card active time is recorded in this range.</Empty>}
   </section>
   <section><h2>Reviews by building engine</h2><p className="insights-note">Review rounds per card, within the selected risk.</p>
    {risk==='all'?<Empty>Choose a risk to compare engines; task risk affects review needs.</Empty>:engineReviewSpreads(d,risk).length?<SpreadChart rows={engineReviewSpreads(d,risk)} rounds records={engine=>d.cards.filter(c=>engineReviewSpreads({...d,cards:[c]},risk).some(r=>r.step===engine)).map(c=>c.key)}/>:<Empty>No cards with a known builder and numbered reviews for this risk.</Empty>}
   </section>
   <section><h2>Gate cost</h2>{gates.length?<><div className="activity-metrics"><Metric label="Gate time" value={formatDuration(gateSeconds)} records={gates.map(s=>sessionName(s))}/><Metric label="Share of classified active time" value={measuredSeconds?formatPercent(gateSeconds/measuredSeconds):'—'} records={gates.map(s=>sessionName(s))}/><Metric label="Conversations with gate timing" value={String(gates.length)} records={gates.map(s=>sessionName(s))}/></div><BarChart rows={gates.filter(s=>knownGateSeconds(s)!>0).sort((a,b)=>knownGateSeconds(b)!-knownGateSeconds(a)!).map(s=>({key:sessionName(s),id:s.id,href:`/sessions/${encodeURIComponent(s.id)}`,count:knownGateSeconds(s)!}))} duration records={id=>gates.filter(s=>s.id===id).map(s=>sessionName(s))}/></>:<Empty>No classified gate time is recorded for these conversations.</Empty>}
   </section>
   <section><h2>Time by phase</h2>{measured.length?<BarChart rows={phases} duration records={label=>measured.filter(s=>(s.timing?.phases[Object.keys(PHASE_LABELS).find(key=>PHASE_LABELS[key]===label)!]??0)>0).map(s=>sessionName(s))}/>:<Empty>No recorded phase timing in this range.</Empty>}</section>
  </>}
  {(section==='live'||diagnostics||questionView==='health')&&workspace&&<WorkspaceLiveMetrics workspace={workspace} sessions={d.sessions}/>}
  {section==='live'&&!workspace&&<p className="insights-note">Open a workspace's Insights for running work and machine samples.</p>}
  {(section==='flow'||section==='live'||diagnostics)&&<>
   <section><h2>Worker time</h2>{measured.length?<><div className="activity-legend">{Object.entries(TIME_LABELS).map(([k,v])=><span key={k}><i className={`activity-swatch activity-${k}`}/>{v}</span>)}</div>
    <div className="insights-list-sort"><button className="insights-sort" onClick={()=>setWorkerSort('name')}>Conversation ↑</button><button className="insights-sort" onClick={()=>setWorkerSort('time')}>Active time ↓</button></div><ul id={workersExpand.id} className="activity-workers">{measured.slice(0,workersExpand.shown).map(s=>{
     const seconds=s.timing!.seconds,total=Object.values(seconds).reduce((a,b)=>a+b,0)
     const rounds=roundCount(marks.filter(m=>m.session_id===s.id))
     return <li key={s.id}><a href={`/sessions/${encodeURIComponent(s.id)}`}>{sessionName(s)}</a><details><summary><span>{formatDuration(total)}{s.engine?` · ${s.engine}`:''}{total?` · ${formatPercent((seconds.tests??0)/total)} tests`:''}{rounds?` · ${rounds} review round${rounds===1?'':'s'}`:''}{s.shared_with_other_cards?' · shared across cards':''}</span>
      <span className="activity-stack" role="img" aria-label={Object.entries(TIME_LABELS).map(([k,v])=>`${v}: ${formatDuration(seconds[k]??0)}`).join(', ')}>{Object.keys(TIME_LABELS).filter(k=>(seconds[k]??0)>0).map(k=><span key={k} className={`activity-${k}`} title={`${TIME_LABELS[k]}: ${formatDuration(seconds[k])}`} style={{width:`${total?seconds[k]/total*100:0}%`}}/>)}</span></summary><dl>{Object.entries(TIME_LABELS).map(([k,v])=><div key={k}><dt>{v}</dt><dd>{formatDuration(seconds[k]??0)}</dd></div>)}</dl></details>
     </li>
    })}</ul>{workersExpand.hasMore&&<ShowAllButton id={workersExpand.id} total={measured.length} limit={50} expanded={workersExpand.expanded} onToggle={workersExpand.toggle}/>}</>:<Empty>No instrumented worker or main conversation time in this range.</Empty>}
   </section>
  </>}
  {section==='live'&&<section><h2>Recorded outcomes</h2><div className="activity-metrics"><Metric label="Review rounds" value={marks.some(m=>!m.round)?'—':String(roundCount(marks))}/><Metric label="Failures" value={String(failures.length)}/><Metric label="Passed reruns" value={String(reruns.length)}/></div><p className="insights-note">Reviews and failures recorded within the selected range. Open Reviews for rounds, findings and individual records.</p></section>}
  {section==='reviews'&&<>
   <section><h2>Review rounds and severities</h2><details><summary>{marks.length} review{marks.length===1?'':'s'}.</summary><ul id={marksExpand.id} className="activity-records">{marks.slice(0,marksExpand.shown).map((m,i)=><li key={i}>{reviewLabel(m)}</li>)}</ul>{marksExpand.hasMore&&<ShowAllButton id={marksExpand.id} total={marks.length} limit={50} expanded={marksExpand.expanded} onToggle={marksExpand.toggle}/>}</details>
    {marks.length?<div className="activity-columns"><div><h3>Review rounds per card</h3><BarChart rows={reviewRounds(marks).map(r=>({...r,key:r.key==='Round number missing'?'Cards missing a round number':`Cards with ${r.key}`}))} records={key=>[...new Set(marks.map(markCardKey))].filter(card=>{const reviews=marks.filter(m=>markCardKey(m)===card);const r=reviewRounds(reviews)[0];return r&&(key===(r.key==='Round number missing'?'Cards missing a round number':`Cards with ${r.key}`))})}/></div><div><h3>Findings by severity</h3>{severities.length?<BarChart rows={severities} records={key=>marks.filter(m=>m.findings?.includes(key)).map(reviewLabel)}/>:<Empty>No findings reported in these review records.</Empty>}</div></div>:<Empty>No review records in this range.</Empty>}
    {marks.length>0&&<><h3>Review outcomes</h3><BarChart rows={counts(marks.map(reviewVerdict))} records={key=>marks.filter(m=>reviewVerdict(m)===key).map(reviewLabel)}/><details><summary>Review records</summary><div className="insights-list-sort">{(['card','round','reviewer'] as const).map(key=><button key={key} className="insights-sort" onClick={()=>setReviewSort(key)}>{key==='card'?'Card':key==='round'?'Round':'Reviewer'}{reviewSort===key?' ↑':''}</button>)}</div><ul id={marksRecordsExpand.id} className="activity-records">{marks.slice(0,marksRecordsExpand.shown).map((m,i)=><li key={i}>{m.card_ref || `#${m.card??'—'}`}  · {m.round?`round ${m.round}`:'round not recorded'} · {reviewVerdict(m)} · {m.by||'unknown reviewer'} · {m.target?`commit ${m.target.slice(0,8)}`:'commit not recorded'}{m.review_date?` · ${m.review_date} (date only)`:''}</li>)}</ul>{marksRecordsExpand.hasMore&&<ShowAllButton id={marksRecordsExpand.id} total={marks.length} limit={50} expanded={marksRecordsExpand.expanded} onToggle={marksRecordsExpand.toggle}/>}</details></>}
   </section>
   <section><h2>Questions</h2>{questions?<div className="activity-metrics"><Metric label="Questions asked" value={String(questions.reduce((n,q)=>n+q.asked,0))} records={questions.filter(q=>q.asked>0).map(q=>(d.sessions.find(s=>s.id===q.session_id)?.title||"Untitled conversation"))}/><Metric label="Reached the person" value={String(questions.reduce((n,q)=>n+q.reached_person,0))} records={questions.filter(q=>q.reached_person>0).map(q=>(d.sessions.find(s=>s.id===q.session_id)?.title||"Untitled conversation"))}/><Metric label="Conversations asking" value={String(questions.length)} records={questions.map(q=>(d.sessions.find(s=>s.id===q.session_id)?.title||"Untitled conversation"))}/></div>:<Empty>Question counts are not available from this server.</Empty>}</section>
  </>}
  {(section==='flow'||section==='reviews')&&<section><h2>Failures and reruns</h2><details><summary>{failures.length} failure{failures.length===1?'':'s'} · {reruns.length} successful rerun{reruns.length===1?'':'s'}.</summary><ul id={failuresExpand.id} className="activity-records">{failureRecords.slice(0,failuresExpand.shown).map((m,i)=><li key={i}>{failureLabel(m)}</li>)}</ul>{failuresExpand.hasMore&&<ShowAllButton id={failuresExpand.id} total={failureRecords.length} limit={50} expanded={failuresExpand.expanded} onToggle={failuresExpand.toggle}/>}</details>
   {failures.length?<div className="activity-columns"><div><h3>Failure type</h3><BarChart rows={counts(failures.map(f=>f.type||'unknown'))} records={key=>failures.filter(f=>(f.type||'unknown')===key).map(failureLabel)}/></div><div><h3>Severity</h3><BarChart rows={counts(failures.map(f=>f.severity||'unknown'))} records={key=>failures.filter(f=>(f.severity||'unknown')===key).map(failureLabel)}/></div></div>:<Empty>No failure records in this range.</Empty>}
   {d.failures.length>0&&<details><summary>Failure and rerun records</summary><div className="insights-list-sort">{(['suite','type','severity'] as const).map(key=><button key={key} className="insights-sort" onClick={()=>setFailureSort(key)}>{key==='suite'?'Suite':key==='type'?'Type':'Severity'}{failureSort===key?' ↑':''}</button>)}</div><ul id={failuresRecordsExpand.id} className="activity-records">{[...d.failures].sort((a,b)=>(a[failureSort]??'').localeCompare(b[failureSort]??'')).slice(0,failuresRecordsExpand.shown).map((f,i)=><li key={i}>{f.type||'unknown type'} · {f.suite||'suite unknown'} · {f.severity||'severity unknown'} · {f.rerun_passed?'rerun passed':'failure'}{f.source==='session_failure_alert'?' · automatic session alert':''}</li>)}</ul>{failuresRecordsExpand.hasMore&&<ShowAllButton id={failuresRecordsExpand.id} total={d.failures.length} limit={50} expanded={failuresRecordsExpand.expanded} onToggle={failuresRecordsExpand.toggle}/>}</details>}
  </section>}
  </>}
 </div>
}
function Empty({children}:{children:React.ReactNode}){return <p className="insights-note">{children}</p>}
function Metric({label,value,records}:{label:string;value:string;records?:string[]}){return <div className="card"><div className="card-label">{label}</div>{records?<details><summary className="card-value">{value}</summary><p className="insights-note">Close this number to return.</p><ul className="activity-records">{records.map((r,i)=><li key={i}>{r}</li>)}</ul></details>:<div className="card-value">{value}</div>}</div>}
function sessionName(s:ActivitySession){return s.title||'Untitled conversation'}
function roundCount(marks:ActivityReport['reviews']){return new Set(marks.filter(m=>m.round).map(m=>`${m.board_id}#${m.card}:${m.round}`)).size}
function Slice({label,value='all',options,onChange}:{label:string;value?:string;options:string[];onChange:(value:string)=>void}){return <label>{label} <select className="insights-select" aria-label={`${label} slice`} value={value} onChange={e=>onChange(e.target.value)}><option value="all">All</option>{options.map(v=><option key={v} value={v}>{v==='unknown'?'Unknown':v==='yes'?'Yes':v==='no'?'No':v}</option>)}</select></label>}
const TIME_LABELS:Record<string,string>={tests:'Tests',build_lint:'Build/lint',git:'Git',read:'Read',edit:'Edit',other:'Other tools',model:'Model/overhead',turn_end:'Turn work',waiting_on_person:'Waiting on person',idle:'Idle',unknown:'Unknown'}
const PHASE_LABELS:Record<string,string>={building:'Building',reviewing:'Reviewing',fixing:'Fixing',waiting_on_person:'Waiting on person',waiting:'Other waiting',gates:'Gates',turn_end:'Turn work',idle:'Idle',unknown:'Unknown'}

function reviewVerdict(m:ActivityReport['reviews'][number]):string { return m.verdict==='changes'?'Changes requested':m.verdict==='rejected'?'Rejected':m.verdict==='approved'?'Approved':'Unknown' }
function reviewLabel(m:ActivityReport['reviews'][number]):string { return `${m.card_ref || (m.card !== null ? `#${m.card}` : 'Unlinked review')} · ${m.by || 'Unknown reviewer'} · ${reviewVerdict(m)}${m.round ? ` · round ${m.round}` : ''}` }
function failureLabel(m:ActivityReport['failures'][number]):string { return `${m.card_ref || (m.card !== null ? `#${m.card}` : 'Unlinked failure')} · ${m.suite || 'Suite unknown'} · ${m.type || 'Type unknown'} · ${m.severity || 'Severity unknown'}` }
