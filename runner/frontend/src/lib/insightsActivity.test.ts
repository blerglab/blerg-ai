import { it, expect } from 'vitest'
import { engineReviewSpreads, knownGateSeconds, activeSeconds, engineSpreads, flowSpreads, activitySlice, reviewRounds, type ActivityReport } from './insightsActivity'
const report: ActivityReport = {
 scope: 'mine', generated_at: '2026-10-31T20:00:00Z', from: '2026-10-24T00:00:00Z', to: '2026-11-01T00:00:00Z', omitted_cards: 0, truncated: false,
 cards: [{ key: 'board-a#7', size: 'M', risk: 'high', complexity: 'low', size_first_set_at: '2026-10-20T00:00:00Z', flow: [{ at:'2026-10-25T00:00:00Z', in_progress:true, done:false },{ at:'2026-10-25T01:00:00Z', in_progress:false, done:true }] }], sessions: [], reviews: [{ session_id:'a', board_id:'board-a',card:7,round:2,by:'codex',target:'abcdef1',verdict:'approved',findings:[],recorded_at:'2026-10-25T02:00:00Z' }], failures: []
}


it('slices risk without reassigning shared worker time to one card', () => {
 const sliced = activitySlice({...report,sessions:[{ id:'s', parent_session_id:null, started_at:null, ended_at:null, shared_with_other_cards:true, cards:['board-a#7','board-a#8'], timing:null }]},'high')
 expect(sliced.cards).toHaveLength(1)
 expect(sliced.sessions).toHaveLength(0)
})
it('counts observed rounds, preserving gaps rather than inventing round one', () => {
 expect(reviewRounds(report.reviews)).toEqual([{key:'1 review round',count:1}])
})


it('keeps unlinked owner time in the unknown-risk slice', () => {
 const d={...report,sessions:[{id:'unlinked',parent_session_id:null,started_at:null,ended_at:null,shared_with_other_cards:false,cards:[],timing:null}]}
 expect(activitySlice(d,'unknown').sessions.map(s=>s.id)).toEqual(['unlinked'])
})
it('sorts round counts numerically and retains records without a round', () => {
 const base=report.reviews[0]
 const rows=reviewRounds([...Array.from({length:10},(_,i)=>({...base,card:1,round:i+1})),...Array.from({length:2},(_,i)=>({...base,card:2,round:i+1})),{...base,card:3,round:undefined}])
 expect(rows.map(r=>r.key)).toEqual(['2 review rounds','10 review rounds','Round number missing'])
})

it('does not put omitted-card sessions into the unlinked unknown-risk slice', () => {
 const d={...report,sessions:[{id:'s',parent_session_id:null,started_at:null,ended_at:null,shared_with_other_cards:false,card_attribution_unknown:true,cards:[],timing:null}]}
 expect(activitySlice(d,'unknown').sessions).toEqual([])
})

it('distinguishes unknown gate time from observed zero and uses active time only',()=>{
 const s={id:'s',parent_session_id:null,started_at:null,ended_at:null,cards:[],shared_with_other_cards:false,timing:{seconds:{model:10,idle:20,waiting_on_person:30,unknown:0},phases:{gates:0},basis:'recorded',incomplete_tools:0}}
 expect(knownGateSeconds(s)).toBe(0)
 expect(activeSeconds(s)).toBe(10)
 expect(knownGateSeconds({...s,timing:{...s.timing,seconds:{unknown:100}}})).toBeNull()
})
it('compares engine observations only inside an explicit risk tag',()=>{
 const s={id:'s',engine:'codex',parent_session_id:null,started_at:null,ended_at:null,cards:['board-a#7'],shared_with_other_cards:false,timing:{seconds:{model:10,idle:20},phases:{gates:0},basis:'recorded',incomplete_tools:0}}
 const d={...report,sessions:[s]}
 expect(engineSpreads(d,'all')).toEqual([])
 expect(engineSpreads(d,'high')[0]).toMatchObject({step:'codex',count:1,samples:[10]})
 expect(engineSpreads(d,'low')).toEqual([])
})

