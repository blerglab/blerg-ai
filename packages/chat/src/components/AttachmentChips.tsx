// The pieces of the chat's file attachments: the chips under the composer (upload state, remove),
// and a message's text with its attachment note drawn as file chips.
import type {ReactNode} from 'react'
import { FileIcon } from './FileIcon'
import { useState } from 'react'
import { useChat } from './context'
import { CAP_SHOW, countLines, firstLines, messageNeedsCap } from '../model/lineCap'
import CodeViewer from './CodeViewer'
import { formatSize } from '../model/artifacts'
import { previewKind } from '../model/artifactPreview'
import { splitAttachmentNote } from '../model/attachments'
import type { AttachmentItem } from '../hooks/useAttachments'
import { MediaPreview } from './ArtifactCard'
import ErrorId from './ErrorId'
import Markdown from './Markdown'
import type { CardRegistry } from '../cards/registry'

function uploadPercent(i: AttachmentItem): number {
  if (!i.size || i.loaded === undefined) return 0
  return Math.max(0, Math.min(100, Math.round((i.loaded / i.size) * 100)))
}

function uploadText(i: AttachmentItem): string {
  const pct = uploadPercent(i)
  // all bytes are sent but the server has not answered yet
  if (pct >= 100) return 'Processing…'
  return `Uploading ${pct}% · ${formatSize(i.loaded ?? 0)} of ${formatSize(i.size)}`
}

function waitText(i: AttachmentItem): string {
  return i.wait === 'offline' ? 'Waiting to reach the server' : 'Waiting for space'
}

/** Another tab's file: it uploads (and is sent) from there. */
function foreignText(i: AttachmentItem): string {
  return i.status === 'done' ? 'Attached in another tab' : i.status === 'error' ? 'Not attached' : 'Waiting in another tab'
}

/** The spoken summary of the waiting files: announced once when it changes, not per chip. */
function waitingAnnouncement(waiting: AttachmentItem[]): string {
  if (!waiting.length) return ''
  const n = waiting.length
  const files = n === 1 ? `${waiting[0]!.name} is` : `${n} files are`
  if (waiting.every(i => i.wait === 'offline')) return `${files} waiting to reach the server. They upload by themselves when it answers.`
  return `${files} waiting for space in this session. They upload by themselves when there is room.`
}

/** The chips under the message box. Waiting files share one set of actions (`waitingActions`),
 *  shown once below the chips, and one live announcement. */
export function ComposerChips({ items, onRemove, waitingActions }: { items: AttachmentItem[]; onRemove: (key: string) => void; waitingActions?: ReactNode }) {
  const waiting = items.filter(i => i.status === 'waiting' && !i.foreign)
  const announcement = waitingAnnouncement(waiting)
  // The live region comes with the first chip (it is uploading then), so the later change to
  // waiting is announced.
  if (items.length === 0) return null
  return (
    <>
    <ul className="attach-chips composer-attachments" aria-label="Attached files">
      {items.map(i => (
        <li key={i.key} className={`attach-chip composer-attachment ${i.status}${i.foreign ? ' is-foreign' : ''}${i.preview ? ' has-preview' : ''}`} data-testid="attach-chip">
          <div className={`attach-chip-preview${i.preview ? '' : ' is-file'}`}>
            {!i.preview ? <FileIcon name={i.name} className="attach-chip-filetype" />
              : i.preview.kind === 'text' ? <span className="attach-chip-excerpt">{i.preview.text}</span>
                : <img src={i.preview.src} alt={`${i.preview.kind === 'pdf' ? 'First page of' : 'Preview of'} ${i.name}`} />}
          </div>
          <FileIcon name={i.name} className="attach-chip-icon" />
          <span className="attach-chip-name" title={i.name}>{i.name}</span>
          <span className="attach-chip-meta">
            {i.foreign ? foreignText(i) : i.status === 'uploading' ? uploadText(i) : i.status === 'waiting' ? waitText(i) : i.status === 'error' ? <span title={i.error}>{i.error ? `Not attached: ${i.error}` : 'Not attached'}</span> : formatSize(i.size)}
          </span>
          {i.status === 'uploading' && !i.foreign && (
            <span
              className="attach-chip-progress"
              role="progressbar"
              aria-label={`Uploading ${i.name}`}
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={uploadPercent(i)}
            >
              <span className="attach-chip-progress-bar" style={{ width: `${uploadPercent(i)}%` }} />
            </span>
          )}
          {!i.foreign && <button
            type="button"
            className="attach-chip-remove"
            aria-label={`Remove ${i.name}`}
            onClick={() => onRemove(i.key)}
          >
            ✕
          </button>}
          {i.status === 'error' && i.error && <span className="attach-chip-error" role="alert">{i.error}<ErrorId id={i.errorId} /></span>}
        </li>
      ))}
    </ul>
    {waiting.length > 0 && waitingActions && <div className="attach-space-panel" data-testid="attach-space-panel">{waitingActions}</div>}
    <span className="chat-sr-only" role="status" aria-live="polite" data-testid="attach-waiting-status">{announcement}</span>
    </>
  )
}

