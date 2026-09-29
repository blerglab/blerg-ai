import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useMessageStore, selectUnreadCount } from '../hooks/useMessageStore'

/**
 * ChatBubble — a small persistent affordance shown in the top bar.
 * Displays the count of unread messages as a teal badge.
 * When a new message arrives (count increases), plays a bounce + glow
 * animation for 1.5 s to draw the eye. Respects prefers-reduced-motion.
 * Clicking navigates to the Chat roll-up route ('/').
 */
export default function ChatBubble() {
  const navigate = useNavigate()
  const unreadCount = useMessageStore(selectUnreadCount)
  const prevCount = useRef(unreadCount)
  const [bouncing, setBouncing] = useState(false)

  useEffect(() => {
    if (unreadCount > prevCount.current) {
      setBouncing(true)
      const t = setTimeout(() => setBouncing(false), 1500)
      prevCount.current = unreadCount
      return () => clearTimeout(t)
    }
    prevCount.current = unreadCount
  }, [unreadCount])

  return (
    <button
      data-testid="chat-bubble"
      onClick={() => navigate('/')}
      className={bouncing ? 'blerg-chat-bounce' : ''}
      aria-label={unreadCount > 0 ? `${unreadCount} unread messages` : 'Chat'}
      style={{
        position: 'relative',
        background: 'none',
        border: 'none',
        padding: '4px',
        cursor: 'pointer',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        borderRadius: '50%',
        width: '28px',
        height: '28px',
        color: unreadCount > 0 ? 'var(--blaze)' : 'var(--fog-dim)',
        fontSize: '16px',
        lineHeight: '1',
        flexShrink: 0,
      }}
    >
      💬
      {unreadCount > 0 && (
        <span
          data-testid="chat-bubble-badge"
          style={{
            position: 'absolute',
            top: '-3px',
            right: '-3px',
            background: 'var(--blaze)',
            color: 'var(--basalt)',
            borderRadius: '50%',
            minWidth: '16px',
            height: '16px',
            fontSize: '10px',
            fontWeight: 700,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            lineHeight: '1',
            padding: '0 2px',
            boxSizing: 'border-box',
          }}
        >
          {unreadCount}
        </span>
      )}
    </button>
  )
}
