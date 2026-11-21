import type { SkillReport } from './skillInsights'
import type { CountRow, StepSpread } from './insights'
export interface FlowEntry { at: string; in_progress: boolean | null; done: boolean | null }
export interface ActivityCard { key: string; reference?:string;title?:string; size: string | null; risk: string | null; complexity: string | null; size_first_set_at: string | null; flow: FlowEntry[] | null }
export interface ActivityHour {at:string;seconds:Record<string,number>;phases:Record<string,number>;detail?:Record<string,number>}
export interface GateRunBucket {at:string;kind:string;suite?:string;region?:string;seconds:number;runs:number;passed:number;failed:number;unknown_outcome:number;repeated:number;reruns:number;repeat_unknown:number;failed_seconds:number;count_unknown?:boolean}
export interface ActivitySession {
 title?: string; has_subagents?: boolean; native_active_time_unknown?: boolean
 id: string; parent_session_id: string | null; started_at: string | null; ended_at: string | null
 shared_with_other_cards: boolean; card_attribution_unknown?: boolean; engine?: string; cards: string[]
 timing: { seconds: Record<string, number>; phases: Record<string, number>; hours?: ActivityHour[]; gate_runs?:GateRunBucket[]; basis: string; incomplete_tools: number } | null
}
export interface ActivityMark {
	label?:string
 session_id: string; board_id: string; card: number | null; card_ref?: string; by?: string; target?: string; round?: number; verdict?: string; findings?: string[]
 severity?: string; type?: string; suite?: string; rerun_passed?: boolean; source?: string; message_id?: string
 at?: string | null; recorded_at: string; review_date?: string
}
export interface ActivityReport {
 skills?:SkillReport
 scope: 'mine'; generated_at: string; from: string; to: string; cards: ActivityCard[]; sessions: ActivitySession[]
 reviews: ActivityMark[]; failures: ActivityMark[]; omitted_cards: number; truncated: boolean; pending_sessions?: number
 questions?: ActivityQuestions[]
 events?: ActivityMark[]
 events_unavailable?: boolean
 history?: {from:string;sessions:ActivitySession[];failures:ActivityMark[]}
}
export interface ActivityQuestions { session_id: string; asked: number; reached_person: number; cards: string[]; card_attribution_unknown?: boolean }
export interface ActivityFilters { size?: string; engine?: string; subagents?: 'all' | 'yes' | 'no'; reviewer?: string }
export type RiskSlice = 'all' | 'low' | 'medium' | 'high' | 'unknown'
export function activitySlice(d: ActivityReport, risk: RiskSlice, filters: ActivityFilters = {}): ActivityReport {
 const {size='all',engine='all',subagents='all',reviewer='all'}=filters
 const sliced=risk!=='all'||size!=='all'||engine!=='all'||subagents!=='all'
 let cards = d.cards.filter(c => (risk==='all'||(c.risk || 'unknown') === risk) && (size==='all'||(c.size||'unknown')===size))
 let keys = new Set(cards.map(c => c.key))
 if(engine!=='all'||subagents!=='all'){
  // Only observed building/fixing sessions establish a builder cohort. Review-only
  // sessions and shared/unreadable card links cannot establish this attribution.
  const builders=d.sessions.filter(s=>!s.shared_with_other_cards&&!s.card_attribution_unknown&&s.cards.length===1&&keys.has(s.cards[0])&&
   ((s.timing?.phases.building??0)+(s.timing?.phases.fixing??0)>0)&&
   (engine==='all'||(s.engine||'unknown')===engine)&&
   (subagents==='all'||s.has_subagents===(subagents==='yes')))
  keys=new Set(builders.flatMap(s=>s.cards));cards=cards.filter(c=>keys.has(c.key))
 }
 const markMatches = (m: ActivityMark) => keys.has(markCardKey(m))
 const unlinked=(risk==='all'||risk==='unknown')&&(size==='all'||size==='unknown')&&engine==='all'&&subagents==='all'
 const linked=(links:string[],unknown?:boolean)=>!unknown&&(links.length===0?unlinked:links.length===1&&keys.has(links[0]))
 return { ...d, cards,
  history:d.history?{...d.history,sessions:sliced?d.history.sessions.filter(s=>!s.shared_with_other_cards&&linked(s.cards,s.card_attribution_unknown)):d.history.sessions,failures:sliced?d.history.failures.filter(markMatches):d.history.failures}:undefined,
  sessions: sliced?d.sessions.filter(s=>!s.shared_with_other_cards&&linked(s.cards,s.card_attribution_unknown)):d.sessions,
  reviews: d.reviews.filter(m=>(!sliced||markMatches(m))&&(reviewer==='all'||(m.by||'unknown')===reviewer)),
  failures: sliced?d.failures.filter(markMatches):d.failures,
  questions: sliced?d.questions?.filter(q=>linked(q.cards,q.card_attribution_unknown)):d.questions,
 }
}

