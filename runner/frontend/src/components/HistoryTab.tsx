import { modelDisplayName } from '@blerglab/chat'
// HistoryTab: the session panel's History — a compact feed of the session's major strokes, newest
// first, one line each (◆ a decision, ◇ the agent's own, ? what was asked of the person and how it was
// resolved, ✓ a milestone, ⚠ a finding), with filter chips; the pieces of work the agent marked
// (topics) are the section headers, and the open one shows the checklist's progress. A stroke's time
// jumps to its moment in the conversation. A topic's header opens onto its timeline: the request,
// each file the topic published with every version, each subagent, how it ended. The work no topic holds is one quiet line after the cards ("Unmarked work ·
// 42 agents · 8 files") that opens onto its files and one line per subagent. Read from the server
// while the tab is on screen (useTopics). No row here
// carries data-event-id or data-call-id: "Show in conversation" looks those up in the whole page and
// must find the chat's card, not this row.
import { createContext, memo, useCallback, useContext, useEffect, useLayoutEffect, useMemo, useRef, useState, type MouseEvent, type ReactNode } from 'react'
import {
  CommitChips as SharedCommitChips,
  Hover,
  formatDuration,
  loadPreviewUrl,
  previewKind,
  scrollToCall,
  scrollToEvent,
  tokenLabel,
  useTranscriptStore,
} from '@blerglab/chat'
import { apiFetch } from '../apiFetch'
import { useTopics, type TopicsView } from '../hooks/useTopics'
import {
  KIND_GLYPH, KIND_WORDS, MARK_COMMAND, STROKE_KINDS, TOPIC_COMMAND, countGlyphs, feedEntries, heldFrom, renamedBy, shownVersion, spanLabel,
  messageLabel, strokeCounts, strokeGlyph, strokeSpan, strokeTime, strokeWords, unmarkedLabel, usageTotal, waitingItems, wallClockMs,
  type AgentItem, type AgentStatus, type CardItem, type DocItem, type DoneItem, type RenameItem, type Stroke, type StrokeCard, type StrokeKind,
  type Topic, type TopicCard, type TopicCommit, type TopicRename, type Unmarked,
} from '../lib/topics'
import { boardCardUrl, useBoardOrigin } from '../lib/boardLink'
import { unknownBoardTarget, useSessionBoard } from '../lib/sessionBoard'
import { ShowMissing } from './RailShowLink'

/** The unmarked line's key in the open set (a topic id is tp_…). */
const UNMARKED = 'unmarked'

export interface HistoryTabProps {
  sessionId: string
  /** On screen: History is the chosen tab and the panel is open. */
  active: boolean
  /** LiveState.filesKey: the Files list changed. */
  filesKey: string
  /** The row whose "Show in conversation" found nothing (the panel's 4 s note). */
  missing: string | null
  /** The panel's jump: look for the row; say so when it is not drawn; close the drawer when it is. */
  onJump: (rowKey: string, go: () => boolean) => void
  /** Opens a file in the chat's viewer. */
  onOpenArtifact: (id: string) => void
  /** The checklist's progress, which the open topic shows instead of its duration; null without one. */
  progress?: { done: number; total: number } | null
}

export default function HistoryTab({ active, filesKey, ...rest }: HistoryTabProps) {
  const view = useTopics(rest.sessionId, active, filesKey)
  const now = useNow(active)
  return <TopicList {...rest} view={view} now={now} />
}

const NO_STROKES: Stroke[] = []

/** How many lines the feed draws at first, and how many more each "Show earlier" adds. */
export const FEED_STEP = 100

/** The time spans are read against: set when the tab comes on screen, then moved on every 30 s. */
function useNow(active: boolean): Date {
  const [now, setNow] = useState(() => new Date())
  useEffect(() => {
    if (!active) return
    const tick = () => setNow(new Date())
    const first = setTimeout(tick, 0)
    const t = setInterval(tick, 30_000)
    return () => {
      clearTimeout(first)
      clearInterval(t)
    }
  }, [active])
  return now
}

type ListProps = Omit<HistoryTabProps, 'active' | 'filesKey'> & { view: TopicsView; now: Date }

