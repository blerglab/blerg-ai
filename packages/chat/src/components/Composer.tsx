// The message box: the text, the paperclip and the chips of what is attached, Send, and the
// keyboard: Enter sends, Shift+Enter breaks the line, Esc interrupts the running turn. The hidden
// file input belongs to ChatView (the Files panel's empty state opens it too); onAttach opens it.
import { insideOpenFence, markdownForPaste } from '../model/richClipboard'
import { useEffect, useRef, type ReactNode } from 'react'
import type { useAttachments } from '../hooks/useAttachments'
import { ComposerChips } from './AttachmentChips'

export interface ComposerProps {
  draft: string
  onDraft: (text: string) => void
  placeholder: string
  /** The session cannot take a message now (starting, ended). */
  locked: boolean
  /** A turn is running: Esc interrupts it. */
  busy: boolean
  onSubmit: () => void
  onInterrupt?: () => void
  attachments: ReturnType<typeof useAttachments>
  /** Opens the file picker; absent when the transport takes no files (no paperclip, no paste). */
  onAttach?: () => void
  /** The host's own controls beside Send. */
  actions?: ReactNode
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

export default function Composer({ draft, onDraft, placeholder, locked, busy, onSubmit, onInterrupt, attachments, onAttach, actions, autoFocus = true }: ComposerProps) {
  const canSend = (draft.trim() !== '' || attachments.done.length > 0) && !attachments.uploading && !locked
  const box = useRef<HTMLTextAreaElement>(null)
  const plainPaste = useRef(false)
  useEffect(() => {
    if (!autoFocus || locked || touchScreen()) return
    // Never take the cursor away from a field the person is already typing in. A focused button
    // (the sidebar row they just clicked) is not one.
    const active = document.activeElement
    const typing = active instanceof HTMLInputElement || active instanceof HTMLTextAreaElement
      || (active instanceof HTMLElement && active.isContentEditable)
    if (typing && active !== box.current) return
    box.current?.focus({ preventScroll: true })
  }, [autoFocus, locked])
  return (
    <>
      <div className="agent-composer">
        <textarea
          ref={box}
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
              attachments.addFiles(files)
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
          onBlur={() => { plainPaste.current = false }}
          onKeyDown={e => {
            // Ctrl/Cmd+Shift+V is the browser's "paste as plain text": the paste that follows is left alone.
            plainPaste.current = (e.ctrlKey || e.metaKey) && e.shiftKey && (e.code === 'KeyV' || e.key.toLowerCase() === 'v')
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault()
              onSubmit()
            } else if (e.key === 'Escape' && busy && onInterrupt) {
              // Like the terminal: Esc stops the turn that is running.
              e.preventDefault()
              onInterrupt()
            }
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
          <button onClick={onSubmit} disabled={!canSend}>Send</button>
        </div>
      </div>
      <ComposerChips items={attachments.items} onRemove={attachments.remove} />
    </>
  )
}
