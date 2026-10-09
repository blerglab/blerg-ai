import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useSessionStore } from '../hooks/useSessionStore'
import { useToastStore } from '../hooks/useToastStore'
import type { MessageInfo } from '../types'
import { apiFetch } from '../apiFetch'
import { Markdown, TimeLabel } from '@blerglab/chat'
import { repoLabel } from '../lib/noRepo'

interface MessageBubbleProps {
  message: MessageInfo
  hideSessionLabel?: boolean
}

const KIND_COLORS: Record<string, string> = {
  update: 'var(--fog-dim)',
  ask: 'var(--amber)',
  note: 'var(--lichen)',
}

function MessageBubble({ message, hideSessionLabel }: MessageBubbleProps) {
  const navigate = useNavigate()
  const session = useSessionStore(s => s.sessions.find(sess => sess.id === message.session_id))
  const sessionLabel = session?.title || (session ? repoLabel(session.repo) : '') || message.session_id

  const [answer, setAnswer] = useState('')
  const [sending, setSending] = useState(false)
  const addToast = useToastStore(s => s.addToast)

  const isOpen = message.status === 'open'
  const kindColor = KIND_COLORS[message.kind] ?? 'var(--fog-dim)'
  // A real reply: answered + non-empty string (not null, not '')
  const hasReply = typeof message.answer === 'string' && message.answer.length > 0

  async function handleSend() {
    if (!answer.trim() || sending) return
    setSending(true)
    try {
      const response = await apiFetch(`/api/messages/${message.id}/reply`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ answer }),
      })
      if (!response.ok) {
        addToast({ title: 'Reply failed', body: `Server returned ${response.status}. Your answer was not saved.` })
      } else {
        setAnswer('')
      }
    } catch {
      addToast({ title: 'Reply failed', body: 'Network error. Your answer was not saved.' })
    } finally {
      setSending(false)
    }
  }

  return (
    <div style={{ marginBottom: hasReply ? 4 : 12 }}>
      {/* Incoming bubble (session's message) */}
      <div
        onClick={() => navigate(`/sessions/${message.session_id}`)}
        style={{
          padding: '10px 14px',
          borderRadius: 8,
          background: isOpen ? 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))' : 'var(--scree)',
          border: `1px solid ${isOpen ? 'var(--blaze)' : 'var(--stone)'}`,
          cursor: 'pointer',
          marginBottom: hasReply ? 4 : 0,
        }}
      >
        {/* Header: session tag + kind badge */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6 }}>
          {!hideSessionLabel && (
            <span
              style={{
                color: 'var(--fog)',
                fontSize: '0.75rem',
                fontWeight: 600,
                overflow: 'hidden',
                textOverflow: 'ellipsis',
                whiteSpace: 'nowrap',
                maxWidth: 160,
              }}
            >
              {sessionLabel}
            </span>
          )}
          <span
            style={{
              color: kindColor,
              fontSize: '0.68rem',
              fontWeight: 600,
              letterSpacing: '0.05em',
              textTransform: 'uppercase',
              background: 'var(--basalt)',
              border: `1px solid ${kindColor}`,
              borderRadius: 4,
              padding: '1px 5px',
              flexShrink: 0,
            }}
          >
            {message.kind}
          </span>
          <TimeLabel ts={message.created_at} style={{ marginLeft: 'auto', color: 'var(--fog-dim)' }} />
          {isOpen && (
            <span style={{ color: 'var(--amber)', fontSize: '0.7rem', flexShrink: 0 }}>
              needs reply
            </span>
          )}
          {!isOpen && (
            <span style={{ color: 'var(--stone)', fontSize: '0.65rem', flexShrink: 0 }}>
              open ↗
            </span>
          )}
        </div>

        {/* Body */}
        <div
          style={{
            color: 'var(--chalk)',
            fontSize: '0.9rem',
            lineHeight: 1.4,
            marginBottom: isOpen ? 10 : 0,
          }}
        >
          <Markdown text={message.body} />
        </div>

        {/* Inline reply box — only for open ask/note */}
        {isOpen && (
          <div
            style={{ display: 'flex', gap: 8, marginTop: 8 }}
            onClick={e => e.stopPropagation()}
          >
            <input
              type="text"
              value={answer}
              onChange={e => setAnswer(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter') handleSend() }}
              placeholder="Type your reply…"
              style={{
                flex: 1,
                background: 'var(--basalt)',
                border: '1px solid var(--stone)',
                borderRadius: 4,
                padding: '6px 10px',
                color: 'var(--chalk)',
                fontSize: '0.9rem',
                outline: 'none',
                fontFamily: 'inherit',
              }}
            />
            <button
              onClick={e => { e.stopPropagation(); handleSend() }}
              disabled={sending || !answer.trim()}
              style={{
                background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))',
                color: 'var(--amber)',
                border: '1px solid var(--blaze)',
                borderRadius: 6,
                padding: '6px 14px',
                fontWeight: 600,
                cursor: sending ? 'default' : 'pointer',
                fontSize: '0.85rem',
                letterSpacing: '0.05em',
                fontFamily: 'inherit',
                opacity: sending ? 0.6 : 1,
              }}
            >
              Send
            </button>
          </div>
        )}
      </div>

      {/* Outgoing reply bubble — only when there's a real answer */}
      {hasReply && (
        <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 12 }}>
          <div
            data-testid="message-reply"
            style={{
              maxWidth: '80%',
              padding: '8px 12px',
              borderRadius: 8,
              background: 'color-mix(in srgb, var(--lichen) 22%, var(--basalt))',
              border: '1px solid color-mix(in srgb, var(--lichen) 35%, var(--scree))',
            }}
          >
            <div style={{ color: 'var(--fog-dim)', fontSize: '0.68rem', fontWeight: 600, marginBottom: 4, textTransform: 'uppercase', letterSpacing: '0.05em' }}>
              You
              {message.answered_at && <TimeLabel ts={message.answered_at} style={{ marginLeft: 8, textTransform: 'none', letterSpacing: 0, fontWeight: 400 }} />}
            </div>
            <div style={{ color: 'var(--chalk)', fontSize: '0.9rem', lineHeight: 1.4 }}>
              <Markdown text={message.answer ?? ''} />
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

// ─── Shared message thread ────────────────────────────────────────────────────

interface MessageThreadProps {
  messages: MessageInfo[]
  /** When true, each bubble omits the session-label line. Use in per-session
   *  views where every message belongs to the same session already on screen. */
  hideSessionLabel?: boolean
}

/**
 * Renders a sorted list of message bubbles — open pinned at top, then
 * newest-first within each group. Used by the per-session Chat tab in
 * SessionDetail.
 */
export function MessageThread({ messages, hideSessionLabel }: MessageThreadProps) {
  const sorted = [...messages].sort((a, b) => {
    const aOpen = a.status === 'open' ? 0 : 1
    const bOpen = b.status === 'open' ? 0 : 1
    if (aOpen !== bOpen) return aOpen - bOpen
    return b.created_at.localeCompare(a.created_at)
  })

  return (
    <div style={{ padding: '12px 16px' }}>
      {sorted.length === 0 ? (
        <div
          style={{
            color: 'var(--fog-dim)',
            fontSize: '0.9rem',
            textAlign: 'center',
            padding: '40px 0',
          }}
        >
          No messages yet
        </div>
      ) : (
        sorted.map(m => <MessageBubble key={m.id} message={m} hideSessionLabel={hideSessionLabel} />)
      )}
    </div>
  )
}
