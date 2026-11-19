import { CardRefText } from './ChecklistRefs'
import { ChecklistRefsContext, useChecklistRefs } from '../lib/checklistRefs'
import type { CardTarget } from '@blerglab/chat'
import { useBlackboardDraft, useBlackboardRevision, type DraftPlan } from '../hooks/useBlackboardDraft'
import { newestOpenMilestone } from '../lib/blackboardMilestones'
import { blackboardTargetDate } from '../lib/blackboardDate'
import { blackboardAxisTicks, blackboardCountTicks } from '../lib/blackboardAxis'
import { blackboardEventLabel, blackboardEventPayload, type ReleaseStyle } from '../lib/blackboardStyle'
import BlackboardProposal, { type PlanProposal } from './BlackboardProposal'
import { useCallback, useEffect, useId, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Hover, IconButton, TicketModal } from '@blerglab/chat'
import { IconMenuItem } from './IconButton'
import { BoardCard, type BoardCardData } from '../../../../packages/board/BoardPresentation'
import { blackboardReads, readBlackboards, type BlackboardRecordSummary } from '../lib/blackboardReads'
import { apiFetch } from '../apiFetch'
import { mark } from '../lib/releasePerfMarks'
import type { BlackboardPath, BlackboardView, SessionTabs } from '../hooks/useSessionTabs'
import { PinnedTicketModal } from './BoardCardView'
import './BlackboardTabs.css'
import BlackboardSkeleton, { BlackboardCountSkeleton } from './BlackboardSkeleton'
import { blackboardViewKey, cachedBlackboardView, cacheBlackboardView, blackboardViewGeneration, invalidateBlackboardViews } from '../lib/blackboardViewCache'

type Milestone = { id: string; name: string; target: string | null; frozen_at: string | null; shipped_at?: string | null; cut_receipt_id?: string; evidence_incomplete?: boolean; stage: string; revision: number; board_id: string; release_id: string }
type Thrust = { id: string; name: string; milestone_id: string; goal_id: string }
type Entry = { card: BoardCardData & { board_id: string }; stage: string; membership: { thrust_id: string; state: string; reason: string; destination?: string }; deleted: boolean; drift: boolean }
type PlainMembership = { id: string; goal_id?: string; state: string }
type PlainEntry = { card: { id: string; title: string }; membership: PlainMembership; stage: string }
type Snapshot = { milestone_id: string; day: string; counts: Record<string, number>; complete: boolean }
type PlanEvent = { milestone_id: string; id: string; kind: string; recorded_at: string; claimed_at?: string; source: string; payload: unknown }
type RecordView = { style?: ReleaseStyle; proposals?: PlanProposal[] | null; name: string; vision: string; plan_revision: number; milestones: Milestone[] | null; goals: { id: string; name: string }[] | null; thrusts: Thrust[] | null; snapshots: Snapshot[] | null; events: PlanEvent[] | null }
type Slice = { cards: Entry[]; complete: boolean; frozen: boolean }
type PlainSlice = { cards: PlainEntry[]; complete: boolean }
export type Data = { record: RecordView; pending_proposals: number; slices: Record<string, Slice>; plain: PlainSlice | null }
const stages = ['done', 'to_test', 'in_work', 'next', 'needs_you']
const labels: Record<string, string> = { all: 'All', active: 'Active', done: 'Done', to_test: 'To test', in_work: 'In work', next: 'Next', needs_you: 'Needs you', unplanned: 'Unplanned', moved_out: 'Moved out', shipped: 'Shipped', drift: 'Added after release' }
const stageColor = (stage: string) => `var(--bb-${stage === 'shipped' ? 'done' : ['moved_out','all'].includes(stage) ? 'next' : stage === 'active' ? 'in_work' : stage})`
const count = (cards: Entry[], stage: string) => cards.filter(c => matches(c, stage)).length
function matches(c: Entry, filter: string): boolean { return filter === 'all' || filter === 'shipped' && ['done', 'to_test'].includes(c.stage) || (filter === 'active' ? !['done', 'to_test'].includes(c.stage) && c.membership.state !== 'moved_out' : ['unplanned', 'moved_out'].includes(filter) ? c.membership.state === filter : c.stage === filter) }
function Bar({ cards }: { cards: Entry[] }) { return <div className="blackboard-bar" role="img" aria-label={stages.map(s => `${count(cards, s)} ${labels[s]}`).join(', ')}>{stages.map(s => <i key={s} style={{ width: `${100 * count(cards, s) / (cards.length || 1)}%`, background: stageColor(s) }} />)}</div> }
function useBlackboardRecords(sessionId: string): BlackboardRecordSummary[] {
 const revision = useBlackboardRevision(sessionId)
 const [records, setRecords] = useState<BlackboardRecordSummary[]>(() => blackboardReads.get(sessionId)?.list ?? [])
 useEffect(() => { let alive = true; void readBlackboards(sessionId).then(list => { if (alive) setRecords(list) }); return () => { alive = false } }, [sessionId, revision])
 return records
}

// The sentinel blackboardId BlackboardScreen reads as "no blackboard yet, show the empty state"
// (card BLG-450, round 3): the button always opens a tab now, never a disabled control, so there
// has to be a real view to open even before a blackboard exists.
const NO_BLACKBOARD = ''

/** What clicking "Open blackboard" opens: the project's single blackboard's current (unfrozen)
 *  milestone, or its latest milestone, or the project level if it has none yet — or, with no
 *  blackboard record at all, the read-only empty state. The button is
 *  never disabled (round 3 review): it always has somewhere to send the person. */
function blackboardOpenView(records: BlackboardRecordSummary[]): Parameters<SessionTabs['open']>[0] {
 if (!records.length) return { kind: 'blackboard', blackboardId: NO_BLACKBOARD, label: 'Blackboard', path: { level: 'project' } }
 const record = records[0]
 const m = newestOpenMilestone(record.milestones ?? []) ?? record.milestones?.at(-1)
 return { kind: 'blackboard', blackboardId: record.id, label: m?.name ?? 'Project', path: m ? { level: 'milestone', milestoneId: m.id } : { level: 'project' } }
}

