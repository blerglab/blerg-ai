import type { ActivityReport, ActivityMark } from '../lib/insightsActivity'
export const emptyActivity: ActivityReport = {scope:'mine',generated_at:'2026-10-31T20:00:00Z',from:'2026-10-25T00:00:00Z',to:'2026-11-01T00:00:00Z',cards:[],sessions:[],reviews:[],failures:[],questions:[],omitted_cards:0,truncated:false}
// Optional owner-authorized local audit export. The CSV/converted records are
// ignored, never bundled into production or posted to the marks endpoint.
const localAudit=import.meta.glob<string>('../../.cache/blg378-review-audit.jsonl',{query:'?raw',import:'default',eager:true})
const audit=import.meta.env.VITE_INSIGHTS_SHOTS_DIR?Object.values(localAudit)[0]:undefined
export const hasLocalReviewAudit=!!audit
const localReviews:ActivityMark[]=audit?audit.trim().split('\n').filter(Boolean).map(line=>{
 const r=JSON.parse(line)
 return {session_id:'fixture',board_id:'fixture',card:r.card,card_ref:r.card_ref,by:r.reviewer_label,target:r.target,round:r.round,verdict:r.verdict,findings:r.findings,at:null,recorded_at:'',review_date:r.review_date}
}):[]
export const richActivity:ActivityReport={
 ...emptyActivity,
 cards:Array.from({length:8},(_,i)=>({key:`fixture#${i+1}`,size:i<6?'M':'L',risk:i<6?'medium':'high',complexity:'medium',size_first_set_at:'2026-10-24T00:00:00Z',flow:[{at:'2026-10-25T00:00:00Z',in_progress:true,done:false},{at:`2026-10-25T${String(i+1).padStart(2,'0')}:00:00Z`,in_progress:false,done:true}]})),
 sessions:Array.from({length:4},(_,i)=>({id:`worker-${i+1}`,title:['Improve ticket flow','Verify recorded reviews','Measure gate time','Check phone layout'][i],engine:i%2?'codex':'claude',has_subagents:i===0,parent_session_id:i?'worker-1':null,started_at:'2026-10-25T00:00:00Z',ended_at:'2026-10-25T04:00:00Z',shared_with_other_cards:false,cards:[`fixture#${i+1}`],timing:{seconds:{tests:3600+i*500,build_lint:1000,git:200,read:300,edit:200,other:100,model:2400,idle:300,waiting_on_person:200,unknown:0},phases:{building:2000,reviewing:600,fixing:300,waiting_on_person:200,idle:300,gates:4600+i*500},basis:'synthetic screenshot fixture',incomplete_tools:0}})),
 questions:[{session_id:'worker-1',asked:4,reached_person:1,cards:['fixture#1']},{session_id:'worker-2',asked:2,reached_person:0,cards:['fixture#2']}],
 reviews:[...localReviews,{session_id:'worker-1',board_id:'fixture',card:1,round:1,by:'fixture-reviewer',target:'abcdef1',verdict:'changes',findings:['high','medium'],recorded_at:'2026-10-25T04:00:00Z'}],
 failures:[{session_id:'worker-1',board_id:'fixture',card:1,type:'gate',suite:'unit tests',severity:'medium',recorded_at:'2026-10-25T03:00:00Z'},{session_id:'worker-1',board_id:'fixture',card:1,type:'gate',suite:'unit tests',severity:'medium',rerun_passed:true,recorded_at:'2026-10-25T04:00:00Z'}]
}

