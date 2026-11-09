// The message box: the text, the paperclip and the chips of what is attached, and Send, and the
// keyboard: Enter sends, Shift+Enter breaks the line. Stop is not here: it sits far from Send, in the
// host's session header (ChatReport.interrupt). The hidden file input belongs to ChatView (the Files
// panel's empty state opens it too); onAttach opens it.
import { insideOpenFence, markdownForPaste } from '../model/richClipboard'
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import type { useAttachments } from '../hooks/useAttachments'
import { ComposerChips } from './AttachmentChips'
import { mark } from '../releasePerfMarks'
import { localStorageChatStorage } from '../model/storage'

export interface ComposerProps {
  submitLabel?: string
  submitTitle?: string
  submitOn?: 'enter' | 'mod-enter'
  inputLabel?: string
  /** Account namespace for the device-local height; absent means no persistence. */
  heightScope?: string
  draft: string
  onDraft: (text: string) => void
  placeholder: string
  /** The session cannot take a message now (starting, ended). */
  locked: boolean
  onSubmit: () => void
  attachments: ReturnType<typeof useAttachments>
  /** Opens the file picker; absent when the transport takes no files (no paperclip, no paste). */
  onAttach?: () => void
  /** The host's own controls beside Send. */
  actions?: ReactNode
  waitingActions?: ReactNode
  /**
   * Put the cursor in the box when it appears and whenever the session unlocks, so a switch to a
   * session is one click, not two. Skipped on touch screens, where focus would raise the keyboard.
   * Default true.
   */
  autoFocus?: boolean
}

// A coarse pointer means a touch screen: focusing the box there pops the keyboard over the chat.
const touchScreen = () =>
  typeof window !== 'undefined' && typeof window.matchMedia === 'function' && window.matchMedia('(pointer: coarse)').matches

// Inserts at the caret the way typing does, so Ctrl+Z undoes it as one step; where the browser has
// no editing command for it, the value is set directly and the box told.
function insertText(box: HTMLTextAreaElement, text: string) {
  box.focus()
  let done = false
  try { done = typeof document.execCommand === 'function' && document.execCommand('insertText', false, text) } catch { /* below */ }
  if (done) return
  box.setRangeText(text, box.selectionStart, box.selectionEnd, 'end')
  box.dispatchEvent(new Event('input', { bubbles: true }))
}

let pastedImageSequence = 0
// The names a clipboard gives an image with no file behind it (a screenshot, a copied picture):
// "image.png" for every one of them, so two would collide. A copied file keeps its own name.
const GENERIC_PASTE_NAME = /^(?:image(?:\.[a-z0-9]{1,10})?|)$/i
function namePastedImage(file: File): File {
  if (!file.type.startsWith('image/') || !GENERIC_PASTE_NAME.test(file.name.trim())) return file
  const ext = file.name.match(/\.([a-z0-9]{1,10})$/i)?.[1]?.toLowerCase() || (file.type === 'image/jpeg' ? 'jpg' : 'png')
  const name = `screenshot-${Date.now()}-${++pastedImageSequence}.${ext}`
  return new File([file], name, { type: file.type, lastModified: file.lastModified })
}

const heightKey = (scope: string) => `blerg.chat.composer.height.${encodeURIComponent(scope)}`
function loadHeight(scope?: string): number | null {
  if (!scope) return null
  const stored = Number(localStorageChatStorage.get(heightKey(scope)))
  return Number.isFinite(stored) && stored >= 40 ? stored : null
}