/** Shared between the full button and the collapsed "Open" menu's row, so both draw the same icon. */
export function BlackboardIcon() {
 return <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" aria-hidden="true"><rect x="3" y="4" width="18" height="15" rx="2"/><path d="M7 8h10M7 12h6M9 19l-2 3m8-3 2 3"/></svg>
}

export function BlackboardButton({ sessionId, tabs }: { sessionId: string; tabs: SessionTabs }) {
 const records = useBlackboardRecords(sessionId)
 const view = blackboardOpenView(records)
 // Always shown and never disabled (card BLG-450, round 3 review): with nothing set up yet, it
 // opens the read-only empty-state tab.
 return <button type="button" className="icon-btn" aria-label="Open blackboard" title="Open blackboard" onClick={() => tabs.open(view, true)}><BlackboardIcon /></button>
}

/** The same opener, as a menu item inside the header's collapsed "Open" menu (narrow widths). */
export function BlackboardMenuItem({ sessionId, tabs, onSelect }: { sessionId: string; tabs: SessionTabs; onSelect(): void }) {
 const records = useBlackboardRecords(sessionId)
 const view = blackboardOpenView(records)
 return <IconMenuItem label="Open blackboard" icon={<BlackboardIcon />} onSelect={() => { tabs.open(view, true); onSelect() }} />
}
export default function BlackboardTabs({ tabs, sessionId }: { tabs: SessionTabs; sessionId: string }) {
 return <>{tabs.tabs.map(tab => tab.view.kind === 'blackboard' && tabs.visible.includes(tab.id) && createPortal(<BlackboardScreen key={`${sessionId}:${tab.id}`} view={tab.view} sessionId={sessionId} navigate={(path, label, blackboardId) => tabs.navigateBlackboard(tab.id, path, label, blackboardId)} />, tabs.targetFor(tab.id), tab.id))}</>
}
// No blackboard yet (the NO_BLACKBOARD sentinel, set by blackboardOpenView so the opener is never
// disabled): the read-only empty state, rather than trying to load a
// blackboard that does not exist. Creating one switches this same tab to BlackboardRecordScreen
// below (navigate with the new id) — there is only ever one blackboard renderer. This dispatcher
// itself calls no hooks, on purpose: BlackboardRecordScreen's hooks must run in the same order on
// every one of its own renders, which a single component swapping between the two bodies on a
// condition that changes at runtime (navigate flips blackboardId from '' to a real id, same tab)
// would violate.
function BlackboardScreen({ view, sessionId, navigate }: { view: BlackboardView; sessionId: string; navigate: (path: BlackboardPath, label: string, blackboardId?: string) => void }) {
 if (view.blackboardId === NO_BLACKBOARD) {
  return <BlackboardEmptyState sessionId={sessionId} onCreated={(blackboardId, path, label) => navigate(path, label, blackboardId)} />
 }
 return <BlackboardRecordScreen view={view} sessionId={sessionId} navigate={navigate} />
}
function BlackboardRecordScreen({ view, sessionId, navigate }: { view: BlackboardView; sessionId: string; navigate: (path: BlackboardPath, label: string, blackboardId?: string) => void }) {
 const revision = useBlackboardRevision(sessionId)
 const base = `/api/blackboards/${encodeURIComponent(view.blackboardId)}`
 const query = `?session=${encodeURIComponent(sessionId)}`
 const cacheKey = blackboardViewKey(view.blackboardId, query)
 const [data, setData] = useState<Data | null>(() => cachedBlackboardView(cacheKey))
 const [loadingCards, setLoadingCards] = useState(() => !cachedBlackboardView(cacheKey))
 const [animateBars, setAnimateBars] = useState(false)
 useEffect(() => { if (!animateBars) return; const timer = window.setTimeout(() => setAnimateBars(false), 700); return () => clearTimeout(timer) }, [animateBars])
 // Only the first load says it is loading; a focus or 30 s refresh keeps the shown cards quiet.
 const cardsShown = useRef(!!cachedBlackboardView(cacheKey))
 // BLG-453: a no-op everywhere except the release-perf harness.
 useEffect(() => { if (data) mark('blackboard-rendered') }, [data])
 const [error, setError] = useState('')
 const [attempt, setAttempt] = useState(0)
 const [filter, setFilter] = useState(view.path.level === 'thrust' ? 'active' : 'all')
 const [sort, setSort] = useState('state')
 const [projectFilter, setProjectFilter] = useState('all')
 const [proposal, setProposal] = useState<PlanProposal | null>(null)
 const [ticket, setTicket] = useState<Entry | null>(null)
 useEffect(() => {
  let alive = true
  const load = async () => {
   const generation = blackboardViewGeneration()
   setAnimateBars(false)
   try {
    const read = async (url: string) => { const r = await apiFetch(url); if (!r.ok) throw new Error(r.status === 403 || r.status === 404 ? 'This workspace cannot read this blackboard.' : 'Couldn’t load the blackboard.'); return r.json() }
    const body = await read(base + query)
    let cursor = body.next_cursor
    const seen = new Set<string>()
    while (cursor) {
     if (seen.has(cursor)) throw new Error('Couldn’t load the complete blackboard. Refresh and try again.')
     seen.add(cursor)
     const next = await read(base + query + '&cursor=' + encodeURIComponent(cursor))
     if (next.record.version !== body.record.version) throw new Error('The blackboard changed. Refresh to load it again.')
     for (const key of ['milestones','goals','thrusts','snapshots','events','proposals']) body.record[key] = [...(body.record[key] ?? []), ...(next.record[key] ?? [])]
     cursor = next.next_cursor
    }
    if (alive) { setData(previous => previous ?? { ...body, slices: {}, plain: null }); if (!cardsShown.current) setLoadingCards(true) }
    const slices = Object.fromEntries(await Promise.all((body.record.milestones ?? []).map(async (m: Milestone) => {
     const url = `${base}/milestones/${encodeURIComponent(m.id)}/slice${query}`
     const slice = await read(url)
     let cursor = slice.next_cursor
     const seen = new Set<string>()
     while (cursor) {
      if (seen.has(cursor)) throw new Error('Couldn’t load all cards. Refresh and try again.')
      seen.add(cursor)
      const next = await read(url + '&cursor=' + encodeURIComponent(cursor))
      slice.cards = [...(slice.cards ?? []), ...(next.cards ?? [])]
      slice.complete = slice.complete && next.complete
      cursor = next.next_cursor
     }
     return [m.id, slice]
    })))
    let plain: PlainSlice | null = null
    {
     const url = `${base}/slice${query}`
     const slice = await read(url)
     let cursor = slice.next_cursor
     const seen = new Set<string>()
     while (cursor) {
      if (seen.has(cursor)) throw new Error('Couldn’t load all items. Refresh and try again.')
      seen.add(cursor)
      const next = await read(url + '&cursor=' + encodeURIComponent(cursor))
      slice.cards = [...(slice.cards ?? []), ...(next.cards ?? [])]
      slice.complete = slice.complete && next.complete
      cursor = next.next_cursor
     }
     plain = { cards: slice.cards ?? [], complete: slice.complete }
    }
    if (alive) {
     const complete = { ...body, slices, plain }
     if (Object.values(slices).every(slice => (slice as Slice).complete) && plain?.complete) cacheBlackboardView(cacheKey, complete, generation)
     setData(complete)
     setLoadingCards(false)
     if (!cardsShown.current) setAnimateBars(true)
     cardsShown.current = true
     setError('')
    }
   } catch (e) { console.error('Could not load blackboard view.', { cause: e instanceof TypeError ? 'network' : 'response' }); invalidateBlackboardViews([sessionId]); if (alive) { cardsShown.current = false; setData(null); setTicket(null); setProposal(null); setError(e instanceof Error ? e.message : 'Couldn’t load the blackboard.') } }
  }
  void load()
  const refresh = () => { void load() }
  window.addEventListener('focus', refresh)
  const timer = window.setInterval(refresh, 30_000)
  return () => { alive = false; clearInterval(timer); window.removeEventListener('focus', refresh) }
 }, [view.blackboardId, sessionId, attempt, base, query, revision, cacheKey])
 const go = (path: BlackboardPath, label: string) => { setFilter(path.level === 'thrust' ? 'active' : 'all'); setTicket(null); navigate(path, label) }
 if (!data && !error) return <BlackboardSkeleton />
 if (!data) return <section className="blackboard" aria-label="Blackboard"><p role="status">{error}</p>{error && <IconButton label="Retry blackboard" onClick={() => setAttempt(n => n + 1)} />}</section>
 const r = data.record, milestones = r.milestones ?? [], thrusts = r.thrusts ?? []
 const m = milestones.find(m => m.id === view.path.milestoneId)
 const t = thrusts.find(t => t.id === view.path.thrustId) ?? (view.path.thrustId === 'everything-else' ? { id: 'everything-else', name: 'Everything else', goal_id: '' } : undefined)
 const project = view.path.level === 'project'
 const cards = project ? Object.values(data.slices).flatMap(s => s.cards ?? []) : (data.slices[m?.id ?? '']?.cards ?? []).filter(c => !t || c.membership.thrust_id === t.id || t.id === 'everything-else' && !c.membership.thrust_id)
 const countsPending = loadingCards || (!project && !!m && data.slices[m.id]?.complete !== true)
 const preRecord = !!m?.frozen_at && !cards.length && !(r.snapshots ?? []).some(s => s.milestone_id === m.id)
 const selectedFilter = filter === 'active' && !cards.some(c => matches(c, 'active')) ? 'all' : filter
 const cardFilters = m?.frozen_at && !t ? ['all','shipped','moved_out','unplanned'] : [...(t ? ['active'] : []),'all','shipped',...stages,'unplanned']
 const shown = cards.filter(c => matches(c, selectedFilter)).sort((a, b) => sort === 'title' ? a.card.title.localeCompare(b.card.title) : sort === 'number' ? a.card.number - b.card.number : ['needs_you', 'in_work', 'next', 'to_test', 'done'].indexOf(a.stage) - ['needs_you', 'in_work', 'next', 'to_test', 'done'].indexOf(b.stage))
 const pending = r.proposals?.find(p => p.status === 'pending')
 const small = project && !milestones.length
 // Release progress counts what is still in it: moved-out cards have no stage segment (as countSlice).
 const progress = cards.filter(c => c.membership.state !== 'moved_out' && c.stage !== 'moved_out')
 const plainItems = (data.plain?.cards ?? []).filter(i => i.membership.state !== 'moved_out')
 const tile = (name: string, entries: Entry[], path: BlackboardPath) => {
  const tilePending = loadingCards || data.slices[path.milestoneId ?? '']?.complete !== true
  const shipped = count(entries, 'shipped')
  const goal = (r.goals ?? []).find(g => g.id === thrusts.find(t => t.id === path.thrustId)?.goal_id)
  const next = entries.filter(c => matches(c, 'active')).sort((a,b) => ['needs_you','in_work','next'].indexOf(a.stage) - ['needs_you','in_work','next'].indexOf(b.stage)).slice(0,3)
  return <div className="blackboard-thrust" key={`${path.milestoneId}:${path.thrustId ?? name}`}>
   {goal && <p className="blackboard-goal-caption">{goal.name}</p>}
   <Hover target={{kind:'run',title:name,facts:{}}} content={<><strong>{name}</strong><p>{tilePending ? 'Loading card details…' : stages.map(stage => `${count(entries,stage)} ${labels[stage].toLowerCase()}`).join(' · ')}</p></>}><button className="blackboard-tile" onClick={() => go(path, name)}><span>{name}<small>{tilePending ? <BlackboardCountSkeleton /> : entries.length ? `${shipped} of ${entries.length} shipped` : milestones.find(m => m.id === path.milestoneId)?.frozen_at ? 'No card history' : 'No cards yet'}</small></span>{tilePending ? <span className="initial-skeleton-value blackboard-skeleton-bar" aria-hidden="true" /> : <Bar cards={entries} />}<p className="blackboard-tile-counts">{tilePending ? <BlackboardCountSkeleton /> : count(entries, 'active') ? `${count(entries, 'active')} open` : entries.length > 0 && shipped === entries.length ? 'All shipped' : null}</p></button></Hover>
   {!!next.length && <div className="blackboard-next" aria-label={`${name}: next cards`}>{next.map(c => <Hover key={`${c.card.board_id}:${c.card.number}`} target={{kind:'card',source:'blerg-board',boardId:c.card.board_id,key:c.card.reference ?? `#${c.card.number}`,title:c.card.title,status:labels[c.stage]}}><button className="blackboard-review" onClick={() => setTicket(c)}>{c.card.reference ?? `#${c.card.number}`}</button></Hover>)}</div>}
  </div>
 }

 return <section className={`blackboard${project ? " blackboard-project" : ""}${animateBars ? " blackboard-bars-enter" : ""}`} aria-label="Blackboard">
  <nav className="blackboard-nav" aria-label="Blackboard levels"><button className="blackboard-pill" aria-current={project ? 'page' : undefined} onClick={() => go({ level: 'project' }, 'Project')}>Project</button>{milestones.map(milestone => <button key={milestone.id} className="blackboard-pill" aria-current={!project && !t && m?.id === milestone.id ? 'page' : undefined} onClick={() => go({ level: 'milestone', milestoneId: milestone.id }, milestone.name)}>{milestone.name}</button>)}{t && <span className="blackboard-pill" aria-current="page">{t.name}</span>}<IconButton label="Refresh blackboard" onClick={() => { setAttempt(n => n + 1) }} /></nav>
  <header className="blackboard-heading"><h1>{project ? r.name : t?.name ?? m?.name}</h1><span>{countsPending && !project ? <BlackboardCountSkeleton /> : project ? null : t ? `${(r.goals ?? []).find(g => g.id === t.goal_id)?.name ?? 'Everything else'} · ${cards.length ? Math.round(100 * (count(cards, 'done') + count(cards, 'to_test')) / cards.length) : 0}% built` : m?.shipped_at && m.cut_receipt_id ? `Shipped ${blackboardTargetDate(m.shipped_at)} · ${cards.length} cards` : m?.evidence_incomplete || m?.stage === 'Final' || m?.stage === 'Finalizing' ? 'Released on board; evidence incomplete' : `${m?.target ? 'Cut ' + blackboardTargetDate(m.target) + ' · ' : ''}${cards.length} cards`}</span></header>
  {project && r.vision && <p className="blackboard-project-vision">{r.vision}</p>}
  {!small && !project && !t && m && (countsPending || !!progress.length) && <Hover target={{kind:'run',title:m.name,facts:{}}} content={<><strong>{m.name}</strong><p>{countsPending ? 'Loading card details…' : stages.map(stage => `${count(progress,stage)} ${labels[stage].toLowerCase()}`).join(' · ')}</p></>}><div className="blackboard-summary" data-testid="blackboard-summary" tabIndex={0}>{countsPending ? <><span className="initial-skeleton-value blackboard-skeleton-bar" aria-hidden="true" /><BlackboardCountSkeleton /></> : <><Bar cards={progress} /><small>{count(progress, 'shipped')} of {progress.length} shipped</small></>}</div></Hover>}
  {!small && (!project && !t && m ? <BlackboardChart snapshots={(r.snapshots ?? []).filter(s => s.milestone_id === m.id)} events={(r.events ?? []).filter(e => e.milestone_id === m.id)} target={m.target} style={r.style} frozen={!!m.frozen_at} /> : loadingCards ? <span className="initial-skeleton-value blackboard-skeleton-bar" aria-hidden="true" /> : <Bar cards={cards} />)}
  {small ? null : project ? <div className="blackboard-filters" role="group" aria-label="Project filters">{[['all', 'All'], ['shipped', 'Shipped'], ...milestones.filter(m => !m.frozen_at).map(m => [m.id, m.name]), ['icebox', 'Icebox']].map(([id, name]) => <Hover key={id} target={{kind:'run',title:name,facts:{}}} content={<BlackboardFilterPreview id={id} record={r} slices={data.slices} />}><button className="blackboard-pill" aria-pressed={projectFilter === id} onClick={() => setProjectFilter(id)}><i style={{background:stageColor(id === 'all' ? 'next' : id === 'shipped' ? 'done' : 'in_work')}} />{name} <b>{id === 'icebox' ? null : id === 'all' ? `${milestones.length} ${milestones.length === 1 ? 'milestone' : 'milestones'}` : id === 'shipped' ? `${milestones.filter(m => m.shipped_at && m.cut_receipt_id).length} ${milestones.filter(m => m.shipped_at && m.cut_receipt_id).length === 1 ? 'release' : 'releases'}` : `${thrusts.filter(t => t.milestone_id === id).length} ${thrusts.filter(t => t.milestone_id === id).length === 1 ? 'thrust' : 'thrusts'}`}</b></button></Hover>)}</div> : !preRecord && <div className="blackboard-filters" role="group" aria-label="Card filters">{cardFilters.map(s => <button key={s} className="blackboard-pill" aria-pressed={selectedFilter === s} onClick={() => setFilter(s)}><i style={{ background: stageColor(s) }} />{labels[s]} {countsPending ? <BlackboardCountSkeleton /> : <b>{count(cards, s)}</b>}</button>)}{t && <label>Sort <select className="select" value={sort} onChange={e => setSort(e.target.value)}><option value="state">State</option><option value="number">Card number</option><option value="title">Title</option></select></label>}</div>}
  {!!data.pending_proposals && <p className="blackboard-context blackboard-proposal">{pending ? <>{pending.actor?.kind === 'agent' && pending.actor.id === sessionId ? 'Main proposes ' : ''}{pending.changes.length} plan {pending.changes.length === 1 ? 'change' : 'changes'}{pending.actor?.kind === 'agent' && pending.actor.id === sessionId ? '.' : ' proposed.'} <button className="blackboard-review" onClick={() => setProposal(pending)}>Review</button></> : <>{data.pending_proposals === 1 ? 'A plan proposal awaits a decision.' : `${data.pending_proposals} plan proposals await a decision.`}</>}</p>}
  {loadingCards && <p className="initial-loader-label" role="status">Loading card details…</p>}
  {!loadingCards && !project && m && !data.slices[m.id]?.complete && <p role="status">Some board data is unavailable. Counts may be incomplete.</p>}
  {project && (small || !!plainItems.length) && <BlackboardPlain record={r} items={plainItems} />}
  {t ? <div className="blackboard-grid">{shown.map(c => <BoardCard key={`${c.card.board_id}:${c.card.number}`} card={c.card} onClick={() => setTicket(c)}><span className="chip">{labels[c.stage] ?? 'Unavailable'}</span>{c.membership.state === 'unplanned' && <span className="chip">Unplanned</span>}{c.drift && <span className="chip">Changed since release</span>}{c.deleted && <span className="chip">Deleted</span>}</BoardCard>)}{!loadingCards && !shown.length && <p>No cards match this filter.</p>}</div> : project ? (!small && <BlackboardProject record={r} slices={data.slices} filter={projectFilter} tile={tile} navigate={go} onFilter={setProjectFilter} />) : <div className="blackboard-committed-groups">{[...(r.goals ?? []), { id: '', name: 'Everything else' }].map(g => { const list = g.id ? thrusts.filter(t => t.milestone_id === m?.id && t.goal_id === g.id) : cards.some(c => !c.membership.thrust_id) ? [{ id: 'everything-else', name: 'Everything else' }] : []; return !!list.length && <section key={g.id}><div className="blackboard-grid">{list.map(t => { const entries = cards.filter(c => c.membership.thrust_id === t.id || t.id === 'everything-else' && !c.membership.thrust_id); return (selectedFilter === 'all' || entries.some(c => matches(c, selectedFilter))) && tile(t.name, entries, { level: 'thrust', milestoneId: m?.id, thrustId: t.id }) })}</div></section> })}</div>}
  {!project && !t && !preRecord && m?.shipped_at && m.cut_receipt_id && <><p className="blackboard-context">{m.target ? m.shipped_at.slice(0,10) <= m.target.slice(0,10) ? 'On time' : 'After the target date' : 'No target date recorded'}</p><section><h2>Decisions</h2>{!(r.events ?? []).some(e => e.milestone_id === m.id && ['decision','date_moved'].includes(e.kind)) && <p>No decisions recorded.</p>}{(r.events ?? []).filter(e => e.milestone_id === m.id && ['decision','date_moved'].includes(e.kind)).slice(-12).map(e => <p key={e.id}>{blackboardTargetDate(e.recorded_at)} · {blackboardEventPayload(e.payload).reason ?? 'No reason recorded'}{e.source === 'agent' ? ' · Reported by an agent' : ''}</p>)}</section><section><h2>Moved out</h2>{!cards.some(c => c.membership.state === 'moved_out') && <p>{preRecord ? 'Moves were not recorded.' : 'No cards moved out.'}</p>}{cards.filter(c => c.membership.state === 'moved_out').map(c => <p key={c.card.number}>{c.card.reference ?? '#' + c.card.number} · {c.membership.reason || 'Reason not recorded'}{c.membership.destination ? ' → ' + c.membership.destination : ''}</p>)}</section></>}
  {proposal && <BlackboardProposal proposal={proposal} milestoneName={proposal.milestone_id ? milestones.find(m => m.id === proposal.milestone_id)?.name ?? 'Milestone' : 'Project'} title={(board, number) => Object.values(data.slices).flatMap(s => s.cards ?? []).find(c => c.card.board_id === board && c.card.number === number)?.card.title} onClose={() => setProposal(null)} />}
  {ticket && (ticket.deleted ? <TicketModal ticket={{source:'blerg-board',key:ticket.card.reference || `#${ticket.card.number}`,title:ticket.card.title,status:labels[ticket.stage]}} onClose={() => setTicket(null)} body={<p>This card was deleted from the board. Showing the title preserved for {m?.name ?? 'this milestone'}.</p>} /> : <PinnedTicketModal canMove={false} tabScoped ticket={{ source: 'blerg-board', board_id: ticket.card.board_id, key: `#${ticket.card.number}`, title: ticket.card.title }} sessionId={sessionId} onClose={() => setTicket(null)} />)}
 </section>
}

