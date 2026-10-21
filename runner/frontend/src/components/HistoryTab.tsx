import { modelDisplayName } from '@blerglab/chat'
// HistoryTab: the session panel's History — the pieces of work the agent marked (topics), newest first.
// A card opens onto a timeline: the request, each file the topic published with every version, each
// subagent, how it ended. The work no topic holds is one quiet line after the cards ("Unmarked work ·
// 42 agents · 8 files") that opens onto its files and one line per subagent. Read from the server
// while the tab is on screen (useTopics). No row here
// carries data-event-id or data-call-id: "Show in conversation" looks those up in the whole page and
// must find the chat's card, not this row.
import { memo, useCallback, useEffect, useLayoutEffect, useRef, useState, type MouseEvent, type ReactNode } from 'react'
import {
  CommitChips as SharedCommitChips,
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
  TOPIC_COMMAND, countsLabel, heldFrom, hm, shownVersion, spanLabel, unmarkedLabel, usageTotal,
  type AgentItem, type AgentStatus, type DocItem, type Topic, type TopicCard, type TopicCommit, type TopicItem, type Unmarked,
} from '../lib/topics'
import { boardCardUrl, useBoardOrigin } from '../lib/boardLink'
import ShowLink from './RailShowLink'

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
}

export default function HistoryTab({ active, filesKey, ...rest }: HistoryTabProps) {
  const view = useTopics(rest.sessionId, active, filesKey)
  const now = useNow(active)
  return <TopicList {...rest} view={view} now={now} />
}

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

export function TopicList({ sessionId, view, now, missing, onJump, onOpenArtifact }: ListProps) {
  // Only where the held part of the conversation begins: a number, so streaming (every new event
  // changes the transcript) does not draw the list again.
  const held = useTranscriptStore(s => heldFrom(s.sessions[sessionId]))
  const [open, setOpen] = useState<ReadonlySet<string>>(() => new Set())
  const toggle = useCallback((id: string) => setOpen(prev => {
    const next = new Set(prev)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    return next
  }), [])
  // The panel's callbacks are new on each of its renders; the cards get stable ones that call the
  // latest, so a memoised card draws again only when its own props change.
  const latest = useRef({ onJump, onOpenArtifact })
  useLayoutEffect(() => { latest.current = { onJump, onOpenArtifact } })
  const jump = useCallback((rowKey: string, go: () => boolean) => latest.current.onJump(rowKey, go), [])
  const openFile = useCallback((id: string) => latest.current.onOpenArtifact(id), [])
  if (view.status === 'no-route') return <p className="rail-empty reel-note" data-testid="reel-no-route">History needs a newer runner server.</p>
  if (view.status === 'loading') return <p className="rail-empty reel-note" data-testid="reel-loading">Loading the history…</p>
  if (!view.reply) return <p className="rail-empty reel-note" data-testid="reel-error">Could not load the history. It is read again when something changes.</p>
  const { topics, unmarked } = view.reply
  const rest = unmarked && (unmarked.agents > 0 || unmarked.documents.length > 0 || (unmarked.cards?.length ?? 0) > 0) ? unmarked : null
  return (
    <>
      {view.status === 'error' && <p className="rail-missing reel-note" role="status" data-testid="reel-stale">Could not refresh the history; this is the last list read.</p>}
      {topics.length === 0
        ? (
          <p className="rail-empty reel-note" data-testid="reel-empty">
            No topics yet. The agent marks a topic when it starts a piece of work: <code>{TOPIC_COMMAND}</code>.
          </p>
        )
        : (
          <ul className="rail-list reel-list" data-testid="reel-list">
            {topics.map(t => (
              <TopicCard key={t.id} sessionId={sessionId} topic={t} now={now} open={open.has(t.id)} onToggle={toggle}
                heldFrom={held} missing={missing} onJump={jump} onOpenArtifact={openFile} />
            ))}
          </ul>
        )}
      {rest && <UnmarkedLine sessionId={sessionId} unmarked={rest} open={open.has(UNMARKED)} onToggle={toggle} onOpenArtifact={openFile} />}
    </>
  )
}

interface CardProps {
  sessionId: string
  topic: Topic
  now: Date
  open: boolean
  onToggle: (id: string) => void
  /** The first seq the chat holds (heldFrom). */
  heldFrom: number
  missing: string | null
  onJump: HistoryTabProps['onJump']
  onOpenArtifact: HistoryTabProps['onOpenArtifact']
}

