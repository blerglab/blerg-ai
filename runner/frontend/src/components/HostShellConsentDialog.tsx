import { useId, useLayoutEffect, useRef } from 'react'
import { createPortal } from 'react-dom'
import { useBackdropClose } from '@blerglab/chat'
import type { HostShellChallenge } from '../shellTransport'
import './CapabilitiesPanel.css'
import './PermissionsModal.css'
import './HostShellConsentDialog.css'

export default function HostShellConsentDialog({ challenge, busy, onCancel, onAllow }: {
  challenge: HostShellChallenge; busy: boolean; onCancel(): void; onAllow(): void
}) {
  const title = useId()
  const description = useId()
  const overlay = useRef<HTMLDivElement>(null)
  const cancel = useRef<HTMLButtonElement>(null)
  const backdrop = useBackdropClose(() => { if (!busy) onCancel() })
  useLayoutEffect(() => {
    const previous = document.activeElement as HTMLElement | null
    const inerted = Array.from(document.body.children).filter((el): el is HTMLElement => el instanceof HTMLElement && el !== overlay.current && !el.inert)
    inerted.forEach(el => { el.inert = true })
    cancel.current?.focus()
    // Something outside (a terminal taking focus as its tab is selected) must not leave the dialog
    // without keyboard focus: bring it back to the dialog's default choice.
    const keep = (e: FocusEvent) => {
      if (overlay.current && e.target instanceof Node && !overlay.current.contains(e.target)) cancel.current?.focus()
    }
    document.addEventListener('focusin', keep)
    const settle = window.setTimeout(() => { if (overlay.current && !overlay.current.contains(document.activeElement)) cancel.current?.focus() }, 0)
    return () => { document.removeEventListener('focusin', keep); window.clearTimeout(settle); inerted.forEach(el => { el.inert = false }); previous?.focus() }
  }, [])
  return createPortal(<div ref={overlay} className="caps-overlay shell-consent-overlay" {...backdrop}>
    <section className="caps-dialog shell-consent" role="dialog" aria-modal="true" aria-labelledby={title} aria-describedby={description} aria-busy={busy} onKeyDown={e => {
      e.stopPropagation()
      if (e.key === 'Escape') { e.preventDefault(); if (!busy) onCancel() }
      // The two choices sit side by side: arrows move between them; Enter activates the focused one.
      if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
        const buttons = Array.from(e.currentTarget.querySelectorAll<HTMLButtonElement>('.shell-consent-actions button:not(:disabled)'))
        if (buttons.length) {
          e.preventDefault()
          const at = buttons.indexOf(document.activeElement as HTMLButtonElement)
          const next = at < 0 ? 0 : (at + (e.key === 'ArrowRight' ? 1 : buttons.length - 1)) % buttons.length
          buttons[next].focus()
        }
      }
      if (e.key === 'Tab') {
        const targets = Array.from(e.currentTarget.querySelectorAll<HTMLElement>('[tabindex="0"], button:not(:disabled)'))
        e.preventDefault()
        const next = targets[(targets.indexOf(document.activeElement as HTMLElement) + (e.shiftKey ? targets.length - 1 : 1)) % targets.length]
        next?.focus()
      }
    }}>
      <header className="caps-header"><h2 id={title}>Allow a shell on this machine?</h2></header>
      <div className="shell-consent-body" role="region" aria-label="Host shell details" tabIndex={0}>
        <p className="shell-consent-brand">Blerg · Host shell</p>
        <p id={description}>This runs as the OS account below. Startup files and ordinary account permissions can expose credentials.</p>
        <dl><dt>Machine</dt><dd>{challenge.machine_id}</dd><dt>Account</dt><dd>{challenge.account} (UID {challenge.uid})</dd><dt>Shell</dt><dd>{challenge.shell}</dd><dt>Folder</dt><dd>{challenge.cwd}</dd></dl>
      </div>
      <footer className="pm-foot shell-consent-actions">
        <button ref={cancel} type="button" className="pm-button" disabled={busy} onClick={onCancel}>Cancel</button>
        <button type="button" className="pm-button pm-primary" disabled={busy} onClick={onAllow}>{busy ? 'Allowing…' : 'Allow this launch'}</button>
      </footer>
    </section>
  </div>, document.body)
}