export function BlackboardChart({ snapshots, events, target, frozen, style }: { style?: ReleaseStyle; snapshots: Snapshot[]; events: PlanEvent[]; target: string | null; frozen: boolean }) {
 const chart = useRef<HTMLElement>(null)
 const clip = useId()
 const [width, setWidth] = useState(600)
 useEffect(() => { const element = chart.current; if (!element) return; const observer = new ResizeObserver(([entry]) => setWidth(Math.max(240, entry.contentRect.width))); observer.observe(element); return () => observer.disconnect() }, [snapshots.length])
 const recentEvents = events
 const dates = [...snapshots.map(s => s.day), ...events.map(e => (e.claimed_at ?? e.recorded_at).slice(0, 10))].sort()
 if (frozen && !snapshots.length) return <p className="blackboard-context">This release predates the record. No daily history is available.</p>
 if (new Set(snapshots.filter(s => Object.values(s.counts).some(n => n > 0)).map(s => s.day)).size < 2) return null
 const today = new Date().toISOString().slice(0, 10)
 const latest = frozen ? snapshots.map(s => s.day).sort().at(-1)! : today
 const start = Date.parse(dates[0]), end = Math.max(Date.parse(dates.at(-1)!), target ? Date.parse(target.slice(0, 10)) : 0, frozen ? 0 : Date.parse(today))
 const days = Math.max(1, Math.round((end - start) / 86400000) + 1), left = 36, plot = width - left - 12, step = plot / days, barWidth = Math.min(48, step * .56)
 const max = Math.max(1, ...snapshots.map(s => stages.reduce((n, k) => n + (s.counts[k] ?? 0), 0)))
 const x = (day: string) => left + ((Date.parse(day) - start) / 86400000 + .5) * step
 const ticks = blackboardAxisTicks(start,end,width).filter(d => d === latest || Math.abs(x(d)-x(latest)) >= 65)
 if (Date.parse(latest) >= start && Date.parse(latest) <= end && !ticks.includes(latest)) ticks.push(latest)
 const eventDays = Array.from(new Set(recentEvents.map(e => (e.claimed_at ?? e.recorded_at).slice(0,10))))
 return <figure ref={chart} className="blackboard-chart"><svg viewBox={`0 0 ${width} 120`} role="img" aria-label="Daily cards by stage and events on one time axis"><title>Daily cards and release events</title>{blackboardCountTicks(max).map(value => <g key={value}><line x1={left} x2={width - 12} y1={80 - 36 * value / max} y2={80 - 36 * value / max} className="blackboard-gridline" /><text x={left - 6} y={84 - 36 * value / max} textAnchor="end">{value}</text></g>)}{!frozen && <line x1={x(today)+step/2} x2={x(today)+step/2} y1={4} y2={80} className="blackboard-today" />}{snapshots.map((s,i) => { let y = 80; const total = stages.reduce((n, k) => n + (s.counts[k] ?? 0), 0); return <g key={s.day} className="blackboard-day"><title>{blackboardTargetDate(s.day)}: {total} cards{!s.complete ? ', incomplete observation' : ''}</title><defs><clipPath id={clip+i}><rect x={x(s.day)-barWidth/2} y={80-total/max*36} width={barWidth} height={total/max*36} rx={3} /></clipPath></defs><g clipPath={`url(#${clip+i})`}>{stages.map(k => { const h = (s.counts[k] ?? 0) / max * 36; y -= h; return <rect key={k} x={x(s.day) - barWidth/2} y={y} width={barWidth} height={h} fill={stageColor(k)} opacity={s.complete ? 1 : .45} /> })}</g><text className="blackboard-total" x={x(s.day)} y={y - 8} textAnchor="middle">{total}</text></g> })}{ticks.sort().map(d => <text className={d === latest ? 'blackboard-latest' : undefined} key={d} x={x(d)} y={108} textAnchor="middle">{blackboardTargetDate(d)}</text>)}{eventDays.map(day=><BlackboardEventMarker key={day} events={recentEvents.filter(e=>(e.claimed_at??e.recorded_at).slice(0,10)===day)} style={style} x={x(day)} width={Math.min(160,Math.max(72,step),...eventDays.filter(d=>d!==day).map(d=>Math.abs(x(d)-x(day))))} today={today} />)}</svg>{snapshots.some(s => !s.complete) && <figcaption>Faint bars are incomplete observations.</figcaption>}</figure>
}

