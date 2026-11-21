import { useSessionStore } from '../hooks/useSessionStore'
import { type ActivityReport, type RiskSlice } from '../lib/insightsActivity'
import { formatTick, formatUSD, partialReportNote, type InsightsResponse } from '../lib/insights'
import { activityTrend, addValue, bucketSize, DAY, HOUR, periodAnswer, timeBuckets } from '../lib/insightsTrend'
import TimeChart, { type TimeSeries } from './charts/TimeChart'
import BudgetBurndown from './charts/BudgetBurndown'
import { buildBurndown } from '../lib/budgetBurndown'
import UsageMeters from './UsageMeters'
import SkillInsights from './SkillInsights'
import ContextFillChart from './charts/ContextFillChart'
import { contextFillSeries } from '../lib/contextFill'
import { useState } from 'react'
import TimeBreakdown from './TimeBreakdown'
import type { TimePhase } from '../lib/timeBreakdown'
import { QUESTIONS, type QuestionView } from '../lib/insightsQuestions'

const PHASES: Record<string,string> = {building:'Building',gates:'Gates',reviewing:'Reviews',waiting_on_person:'Waiting on you',turn_end:'Turn work',idle:'Idle',unknown:'Unclassified'}
const FAILURES:Record<string,string>={test:'A test failed.',gate:'A required check failed, such as tests, lint or a build.',flake:'A failed check passed on a repeated run.',review:'A review found a problem that needs changes.',session:'A conversation stopped with an error.',engine:'The coding engine failed.',deploy:'A deployment failed.',merge:'A merge failed.',unknown:'The failure record did not include its type.'}
const title=(s:string)=>s==='unknown'?'Unclassified':s.charAt(0).toUpperCase()+s.slice(1)