export type ActivitySpread = StepSpread & { samples: number[] }
export function spread(key: string, values: number[]): ActivitySpread {
 const sorted = values.filter(v => Number.isFinite(v) && v >= 0).sort((a,b) => a-b)
 const q = (f: number) => { const n=(sorted.length-1)*f, lo=Math.floor(n); return sorted[lo]+(sorted[Math.ceil(n)]-sorted[lo])*(n-lo) }
 const median=q(0.5)
 return { samples:sorted, runtime:'flow',step:key,count:sorted.length,min:sorted[0],p25:q(.25),p50:median,p75:q(.75),p90:q(.9),max:sorted[sorted.length-1],slow:sorted.filter(v=>v>2*median).length }
}
// Only a single known card link supports allocating a conversation's time.
export function cardActiveTime(d: ActivityReport, key: string): number | null {
 const linked=d.sessions.filter(s=>s.cards.includes(key))
 if(!linked.length||linked.some(s=>s.shared_with_other_cards||s.card_attribution_unknown||s.native_active_time_unknown||s.cards.length!==1||!s.timing||(s.timing.seconds.unknown??0)>0))return null
 return linked.reduce((n,s)=>n+activeSeconds(s),0)
}
export function flowSpreads(d: ActivityReport): ActivitySpread[] {
 const groups=new Map<string,number[]>()
 for(const c of d.cards){const seconds=cardActiveTime(d,c.key);if(seconds===null)continue;const key=c.size||'Unknown size';groups.set(key,[...(groups.get(key)??[]),seconds])}
 const order=['XS','S','M','L','XL','Unknown size']
 return [...groups].sort(([a],[b])=>order.indexOf(a)-order.indexOf(b)).map(([k,v])=>spread(k,v))
}
export function counts(values: string[]): CountRow[] {
 const m=new Map<string,number>();for (const v of values)m.set(v,(m.get(v)??0)+1)
 return [...m].sort(([a],[b])=>a.localeCompare(b)).map(([key,count])=>({key,count}))
}
export function reviewRounds(reviews: ActivityMark[]): CountRow[] {
 const cards=new Map<string,{ rounds:Set<number>; missing:boolean }>()
 for(const r of reviews){const key=markCardKey(r),entry=cards.get(key)??{rounds:new Set<number>(),missing:false};if(r.round)entry.rounds.add(r.round);else entry.missing=true;cards.set(key,entry)}
 const sizes=new Map<number,number>();let missing=0
 for(const entry of cards.values()){if(entry.missing){missing++;continue};const n=entry.rounds.size;sizes.set(n,(sizes.get(n)??0)+1)}
 return [...[...sizes].sort(([a],[b])=>a-b).map(([n,count])=>({key:`${n} review round${n===1?'':'s'}`,count})),...(missing?[{key:'Round number missing',count:missing}]:[])]
}

const ACTIVE_KEYS=['tests','build_lint','git','read','edit','other','model','turn_end']
export function activeSeconds(s:ActivitySession):number{return ACTIVE_KEYS.reduce((n,k)=>n+(s.timing?.seconds[k]??0),0)}
export function knownGateSeconds(s:ActivitySession):number|null{
 const t=s.timing
 if(!t||(t.seconds.unknown??0)>0||Object.values(t.seconds).reduce((a,b)=>a+b,0)<=0)return null
 return Number.isFinite(t.phases.gates)?t.phases.gates:null
}
export function engineSpreads(d:ActivityReport,risk:RiskSlice):ActivitySpread[]{
 if(risk==='all')return []
 const groups=new Map<string,number[]>()
 for(const s of activitySlice(d,risk).sessions){const seconds=activeSeconds(s);if(!s.engine||seconds<=0)continue;groups.set(s.engine,[...(groups.get(s.engine)??[]),seconds])}
 return [...groups].sort(([a],[b])=>a.localeCompare(b)).map(([engine,values])=>spread(engine,values))
}

export function markCardKey(m: ActivityMark): string { return `${m.board_id}#${m.card ?? m.card_ref ?? 'unknown'}` }

// A card contributes once. A reviewer's engine never establishes the builder.
// Mixed, missing and shared builder attribution cannot support this comparison.
export function engineReviewSpreads(d:ActivityReport,risk:RiskSlice):ActivitySpread[]{
 if(risk==='all')return []
 const selected=activitySlice(d,risk),groups=new Map<string,number[]>()
 for(const card of selected.cards){
  const builders=d.sessions.filter(s=>s.cards.includes(card.key)&&((s.timing?.phases.building??0)+(s.timing?.phases.fixing??0)>0))
  if(!builders.length||builders.some(s=>s.shared_with_other_cards||s.card_attribution_unknown||s.cards.length!==1||!s.engine))continue
  const engines=new Set(builders.map(s=>s.engine!))
  if(engines.size!==1)continue
  const reviews=selected.reviews.filter(m=>markCardKey(m)===card.key)
  if(!reviews.length||reviews.some(m=>!m.round))continue
  const rounds=new Set(reviews.map(m=>m.round)).size,engine=[...engines][0]
  groups.set(engine,[...(groups.get(engine)??[]),rounds])
 }
 return [...groups].sort(([a],[b])=>a.localeCompare(b)).map(([engine,values])=>spread(engine,values))
}