function BlackboardPlain({ record: r, items }: { record: RecordView; items: PlainEntry[] }) {
 const goals = r.goals ?? []
 const goalIds = new Set(goals.map(g => g.id))
 const groups = goals.length > 1 ? [...goals, { id: '', name: 'No goal' }] : [{ id: '', name: '' }]
 return <>{groups.map(g => {
  const list = goals.length > 1 ? g.id ? items.filter(i => i.membership.goal_id === g.id) : items.filter(i => !i.membership.goal_id || !goalIds.has(i.membership.goal_id)) : items
  return <section key={g.id || 'all'}>{g.name && <h2>{g.name}</h2>}<ul className="blackboard-plain-list">{list.map(item => <li key={item.card.id}>{item.stage === 'done' ? '✓ ' : ''}{item.card.title}</li>)}{!list.length && <li className="blackboard-context">Nothing planned yet.</li>}</ul></section>
 })}</>
}
function BlackboardProject({ record:r, slices, filter, tile, navigate, onFilter }: {record:RecordView; slices:Data['slices']; filter:string; tile:(name:string,entries:Entry[],path:BlackboardPath,noTitles?:boolean)=>React.ReactNode; navigate:(path:BlackboardPath,label:string)=>void; onFilter:(filter:string)=>void}) {
 const thrusts = (r.thrusts ?? []).filter(t => filter === 'all' ? !(r.milestones ?? []).find(m => m.id === t.milestone_id)?.frozen_at : t.milestone_id === filter)
 const history = <BlackboardProjectHistory record={r} slices={slices} filter={filter} tile={tile} navigate={navigate} onFilter={onFilter} />
 if (filter === 'shipped' || filter === 'icebox' || !thrusts.length) return history
 return <>
  <div className="blackboard-project-goals">{thrusts.map(t => tile(t.name, (slices[t.milestone_id]?.cards ?? []).filter(c => c.membership.thrust_id === t.id), {level:'thrust',milestoneId:t.milestone_id,thrustId:t.id}))}</div>
  {filter === 'all' && <details className="blackboard-history"><summary>Milestone history</summary>{history}</details>}
 </>
}

