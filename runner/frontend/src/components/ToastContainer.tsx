import { useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { useToastStore, type Toast } from '../hooks/useToastStore'
import NoticeRegion from './NoticeRegion'
import { playToastSound, playMessageSound } from '../sound'

// How long a toast stays before auto-dismissing.
const TOAST_TTL_MS = 6000

function ToastItem({ toast }: { toast: Toast }) {
  const navigate = useNavigate()
  const removeToast = useToastStore((s) => s.removeToast)
  const isMessage = toast.variant === 'message'
  const hasAction = !!toast.action

  useEffect(() => {
    if (hasAction) {
      // A toast with an action (Undo) answers the person's own click: no sound.
    } else if (isMessage) {
      playMessageSound()
    } else {
      playToastSound()
    }
    const timer = setTimeout(() => removeToast(toast.id), toast.ttlMs ?? TOAST_TTL_MS)
    return () => clearTimeout(timer)
  }, [toast.id, removeToast, isMessage, hasAction, toast.ttlMs])

  const clickable = !!toast.url
  return (
    <div
      data-testid={isMessage ? 'toast-message' : 'toast-session'}
      role="status"
      onClick={() => {
        if (toast.url) navigate(toast.url)
        removeToast(toast.id)
      }}
      className="message-toast"
      style={{ cursor: clickable ? 'pointer' : 'default' }}
    >
      <button type="button" aria-label="Dismiss notification" onClick={event => { event.stopPropagation(); removeToast(toast.id) }} style={{ float: 'right', marginLeft: 8, background: 'transparent', border: 0, color: 'var(--chalk)', cursor: 'pointer' }}>×</button>
      <div
        data-testid="toast-title"
        className="confirm-popover-title"
      >
        {toast.title}
      </div>
      <div className="confirm-popover-text">{toast.body}</div>
      {toast.action && (
        <button
          type="button"
          className="toast-action confirm-popover-btn"
          data-testid="toast-action"
          onClick={event => {
            event.stopPropagation()
            toast.action!.onClick()
            removeToast(toast.id)
          }}
          style={{ marginTop: '6px', minHeight: '28px', padding: '2px 10px', fontSize: '12px' }}
        >
          {toast.action.label}
        </button>
      )}
    </div>
  )
}

// ToastContainer renders the stack of in-app notifications. Mounted once inside
// the Router so toasts can deep-link to their session on click.
export default function ToastContainer() {
  const toasts = useToastStore((s) => s.toasts)
  if (toasts.length === 0) return null

  return (
    <NoticeRegion>
      {toasts.map((t) => (
        <ToastItem key={t.id} toast={t} />
      ))}
    </NoticeRegion>
  )
}
