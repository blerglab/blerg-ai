// The message box: the text, the paperclip and the chips of what is attached, Send, and the
// keyboard: Enter sends, Shift+Enter breaks the line, Esc interrupts the running turn. The hidden
// file input belongs to ChatView (the Files panel's empty state opens it too); onAttach opens it.
import type { ReactNode } from 'react'
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
}

export default function Composer({ draft, onDraft, placeholder, locked, busy, onSubmit, onInterrupt, attachments, onAttach, actions }: ComposerProps) {
  const canSend = (draft.trim() !== '' || attachments.done.length > 0) && !attachments.uploading && !locked
  return (
    <>
      <div className="agent-composer">
        <textarea
          value={draft}
          placeholder={placeholder}
          disabled={locked}
          onChange={e => onDraft(e.target.value)}
          onPaste={e => {
            if (!onAttach) return
            const files = Array.from(e.clipboardData?.files ?? [])
            if (files.length === 0) return
            // A copied file brings no text; a page with an image brings both, and keeps its text.
            if (!e.clipboardData.getData('text/plain')) e.preventDefault()
            attachments.addFiles(files)
          }}
          onKeyDown={e => {
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