function BlackboardProjectHistory({ record:r, slices, filter, tile, navigate, onFilter }: {record:RecordView; slices:Data['slices']; filter:string; tile:(name:string,entries:Entry[],path:BlackboardPath,noTitles?:boolean)=>React.ReactNode; navigate:(path:BlackboardPath,label:string)=>void; onFilter:(filter:string)=>void}) {
 const milestones = r.milestones ?? [], thrusts = r.thrusts ?? []
 const goals = r.goals ?? []
 const shipped = milestones.filter(m => m.shipped_at && m.cut_receipt_id).sort((a,b) => b.shipped_at!.localeCompare(a.shipped_at!))
 const upcoming = milestones.filter(m => !(m.shipped_at && m.cut_receipt_id)).sort((a,b) => (a.target ?? '9999').localeCompare(b.target ?? '9999'))
 if (filter === 'icebox') return <p className="blackboard-context">Icebox counts are not available in this record.</p>
 if (filter === 'shipped') {
  const groups = Array.from(new Set(shipped.map(m => m.name.match(/^v?(\d+\.\d+)\./)?.[1] ?? 'Past releases'))).sort().reverse()
  return shipped.length ? <div className="blackboard-project-goals">{groups.map(group => <section key={group}><h2>{group === 'Past releases' ? group : group + '.x'}</h2><div className="blackboard-project-path">{shipped.filter(m => (m.name.match(/^v?(\d+\.\d+)\./)?.[1] ?? 'Past releases') === group).map(m => tile(m.name, slices[m.id]?.cards ?? [], {level:'milestone',milestoneId:m.id}))}</div></section>)}</div> : <p className="blackboard-context">No shipped milestones recorded yet.</p>
 }
 const summary = (m: Milestone, pushes: Thrust[], goalId?: string) => {
  const entries = (slices[m.id]?.cards ?? []).filter(c => !goalId || pushes.some(t => t.id === c.membership.thrust_id))
  const built = count(entries, 'done') + count(entries, 'to_test')
  const partial = slices[m.id]?.complete === false
  return <Hover key={m.id} target={{kind:'run',title:m.name,facts:{}}} content={<><strong>{m.name}</strong>{pushes.slice(0,8).map(t => <p key={t.id}>{t.name}</p>)}{pushes.length > 8 && <p>{pushes.length - 8} more thrusts</p>}<p>{entries.length ? stages.map(s => `${count(entries,s)} ${labels[s].toLowerCase()}`).join(' · ') : m.frozen_at ? 'No card history recorded.' : 'No cards assigned yet.'}</p></>}>
   <button type="button" className="blackboard-tile blackboard-project-summary" onClick={() => navigate({level:'milestone',milestoneId:m.id},m.name)}>
    <span>{m.name}<small>{m.shipped_at && m.cut_receipt_id ? 'Shipped' : m.evidence_incomplete ? 'Released on board; evidence incomplete' : entries.length ? `${Math.round(100 * built / entries.length)}% built` : 'Planned'}</small></span>
    {entries.length > 0 && <Bar cards={entries} />}
    <p>{pushes.length} {pushes.length === 1 ? 'thrust' : 'thrusts'}{m.shipped_at && m.cut_receipt_id ? ` · shipped ${blackboardTargetDate(m.shipped_at)}` : m.evidence_incomplete ? ` · evidence incomplete` : m.target ? ` · target ${blackboardTargetDate(m.target)}` : ' · no target date'}</p>
    <p>{entries.length ? `${count(entries,'done')} done · ${count(entries,'to_test')} to test · ${entries.length} cards` : m.frozen_at ? 'No card history recorded.' : 'No cards assigned yet.'}{partial ? ' Counts may be incomplete.' : ''}</p>
   </button>
  </Hover>
 }
 const ungrouped = milestones.filter(m => !thrusts.some(t => t.milestone_id === m.id) && (filter === 'all' || filter === m.id))
 return <>
  {filter === 'all' && <p className="blackboard-context">Goals across milestones · {shipped.length ? `${shipped.length} shipped ${shipped.length === 1 ? 'release' : 'releases'}` : 'No shipped milestones recorded yet'} · Icebox counts unavailable</p>}
  <div className="blackboard-project-goals">{goals.map(g => {
   const pushes = thrusts.filter(t => t.goal_id === g.id && (filter === 'all' || t.milestone_id === filter))
   const past = shipped.filter(m => pushes.some(t => t.milestone_id === m.id))
   const empty = (name: string, message = 'Not planned') => <div className="blackboard-project-empty"><span>{name}</span><p>{message}</p></div>
   const milestoneCell = (m: Milestone | undefined, fallback: string) => {
    const work = pushes.filter(t => t.milestone_id === m?.id)
    return m && work.length ? summary(m, work, g.id) : empty(m?.name ?? fallback)
   }
   return <section key={g.id}><h2>{g.name}</h2><div className="blackboard-project-path">
    {filter === 'all' ? <>
     <div className="blackboard-project-cell">{past.length > 0
      ? <button type="button" className="blackboard-tile blackboard-project-summary" onClick={() => onFilter('shipped')}><span>Shipped<small>{past.length} {past.length === 1 ? 'release' : 'releases'}</small></span><p>{past.map(m => m.name).slice(0,3).join(' · ')}{past.length > 3 ? ` · ${past.length - 3} more` : ''}</p></button>
      : empty('Shipped', shipped.length ? 'No shipped work recorded' : 'No shipped milestones yet')}</div>
     <div className="blackboard-project-cell">{milestoneCell(upcoming[0], 'Current milestone')}</div>
     <div className="blackboard-project-cell">{milestoneCell(upcoming[1], 'Next milestone')}</div>
     <div className="blackboard-project-cell">{upcoming.slice(2).some(m => pushes.some(t => t.milestone_id === m.id))
      ? <><span className="blackboard-context">Later</span>{upcoming.slice(2).filter(m => pushes.some(t => t.milestone_id === m.id)).map(m => milestoneCell(m, m.name))}</>
      : empty('Later')}</div>
    </> : pushes.map(t => tile(t.name,(slices[t.milestone_id]?.cards ?? []).filter(c => c.membership.thrust_id === t.id),{level:'thrust',milestoneId:t.milestone_id,thrustId:t.id}))}
   </div></section>
  })}</div>
  {ungrouped.length > 0 && <section><h2>Milestones without recorded goals</h2><div className="blackboard-project-history">{ungrouped.map(m => summary(m, []))}</div></section>}
 </>
}

