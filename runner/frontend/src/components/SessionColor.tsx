import { useId, useLayoutEffect, useRef, useState } from 'react'
import { usePaletteCommands } from '../commands/context'
import { apiFetch } from '../apiFetch'
import { SESSION_COLORS, sessionColor } from '../lib/sessionColors'

export function SessionColorPicker({ sessionId, color, commandsOnly = false }: {sessionId: string; color?: string; commandsOnly?: boolean}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [open, setOpen] = useState(false)
  const trigger = useRef<HTMLButtonElement>(null)
  const box = useRef<HTMLDivElement>(null)
  const current = sessionColor(color)
  const paletteId = useId()
  useLayoutEffect(() => {
    if (!open) return
    const place = () => {
      const anchor = trigger.current!.getBoundingClientRect()
      const panel = box.current!
      panel.style.left = `${Math.max(8, Math.min(anchor.left, innerWidth - panel.offsetWidth - 8))}px`
      panel.style.top = `${Math.max(8, Math.min(anchor.bottom + 6, innerHeight - panel.offsetHeight - 8))}px`
    }
    place()
    const resize = new ResizeObserver(place)
    if (box.current) resize.observe(box.current)
    box.current?.querySelector<HTMLButtonElement>('[aria-checked="true"]')?.focus()
    const away = (event: PointerEvent) => {
      if (box.current?.contains(event.target as Node) || trigger.current?.contains(event.target as Node)) return
      setOpen(false)
      trigger.current?.focus()
    }
    const escape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      event.preventDefault(); event.stopPropagation()
      setOpen(false); trigger.current?.focus()
    }
    document.addEventListener('pointerdown', away)
    document.addEventListener('keydown', escape, true)
    window.addEventListener('resize', place)
    window.addEventListener('scroll', place, true)
    return () => {
      resize.disconnect()
      document.removeEventListener('pointerdown', away)
      document.removeEventListener('keydown', escape, true)
      window.removeEventListener('resize', place)
      window.removeEventListener('scroll', place, true)
    }
  }, [open])
  async function save(value: string) {
    if (busy) return
    setBusy(true); setError('')
    try {
      const reply = await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({color:value}) })
      if (!reply.ok) throw new Error('refused')
    } catch { setError('Could not set color. Try again.') }
    finally { setBusy(false) }
  }
  usePaletteCommands(commandsOnly ? [
    { id: `color-menu:${sessionId}`, title: 'Set color…', refine: 'Set color: ', here: 4, destructive: false, run: () => {} },
    ...SESSION_COLORS.map(item => ({ id: `color:${sessionId}:${item.key}`, title: `Set color: ${item.name}`, destructive: false, run: () => save(item.key) })),
  ] : [])
  const paint = (key: string) => ({color: key ? `var(--session-color-${key})` : 'var(--chalk)'})
  return <>{!commandsOnly && <div className="session-color-picker" onClick={event => event.stopPropagation()} onKeyDown={event => { if (event.key !== 'Tab') event.stopPropagation() }} onBlur={event => {
    if (!event.currentTarget.contains(event.relatedTarget)) setOpen(false)
  }}>
    <button ref={trigger} type="button" className="session-color-swatch" aria-label={`Session color: ${current.name}`} title={`Session color: ${current.name}`} aria-expanded={open} aria-haspopup="dialog" aria-controls={open ? paletteId : undefined} style={paint(current.key)} onClick={() => setOpen(!open)}>
      <span className={current.key ? 'session-color-disc' : 'session-color-none'} aria-hidden="true" />
    </button>
    {open && <div ref={box} id={paletteId} className="confirm-popover confirm-popover-anchored session-color-popover" role="dialog" aria-label="Choose session color">
      <div className="session-color-grid" role="radiogroup" aria-label="Session color" aria-busy={busy}>
        {SESSION_COLORS.map((item, index) => <button type="button" key={item.key} className="session-color-swatch" role="radio"
          aria-label={item.name} title={item.name} aria-checked={current.key === item.key}
          tabIndex={current.key === item.key ? 0 : -1} aria-disabled={busy} style={paint(item.key)}
          onClick={() => void save(item.key)} onKeyDown={event => {
            if (!['ArrowRight','ArrowLeft','ArrowUp','ArrowDown','Home','End'].includes(event.key)) return
            event.preventDefault(); event.stopPropagation()
            if (busy) return
            const next = event.key === 'Home' ? 0 : event.key === 'End' ? SESSION_COLORS.length - 1 : (index + (event.key === 'ArrowRight' || event.key === 'ArrowDown' ? 1 : -1) + SESSION_COLORS.length) % SESSION_COLORS.length
            event.currentTarget.parentElement?.querySelectorAll<HTMLButtonElement>('[role="radio"]')[next]?.focus()
            void save(SESSION_COLORS[next].key)
          }}><span className={item.key ? 'session-color-disc' : 'session-color-none'} aria-hidden="true" /></button>)}
      </div>
      {error && <span role="alert" className="session-color-error">{error}</span>}
    </div>}
  </div>}{commandsOnly && error && <span role="alert" className="session-color-error">{error}</span>}</>
}
