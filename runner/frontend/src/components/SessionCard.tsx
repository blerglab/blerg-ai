import { modelDisplayName } from '@blerglab/chat'
import { useContext } from 'react'
import {ChildSessionActions, ParentSessionActions} from './ChildDiscard'
import { useNavigate } from 'react-router-dom'
import { OpenSessionContext } from '../lib/openSession'
import type { SessionInfo, DaemonInfo } from '../types'
import { send } from '../ws'
import { isNoRepo, repoLabel } from '../lib/noRepo'
import { startedByLabel } from '../lib/startedBy'
import { BACKGROUND_COLOR, PAUSED_WORDS, displayLabel, displayState, pausedByRestart } from '../lib/displayState'

export interface SessionCardProps {
  /** The parent's started sessions while they are collapsed under it (null or absent: shown, or none). */
  hiddenChildren?: { total: number; running: number; waiting: number } | null
  /** The card's started sessions: how many (the plain "↳ N" chip), whether they are shown, and the
   *  show/hide handler (the round chevron in the card's second line, over the nested indent line). */
  childrenControl?: { total: number; shown: boolean; toggle: () => void } | null
  session: SessionInfo
  daemon: DaemonInfo | undefined
  active?: boolean
  /** Drawn under its parent's card: the "from {parent}" chip would only repeat what the nesting shows. */
  nested?: boolean
}

const STATUS_STYLES: Record<string, { bg: string; border: string; color: string; text: string }> = {
  waiting:  { bg: 'color-mix(in srgb, var(--amber) 16%, var(--scree-2))', border: 'color-mix(in srgb, var(--amber) 40%, var(--stone))', color: 'var(--amber)', text: 'waiting' },
  running:  { bg: 'color-mix(in srgb, var(--lichen) 16%, var(--scree-2))', border: 'color-mix(in srgb, var(--lichen) 40%, var(--stone))', color: 'var(--lichen)', text: 'running' },
  idle:     { bg: 'var(--scree-2)', border: 'var(--stone)', color: 'var(--fog-dim)', text: 'idle' },
  // idle to the engine, but its agent has background work running: quieter than running, not resting
  background: { bg: 'var(--scree-2)', border: 'color-mix(in srgb, var(--lichen) 22%, var(--stone))', color: BACKGROUND_COLOR, text: 'working in background' },
  starting: { bg: 'color-mix(in srgb, var(--batch) 16%, var(--scree-2))', border: 'color-mix(in srgb, var(--batch) 40%, var(--stone))', color: 'var(--batch)', text: 'starting' },
  stopped:  { bg: 'var(--scree-2)', border: 'var(--stone)', color: 'var(--fog-dim)', text: 'stopped' },
  error:    { bg: 'color-mix(in srgb, var(--danger) 16%, var(--scree-2))', border: 'color-mix(in srgb, var(--danger) 40%, var(--stone))', color: 'var(--danger)', text: 'error' },
  // cluster pod died; the session resumes when you send it a message
  disconnected: { bg: 'var(--scree-2)', border: 'var(--stone)', color: 'var(--fog-dim)', text: 'paused' },
}

// Where the session runs, in one word. Sessions created before the runtime
// column existed carry no value at all — those get no chip rather than a
// guess, since "Host" is exactly the claim we must not make wrongly.
const RUNTIME_CHIP: Record<string, string> = {
  cluster: 'Cluster',
  docker: 'Sandbox',
  daemon: 'Host',
}

// "" is the legacy spelling of tmux (see protocol SessionInfo.Kind); an
// absent field is unknown and gets no chip.
function kindChipLabel(kind: string | undefined): string | null {
  if (kind === undefined) return null
  // An agent session is the default kind: a chip on every card would say nothing.
  if (kind === 'agent') return null
  if (kind === 'tmux' || kind === '') return 'Terminal'
  return null
}

