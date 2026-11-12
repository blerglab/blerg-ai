import { boardPageCacheKey, cachedBoardPage, rememberBoardPage, type BoardPage } from '../lib/boardPageCache'
import { BoardSkeleton } from '../../../../packages/board/BoardSkeleton'
import './DesktopEnrollNotice.css'
import { useBoardViewState } from '../../../../packages/board/viewState'
import { useColumnFocus } from '../../../../packages/board/columnFocus'
import { BoardColumn, BoardCard } from '../../../../packages/board/BoardPresentation'
import { FilterMenu, ActiveFilters } from '../../../../packages/board/FilterMenu'
import { chipsFor, emptyFilter, matchesFilter } from '../../../../packages/board/boardFilter'
import { HiddenColumnsNotice, useColumnVisibility } from '../../../../packages/board/columnVisibility'
import { Hover, IconButton, scrollToCall } from '@blerglab/chat'
import { checklistAssignment } from '@blerglab/chat'
import type { SessionLive } from '../hooks/useSessionLive'
import { workTicket } from '../lib/pins'
import { boardCardRef } from '../lib/liveBoardCard'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import type { SessionTabs } from '../hooks/useSessionTabs'
import { OPEN_SESSION_BOARD } from '../lib/openSessionBoard'
import { moveLiveBoardCard, signedInAccount, type LiveBoardCard } from '../lib/liveBoardCard'
import { boardViewRequest } from '../lib/boardViewRequest'
import { readPermissions } from '../lib/sessionPermissions'
import { useBoardOrigin } from '../lib/boardLink'
import { boardPageUrl, useSessionBoardLinks, publishSessionPermissions, boardLinksTicket, refreshSessionBoardNames } from '../lib/sessionBoardLinks'
import WorkerKindIcon from './WorkerKindIcon'
import { SessionStateGlyph } from './SessionCard'
import { useBoardWorkerKinds, type BoardWorkerKinds } from '../hooks/useBoardWorkerKinds'
import { runTarget } from '../lib/runFacts'
import { PinnedTicketModal, PinnedTicketSideView } from './BoardCardView'
import './SessionBoardTabs.css'


function boardWorkers(live: SessionLive, tabs: SessionTabs, workerKinds: BoardWorkerKinds, boardId: string) {
 const workers = new Map<number, { id: string; title: string; status: string; kind: 'card' | 'role' | 'review' | 'research'; open: () => void }[]>()
 const addWorker = (ticket: Parameters<typeof boardCardRef>[0] | null, worker: { id: string; title: string; status: string; kind: 'card' | 'role' | 'review' | 'research'; open: () => void }) => {
  const ref = ticket && boardCardRef(ticket)
  if (ref?.board === boardId) workers.set(ref.number, [...(workers.get(ref.number) ?? []), worker])
 }
 for (const child of live.children ?? []) {
  const item = checklistAssignment(live.live.checklist?.items ?? [], 'child_session', child.id)
  addWorker(workTicket(item ?? null, live.live.tickets ?? [], child.ticket), { id: child.id, title: child.title, status: child.asking ? 'waiting' : child.status, kind: workerKinds[child.id] ?? 'card', open: () => tabs.open({ kind: 'session-chat', sessionId: child.id, label: child.title }, true) })
 }
 for (const agent of live.live.subagents) {
  const item = checklistAssignment(live.live.checklist?.items ?? [], 'subagent', agent.childId || agent.key)
  addWorker(workTicket(item ?? null, live.live.tickets ?? []), { id: agent.key, title: agent.title, status: agent.background ? 'background' : 'running', kind: 'card', open: () => { tabs.select('chat'); scrollToCall(agent.key) } })
 }
 return (number: number) => workers.get(number)?.map(worker => <Hover key={worker.id} target={runTarget({ title: worker.title, kind: `${worker.kind} worker`, state: worker.status })}><button type="button" className="icon-btn session-board-worker" aria-label={`${worker.title}, ${worker.status}`} onClick={worker.open}><WorkerKindIcon kind={worker.kind} /><SessionStateGlyph status={worker.status} label={worker.status} /></button></Hover>)
}