export function TopicList({ sessionId, view, now, missing, onJump, onOpenArtifact, progress = null }: ListProps) {
  // Only where the held part of the conversation begins: a number, so streaming (every new event
  // changes the transcript) does not draw the list again.
  const held = useTranscriptStore(s => heldFrom(s.sessions[sessionId]))
  const [open, setOpen] = useState<ReadonlySet<string>>(() => new Set())
  const [shown, setShown] = useState<ReadonlySet<StrokeKind>>(() => new Set())
  const [limit, setLimit] = useState(FEED_STEP)
  const toggle = useCallback((id: string) => setOpen(prev => {
    const next = new Set(prev)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    return next
  }), [])
  const filter = useCallback((k: StrokeKind) => setShown(prev => {
    const next = new Set(prev)
    if (next.has(k)) next.delete(k)
    else next.add(k)
    return next
  }), [])
  // The panel's callbacks are new on each of its renders; the cards get stable ones that call the
  // latest, so a memoised card draws again only when its own props change.
  const latest = useRef({ onJump, onOpenArtifact })
  useLayoutEffect(() => { latest.current = { onJump, onOpenArtifact } })
  const jump = useCallback((rowKey: string, go: () => boolean) => latest.current.onJump(rowKey, go), [])
  // The waiting items themselves (a fold's waiting members, never the fold's line or its chips), and
  // their ids: one Set per reply, so the memoised cards and lines do not draw again on every render.
  const replyStrokes = view.status !== 'no-route' && view.status !== 'loading' ? view.reply?.strokes ?? NO_STROKES : NO_STROKES
  const waiting = useMemo(() => waitingItems(replyStrokes), [replyStrokes])
  const pinnedIds = useMemo<ReadonlySet<string>>(() => new Set(waiting.map(st => st.id)), [waiting])
  const openFile = useCallback((id: string) => latest.current.onOpenArtifact(id), [])
  if (view.status === 'no-route') return <p className="rail-empty reel-note" data-testid="reel-no-route">History needs a newer runner server.</p>
  if (view.status === 'loading') return <p className="rail-empty reel-note" data-testid="reel-loading">Loading the history…</p>
  if (!view.reply) return <p className="rail-empty reel-note" data-testid="reel-error">Could not load the history. It is read again when something changes.</p>
  const { topics, unmarked } = view.reply
  const strokes = view.reply.strokes ?? NO_STROKES
  const rest = unmarked && (unmarked.agents > 0 || unmarked.documents.length > 0 || (unmarked.cards?.length ?? 0) > 0 || (unmarked.renames?.length ?? 0) > 0) ? unmarked : null
  // The newest lines first, FEED_STEP more at a time: a long session's feed is not drawn whole.
  const passing = shown.size === 0 ? strokes : strokes.filter(st => shown.has(st.kind))
  const visible = passing.length > limit ? passing.slice(0, limit) : passing
  const hidden = passing.length - visible.length
  const truncated = view.reply.strokes_truncated ?? 0
  // While earlier lines are held back, a topic older than the oldest line drawn waits with them.
  const oldest = hidden > 0 ? Date.parse(visible[visible.length - 1].at) : Number.NEGATIVE_INFINITY
  const shownTopics = hidden > 0
    ? topics.filter(t => t.status === 'open' || Date.parse(t.ended_at ?? t.started_at) >= oldest || visible.some(st => st.topic_id === t.id))
    : topics
  const entries = feedEntries(shownTopics, visible, shown)
  // Waiting items are pinned above the feed: never out of sight, however far back they are (#121).

  return (
    <HistorySession.Provider value={sessionId}>
      {view.status === 'error' && <p className="rail-missing reel-note" role="status" data-testid="reel-stale">Could not refresh the history; this is the last list read.</p>}
      {strokes.length > 0 && <StrokeFilters counts={strokeCounts(strokes)} shown={shown} onToggle={filter} />}
      {waiting.length > 0 && (shown.size === 0 || shown.has('attention')) && (
        <section className="stroke-pinned" aria-label="Waiting on you" data-testid="stroke-pinned">
          <h3 className="stroke-pinned-head"><span aria-hidden="true">? </span>Waiting on you</h3>
          <ol className="stroke-list">
            {waiting.map(st => <StrokeLine key={`pin:${st.id}`} stroke={st} now={now} heldFrom={held} missing={missing} onJump={jump} testId="stroke-pin" />)}
          </ol>
        </section>
      )}
      {topics.length === 0 && strokes.length === 0
        ? (
          <p className="rail-empty reel-note" data-testid="reel-empty">
            No topics yet. The agent marks a topic when it starts a piece of work: <code>{TOPIC_COMMAND}</code>, and a decision,
            milestone or finding with <code>{MARK_COMMAND}</code>.
          </p>
        )
        : entries.length === 0
        ? <p className="rail-empty reel-note" data-testid="stroke-none">Nothing of these kinds yet.</p>
        : (
          <ul className="rail-list reel-list" data-testid="reel-list">
            {entries.map(e => (e.type === 'topic'
              ? (
                <TopicCard key={e.topic.id} topic={e.topic} strokes={e.strokes} pinned={pinnedIds} now={now} open={open.has(e.topic.id)} onToggle={toggle}
                  heldFrom={held} missing={missing} onJump={jump} onOpenArtifact={openFile} progress={e.topic.status === 'open' ? progress : null} />
              )
              : (
                <li key={`loose:${e.key}`} className="stroke-run" data-testid="stroke-run">
                  <ol className="stroke-list">
                    {e.strokes.map(st => <StrokeLine key={st.id} stroke={st} now={now} heldFrom={held} missing={missing} onJump={jump} pinned={pinnedIds} />)}
                  </ol>
                </li>
              )))}
          </ul>
        )}
      {hidden > 0 && (
        <button type="button" className="rail-link stroke-earlier" data-testid="stroke-earlier" onClick={() => setLimit(l => l + FEED_STEP)}>
          Show earlier ({hidden})
        </button>
      )}
      {hidden === 0 && truncated > 0 && shown.size === 0 && (
        <p className="rail-empty reel-note" data-testid="stroke-truncated">{truncated} older {truncated === 1 ? 'line is' : 'lines are'} not loaded.</p>
      )}
      {rest && shown.size === 0 && hidden === 0 && <UnmarkedLine sessionId={sessionId} unmarked={rest} open={open.has(UNMARKED)} onToggle={toggle} onOpenArtifact={openFile} />}
    </HistorySession.Provider>
  )
}