it('slices card evidence by size, observed building engine and recorded subagent use',()=>{
 const session={id:'builder',engine:'codex',has_subagents:true,parent_session_id:null,started_at:null,ended_at:null,cards:['board-a#7'],shared_with_other_cards:false,timing:{seconds:{model:10},phases:{building:10},basis:'recorded',incomplete_tools:0}}
 const d={...report,sessions:[session],questions:[{session_id:'old-worker',asked:3,reached_person:1,cards:['board-a#7']}]}
 expect(activitySlice(d,'high',{size:'M',engine:'codex',subagents:'yes'}).questions).toHaveLength(1)
 expect(activitySlice(d,'high',{size:'S'}).cards).toEqual([])
 expect(activitySlice(d,'high',{engine:'claude'}).reviews).toEqual([])
 expect(activitySlice(d,'high',{subagents:'no'}).questions).toEqual([])
 expect(activitySlice({...d,sessions:[{...session,has_subagents:undefined}]},'all',{subagents:'no'}).cards).toEqual([])
 expect(activitySlice({...d,sessions:[{...session,timing:{...session.timing,phases:{reviewing:10}}}]},'all',{engine:'codex'}).cards).toEqual([])
})

it('slices reviewers independently, preserving flow and missing attribution',()=>{
 expect(activitySlice(report,'all',{reviewer:'claude'}).reviews).toEqual([])
 expect(activitySlice(report,'all',{reviewer:'codex'}).cards).toEqual(report.cards)
 expect(activitySlice({...report,reviews:report.reviews.map(r=>({...r,by:undefined}))},'all',{reviewer:'unknown'}).reviews).toHaveLength(1)
})

it('never allocates shared or unreadable question counts to a size or risk',()=>{
 const d={...report,questions:[{session_id:'a',asked:3,reached_person:1,cards:['board-a#7','board-b#1']},{session_id:'b',asked:1,reached_person:1,cards:[],card_attribution_unknown:true}]}
 expect(activitySlice(d,'high').questions).toEqual([])
 expect(activitySlice(d,'all',{size:'unknown'}).questions).toEqual([])
 expect(activitySlice(d,'all').questions).toHaveLength(2)
})

it('compares review rounds by building engine within risk, once per card',()=>{
 const s={id:'builder',engine:'codex',parent_session_id:null,started_at:null,ended_at:null,cards:['board-a#7'],shared_with_other_cards:false,timing:{seconds:{model:10},phases:{building:10},basis:'recorded',incomplete_tools:0}}
 const d={...report,sessions:[s,{...s,id:'second'}],reviews:[...report.reviews,...report.reviews,{...report.reviews[0],round:3}]}
 expect(engineReviewSpreads(d,'all')).toEqual([])
 expect(engineReviewSpreads(d,'high')[0]).toMatchObject({step:'codex',count:1,samples:[2]})
 expect(engineReviewSpreads(d,'low')).toEqual([])
 expect(engineReviewSpreads({...d,sessions:[s,{...s,engine:'claude'}]},'high')).toEqual([])
 expect(engineReviewSpreads({...d,reviews:[{...report.reviews[0],round:undefined}]},'high')).toEqual([])
 expect(engineReviewSpreads({...d,sessions:[{...s,shared_with_other_cards:true}]},'high')).toEqual([])
})

it('sums linked workers and subagents active time, excluding idle and person wait',()=>{
 const worker={id:'w',parent_session_id:null,started_at:null,ended_at:null,cards:['board-a#7'],shared_with_other_cards:false,timing:{seconds:{model:10,tests:5,turn_end:2,idle:100,waiting_on_person:200},phases:{},basis:'recorded',incomplete_tools:0}}
 const d={...report,sessions:[worker,{...worker,id:'child',parent_session_id:'w'}]}
 expect(flowSpreads(d)[0]).toMatchObject({step:'M',samples:[34],count:1})
 expect(flowSpreads({...d,cards:d.cards.map(c=>({...c,flow:null,size_first_set_at:null}))})[0].samples).toEqual([34])
 expect(flowSpreads(report)).toEqual([])
 expect(flowSpreads({...d,sessions:[{...worker,shared_with_other_cards:true}]})).toEqual([])
 expect(flowSpreads({...d,sessions:[{...worker,timing:null}]})).toEqual([])
 expect(flowSpreads({...d,sessions:[{...worker,timing:{...worker.timing,seconds:{unknown:20}}}]})).toEqual([])
})

it('retains unknown native subagent time rather than using elapsed duration',()=>{
 const d={...report,sessions:[{id:'s',parent_session_id:null,started_at:null,ended_at:null,cards:['board-a#7'],shared_with_other_cards:false,native_active_time_unknown:true,timing:{seconds:{model:10},phases:{},basis:'recorded',incomplete_tools:0}}]}
 expect(flowSpreads(d)).toEqual([])
})