export default function QuestionInsights({view, activity:d, summary, workspace}:{view:QuestionView;activity:ActivityReport;summary:InsightsResponse;filterTarget?:HTMLElement|null;risk?:RiskSlice;workspace?:string}) {
 const daemons=useSessionStore(s=>s.daemons)
 const [phase,setPhase]=useState<TimePhase|null>(null)
 const from=new Date(Math.ceil(Date.parse(d.from)/HOUR)*HOUR).toISOString(),to=new Date(Math.floor(Date.parse(d.to)/HOUR)*HOUR).toISOString()
 const size=bucketSize(d.from,d.to), span=Date.parse(to)-Date.parse(from)
 const previousFrom=new Date(Date.parse(from)-span).toISOString()
 const period=span<=DAY?'24 hours':span>=89*DAY?'90 days':span>=29*DAY?'30 days':'7 days'
 const chartView=view==='spending'||view==='skills'||view==='context'?'time':view
 let buckets=activityTrend(d,chartView,from,to)
 let previous=activityTrend(d,chartView,previousFrom,from)
 let series:TimeSeries[]=[], label='', missing='', answer=''
 const now=new Date()
 const budgets=['claude','codex'].map(p=>buildBurndown(summary.usage_budget||[],p)).filter((b):b is NonNullable<typeof b>=>!!b)
 if(view==='health'){
  label='Failures';series=Object.entries(FAILURES).map(([key,description])=>({key,label:title(key),description}))
  missing='No failures were recorded in this period. Agents record failures with a failure mark; conversation errors are included automatically.'
 }
 if(view==='time'){
  label='Recorded time';series=Object.entries(PHASES).map(([key,label])=>({key,label}))
  missing='No dated timing is available in this period. Conversations need recorded tool and status events; refresh after the runner has processed them.'
 }
 if(view==='cost'){
  label='Recorded card active time';series=['XS','S','M','L','XL','Unknown size'].map(key=>({key,label:key}))
  missing=d.omitted_cards?'Card time is unavailable because linked cards could not be read with your current board access.':!d.cards.length?'No linked cards. Pin a card, assign a worker to a checklist item naming #N, or include #N in its title with one board selected.':'Linked cards have no dated active time in this period. Missing timing and work shared across cards cannot be allocated to one card.'
 }
 if(view==='spending'){
  label='Estimated spending';buckets=timeBuckets(from,to);previous=timeBuckets(previousFrom,from,size)
  for(const row of [...summary.tokens.by_engine_day||[],...summary.tokens.previous_by_engine_day||[]])if(row.complete&&row.cost_usd!==null){if(Date.parse(row.at)>=Date.parse(from)&&Date.parse(row.at)<Date.parse(to))addValue(buckets,row.at,row.engine,row.cost_usd,size);else if(Date.parse(row.at)>=Date.parse(previousFrom)&&Date.parse(row.at)<Date.parse(from))addValue(previous,row.at,row.engine,row.cost_usd,size)}
  series=[...new Set(['claude','codex',...(summary.tokens.by_engine_day||[]).map(r=>r.engine)])].map(key=>({key,label:title(key)}))
  // A provider on a subscription plan with a weekly usage window gets a percent burndown instead
  // of dollar bars (BLG-462); a metered (API-billed) engine keeps dollars, so a mixed account
  // shows both, each labeled.
  series=series.filter(s=>!budgets.some(b=>b.provider===s.key))
  missing=summary.tokens.unpriced_models.length?'Spending is incomplete because model prices are missing. An administrator needs to set prices for the models used.':'No priced usage is recorded in this period. Usage arrives when a turn finishes.'
 }
 if(view!=='spending'&&view!=='skills'&&view!=='context')answer=periodAnswer(buckets,previous,label,period,!!d.history)
 // BLG-463: each reading is a point-in-time percent, not an additive amount, so there is no
 // subject-bearing sum to compare against the previous period the way periodAnswer() does for
 // the other views (BLG-464 round 2 found that reusing it without a real subject produces a
 // sentence with no subject). The chart and its own figcaption answer the question instead.
 const contextSeries=view==='context'?contextFillSeries(summary.context_fill||[],from,to):[]
 const contextHasData=contextSeries.some(s=>s.points.length>0)
 if(view==='health'){
  const recent=d.failures.filter(f=>!f.rerun_passed&&Date.parse(f.at||f.recorded_at)>=Date.parse(to)).length
  if(recent){answer+=` ${recent} failure${recent===1?' was':'s were'} recorded in the current hour.`;missing='The current hour is still in progress. Its failure records are available below; the trend compares completed UTC hours.'}
 }
 if(view!=='spending'&&(d.truncated||d.omitted_cards>0))answer=`${label} cannot be compared reliably. ${partialReportNote(d.truncated,d.omitted_cards,workspace)}`
 if((view==='time'||view==='cost')&&d.pending_sessions)answer=`${label} is incomplete: timing for ${d.pending_sessions} completed conversations is being prepared in the background. This will update automatically.`
 const events=(d.events||[]).filter(e=>Date.parse(e.at||e.recorded_at)>=Date.parse(d.from)).map(e=>({at:e.at||e.recorded_at,label:e.label||(e.target?`Milestone · ${e.target.slice(0,8)}`:'Milestone')}))
 const hasData=buckets.some(b=>series.some(s=>(b.values[s.key]||0)>0))
 // A provider with a recorded burndown already answers "is it on pace" in the chart below (its own
 // figcaption); the live reading here would otherwise repeat or contradict that for the same window.
 const weekly=daemons.flatMap(daemon=>(daemon.usage_limits||[]).filter(u=>(u.provider==='claude'||u.provider==='codex')&&!budgets.some(b=>b.provider===u.provider)).flatMap(u=>(u.windows||[]).filter(w=>w.id==='7d').map(w=>({daemon,u,w}))))
 const weeklyDaemons=daemons.map(daemon=>({...daemon,available_engines:[],usage_limits:(daemon.usage_limits||[]).filter(u=>(u.provider==='claude'||u.provider==='codex')&&!budgets.some(b=>b.provider===u.provider)).map(u=>({...u,windows:u.windows.filter(w=>w.id==='7d')})).filter(u=>u.windows.length>0)})).filter(d=>d.usage_limits.length>0)
 const pace=weekly.map(({daemon,u,w})=>{
  const end=Date.parse(w.resets_at||''),reading=Date.parse(u.fetched_at||''),elapsed=reading-(end-7*DAY)
  const valid=Number.isFinite(reading)&&elapsed>=HOUR&&elapsed<=7*DAY&&end>now.getTime()&&now.getTime()-reading>=0&&now.getTime()-reading<20*60_000
  const projected=valid?w.used_percent*7*DAY/elapsed:null
  const settling=Number.isFinite(reading)&&elapsed>=0&&elapsed<HOUR&&end>now.getTime()
  const who=`${title(u.provider)}${daemons.length>1?` · ${daemon.name}`:''}`
  return {summary:`${who} ${projected===null?settling?'needs an hour after reset to show its pace':'needs a fresh reading to show its pace':projected>100?'is on pace to exceed its weekly limit':'is within its weekly pace'}`,detail:`${who}: ${projected===null?'projection unavailable':`${Math.round(projected)}% projected by reset`}${Number.isFinite(end)?` · resets ${new Date(end).toLocaleString([], {month:'short',day:'numeric',hour:'numeric',minute:'2-digit'})}`:' · reset date not reported'}.`}
 })
 if(view==='spending')answer=pace.length?`${pace.map(p=>p.summary).join('; ')}.`:budgets.length?'':'Weekly pace is unavailable: Claude and Codex have not reported a weekly usage limit and reset date.'
 return <section className="insights-question" data-testid={`question-${view}`}>
  <h1>{view==='spending'?'Am I on pace for my limits?':QUESTIONS.find(q=>q.id===view)!.question}</h1>
  {view!=='skills'&&view!=='context'&&<p className="insights-answer" role={(view==='time'||view==='cost')&&d.pending_sessions?'status':undefined}>{answer}</p>}
  {view!=='skills'&&view!=='context'&&<p className="insights-note">Completed UTC hours; both periods cover the same elapsed time.</p>}
  {view==='skills'&&<SkillInsights activity={d}/>}
  {view==='context'&&<ContextFillChart series={contextSeries} from={from} to={to} hourly={size===HOUR}/>}
  {view==='context'&&!contextHasData&&<p className="insights-note">No context readings were recorded in this period. The engine reports one at the end of a turn; older conversations and some engines may not have reported one.</p>}
  {view==='context'&&summary.context_fill_truncated&&<p className="insights-note">Some context readings are outside this report's limit. Narrow the time range.</p>}
  {view==='spending'&&<p className="insights-note">{periodAnswer(buckets,previous,label,period,!!summary.tokens.previous_by_engine_day)}</p>}
  {view==='spending'&&weeklyDaemons.length>0&&<><UsageMeters daemons={weeklyDaemons}/><p className="insights-note">Provider limits include work outside Blerg. Projections assume the pace since the weekly window began continues.</p></>}
  {view==='spending'&&pace.map((p,i)=><p key={i} className="insights-note">{p.detail}</p>)}
  {view==='spending'&&pace.length>0&&['claude','codex'].filter(provider=>!weekly.some(r=>r.u.provider===provider)&&!budgets.some(b=>b.provider===provider)).map(provider=><p key={provider} className="insights-note">{title(provider)} has not reported a weekly limit and reset date, so its pace is unavailable.</p>)}
  {view==='spending'&&budgets.length>0&&<>
   {budgets.length>0&&series.length>0&&<p className="insights-note">{budgets.map(b=>title(b.provider)).join(' and ')} {budgets.length>1?'are':'is'} on a subscription plan: shown here as a percent of {budgets.length>1?'their':'its'} weekly budget, not dollars. {series.map(s=>s.label).join(' and ')} {series.length>1?'are':'is'} metered and kept in dollars below.</p>}
   {budgets.map(b=><BudgetBurndown key={b.provider} series={b}/>)}
  </>}
  {view!=='skills'&&view!=='context'&&(view!=='spending'||series.length>0)&&<TimeChart key={view} buckets={buckets} series={series} events={events} label={label} format={view==='health'?n=>String(Math.round(n)):view==='spending'?formatUSD:formatTick} share={view==='time'} now={d.to} hourly={size===HOUR} selectedCategory={phase} onSelectCategory={view==='time'?key=>setPhase(p=>p===key?null:key as TimePhase):undefined}/>}
  {view==='time'&&phase&&<TimeBreakdown activity={d} phase={phase} from={from} to={to} previousFrom={previousFrom} period={period} events={events} workspace={workspace} onClose={()=>setPhase(null)}/>}
  {view!=='context'&&!hasData&&!((view==='time'||view==='cost')&&d.pending_sessions)&&!(view==='spending'&&series.length===0)&&<p className="insights-note">{missing}</p>}
  {view==='cost'&&d.sessions.some(s=>s.native_active_time_unknown)&&<p className="insights-note">Some card time is missing because native agents did not report their active time.</p>}
  {view==='spending'&&hasData&&summary.tokens.unpriced_models.length>0&&<p className="insights-note">Some spending is missing because model prices are not set. Only priced usage is shown.</p>}

  {(d.truncated||d.omitted_cards>0)&&<p className="insights-note">{partialReportNote(d.truncated,d.omitted_cards,workspace)}</p>}
  {d.events_unavailable?<p className="insights-note">Deployment and release history could not be read. Any recorded milestones are still shown.</p>:!events.length&&<p className="insights-note">No deployments, releases or milestones recorded in this period.</p>}
 </section>
}