/** The filter chips: a glyph and a count each, the kind's word in the tooltip and the name. None
 *  pressed shows every kind; pressed ones show only theirs. */
function StrokeFilters({ counts, shown, onToggle }: { counts: Record<StrokeKind, number>; shown: ReadonlySet<StrokeKind>; onToggle: (k: StrokeKind) => void }) {
  return (
    <div className="stroke-filters" role="group" aria-label="Show only" data-testid="stroke-filters">
      {STROKE_KINDS.map(k => (
        <button key={k} type="button" className={`stroke-filter stroke-${k}`} aria-pressed={shown.has(k)} title={KIND_WORDS[k]}
          aria-label={`${KIND_WORDS[k]}, ${counts[k]}`} data-testid={`stroke-filter-${k}`} onClick={() => onToggle(k)}>
          <span className="stroke-filter-glyph" aria-hidden="true">{KIND_GLYPH[k]}</span>
          <span className="stroke-filter-count" aria-hidden="true">{counts[k]}</span>
        </button>
      ))}
    </div>
  )
}

/** The proposal's card, else the row of the event: where a stroke's jump goes. */
function goTo(jump: NonNullable<Stroke['jump']>): boolean {
  if (jump.event_id && scrollToEvent(jump.event_id)) return true
  return jump.proposal_id ? scrollToProposal(jump.proposal_id) : false
}

/** One stroke on one line: its glyph (its meaning in the tooltip and for a screen reader), its text
 *  (as text, never markup), how it was resolved, its card and commit chips, and its time, which jumps
 *  to its moment in the conversation when the chat holds it. A fold of repeated strokes ("Filed 26
 *  cards → approved, 1 rejected") shows its cards as chips that wrap under it, its span, and opens on
 *  a tap onto one line per stroke. */
const StrokeLine = memo(function StrokeLine({ stroke, now, heldFrom, missing, onJump, testId = 'stroke', pinned, keyPrefix = '', group }: {
  stroke: Stroke
  now: Date
  heldFrom: number
  missing: string | null
  onJump: HistoryTabProps['onJump']
  testId?: string
  /** The ids pinned above the feed: such a stroke here is only a marker pointing up. */
  pinned?: ReadonlySet<string>
  /** The parent's row key, so a fold's items have keys of their own. */
  keyPrefix?: string
  /** In an opened fold: "waiting" for the waiting group at the top. */
  group?: 'waiting' | 'rest'
}) {
  const [open, setOpen] = useState(false)
  // A fold's line is never a marker: only a waiting item itself is pinned (its line keeps its own id).
  if (pinned?.has(stroke.id) && (stroke.items?.length ?? 0) < 2) {
    return (
      <li className="stroke stroke-pinned-marker" data-testid="stroke-pinned-marker" data-group={group} title={`${stroke.text} (waiting: pinned above)`}>
        <span className="stroke-glyph" aria-hidden="true">↑</span>
        <span className="stroke-text">pinned<span className="rail-sr">: {stroke.text}, waiting, pinned above</span></span>
      </li>
    )
  }
  return <StrokeRow stroke={stroke} now={now} heldFrom={heldFrom} missing={missing} onJump={onJump} testId={testId} pinned={pinned} keyPrefix={keyPrefix} open={open} setOpen={setOpen} group={group} />
})