/** A person's message as Markdown; one over the line cap (a pasted log, a file) shows its first
 *  lines with "Show all N lines" and "Open". Short messages are untouched. (A message that
 *  renders as the collapsed session brief never comes through here.) */
function CappedMarkdown({ text, cards }: { text: string; cards?: CardRegistry }) {
  const [expanded, setExpanded] = useState(false)
  const [open, setOpen] = useState(false)
  if (!messageNeedsCap(text)) return <Markdown text={text} cards={cards} />
  const collapsed = !expanded
  const total = countLines(text)
  return (
    <div className={`md-capped${collapsed ? ' is-capped' : ''}`} data-testid="message-capped">
      <Markdown text={collapsed ? firstLines(text, CAP_SHOW) : text} cards={cards} />
      <div className="md-codeblock-actions">
        <button type="button" className="md-codeblock-more" aria-expanded={!collapsed} onClick={() => setExpanded(!expanded)}>
          {collapsed ? `Show all ${total} lines` : 'Show less'}
        </button>
        <button type="button" className="md-codeblock-more" onClick={() => setOpen(true)}>Open</button>
      </div>
      {open && <CodeViewer text={text} lang={null} title="Message" onClose={() => setOpen(false)} />}
    </div>
  )
}

/** A message's text, with a trailing attachment note shown as chips instead of raw text, and an
 *  inline preview for each attached image, video or audio file the session's Files list knows.
 *  `at`: the message's time (for an older note that names files only by name). */
export function MessageText({ text, cards, at }: { text: string; cards?: CardRegistry; at?: string }) {
  const chat = useChat()
  const { body, files } = splitAttachmentNote(text)
  if (files.length === 0) return <CappedMarkdown text={text} cards={cards} />
  const previews = chat?.files
    // A note with IDs names its files exactly; an older name-only note gets the newest upload of
    // that name made at or before the message (`at`), never a later one.
    ? files.map(f => f.id ? chat.attachmentById?.(f.id) : chat.attachmentAt?.(f.name, at)).filter((a): a is NonNullable<typeof a> => !!a && previewKind(a.view, a.size) !== null)
    : []
  return (
    <>
      {body && <CappedMarkdown text={body} cards={cards} />}
      {previews.length > 0 && chat?.files && (
        <div className="attach-previews" data-testid="message-attachment-previews">
          {previews.map(a => (
            <MediaPreview key={a.id} sessionId={chat.sessionId} files={chat.files!} artifact={a} onOpen={autoplay => chat.view(a, { autoplay })} />
          ))}
        </div>
      )}
      <ul className="attach-chips in-message" data-testid="message-attachments" aria-label="Attached files">
        {files.map((f, n) => (
          <li key={n} className="attach-chip done">
            <FileIcon name={f.name} className="attach-chip-icon" />
            <span className="attach-chip-name" title={f.name}>{f.name}</span>
            <span className="attach-chip-meta">{f.size}</span>
          </li>
        ))}
      </ul>
    </>
  )
}
