// Ctrl/Cmd+A inside a modal selects the modal's content, not the page behind it, the way a document
// viewer behaves. Only the topmost open modal answers (the last one mounted), the listener is on the
// document so it also works when the focus fell back to the page, and a text field keeps its own
// select-all.
import { useEffect, type RefObject } from 'react'

const open: Array<RefObject<HTMLElement | null>> = []

const typing = (t: EventTarget | null) =>
  t instanceof HTMLElement && (t.isContentEditable || t.matches('input, textarea, select'))

/** useSelectAllIn: while mounted, Ctrl/Cmd+A selects the children of `dialog`'s `.artifact-body`
 *  (or the dialog itself when it has none). */
export function useSelectAllIn(dialog: RefObject<HTMLElement | null>) {
  useEffect(() => {
    open.push(dialog)
    const onKey = (e: KeyboardEvent) => {
      if (open[open.length - 1] !== dialog) return
      if (e.key.toLowerCase() !== 'a' || !(e.ctrlKey || e.metaKey) || e.altKey || e.shiftKey) return
      if (typing(e.target)) return
      const root = dialog.current
      if (!root) return
      const body = root.querySelector('.artifact-body') ?? root
      const sel = window.getSelection()
      if (!sel) return
      e.preventDefault()
      sel.selectAllChildren(body)
    }
    document.addEventListener('keydown', onKey, true)
    return () => {
      document.removeEventListener('keydown', onKey, true)
      const i = open.lastIndexOf(dialog)
      if (i >= 0) open.splice(i, 1)
    }
  }, [dialog])
}