function StrokeRow({ stroke, now, heldFrom, missing, onJump, testId, pinned, keyPrefix, open, setOpen, group }: {
  stroke: Stroke
  now: Date
  heldFrom: number
  missing: string | null
  onJump: HistoryTabProps['onJump']
  testId: string
  pinned?: ReadonlySet<string>
  keyPrefix: string
  open: boolean
  setOpen: (f: (o: boolean) => boolean) => void
  group?: 'waiting' | 'rest'
}) {
  const words = strokeWords(stroke)
  const rowKey = `${keyPrefix}${testId}:${stroke.id}`
  const folded = (stroke.items?.length ?? 0) > 1
  const when = folded && stroke.first_at ? strokeSpan(stroke.first_at, stroke.at, now) : strokeTime(stroke.at, now)
  const jump = stroke.jump
  const reachable = jump && jump.seq >= heldFrom
  const full = stroke.outcome ? `${stroke.text} → ${stroke.outcome}` : stroke.text
  const cls = [
    'stroke', `stroke-${stroke.kind}`,
    stroke.kind === 'decision' && stroke.by !== 'person' ? 'by-agent' : '',
    stroke.state === 'open' ? 'is-waiting' : '',
    stroke.state === 'declined' || stroke.state === 'expired' || stroke.state === 'unanswered' ? 'is-closed' : '',
    folded ? 'is-fold' : '',
  ].filter(Boolean).join(' ')
  const listId = `fold-${(keyPrefix + testId).replace(/[^\w-]/g, '-')}-${stroke.id}`
  const text = (
    <>
      {stroke.text}
      {stroke.outcome && <span className="stroke-outcome"> → {stroke.outcome}</span>}
    </>
  )
  return (
    <li className={cls} data-testid={testId} data-kind={stroke.kind} data-group={group}>
      <span className="stroke-glyph" title={words} aria-hidden="true">{strokeGlyph(stroke)}</span>
      <span className="rail-sr">{words}: </span>
      {folded
        ? (
          <button type="button" className="stroke-text stroke-fold" title={full} data-testid="stroke-text" aria-expanded={open}
            aria-controls={open ? listId : undefined} onClick={() => setOpen(o => !o)}>{text}</button>
        )
        : <span className="stroke-text" title={full} data-testid="stroke-text">{text}</span>}
      {stroke.card && <StrokeCardChip card={stroke.card} />}
      {stroke.commits && stroke.commits.length > 0 && <CommitChips commits={stroke.commits} />}
      {reachable
        ? (
          <button type="button" className="stroke-time stroke-jump" title={`Show in conversation (${new Date(stroke.at).toLocaleString()})`}
            aria-label={`Show in conversation, ${when}`} data-testid="stroke-jump" onClick={() => onJump(rowKey, () => goTo(jump))}>{when}</button>
        )
        : (
          <time className="stroke-time" dateTime={stroke.at} data-testid="stroke-time"
            title={jump ? 'Earlier than what is loaded' : new Date(stroke.at).toLocaleString()}>{when}</time>
        )}
      {folded && stroke.cards && stroke.cards.length > 0 && (
        <span className="stroke-cards" data-testid="stroke-cards">
          {(stroke.cards_truncated ?? 0) > 0 && (
            <span className="reel-chip older" title={`${stroke.cards_truncated} earlier cards are not listed`} data-testid="stroke-cards-earlier">+{stroke.cards_truncated}</span>
          )}
          {stroke.cards.map(c => <StrokeCardChip key={`${c.board_id ?? ''}#${c.number}`} card={c} />)}
        </span>
      )}
      {missing === rowKey && <ShowMissing />}
      {folded && open && (
        <ol className="stroke-list stroke-fold-items" id={listId} data-testid="stroke-fold-items">
          {/* The waiting members first, as a group of their own, then the rest newest first: "and N
              earlier" below them is then true of every row above it. */}
          {waitingFirst(stroke.items!).map(([it, group]) => (
            <StrokeLine key={it.id} stroke={it} now={now} heldFrom={heldFrom} missing={missing} onJump={onJump} testId="stroke-item" pinned={pinned}
              keyPrefix={`${rowKey}/`} group={group} />
          ))}
          {(stroke.items_truncated ?? 0) > 0 && (
            <li className="stroke stroke-earlier-items" data-testid="stroke-items-earlier">and {stroke.items_truncated} earlier</li>
          )}
        </ol>
      )}
    </li>
  )
}

/** A fold's members as the opened list draws them: the waiting ones first (a group of their own),
 *  then the others in their order (newest first). */
function waitingFirst(items: readonly Stroke[]): [Stroke, 'waiting' | 'rest'][] {
  return [
    ...items.filter(i => i.state === 'open').map(i => [i, 'waiting'] as [Stroke, 'waiting']),
    ...items.filter(i => i.state !== 'open').map(i => [i, 'rest'] as [Stroke, 'rest']),
  ]
}

/** The session History shows: its chips' hovercards read from it. */
const HistorySession = createContext('')

/** A board card's hovercard target from History's copy of it. */
function boardTarget(card: { board_id?: string; number: number; title?: string; column?: string; type?: string; priority?: string }) {
  return {
    kind: 'card' as const, source: 'blerg-board', key: `#${card.number}`, ...(card.board_id ? { boardId: card.board_id } : {}),
    ...(card.title ? { title: card.title } : {}), ...(card.column ? { status: card.column } : {}),
    ticket: { ...(card.type ? { type: card.type } : {}), ...(card.priority ? { priority: card.priority } : {}) },
  }
}

/** A card a stroke names: "#115", a link to the card on its Board page in Blerg, with the card's
 *  hovercard, when the board is known: the chip's own, else the session's one board (#139). With no
 *  board known, just the number, whose hovercard says so ("Card #115 · board unknown"). */
function StrokeCardChip({ card }: { card: StrokeCard }) {
  const boardBase = useBoardOrigin()
  const sessionBoard = useSessionBoard()
  const boardId = card.board_id || sessionBoard?.id
  const boardName = card.board_id ? card.board_name : card.board_name || sessionBoard?.name
  const label = `${boardName ? `${boardName} ` : ''}#${card.number}${card.title ? ` ${card.title}` : ''}`
  if (!boardId) {
    return (
      <Hover target={unknownBoardTarget(card.number)}>
        <span className="reel-chip stroke-card" tabIndex={0} title={`Card #${card.number} · board unknown`} aria-label={`Card #${card.number}, board unknown`}
          data-testid="stroke-card" data-board="unknown">#{card.number}</span>
      </Hover>
    )
  }
  return (
    <Hover target={boardTarget({ ...card, board_id: boardId })}>
      <a className="reel-chip stroke-card" href={boardCardUrl(boardId, card.number, boardBase)} target="_blank" rel="noopener"
        title={label} aria-label={`Card ${label}`} data-testid="stroke-card">#{card.number}</a>
    </Hover>
  )
}

