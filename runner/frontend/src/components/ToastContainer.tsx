import { useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { useToastStore, type Toast } from '../hooks/useToastStore'
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
      style={{
        background: 'var(--basalt)',
        border: isMessage ? '1px solid var(--scree-2)' : '1px solid var(--stone)',
        borderLeft: isMessage ? '3px solid var(--blaze)' : '3px solid var(--amber)',
        borderRadius: '6px',
        padding: '10px 14px',
        boxShadow: '0 6px 20px rgba(0,0,0,0.45)',
        cursor: clickable ? 'pointer' : 'default',
        maxWidth: '340px',
        pointerEvents: 'auto',
      }}
    >
      <div
        data-testid="toast-title"
        style={{
          color: isMessage ? 'var(--blaze)' : 'var(--chalk)',
          fontSize: '13px',
          fontWeight: 600,
          letterSpacing: '0.03em',
        }}
      >
        {toast.title}
      </div>
      <div style={{ color: 'var(--fog-dim)', fontSize: '12px', marginTop: '2px' }}>{toast.body}</div>
      {toast.action && (
        <button
          type="button"
          className="toast-action"
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
    <div
      style={{
        position: 'fixed',
        // Sit below the sticky header / +New button bar so toasts don't cover
        // (and block clicks on) the top controls.
        top: '64px',
        right: '12px',
        display: 'flex',
        flexDirection: 'column',
        gap: '8px',
        // Above the phone's rail drawer (1000): a toast's Undo must stay reachable there.
        zIndex: 1100,
        pointerEvents: 'none',
      }}
    >
      {toasts.map((t) => (
        <ToastItem key={t.id} toast={t} />
      ))}
    </div>
  )
}
