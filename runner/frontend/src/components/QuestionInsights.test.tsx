import { expect, it } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import QuestionInsights from './QuestionInsights'
import { richActivity, emptyActivity } from '../test/activityFixture'
import { richInsights } from '../test/insightsFixture'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon } from '../test/fixtures'

it('leads health with dated failures and explains the types',()=>{
 render(<QuestionInsights view="health" activity={richActivity} summary={richInsights}/>)
 expect(screen.getByRole('figure',{name:'Failures'})).toBeInTheDocument()
 expect(screen.getByRole('button',{name:'Gate'})).toHaveAttribute('title',expect.stringContaining('required check'))
 expect(screen.queryByText('Value')).toBeNull()
 expect(screen.getByText(/Failures: no earlier activity/)).toBeInTheDocument()
 expect(screen.getByText(/Release 0.6.0/,{selector:'li'})).toBeInTheDocument()
 // Compact time slots cannot fit full event labels side by side. The event
 // marker stays aligned with its date; its full name is readable below once.
 expect([...document.querySelectorAll('.time-chart svg text')].some(t=>t.textContent?.includes('Release 0.6.0'))).toBe(false)
})
it('uses dated time with a category key and omits zero categories',()=>{
 render(<QuestionInsights view="time" activity={richActivity} summary={richInsights}/>)
 const gates=screen.getByRole('button',{name:'Gates'})
 expect(gates).toHaveAttribute('aria-pressed','true')
 fireEvent.click(screen.getByRole('button',{name:'Show Gates breakdown'}))
 expect(screen.getByRole('button',{name:'Hide Gates breakdown'})).toHaveAttribute('aria-expanded','true')
 expect(screen.getByTestId('time-breakdown-gates')).toBeInTheDocument()
 expect(screen.queryByRole('button',{name:'Unclassified'})).toBeNull()
 expect(screen.getByText(/Recorded time is down/)).toBeInTheDocument()
})
it('opens Building in place and keeps the parent time accounted for',()=>{
 render(<QuestionInsights view="time" activity={richActivity} summary={richInsights}/>)
 fireEvent.click(screen.getByRole('button',{name:'Show Building breakdown'}))
 expect(screen.getByTestId('time-breakdown-building')).toBeInTheDocument()
 expect(screen.getByRole('heading',{name:'First build and rework by card'})).toBeInTheDocument()
 expect(screen.getByRole('heading',{name:'By engine'})).toBeInTheDocument()
 fireEvent.click(screen.getByRole('button',{name:'Hide Building breakdown'}))
 expect(screen.queryByTestId('time-breakdown-building')).toBeNull()
})
it('can close a breakdown after changing to a period without that category',()=>{
 const v=render(<QuestionInsights view="time" activity={richActivity} summary={richInsights}/>)
 fireEvent.click(screen.getByRole('button',{name:'Show Gates breakdown'}))
 v.rerender(<QuestionInsights view="time" activity={emptyActivity} summary={richInsights}/>)
 expect(screen.queryByRole('button',{name:'Gates'})).toBeNull()
 fireEvent.click(screen.getByRole('button',{name:'Close breakdown'}))
 expect(screen.queryByTestId('time-breakdown-gates')).toBeNull()
})
it('keeps one labeled card detail section and explains unreadable links',()=>{
 const v=render(<QuestionInsights view="cost" activity={richActivity} summary={richInsights}/>)
 expect(document.querySelectorAll('.time-chart')).toHaveLength(1)
 v.rerender(<QuestionInsights view="cost" activity={{...emptyActivity,omitted_cards:4}} summary={richInsights}/>)
 expect(screen.getAllByText(/linked cards could not be read with your current board access/i).length).toBeGreaterThan(0)
})
it('does not infer a spending engine from model names or top workspaces',()=>{
 render(<QuestionInsights view="spending" activity={emptyActivity} summary={{...richInsights,tokens:{...richInsights.tokens,by_engine_day:undefined}}}/>)
 expect(screen.getByText(/Weekly pace is unavailable/)).toBeInTheDocument()
 expect(screen.getByText(/Spending is incomplete because model prices are missing/)).toBeInTheDocument()
 expect(document.querySelectorAll('.time-chart rect')).toHaveLength(0)
})
it('projects weekly account use using the reading time and reset, without summing accounts',()=>{
 const now=new Date().toISOString()
 useSessionStore.setState({daemons:[makeDaemon({usage_limits:[{provider:'claude',fetched_at:now,windows:[{id:'7d',used_percent:60,resets_at:new Date(Date.parse(now)+3.5*86400_000).toISOString()}]}]})]})
 render(<QuestionInsights view="spending" activity={{...emptyActivity,generated_at:now}} summary={richInsights}/>)
 expect(screen.getByText(/Claude is on pace to exceed its weekly limit/)).toBeInTheDocument()
 expect(screen.getByText(/Claude: 120% projected by reset/)).toBeInTheDocument()
 useSessionStore.setState({daemons:[]})
})
it('compares priced spending with the previous period',()=>{
 const row={turns:1,input:0,output:0,cache_read:0,cache_write:0,engine:'codex',complete:true,day:'2026-10-30',at:'2026-10-30T12:00:00Z',cost_usd:82}
 render(<QuestionInsights view="spending" activity={{...emptyActivity,from:'2026-10-26T00:00:00Z',to:'2026-11-02T00:00:00Z'}} summary={{...richInsights,tokens:{...richInsights.tokens,by_engine_day:[row],previous_by_engine_day:[{...row,at:'2026-10-23T12:00:00Z',cost_usd:100}]}}}/>)
 expect(screen.getByText('Estimated spending is down 18% against the previous 7 days.')).toBeInTheDocument()
})
it('waits for an hour of evidence after a weekly reset before projecting',()=>{
 const now=new Date().toISOString()
 useSessionStore.setState({daemons:[makeDaemon({usage_limits:[{provider:'codex',fetched_at:now,windows:[{id:'7d',used_percent:1,resets_at:new Date(Date.parse(now)+7*86400_000-5*60_000).toISOString()}]}]})]})
 render(<QuestionInsights view="spending" activity={emptyActivity} summary={richInsights}/>)
 expect(screen.getByText(/Codex: projection unavailable/)).toBeInTheDocument()
 expect(screen.queryByText(/% projected by reset/)).toBeNull()
 useSessionStore.setState({daemons:[]})
})
it('shows a weekly-budget burndown instead of dollar bars for a provider with usage-window samples',()=>{
 const start=Date.parse('2026-10-28T00:00:00Z'),resets=start+7*86400_000
 const usage_budget=[{provider:'claude',window_id:'7d',used_percent:22,window_start:new Date(start).toISOString(),resets_at:new Date(resets).toISOString(),sampled_at:new Date(start+86400_000).toISOString()}]
 render(<QuestionInsights view="spending" activity={emptyActivity} summary={{...richInsights,usage_budget}}/>)
 expect(screen.getByTestId('budget-burndown-claude')).toBeInTheDocument()
 expect(screen.getByText(/Claude is on a subscription plan: shown here as a percent of its weekly budget, not dollars/)).toBeInTheDocument()
 // Claude no longer draws dollar bars; the metered engine (codex, from richInsights' fixtures) still does.
 expect(screen.queryByRole('button',{name:'Claude'})).toBeNull()
})
it('keeps a metered engine in dollars when another is on a subscription plan (a mixed account)',()=>{
 const row={turns:1,input:0,output:0,cache_read:0,cache_write:0,engine:'codex',complete:true,day:'2026-10-30',at:'2026-10-30T12:00:00Z',cost_usd:42}
 const start=Date.parse('2026-10-28T00:00:00Z'),resets=start+7*86400_000
 const usage_budget=[{provider:'claude',window_id:'7d',used_percent:22,window_start:new Date(start).toISOString(),resets_at:new Date(resets).toISOString(),sampled_at:new Date(start+86400_000).toISOString()}]
 render(<QuestionInsights view="spending" activity={{...emptyActivity,from:'2026-10-26T00:00:00Z',to:'2026-11-02T00:00:00Z'}} summary={{...richInsights,tokens:{...richInsights.tokens,by_engine_day:[row],previous_by_engine_day:undefined},usage_budget}}/>)
 expect(screen.getByTestId('budget-burndown-claude')).toBeInTheDocument()
 expect(screen.getByRole('button',{name:'Codex'})).toBeInTheDocument()
 expect(screen.getByText(/Codex is metered and kept in dollars below/)).toBeInTheDocument()
})
it('answers skills without claiming compliance or benefit from a load, as a glanceable card',()=>{
 render(<QuestionInsights view="skills" activity={{...richActivity,skills:{loads:[{session_id:'worker-1',role:'subagent',engine:'claude',skill:'ux-design',version:'a'.repeat(64),mode:'on_demand',bytes:100,basis:'complete_body_observed'}],reporting:[],evidence:[],truncated:false}}} summary={richInsights}/>)
 expect(screen.getByRole('heading',{level:1})).toHaveTextContent('Are my skills helping?')
 const card=screen.getByTestId('skill-card-ux-design')
 expect(card).toHaveTextContent('1 load')
 expect(card).toHaveTextContent('1 conversation')
 // "Not enough version evidence" with one version/no assessment: an unknown-state glyph, dashes
 // for the unmeasured stats, not a sentence claiming compliance or benefit.
 expect(within(card).getByRole('img',{name:/Helping: Not enough version evidence yet\./})).toBeInTheDocument()
 const stats=within(card).getByTestId('skill-stats-ux-design')
 expect(within(stats).getAllByRole('definition').map(f=>f.textContent)).toEqual(['—','—','0'])
 // BLG-464 fix: skills has no subject-bearing trend line to answer the heading with ("is down 17%
 // against the previous 7 days" read as a subjectless sentence fragment); the question answers
 // from the cards instead, not from a reused time-series comparison.
 expect(screen.queryByText(/against the previous/)).toBeNull()
})
it('keeps a subject on every other tab\'s trend line (no shared regression)',()=>{
 render(<QuestionInsights view="time" activity={richActivity} summary={richInsights}/>)
 expect(screen.getByText(/^Recorded time (is|has)/)).toBeInTheDocument()
})