export default function SessionBoardTabs({ tabs, sessionId, live, refreshKey }: { tabs: SessionTabs; sessionId: string; live: SessionLive; refreshKey?: string }) {
 const workerKinds = useBoardWorkerKinds(live.children)
 return <>{tabs.tabs.map(tab => tab.view.kind === 'ticket' && createPortal(<PinnedTicketSideView worker={boardWorkers(live, tabs, workerKinds, tab.view.ticket.board_id ?? '')(Number(tab.view.ticket.key.replace(/^#/, '')))} ticket={tab.view.ticket} sessionId={tab.view.sessionId} tabScoped onClose={() => tabs.close(tab.id, 'tab')} />, tabs.targetFor(tab.id), tab.id))}<SessionBoardHost tabs={tabs} sessionId={sessionId} refreshKey={refreshKey} />{tabs.tabs.map(tab => tab.view.kind === 'board' && createPortal(
  <BoardTab tabs={tabs} scope={tabs.scope + tab.id} keyboardActive={tabs.active === undefined || tabs.active === tab.id} onFocusName={name => tabs.setBoardFocus?.(tab.id, name)} boardId={tab.view.boardId} label={tab.view.label} sessionId={tab.view.sessionId ?? sessionId} live={live} active={tabs.visible.includes(tab.id)} />,
  tabs.targetFor(tab.id), tab.id))}</>
}

export function SessionBoardHost({ tabs, sessionId, refreshKey }: { tabs: SessionTabs; sessionId: string; refreshKey?: string }) {
 const { open, setBoardLabel } = tabs
 const links = useSessionBoardLinks(sessionId, refreshKey)
 useEffect(() => { for (const link of links) setBoardLabel(link.id, link.name) }, [links, setBoardLabel])
 useEffect(() => {
  const receive = (event: Event) => {
   const e = event as CustomEvent
   if (e.detail.session !== sessionId || e.detail.account !== signedInAccount()) return
   if (e.detail.probe) { e.preventDefault(); return }
   const link = links.find(link => link.id === e.detail.boardId)
   if (!link?.readable) return
   e.preventDefault()
   if (e.detail.ticket) { open({ kind: 'ticket', ticket: { ...e.detail.ticket, key: `#${boardCardRef(e.detail.ticket)?.number}` }, sessionId, label: e.detail.ticket.key }, true); return }
   open({ kind: 'board', boardId: link.id, label: e.detail.label || link.name, sessionId })
  }
  window.addEventListener(OPEN_SESSION_BOARD, receive)
  return () => window.removeEventListener(OPEN_SESSION_BOARD, receive)
 }, [sessionId, open, links])
 return null
}

function compareRank(a: { rank?: string }, b: { rank?: string }): number {
 const x = a.rank ?? '', y = b.rank ?? ''
 return x < y ? -1 : x > y ? 1 : 0
}

async function boardResponseError(response: Response, fallback: string) {
 try { const body = await response.json(); if (typeof body.error === 'string' && body.error) return new Error(body.error) } catch { /* Use status wording. */ }
 return new Error(response.status === 403 || response.status === 404 ? 'This workspace cannot read this board.' : fallback)
}

/** BLG-431: a board with hundreds of cards (sorted by rank, so "first" means the top of each
 *  column) used to mount every BoardCard in one commit on first load - the whole point of
 *  windowing. Reveals REVEAL_BATCH more each animation frame until every card is mounted, so
 *  first paint only pays for the first batch. Only applies to a genuinely cold load (`cold`,
 *  captured once at mount): a cached revisit already has every card in memory with nothing to
 *  hide a wait behind, and a later background refresh must not re-hide already-visible cards and
 *  reveal them again - both would be a visible regression, not a win. */
const REVEAL_BATCH = 60
function useProgressiveReveal(total: number, cold: boolean): number {
 const windowing = useRef(cold)
 const [revealed, setRevealed] = useState(() => (cold ? Math.min(REVEAL_BATCH, total) : total))
 useEffect(() => {
  if (!windowing.current) { setRevealed(total); return }
  if (revealed >= total) { windowing.current = false; return }
  const frame = requestAnimationFrame(() => setRevealed(r => Math.min(r + REVEAL_BATCH, total)))
  return () => cancelAnimationFrame(frame)
  // `cold` is captured once at mount (see windowing's own initial value); it never changes.
 }, [revealed, total])
 return revealed
}
function BoardTab({ tabs, keyboardActive, onFocusName, scope, boardId, label, sessionId, active, live }: { tabs: SessionTabs; keyboardActive: boolean; onFocusName: (name: string) => void; scope: string; boardId: string; label: string; sessionId: string; active: boolean; live: SessionLive }) {
 const origin = useBoardOrigin()
 const workerKinds = useBoardWorkerKinds(live.children)
 const workerGlyphs = boardWorkers(live, tabs, workerKinds, boardId)
 const [query, setQuery] = useBoardViewState(scope, 'query', '')
 const [facets, setFacets] = useBoardViewState(scope, 'facets', emptyFilter)
 const [release, setRelease] = useBoardViewState(scope, 'release', '')
 const [card, setCard] = useBoardViewState<LiveBoardCard | null>(scope, 'card', null)
 const cacheKey = boardPageCacheKey(signedInAccount(), sessionId, boardId)
 const [cached] = useState(() => cachedBoardPage(cacheKey, query, release))
 const [cold] = useState(() => !cached)
 const [data, setData] = useState<BoardPage | null>(cached?.data ?? null)
 // Rank order, the same order the board itself sorts and shows cards in: "first" means the top
 // of each column, not an arbitrary subset.
 const cardOrder = useMemo(() => new Map((data?.cards ?? []).map((c, i) => [c.id, i])), [data?.cards])
 const revealed = useProgressiveReveal(data?.cards.length ?? 0, cold)
 const visibility = useColumnVisibility(signedInAccount(), boardId, data?.columns ?? [], scope)
 const focus = useColumnFocus(scope, keyboardActive && !card, data?.columns.filter(c => !visibility.hidden.includes(c.id)).map(c => c.id))
 const metadata = useRef<{ columns: BoardPage['columns']; releases: BoardPage['releases']; at: number } | null>(cached ? { columns: cached.data.columns, releases: cached.data.releases, at: cached.loadedAt } : null)
 const [filters, setFilters] = useState({ query, tag: '' })
 const [error, setError] = useState('')
 const [loadedAt, setLoadedAt] = useState<Date | null>(cached ? new Date(cached.loadedAt) : null)
 const [refreshFailed, setRefreshFailed] = useState(false)
 const [moveError, setMoveError] = useState('')
 const [attempt, setAttempt] = useState(0)
 const [canMove, setCanMove] = useState(false)
 const [busy, setBusy] = useState(false)
 useEffect(() => { if (data) onFocusName(data.columns.find(c => c.id === focus.column)?.name ?? '') }, [data, focus.column, onFocusName])
 const cardButton = useRef<HTMLButtonElement | null>(null)
 const closeCard = useCallback(() => { setCard(null); setAttempt(a => a + 1); cardButton.current?.focus() }, [setCard])
 useEffect(() => {
  if (!keyboardActive || !card) return
  const escape = (e: KeyboardEvent) => { if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); closeCard() } }
  document.addEventListener('keydown', escape)
  return () => document.removeEventListener('keydown', escape)
 }, [keyboardActive, card, closeCard])
 useEffect(() => {
  const timer = setTimeout(() => setFilters(before => before.query === query && before.tag === '' ? before : { query, tag: '' }), 300)
  return () => clearTimeout(timer)
 }, [query])
 useEffect(() => {
  if (!active) return
  let live = true

  let running = false
  let failures = 0
  let denied = false
  let timer: ReturnType<typeof setTimeout> | undefined
  const load = async () => {
   if (running || document.visibilityState === 'hidden') return
   running = true
   denied = false
   try {
    const ticket = boardLinksTicket()
    const permissions = await boardViewRequest(`/api/sessions/${encodeURIComponent(sessionId)}/permissions`)
    if (permissions.status === 403 || permissions.status === 404) denied = true
    if (!permissions.ok) throw await boardResponseError(permissions, 'Couldn’t refresh the board permissions.')
    const view = readPermissions(await permissions.json())
    if (!live) return
    const level = view.boards.find(b => b.board_id === boardId)?.level
    if (!level || level === 'propose') { denied = true; publishSessionPermissions(sessionId, view, ticket); throw new Error('This workspace cannot read this board.') }
    if (live) setCanMove(level === 'full')
    if (!metadata.current || Date.now() - metadata.current.at >= 60_000) await refreshSessionBoardNames()
    if (!live) return
    publishSessionPermissions(sessionId, view, ticket)
    const cards: LiveBoardCard[] = []
    let cursor = ''
    let columns = metadata.current?.columns ?? []
    let releases = metadata.current?.releases ?? []
    const refreshMetadata = !metadata.current || Date.now() - metadata.current.at >= 60_000
    let pages = 0
    const unique = new Map<string, LiveBoardCard>()
    const seen = new Set<string>()
    do {
     const params = new URLSearchParams({ q: filters.query, tag: filters.tag, release, cursor, session: sessionId, metadata: refreshMetadata && pages === 0 ? '1' : '0' })
     const res = await boardViewRequest(`/api/board-cards/${encodeURIComponent(boardId)}/view?${params}`)
     if (res.status === 403 || res.status === 404) denied = true
     if (!res.ok) throw await boardResponseError(res, 'Couldn’t refresh the board.')
     const page = await res.json() as BoardPage
     if (refreshMetadata && pages === 0) {
      columns = [...(page.columns ?? [])].sort(compareRank)
      releases = page.releases ?? []
      metadata.current = { columns, releases, at: Date.now() }
     }
     for (const card of page.cards ?? []) unique.set(card.id, card)
     pages++
     cursor = page.next_cursor ?? ''
     if (cursor && seen.has(cursor)) throw new Error('The board returned a repeated page.')
     seen.add(cursor)
    } while (cursor && pages < 10)
    cards.push(...[...unique.values()].sort((a, b) => compareRank(a, b) || a.number - b.number))
    if (cards.some(card => !columns.some(column => column.id === card.column_id))) {
     metadata.current = null
     if (!refreshMetadata) { if (live) setAttempt(a => a + 1); return }
    }
    failures = 0
    if (live) { const loaded = { columns, cards, releases }; const at = Date.now(); rememberBoardPage(cacheKey, loaded, filters.query, release, at); setData(loaded); setLoadedAt(new Date(at)); setRefreshFailed(false); setError(cursor ? 'Showing up to 1,000 cards. Narrow the filters to see more.' : '') }
   } catch (e) {
    failures++
    if (live) { if (denied) { setCard(null); setCanMove(false) } setRefreshFailed(!denied); setError(e instanceof Error ? e.message : 'The board could not be read.') }
   } finally { running = false; if (live) timer = setTimeout(() => void load(), Math.min(120_000, 15_000 * 2 ** failures)) }
  }
  void load()
  const visible = () => { if (document.visibilityState === 'visible') { clearTimeout(timer); void load() } else clearTimeout(timer) }
  document.addEventListener('visibilitychange', visible)
  return () => { live = false; clearTimeout(timer); document.removeEventListener('visibilitychange', visible) }
 }, [boardId, sessionId, active, filters, release, attempt, setCard, cacheKey])
 async function move(number: number, column: string, version: number, from: string) {
  if (!canMove || busy || from === column || !Number.isSafeInteger(version) || version < 0) return
  setMoveError('')
  setBusy(true)
  try { await moveLiveBoardCard(boardId, number, column, sessionId, version, true); setAttempt(a => a + 1) }
  catch (e) { setMoveError(e instanceof Error ? e.message : 'The card was not moved.'); metadata.current = null; setAttempt(a => a + 1) }
  finally { setBusy(false) }
 }
 return <section className="session-board" aria-label={label}>
  <div className="session-board-filter">
   <input aria-label="Filter board text" placeholder="Find cards" value={query} onChange={e => setQuery(e.target.value)} />
   <FilterMenu chips={chipsFor(data?.cards ?? [], facets)} filter={facets} onFilter={setFacets} visibility={visibility} />
   <ActiveFilters filter={facets} onFilter={setFacets} />
   <select aria-label="Filter board release" value={release} onChange={e => setRelease(e.target.value)}><option value="">All releases</option>{data?.releases?.filter(r => r.status !== 'released').map(r => <option key={r.id} value={r.id}>{r.status === 'in progress' ? `This release: ${r.name}` : r.name}</option>)}</select>
   <IconButton label="Refresh" onClick={() => { metadata.current = null; return refreshSessionBoardNames(true).then(() => setAttempt(a => a + 1)) }} />
   <a className="session-board-open" href={boardPageUrl(origin, boardId)} target="_blank" rel="noopener" aria-label="Open in Board" title="Open in Board">↗</a>
   {!focus.column && <HiddenColumnsNotice boardId={boardId} visibility={visibility} cards={data?.cards ?? []} />}
  </div>
  <p className="session-board-touch-help">Tap a card, then Status to move it.</p>
  {moveError && <div className="desktop-notice" role="status"><span className="desktop-notice-text">{moveError}</span><button type="button" className="icon-btn" aria-label="Dismiss move notice" title="Dismiss move notice" onClick={() => setMoveError('')}>×</button></div>}
  {error && <div className="desktop-notice" role={data ? 'status' : 'alert'}><span className="desktop-notice-text">{data && refreshFailed && loadedAt ? <>Couldn't refresh the board. Showing what was loaded at <time dateTime={loadedAt.toISOString()}>{loadedAt.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</time>. {error !== 'Couldn’t refresh the board.' && error}</> : error}</span><button type="button" className="icon-btn" aria-label="Dismiss board notice" title="Dismiss board notice" onClick={() => setError('')}>×</button></div>}
  <div className="session-board-content">
  <div className={`session-board-columns${focus.column ? " shared-board-focused" : ""}`} role="region" aria-label="Board columns" aria-busy={!data && !error} tabIndex={0}>
   {!data && !error && <BoardSkeleton focused={!!focus.column} />}
   {data?.columns.filter(column => !visibility.hidden.includes(column.id) && (!focus.column || focus.column === column.id)).map(column => <BoardColumn focused={focus.column === column.id} onFocusColumn={() => focus.toggle(column.id)} key={column.id} name={column.name} terminal={column.is_terminal} count={data.cards.filter(c => c.column_id === column.id && matchesFilter(c, facets)).length} aria-label={column.name}
    onDragOver={e => { if (canMove && !busy && e.dataTransfer.types.includes('application/x-blerg-board-card')) { e.preventDefault(); e.stopPropagation() } }}
    onDrop={e => { const raw = e.dataTransfer.getData('application/x-blerg-board-card'); if (!raw) return; e.preventDefault(); e.stopPropagation(); try { const dragged = JSON.parse(raw); if (dragged.board === boardId && Number.isSafeInteger(dragged.number)) void move(dragged.number, column.id, dragged.version, dragged.column) } catch { /* External drag. */ } }}>
    <div className="cards">
    {data.cards.filter(c => c.column_id === column.id && matchesFilter(c, facets) && (cardOrder.get(c.id) ?? 0) < revealed).map(c => <div key={c.id} className={`session-board-card${workerGlyphs(c.number)?.length ? " has-workers" : ""}`}><BoardCard card={c} draggable={canMove && !busy}
     onDragStart={e => { e.stopPropagation(); e.dataTransfer.effectAllowed = 'move'; e.dataTransfer.setData('application/x-blerg-board-card', JSON.stringify({ board: boardId, number: c.number, version: c.version, column: c.column_id })) }}
     onClick={e => { cardButton.current = e.currentTarget; setCard(c) }}>
    </BoardCard>{!!workerGlyphs(c.number)?.length && <div className="session-board-workers">{workerGlyphs(c.number)}</div>}</div>)}
    </div>
   </BoardColumn>)}
  </div>
  {card && <PinnedTicketModal key={card.id} ticket={{ source: 'blerg-board', board_id: boardId, key: card.reference || `#${card.number}`, title: card.title }} sessionId={sessionId} canMove={canMove} tabScoped worker={workerGlyphs(card.number)} onOpenTab={() => { tabs.open({ kind: 'ticket', ticket: { source: 'blerg-board', board_id: boardId, key: `#${card.number}`, title: card.title }, sessionId, label: card.reference || `#${card.number}` }, true); closeCard() }} onClose={closeCard} />}
  </div>
 </section>
}
