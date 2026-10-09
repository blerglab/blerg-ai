import { useParams, useNavigate } from 'react-router-dom'
import { isAutomated, startedByLabel } from '../lib/startedBy'
import { useRef, useState, useEffect, useMemo } from 'react'
import { useSessionStore } from '../hooks/useSessionStore'
import { useToastStore } from '../hooks/useToastStore'
import { useMessageStore } from '../hooks/useMessageStore'
import { useIsMobile } from '../hooks/useIsMobile'
import { send } from '../ws'
import { arrowSeq, KEY_SEQ, type ArrowDir } from '../terminalKeys'
import { sessionViewportBox } from '../terminalRenderer'
import TerminalDOMView, { type TerminalHandle } from './TerminalDOMView'
import AgentChatView from './AgentChatView'
import { MessageThread } from './MessageThread'
import { apiFetch } from '../apiFetch'
import { StartProgress, placeholderAttempt, describeSessionEnd } from '@blerglab/chat'
import CapabilitiesPanel from './CapabilitiesPanel'
import { isNoRepo, repoLabel } from '../lib/noRepo'

// PendingStart is the start panel shown before the server has announced the
// session; its clock starts when it first shows.
function PendingStart() {
  const [since] = useState(() => Date.now())
  return <StartProgress attempt={placeholderAttempt(since)} />
}