const chipStyle: React.CSSProperties = {
  color: 'var(--fog-dim)',
  fontSize: '0.64rem',
  flexShrink: 0,
  background: 'var(--basalt)',
  border: '1px solid var(--scree)',
  borderRadius: 4,
  padding: '0 4px', lineHeight: '15px',
  letterSpacing: '0.02em',
}

// The status glyph's slot: one fixed width whatever the state, so every card's name starts at the
// same place (and the chips row under it lines up: STATUS_SLOT + the row's gap).
const STATUS_SLOT = 22
const ROW_GAP = 6

// The ⏱ count, as the glyph and as the chip: past nine it reads "9+". The ⏱ is drawn a size
// smaller than the count (it renders wide): "⏱2" fits the status slot, and "⏱9+" (rare) spills
// 2 px each side of it, into the card's padding and the row's gap, the name staying where it is.
function taskCount(n: number): string {
  return n > 9 ? '9+' : String(n)
}

function TaskCount({ n }: { n: number }) {
  return <><span style={{ fontSize: '0.85em' }}>⏱</span>{n > 0 ? taskCount(n) : ''}</>
}

// The background glyph and the ⏱ chip share one look: the chip's size and spacing, the quiet
// background colour.
const backgroundText: React.CSSProperties = {
  color: BACKGROUND_COLOR,
  fontSize: '0.64rem',
  fontWeight: 600,
  letterSpacing: '-0.02em',
  fontVariantNumeric: 'tabular-nums',
  whiteSpace: 'nowrap',
}

// StatusIndicator renders the per-state glyph that sits at the left of the card:
//   running → animated spinner (it's doing work)
//   waiting → a question mark (it needs an answer)
//   idle    → a snoozing "Zz" (it's resting)
//   background → ⏱ and the count of background tasks (its turn is over, its work is not)
//   other   → the classic coloured dot
// A glyph only: the words are in the title and the accessible name, so the small card stays light.
// Keeps data-testid="status-dot" + title across all variants for accessibility/tests.
function StatusIndicator(props: { status: string; color: string; text: string; count?: number }) {
  return (
    <span data-testid="status-slot" style={{ width: STATUS_SLOT, flexShrink: 0, display: 'inline-flex', justifyContent: 'center', alignItems: 'center' }}>
      <StatusGlyph {...props} />
    </span>
  )
}

function StatusGlyph({ status, color, text, count = 0 }: { status: string; color: string; text: string; count?: number }) {
  const common = { 'data-testid': 'status-dot', title: text } as const

  if (status === 'background') {
    return (
      <span
        {...common}
        role="img"
        aria-label={text}
        data-status="background"
        style={{ ...backgroundText, color, lineHeight: 1, display: 'inline-block' }}
      >
        <TaskCount n={count} />
      </span>
    )
  }

  if (status === 'running') {
    return (
      <span
        {...common}
        className="blerg-status-spinner"
        style={{ color, borderTopColor: color }}
      />
    )
  }

  if (status === 'waiting') {
    return (
      <span
        {...common}
        style={{
          color,
          fontWeight: 800,
          fontSize: '0.95rem',
          lineHeight: 1,
          width: 10,
          textAlign: 'center',
          flexShrink: 0,
          display: 'inline-block',
        }}
      >
        ?
      </span>
    )
  }

  if (status === 'idle') {
    return (
      <span
        {...common}
        style={{
          color,
          fontWeight: 700,
          fontStyle: 'italic',
          fontSize: '0.72rem',
          letterSpacing: '-0.5px',
          lineHeight: 1,
          width: 10,
          textAlign: 'center',
          flexShrink: 0,
          display: 'inline-block',
        }}
      >
        Zz
      </span>
    )
  }

  return (
    <span
      {...common}
      style={{
        width: 8,
        height: 8,
        borderRadius: '50%',
        background: color,
        flexShrink: 0,
        display: 'inline-block',
      }}
    />
  )
}

// Statuses in which a session can have something running in the background.
const LIVE_STATUSES = new Set(['starting', 'running', 'idle', 'waiting'])