interface CardProps {
  topic: Topic
  strokes: Stroke[]
  /** The ids pinned above the feed: they stand as a marker here. */
  pinned: ReadonlySet<string>
  now: Date
  open: boolean
  onToggle: (id: string) => void
  /** The first seq the chat holds (heldFrom). */
  heldFrom: number
  missing: string | null
  onJump: HistoryTabProps['onJump']
  onOpenArtifact: HistoryTabProps['onOpenArtifact']
  progress: { done: number; total: number } | null
}

const TopicCard = memo(function TopicCard({ topic, strokes, pinned, now, open, onToggle, heldFrom, missing, onJump, onOpenArtifact, progress }: CardProps) {
  const counts = countGlyphs(topic.counts)
  const bodyId = `reel-body-${topic.id}`
  const running = topic.status === 'open'
  return (
    <li className={`rail-item reel-card${running ? ' is-open' : ''}`} data-testid="reel-card">
      <button type="button" className="reel-head" aria-expanded={open} aria-controls={open ? bodyId : undefined} data-testid="reel-head" onClick={() => onToggle(topic.id)}>
        <span className="reel-caret" aria-hidden="true" />
        {running && <span className="reel-now" data-testid="reel-now" title="Running now" aria-hidden="true" />}
        <span className="rail-item-title" title={topic.title}>{topic.title}</span>
        <span className="reel-meta">
          {running && progress && progress.total > 0
            ? <span className="reel-progress" data-testid="reel-progress" title={`Checklist: ${progress.done} of ${progress.total} done`}>{progress.done} of {progress.total}</span>
            : <span className="reel-span" data-testid="reel-span">{spanLabel(topic, now)}</span>}
          {counts.length > 0 && (
            <span className="reel-counts" data-testid="reel-counts" aria-label={counts.map(c => c.words).join(', ')}>
              {counts.map(c => <span key={c.glyph} className="reel-count" title={c.words} aria-hidden="true">{c.glyph}{c.n}</span>)}
            </span>
          )}
        </span>
      </button>
      {topic.outcome && <div className="rail-item-line reel-outcome" data-testid="reel-outcome" title={topic.outcome}>{topic.outcome}</div>}
      {strokes.length > 0 && (
        <ol className="stroke-list">
          {strokes.map(st => <StrokeLine key={st.id} stroke={st} now={now} heldFrom={heldFrom} missing={missing} onJump={onJump} pinned={pinned} />)}
        </ol>
      )}
      {open && <TopicSummary id={bodyId} topic={topic} now={now} heldFrom={heldFrom} missing={missing} onJump={onJump} onOpenArtifact={onOpenArtifact} />}
    </li>
  )
})

/** The work no topic holds: one line, collapsed, that opens onto its files (the version chips of a
 *  topic's document rows) and one line per subagent. No request rows: a message is not a topic. */
const UnmarkedLine = memo(function UnmarkedLine({ sessionId, unmarked, open, onToggle, onOpenArtifact }: {
  sessionId: string
  unmarked: Unmarked
  open: boolean
  onToggle: (id: string) => void
  onOpenArtifact: HistoryTabProps['onOpenArtifact']
}) {
  const bodyId = 'reel-body-unmarked'
  return (
    <div className="reel-rest" data-testid="reel-unmarked">
      <button type="button" className="reel-head" aria-expanded={open} aria-controls={open ? bodyId : undefined}
        data-testid="reel-unmarked-head" onClick={() => onToggle(UNMARKED)}>
        <span className="reel-caret" aria-hidden="true" />
        <span className="reel-rest-label">{unmarkedLabel(unmarked)}</span>
      </button>
      {open && (
        <ol className="reel-timeline" id={bodyId}>
          {unmarked.documents.map(d => <DocumentRow key={`document:${d.event_id}`} sessionId={sessionId} item={d} here="published outside a topic" onOpenArtifact={onOpenArtifact} />)}
          {unmarked.subagents.map(a => <AgentLine key={`agent:${a.event_id}`} item={a} />)}
          {(unmarked.cards ?? []).map(c => <li key={`card:${c.event_id}`} className="reel-row reel-line" data-testid="reel-filed-card"><CardChip card={c.card} /></li>)}
          {(unmarked.renames ?? []).map(r => <li key={`rename:${r.event_id}`} className="reel-row reel-line" data-testid="reel-rename"><RenameLine rename={r.rename} /></li>)}
        </ol>
      )}
    </div>
  )
})

type Msg = { seq: number; event_id: string; ts: string; text: string }

/** A topic opened: one compact line per kind of thing it holds, never the chat replayed. Your
 *  messages (how many, each a time that jumps to it), its subagents (how many, each a chip with its
 *  status that jumps to it, their tokens and time), its files and its commits and cards as chips,
 *  its renames. Each line's time jumps to its first item in the conversation. */
