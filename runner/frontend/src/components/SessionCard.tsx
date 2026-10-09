import { useNavigate } from 'react-router-dom'
import type { SessionInfo, DaemonInfo } from '../types'
import { send } from '../ws'
import { repoLabel } from '../lib/noRepo'
import { startedByLabel } from '../lib/startedBy'

interface Props {
  session: SessionInfo
  daemon: DaemonInfo | undefined
  active?: boolean
}

const STATUS_STYLES: Record<string, { bg: string; border: string; color: string; text: string }> = {
  waiting:  { bg: 'color-mix(in srgb, var(--amber) 16%, var(--scree-2))', border: 'color-mix(in srgb, var(--amber) 40%, var(--stone))', color: 'var(--amber)', text: 'waiting' },
  running:  { bg: 'color-mix(in srgb, var(--lichen) 16%, var(--scree-2))', border: 'color-mix(in srgb, var(--lichen) 40%, var(--stone))', color: 'var(--lichen)', text: 'running' },
  idle:     { bg: 'var(--scree-2)', border: 'var(--stone)', color: 'var(--fog-dim)', text: 'idle' },
  starting: { bg: 'color-mix(in srgb, var(--batch) 16%, var(--scree-2))', border: 'color-mix(in srgb, var(--batch) 40%, var(--stone))', color: 'var(--batch)', text: 'starting' },
  stopped:  { bg: 'var(--scree-2)', border: 'var(--stone)', color: 'var(--fog-dim)', text: 'stopped' },
  error:    { bg: 'color-mix(in srgb, var(--danger) 16%, var(--scree-2))', border: 'color-mix(in srgb, var(--danger) 40%, var(--stone))', color: 'var(--danger)', text: 'error' },
  // cluster pod died; the session resumes when you send it a message
  disconnected: { bg: 'var(--scree-2)', border: 'var(--stone)', color: 'var(--fog-dim)', text: 'paused' },
}

// Abbreviates a model ID to a compact display name, e.g. "claude-sonnet-4-6" → "sonnet-4-6"
function shortModel(model: string): string {
  return model.replace(/^claude-/, '')
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
  if (kind === 'agent') return 'Agent'
  if (kind === 'tmux' || kind === '') return 'Terminal'
  return null
}

const chipStyle: React.CSSProperties = {
  color: 'var(--fog-dim)',
  fontSize: '0.68rem',
  flexShrink: 0,
  background: 'var(--basalt)',
  border: '1px solid var(--scree)',
  borderRadius: 4,
  padding: '1px 5px',
  letterSpacing: '0.04em',
}

// StatusIndicator renders the per-state glyph that sits at the left of the card:
//   running → animated spinner (it's doing work)
//   waiting → a question mark (it needs an answer)
//   idle    → a snoozing "Zz" (it's resting)
//   other   → the classic coloured dot
// Keeps data-testid="status-dot" + title across all variants for accessibility/tests.
function StatusIndicator({ status, color, text }: { status: string; color: string; text: string }) {
  const common = { 'data-testid': 'status-dot', title: text } as const

  if (status === 'running') {
    return (
      <span
        {...common}
        className="blerg-status-spinner"
        style={{ borderTopColor: color }}
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

export default function SessionCard({ session, active }: Props) {
  const navigate = useNavigate()
  const status = session.status
  const statusStyle = STATUS_STYLES[status] ?? STATUS_STYLES.stopped
  const isWaiting = status === 'waiting'

  // Flash and unread dot driven by server-persisted session.unread.
  const unread = !!session.unread
  const flashing = isWaiting && unread
  const hasDetails = (status === 'error' && !!session.error_reason)
    || !!(session.runtime && RUNTIME_CHIP[session.runtime]) || !!kindChipLabel(session.kind) || !!session.model || !!session.cron_id || !!session.started_by

  return (
    <div
      data-active={active ? 'true' : undefined}
      className={flashing ? 'blerg-runner-waiting-flash' : undefined}
      onClick={() => {
        navigate(`/sessions/${session.id}`)
      }}
      style={{
        // backgroundColor (longhand) so the flash class's background-image survives.
        backgroundColor: active ? 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))' : 'var(--scree)',
        borderRadius: 8,
        padding: '6px 10px',
        marginBottom: 6,
        cursor: 'pointer',
        display: 'flex',
        flexDirection: 'column',
        gap: 3,
        // Selection reads as a blaze (brand accent = "active") left bar + a
        // soft lift — distinct from amber, which means "waiting". The status
        // dot still shows what state the session is in.
        borderLeft: active ? '3px solid var(--blaze)' : isWaiting ? `3px solid ${statusStyle.border}` : `1px solid ${statusStyle.border}`,
        borderTop: `1px solid ${statusStyle.border}`,
        borderRight: `1px solid ${statusStyle.border}`,
        borderBottom: `1px solid ${statusStyle.border}`,
        boxShadow: active ? '0 0 0 1px var(--blaze-soft), 0 2px 10px rgba(0,0,0,0.45)' : undefined,
      }}
    >
      {/* Row 1: status indicator + name (+ unread dot, star). The name gets
          the whole line so it stays readable in the narrow sidebar. */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
      {/* Status indicator — spinner / "?" / "Zz" / dot depending on state */}
      <StatusIndicator status={status} color={statusStyle.color} text={statusStyle.text} />

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

      {/* Row 2: failure reason and the small chips (where/how it runs, model).
          Text is deliberately small so the card grows by one compact line. */}
      {hasDetails && <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 4, minWidth: 0, paddingLeft: 16 }}>
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
      {session.started_by && session.started_by.kind !== 'cron' && !session.cron_id && (
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
          style={{
            color: 'var(--fog-dim)',
            fontSize: '0.66rem',
            fontFamily: 'monospace',
            flexShrink: 0,
            background: 'var(--basalt)',
            border: '1px solid var(--scree)',
            borderRadius: 4,
            padding: '1px 5px',
          }}
        >
          {shortModel(session.model)}
        </span>
      )}

      </div>}
    </div>
  )
}
