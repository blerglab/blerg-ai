import { useLayoutEffect, useRef, type RefObject } from 'react'
import type { ShellWorkspaceHandle } from '../components/ShellWorkspace'
import { signedInAccount } from '../lib/liveBoardCard'

type Focus = { selector: string; index: number; shellId?: string }
/** In-memory per account/session: no DOM nodes or file names retained after leaving. */
export function useSessionFocus(root: RefObject<HTMLElement | null>, session: string | undefined, ready: boolean, mobile: boolean, shell: RefObject<ShellWorkspaceHandle | null>) {
  const remembered = useRef(new Map<string, Focus>())
  const account = signedInAccount()
  useLayoutEffect(() => {
    if (!session || !ready) return
    const node = root.current
    if (!node) return
    const key = JSON.stringify([account, session])
    const wanted = remembered.current.get(key) ?? { selector: '.agent-composer textarea', index: 0 }
    let restoring = true
    let restored = false
    let applying = false
    const selectors = ['.agent-composer textarea', '.shell-terminal .xterm-helper-textarea', '[role="tab"][aria-selected="true"]', '.session-file-target [tabindex], .session-file-target input, .session-file-target textarea', '.explorer-dock input']
    const remember = (event: FocusEvent) => {
      if (applying || !(event.target instanceof HTMLElement)) return
      // A control explicitly focused after mount wins over the initial restoration.
      if (restoring) { restored = true; restoring = false }
      for (const selector of selectors) {
        const targets = [...node.querySelectorAll(selector)]
        const index = targets.indexOf(event.target)
        if (index !== -1) { remembered.current.set(key, { selector, index, shellId: event.target.closest<HTMLElement>('[data-shell-id]')?.dataset.shellId }); break }
      }
    }
    // An arriving composer/shell may mount after the route. Retry only until its intended target
    // exists, and stop immediately when the person interacts, so late data cannot steal focus.
    const restore = () => {
      if (restored) return
      if (mobile) { (document.activeElement as HTMLElement | null)?.blur(); restored = true; return }
      // Reopening a session must never focus terminal input or reopen the phone keyboard.
      if (wanted.shellId) { restored = true; return }
      const target = node.querySelectorAll<HTMLElement>(wanted.selector)[wanted.index]
      if (!target || target.closest('[hidden], [inert]') || target.matches(':disabled') || !target.getClientRects().length) return
      const active = document.activeElement
      if (active instanceof HTMLElement && !node.contains(active) && active.matches('input, textarea, select, [role=separator], [contenteditable=true]')) { restored = true; return }
      applying = true
      target.focus({ preventScroll: true })
      applying = false
      restored = document.activeElement === target
    }
    const stop = () => { restored = true; restoring = false }
    const outsideFocus = (event: FocusEvent) => { if (!applying && event.target instanceof Node && !node.contains(event.target)) stop() }
    node.addEventListener('focusin', remember)
    document.addEventListener('focusin', outsideFocus)
    document.addEventListener('pointerdown', stop, true)
    document.addEventListener('keydown', stop, true)
    const observer = new MutationObserver(() => { restoring = true; restore(); restoring = false })
    observer.observe(node, { childList: true, subtree: true, attributes: true, attributeFilter: ['disabled', 'hidden', 'inert', 'data-active', 'data-shells-restored', 'class', 'style'] })
    const frame = requestAnimationFrame(() => { restore(); restoring = false })
    return () => {
      cancelAnimationFrame(frame); observer.disconnect()
      node.removeEventListener('focusin', remember)
      document.removeEventListener('focusin', outsideFocus)
      document.removeEventListener('pointerdown', stop, true)
      document.removeEventListener('keydown', stop, true)
    }
  }, [account, session, ready, mobile, root, shell])
}