function BlackboardFilterPreview({ id, record:r, slices }: {id:string;record:RecordView;slices:Data['slices']}) {
 const milestones=r.milestones ?? []
 if(id==='icebox')return <p>Icebox counts are not available in this record.</p>
 if(id==='shipped') {
  const releases=milestones.filter(m=>m.shipped_at && m.cut_receipt_id)
  const knownTargets=releases.filter(m=>m.target)
  const onTime=knownTargets.filter(m=>m.shipped_at!.slice(0,10)<=m.target!.slice(0,10)).length
  const dates=releases.map(m=>Date.parse(m.shipped_at!)).sort((a,b)=>a-b)
  const gaps=dates.slice(1).map((at,i)=>(at-dates[i])/86400000).sort((a,b)=>a-b)
  const middle=Math.floor(gaps.length/2)
  const typical=gaps.length%2?gaps[middle]:(gaps[middle-1]+gaps[middle])/2
  return <><p>{releases.length} {releases.length===1?'release':'releases'}</p>{!!knownTargets.length && <p>{onTime} on time · {knownTargets.length-onTime} late</p>}{knownTargets.length<releases.length && <p>Target dates were not recorded for {releases.length-knownTargets.length} {releases.length-knownTargets.length===1?'release':'releases'}.</p>}{!!gaps.length && <p>Typical gap: {Math.round(typical)} {Math.round(typical)===1?'day':'days'}</p>}</>
 }
 const thrusts=(r.thrusts ?? []).filter(t=>t.milestone_id===id)
 if(id==='all')return <p>{milestones.length} milestones · {(r.goals ?? []).length} goals</p>
 return <>{thrusts.slice(0,8).map(t=>{const cards=(slices[id]?.cards ?? []).filter(c=>c.membership.thrust_id===t.id);return <p key={t.id}>{t.name} · {count(cards,'done')} of {cards.length}<Bar cards={cards} /></p>})}{thrusts.length>8 && <p>{thrusts.length-8} more thrusts</p>}{!thrusts.length && <p>No thrusts recorded.</p>}</>
}