export default function Composer({ submitLabel = 'Send', submitTitle = 'Send message', submitOn = 'enter', inputLabel, heightScope, draft, onDraft, placeholder, locked, onSubmit, attachments, onAttach, actions, waitingActions, autoFocus = true }: ComposerProps) {
  const canSend = (draft.trim() !== '' || attachments.done.length > 0) && !attachments.uploading && !locked
  const box = useRef<HTMLTextAreaElement>(null)
  const [manual, setManual] = useState(() => ({ scope: heightScope, height: loadHeight(heightScope) }))
  const manualHeight = manual.scope === heightScope ? manual.height : loadHeight(heightScope)
  const handle = useRef<HTMLDivElement>(null)
  const drag = useRef<{ y: number; height: number } | null>(null)
  const pane = () => box.current?.closest<HTMLElement>('.question-chat, .agent-chat') ?? box.current?.parentElement?.parentElement
  const dragLimit = () => Math.max(40, (pane()?.clientHeight ?? 0) * .85)
  useLayoutEffect(() => {
    const textarea = box.current
    const container = pane()
    if (!textarea || !container) return
    const fit = () => {
      const paneHeight = container.clientHeight
      const maximum = Math.max(40, paneHeight * .85)
      const automatic = Math.max(40, Math.min(420, paneHeight * .5))
      // Reset before measuring so deleting text can shrink the box too. scrollHeight excludes borders.
      textarea.style.height = '40px'
      const borders = textarea.offsetHeight - textarea.clientHeight
      const height = Math.min(maximum, manualHeight ?? Math.max(40, Math.min(automatic, textarea.value ? textarea.scrollHeight + borders : 40)))
      textarea.style.height = `${height}px`
      handle.current?.setAttribute('aria-valuenow', String(Math.round(height)))
      handle.current?.setAttribute('aria-valuemax', String(Math.round(maximum)))
    }
    fit()
    if (typeof ResizeObserver === 'undefined') return
    let width = textarea.getBoundingClientRect().width
    let height = container.clientHeight
    let frame = 0
    const observer = new ResizeObserver(() => {
      const nextWidth = textarea.getBoundingClientRect().width
      const nextHeight = container.clientHeight
      if (nextWidth === width && nextHeight === height) return
      width = nextWidth
      height = nextHeight
      cancelAnimationFrame(frame)
      frame = requestAnimationFrame(fit)
    })
    observer.observe(textarea)
    observer.observe(container)
    return () => { observer.disconnect(); cancelAnimationFrame(frame) }
  }, [draft, manualHeight])
  const resize = (height: number | null) => {
    const next = height === null ? null : Math.max(40, Math.min(dragLimit(), height))
    setManual({ scope: heightScope, height: next })
    if (heightScope) localStorageChatStorage.set(heightKey(heightScope), next === null ? null : String(next))
  }
  // BLG-453: a no-op everywhere except the release-perf harness — fires
  // once this keystroke's draft text has committed and the textarea has
  // re-rendered with it, the typing scenario's readiness signal.
  useEffect(() => { mark('key-committed') }, [draft])
  const plainPaste = useRef(false)
  useEffect(() => {
    if (!autoFocus || locked || touchScreen()) return
    // Never take the cursor away from a field the person is already typing in. A focused button
    // (the sidebar row they just clicked) is not one.
    const active = document.activeElement
    const typing = active instanceof HTMLInputElement || active instanceof HTMLTextAreaElement
      || (active instanceof HTMLElement && active.isContentEditable)
    if (typing && active !== box.current) return
    if (box.current) { box.current.dataset.autofocused = "true"; box.current.focus({ preventScroll: true }) }
  }, [autoFocus, locked])
  return (
    <>
      <div className="agent-composer">
        <div
          ref={handle}
          role="separator"
          aria-orientation="horizontal"
          aria-valuenow={40}
          aria-valuemin={40}
          tabIndex={0}
          className="agent-composer-resize"
          aria-label="Resize message box"
          title="Drag to resize · Arrow keys resize · Double-click or Home resets"
          onDoubleClick={() => resize(null)}
          onPointerDown={event => {
            if (event.button !== 0) return
            event.preventDefault()
            event.currentTarget.setPointerCapture(event.pointerId)
            drag.current = { y: event.clientY, height: box.current!.getBoundingClientRect().height }
          }}
          onPointerMove={event => {
            if (drag.current) resize(drag.current.height + drag.current.y - event.clientY)
          }}
          onPointerUp={event => {
            drag.current = null
            if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId)
          }}
          onPointerCancel={() => { drag.current = null }}
          onLostPointerCapture={() => { drag.current = null }}
          onKeyDown={event => {
            if (event.key === 'Home') { event.preventDefault(); resize(null) }
            if (event.key === 'ArrowUp' || event.key === 'ArrowDown') {
              event.preventDefault()
              resize((box.current!.getBoundingClientRect().height || parseFloat(box.current!.style.height)) + (event.key === 'ArrowUp' ? 24 : -24))
            }
          }}
        />
        <textarea
          ref={box}
          aria-label={inputLabel}
          onPointerDown={event => { delete event.currentTarget.dataset.autofocused }}
          onKeyDownCapture={event => { delete event.currentTarget.dataset.autofocused }}
          value={draft}
          placeholder={placeholder}
          disabled={locked}
          onChange={e => onDraft(e.target.value)}
          onPaste={e => {
            const plainOnly = plainPaste.current
            plainPaste.current = false
            const files = onAttach ? Array.from(e.clipboardData?.files ?? []) : []
            if (files.length > 0) {
              // A copied file brings no text; a page with an image brings both, and keeps its text.
              if (!e.clipboardData.getData('text/plain')) e.preventDefault()
              attachments.addFiles(files.map(namePastedImage))
            }
            if (plainOnly || e.defaultPrevented) return
            const html = e.clipboardData?.getData('text/html')
            if (!html) return
            // Inside an open code fence the text goes in as copied, like any editor: converting it
            // would fence it again inside the fence.
            const ta = e.currentTarget
            if (insideOpenFence(ta.value.slice(0, ta.selectionStart))) return
            const md = markdownForPaste(html, e.clipboardData.getData('text/plain'))
            if (md === null) return
            e.preventDefault()
            insertText(e.currentTarget, md)
          }}
          onBlur={event => { plainPaste.current = false; delete event.currentTarget.dataset.autofocused }}
          onKeyDown={e => {
            // Ctrl/Cmd+Shift+V is the browser's "paste as plain text": the paste that follows is left alone.
            plainPaste.current = (e.ctrlKey || e.metaKey) && e.shiftKey && (e.code === 'KeyV' || e.key.toLowerCase() === 'v')
            if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing && (submitOn === 'enter' || e.ctrlKey || e.metaKey)) {
              e.preventDefault()
              if (canSend) onSubmit()
            }
            // Esc never interrupts (owner, 2026-10-26): interrupting is rare and Stop sits in the
            // session header, far from Send; Esc stays free for dismissing menus, popovers and dialogs.
          }}
        />
        <div className="agent-composer-actions">
          {actions}
          {onAttach && (
            <button
              type="button"
              className="agent-attach"
              aria-label="Attach files"
              title="Attach files"
              disabled={locked}
              onClick={onAttach}
            >
              📎
            </button>
          )}
          <button type="button" className="agent-composer-send" title={submitTitle} onClick={onSubmit} disabled={!canSend}>{submitLabel}</button>
        </div>
      </div>
      <ComposerChips items={attachments.items} onRemove={attachments.remove} waitingActions={waitingActions} />
    </>
  )
}
