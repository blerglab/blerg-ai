import { expect,it } from 'vitest'
import { skillBenefit, skillCohorts, type SkillLoad } from './skillInsights'
import { richActivity } from '../test/activityFixture'
const load:SkillLoad={session_id:'worker-1',role:'worker',engine:'claude',skill:'ux-design',version:'a'.repeat(64),mode:'on_demand',bytes:100,basis:'complete_body_observed'}
it('compares exact versions without counting a subagent as another conversation or an agent declaration as a correction',()=>{
 const cohorts=skillCohorts({...richActivity,skills:{loads:[load,{...load,parent_call_id:'native-worker',role:'subagent'},{...load,version:'b'.repeat(64),session_id:'worker-2'}],reporting:[],evidence:[{...load,rule:'say-once',result:'missed',source:'agent',at:''},{...load,rule:'plain-words',result:'missed',source:'person',at:''}],truncated:false}})
 expect(cohorts).toHaveLength(2)
 expect(cohorts[0].sessions).toBe(1)
 expect(cohorts[0].bytes).toBe(200)
 expect(cohorts[0].corrections).toBe(1)
 expect(cohorts[0].declared).toHaveLength(1)
 expect(cohorts[1].corrections).toBe(0)
})
it('does not substitute missing reporting with unused',()=>{expect(skillCohorts({...richActivity,skills:undefined})).toEqual([])})
it('does not score disabled skills as unused or loads that happened after an edit as timely',()=>{
 const d={...richActivity,skills:{loads:[{...load,at:'2026-10-25T04:00:00Z'}],reporting:[],evidence:[],applicability:[{session_id:'worker-1',skill:'ux-design',basis:'successful_direct_edit',at:'2026-10-25T03:00:00Z'},{session_id:'worker-2',skill:'ux-design',basis:'successful_direct_edit',at:'2026-10-25T03:00:00Z'}],truncated:false}}
 const cohort=skillCohorts(d)[0]
 expect(cohort.applicable).toBe(1)
 expect(cohort.loadedApplicable).toBe(0)
 expect(skillCohorts({...d,skills:{...d.skills,loads:[{...load,at:'2026-10-25T02:00:00Z'}]}})[0].loadedApplicable).toBe(1)
})
it('keeps partial responses and cards with mixed skill versions honest',()=>{
 expect(skillCohorts({...richActivity,skills:{loads:null,reporting:null,evidence:[],truncated:true} as unknown as import('./skillInsights').SkillReport})).toEqual([])
 const cohort=skillCohorts({...richActivity,skills:{loads:[load,{...load,version:'b'.repeat(64)}],reporting:[],evidence:[],truncated:false}})[0]
 expect(cohort.mixedCards).toBe(1)
 expect(cohort.reviews).toBe(0)
})

// skillBenefit drives the Skills tab's one-glyph "helping" summary (BLG-464): each state must be
// distinguishable, since fewer/more/same/insufficient become different glyphs and colors.
type Cohort=ReturnType<typeof skillCohorts>[number]
const cohort=(over:Partial<Cohort>):Cohort=>({skill:'ux-design',version:'a',loads:[],reporting:[],evidence:[],firstSeen:0,sessions:0,bytes:0,reviews:0,findings:0,rework:0,mixedCards:0,corrections:0,assessed:[],declared:[],applicable:0,loadedApplicable:0,images:0,fullSuites:0,redBeforeEdit:0,...over})
it('flags not enough version evidence with fewer than two versions',()=>{
 expect(skillBenefit([])).toEqual({state:'insufficient-versions',text:'Not enough version evidence yet.'})
 expect(skillBenefit([cohort({})])).toEqual({state:'insufficient-versions',text:'Not enough version evidence yet.'})
})
it('flags not enough comparable reviews when either version lacks reviews, or they are out of order',()=>{
 expect(skillBenefit([cohort({firstSeen:1,reviews:0}),cohort({firstSeen:2,reviews:3,findings:1})]).state).toBe('insufficient-reviews')
 expect(skillBenefit([cohort({firstSeen:2,reviews:3,findings:1}),cohort({firstSeen:1,reviews:2,findings:1})]).state).toBe('insufficient-reviews')
 expect(skillBenefit([cohort({firstSeen:null,reviews:2,findings:1}),cohort({firstSeen:2,reviews:2,findings:1})]).state).toBe('insufficient-reviews')
})
it('reports fewer, more or the same findings per review between the last two versions',()=>{
 const fewer=skillBenefit([cohort({firstSeen:1,reviews:2,findings:4}),cohort({firstSeen:2,reviews:2,findings:1})])
 expect(fewer.state).toBe('fewer')
 expect(fewer.text).toContain('fewer findings per review (2.0 to 0.5)')
 const more=skillBenefit([cohort({firstSeen:1,reviews:2,findings:1}),cohort({firstSeen:2,reviews:2,findings:4})])
 expect(more.state).toBe('more')
 expect(more.text).toContain('more findings per review (0.5 to 2.0)')
 const same=skillBenefit([cohort({firstSeen:1,reviews:2,findings:2}),cohort({firstSeen:2,reviews:2,findings:2})])
 expect(same.state).toBe('same')
 expect(same.text).toContain('the same number of findings per review (1.0 to 1.0)')
})
it('compares only the last two versions, ignoring an insufficient earlier one',()=>{
 const result=skillBenefit([cohort({firstSeen:0,reviews:0}),cohort({firstSeen:1,reviews:2,findings:4}),cohort({firstSeen:2,reviews:2,findings:1})])
 expect(result.state).toBe('fewer')
})
