import { markCardKey, type ActivityReport } from './insightsActivity'
export interface SkillLoad { session_id:string; parent_call_id?:string; role:string; engine:string; skill:string; version:string; mode:'always'|'on_demand'; bytes:number; basis:string; at?:string }
export interface SkillEvidence { session_id:string; skill:string; version:string; rule:string; result:'followed'|'missed'|'unknown'; source:'agent'|'person'; at:string }
export interface SkillReport {
 facts?:{session_id:string;image_artifacts:number;full_suite_runs:number;reported_test_failures_before_edit:number}[]
 applicability?:{session_id:string;skill:string;parent_call_id?:string;basis:string;at?:string}[]
 loads:SkillLoad[]; reporting:SkillLoad[]; evidence:SkillEvidence[]; truncated:boolean; unavailable?:boolean
}
export const skillNames:Record<string,string>={'ux-design':'UX design','test-driven-development':'Testing','managing-tickets':'Tickets',coordinating:'Coordination',planning:'Planning',visualizing:'Charts and diagrams'}
export const ruleNames:Record<string,string>={'red-before-fix':'Test fails before the fix','region-once':'Whole region run once',screenshots:'Screenshots inspected','reuse-patterns':'Existing patterns reused','plain-words':'Plain words','say-once':'Each thing said once','natural-width':'Controls at natural width','estimate-first':'Estimate before starting','record-reviews':'Reviews recorded','record-failures':'Failures recorded','review-before-merge':'Review before merging','bounded-scope':'Work kept within scope'}
const timestamp=(at?:string)=>at?Date.parse(at):NaN
export function skillCohorts(d:ActivityReport){
 const report=d.skills
 if(!report)return []
 const loads=report.loads??[],reporting=report.reporting??[]
 const groups=new Map<string,{skill:string;version:string}>()
 for(const load of [...loads,...reporting])groups.set(`${load.skill}:${load.version}`,{skill:load.skill,version:load.version})
 const sessions=new Map(d.sessions.map(s=>[s.id,s]))
 const singleCard=(id:string)=>{const s=sessions.get(id);return s&&!s.shared_with_other_cards&&!s.card_attribution_unknown&&s.cards.length===1?s.cards[0]:null}
 const cardVersions=new Map<string,Set<string>>()
 for(const load of loads){const card=singleCard(load.session_id);if(card){const key=`${load.skill}:${card}`;if(!cardVersions.has(key))cardVersions.set(key,new Set());cardVersions.get(key)!.add(load.version)}}
 return [...groups.values()].map(group=>{
  const matches=(v:{skill:string;version:string})=>v.skill===group.skill&&v.version===group.version
  const observed=loads.filter(v=>matches(v)&&sessions.has(v.session_id))
  const coverage=reporting.filter(v=>matches(v)&&sessions.has(v.session_id))
  const evidence=(report.evidence??[]).filter(v=>matches(v)&&sessions.has(v.session_id))
  const ids=new Set(observed.map(l=>l.session_id))
  const enabled=new Set([...loads,...reporting].filter(l=>l.skill===group.skill).map(l=>l.session_id))
  const signals=(report.applicability??[]).filter(a=>a.skill===group.skill&&sessions.has(a.session_id)&&enabled.has(a.session_id))
  const applicable=new Set(signals.map(a=>a.session_id))
  const loadedApplicable=[...applicable].filter(id=>signals.filter(a=>a.session_id===id).every(a=>observed.some(l=>l.session_id===id&&(l.parent_call_id??'')===(a.parent_call_id??'')&&timestamp(l.at)<=timestamp(a.at)))).length
  const allCards=new Set([...ids].map(singleCard).filter((c):c is string=>c!==null))
  const cards=new Set([...allCards].filter(card=>cardVersions.get(`${group.skill}:${card}`)?.size===1))
  const reviews=d.reviews.filter(r=>cards.has(markCardKey(r)))
  const assessed=evidence.filter(e=>e.source==='person'),declared=evidence.filter(e=>e.source==='agent')
  const facts=(report.facts??[]).filter(f=>ids.has(f.session_id))
  const times=observed.map(l=>timestamp(l.at)).filter(Number.isFinite)
  return {...group,loads:observed,reporting:coverage,evidence,firstSeen:times.length?Math.min(...times):null,sessions:ids.size,bytes:observed.reduce((n,l)=>n+l.bytes,0),reviews:reviews.length,findings:reviews.reduce((n,r)=>n+(r.findings?.length??0),0),rework:d.sessions.filter(s=>cards.has(singleCard(s.id)??'')).reduce((n,s)=>n+(s.timing?.phases.fixing??0),0),mixedCards:allCards.size-cards.size,corrections:assessed.filter(e=>e.result==='missed').length,assessed,declared,applicable:applicable.size,loadedApplicable,images:facts.reduce((n,f)=>n+f.image_artifacts,0),fullSuites:facts.reduce((n,f)=>n+f.full_suite_runs,0),redBeforeEdit:facts.reduce((n,f)=>n+f.reported_test_failures_before_edit,0)}
 }).filter(g=>g.loads.length||g.reporting.length).sort((a,b)=>a.skill.localeCompare(b.skill)||(a.firstSeen??Infinity)-(b.firstSeen??Infinity)||a.version.localeCompare(b.version))
}
// state drives the one-glyph summary (BLG-464): 'fewer' is the only state worth calling out as
// good, 'more' is worth attention, 'same'/the two 'insufficient-*' states are neutral/unknown.
export interface SkillBenefit { state:'insufficient-versions'|'insufficient-reviews'|'fewer'|'more'|'same'; text:string }
export function skillBenefit(versions:ReturnType<typeof skillCohorts>):SkillBenefit{
 if(versions.length<2)return {state:'insufficient-versions',text:'Not enough version evidence yet.'}
 const [before,after]=versions.slice(-2)
 if(before.firstSeen===null||after.firstSeen===null||before.firstSeen>=after.firstSeen||!before.reviews||!after.reviews)return {state:'insufficient-reviews',text:'Not enough comparable reviews across versions yet.'}
 const a=before.findings/before.reviews,b=after.findings/after.reviews
 const state=b<a?'fewer':b>a?'more':'same'
 const direction=state==='same'?'the same number of':state
 return {state,text:`The newer version’s linked cards have ${direction} findings per review (${a.toFixed(1)} to ${b.toFixed(1)}). The work may differ.`}
}
