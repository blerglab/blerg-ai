// Ctrl/Cmd+A in the chat pane (BLG-484): focus (or the last click) inside a message scopes the
// selection to that message — to its code block when the click landed in one — and anywhere else
// in the pane scopes it to the transcript, never the sidebar, panel or toolbars outside it. A text
// field keeps its own select-all, and a dialog mounted with useSelectAllIn (a file, diff, ticket
// or artifact viewer over the pane) answers in its place.
import { useEffect, useRef, type RefObject } from 'react'
import { anySelectAllDialogOpen, isSelectAllCombo, typing } from './useSelectAllIn'

/** The element Ctrl/Cmd+A should select the children of, for a click or focus landing on `point`
 *  inside the chat `root`: the code of a code block, else the whole message, else the transcript. */
function scopeOf(root: HTMLElement, point: Element): Element {
  const pre = point.closest('.md-codeblock')?.querySelector('pre')
  if (pre) return pre
  const card = point.closest('.agent-card')
  if (card) return card
  return root.querySelector('.agent-timeline') ?? root
}

export function useChatSelectAll(root: RefObject<HTMLElement | null>) {
  const lastPoint = useRef<Element | null>(null)
  useEffect(() => {
    const node = root.current
    if (!node) return
    const onPointerDown = (e: MouseEvent) => {
      lastPoint.current = e.target instanceof Element ? e.target : null
    }
    const onKey = (e: KeyboardEvent) => {
      if (anySelectAllDialogOpen()) return
      if (!isSelectAllCombo(e)) return
      if (typing(e.target)) return
      const chatRoot = root.current
      if (!chatRoot) return
      const active = document.activeElement
      const point = active instanceof Element && active !== document.body && chatRoot.contains(active)
        ? active
        : lastPoint.current
      if (!point) return
      const sel = window.getSelection()
      if (!sel) return
      e.preventDefault()
      sel.selectAllChildren(scopeOf(chatRoot, point))
    }
    node.addEventListener('mousedown', onPointerDown, true)
    document.addEventListener('keydown', onKey, true)
    return () => {
      node.removeEventListener('mousedown', onPointerDown, true)
      document.removeEventListener('keydown', onKey, true)
    }
  }, [root])
}
