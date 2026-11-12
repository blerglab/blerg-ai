import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'

export default function ShellRenameDialog({ name, onSave, onCancel }: { name: string; onSave(name: string): Promise<void>; onCancel(): void }) {
  const input = useRef<HTMLInputElement>(null)
  const [value, setValue] = useState(name)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  useEffect(() => { const previous = document.activeElement as HTMLElement | null; input.current?.focus(); input.current?.select(); return () => { if (previous?.isConnected) previous.focus() } }, [])
  return createPortal(<div className="shell-rename-backdrop" onClick={e => { if (e.target === e.currentTarget && !busy) onCancel() }}>
    <form className="shell-rename-dialog" role="dialog" aria-modal="true" aria-label="Rename shell" onKeyDown={e => {
      e.stopPropagation()
      if (e.key === 'Escape' && !busy) { e.preventDefault(); onCancel() }
      if (e.key === 'Tab') {
        const items = [...e.currentTarget.querySelectorAll<HTMLElement>('input, button:not(:disabled)')]
        const first = items[0], last = items.at(-1)
        if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last?.focus() }
        if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first?.focus() }
      }
    }} onSubmit={e => { e.preventDefault(); if (busy) return; setBusy(true); void onSave(value.trim()).catch(() => { setError('Shell could not be renamed. Try again.'); setBusy(false) }) }}>
      <label>Shell name<input ref={input} value={value} maxLength={120} disabled={busy} onChange={e => setValue(e.target.value)} /></label>
      <p>Leave empty to use the default name.</p>
      {error && <p role="alert">{error}</p>}
      <div><button type="button" className="btn" disabled={busy} onClick={onCancel}>Cancel</button><button className="btn primary" disabled={busy}>Save</button></div>
    </form>
  </div>, document.body)
}