it('keeps a skill reporting outage separate from other Insights',()=>{
 render(<QuestionInsights view="skills" activity={{...richActivity,skills:{...richActivity.skills!,unavailable:true}}} summary={richInsights}/>)
 expect(screen.getByText(/Skill evidence is temporarily unavailable/)).toBeInTheDocument()
 expect(screen.queryByText(/No skill loads/)).toBeNull()
})
it('draws one line per conversation over the full report window, Main named and emphasized',()=>{
 render(<MemoryRouter><QuestionInsights view="context" activity={emptyActivity} summary={richInsights}/></MemoryRouter>)
 expect(screen.getByRole('heading',{level:1})).toHaveTextContent('How full is my context?')
 expect(screen.getByRole('figure',{name:'Context fill'})).toBeInTheDocument()
 expect(screen.getAllByText(/Main is 81% full as of/).length).toBeGreaterThan(0)
 expect(screen.queryByText(/No context readings were recorded in this period/)).toBeNull()
 expect(document.querySelector('.context-fill-series-main')).toBeTruthy()
 // Seven conversations total; Main plus the 5 fullest others are drawn, the 6th behind "See all".
 expect(document.querySelectorAll('.context-fill-series')).toHaveLength(6)
 expect(screen.getByRole('button',{name:'See all 6'})).toBeInTheDocument()
 // A point-in-time percent has no additive previous-period sum (BLG-464 round 2): no comparison
 // sentence or "completed UTC hours" note, and no generic TimeChart underneath.
 expect(screen.queryByText(/Completed UTC hours/)).toBeNull()
 expect(document.querySelectorAll('.time-chart svg')).toHaveLength(1)
})
it('shows a no-data note when no context readings fall in the requested period',()=>{
 render(<MemoryRouter><QuestionInsights view="context" activity={{...emptyActivity,from:'2026-09-14T00:00:00Z',to:'2026-10-14T00:00:00Z'}} summary={richInsights}/></MemoryRouter>)
 expect(screen.getByText(/No context readings were recorded in this period/)).toBeInTheDocument()
})
it('flags truncated context data separately from the no-data note',()=>{
 render(<MemoryRouter><QuestionInsights view="context" activity={emptyActivity} summary={{...richInsights,context_fill_truncated:true}}/></MemoryRouter>)
 expect(screen.getByText(/Some context readings are outside this report's limit/)).toBeInTheDocument()
 expect(screen.queryByText(/No context readings were recorded in this period/)).toBeNull()
})
it('labels pending historical timing without suggesting a shorter period',()=>{
 render(<QuestionInsights view="time" activity={{...richActivity,pending_sessions:12}} summary={richInsights}/>)
 expect(screen.getByText(/timing for 12 completed conversations/i)).toHaveAttribute('role','status')
 expect(screen.getByText(/Recorded time is incomplete/)).toBeInTheDocument()
 expect(screen.queryByText(/Try a shorter period/)).toBeNull()
 fireEvent.click(screen.getByRole('button',{name:'Show Gates breakdown'}))
 expect(screen.getByTestId('time-breakdown-gates')).toHaveTextContent('Gate time comparison is incomplete while older timing is being prepared.')
})
