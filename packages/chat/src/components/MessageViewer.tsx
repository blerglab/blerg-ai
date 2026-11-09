// MessageViewer: a message popped out of the chat (BLG-449), drawn exactly as the transcript
// itself draws it — the same Markdown component, the same cards and workspace-file links, raw
// HTML left as literal text (never rendered: EventCards never passes `html` either). The text is
// a snapshot taken when the tab opened; it does not follow the live message.
import { ContentActions } from './ContentActions'
import { useId, useRef } from 'react'
import type { CardRegistry } from '../cards/registry'
import Markdown from './Markdown'
import ViewerToolbar, { ViewerAction } from './ViewerToolbar'

export default function MessageViewer({ text, label, onClose, embedded = false, cards, workspaceLinks = false, kept = true }: {
  text: string
  label: string
  onClose: () => void
  embedded?: boolean
  cards?: CardRegistry
  /** Whether a file path or link in the text may open the session's own copy (EventCards: on for
   *  an assistant reply, never for a person's own message). */
  workspaceLinks?: boolean
  /** False: the host's bounded snapshot store had no room for this one. It stays for this
   *  session; a reload will not bring it back. */
  kept?: boolean
}) {
  const titleId = useId()
  const body = useRef<HTMLDivElement>(null)
  return (
    <div className={embedded ? 'embedded-viewer' : 'artifact-overlay artifact-overlay-viewer'} data-testid="message-viewer">
      <div
        className="artifact-dialog artifact-viewer"
        role={embedded ? 'region' : 'dialog'}
        aria-modal={embedded ? undefined : true}
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={e => {
          if (!embedded && e.key === 'Escape') { e.stopPropagation(); onClose() }
        }}
      >
        {/* The tab already names this message; the toolbar repeats the title only for a modal
            standing alone (showPath, as ArtifactViewer's embedded file tabs already do). */}
        <ViewerToolbar print={false} titleId={titleId} title={label} meta="Message snapshot" copyLabel="Copy title" showPath={!embedded}>
          <ContentActions text={text} markdown name="message.md" content={() => body.current} />
          {!embedded && <ViewerAction icon="close" label="Close" onClick={onClose} />}
        </ViewerToolbar>
        {!kept && (
          <p className="artifact-status" data-testid="message-not-kept">This message was too large to keep — it won’t be here after a reload.</p>
        )}
        <div ref={body} className="artifact-body">
          <Markdown text={text} className="artifact-markdown md-doc md-box" cards={cards} workspaceLinks={workspaceLinks} />
        </div>
      </div>
    </div>
  )
}