function TopicSummary({ id, topic, now, heldFrom, missing, onJump, onOpenArtifact }: {
  id: string
  topic: Topic
  now: Date
  heldFrom: number
  missing: string | null
  onJump: HistoryTabProps['onJump']
  onOpenArtifact: HistoryTabProps['onOpenArtifact']
}) {
  const messages: Msg[] = topic.messages
    ?? topic.items.flatMap(it => (it.type === 'request' && !it.marker ? [{ seq: it.seq, event_id: it.event_id, ts: it.ts, text: it.text }] : []))
  const agents = topic.items.filter((it): it is AgentItem => it.type === 'agent')
  const docs = topic.items.filter((it): it is DocItem => it.type === 'document')
  const dones = topic.items.filter((it): it is DoneItem => it.type === 'done' && (it.commits?.length ?? 0) > 0)
  const cards = topic.items.filter((it): it is CardItem => it.type === 'card')
  const renames = topic.items.filter((it): it is RenameItem => it.type === 'rename')
  const line = { now, heldFrom, missing, onJump, topicId: topic.id }
  const tokens = agents.reduce((n, a) => n + usageTotal(a.usage), 0)
  // Time on the clock: parallel subagents are not added up.
  const ms = wallClockMs(agents)
  const days = new Set(messages.map(m => new Date(m.ts).toDateString()))
  const spansDays = days.size > 1
  const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`
  // A plain click opens the file in the chat; a modified click keeps the link's own behaviour.
  const openFile = (fileId: string) => (e: MouseEvent<HTMLAnchorElement>) => {
    if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
    e.preventDefault()
    onOpenArtifact(fileId)
  }
  return (
    <ol className="reel-summary" id={id} data-testid="reel-summary">
      {messages.length > 0 && (
        <SummaryLine {...line} kind="messages" glyph="💬" words={plural(messages.length, 'message of yours', 'messages of yours')}
          first={{ seq: messages[0].seq, ts: messages[0].ts, go: () => scrollToEvent(messages[0].event_id) }}>
          <span className="reel-sum-label">{plural(messages.length + (topic.messages_truncated ?? 0), 'message', 'messages')}</span>
          {(topic.messages_truncated ?? 0) > 0 && (
            <span className="reel-chip older" title={`${topic.messages_truncated} earlier messages are not listed`} data-testid="reel-sum-messages-earlier">+{topic.messages_truncated}</span>
          )}
          {messages.map(m => (
            <JumpChip key={m.event_id} {...line} rowKey={`msg:${m.event_id}`} seq={m.seq} label={messageLabel(m.ts, spansDays)} title={m.text}
              name={`Your message at ${messageLabel(m.ts, spansDays)}: ${m.text}`} testId="reel-sum-message" go={() => scrollToEvent(m.event_id)} />
          ))}
        </SummaryLine>
      )}
      {agents.length > 0 && (
        <SummaryLine {...line} kind="agents" glyph="↳" words={plural(agents.length, 'subagent', 'subagents')}
          first={{ seq: agents[0].seq, ts: agents[0].ts, go: () => scrollToCall(agents[0].call_id) }}>
          <span className="reel-sum-label">{plural(agents.length, 'subagent', 'subagents')}</span>
          {agents.map(a => (
            <JumpChip key={a.event_id} {...line} rowKey={`agent:${a.call_id}`} seq={a.seq} testId="reel-sum-agent"
              label={<><StatusMark status={a.status} /> <span className="reel-sum-name">{a.title || a.agent_type || 'Subagent'}</span></>}
              title={[a.title, STATUS_WORD[a.status], modelDisplayName(a.model), usageTotal(a.usage) > 0 ? tokenLabel(usageTotal(a.usage)) : '', a.duration_ms > 0 ? formatDuration(a.duration_ms) : ''].filter(Boolean).join(' · ')}
              name={`${a.title || 'Subagent'}, ${STATUS_WORD[a.status]}`} go={() => scrollToCall(a.call_id)} />
          ))}
          {(tokens > 0 || ms > 0) && (
            <span className="reel-sum-meta" data-testid="reel-sum-agent-meta">{[tokens > 0 ? tokenLabel(tokens) : '', ms > 0 ? formatDuration(ms) : ''].filter(Boolean).join(' · ')}</span>
          )}
        </SummaryLine>
      )}
      {docs.length > 0 && (
        <SummaryLine {...line} kind="files" glyph="📄" words={plural(docs.length, 'file', 'files')}
          first={{ seq: docs[0].seq, ts: docs[0].ts, go: () => scrollToEvent(docs[0].event_id) }}>
          {docs.map(d => {
            const v = shownVersion(d)
            const label = `${d.name}${v ? ` v${v.version}` : ''}`
            if (!v || v.deleted || v.missing) {
              return <span key={d.event_id} className="reel-chip deleted" aria-disabled="true" title={`${label} is no longer available`} data-testid="reel-sum-file"><s>{label}</s></span>
            }
            return (
              <a key={d.event_id} className="reel-chip in-topic reel-sum-file" href={`?artifact=${encodeURIComponent(v.artifact_id)}`} title={label}
                data-testid="reel-sum-file" onClick={openFile(v.artifact_id)}>{label}</a>
            )
          })}
        </SummaryLine>
      )}
      {dones.length > 0 && (
        <SummaryLine {...line} kind="commits" glyph="⎇" words="Commits"
          first={{ seq: dones[0].seq, ts: dones[0].ts, go: () => scrollToEvent(dones[0].event_id) }}>
          <CommitChips commits={dones.flatMap(d => d.commits ?? [])} />
        </SummaryLine>
      )}
      {cards.length > 0 && (
        <SummaryLine {...line} kind="cards" glyph="▤" words={plural(cards.length, 'card filed', 'cards filed')}
          first={{ seq: cards[0].seq, ts: cards[0].ts, go: () => scrollToProposal(cards[0].card.proposal_id) }}>
          {cards.map(c => <CardChip key={c.event_id} card={c.card} />)}
        </SummaryLine>
      )}
      {renames.length > 0 && (
        <SummaryLine {...line} kind="renames" glyph="✎" words={plural(renames.length, 'rename', 'renames')}
          first={{ seq: renames[0].seq, ts: renames[0].ts, go: () => scrollToEvent(renames[0].event_id) }}>
          {renames.map(r => <RenameLine key={r.event_id} rename={r.rename} />)}
        </SummaryLine>
      )}
    </ol>
  )
}

/** One summary line: its glyph (its words in the tooltip and for a screen reader), its chips, and its
 *  time, which jumps to the line's first item when the chat holds it. */
function SummaryLine({ kind, glyph, words, first, children, now, heldFrom, missing, onJump, topicId }: {
  kind: string
  glyph: string
  words: string
  first: { seq: number; ts: string; go: () => boolean }
  children: ReactNode
  now: Date
  heldFrom: number
  missing: string | null
  onJump: HistoryTabProps['onJump']
  topicId: string
}) {
  const rowKey = `sum:${topicId}:${kind}`
  const when = strokeTime(first.ts, now)
  return (
    <li className={`reel-sum reel-sum-${kind}`} data-testid={`reel-sum-${kind}`}>
      <span className="reel-sum-glyph" title={words} aria-hidden="true">{glyph}</span>
      <span className="rail-sr">{words}: </span>
      <span className="reel-sum-body">{children}</span>
      {first.seq >= heldFrom
        ? <button type="button" className="stroke-time stroke-jump" aria-label={`Show in conversation, ${when}`} title="Show in conversation" data-testid="reel-sum-jump" onClick={() => onJump(rowKey, first.go)}>{when}</button>
        : <time className="stroke-time" dateTime={first.ts} title="Earlier than what is loaded">{when}</time>}
      {missing === rowKey && <ShowMissing />}
    </li>
  )
}

/** A chip that jumps to its item in the conversation (or, older than what the chat holds, only says
 *  what it is). */
function JumpChip({ rowKey, seq, label, title, name, testId, go, heldFrom, missing, onJump }: {
  rowKey: string
  seq: number
  label: ReactNode
  title: string
  name: string
  testId: string
  go: () => boolean
  heldFrom: number
  missing: string | null
  onJump: HistoryTabProps['onJump']
}) {
  if (seq < heldFrom) return <span className="reel-chip reel-jump-chip is-earlier" title={`${title} (earlier than what is loaded)`} data-testid={testId}>{label}</span>
  return (
    <>
      <button type="button" className="reel-chip reel-jump-chip" title={title} aria-label={`${name}. Show in conversation`} data-testid={testId}
        onClick={() => onJump(rowKey, go)}>{label}</button>
      {missing === rowKey && <ShowMissing />}
    </>
  )
}

/** "Renamed by the parent: Worker → Fix the login flow". */
function RenameLine({ rename }: { rename: TopicRename }) {
  return (
    <div className="reel-rename-text" title={`${rename.from || 'Untitled'} → ${rename.to}`}>
      <span className="reel-who">{renamedBy(rename.by)}</span> {rename.from || 'Untitled'} → {rename.to}
    </div>
  )
}

/** The proposal's card in the chat (the decision itself draws nothing of its own). */
function scrollToProposal(id: string): boolean {
  const el = id ? document.querySelector<HTMLElement>(`[data-proposal-id="${CSS.escape(id)}"]`) : null
  if (!el) return false
  el.scrollIntoView({ block: 'center', behavior: 'smooth' })
  return true
}

/** A card an approved proposal created: board, #number and title, as one chip that opens the card
 *  on its Board page, in Blerg (never a link out of Blerg). */
function CardChip({ card }: { card: TopicCard }) {
  const boardBase = useBoardOrigin()
  const label = `${card.board_name || 'Board'} #${card.number}${card.title ? ` ${card.title}` : ''}`
  return (
    <Hover target={boardTarget(card)}>
      <a className="reel-chip reel-card-chip" href={boardCardUrl(card.board_id, card.number, boardBase)} target="_blank" rel="noopener"
        title={`${label}${card.column ? ` · ${card.column}` : ''}${card.auto ? ' · approved by a session permission' : ''}`} data-testid="reel-card-chip">
        <span className="reel-card-board">{card.board_name || 'Board'}</span> <span className="reel-card-number">#{card.number}</span>
        {card.title && <> <span className="reel-card-title">{card.title}</span></>}
      </a>
    </Hover>
  )
}

