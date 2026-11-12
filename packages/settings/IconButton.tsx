import { useState, type MouseEvent } from 'react'
import './IconButton.css'

/** Quiet refresh/retry affordance, shared by every host. Keep a label for a whole-view failure. */
export function IconButton({ label = 'Refresh', busy = false, disabled = false, labeled = false, preserveFocus = false, onClick }: {
  label?: string
  busy?: boolean
  disabled?: boolean
  labeled?: boolean
  /** Keep a busy control focusable when it anchors keyboard navigation. */
  preserveFocus?: boolean
  onClick?: (event: MouseEvent<HTMLButtonElement>) => unknown
}) {
  const [pending, setPending] = useState(false)
  const running = busy || pending
  return <button type="button" className="refresh-control" aria-label={label} title={label}
    aria-busy={running} disabled={disabled || (running && !preserveFocus)} aria-disabled={disabled || running} onClick={event => {
      if (disabled || running) return
      const result = onClick?.(event)
      if (result && typeof (result as PromiseLike<unknown>).then === 'function') {
        setPending(true)
        // The caller owns the error message; settling always releases the control.
        void Promise.resolve(result).then(() => setPending(false), () => setPending(false))
      }
    }}>
    <svg className={running ? 'refresh-control-spin' : undefined} viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.75" aria-hidden="true"><path d="M20 10a8 8 0 1 0-1 8M20 3v7h-7" /></svg>
    {labeled && <span>{label}</span>}
  </button>
}