/** Read-only setup preview. Decisions stay with Main's existing question. */
function BlackboardEmptyState({ sessionId, onCreated }: { sessionId: string; onCreated(blackboardId: string, path: BlackboardPath, label: string): void }) {
 const [attempt, setAttempt] = useState(0)
 const [refreshFailed, setRefreshFailed] = useState(false)
 const draftFailure = useCallback(() => setRefreshFailed(true), [])
 const draft = useBlackboardDraft(sessionId, attempt, draftFailure)
 const records = useBlackboardRecords(sessionId)
 useEffect(() => {
  if (!records.length) return
  const view = blackboardOpenView(records)
  if (view.kind === 'blackboard') onCreated(view.blackboardId, view.path, view.label)
 }, [records, onCreated])
 return <>
 {refreshFailed && <p role="alert">Couldn’t refresh the blackboard. <IconButton label="Retry blackboard" onClick={() => { setRefreshFailed(false); setAttempt(n => n + 1) }} /></p>}
 {draft ? <BlackboardDraftScreen sessionId={sessionId} plan={draft} /> : <section className="blackboard blackboard-empty" aria-label="Blackboard"><div className="blackboard-empty-intro"><h1>Give your next idea a clear path</h1><p>Discuss your vision and goals with Main. Main asks you in chat or Needs you before creating the blackboard.</p></div></section>}
 </>
}