/** The commits a done names: the chat's chips in the History classes (a link to the commit's page
 *  when the server knows the repository, else a button that copies the sha). */
function CommitChips({ commits }: { commits: TopicCommit[] }) {
  const sessionId = useContext(HistorySession)
  return <SharedCommitChips commits={commits} variant="reel" hover={sessionId ? { sessionId } : undefined} />
}

/** where: the row's "Show in conversation" (none in the unmarked list); here: what a filled chip means. */
function DocumentRow({ sessionId, item, where, here = 'published in this topic', onOpenArtifact }: {
  sessionId: string; item: DocItem; where?: ReactNode; here?: string; onOpenArtifact: (id: string) => void
}) {
  // A plain click opens the file in the chat; a modified click keeps the link's own behaviour (a new tab).
  const openFile = (id: string) => (e: MouseEvent<HTMLAnchorElement>) => {
    if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
    e.preventDefault()
    onOpenArtifact(id)
  }
  // The server lists the newest 20 versions plus every one this topic published.
  const older = Math.max(0, (item.versions_total ?? item.versions.length) - item.versions.length)
  return (
    <li className="reel-row" data-testid="reel-document">
      <div className="rail-item-head">
        <DocIcon sessionId={sessionId} item={item} />
        <span className="rail-item-title" title={item.name}>{item.name}</span>
      </div>
      {/* Oldest first, the ones left out before them. Each named "plan.md v2" for a screen reader. */}
      <div className="reel-chips">
        {older > 0 && (
          <span className="reel-chip older" title="Older versions are in the Files list" data-testid="reel-older">+{older} older</span>
        )}
        {item.versions.map(v => (v.deleted
          ? (
            <span key={v.artifact_id} className={`reel-chip deleted${v.in_topic ? ' in-topic' : ''}`} aria-disabled="true" title={`v${v.version} was deleted`} data-testid="reel-chip">
              <span className="rail-sr">{item.name} </span><s>v{v.version}</s><span className="rail-sr"> (deleted)</span>
            </span>
          )
          : v.missing
          ? (
            <span key={v.artifact_id} className={`reel-chip missing${v.in_topic ? ' in-topic' : ''}`} aria-disabled="true" title={`v${v.version} is no longer available`} data-testid="reel-chip">
              <span className="rail-sr">{item.name} </span><s>v{v.version}</s><span className="rail-sr"> (no longer available)</span>
            </span>
          )
          : (
            <Hover key={v.artifact_id} target={{ kind: 'file', sessionId, id: v.artifact_id }}>
              <a className={`reel-chip${v.in_topic ? ' in-topic' : ''}`} href={`?artifact=${encodeURIComponent(v.artifact_id)}`}
                aria-label={`${item.name} v${v.version}`} title={v.in_topic ? `v${v.version}, ${here}` : `v${v.version}`}
                data-testid="reel-chip" onClick={openFile(v.artifact_id)}>v{v.version}</a>
            </Hover>
          )
        ))}
      </div>
      {where && <div className="rail-actions">{where}</div>}
    </li>
  )
}