export default function SessionDetail() {
  const { id: sessionId } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const isMobile = useIsMobile()

  const session = useSessionStore(s => s.sessions.find(sess => sess.id === sessionId))
  const daemon = useSessionStore(s => s.daemons.find(d => d.id === session?.daemon_id))
  const pendingSessionIds = useSessionStore(s => s.pendingSessionIds)
  const failedSessions = useSessionStore(s => s.failedSessions)
  const clearFailedSession = useSessionStore(s => s.clearFailedSession)

  const addToast = useToastStore(s => s.addToast)

  // Opening a session marks it as read on the server — clears the unread indicator.
  useEffect(() => {
    if (sessionId) send({ type: 'mark_session_read', session_id: sessionId })
  }, [sessionId])

  const termRef = useRef<TerminalHandle | null>(null)
  const rootRef = useRef<HTMLDivElement | null>(null)

  // Pin the mobile session container to the VISUAL viewport while the soft
  // keyboard is open. Android keeps the layout viewport full-height when the
  // keyboard opens, so with plain `inset:0` the key bar and the terminal's
  // bottom rows sit behind the keyboard. Tracking vv.height/offsetTop keeps
  // the whole flex column — header, terminal, key bar — above the keyboard,
  // and restoring inset:0 on close cannot go stale (no derived px height).
  // Styles are set imperatively to avoid re-rendering the terminal on every
  // viewport event. This effect runs AFTER TerminalDOMView's own vv listener
  // registers (child effects run first), so the terminal's kbRef guard is in
  // place before the container resize triggers its ResizeObserver.
  useEffect(() => {
    if (!isMobile) return
    const vv = window.visualViewport
    if (!vv) return
    const apply = () => {
      // Read the ref per event: the pending/error views render without it, so
      // the root may not exist yet when this effect first runs.
      const root = rootRef.current
      if (!root) return
      const box = sessionViewportBox(true, window.innerHeight, vv.height, vv.offsetTop)
      if (box) {
        root.style.top = `${box.top}px`
        root.style.height = `${box.height}px`
        root.style.bottom = 'auto'
      } else {
        root.style.top = '0'
        root.style.height = ''
        root.style.bottom = '0'
      }
    }
    vv.addEventListener('resize', apply)
    vv.addEventListener('scroll', apply)
    apply()
    return () => {
      vv.removeEventListener('resize', apply)
      vv.removeEventListener('scroll', apply)
    }
  }, [isMobile])

  const [activeTab, setActiveTab] = useState<'terminal' | 'chat'>('terminal')
  const allMessages = useMessageStore(s => s.messages)
  const sessionMessages = useMemo(
    () => allMessages.filter(m => m.session_id === sessionId),
    [allMessages, sessionId],
  )
  const [editing, setEditing] = useState(false)
  const [editValue, setEditValue] = useState('')
  const [saving, setSaving] = useState(false)
  const [killArmed, setKillArmed] = useState(false)
  const [showCaps, setShowCaps] = useState(false)
  const killTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  function sendKey(seq: string) {
    if (!sessionId) return
    send({ type: 'send_input', session_id: sessionId, data: btoa(seq) })
  }

  function sendArrow(dir: ArrowDir) {
    const app = !!termRef.current?.applicationCursorKeysMode
    sendKey(arrowSeq(dir, app))
  }

  // Blurs the focused element so Android doesn't open the soft keyboard when
  // tapping a control button (the hidden textarea stays focused between taps but
  // a touch-with-focused-textarea can trigger the keyboard without this).
  function blurKeyboard() {
    (document.activeElement as HTMLElement | null)?.blur()
  }

  if (!session) {
    const isFailed = sessionId !== undefined && sessionId in failedSessions
    const isPending = sessionId !== undefined && sessionId in pendingSessionIds

    if (isFailed) {
      const message = failedSessions[sessionId!] || 'Session failed to start.'
      return (
        <div style={{ padding: '24px', color: 'var(--chalk)' }}>
          <div style={{ color: 'var(--danger)', marginBottom: 16 }}>{message}</div>
          <button
            onClick={() => { clearFailedSession(sessionId!); navigate('/sessions') }}
            style={{ background: 'none', border: 'none', color: 'var(--blaze)', cursor: 'pointer', fontSize: 16, fontFamily: 'inherit' }}
          >
            Back to home
          </button>
        </div>
      )
    }

    if (isPending) {
      // The start request is in flight and the server has not announced the
      // session yet (it does so the moment the row exists) — show the same
      // panel the session will continue in, not a bare "Starting…".
      return (
        <div style={{ padding: '16px', color: 'var(--chalk)' }}>
          {/* Keyed by id: each pending start gets its own clock. */}
          <PendingStart key={sessionId} />
        </div>
      )
    }

    return (
      <div style={{ padding: '24px', color: 'var(--chalk)' }}>
        <button
          onClick={() => navigate('/sessions')}
          style={{ background: 'none', border: 'none', color: 'var(--blaze)', cursor: 'pointer', fontSize: 16, marginBottom: 16, fontFamily: 'inherit' }}
        >
          ← Back
        </button>
        <div>Session not found</div>
      </div>
    )
  }

  // disconnected (cluster pod gone) still shows the kill button — the server
  // deletes the Job and marks the session stopped.
  const isActive = session.status === 'running' || session.status === 'idle' || session.status === 'waiting' || session.status === 'starting' || session.status === 'disconnected'
  const isAgent = session.kind === 'agent'
  // The Messages tab exists for terminal sessions only (see the tab toggle below).
  const showMessages = !isAgent && activeTab === 'chat'
  const endLine = describeSessionEnd(session)

  // A live cluster session can be paused: its pod is freed, the session stays and a message resumes it.
  const canPause = session.runtime === 'cluster' && ['running', 'idle', 'waiting'].includes(session.status)

  async function handlePauseClick() {
    if (!sessionId) return
    try {
      const res = await apiFetch(`/api/sessions/${sessionId}/pause`, { method: 'POST' })
      if (!res.ok) addToast({ title: 'Pause failed', body: `Server returned ${res.status}. The session is still running.` })
    } catch {
      addToast({ title: 'Pause failed', body: 'Network error. The session is still running.' })
    }
  }

  async function handleKillClick() {
    if (!sessionId) return
    if (killArmed) {
      if (killTimerRef.current) clearTimeout(killTimerRef.current)
      setKillArmed(false)
      try {
        const res = await apiFetch(`/api/sessions/${sessionId}`, { method: 'DELETE' })
        if (res.ok) {
          navigate('/sessions')
        } else {
          addToast({ title: 'Kill failed', body: `Server returned ${res.status}. The session may still be running.` })
        }
      } catch {
        addToast({ title: 'Kill failed', body: 'Network error. The session may still be running.' })
      }
    } else {
      setKillArmed(true)
      killTimerRef.current = setTimeout(() => setKillArmed(false), 3000)
    }
  }


  return (
    <div ref={rootRef} style={{ display: 'flex', flexDirection: 'column', ...(isMobile ? { position: 'fixed', inset: 0 } : { height: '100%' }), background: 'var(--basalt)', color: 'var(--chalk)' }}>
      {/* Header */}
      <div style={{ padding: '12px 16px', borderBottom: '1px solid var(--stone)', display: 'flex', alignItems: 'center', gap: 12, background: 'var(--basalt)' }}>
        {isMobile && (
          <button
            onClick={() => navigate('/sessions')}
            style={{ background: 'none', border: 'none', color: 'var(--blaze)', cursor: 'pointer', fontSize: 18, lineHeight: 1, padding: 0, fontFamily: 'inherit' }}
          >
            ←
          </button>
        )}
        <div style={{ flex: 1, minWidth: 0 }}>
          {editing ? (
            <input
              value={editValue}
              onChange={e => setEditValue(e.target.value)}
              autoFocus
              disabled={saving}
              onKeyDown={async e => {
                if (e.key === 'Enter' && !e.shiftKey) {
                  e.preventDefault()
                  const trimmed = editValue.trim()
                  if (!trimmed) { setEditing(false); return }
                  if (trimmed === (session.title || session.repo)) { setEditing(false); return }
                  setSaving(true)
                  try {
                    await apiFetch(`/api/sessions/${sessionId}`, {
                      method: 'PATCH',
                      headers: { 'Content-Type': 'application/json' },
                      body: JSON.stringify({ title: trimmed }),
                    })
                    setSaving(false)
                    setEditing(false)
                  } catch {
                    setSaving(false)
                  }
                } else if (e.key === 'Escape') {
                  setEditing(false)
                }
              }}
              onBlur={async () => {
                const trimmed = editValue.trim()
                if (!trimmed || trimmed === (session.title || session.repo)) { setEditing(false); return }
                setSaving(true)
                try {
                  await apiFetch(`/api/sessions/${sessionId}`, {
                    method: 'PATCH',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ title: trimmed }),
                  })
                  setSaving(false)
                  setEditing(false)
                } catch {
                  setSaving(false)
                }
              }}
              style={{ fontWeight: 600, fontSize: 15, color: 'var(--chalk)', background: 'transparent', border: 'none', borderBottom: '1px solid var(--blaze)', outline: 'none', width: '100%', fontFamily: 'inherit' }}
            />
          ) : (
            <div
              onClick={() => { setEditing(true); setEditValue(session.title || session.repo || '') }}
              style={{ fontWeight: 600, fontSize: 15, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', color: 'var(--chalk)', cursor: 'text' }}
            >
              {session.title || repoLabel(session.repo)}
            </div>
          )}
          <div style={{ fontSize: 12, color: 'var(--fog)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {isAutomated(session) && <span data-testid="detail-started-by">started by {startedByLabel(session)} · </span>}
            {/* Fixed at start; an interactive session is the ordinary case and goes unmarked. */}
            {session.interaction === 'unattended' && (
              <span data-testid="detail-unattended" title="Started unattended: the agent does not wait for you in the chat">Unattended · </span>
            )}
            {daemon?.name ?? session.daemon_id} · {session.project_path}
            {session.model ? ` · ${session.model.replace(/^claude-/, '')}` : ''}
            {session.effort ? ` · ${session.effort}` : ''}
          </div>
        </div>
        <button
          data-testid="star-toggle"
          data-starred={session.starred ? 'true' : 'false'}
          onClick={() => send({ type: 'set_session_star', session_id: session.id, starred: !session.starred })}
          style={{
            background: 'none',
            border: 'none',
            cursor: 'pointer',
            padding: 0,
            flexShrink: 0,
            fontSize: '1rem',
            lineHeight: 1,
            color: session.starred ? 'var(--blaze)' : 'var(--fog-dim)',
            fontFamily: 'inherit',
          }}
        >
          {session.starred ? '★' : '☆'}
        </button>
        {/* Agent sessions have this in their chat toolbar; a terminal session
            has no structured report to show, and says so. */}
        {!isAgent && (
          <button
            type="button"
            data-testid="capabilities-toggle-terminal"
            aria-haspopup="dialog"
            aria-label="Skills & plugins"
            title="Skills & plugins"
            onClick={() => setShowCaps(true)}
            style={{ background: 'none', border: 'none', cursor: 'pointer', padding: 0, flexShrink: 0, fontSize: '1rem', lineHeight: 1, color: 'var(--fog-dim)', fontFamily: 'inherit' }}
          >
            🧩
          </button>
        )}
        {showCaps && (
          <CapabilitiesPanel
            report={null}
            unavailable="Not available for terminal sessions. A terminal session runs the engine's interactive CLI, which doesn't report what it loaded; start an agent session to see its skills, plugins and MCP servers here."
            onClose={() => setShowCaps(false)}
          />
        )}
        {!isMobile && !isAgent && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
            {(['up', 'down', 'left', 'right'] as const).map(dir => (
              <button
                key={dir}
                type="button"
                onClick={() => sendArrow(dir)}
                style={{
                  background: 'var(--scree)',
                  border: '1px solid var(--stone)',
                  borderRadius: 5,
                  color: 'var(--chalk)',
                  cursor: 'pointer',
                  fontSize: 13,
                  fontFamily: 'inherit',
                  padding: '3px 7px',
                  minWidth: 28,
                  textAlign: 'center',
                }}
              >
                {({ up: '↑', down: '↓', left: '←', right: '→' } as const)[dir]}
              </button>
            ))}
            {([
              { label: '⏎', seq: KEY_SEQ.enter },
              { label: 'esc', seq: KEY_SEQ.esc },
              { label: 'tab', seq: KEY_SEQ.tab },
            ] as const).map(({ label, seq }) => (
              <button
                key={label}
                type="button"
                onClick={() => sendKey(seq)}
                style={{
                  background: 'var(--scree)',
                  border: '1px solid var(--stone)',
                  borderRadius: 5,
                  color: 'var(--chalk)',
                  cursor: 'pointer',
                  fontSize: 12,
                  fontFamily: 'inherit',
                  padding: '3px 7px',
                  minWidth: 28,
                  textAlign: 'center',
                }}
              >
                {label}
              </button>
            ))}
          </div>
        )}
        {canPause && (
          <button
            onClick={handlePauseClick}
            title="Free the pod but keep the session — send a message to resume it"
            style={{
              background: 'var(--basalt)',
              border: '1px solid var(--scree)',
              borderRadius: 6,
              color: 'var(--chalk)',
              cursor: 'pointer',
              fontSize: 13,
              fontWeight: 700,
              padding: '4px 10px',
              fontFamily: 'inherit',
              whiteSpace: 'nowrap',
            }}
          >
            ⏸ Pause
          </button>
        )}
        {isActive && (
          <button
            onClick={handleKillClick}
            title={killArmed ? 'Click again to confirm' : 'Kill session'}
            style={{
              background: killArmed ? 'color-mix(in srgb, var(--danger) 35%, var(--scree))' : 'color-mix(in srgb, var(--danger) 22%, var(--basalt))',
              border: `1px solid ${killArmed ? 'var(--danger)' : 'color-mix(in srgb, var(--danger) 35%, var(--scree))'}`,
              borderRadius: 6,
              color: 'var(--danger)',
              cursor: 'pointer',
              fontSize: 13,
              fontWeight: 700,
              padding: '4px 10px',
              fontFamily: 'inherit',
              whiteSpace: 'nowrap',
              transition: 'background 0.15s, border-color 0.15s',
            }}
          >
            {killArmed ? '✕ Confirm kill?' : '✕ Kill'}
          </button>
        )}
      </div>

      {/* Posture banner — states plainly how this session runs and whether
          permission prompts are bypassed (R5); persistent, not dismissible. */}
      <div data-testid="posture-banner" style={{ fontSize: 12, color: 'var(--amber)', padding: '4px 12px', borderBottom: '1px solid var(--stone)', background: 'color-mix(in srgb, var(--amber) 8%, var(--basalt))' }}>
        {/* Where it runs decides the claim, so runtime is checked before
            kind: an agent session in a pod or the sandbox is not
            "unsandboxed on" anything. */}
        {session.runtime === 'cluster'
          ? `Cluster session — runs in a Kubernetes pod with your (or the cluster's) credentials${isAgent ? ', no permission prompts' : ''}`
          : session.runtime === 'docker'
            ? `Docker sandbox — ${isNoRepo(session.repo) ? 'scratch folder' : 'repo'} mounted at /workspace; your ~/.claude is shared${isAgent || session.skip_permissions ? ' · permission prompts bypassed' : ''}`
            : isAgent
              ? `Agent session — runs unsandboxed on ${daemon?.name ?? session.daemon_id} as you, no permission prompts, using your engine login`
              : `Runs directly on ${daemon?.name ?? session.daemon_id} as you, using your engine login`}
      </div>

      {/* Why a finished session ended ("Ended by you", "The process exited
          on its own", ...). A failure the error banner below already explains
          is left to the banner; a session with no recorded reason (it ended
          before the runner kept them) shows nothing new. */}
      {endLine && !endLine.coveredByError && (
        <div data-testid="end-reason" style={{ fontSize: 12, color: 'var(--fog)', padding: '4px 12px', borderBottom: '1px solid var(--stone)' }}>
          {endLine.text}
        </div>
      )}

      {/* Spawn/run failure reason + a way to clear the row once the daemon
          that owned it is gone (trial blocker 2 / R7). */}
      {session.status === 'error' && session.error_reason && (
        <div data-testid="error-reason" style={{ color: 'var(--danger)', padding: '6px 12px', fontSize: 12, borderBottom: '1px solid var(--stone)', background: 'color-mix(in srgb, var(--danger) 8%, var(--basalt))', display: 'flex', alignItems: 'center', gap: 12 }}>
          <span style={{ flex: 1, minWidth: 0, overflowWrap: 'break-word' }}>{session.error_reason}</span>
          <button
            onClick={async () => {
              try {
                const res = await apiFetch(`/api/sessions/${sessionId}`, { method: 'DELETE' })
                if (res.ok) {
                  navigate('/sessions')
                } else {
                  addToast({ title: 'Delete failed', body: `Server returned ${res.status}.` })
                }
              } catch {
                addToast({ title: 'Delete failed', body: 'Network error.' })
              }
            }}
            style={{
              background: 'color-mix(in srgb, var(--danger) 22%, var(--basalt))',
              border: '1px solid color-mix(in srgb, var(--danger) 35%, var(--scree))',
              borderRadius: 6,
              color: 'var(--danger)',
              cursor: 'pointer',
              fontSize: 12,
              fontWeight: 600,
              padding: '3px 9px',
              fontFamily: 'inherit',
              whiteSpace: 'nowrap',
              flexShrink: 0,
            }}
          >
            Delete session
          </button>
        </div>
      )}

      {/* Tab toggle — Terminal / Messages. Terminal sessions only: an agent session shows its
          asks, updates and notes in its own conversation and is answered from its composer, so
          it has one view and no tabs. A terminal has no conversation, so the messages its
          `blerg-runner ask/update/note` sends (and the box to answer an ask) are a tab. */}
      {!isAgent && <div style={{ display: 'flex', borderBottom: '1px solid var(--stone)', background: 'var(--basalt)', flexShrink: 0 }}>
        <button
          data-testid="tab-terminal"
          onClick={() => setActiveTab('terminal')}
          style={{
            flex: 1,
            padding: '7px 16px',
            background: 'none',
            border: 'none',
            borderBottom: activeTab === 'terminal' ? '2px solid var(--blaze)' : '2px solid transparent',
            color: activeTab === 'terminal' ? 'var(--chalk)' : 'var(--fog-dim)',
            cursor: 'pointer',
            fontSize: 13,
            fontFamily: 'inherit',
            fontWeight: activeTab === 'terminal' ? 600 : 400,
          }}
        >
          Terminal
        </button>
        <button
          data-testid="tab-chat"
          onClick={() => setActiveTab('chat')}
          style={{
            flex: 1,
            padding: '7px 16px',
            background: 'none',
            border: 'none',
            borderBottom: activeTab === 'chat' ? '2px solid var(--blaze)' : '2px solid transparent',
            color: activeTab === 'chat' ? 'var(--chalk)' : 'var(--fog-dim)',
            cursor: 'pointer',
            fontSize: 13,
            fontFamily: 'inherit',
            fontWeight: activeTab === 'chat' ? 600 : 400,
          }}
        >
          Messages
        </button>
      </div>}

      {/* Terminal / agent region — always mounted; hidden when the Messages tab is active */}
      <div style={{ position: 'relative', flex: 1, overflow: 'hidden', minHeight: 0, ...(showMessages ? { display: 'none' } : {}) }}>
        {isAgent
          // Keyed by id: draft, pending turn and notices belong to one
          // session and must not carry over when the route switches sessions.
          ? <AgentChatView key={session.id} session={session} />
          : <TerminalDOMView sessionId={sessionId ?? ''} ref={termRef} />}
      </div>

      {/* Messages — only rendered when its tab is active */}
      {showMessages && (
        <div style={{ flex: 1, overflow: 'auto', minHeight: 0, background: 'var(--basalt)' }}>
          <MessageThread messages={sessionMessages} hideSessionLabel />
        </div>
      )}

      {/* Docked key row (mobile only) — terminal sessions only */}
      {isMobile && !isAgent && (
        <div style={{
          display: 'flex',
          alignItems: 'center',
          gap: 6,
          padding: '6px 10px',
          borderTop: '1px solid var(--stone)',
          background: 'var(--basalt)',
          flexWrap: 'wrap',
        }}>
          {(['up', 'down', 'left', 'right'] as const).map(dir => (
            <button
              key={dir}
              type="button"
              onPointerDown={e => { e.preventDefault(); blurKeyboard(); sendArrow(dir) }}
              style={{
                background: 'var(--scree)',
                border: '1px solid var(--stone)',
                borderRadius: 6,
                color: 'var(--chalk)',
                cursor: 'pointer',
                fontSize: 16,
                fontFamily: 'inherit',
                padding: '6px 10px',
                minWidth: 40,
                textAlign: 'center',
              }}
            >
              {({ up: '↑', down: '↓', left: '←', right: '→' } as const)[dir]}
            </button>
          ))}
          {([
            { label: '⏎', seq: KEY_SEQ.enter },
            { label: 'esc', seq: KEY_SEQ.esc },
            { label: 'tab', seq: KEY_SEQ.tab },
          ] as const).map(({ label, seq }) => (
            <button
              key={label}
              type="button"
              onPointerDown={e => { e.preventDefault(); blurKeyboard(); sendKey(seq) }}
              style={{
                background: 'var(--scree)',
                border: '1px solid var(--stone)',
                borderRadius: 6,
                color: 'var(--chalk)',
                cursor: 'pointer',
                fontSize: 13,
                fontFamily: 'inherit',
                padding: '6px 10px',
                minWidth: 40,
                textAlign: 'center',
              }}
            >
              {label}
            </button>
          ))}
        </div>
      )}

    </div>
  )
}
