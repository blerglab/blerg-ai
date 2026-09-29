import { useRef } from 'react'
import type { PointerEvent, MouseEvent } from 'react'

// useBackdropClose: props for a modal's backdrop element that close it on a
// click on the backdrop itself — but only when the press STARTED there too.
//
// A bare `onClick={e => e.target === e.currentTarget && onClose()}` also fires
// for a press that began inside the dialog and was released over the backdrop
// (drag-selecting text in a field past the panel's edge, a slightly slipping
// tap): the browser dispatches that click to the nearest common ancestor,
// which is the backdrop. Dialog libraries (Radix, Reach, Headless UI) check
// where the pointer went DOWN for exactly this reason; so does this.
//
// Spread onto the backdrop: <div {...backdrop} className="overlay">.
export function useBackdropClose(onClose: () => void) {
  const pressedOnBackdrop = useRef(false)
  return {
    onPointerDown: (e: PointerEvent<HTMLElement>) => {
      pressedOnBackdrop.current = e.target === e.currentTarget
    },
    onClick: (e: MouseEvent<HTMLElement>) => {
      const started = pressedOnBackdrop.current
      pressedOnBackdrop.current = false
      if (started && e.target === e.currentTarget) onClose()
    },
  }
}