// Dated telemetry is separate from lifetime totals.
for(const s of richActivity.sessions){
 if(s.timing){s.timing.hours=Array.from({length:7},(_,i)=>({at:`2026-10-${25+i}T10:00:00Z`,seconds:Object.fromEntries(Object.entries(s.timing!.seconds).map(([k,v])=>[k,v/7])),phases:Object.fromEntries(Object.entries(s.timing!.phases).map(([k,v])=>[k,v/7])),detail:{'gates.tests':(s.timing!.phases.gates-1000)/7,'gates.build_lint':900/7,'gates.commit_hooks':100/7,'building.model':1400/7,'building.tools':900/7,'building.first':2000/7,'building.rework':300/7}}))
 s.timing.gate_runs=s.timing.hours.flatMap((h,i)=>['tests','build_lint','commit_hooks'].map(kind=>({at:h.at,kind,suite:i===0?undefined:kind==='tests'?'src/components':kind==='build_lint'?'typecheck':'commit hooks',region:i===0?undefined:'runner/frontend',seconds:h.detail![`gates.${kind}`],runs:kind==='tests'?4:1,passed:kind==='tests'?3:1,failed:kind==='tests'?1:0,unknown_outcome:0,repeated:i===0?0:kind==='tests'?2:0,reruns:i===0?0:kind==='tests'?1:0,repeat_unknown:i===0?kind==='tests'?4:1:0,failed_seconds:kind==='tests'?h.detail!['gates.tests']/4:0})))
 }
}
richActivity.history={from:'2026-10-18T00:00:00Z',sessions:richActivity.sessions.map(s=>({...s,timing:s.timing?{...s.timing,gate_runs:undefined,hours:s.timing.hours?.map(h=>({...h,at:new Date(Date.parse(h.at)-7*86400_000).toISOString(),phases:Object.fromEntries(Object.entries(h.phases).map(([k,v])=>[k,v*1.2])),detail:Object.fromEntries(Object.entries(h.detail||{}).map(([k,v])=>[k,v*1.2]))}))}:null})),failures:[]}
richActivity.events=[{session_id:'worker-1',board_id:'fixture',card:1,label:'Release 0.6.0',recorded_at:'2026-10-29T09:00:00Z'}]

if(localReviews.length){
 const seen=new Set(richActivity.cards.map(c=>c.key))
 for(const r of localReviews){const key=`fixture#${r.card??r.card_ref??'unknown'}`;if(!seen.has(key)){seen.add(key);richActivity.cards.push({key,size:null,risk:null,complexity:null,size_first_set_at:null,flow:null})}}
}
richActivity.skills={
 loads:[
  {session_id:'worker-1',role:'worker',engine:'claude',skill:'ux-design',version:'a'.repeat(64),mode:'on_demand',bytes:4210,basis:'native_skill_success'},
  {session_id:'worker-2',role:'worker',engine:'codex',skill:'ux-design',version:'b'.repeat(64),mode:'on_demand',bytes:4600,basis:'complete_body_observed'},
  {session_id:'worker-1',role:'subagent',parent_call_id:'native-review',engine:'claude',skill:'test-driven-development',version:'c'.repeat(64),mode:'on_demand',bytes:6100,basis:'native_skill_success'},
 ],
 reporting:[{session_id:'worker-3',role:'worker',engine:'hermes',skill:'ux-design',version:'b'.repeat(64),mode:'on_demand',bytes:4600,basis:'partial_native_observation'}],
 evidence:[{session_id:'worker-1',skill:'ux-design',version:'a'.repeat(64),rule:'say-once',result:'missed',source:'person',at:'2026-10-25T04:00:00Z'},{session_id:'worker-2',skill:'ux-design',version:'b'.repeat(64),rule:'screenshots',result:'followed',source:'agent',at:'2026-10-26T04:00:00Z'}],
 applicability:[{session_id:'worker-1',skill:'ux-design',basis:'successful_direct_edit'},{session_id:'worker-2',skill:'ux-design',basis:'successful_direct_edit'}],
 truncated:false,
}

for(const load of richActivity.skills.loads)load.at=load.version==='b'.repeat(64)?'2026-10-26T02:00:00Z':'2026-10-25T02:00:00Z'
for(const signal of richActivity.skills.applicability??[])signal.at='2026-10-26T03:00:00Z'