const TopicCard = memo(function TopicCard({ sessionId, topic, now, open, onToggle, heldFrom, missing, onJump, onOpenArtifact }: CardProps) {
  const counts = countsLabel(topic.counts)
  const bodyId = `reel-body-${topic.id}`
  const running = topic.status === 'open'
  return (
    <li className={`rail-item reel-card${running ? ' is-open' : ''}`} data-testid="reel-card">
      <button type="button" className="reel-head" aria-expanded={open} aria-controls={open ? bodyId : undefined} data-testid="reel-head" onClick={() => onToggle(topic.id)}>
        <span className="reel-caret" aria-hidden="true" />
        {running && <span className="reel-now" data-testid="reel-now" title="Running now" aria-hidden="true" />}
        <span className="rail-item-title">{topic.title}</span>
      </button>
      {topic.outcome && <div className="rail-item-line reel-outcome" data-testid="reel-outcome">{topic.outcome}</div>}
      <div className="rail-item-line" data-testid="reel-span">{spanLabel(topic, now)}</div>
      {counts && <div className="rail-item-line" data-testid="reel-counts">{counts}</div>}
      {open && (
        <ol className="reel-timeline" id={bodyId}>
          {topic.items.map(item => (
            <Row key={`${item.type}:${item.event_id}`} sessionId={sessionId} item={item} held={item.seq >= heldFrom}
              missing={missing} onJump={onJump} onOpenArtifact={onOpenArtifact} />
          ))}
        </ol>
      )}
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
        </ol>
      )}
    </div>
  )
})

function Row({ sessionId, item, held, missing, onJump, onOpenArtifact }: {
  sessionId: string
  item: TopicItem
  held: boolean
  missing: string | null
  onJump: HistoryTabProps['onJump']
  onOpenArtifact: HistoryTabProps['onOpenArtifact']
}) {
  const rowKey = `reel:${item.event_id}`
  const where = held
    ? <ShowLink missing={missing === rowKey} onShow={() => onJump(rowKey, () => (item.type === 'agent' ? scrollToCall(item.call_id) : item.type === 'card' ? scrollToProposal(item.card.proposal_id) : scrollToEvent(item.event_id)))} />
    : <time className="reel-time" dateTime={item.ts} title="Earlier than what is loaded" data-testid="reel-earlier">{hm(item.ts)}</time>
  switch (item.type) {
    case 'request':
      return (
        <li className="reel-row" data-testid="reel-request">
          <div><span className="reel-who">{item.marker ? 'Started:' : 'You:'}</span> {item.text}</div>
          <div className="rail-actions">{where}</div>
        </li>
      )
    case 'done':
      return (
        <li className="reel-row" data-testid="reel-done">
          <div><span className="reel-who">{item.outcome ? 'Done:' : 'Done'}</span>{item.outcome ? ` ${item.outcome}` : ''}</div>
          {item.commits && item.commits.length > 0 && <CommitChips commits={item.commits} />}
          <div className="rail-actions">{where}</div>
        </li>
      )
    case 'document':
      return <DocumentRow sessionId={sessionId} item={item} where={where} onOpenArtifact={onOpenArtifact} />
    case 'agent':
      return <AgentRow item={item} where={where} />
    case 'card':
      return (
        <li className="reel-row" data-testid="reel-filed-card">
          <div><span className="reel-who">Filed:</span> <CardChip card={item.card} /></div>
          <div className="rail-actions">{where}</div>
        </li>
      )
  }
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
    <a className="reel-chip reel-card-chip" href={boardCardUrl(card.board_id, card.number, boardBase)} target="_blank" rel="noopener"
      title={`${label}${card.column ? ` · ${card.column}` : ''}${card.auto ? ' · approved by a session permission' : ''}`} data-testid="reel-card-chip">
      <span className="reel-card-board">{card.board_name || 'Board'}</span> <span className="reel-card-number">#{card.number}</span>
      {card.title && <> <span className="reel-card-title">{card.title}</span></>}
    </a>
  )
}

/** The commits a done names: the chat's chips in the History classes (a link to the commit's page
 *  when the server knows the repository, else a button that copies the sha). */
function CommitChips({ commits }: { commits: TopicCommit[] }) {
  return <SharedCommitChips commits={commits} variant="reel" />
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
            <a key={v.artifact_id} className={`reel-chip${v.in_topic ? ' in-topic' : ''}`} href={`?artifact=${encodeURIComponent(v.artifact_id)}`}
              aria-label={`${item.name} v${v.version}`} title={v.in_topic ? `v${v.version}, ${here}` : `v${v.version}`}
              data-testid="reel-chip" onClick={openFile(v.artifact_id)}>v{v.version}</a>
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

function AgentRow({ item, where }: { item: AgentItem; where: ReactNode }) {
  const model = modelDisplayName(item.model)
  const tokens = usageTotal(item.usage)
  return (
    <li className="reel-row rail-sub" data-testid="reel-agent">
      <div className="rail-item-head">
        <span className="rail-sub-icon" aria-hidden="true">↳</span>
        <span className="rail-item-title" title={item.title}>{item.title}</span>
      </div>
      <div className="rail-chips">
        <span className={`rail-chip status reel-status-${item.status}`} data-testid="reel-agent-status"><StatusMark status={item.status} />{STATUS_WORD[item.status]}</span>
        {item.agent_type && <span className="rail-chip">{item.agent_type}</span>}
        {model && <span className="rail-chip" title={item.model}>{model}</span>}
        {tokens > 0 && <span className="rail-chip" data-testid="reel-agent-tokens">{tokenLabel(tokens)}</span>}
        {item.duration_ms > 0 && <span className="rail-chip">{formatDuration(item.duration_ms)}</span>}
      </div>
      <div className="rail-actions">{where}</div>
    </li>
  )
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
