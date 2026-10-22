// ConfirmPopover: a small confirmation anchored to the control that asked for it, for an action
// that cannot be undone (End session). One shared component replaces the old "press again to
// confirm" arming everywhere. It is an alertdialog named by its title and described by its text;
// focus starts on Cancel (the safe choice), Tab stays between its two buttons, the rest of the page
// is inert while it shows, and Esc or a press outside cancels and the focus goes back to the anchor.
// Under the anchor on a wide screen, kept inside the window; on a phone (640 px and less) a small
// bottom sheet with the same two buttons.
import { useEffect, useId, useLayoutEffect, useRef, useState, type RefObject } from 'react'
import { createPortal } from 'react-dom'
import './confirmPopover.css'

export interface ConfirmPopoverProps {
  /** The control that opened it: placed against it, and the focus returns to it on cancel. */
  anchor: RefObject<HTMLElement | null>
  title: string
  description: string
  /** The red button's words, e.g. "End session". */
  confirmLabel: string
  cancelLabel?: string
  onConfirm: () => void
  onCancel: () => void
  testId?: string
}

// The gap kept from the anchor and from the window's edges.
const GAP = 6
const EDGE = 8

export default function ConfirmPopover({ anchor, title, description, confirmLabel, cancelLabel = 'Cancel', onConfirm, onCancel, testId = 'confirm-popover' }: ConfirmPopoverProps) {
  const titleId = useId()
  const textId = useId()
  const box = useRef<HTMLDivElement | null>(null)
  const cancel = useRef<HTMLButtonElement | null>(null)
  const confirm = useRef<HTMLButtonElement | null>(null)
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null)
  // Set as it closes, so giving the focus back to the anchor is not taken back by the focus guard.
  const closing = useRef(false)

  // Placed under the anchor, its right edge on the anchor's, moved to stay inside the window, and
  // above the anchor when there is no room below. Measured before paint, again on resize and scroll.
  useLayoutEffect(() => {
    function place() {
      const a = anchor.current?.getBoundingClientRect()
      const el = box.current
      if (!a || !el) return
      const w = el.offsetWidth
      const h = el.offsetHeight
      const vw = document.documentElement.clientWidth || window.innerWidth
      const vh = window.innerHeight
      let left = a.right - w
      left = Math.max(EDGE, Math.min(left, vw - EDGE - w))
      let top = a.bottom + GAP
      if (top + h > vh - EDGE && a.top - GAP - h >= EDGE) top = a.top - GAP - h
      setPos(p => (p && p.top === top && p.left === left ? p : { top, left }))
    }
    place()
    window.addEventListener('resize', place)
    window.addEventListener('scroll', place, true)
    return () => {
      window.removeEventListener('resize', place)
      window.removeEventListener('scroll', place, true)
    }
  }, [anchor])

  // Modal: everything else on the page is inert (out of the accessibility tree and the tab order)
  // while it shows. Released before the focus goes back to the anchor, which an inert page could
  // not take.
  const scrim = useRef<HTMLDivElement | null>(null)
  const inerted = useRef<HTMLElement[]>([])
  useLayoutEffect(() => {
    const list = inerted.current
    for (const el of Array.from(document.body.children) as HTMLElement[]) {
      if (el === box.current || el === scrim.current || el.inert) continue
      el.inert = true
      list.push(el)
    }
    return () => releaseInert(list)
  }, [])
  const done = (then?: () => void) => {
    closing.current = true
    releaseInert(inerted.current)
    then?.()
  }

  // The focus starts on Cancel, once it is placed (a hidden button cannot take the focus).
  const focused = useRef(false)
  useEffect(() => {
    if (!pos || focused.current) return
    focused.current = true
    cancel.current?.focus()
  }, [pos])

  // Esc and a press outside cancel. Esc is caught on the way down, so a drawer or dialog around the
  // anchor does not close on it as well.
  const onCancelRef = useRef(onCancel)
  useEffect(() => { onCancelRef.current = onCancel })
  useEffect(() => {
    const back = () => {
      closing.current = true
      releaseInert(inerted.current)
      onCancelRef.current()
      anchor.current?.focus()
    }
    const key = (e: KeyboardEvent) => {
      if (e.key === 'Tab') {
        // Two buttons: Tab and Shift+Tab move between them and never leave.
        e.preventDefault()
        e.stopPropagation()
        const next = document.activeElement === cancel.current ? confirm.current : cancel.current
        next?.focus()
        return
      }
      if (e.key !== 'Escape') return
      e.stopPropagation()
      e.preventDefault()
      back()
    }
    // Modal while it shows: anything that takes the focus elsewhere (a composer's autofocus) gives
    // it back to Cancel.
    const keep = (e: FocusEvent) => {
      const t = e.target as Node | null
      if (!closing.current && t && box.current && !box.current.contains(t)) cancel.current?.focus()
    }
    const away = (e: PointerEvent) => {
      const t = e.target as Node
      // The scrim covers the page, the anchor included: any press outside the card cancels.
      if (box.current?.contains(t)) return
      back()
    }
    document.addEventListener('keydown', key, true)
    document.addEventListener('pointerdown', away, true)
    document.addEventListener('focusin', keep, true)
    return () => {
      document.removeEventListener('keydown', key, true)
      document.removeEventListener('pointerdown', away, true)
      document.removeEventListener('focusin', keep, true)
    }
  }, [anchor])

  return createPortal(
    <>
      <div ref={scrim} className="confirm-popover-scrim" aria-hidden="true" />
      <div
        ref={box}
        className="confirm-popover"
        role="alertdialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={textId}
        data-testid={testId}
        style={pos ? { top: pos.top, left: pos.left } : { visibility: 'hidden' }}
      >
        <h2 id={titleId} className="confirm-popover-title">{title}</h2>
        <p id={textId} className="confirm-popover-text">{description}</p>
        <div className="confirm-popover-actions">
          <button ref={cancel} type="button" className="confirm-popover-btn" onClick={() => done(() => { onCancel(); anchor.current?.focus() })}>{cancelLabel}</button>
          <button ref={confirm} type="button" className="confirm-popover-btn danger" data-testid={`${testId}-confirm`} onClick={() => done(onConfirm)}>{confirmLabel}</button>
        </div>
      </div>
    </>,
    document.body,
  )
}

/** Gives back what the popover made inert (emptying the list, so a second call does nothing). */
function releaseInert(list: HTMLElement[]) {
  for (const el of list.splice(0)) el.inert = false
}