/** The pending draft `blerg-runner plan draft` stored for this workspace: shown instead of the
 *  empty state until the person answers Main’s bound question (BLG-450 phase 2 round 4). Every
 *  piece of the draft's own content (name, vision, goals, milestone name, thrust names and
 *  reasons) is agent-derived and unreviewed — marked as a draft throughout, plain text only
 *  (never rendered as markdown or HTML), the same "untrusted_data" convention the rest of this
 *  codebase already uses for agent-proposed, not-yet-approved content. */
function BlackboardDraftScreen({ sessionId, plan }: { sessionId: string; plan: DraftPlan }) {
 const [ticket, setTicket] = useState<CardTarget | null>(null)
 const refs = useChecklistRefs(sessionId, (plan.thrusts ?? []).map(t => t.reason), plan.board_id ? { id: plan.board_id } : null, true, setTicket)
 const goals = plan.goals ?? []
 const thrusts = plan.thrusts ?? []
 const groups = [
  ...goals.map((g, i) => ({ key: String(i), name: g, list: thrusts.filter(t => t.goal_index === i) })),
  { key: 'other', name: 'Everything else', list: thrusts.filter(t => t.goal_index == null || t.goal_index < 0 || t.goal_index >= goals.length) },
 ].filter(g => g.key !== 'other' || g.list.length)
 return (
  <section className="blackboard blackboard-draft" aria-label="Blackboard">
   <p className="blackboard-draft-badge" role="status">Draft from Main, not yet created. Decide in chat or Needs you.</p>
   <header className="blackboard-heading">
    <h1>{plan.name}</h1>
   </header>
   {plan.vision && <p className="blackboard-draft-vision">{plan.vision}</p>}
   {plan.milestone?.name && <p className="blackboard-draft-milestone"><strong>{plan.milestone.name}</strong>{plan.milestone.target && <> · target {blackboardTargetDate(plan.milestone.target, 'en-US')}</>}</p>}
   <ChecklistRefsContext.Provider value={refs}>
    <div className="blackboard-draft-groups">{groups.map(g => {
     const colon = g.name.indexOf(':')
     return <section key={g.key}><h2>{colon > 0 ? <><strong>{g.name.slice(0, colon)}</strong><span>{g.name.slice(colon)}</span></> : g.name}</h2>
      {g.list.map((t, i) => <div key={i} className="blackboard-draft-thrust"><h3>{t.name}</h3><p><CardRefText text={t.reason} /></p></div>)}
     </section>
    })}</div>
   </ChecklistRefsContext.Provider>
   {ticket?.boardId && <PinnedTicketModal canMove={false} tabScoped ticket={{ source: 'blerg-board', board_id: ticket.boardId, key: ticket.key, title: ticket.title ?? ticket.key }} sessionId={sessionId} onClose={() => setTicket(null)} />}
  </section>
 )
}

function BlackboardEventMarker({events,style,x,width,today}:{events:PlanEvent[];style?:ReleaseStyle;x:number;width:number;today:string}) {
 const day=(events[0].claimed_at??events[0].recorded_at).slice(0,10),label=Array.from(new Set(events.map(e=>blackboardEventLabel(e,style)))).map(label => { const n=events.filter(e=>blackboardEventLabel(e,style)===label).length; return n>1 ? `${label} ×${n}` : label }).join(' · ')
 return <g role="img" aria-label={blackboardTargetDate(day)+': '+label}><title>{blackboardTargetDate(day)}: {label}</title><foreignObject x={x-width/2+1} y={12} width={Math.max(1,width-2)} height={20}><Hover target={{kind:'run',title:label,facts:{}}} content={<>{events.map(e=>{const payload=blackboardEventPayload(e.payload);const source=e.source==='agent'?'Reported by an agent':e.source==='import'?'Imported evidence':e.source==='board'?'Observed on the board':e.source==='person'?'Recorded by a person':'Recorded event';return <div key={e.id}><strong>{blackboardEventLabel(e,style)}</strong><p>Recorded {blackboardTargetDate(e.recorded_at)}</p>{e.claimed_at && <p>Reported date {blackboardTargetDate(e.claimed_at)}</p>}{payload.reason && <p>{payload.reason}</p>}{payload.gate_result && <p>{payload.gate_result}</p>}<p>{source}</p></div>})}</>}><span className="blackboard-chart-event" style={{fontSize:width<65?10:11}} tabIndex={0}>{width>65 && <i className={day>today?'planned':undefined} />}{label}</span></Hover></foreignObject></g>
}