const rawOf = (sessionId: string) => (id: string) =>
  apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/artifacts/${encodeURIComponent(id)}/raw`).then(r => {
    if (!r.ok) throw new Error(`HTTP ${r.status}`)
    return r.blob()
  })

/** A mock image gets a thumbnail (the transcript's own preview cache); anything else an icon. */
function DocIcon({ sessionId, item }: { sessionId: string; item: DocItem }) {
  const shown = shownVersion(item)
  const id = shown && !shown.deleted && !shown.missing && item.mock && previewKind(item.view, item.size) === 'image' ? shown.artifact_id : null
  const [thumb, setThumb] = useState<{ id: string; url: string } | null>(null)
  useEffect(() => {
    if (!id) return
    let live = true
    loadPreviewUrl(sessionId, { id, view: item.view, content_type: item.content_type }, rawOf(sessionId)).then(
      url => { if (live) setThumb({ id, url }) },
      () => { /* the icon stays */ },
    )
    return () => { live = false }
  }, [sessionId, id, item.view, item.content_type])
  if (id && thumb?.id === id) return <img className="reel-thumb" src={thumb.url} alt="" data-testid="reel-thumb" />
  return <span className="reel-icon" aria-hidden="true">{item.view === 'html' ? '🖥' : item.mock ? '🖼' : '📄'}</span>
}

const STATUS_WORD: Record<AgentStatus, string> = {
  running: 'running', background: 'in the background', done: 'done', error: 'error', stopped: 'stopped',
}

/** The subagent card's glyphs: a spinner while it runs, ✓ done, ✗ error, ■ stopped. */
function StatusMark({ status }: { status: AgentStatus }) {
  switch (status) {
    case 'running':
    case 'background':
      return <span className="rail-spin" aria-hidden="true" />
    case 'done':
      return <span className="reel-glyph" aria-hidden="true">✓</span>
    case 'error':
      return <span className="reel-glyph" aria-hidden="true">✗</span>
    case 'stopped':
      return <span className="reel-glyph" aria-hidden="true">■</span>
  }
}

/** An unmarked subagent on one line: its status mark, its title, its model and tokens. */
function AgentLine({ item }: { item: AgentItem }) {
  const tokens = usageTotal(item.usage)
  const meta = [modelDisplayName(item.model), tokens > 0 ? tokenLabel(tokens) : ''].filter(Boolean).join(' · ')
  return (
    <li className="reel-row reel-line" data-testid="reel-unmarked-agent">
      <span className={`reel-line-mark reel-status-${item.status}`} title={STATUS_WORD[item.status]}>
        <StatusMark status={item.status} /><span className="rail-sr">{STATUS_WORD[item.status]} </span>
      </span>
      <span className="reel-line-title" title={item.title}>{item.title}</span>
      {meta && <span className="reel-line-meta" title={item.model}>{meta}</span>}
    </li>
  )
}