function backgroundTitle(n: number): string {
  return n === 1 ? '1 background task running' : `${n} background tasks running`
}

// A session at work: discarding it stops that work, and its confirmation says so.
const WORKING = new Set(['starting', 'running', 'waiting'])

/** What a collapsed parent says of its hidden started sessions, in words (the glyph's tooltip). */
function hiddenChildrenWords(h: { total: number; running: number; waiting: number }): string {
  const parts = [h.waiting ? `${h.waiting} ${h.waiting === 1 ? 'needs' : 'need'} you` : '', h.running ? `${h.running} running` : ''].filter(Boolean)
  const all = `${h.total} started ${h.total === 1 ? 'session' : 'sessions'}`
  return parts.length ? `${parts.join(', ')}, hidden (${all})` : `${all} hidden`
}

export default function SessionCard({ session, active, hiddenChildren = null, childrenControl = null, nested = false }: SessionCardProps) {
  const navigate = useNavigate()
  const openSession = useContext(OpenSessionContext)
  const status = session.status
  // What the card shows: an idle session with background work running reads as "background".
  const shown = displayState(session)
  // A session a daemon restart left paused needs the person as a waiting one does: the same "?",
  // its own words in the tooltip.
  const paused = pausedByRestart(session)
  const glyph = paused ? 'waiting' : shown
  const statusStyle = STATUS_STYLES[glyph] ?? STATUS_STYLES.stopped
  const isWaiting = status === 'waiting'

  // Flash and unread dot driven by server-persisted session.unread.
  const unread = !!session.unread
  const flashing = isWaiting && unread
  // Background tasks its agent left running (the server's count, so it is right for a session
  // nobody has open).
  const bgTasks = LIVE_STATUSES.has(status) ? session.background_tasks ?? 0 : 0
  // In the background state the status glyph carries the count; the chip shows it otherwise.
  const bgChip = shown !== 'background' && bgTasks > 0
  // The session's org/repo in small text, so the list needs no repo headings. Not for a no-repo
  // session, and not for an untitled one: its name already is the repo.
  const repoLine = session.title && !isNoRepo(session.repo) ? session.repo : ''
  // A session with background tasks keeps its second line when the count moves into the status slot,
  // so the card does not change height as its turn ends.
  const hasChildren = (childrenControl?.total ?? 0) > 0
  const leftBorder = active || isWaiting || paused ? 3 : 1
  const hasDetails = hasChildren || !!repoLine || bgTasks > 0 || (status === 'error' && !!session.error_reason)
    || !!(session.runtime && RUNTIME_CHIP[session.runtime]) || !!kindChipLabel(session.kind) || !!session.model || !!session.cron_id || !!session.started_by

  return (
    <div
      data-active={active ? 'true' : undefined}
      className={flashing ? 'blerg-runner-waiting-flash' : undefined}
      onClick={() => {
        if (openSession) openSession(session.id)
        else navigate(`/sessions/${session.id}`)
      }}
      style={{
        // backgroundColor (longhand) so the flash class's background-image survives.
        backgroundColor: active ? 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))' : 'var(--scree)',
        borderRadius: 8,
        padding: '6px 10px',
        marginBottom: 6,
        cursor: 'pointer',
        position: 'relative',
        display: 'flex',
        flexDirection: 'column',
        gap: 3,
        // Selection reads as a blaze (brand accent = "active") left bar + a
        // soft lift — distinct from amber, which means "waiting". The status
        // dot still shows what state the session is in.
        borderLeft: active ? '3px solid var(--blaze)' : isWaiting || paused ? `3px solid ${statusStyle.border}` : `1px solid ${statusStyle.border}`,
        borderTop: `1px solid ${statusStyle.border}`,
        borderRight: `1px solid ${statusStyle.border}`,
        borderBottom: `1px solid ${statusStyle.border}`,
        boxShadow: active ? '0 0 0 1px var(--blaze-soft), 0 2px 10px rgba(0,0,0,0.45)' : undefined,
      }}
    >
      {/* Row 1: status indicator + name (+ unread dot, star). The name gets
          the whole line so it stays readable in the narrow sidebar. */}
      <div style={{ display: 'flex', alignItems: 'center', gap: ROW_GAP, minWidth: 0 }}>
      {/* Status indicator — spinner / "?" / "Zz" / "⏱ n" / dot depending on state */}
      <StatusIndicator status={glyph} color={statusStyle.color} text={paused ? PAUSED_WORDS : displayLabel(shown, bgTasks)} count={bgTasks} />

      {/* Session name */}
      <span style={{
        color: 'var(--chalk)',
        fontWeight: 600,
        fontSize: '0.9rem',
        flex: 1,
        minWidth: 0,
        overflow: 'hidden',
        textOverflow: 'ellipsis',
        whiteSpace: 'nowrap',
      }}>
        {session.title || repoLabel(session.repo)}
      </span>

      {/* Its started sessions are collapsed: one glyph says what is behind them (amber "?" when one
          needs you, a running dot when one runs), the count beside it; the words are the tooltip. */}
      {/* Its started sessions: "↳ N", plain information. Collapsed, one glyph also says what is behind
          them (amber "?" when one needs you, a running dot when one runs); the words are the tooltip. */}
      {(hiddenChildren?.total ?? childrenControl?.total ?? 0) > 0 && (() => {
        const h = hiddenChildren ?? { total: childrenControl!.total, running: 0, waiting: 0 }
        const words = hiddenChildren ? hiddenChildrenWords(h) : `${h.total} started ${h.total === 1 ? 'session' : 'sessions'}`
        return (
          <span data-testid="hidden-children" role="img" aria-label={words} title={words}
            data-state={h.waiting ? 'waiting' : h.running ? 'running' : 'idle'}
            style={{ ...chipStyle, color: h.waiting ? 'var(--amber)' : h.running ? 'var(--lichen)' : 'var(--fog-dim)' }}>
            ↳{h.waiting ? '?' : h.running ? '●' : ''} {h.total}
          </span>
        )
      })()}
      {/* Unread dot — shown whenever session.unread is true, regardless of status */}
      {unread && (
        <span
          data-testid="unread-dot"
          style={{
            width: 7,
            height: 7,
            borderRadius: '50%',
            background: 'var(--blaze)',
            flexShrink: 0,
            display: 'inline-block',
            marginLeft: 'auto',
          }}
        />
      )}

      {!session.archived_at && (session.parent_session_id ? <ChildSessionActions parentId={session.parent_session_id} childId={session.id} title={session.title || 'Untitled session'} running={WORKING.has(status)}/> : <ParentSessionActions sessionId={session.id} title={session.title || 'Untitled session'} running={WORKING.has(status)}/>)}
      {/* Star toggle — always visible, outline when unstarred, filled orange when starred */}
      <button
        data-testid="star-toggle"
        data-starred={session.starred ? 'true' : 'false'}
        onClick={(e) => {
          e.stopPropagation()
          send({ type: 'set_session_star', session_id: session.id, starred: !session.starred })
        }}
        style={{
          background: 'none',
          border: 'none',
          cursor: 'pointer',
          padding: 0,
          flexShrink: 0,
          fontSize: '0.9rem',
          lineHeight: 1,
          color: session.starred ? 'var(--blaze)' : 'var(--fog-dim)',
        }}
      >
        {session.starred ? '★' : '☆'}
      </button>
      </div>

      {/* Row 2, the last: the org/repo, failure reason and the small chips (where/how it runs,
          model) on ONE line that never wraps, so a card is two lines at most; the repo and a
          failure reason give way (ellipsis), the chips keep their size. */}
      {hasDetails && <div style={{ display: 'flex', flexWrap: 'nowrap', overflow: 'hidden', alignItems: 'center', gap: 4, minWidth: 0, minHeight: 15, paddingLeft: STATUS_SLOT + ROW_GAP }}>
      {repoLine && (
        <span
          data-testid="session-repo"
          title={repoLine}
          style={{
            color: 'var(--fog-dim)',
            fontSize: '0.7rem',
            // The one thing that gives way (the full name is in the title): the chips keep their size.
            flex: '0 1 auto',
            minWidth: 0,
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
            marginRight: 2,
          }}
        >
          {repoLine}
        </span>
      )}
      {/* Background tasks running — a long watch is otherwise invisible from the list */}
      {bgChip && (
        <span
          data-testid="background-chip"
          title={backgroundTitle(bgTasks)}
          aria-label={backgroundTitle(bgTasks)}
          style={{ ...chipStyle, ...backgroundText, borderColor: `color-mix(in srgb, ${BACKGROUND_COLOR} 40%, var(--scree))` }}
        >
          <TaskCount n={bgTasks} />
        </span>
      )}

      {/* Spawn/run failure reason — second line, only on failed sessions */}
      {status === 'error' && session.error_reason && (
        <span
          data-testid="error-reason"
          title={session.error_reason}
          style={{
            color: 'var(--danger)',
            fontSize: '0.72rem',
                        overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
          }}
        >
          {session.error_reason}
        </span>
      )}

      {/* Where and how it runs — the two facts the launch sheet now makes
          explicit, carried through to the list so a Host session is never a
          silent one. */}
      {session.cron_id && (
        <span data-testid="cron-badge" title="Started by a cron" style={{ ...chipStyle, color: 'var(--amber)', borderColor: 'var(--amber)' }}>
          cron
        </span>
      )}
      {session.started_by && session.started_by.kind !== 'cron' && !session.cron_id && !(nested && session.started_by.kind === 'session') && (
        <span data-testid="started-by-chip" title={`Started by ${startedByLabel(session)}`} style={{ ...chipStyle, color: 'var(--amber)', borderColor: 'var(--amber)' }}>
          {startedByLabel(session)}
        </span>
      )}
      {session.runtime && RUNTIME_CHIP[session.runtime] && (
        <span data-testid="runtime-chip" style={chipStyle}>
          {RUNTIME_CHIP[session.runtime]}
        </span>
      )}
      {kindChipLabel(session.kind) && (
        <span data-testid="kind-chip" style={chipStyle}>
          {kindChipLabel(session.kind)}
        </span>
      )}

      {/* Compact model chip — effort is shown in SessionDetail, not here */}
      {session.model && (
        <span
          data-testid="model-chip"
          title={session.model}
          style={{
            color: 'var(--fog-dim)',
            fontSize: '0.62rem',
            fontFamily: 'monospace',
            flexShrink: 0,
            background: 'var(--basalt)',
            border: '1px solid var(--scree)',
            borderRadius: 4,
            padding: '0 4px', lineHeight: '15px',
          }}
        >
          {modelDisplayName(session.model)}
        </span>
      )}

      </div>}
      {hasChildren && (() => {
        const n = childrenControl!.total
        const label = `${childrenControl!.shown ? 'Hide' : 'Show'} ${n} started ${n === 1 ? 'session' : 'sessions'}`
        // Centered on the nested list's indent line (21.5 px in from the branch's left edge), whatever
        // the card's left border is (SessionList: marginLeft 21 + a 1 px line).
        return (
          <button type="button" className="session-children-toggle" data-testid="children-toggle" aria-expanded={childrenControl!.shown}
            aria-label={label} title={label} style={{ left: 14 - leftBorder }}
            onClick={e => { e.stopPropagation(); e.preventDefault(); childrenControl!.toggle() }}>
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              {childrenControl!.shown ? <path d="M6 15l6-6 6 6" /> : <path d="M6 9l6 6 6-6" />}
            </svg>
          </button>
        )
      })()}
    </div>
  )
}
